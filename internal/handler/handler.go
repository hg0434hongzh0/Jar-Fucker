package handler

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/cfr"
	"github.com/hg0434hongzh0/Jar-Fucker/internal/jar"
)

type Handler struct {
	javaPath string
	cfrPath  string
}

func New(webFS fs.FS) http.Handler {
	h := &Handler{}

	mux := http.NewServeMux()

	mux.Handle("GET /", http.FileServerFS(webFS))

	mux.HandleFunc("POST /api/scan", h.handleScan)
	mux.HandleFunc("POST /api/analyze", h.handleAnalyze)
	mux.HandleFunc("POST /api/extract", h.handleExtract)
	mux.HandleFunc("POST /api/decompile", h.handleDecompile)
	mux.HandleFunc("POST /api/upload", h.handleUpload)
	mux.HandleFunc("GET /api/tree", h.handleTree)
	mux.HandleFunc("GET /api/file", h.handleFile)
	mux.HandleFunc("POST /api/search", h.handleSearch)
	mux.HandleFunc("GET /api/browse", h.handleBrowse)
	mux.HandleFunc("GET /api/config", h.handleGetConfig)
	mux.HandleFunc("PUT /api/config", h.handleSetConfig)

	return mux
}

func (h *Handler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// POST /api/scan - 扫描目录内所有 JAR 文件
func (h *Handler) handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir string `json:"dir"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, 400, "无效请求")
		return
	}
	if req.Dir == "" {
		h.writeError(w, 400, "请提供目录路径")
		return
	}

	jars, err := jar.ScanDir(req.Dir)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, 400, "无效请求")
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

	total, err := jar.ExtractAll(req.Jars, req.OutputDir)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, 400, "无效请求")
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

	if len(req.Jars) == 1 {
		result, err := cfr.Decompile(req.Jars[0], req.OutputDir, h.javaPath, h.cfrPath, req.FilterPkg)
		if err != nil {
			h.writeError(w, 500, err.Error())
			return
		}
		h.writeJSON(w, result)
		return
	}

	result, err := cfr.DecompileMultiple(req.Jars, req.OutputDir, h.javaPath, h.cfrPath)
	if err != nil {
		h.writeError(w, 500, err.Error())
		return
	}
	h.writeJSON(w, result)
}

// POST /api/upload - 接收拖拽上传的 JAR 文件
func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(500 << 20); err != nil {
		h.writeError(w, 400, "上传解析失败: "+err.Error())
		return
	}

	tmpDir, err := os.MkdirTemp("", "jar-fucker-upload-*")
	if err != nil {
		h.writeError(w, 500, "无法创建临时目录")
		return
	}

	files := r.MultipartForm.File["files"]
	var saved []jar.JarFile

	for _, fh := range files {
		if !strings.HasSuffix(strings.ToLower(fh.Filename), ".jar") {
			continue
		}

		src, err := fh.Open()
		if err != nil {
			continue
		}

		dstPath := filepath.Join(tmpDir, fh.Filename)
		dst, err := os.Create(dstPath)
		if err != nil {
			src.Close()
			continue
		}

		io.Copy(dst, src)
		src.Close()
		dst.Close()

		saved = append(saved, jar.JarFile{
			Name: fh.Filename,
			Path: dstPath,
			Size: fh.Size,
		})
	}

	h.writeJSON(w, map[string]any{
		"tempDir": tmpDir,
		"files":   saved,
		"total":   len(saved),
	})
}

func (h *Handler) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, 400, "无效请求")
		return
	}
	if req.Path == "" {
		h.writeError(w, 400, "请提供 JAR 文件路径")
		return
	}

	info, err := jar.Analyze(req.Path)
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

	tree, err := jar.BuildTree(root)
	if err != nil {
		h.writeError(w, 400, err.Error())
		return
	}
	h.writeJSON(w, tree)
}

func (h *Handler) handleFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		h.writeError(w, 400, "请提供 path 参数")
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		h.writeError(w, 404, "文件不存在: "+path)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, 400, "无效请求")
		return
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 100
	}

	results, err := jar.Search(req.Dir, req.Keyword, req.IgnoreCase, req.MaxResults)
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
	javaPath := h.javaPath
	if javaPath == "" {
		javaPath, _ = cfr.FindJava()
	}

	cfrPath := h.cfrPath
	if cfrPath == "" {
		cfrPath = cfr.DefaultCFRPath()
	}

	h.writeJSON(w, map[string]string{
		"javaPath":   javaPath,
		"cfrPath":    cfrPath,
		"cfrVersion": cfr.Version,
	})
}

func (h *Handler) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JavaPath string `json:"javaPath"`
		CfrPath  string `json:"cfrPath"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, 400, "无效请求")
		return
	}

	if req.JavaPath != "" {
		h.javaPath = req.JavaPath
	}
	if req.CfrPath != "" {
		h.cfrPath = req.CfrPath
	}

	h.writeJSON(w, map[string]string{"status": "ok"})
}
