package vscodeaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const workspaceName = "Jar-Fucker-Audit.code-workspace"

type Options struct {
	OutputDir string
	Jars      []string
	SourceDir string
}

type Result struct {
	WorkspaceFile string   `json:"workspaceFile"`
	SourceRoots   []string `json:"sourceRoots"`
	Libraries     []string `json:"libraries"`
	Opened        bool     `json:"opened"`
	OpenError     string   `json:"openError,omitempty"`
}

type workspaceFile struct {
	Folders  []workspaceFolder `json:"folders"`
	Settings workspaceSettings `json:"settings"`
}

type workspaceFolder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type workspaceSettings struct {
	SourcePaths         []string        `json:"java.project.sourcePaths"`
	ReferencedLibraries []string        `json:"java.project.referencedLibraries"`
	OutputPath          string          `json:"java.project.outputPath"`
	UpdateBuildConfig   string          `json:"java.configuration.updateBuildConfiguration"`
	Exclude             map[string]bool `json:"files.exclude"`
	WatcherExclude      map[string]bool `json:"files.watcherExclude"`
	SearchExclude       map[string]bool `json:"search.exclude"`
}

func Create(opts Options) (*Result, error) {
	outputDir, err := existingDir(opts.OutputDir, "反编译输出目录")
	if err != nil {
		return nil, err
	}
	jars, err := validJars(opts.Jars)
	if err != nil {
		return nil, err
	}
	if len(jars) == 0 {
		return nil, fmt.Errorf("没有可添加到 VS Code 的 JAR")
	}

	libraries := append([]string{}, jars...)
	if strings.TrimSpace(opts.SourceDir) != "" {
		if sourceDir, sourceErr := existingDir(opts.SourceDir, "JAR 源目录"); sourceErr == nil {
			if scanned, scanErr := scanJars(sourceDir, 10000); scanErr == nil {
				libraries = append(libraries, scanned...)
			}
		}
	}
	libraries = uniqueSorted(libraries)
	sourceRoots := detectSourceRoots(outputDir, jars)

	content := workspaceFile{
		Folders: []workspaceFolder{{Name: "Jar-Fucker 反编译源码", Path: "."}},
		Settings: workspaceSettings{
			SourcePaths:         sourceRoots,
			ReferencedLibraries: slashPaths(libraries),
			OutputPath:          ".jar-fucker-vscode/bin",
			UpdateBuildConfig:   "automatic",
			Exclude:             map[string]bool{".jar-fucker-vscode": true},
			WatcherExclude:      map[string]bool{"**/.jar-fucker-vscode/**": true},
			SearchExclude:       map[string]bool{"**/.jar-fucker-vscode/**": true},
		},
	}
	data, err := json.MarshalIndent(content, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("生成 VS Code 工作区失败: %w", err)
	}
	data = append(data, '\n')
	workspacePath := filepath.Join(outputDir, workspaceName)
	if err := writeAtomic(workspacePath, data); err != nil {
		return nil, fmt.Errorf("写入 VS Code 工作区失败: %w", err)
	}
	return &Result{
		WorkspaceFile: workspacePath,
		SourceRoots:   sourceRoots,
		Libraries:     libraries,
	}, nil
}

func Open(workspacePath string) error {
	codePath, err := findCode()
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" && strings.EqualFold(filepath.Ext(codePath), ".cmd") {
		commandLine := fmt.Sprintf("\"%s\" --reuse-window \"%s\"", codePath, workspacePath)
		cmd = exec.Command("cmd.exe", "/d", "/s", "/c", commandLine)
	} else {
		cmd = exec.Command(codePath, "--reuse-window", workspacePath)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 VS Code 失败: %w", err)
	}
	return nil
}

func findCode() (string, error) {
	for _, name := range []string{"code", "code-insiders"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	if runtime.GOOS == "windows" {
		for _, candidate := range []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Microsoft VS Code", "Code.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft VS Code", "Code.exe"),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("没有找到 VS Code 命令行，请在 VS Code 中安装 shell command 'code'，或手动打开生成的工作区文件")
}

func existingDir(input, label string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(input))
	if err != nil {
		return "", fmt.Errorf("%s无效: %w", label, err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s不存在: %s", label, abs)
	}
	return abs, nil
}

func validJars(inputs []string) ([]string, error) {
	var jars []string
	for _, input := range inputs {
		abs, err := filepath.Abs(strings.TrimSpace(input))
		if err != nil {
			return nil, fmt.Errorf("JAR 路径无效: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() || !strings.EqualFold(filepath.Ext(abs), ".jar") {
			return nil, fmt.Errorf("JAR 文件不存在或格式不正确: %s", abs)
		}
		jars = append(jars, abs)
	}
	return uniquePaths(jars), nil
}

func scanJars(root string, limit int) ([]string, error) {
	var jars []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(info.Name()), ".jar") {
			jars = append(jars, path)
			if len(jars) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return jars, err
}

func detectSourceRoots(outputDir string, jars []string) []string {
	if len(jars) <= 1 {
		return []string{"."}
	}
	used := map[string]int{}
	var roots []string
	for _, jarPath := range jars {
		name := strings.TrimSuffix(filepath.Base(jarPath), filepath.Ext(jarPath))
		key := strings.ToLower(name)
		used[key]++
		if used[key] > 1 {
			name = fmt.Sprintf("%s-%d", name, used[key])
		}
		if info, err := os.Stat(filepath.Join(outputDir, name)); err == nil && info.IsDir() {
			roots = append(roots, "./"+filepath.ToSlash(name))
		}
	}
	if len(roots) == 0 {
		return []string{"."}
	}
	return roots
}

func slashPaths(paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = filepath.ToSlash(path)
	}
	return out
}

func uniquePaths(paths []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(clean)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

func uniqueSorted(paths []string) []string {
	seen := map[string]string{}
	for _, path := range paths {
		clean := filepath.Clean(path)
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(clean)
		}
		seen[key] = clean
	}
	out := make([]string, 0, len(seen))
	for _, path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vscode-workspace-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
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
	_ = os.Remove(path)
	return os.Rename(tmpPath, path)
}
