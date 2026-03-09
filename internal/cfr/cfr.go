package cfr

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	Version    = "0.152"
	DownloadURL = "https://github.com/leibnitz27/cfr/releases/download/" + Version + "/cfr-" + Version + ".jar"
)

type Result struct {
	OutputDir string `json:"outputDir"`
	JavaFiles int    `json:"javaFiles"`
	Elapsed   string `json:"elapsed"`
}

func DefaultCFRPath() string {
	exe, _ := os.Executable()
	return filepath.Join(filepath.Dir(exe), "cfr", "cfr-"+Version+".jar")
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
	if cfrPath == "" {
		cfrPath = DefaultCFRPath()
	}

	if _, err := os.Stat(cfrPath); err == nil {
		return nil
	}

	dir := filepath.Dir(cfrPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("无法创建 CFR 目录: %w", err)
	}

	resp, err := http.Get(DownloadURL)
	if err != nil {
		return fmt.Errorf("下载 CFR 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("下载 CFR 失败: HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(cfrPath)
	if err != nil {
		return fmt.Errorf("无法保存 CFR: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

// DecompileMultiple 批量反编译多个 JAR，每个 JAR 输出到独立子目录
func DecompileMultiple(jars []string, outputDir, javaPath, cfrPath string) (*Result, error) {
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
	if err := EnsureCFR(cfrPath); err != nil {
		return nil, err
	}

	absOut, _ := filepath.Abs(outputDir)
	os.MkdirAll(absOut, 0755)
	start := time.Now()
	totalJava := 0

	for _, jarPath := range jars {
		jarName := strings.TrimSuffix(filepath.Base(jarPath), filepath.Ext(jarPath))
		subDir := filepath.Join(absOut, jarName)

		result, err := Decompile(jarPath, subDir, javaPath, cfrPath, "")
		if err != nil {
			continue
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

	if err := EnsureCFR(cfrPath); err != nil {
		return nil, err
	}

	absJar, _ := filepath.Abs(jarPath)
	absOut, _ := filepath.Abs(outputDir)
	os.MkdirAll(absOut, 0755)

	args := []string{
		"-jar", cfrPath,
		absJar,
		"--outputdir", absOut,
		"--silent", "true",
	}
	if filterPkg != "" {
		args = append(args, "--jarfilter", strings.ReplaceAll(filterPkg, ".", "/"))
	}

	start := time.Now()

	cmd := exec.Command(javaPath, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("CFR 反编译失败: %w", err)
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
