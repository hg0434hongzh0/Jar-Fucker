package cfr

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	Version = "fernflower-idea-262.8665.81"
)

const fernflowerMainClass = "org.jetbrains.java.decompiler.main.decompiler.ConsoleDecompiler"

const maxCommandLogBytes = 16 * 1024

type Result struct {
	OutputDir string `json:"outputDir"`
	JavaFiles int    `json:"javaFiles"`
	Elapsed   string `json:"elapsed"`
}

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
	buf bytes.Buffer
	max int
}

func newLimitedBuffer(max int) *limitedBuffer { return &limitedBuffer{max: max} }

func (b *limitedBuffer) Write(p []byte) (int, error) {
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

func (b *limitedBuffer) String() string { return b.buf.String() }

func DefaultCFRPath() string {
	if path := findBundledFernflower(); path != "" {
		return path
	}

	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), Version+".zip")
}

func FindJava() (string, error) {
	if jh := os.Getenv("JAVA_HOME"); jh != "" {
		bin := filepath.Join(jh, "bin", "java")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}

	path, err := exec.LookPath("java")
	if err == nil {
		return path, nil
	}

	return "", fmt.Errorf("未找到 Java，请安装 JDK/JRE 或设置 JAVA_HOME")
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
	if toolPath == "" {
		toolPath = DefaultCFRPath()
	}
	absTool, _ := filepath.Abs(toolPath)
	info, err := os.Stat(absTool)
	if err != nil {
		return nil, fmt.Errorf("未找到 Fernflower: %s", absTool)
	}

	if !info.IsDir() && strings.EqualFold(filepath.Ext(absTool), ".jar") {
		return &fernflowerTool{Args: []string{"-jar", absTool}}, nil
	}

	workDir := absTool
	if !info.IsDir() && strings.EqualFold(filepath.Ext(absTool), ".zip") {
		workDir = filepath.Join(filepath.Dir(absTool), strings.TrimSuffix(filepath.Base(absTool), filepath.Ext(absTool)))
		if _, err := os.Stat(workDir); err != nil {
			if err := unzipFernflower(absTool, filepath.Dir(absTool)); err != nil {
				return nil, err
			}
		}
	}

	if !isFernflowerProject(workDir) {
		return nil, fmt.Errorf("Fernflower 路径无效，请选择 fernflower.jar、源码 zip 或解压目录: %s", absTool)
	}

	cp, err := ensureFernflowerClasspath(ctx, workDir, javaPath)
	if err != nil {
		return nil, err
	}
	return &fernflowerTool{Args: []string{"-cp", cp, fernflowerMainClass}}, nil
}

func findBundledFernflower() string {
	dirs := []string{"."}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}

	for _, dir := range dirs {
		for _, pattern := range []string{"fernflower*.jar", "fernflower*.zip"} {
			matches, _ := filepath.Glob(filepath.Join(dir, pattern))
			if len(matches) > 0 {
				return matches[0]
			}
		}
	}
	return ""
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

func ensureFernflowerClasspath(ctx context.Context, projectDir, javaPath string) (string, error) {
	libDir := filepath.Join(projectDir, "build", "install", "fernflower", "lib")
	if cp := classpathFromLibDir(libDir); cp != "" {
		return cp, nil
	}

	gradlew := filepath.Join(projectDir, "gradlew")
	if runtime.GOOS == "windows" {
		gradlew = filepath.Join(projectDir, "gradlew.bat")
	}
	cmd := exec.CommandContext(ctx, gradlew, "installDist")
	cmd.Dir = projectDir
	cmd.Env = fernflowerBuildEnv(javaPath)
	logBuf := newLimitedBuffer(maxCommandLogBytes)
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Run(); err != nil {
		return "", commandError("构建 Fernflower 失败", err, logBuf.String())
	}

	cp := classpathFromLibDir(libDir)
	if cp == "" {
		return "", fmt.Errorf("构建 Fernflower 后未找到 lib 目录: %s", libDir)
	}
	return cp, nil
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

func classpathFromLibDir(libDir string) string {
	matches, _ := filepath.Glob(filepath.Join(libDir, "*.jar"))
	if len(matches) == 0 {
		return ""
	}
	return strings.Join(matches, string(os.PathListSeparator))
}

func unzipFernflower(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("无法打开 Fernflower 压缩包: %w", err)
	}
	defer r.Close()

	absDest, _ := filepath.Abs(destDir)
	for _, f := range r.File {
		target := filepath.Join(absDest, f.Name)
		if !strings.HasPrefix(target, absDest+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
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

	absOut, _ := filepath.Abs(outputDir)
	os.MkdirAll(absOut, 0755)
	start := time.Now()
	totalJava := 0

	for _, jarPath := range jars {
		jarName := strings.TrimSuffix(filepath.Base(jarPath), filepath.Ext(jarPath))
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

	absJar, _ := filepath.Abs(jarPath)
	absOut, _ := filepath.Abs(outputDir)
	os.MkdirAll(absOut, 0755)
	sourceDir, err := extractJarForFernflower(absJar, filterPkg)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(sourceDir)

	args := append([]string{}, tool.Args...)
	args = append(args, "-log=ERROR", sourceDir, absOut)

	start := time.Now()

	cmd := exec.CommandContext(ctx, javaPath, args...)
	logBuf := newLimitedBuffer(maxCommandLogBytes)
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf

	if err := cmd.Run(); err != nil {
		return nil, commandError("Fernflower 反编译失败", err, logBuf.String())
	}

	elapsed := time.Since(start)

	javaFiles := 0
	filepath.Walk(absOut, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".java") {
			javaFiles++
		}
		return nil
	})

	return &Result{
		OutputDir: absOut,
		JavaFiles: javaFiles,
		Elapsed:   fmt.Sprintf("%.1fs", elapsed.Seconds()),
	}, nil
}

func extractJarForFernflower(jarPath, filterPkg string) (string, error) {
	r, err := zip.OpenReader(jarPath)
	if err != nil {
		return "", fmt.Errorf("无法打开 JAR: %w", err)
	}
	defer r.Close()

	tmpDir, err := os.MkdirTemp("", "jar-fucker-fernflower-*")
	if err != nil {
		return "", fmt.Errorf("无法创建临时目录: %w", err)
	}

	filterPrefix := strings.ReplaceAll(strings.Trim(filterPkg, "."), ".", "/")
	if filterPrefix != "" {
		filterPrefix += "/"
	}

	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if filterPrefix != "" && strings.HasSuffix(f.Name, ".class") && !strings.HasPrefix(f.Name, filterPrefix) {
			continue
		}

		target := filepath.Join(tmpDir, f.Name)
		if !strings.HasPrefix(target, tmpDir+string(os.PathSeparator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", err
		}

		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return "", err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}

	return tmpDir, nil
}
