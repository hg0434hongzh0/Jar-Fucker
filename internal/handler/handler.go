package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/cfr"
	"github.com/hg0434hongzh0/Jar-Fucker/internal/jar"
	"github.com/hg0434hongzh0/Jar-Fucker/internal/vscodeaudit"
)

type Handler struct {
	mu             sync.RWMutex
	javaPath       string
	cfrPath        string
	configPath     string
	sessionToken   string
	taskMu         sync.Mutex
	decompileTasks sync.Map
	uploadDirs     sync.Map
}

type Options struct {
	SessionToken string
	ConfigPath   string
}

const (
	maxJSONBody                = 1 << 20
	maxUploadBody              = 2 << 30
	maxUploadedFiles           = 4096
	maxViewerFileBytes         = 5 << 20
	uploadDirPrefix            = "jar-fucker-upload-"
	staleUploadMaxAge          = 24 * time.Hour
	decompileHeartbeatInterval = 2 * time.Second
)

type appConfig struct {
	JavaPath string `json:"javaPath"`
	CfrPath  string `json:"cfrPath"`
}

func NewSessionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成启动令牌失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func New(webFS fs.FS, option ...Options) http.Handler {
	opts := Options{}
	if len(option) > 0 {
		opts = option[0]
	}
	if opts.SessionToken == "" {
		opts.SessionToken, _ = NewSessionToken()
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath = defaultConfigPath()
	}

	h := &Handler{
		configPath:   opts.ConfigPath,
		sessionToken: opts.SessionToken,
	}
	h.loadConfig()
	h.cleanupStaleUploads()

	mux := http.NewServeMux()

	mux.Handle("GET /", http.FileServerFS(webFS))

	mux.HandleFunc("POST /api/scan", h.handleScan)
	mux.HandleFunc("POST /api/analyze", h.handleAnalyze)
	mux.HandleFunc("POST /api/extract", h.handleExtract)
	mux.HandleFunc("POST /api/decompile", h.handleDecompile)
	mux.HandleFunc("POST /api/decompile/stream", h.handleDecompileStream)
	mux.HandleFunc("POST /api/decompile/task", h.handleStartDecompileTask)
	mux.HandleFunc("GET /api/decompile/task", h.handleGetDecompileTask)
	mux.HandleFunc("DELETE /api/decompile/task", h.handleCancelDecompileTask)
	mux.HandleFunc("POST /api/upload", h.handleUpload)
	mux.HandleFunc("DELETE /api/upload", h.handleDeleteUpload)
	mux.HandleFunc("GET /api/tree", h.handleTree)
	mux.HandleFunc("GET /api/file", h.handleFile)
	mux.HandleFunc("POST /api/search", h.handleSearch)
	mux.HandleFunc("GET /api/browse", h.handleBrowse)
	mux.HandleFunc("GET /api/config", h.handleGetConfig)
	mux.HandleFunc("PUT /api/config", h.handleSetConfig)
	mux.HandleFunc("GET /api/session", h.handleSession)
	mux.HandleFunc("POST /api/vscode-workspace", h.handleVSCodeWorkspace)

	return h.secure(mux)
}

func (h *Handler) handleSession(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleVSCodeWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OutputDir string   `json:"outputDir"`
		Jars      []string `json:"jars"`
		SourceDir string   `json:"sourceDir"`
		Open      bool     `json:"open"`
	}
	if !h.decodeJSON(w, r, &req) {
		return
	}
	result, err := vscodeaudit.Create(vscodeaudit.Options{
		OutputDir: req.OutputDir,
		Jars:      req.Jars,
		SourceDir: req.SourceDir,
	})
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Open {
		if err := vscodeaudit.Open(result.WorkspaceFile); err != nil {
			result.OpenError = err.Error()
		} else {
			result.Opened = true
		}
	}
	h.writeJSON(w, result)
}

func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ".jar-fucker.json"
	}
	return filepath.Join(dir, "Jar-Fucker", "config.json")
}

func (h *Handler) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}

		if !isLoopbackHost(r.Host) || !sameOriginRequest(r) {
			h.writeError(w, http.StatusForbidden, "拒绝非本机来源的请求")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && !h.validSessionToken(r.Header.Get("X-Jar-Fucker-Token")) {
			h.writeError(w, http.StatusUnauthorized, "启动令牌无效，请从应用启动页重新打开")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) validSessionToken(token string) bool {
	if token == "" || h.sessionToken == "" || len(token) != len(h.sessionToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(h.sessionToken)) == 1
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func sameOriginRequest(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host) && isLoopbackHost(u.Host)
}

func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		h.writeError(w, http.StatusBadRequest, "无效请求: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		h.writeError(w, http.StatusBadRequest, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}

func (h *Handler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeError(w http.ResponseWriter, code int, msg string) {
	h.writeErrorWithLog(w, code, msg, "")
}

func (h *Handler) writeErrorWithLog(w http.ResponseWriter, code int, msg, log string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	resp := map[string]string{"error": msg}
	if strings.TrimSpace(log) != "" {
		resp["log"] = log
	}
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) loadConfig() {
	paths := []string{h.configPath}
	if filepath.Clean(h.configPath) != filepath.Clean(".jar-fucker.json") {
		paths = append(paths, ".jar-fucker.json")
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg appConfig
		if json.Unmarshal(data, &cfg) != nil {
			continue
		}
		h.javaPath = strings.TrimSpace(cfg.JavaPath)
		h.cfrPath = strings.TrimSpace(cfg.CfrPath)
		return
	}
}

func writeConfigFile(path string, cfg appConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (h *Handler) getToolConfig() (string, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.javaPath, h.cfrPath
}

// POST /api/scan - 扫描目录内所有 JAR 文件
func (h *Handler) handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir string `json:"dir"`
	}
	if !h.decodeJSON(w, r, &req) {
		return
	}
	if req.Dir == "" {
		h.writeError(w, 400, "请提供目录路径")
		return
	}

	jars, err := jar.ScanDirContext(r.Context(), req.Dir)
	if err != nil {
		h.writeError(w, 400, err.Error())
		return
	}

	h.writeJSON(w, map[string]any{
		"dir":   req.Dir,
		"jars":  jars,
		"total": len(jars),
	})
}

// POST /api/extract - 批量提取多个 JAR 到输出目录
func (h *Handler) handleExtract(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Jars      []jar.JarFile `json:"jars"`
		OutputDir string        `json:"outputDir"`
	}
	if !h.decodeJSON(w, r, &req) {
		return
	}
	if len(req.Jars) == 0 {
		h.writeError(w, 400, "没有需要提取的 JAR 文件")
		return
	}
	if req.OutputDir == "" {
		h.writeError(w, 400, "请提供输出目录")
		return
	}

	total, err := jar.ExtractAllContext(r.Context(), req.Jars, req.OutputDir)
	if err != nil {
		h.writeError(w, 500, err.Error())
		return
	}

	absOut, _ := filepath.Abs(req.OutputDir)
	h.writeJSON(w, map[string]any{
		"outputDir":  absOut,
		"jarCount":   len(req.Jars),
		"totalFiles": total,
	})
}

// POST /api/decompile - 批量反编译多个 JAR
func (h *Handler) handleDecompile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Jars      []string `json:"jars"`
		OutputDir string   `json:"outputDir"`
		FilterPkg string   `json:"filterPkg"`
	}
	if !h.decodeJSON(w, r, &req) {
		return
	}

	if len(req.Jars) == 0 {
		h.writeError(w, 400, "请提供 JAR 文件列表")
		return
	}
	if req.OutputDir == "" {
		h.writeError(w, 400, "请提供输出目录")
		return
	}
	if !h.taskMu.TryLock() {
		h.writeError(w, http.StatusConflict, "已有反编译任务正在运行")
		return
	}
	defer h.taskMu.Unlock()

	if len(req.Jars) == 1 {
		javaPath, cfrPath := h.getToolConfig()
		result, err := cfr.DecompileContext(r.Context(), req.Jars[0], req.OutputDir, javaPath, cfrPath, req.FilterPkg)
		if err != nil {
			h.writeErrorWithLog(w, 500, err.Error(), cfr.LogFromError(err))
			return
		}
		h.writeJSON(w, result)
		return
	}

	javaPath, cfrPath := h.getToolConfig()
	result, err := cfr.DecompileMultipleContext(r.Context(), req.Jars, req.OutputDir, javaPath, cfrPath, req.FilterPkg)
	if err != nil {
		h.writeErrorWithLog(w, 500, err.Error(), cfr.LogFromError(err))
		return
	}
	h.writeJSON(w, result)
}

type decompileRequest struct {
	Jars      []string `json:"jars"`
	OutputDir string   `json:"outputDir"`
	FilterPkg string   `json:"filterPkg"`
}

type backgroundDecompileTask struct {
	mu        sync.RWMutex
	id        string
	status    string
	event     decompileEvent
	hasEvent  bool
	result    *cfr.Result
	error     string
	warnings  []string
	cancel    context.CancelFunc
	createdAt time.Time
	updatedAt time.Time
}

type decompileTaskSnapshot struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	Event     *decompileEvent `json:"event,omitempty"`
	Result    *cfr.Result     `json:"result,omitempty"`
	Error     string          `json:"error,omitempty"`
	Warnings  []string        `json:"warnings,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func (task *backgroundDecompileTask) snapshot() decompileTaskSnapshot {
	task.mu.RLock()
	defer task.mu.RUnlock()
	var event *decompileEvent
	if task.hasEvent {
		copy := task.event
		event = &copy
	}
	return decompileTaskSnapshot{
		ID:        task.id,
		Status:    task.status,
		Event:     event,
		Result:    task.result,
		Error:     task.error,
		Warnings:  append([]string(nil), task.warnings...),
		CreatedAt: task.createdAt,
		UpdatedAt: task.updatedAt,
	}
}

func (task *backgroundDecompileTask) send(event decompileEvent) bool {
	task.mu.Lock()
	defer task.mu.Unlock()
	task.event = event
	task.hasEvent = true
	task.updatedAt = time.Now()
	switch event.Type {
	case "warning":
		warning := event.Message
		if event.Detail != "" {
			warning += ": " + event.Detail
		}
		task.warnings = append(task.warnings, warning)
	case "error":
		task.status = "error"
		task.error = event.Message
	case "done":
		task.status = "done"
		task.result = event.Result
	}
	return true
}

type decompileEvent struct {
	Type      string      `json:"type"`
	Message   string      `json:"message,omitempty"`
	Detail    string      `json:"detail,omitempty"`
	Current   int         `json:"current,omitempty"`
	Total     int         `json:"total,omitempty"`
	Percent   int         `json:"percent,omitempty"`
	Jar       string      `json:"jar,omitempty"`
	Result    *cfr.Result `json:"result,omitempty"`
	JavaFiles int         `json:"javaFiles,omitempty"`
	Elapsed   string      `json:"elapsed,omitempty"`
	Log       string      `json:"log,omitempty"`
}

type decompileOutcome struct {
	result *cfr.Result
	err    error
}

func offerDecompileProgress(ch chan cfr.Progress, update cfr.Progress) {
	select {
	case ch <- update:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- update:
	default:
	}
}

func mapDecompileProgress(update cfr.Progress, jarName string, current, total, jarStart, jarEnd int) decompileEvent {
	localPercent := 0
	eventType := "phase"
	message := fmt.Sprintf("正在准备 %s", jarName)
	detail := fmt.Sprintf("%d/%d", current, total)

	switch update.Phase {
	case cfr.ProgressExtracting:
		message = fmt.Sprintf("正在展开 %s", jarName)
	case cfr.ProgressDecompiling:
		localPercent = 10
		message = fmt.Sprintf("Fernflower 正在反编译 %s", jarName)
		if update.CompletedUnits > 0 {
			eventType = "progress"
			completed := update.CompletedUnits
			if update.ClassFiles > 0 {
				completed = min(completed, update.ClassFiles)
				localPercent += completed * 80 / update.ClassFiles
				detail = fmt.Sprintf("已完成 %d 个编译单元 · 输入 %d 个 class 文件", update.CompletedUnits, update.ClassFiles)
			} else {
				detail = fmt.Sprintf("已完成 %d 个编译单元", update.CompletedUnits)
			}
			if update.Detail != "" {
				detail += " · " + update.Detail
			}
		}
	case cfr.ProgressFinalizing:
		localPercent = 95
		message = fmt.Sprintf("正在统计 %s 的输出", jarName)
	}

	localPercent = min(localPercent, 95)
	percent := jarStart + max(0, jarEnd-jarStart)*localPercent/100
	return decompileEvent{
		Type:    eventType,
		Message: message,
		Detail:  detail,
		Current: current,
		Total:   total,
		Percent: percent,
		Jar:     jarName,
	}
}

func decompileHeartbeat(update cfr.Progress, jarName string, current, total, percent int, elapsed time.Duration) decompileEvent {
	detail := fmt.Sprintf("%d/%d · 已运行 %s", current, total, elapsed.Round(time.Second))
	if update.CompletedUnits > 0 {
		if update.ClassFiles > 0 {
			detail = fmt.Sprintf("已完成 %d 个编译单元 · 输入 %d 个 class 文件 · 已运行 %s", update.CompletedUnits, update.ClassFiles, elapsed.Round(time.Second))
		} else {
			detail = fmt.Sprintf("已完成 %d 个编译单元 · 已运行 %s", update.CompletedUnits, elapsed.Round(time.Second))
		}
	}
	return decompileEvent{
		Type:    "heartbeat",
		Message: fmt.Sprintf("%s 仍在处理中", jarName),
		Detail:  detail,
		Current: current,
		Total:   total,
		Percent: percent,
		Jar:     jarName,
		Elapsed: elapsed.Round(time.Second).String(),
	}
}

// POST /api/decompile/stream - 流式返回批量反编译进度
func validateDecompileRequest(req decompileRequest) error {
	if len(req.Jars) == 0 {
		return errors.New("请提供 JAR 文件列表")
	}
	if req.OutputDir == "" {
		return errors.New("请提供输出目录")
	}
	return nil
}

func (h *Handler) handleDecompileStream(w http.ResponseWriter, r *http.Request) {
	var req decompileRequest
	if !h.decodeJSON(w, r, &req) {
		return
	}
	if err := validateDecompileRequest(req); err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.taskMu.TryLock() {
		h.writeError(w, http.StatusConflict, "已有反编译任务正在运行")
		return
	}
	defer h.taskMu.Unlock()

	if _, ok := w.(http.Flusher); !ok {
		h.writeError(w, http.StatusInternalServerError, "当前服务器不支持进度流")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	enc := json.NewEncoder(w)
	responseController := http.NewResponseController(w)
	send := func(event decompileEvent) bool {
		if err := enc.Encode(event); err != nil {
			cancel()
			return false
		}
		if err := responseController.Flush(); err != nil {
			cancel()
			return false
		}
		return true
	}

	h.runDecompile(ctx, req, send)
}

func (h *Handler) runDecompile(parent context.Context, req decompileRequest, send func(decompileEvent) bool) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	absOut, err := filepath.Abs(req.OutputDir)
	if err != nil {
		_ = send(decompileEvent{Type: "error", Message: "输出目录无效: " + err.Error()})
		return
	}
	start := time.Now()
	total := len(req.Jars)
	totalJava := 0
	failedJars := 0

	if !send(decompileEvent{Type: "start", Message: "正在检查运行环境", Total: total, Percent: 0}) {
		return
	}
	javaPath, cfrPath := h.getToolConfig()
	if javaPath == "" {
		if !send(decompileEvent{Type: "phase", Message: "正在检测 Java 21+", Total: total, Percent: 2}) {
			return
		}
		javaPath, err = cfr.FindJava()
		if err != nil {
			_ = send(decompileEvent{Type: "error", Message: err.Error(), Percent: 2})
			return
		}
	}
	if cfrPath == "" {
		cfrPath = cfr.DefaultCFRPath()
	}
	if !send(decompileEvent{Type: "phase", Message: "正在准备 Fernflower", Detail: filepath.Base(cfrPath), Total: total, Percent: 5}) {
		return
	}
	if err := cfr.EnsureDecompilerContext(ctx, cfrPath, javaPath); err != nil {
		_ = send(decompileEvent{Type: "error", Message: err.Error(), Percent: 5, Log: cfr.LogFromError(err)})
		return
	}

	usedOutputNames := make(map[string]int)
	for i, jarPath := range req.Jars {
		jarName := filepath.Base(jarPath)
		jarStartPercent := 8 + i*90/total
		jarEndPercent := 8 + (i+1)*90/total

		outDir := absOut
		if total > 1 {
			baseName := strings.TrimSuffix(jarName, filepath.Ext(jarName))
			key := strings.ToLower(baseName)
			usedOutputNames[key]++
			if usedOutputNames[key] > 1 {
				baseName = fmt.Sprintf("%s-%d", baseName, usedOutputNames[key])
			}
			outDir = filepath.Join(absOut, baseName)
		}

		progressCh := make(chan cfr.Progress, 1)
		doneCh := make(chan decompileOutcome, 1)
		jarStarted := time.Now()
		go func() {
			result, err := cfr.DecompileContextWithProgress(ctx, jarPath, outDir, javaPath, cfrPath, req.FilterPkg, func(update cfr.Progress) {
				offerDecompileProgress(progressCh, update)
			})
			doneCh <- decompileOutcome{result: result, err: err}
		}()

		ticker := time.NewTicker(decompileHeartbeatInterval)
		latestUpdate := cfr.Progress{Phase: cfr.ProgressExtracting}
		currentPercent := jarStartPercent
		var outcome decompileOutcome
		finished := false
		for !finished {
			select {
			case update := <-progressCh:
				latestUpdate = update
				event := mapDecompileProgress(update, jarName, i+1, total, jarStartPercent, jarEndPercent)
				currentPercent = max(currentPercent, event.Percent)
				event.Percent = currentPercent
				if !send(event) {
					ticker.Stop()
					cancel()
					<-doneCh
					return
				}
			case <-ticker.C:
				if !send(decompileHeartbeat(latestUpdate, jarName, i+1, total, currentPercent, time.Since(jarStarted))) {
					ticker.Stop()
					cancel()
					<-doneCh
					return
				}
			case outcome = <-doneCh:
				finished = true
			case <-ctx.Done():
				ticker.Stop()
				cancel()
				<-doneCh
				return
			}
		}
		ticker.Stop()

		select {
		case update := <-progressCh:
			latestUpdate = update
			event := mapDecompileProgress(update, jarName, i+1, total, jarStartPercent, jarEndPercent)
			currentPercent = max(currentPercent, event.Percent)
			event.Percent = currentPercent
			if !send(event) {
				return
			}
		default:
		}
		if outcome.err != nil {
			if ctx.Err() != nil {
				return
			}
			failedJars++
			if !send(decompileEvent{Type: "warning", Message: fmt.Sprintf("已跳过 %s（反编译失败）", jarName), Detail: outcome.err.Error(), Current: i + 1, Total: total, Percent: jarEndPercent, Jar: jarName, Log: cfr.LogFromError(outcome.err)}) {
				return
			}
			continue
		}
		totalJava += outcome.result.JavaFiles
		if !send(decompileEvent{Type: "progress", Message: fmt.Sprintf("已完成 %s", jarName), Current: i + 1, Total: total, Percent: jarEndPercent, Jar: jarName, JavaFiles: totalJava}) {
			return
		}
	}

	result := &cfr.Result{
		OutputDir:     absOut,
		JavaFiles:     totalJava,
		Elapsed:       fmt.Sprintf("%.1fs", time.Since(start).Seconds()),
		SucceededJars: total - failedJars,
		FailedJars:    failedJars,
	}
	_ = send(decompileEvent{Type: "done", Message: "反编译完成", Total: total, Percent: 100, Result: result, JavaFiles: totalJava, Elapsed: result.Elapsed})
}

func (h *Handler) handleStartDecompileTask(w http.ResponseWriter, r *http.Request) {
	var req decompileRequest
	if !h.decodeJSON(w, r, &req) {
		return
	}
	if err := validateDecompileRequest(req); err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.taskMu.TryLock() {
		h.writeError(w, http.StatusConflict, "已有反编译任务正在运行")
		return
	}

	id, err := NewSessionToken()
	if err != nil {
		h.taskMu.Unlock()
		h.writeError(w, http.StatusInternalServerError, "无法创建任务编号")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	task := &backgroundDecompileTask{
		id:        id,
		status:    "running",
		cancel:    cancel,
		createdAt: now,
		updatedAt: now,
	}
	h.decompileTasks.Store(id, task)
	h.cleanupFinishedDecompileTasks(now.Add(-24 * time.Hour))

	go func() {
		defer h.taskMu.Unlock()
		defer cancel()
		h.runDecompile(ctx, req, task.send)
		task.mu.Lock()
		if task.status == "running" {
			if ctx.Err() != nil {
				task.status = "canceled"
				task.error = "任务已取消"
			} else {
				task.status = "error"
				task.error = "反编译任务意外结束"
			}
			task.updatedAt = time.Now()
		}
		task.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	h.writeJSON(w, task.snapshot())
}

func (h *Handler) handleGetDecompileTask(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	value, ok := h.decompileTasks.Load(id)
	if !ok {
		h.writeError(w, http.StatusNotFound, "反编译任务不存在或已过期")
		return
	}
	h.writeJSON(w, value.(*backgroundDecompileTask).snapshot())
}

func (h *Handler) handleCancelDecompileTask(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	value, ok := h.decompileTasks.Load(id)
	if !ok {
		h.writeError(w, http.StatusNotFound, "反编译任务不存在或已过期")
		return
	}
	task := value.(*backgroundDecompileTask)
	task.mu.RLock()
	status := task.status
	cancel := task.cancel
	task.mu.RUnlock()
	if status == "running" && cancel != nil {
		cancel()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) cleanupFinishedDecompileTasks(cutoff time.Time) {
	h.decompileTasks.Range(func(key, value any) bool {
		task := value.(*backgroundDecompileTask)
		task.mu.RLock()
		finished := task.status != "running" && task.updatedAt.Before(cutoff)
		task.mu.RUnlock()
		if finished {
			h.decompileTasks.Delete(key)
		}
		return true
	})
}

// POST /api/upload - 接收拖拽上传的 JAR 文件
func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
	reader, err := r.MultipartReader()
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "无法解析上传请求: "+err.Error())
		return
	}

	tmpDir, err := os.MkdirTemp("", uploadDirPrefix+"*")
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "无法创建临时目录")
		return
	}
	keepDir := false
	defer func() {
		if !keepDir {
			_ = removeManagedUploadDir(tmpDir)
		}
	}()

	var saved []jar.JarFile
	usedNames := make(map[string]int)
	receivedFiles := 0
	jarFiles := 0

	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			h.writeUploadReadError(w, nextErr)
			return
		}

		if part.FormName() != "files" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		receivedFiles++

		fileName := filepath.Base(part.FileName())
		if !strings.EqualFold(filepath.Ext(fileName), ".jar") {
			_ = part.Close()
			continue
		}

		jarFiles++
		if jarFiles > maxUploadedFiles {
			_ = part.Close()
			h.writeError(w, http.StatusBadRequest, fmt.Sprintf("单次最多导入 %d 个 JAR", maxUploadedFiles))
			return
		}

		safeName := uniqueUploadName(fileName, usedNames)
		dstPath := filepath.Join(tmpDir, safeName)
		dst, openErr := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if openErr != nil {
			_ = part.Close()
			h.writeError(w, http.StatusInternalServerError, "无法保存 "+safeName+": "+openErr.Error())
			return
		}

		written, copyErr := io.Copy(dst, part)
		partErr := part.Close()
		dstErr := dst.Close()
		if copyErr != nil {
			h.writeUploadReadError(w, copyErr)
			return
		}
		if partErr != nil || dstErr != nil {
			h.writeError(w, http.StatusInternalServerError, "上传文件写入失败: "+safeName)
			return
		}
		if _, analyzeErr := jar.Analyze(dstPath); analyzeErr != nil {
			h.writeError(w, http.StatusBadRequest, safeName+" 不是有效的 JAR: "+analyzeErr.Error())
			return
		}

		saved = append(saved, jar.JarFile{
			Name: safeName,
			Path: dstPath,
			Size: written,
		})
	}

	if receivedFiles == 0 {
		h.writeError(w, http.StatusBadRequest, "没有收到文件")
		return
	}
	if len(saved) == 0 {
		h.writeError(w, http.StatusBadRequest, "没有有效的 .jar 文件")
		return
	}

	keepDir = true
	h.uploadDirs.Store(tmpDir, time.Now())
	h.writeJSON(w, map[string]any{
		"tempDir":            tmpDir,
		"suggestedOutputDir": suggestedUploadOutputDir(),
		"files":              saved,
		"total":              len(saved),
	})
}

func (h *Handler) writeUploadReadError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		h.writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("上传总大小超过 %s", formatByteLimit(maxUploadBody)))
		return
	}
	h.writeError(w, http.StatusBadRequest, "上传数据读取失败: "+err.Error())
}

func formatByteLimit(size int64) string {
	const gib = int64(1 << 30)
	if size%gib == 0 {
		return fmt.Sprintf("%d GiB", size/gib)
	}
	return fmt.Sprintf("%d 字节", size)
}

func suggestedUploadOutputDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, "Jar-Fucker Output", time.Now().Format("20060102-150405"))
}

func (h *Handler) handleDeleteUpload(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if _, ok := h.uploadDirs.Load(dir); !ok {
		h.writeError(w, http.StatusNotFound, "上传会话不存在")
		return
	}
	if err := removeManagedUploadDir(dir); err != nil {
		h.writeError(w, http.StatusInternalServerError, "清理上传文件失败: "+err.Error())
		return
	}
	h.uploadDirs.Delete(dir)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) cleanupStaleUploads() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleUploadMaxAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), uploadDirPrefix) {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(cutoff) {
			_ = removeManagedUploadDir(filepath.Join(os.TempDir(), entry.Name()))
		}
	}
}

func removeManagedUploadDir(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	absTemp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absTemp, absDir)
	if err != nil || rel == "." || filepath.Dir(rel) != "." || !strings.HasPrefix(filepath.Base(rel), uploadDirPrefix) {
		return fmt.Errorf("拒绝清理非托管目录: %s", absDir)
	}
	return os.RemoveAll(absDir)
}

func (h *Handler) pathIsUploaded(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	found := false
	h.uploadDirs.Range(func(key, _ any) bool {
		root, ok := key.(string)
		if !ok {
			return true
		}
		rel, err := filepath.Rel(root, absPath)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			found = true
			return false
		}
		return true
	})
	return found
}

func uniqueUploadName(name string, used map[string]int) string {
	count := used[name]
	used[name] = count + 1
	if count == 0 {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s-%d%s", base, count+1, ext)
}

func (h *Handler) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !h.decodeJSON(w, r, &req) {
		return
	}
	if req.Path == "" {
		h.writeError(w, 400, "请提供 JAR 文件路径")
		return
	}

	info, err := jar.AnalyzeContext(r.Context(), req.Path)
	if err != nil {
		h.writeError(w, 400, err.Error())
		return
	}
	h.writeJSON(w, info)
}

func (h *Handler) handleTree(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("root")
	if root == "" {
		h.writeError(w, 400, "请提供 root 参数")
		return
	}

	offset, err := parseNonNegativeQueryInt(r, "offset", 0)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := parseNonNegativeQueryInt(r, "limit", jar.DefaultTreePageSize)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tree, err := jar.ListTreeContext(r.Context(), root, r.URL.Query().Get("path"), offset, limit)
	if err != nil {
		h.writeError(w, 400, err.Error())
		return
	}
	h.writeJSON(w, tree)
}

func parseNonNegativeQueryInt(r *http.Request, name string, fallback int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s 必须是非负整数", name)
	}
	return value, nil
}

func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		h.writeError(w, 400, "请提供 path 参数")
		return
	}

	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		h.writeError(w, 404, "文件不存在: "+path)
		return
	}
	if info.Size() > maxViewerFileBytes {
		h.writeError(w, http.StatusRequestEntityTooLarge, "文件超过 5 MiB，请使用外部编辑器查看")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		h.writeError(w, 404, "文件不存在: "+path)
		return
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxViewerFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		h.writeError(w, http.StatusInternalServerError, "读取文件失败: "+path)
		return
	}
	if len(data) > maxViewerFileBytes {
		h.writeError(w, http.StatusRequestEntityTooLarge, "文件超过 5 MiB，请使用外部编辑器查看")
		return
	}

	h.writeJSON(w, map[string]any{
		"name":    filepath.Base(path),
		"path":    path,
		"content": string(data),
		"size":    len(data),
	})
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir        string `json:"dir"`
		Keyword    string `json:"keyword"`
		IgnoreCase bool   `json:"ignoreCase"`
		MaxResults int    `json:"maxResults"`
	}
	if !h.decodeJSON(w, r, &req) {
		return
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 100
	}
	if strings.TrimSpace(req.Keyword) == "" {
		h.writeError(w, http.StatusBadRequest, "搜索关键词不能为空")
		return
	}

	results, err := jar.SearchContext(r.Context(), req.Dir, req.Keyword, req.IgnoreCase, req.MaxResults)
	if err != nil {
		h.writeError(w, 500, err.Error())
		return
	}

	h.writeJSON(w, map[string]any{
		"results": results,
		"total":   len(results),
	})
}

type browseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size,omitempty"`
	IsJar bool   `json:"isJar,omitempty"`
}

func (h *Handler) handleBrowse(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("dir")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		h.writeError(w, 400, "无效路径")
		return
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		h.writeError(w, 400, "无法读取目录: "+err.Error())
		return
	}

	result := make([]browseEntry, 0, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}

		entry := browseEntry{
			Name: e.Name(),
			Path: filepath.Join(absDir, e.Name()),
		}

		if e.IsDir() {
			entry.Type = "dir"
		} else {
			entry.Type = "file"
			info, _ := e.Info()
			if info != nil {
				entry.Size = info.Size()
			}
			if strings.HasSuffix(strings.ToLower(e.Name()), ".jar") {
				entry.IsJar = true
			}
		}

		result = append(result, entry)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Type != result[j].Type {
			return result[i].Type == "dir"
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	h.writeJSON(w, map[string]any{
		"dir":     absDir,
		"parent":  filepath.Dir(absDir),
		"entries": result,
	})
}

func (h *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	configuredJava, configuredCFR := h.getToolConfig()
	effectiveJava := configuredJava
	if effectiveJava == "" {
		effectiveJava, _ = cfr.FindJava()
	}
	effectiveCFR := configuredCFR
	if effectiveCFR == "" {
		effectiveCFR = cfr.DefaultCFRPath()
	}

	h.writeJSON(w, map[string]any{
		"javaPath":          configuredJava,
		"cfrPath":           configuredCFR,
		"effectiveJavaPath": effectiveJava,
		"effectiveCfrPath":  effectiveCFR,
		"decompilerName":    "Fernflower",
		"decompilerVersion": cfr.Version,
		"configPath":        h.configPath,
		"ready":             effectiveJava != "" && effectiveCFR != "",
	})
}

func (h *Handler) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JavaPath string `json:"javaPath"`
		CfrPath  string `json:"cfrPath"`
	}
	if !h.decodeJSON(w, r, &req) {
		return
	}
	req.JavaPath = strings.TrimSpace(req.JavaPath)
	req.CfrPath = strings.TrimSpace(req.CfrPath)
	if err := validateJavaPath(req.JavaPath); err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.validateDecompilerPath(req.CfrPath); err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := appConfig{JavaPath: req.JavaPath, CfrPath: req.CfrPath}

	h.mu.Lock()
	defer h.mu.Unlock()
	if err := writeConfigFile(h.configPath, cfg); err != nil {
		h.writeError(w, 500, "保存配置失败: "+err.Error())
		return
	}
	h.javaPath = cfg.JavaPath
	h.cfrPath = cfg.CfrPath

	h.writeJSON(w, map[string]string{"status": "ok"})
}

func validateJavaPath(path string) error {
	if path == "" {
		return nil
	}
	resolved := path
	if !strings.ContainsAny(path, `/\\`) {
		var err error
		resolved, err = exec.LookPath(path)
		if err != nil {
			return fmt.Errorf("Java 可执行文件不存在: %s", path)
		}
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		return fmt.Errorf("Java 可执行文件不存在: %s", path)
	}
	return cfr.ValidateJava(resolved)
}

func (h *Handler) validateDecompilerPath(path string) error {
	if path == "" {
		return nil
	}
	if h.pathIsUploaded(path) {
		return fmt.Errorf("不能把待分析的上传 JAR 设为反编译器")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("Fernflower 路径不存在: %s", path)
	}
	if info.IsDir() {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jar" && ext != ".zip" {
		return fmt.Errorf("Fernflower 只支持 .jar、源码 .zip 或源码目录")
	}
	return nil
}
