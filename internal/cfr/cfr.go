package cfr

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/safezip"
)

const (
	Version = "fernflower-idea-262.8665.81"
)

const fernflowerMainClass = "org.jetbrains.java.decompiler.main.decompiler.ConsoleDecompiler"

const (
	fernflowerJarName = "fernflower.jar"
	minJavaMajor      = 21
)

const maxCommandLogBytes = 16 * 1024

var fernflowerBuildMu sync.Mutex

//go:embed assets/fernflower.jar
var bundledFernflowerJar []byte

var (
	bundledToolOnce sync.Once
	bundledToolPath string
	bundledToolErr  error
)

type Result struct {
	OutputDir     string `json:"outputDir"`
	JavaFiles     int    `json:"javaFiles"`
	Elapsed       string `json:"elapsed"`
	SucceededJars int    `json:"succeededJars,omitempty"`
	FailedJars    int    `json:"failedJars,omitempty"`
}

type ProgressPhase string

const (
	ProgressExtracting  ProgressPhase = "extracting"
	ProgressDecompiling ProgressPhase = "decompiling"
	ProgressFinalizing  ProgressPhase = "finalizing"
)

type Progress struct {
	Phase ProgressPhase
	// CompletedUnits counts root compilation units whose Fernflower processing
	// attempt has ended. ClassFiles is a conservative upper bound because inner
	// classes are folded into their root compilation unit.
	CompletedUnits int
	ClassFiles     int
	Detail         string
}

type ProgressFunc func(Progress)

type CommandError struct {
	Message string
	Log     string
	Err     error
}

func (e *CommandError) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (e *CommandError) Unwrap() error { return e.Err }

func LogFromError(err error) string {
	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		return commandErr.Log
	}
	return ""
}

func commandError(message string, err error, log string) error {
	return &CommandError{Message: message, Err: err, Log: strings.TrimSpace(log)}
}

type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func newLimitedBuffer(max int) *limitedBuffer { return &limitedBuffer{max: max} }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.max <= 0 {
		return n, nil
	}
	if len(p) >= b.max {
		b.buf.Reset()
		b.buf.Write(p[len(p)-b.max:])
		return n, nil
	}
	b.buf.Write(p)
	if b.buf.Len() > b.max {
		data := b.buf.Bytes()
		kept := append([]byte(nil), data[b.buf.Len()-b.max:]...)
		b.buf.Reset()
		b.buf.Write(kept)
	}
	return n, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

const (
	fernflowerClassMarker     = "Decompiling class "
	fernflowerClassDoneMarker = "... done"
)

type fernflowerProgressWriter struct {
	pending        []byte
	activeClass    string
	readingClass   bool
	completedUnits int
	classFiles     int
	reportEvery    int
	report         ProgressFunc
}

func newFernflowerProgressWriter(classFiles int, report ProgressFunc) *fernflowerProgressWriter {
	return &fernflowerProgressWriter{
		classFiles:  classFiles,
		reportEvery: max(1, (classFiles+99)/100),
		report:      report,
	}
}

func (w *fernflowerProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.pending = append(w.pending, p...)
	for {
		newline := bytes.IndexByte(w.pending, '\n')
		if newline < 0 {
			break
		}
		w.consumeLine(string(w.pending[:newline]))
		w.pending = w.pending[newline+1:]
	}
	if len(w.pending) > 64<<10 {
		w.pending = append([]byte(nil), w.pending[len(w.pending)-(64<<10):]...)
	}
	return n, nil
}

func (w *fernflowerProgressWriter) Flush() {
	if len(w.pending) == 0 {
		return
	}
	w.consumeLine(string(w.pending))
	w.pending = nil
}

func (w *fernflowerProgressWriter) consumeLine(line string) {
	marker := strings.Index(line, fernflowerClassMarker)
	if marker >= 0 {
		w.activeClass = strings.TrimSpace(line[marker+len(fernflowerClassMarker):])
		if len(w.activeClass) > 512 {
			w.activeClass = w.activeClass[:512]
		}
		w.readingClass = true
		return
	}
	if !w.readingClass || !strings.HasSuffix(strings.TrimSpace(line), fernflowerClassDoneMarker) {
		return
	}
	w.readingClass = false
	w.completedUnits++
	if w.report == nil || (w.completedUnits != 1 && w.completedUnits%w.reportEvery != 0) {
		return
	}
	w.report(Progress{
		Phase:          ProgressDecompiling,
		CompletedUnits: w.completedUnits,
		ClassFiles:     w.classFiles,
		Detail:         w.activeClass,
	})
}

func configureCommandCancellation(cmd *exec.Cmd) {
	if runtime.GOOS != "windows" {
		return
	}
	cmd.WaitDelay = 5 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		taskkill := "taskkill.exe"
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			taskkill = filepath.Join(systemRoot, "System32", taskkill)
		}
		killer := exec.Command(taskkill, "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		killer.Stdout = io.Discard
		killer.Stderr = io.Discard
		if err := killer.Run(); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}

func DefaultCFRPath() string {
	if path, err := materializeBundledFernflower(); err == nil {
		return path
	}
	if path := findBundledFernflower(); path != "" {
		return path
	}

	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), Version)
}

func materializeBundledFernflower() (string, error) {
	bundledToolOnce.Do(func() {
		cacheDir, err := os.UserCacheDir()
		if err != nil || cacheDir == "" {
			cacheDir = os.TempDir()
		}
		toolDir := filepath.Join(cacheDir, "Jar-Fucker", "tools")
		if err := os.MkdirAll(toolDir, 0700); err != nil {
			bundledToolErr = fmt.Errorf("无法创建工具缓存目录: %w", err)
			return
		}
		hash := sha256.Sum256(bundledFernflowerJar)
		toolPath := filepath.Join(toolDir, fmt.Sprintf("fernflower-%s-%x.jar", Version, hash[:8]))
		if data, err := os.ReadFile(toolPath); err == nil {
			if sha256.Sum256(data) == hash {
				bundledToolPath = toolPath
				return
			}
			if err := os.Remove(toolPath); err != nil {
				bundledToolErr = fmt.Errorf("Fernflower 缓存校验失败且无法替换: %w", err)
				return
			}
		}

		tmp, err := os.CreateTemp(toolDir, ".fernflower-*.tmp")
		if err != nil {
			bundledToolErr = err
			return
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if err := tmp.Chmod(0600); err != nil {
			tmp.Close()
			bundledToolErr = err
			return
		}
		if _, err := tmp.Write(bundledFernflowerJar); err != nil {
			tmp.Close()
			bundledToolErr = err
			return
		}
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			bundledToolErr = err
			return
		}
		if err := tmp.Close(); err != nil {
			bundledToolErr = err
			return
		}
		if err := os.Rename(tmpPath, toolPath); err != nil {
			bundledToolErr = err
			return
		}
		bundledToolPath = toolPath
	})
	return bundledToolPath, bundledToolErr
}

func FindJava() (string, error) {
	javaPath, major, err := findJavaWithMinMajor(minJavaMajor)
	if err == nil {
		return javaPath, nil
	}
	if major > 0 {
		return "", fmt.Errorf("未找到 Java %d+，当前最高版本为 Java %d，请安装 JDK/JRE %d+ 或设置 JAVA_HOME", minJavaMajor, major, minJavaMajor)
	}
	return "", fmt.Errorf("未找到 Java %d+，请安装 JDK/JRE %d+ 或设置 JAVA_HOME", minJavaMajor, minJavaMajor)
}

func ValidateJava(javaPath string) error {
	major, err := javaMajor(javaPath)
	if err != nil {
		return fmt.Errorf("无法运行 Java: %w", err)
	}
	if major < minJavaMajor {
		return fmt.Errorf("Fernflower 需要 Java %d+，当前为 Java %d", minJavaMajor, major)
	}
	return nil
}

func findJavaWithMinMajor(minMajor int) (string, int, error) {
	bestMajor := 0
	bestPath := ""
	for _, candidate := range javaCandidates() {
		major, err := javaMajor(candidate)
		if err != nil {
			continue
		}
		if major >= minMajor {
			return candidate, major, nil
		}
		if major > bestMajor {
			bestMajor = major
			bestPath = candidate
		}
	}
	if bestPath != "" {
		return bestPath, bestMajor, fmt.Errorf("Java 版本过低")
	}
	return "", 0, fmt.Errorf("未找到 Java")
}

func javaCandidates() []string {
	var candidates []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
		key := strings.ToLower(filepath.Clean(path))
		if seen[key] {
			return
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			seen[key] = true
			candidates = append(candidates, path)
		}
	}
	javaExe := "java"
	if runtime.GOOS == "windows" {
		javaExe = "java.exe"
	}
	addJavaHome := func(home string) {
		if home != "" {
			add(filepath.Join(home, "bin", javaExe))
		}
	}

	addJavaHome(os.Getenv("JAVA_HOME"))
	if path, err := exec.LookPath("java"); err == nil {
		add(path)
	}

	var patterns []string
	switch runtime.GOOS {
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles(x86)")} {
			if root == "" {
				continue
			}
			patterns = append(patterns,
				filepath.Join(root, "Java", "*", "bin", javaExe),
				filepath.Join(root, "Eclipse Adoptium", "*", "bin", javaExe),
				filepath.Join(root, "Microsoft", "jdk-*", "bin", javaExe),
			)
		}
	case "darwin":
		patterns = append(patterns,
			"/Library/Java/JavaVirtualMachines/*/Contents/Home/bin/java",
			"/opt/homebrew/opt/openjdk*/bin/java",
			"/usr/local/opt/openjdk*/bin/java",
		)
	default:
		patterns = append(patterns,
			"/usr/lib/jvm/*/bin/java",
			"/usr/java/*/bin/java",
			"/opt/java/*/bin/java",
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		patterns = append(patterns, filepath.Join(home, ".jdks", "*", "bin", javaExe))
	}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			add(match)
		}
	}
	return candidates
}

func javaMajor(javaPath string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, javaPath, "-version")
	configureCommandCancellation(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("Java 版本检测超时: %w", ctx.Err())
		}
		return 0, err
	}
	return parseJavaMajor(string(out))
}

func parseJavaMajor(output string) (int, error) {
	text := output
	idx := strings.Index(text, "version")
	if idx >= 0 {
		text = text[idx+len("version"):]
	} else {
		idx = strings.IndexAny(text, "0123456789")
		if idx < 0 {
			return 0, fmt.Errorf("无法解析 Java 版本: %s", strings.TrimSpace(output))
		}
		text = text[idx:]
	}
	text = strings.TrimLeft(text, " \t\r\n\"")
	if strings.HasPrefix(text, "1.") {
		text = strings.TrimPrefix(text, "1.")
	}
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("无法解析 Java 版本: %s", strings.TrimSpace(output))
	}
	return strconv.Atoi(text[:end])
}

func EnsureCFR(cfrPath string) error {
	return EnsureCFRContext(context.Background(), cfrPath)
}

func EnsureCFRContext(ctx context.Context, cfrPath string) error {
	javaPath, _ := FindJava()
	return EnsureDecompilerContext(ctx, cfrPath, javaPath)
}

func EnsureDecompilerContext(ctx context.Context, cfrPath, javaPath string) error {
	if cfrPath == "" {
		cfrPath = DefaultCFRPath()
	}

	_, err := ensureFernflower(ctx, cfrPath, javaPath)
	return err
}

type fernflowerTool struct {
	Args []string
}

func ensureFernflower(ctx context.Context, toolPath, javaPath string) (*fernflowerTool, error) {
	jarPath, err := ensureFernflowerJarPath(ctx, toolPath, javaPath)
	if err != nil {
		return nil, err
	}
	if isUploadTempPath(jarPath) {
		return nil, fmt.Errorf("拒绝执行上传会话中的 JAR 作为反编译器")
	}
	if err := validateFernflowerJar(jarPath); err != nil {
		return nil, err
	}
	return &fernflowerTool{Args: []string{"-jar", jarPath}}, nil
}

func ensureFernflowerJarPath(ctx context.Context, toolPath, javaPath string) (string, error) {
	if toolPath == "" {
		toolPath = DefaultCFRPath()
	}
	absTool, _ := filepath.Abs(toolPath)
	info, err := os.Stat(absTool)
	if err != nil {
		return "", fmt.Errorf("未找到 Fernflower: %s", absTool)
	}

	if !info.IsDir() && strings.EqualFold(filepath.Ext(absTool), ".jar") {
		return absTool, nil
	}

	workDir := absTool
	if !info.IsDir() && strings.EqualFold(filepath.Ext(absTool), ".zip") {
		workDir, err = prepareFernflowerSourceZip(ctx, absTool)
		if err != nil {
			return "", err
		}
	}

	if !isDir(workDir) {
		return "", fmt.Errorf("Fernflower 路径无效，请选择 %s、源码 zip 或解压目录: %s", fernflowerJarName, absTool)
	}

	if isFernflowerProject(workDir) {
		return ensureFernflowerJar(ctx, workDir, javaPath)
	}

	if jar := findJarInDir(workDir); jar != "" {
		return jar, nil
	}

	return "", fmt.Errorf("Fernflower 路径无效，请选择 %s、源码 zip 或解压目录: %s", fernflowerJarName, absTool)
}

func findBundledFernflower() string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	dirs = uniqueCleanDirs(dirs)

	// 优先使用已经构建好的 fernflower.jar。这样默认路径就是可直接
	// `java -jar` 的产物，不会因为误识别到源码目录而触发 Gradle。
	for _, dir := range dirs {
		for _, rel := range []string{
			fernflowerJarName,
			filepath.Join(Version, "build", "libs", fernflowerJarName),
		} {
			if jar := existingFile(filepath.Join(dir, rel)); jar != "" {
				return jar
			}
		}
	}

	// 没有现成 jar 时才退回源码目录/zip，让后续逻辑按需构建。
	for _, dir := range dirs {
		projectDir := filepath.Join(dir, Version)
		if isFernflowerProject(projectDir) {
			return projectDir
		}
	}
	for _, dir := range dirs {
		if zip := existingFile(filepath.Join(dir, Version+".zip")); zip != "" {
			return zip
		}
	}
	return ""
}

func uniqueCleanDirs(dirs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err == nil {
			dir = abs
		}
		key := strings.ToLower(filepath.Clean(dir))
		if seen[key] {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			seen[key] = true
			out = append(out, dir)
		}
	}
	return out
}

func existingFile(path string) string {
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		return path
	}
	return ""
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func findJarInDir(dir string) string {
	return existingFile(filepath.Join(dir, fernflowerJarName))
}

func validateFernflowerJar(jarPath string) error {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return fmt.Errorf("无法打开 Fernflower JAR: %w", err)
	}
	defer r.Close()
	mainClass := strings.ReplaceAll(fernflowerMainClass, ".", "/") + ".class"
	for _, file := range r.File {
		if file.Name == mainClass {
			return nil
		}
	}
	return fmt.Errorf("所选 JAR 不包含 Fernflower 入口类 %s", fernflowerMainClass)
}

func isUploadTempPath(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absTemp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absTemp, absPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	first := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
	return strings.HasPrefix(first, "jar-fucker-upload-")
}

func isFernflowerProject(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "gradlew")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "build.gradle.kts")); err != nil {
		return false
	}
	return true
}

func ensureFernflowerJar(ctx context.Context, projectDir, javaPath string) (string, error) {
	fernflowerBuildMu.Lock()
	defer fernflowerBuildMu.Unlock()

	jarPath := filepath.Join(projectDir, "build", "libs", fernflowerJarName)
	if !fernflowerJarNeedsBuild(projectDir, jarPath) {
		return jarPath, nil
	}

	if javaPath == "" {
		var err error
		javaPath, err = FindJava()
		if err != nil {
			return "", err
		}
	}

	gradlew := filepath.Join(projectDir, "gradlew")
	if runtime.GOOS == "windows" {
		gradlew = filepath.Join(projectDir, "gradlew.bat")
	}
	cmd := exec.CommandContext(ctx, gradlew, "jar", "--no-daemon")
	cmd.Dir = projectDir
	cmd.Env = fernflowerBuildEnv(javaPath)
	logBuf := newLimitedBuffer(maxCommandLogBytes)
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	configureCommandCancellation(cmd)
	if err := cmd.Run(); err != nil {
		return "", commandError("构建 Fernflower JAR 失败", err, logBuf.String())
	}

	if jar := existingFile(jarPath); jar != "" {
		return jar, nil
	}
	return "", fmt.Errorf("构建 Fernflower JAR 后未找到产物: %s", jarPath)
}

func fernflowerJarNeedsBuild(projectDir, jarPath string) bool {
	jarInfo, err := os.Stat(jarPath)
	if err != nil || jarInfo.IsDir() || jarInfo.Size() == 0 {
		return true
	}
	jarTime := jarInfo.ModTime()
	newer := false
	check := func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || newer {
			return nil
		}
		if info.ModTime().After(jarTime) {
			newer = true
		}
		return nil
	}
	for _, rel := range []string{"src", "resources"} {
		_ = filepath.Walk(filepath.Join(projectDir, rel), check)
		if newer {
			return true
		}
	}
	for _, rel := range []string{"build.gradle.kts", "gradle.properties"} {
		if info, err := os.Stat(filepath.Join(projectDir, rel)); err == nil && info.ModTime().After(jarTime) {
			return true
		}
	}
	return false
}

func fernflowerBuildEnv(javaPath string) []string {
	env := os.Environ()
	if javaPath == "" {
		return env
	}
	javaBin := filepath.Dir(javaPath)
	javaHome := filepath.Dir(javaBin)
	return append(env,
		"JAVA_HOME="+javaHome,
		"PATH="+javaBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
}

func prepareFernflowerSourceZip(ctx context.Context, zipPath string) (string, error) {
	info, err := os.Stat(zipPath)
	if err != nil {
		return "", fmt.Errorf("无法读取 Fernflower 压缩包: %w", err)
	}
	cacheBase, err := os.UserCacheDir()
	if err != nil || cacheBase == "" {
		cacheBase = os.TempDir()
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", zipPath, info.Size(), info.ModTime().UnixNano())))
	cacheParent := filepath.Join(cacheBase, "Jar-Fucker", "fernflower")
	cacheRoot := filepath.Join(cacheParent, fmt.Sprintf("%x", fingerprint[:12]))
	if project := findFernflowerProject(cacheRoot, 3); project != "" {
		return project, nil
	}
	if _, err := os.Stat(cacheRoot); err == nil {
		return "", fmt.Errorf("Fernflower 缓存不完整，请删除后重试: %s", cacheRoot)
	}
	if err := os.MkdirAll(cacheParent, 0700); err != nil {
		return "", fmt.Errorf("无法创建 Fernflower 缓存目录: %w", err)
	}
	staging, err := os.MkdirTemp(cacheParent, ".extract-*")
	if err != nil {
		return "", fmt.Errorf("无法创建 Fernflower 解压目录: %w", err)
	}
	defer os.RemoveAll(staging)
	if _, err := safezip.ExtractFileWithOptions(zipPath, staging, safezip.Options{Context: ctx, Limits: safezip.DefaultLimits()}); err != nil {
		return "", fmt.Errorf("无法安全解压 Fernflower 压缩包: %w", err)
	}
	project := findFernflowerProject(staging, 3)
	if project == "" {
		return "", fmt.Errorf("Fernflower 压缩包中未找到 Gradle 项目")
	}
	projectRel, err := filepath.Rel(staging, project)
	if err != nil {
		return "", err
	}
	if err := os.Rename(staging, cacheRoot); err != nil {
		if existing := findFernflowerProject(cacheRoot, 3); existing != "" {
			return existing, nil
		}
		return "", fmt.Errorf("无法提交 Fernflower 缓存: %w", err)
	}
	return filepath.Join(cacheRoot, projectRel), nil
}

func findFernflowerProject(root string, depth int) string {
	if depth < 0 || !isDir(root) {
		return ""
	}
	if isFernflowerProject(root) {
		return root
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if project := findFernflowerProject(filepath.Join(root, entry.Name()), depth-1); project != "" {
				return project
			}
		}
	}
	return ""
}

func EnsureCFRLegacy(ctx context.Context, cfrPath string) error {
	if cfrPath == "" {
		cfrPath = DefaultCFRPath()
	}

	if _, err := os.Stat(cfrPath); err == nil {
		return nil
	}

	dir := filepath.Dir(cfrPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("无法创建反编译器目录: %w", err)
	}
	return ctx.Err()
}

// DecompileMultiple 批量反编译多个 JAR，每个 JAR 输出到独立子目录
func DecompileMultiple(jars []string, outputDir, javaPath, cfrPath, filterPkg string) (*Result, error) {
	return DecompileMultipleContext(context.Background(), jars, outputDir, javaPath, cfrPath, filterPkg)
}

func DecompileMultipleContext(ctx context.Context, jars []string, outputDir, javaPath, cfrPath, filterPkg string) (*Result, error) {
	if javaPath == "" {
		var err error
		javaPath, err = FindJava()
		if err != nil {
			return nil, err
		}
	}
	if cfrPath == "" {
		cfrPath = DefaultCFRPath()
	}
	if _, err := ensureFernflower(ctx, cfrPath, javaPath); err != nil {
		return nil, err
	}

	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, fmt.Errorf("输出目录无效: %w", err)
	}
	if err := os.MkdirAll(absOut, 0755); err != nil {
		return nil, fmt.Errorf("无法创建输出目录: %w", err)
	}
	start := time.Now()
	totalJava := 0
	usedNames := make(map[string]int)

	for _, jarPath := range jars {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		jarName := strings.TrimSuffix(filepath.Base(jarPath), filepath.Ext(jarPath))
		key := strings.ToLower(jarName)
		usedNames[key]++
		if usedNames[key] > 1 {
			jarName = fmt.Sprintf("%s-%d", jarName, usedNames[key])
		}
		subDir := filepath.Join(absOut, jarName)

		result, err := DecompileContext(ctx, jarPath, subDir, javaPath, cfrPath, filterPkg)
		if err != nil {
			return nil, fmt.Errorf("反编译 %s 失败: %w", filepath.Base(jarPath), err)
		}
		totalJava += result.JavaFiles
	}

	return &Result{
		OutputDir: absOut,
		JavaFiles: totalJava,
		Elapsed:   fmt.Sprintf("%.1fs", time.Since(start).Seconds()),
	}, nil
}

func Decompile(jarPath, outputDir, javaPath, cfrPath, filterPkg string) (*Result, error) {
	return DecompileContext(context.Background(), jarPath, outputDir, javaPath, cfrPath, filterPkg)
}

func DecompileContext(ctx context.Context, jarPath, outputDir, javaPath, cfrPath, filterPkg string) (*Result, error) {
	return DecompileContextWithProgress(ctx, jarPath, outputDir, javaPath, cfrPath, filterPkg, nil)
}

func DecompileContextWithProgress(ctx context.Context, jarPath, outputDir, javaPath, cfrPath, filterPkg string, report ProgressFunc) (*Result, error) {
	if javaPath == "" {
		var err error
		javaPath, err = FindJava()
		if err != nil {
			return nil, err
		}
	}

	if cfrPath == "" {
		cfrPath = DefaultCFRPath()
	}

	tool, err := ensureFernflower(ctx, cfrPath, javaPath)
	if err != nil {
		return nil, err
	}

	absJar, err := filepath.Abs(jarPath)
	if err != nil {
		return nil, fmt.Errorf("JAR 路径无效: %w", err)
	}
	if info, err := os.Stat(absJar); err != nil || info.IsDir() {
		return nil, fmt.Errorf("JAR 文件不存在: %s", absJar)
	}
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, fmt.Errorf("输出目录无效: %w", err)
	}
	if err := os.MkdirAll(absOut, 0755); err != nil {
		return nil, fmt.Errorf("无法创建输出目录: %w", err)
	}
	if report != nil {
		report(Progress{Phase: ProgressExtracting})
	}
	sourceDir, classFiles, err := extractJarForFernflowerWithStats(ctx, absJar, filterPkg)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(sourceDir)

	args := append([]string{}, tool.Args...)
	logLevel := "ERROR"
	if report != nil {
		logLevel = "INFO"
		report(Progress{Phase: ProgressDecompiling, ClassFiles: classFiles})
	}
	args = append(args, "-log="+logLevel, sourceDir, absOut)

	start := time.Now()

	cmd := exec.CommandContext(ctx, javaPath, args...)
	logBuf := newLimitedBuffer(maxCommandLogBytes)
	var progressWriter *fernflowerProgressWriter
	if report != nil {
		progressWriter = newFernflowerProgressWriter(classFiles, report)
		cmd.Stdout = io.MultiWriter(logBuf, progressWriter)
	} else {
		cmd.Stdout = logBuf
	}
	cmd.Stderr = logBuf
	configureCommandCancellation(cmd)

	if err := cmd.Run(); err != nil {
		if progressWriter != nil {
			progressWriter.Flush()
		}
		return nil, commandError("Fernflower 反编译失败", err, logBuf.String())
	}
	if progressWriter != nil {
		progressWriter.Flush()
		report(Progress{Phase: ProgressFinalizing, CompletedUnits: progressWriter.completedUnits, ClassFiles: classFiles})
	}

	elapsed := time.Since(start)

	javaFiles := 0
	if err := filepath.Walk(absOut, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(path), ".java") {
			javaFiles++
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("统计反编译结果失败: %w", err)
	}

	return &Result{
		OutputDir: absOut,
		JavaFiles: javaFiles,
		Elapsed:   fmt.Sprintf("%.1fs", elapsed.Seconds()),
	}, nil
}

func extractJarForFernflower(ctx context.Context, jarPath, filterPkg string) (string, error) {
	dir, _, err := extractJarForFernflowerWithStats(ctx, jarPath, filterPkg)
	return dir, err
}

func extractJarForFernflowerWithStats(ctx context.Context, jarPath, filterPkg string) (string, int, error) {
	tmpDir, err := os.MkdirTemp("", "jar-fucker-fernflower-*")
	if err != nil {
		return "", 0, fmt.Errorf("无法创建临时目录: %w", err)
	}
	keepDir := false
	defer func() {
		if !keepDir {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	filterPrefix := strings.ReplaceAll(strings.Trim(filterPkg, "."), ".", "/")
	if filterPrefix != "" {
		filterPrefix += "/"
	}
	options := safezip.Options{
		Context:            ctx,
		Limits:             safezip.DefaultLimits(),
		SkipDuplicateFiles: true,
		Include: func(file *zip.File) bool {
			name := strings.ReplaceAll(file.Name, `\`, "/")
			return filterPrefix == "" || !strings.HasSuffix(strings.ToLower(name), ".class") || strings.HasPrefix(name, filterPrefix)
		},
	}
	if _, err := safezip.ExtractFileWithOptions(jarPath, tmpDir, options); err != nil {
		return "", 0, fmt.Errorf("无法安全展开 JAR: %w", err)
	}
	classFiles, err := countFernflowerClassFiles(ctx, tmpDir)
	if err != nil {
		return "", 0, fmt.Errorf("统计 Fernflower 输入类失败: %w", err)
	}

	keepDir = true
	return tmpDir, classFiles, nil
}

func countFernflowerClassFiles(ctx context.Context, root string) (int, error) {
	total := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		name := info.Name()
		if strings.HasSuffix(name, ".class") {
			total++
			return nil
		}
		if !strings.HasSuffix(name, ".jar") && !strings.HasSuffix(name, ".zip") {
			return nil
		}

		archiveClasses, err := countFernflowerArchiveClasses(ctx, path)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			// Fernflower logs unreadable nested archives and continues scanning.
			return nil
		}
		total += archiveClasses
		return nil
	})
	return total, err
}

func countFernflowerArchiveClasses(ctx context.Context, archivePath string) (int, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return 0, err
	}
	defer r.Close()

	total := 0
	for _, file := range r.File {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		name := file.Name
		if strings.HasPrefix(name, "META-INF/versions") || strings.HasSuffix(name, "/") {
			continue
		}
		if strings.HasSuffix(name, ".class") {
			total++
		}
	}
	return total, nil
}
