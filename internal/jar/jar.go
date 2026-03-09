package jar

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type JarFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ScanDir 递归扫描目录内所有 .jar 文件
func ScanDir(dir string) ([]JarFile, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	if fi, err := os.Stat(absDir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("目录不存在: %s", absDir)
	}

	var jars []JarFile
	filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(info.Name()), ".jar") {
			jars = append(jars, JarFile{
				Name: info.Name(),
				Path: path,
				Size: info.Size(),
			})
		}
		return nil
	})
	return jars, nil
}

// ExtractAll 将多个 JAR 包解压到输出目录（每个 JAR 一个子目录）
func ExtractAll(jars []JarFile, outputDir string) (int, error) {
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return 0, err
	}
	os.MkdirAll(absOut, 0755)

	total := 0
	for _, j := range jars {
		jarName := strings.TrimSuffix(j.Name, filepath.Ext(j.Name))
		subDir := filepath.Join(absOut, jarName)
		count, err := Extract(j.Path, subDir)
		if err != nil {
			continue
		}
		total += count
	}
	return total, nil
}

type Info struct {
	Path          string   `json:"path"`
	Name          string   `json:"name"`
	Size          int64    `json:"size"`
	TotalFiles    int      `json:"totalFiles"`
	ClassFiles    int      `json:"classFiles"`
	ResourceFiles int      `json:"resourceFiles"`
	Packages      []string `json:"packages"`
	Manifest      string   `json:"manifest"`
}

type TreeNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Type     string      `json:"type"`
	Size     int64       `json:"size,omitempty"`
	Children []*TreeNode `json:"children,omitempty"`
}

func Analyze(jarPath string) (*Info, error) {
	absPath, err := filepath.Abs(jarPath)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("文件不存在: %s", absPath)
	}

	r, err := zip.OpenReader(absPath)
	if err != nil {
		return nil, fmt.Errorf("无法打开 JAR 文件: %w", err)
	}
	defer r.Close()

	info := &Info{
		Path: absPath,
		Name: filepath.Base(absPath),
		Size: fi.Size(),
	}

	pkgSet := make(map[string]bool)

	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		info.TotalFiles++

		if strings.HasSuffix(f.Name, ".class") {
			info.ClassFiles++
			dir := filepath.Dir(f.Name)
			if dir != "." {
				pkgSet[strings.ReplaceAll(dir, "/", ".")] = true
			}
		} else {
			info.ResourceFiles++
		}

		if f.Name == "META-INF/MANIFEST.MF" {
			rc, err := f.Open()
			if err == nil {
				data, _ := io.ReadAll(rc)
				info.Manifest = string(data)
				rc.Close()
			}
		}
	}

	for pkg := range pkgSet {
		info.Packages = append(info.Packages, pkg)
	}
	sort.Strings(info.Packages)

	return info, nil
}

func Extract(jarPath, outputDir string) (int, error) {
	absJar, err := filepath.Abs(jarPath)
	if err != nil {
		return 0, err
	}

	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return 0, err
	}

	r, err := zip.OpenReader(absJar)
	if err != nil {
		return 0, fmt.Errorf("无法打开 JAR: %w", err)
	}
	defer r.Close()

	count := 0
	for _, f := range r.File {
		target := filepath.Join(absOut, f.Name)

		if !strings.HasPrefix(target, absOut+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return count, err
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}

		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}

		io.Copy(out, rc)
		out.Close()
		rc.Close()
		count++
	}

	return count, nil
}

func BuildTree(rootDir string) (*TreeNode, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	root := &TreeNode{
		Name: filepath.Base(absRoot),
		Path: "",
		Type: "dir",
	}

	err = buildTreeRecursive(absRoot, absRoot, root, 0, 10)
	return root, err
}

func buildTreeRecursive(baseDir, currentDir string, node *TreeNode, depth, maxDepth int) error {
	if depth >= maxDepth {
		return nil
	}

	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return nil
	}

	dirs := make([]os.DirEntry, 0)
	files := make([]os.DirEntry, 0)
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}

	for _, d := range dirs {
		relPath, _ := filepath.Rel(baseDir, filepath.Join(currentDir, d.Name()))
		child := &TreeNode{
			Name: d.Name(),
			Path: filepath.ToSlash(relPath),
			Type: "dir",
		}
		buildTreeRecursive(baseDir, filepath.Join(currentDir, d.Name()), child, depth+1, maxDepth)
		node.Children = append(node.Children, child)
	}

	for _, f := range files {
		relPath, _ := filepath.Rel(baseDir, filepath.Join(currentDir, f.Name()))
		info, _ := f.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		node.Children = append(node.Children, &TreeNode{
			Name: f.Name(),
			Path: filepath.ToSlash(relPath),
			Type: "file",
			Size: size,
		})
	}

	return err
}

type SearchResult struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Content string   `json:"content"`
	Context []string `json:"context"`
}

func Search(dir, keyword string, ignoreCase bool, maxResults int) ([]SearchResult, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	var results []SearchResult
	contextLines := 2

	filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".java") {
			return nil
		}
		if len(results) >= maxResults {
			return filepath.SkipAll
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		relPath, _ := filepath.Rel(absDir, path)

		searchKw := keyword
		if ignoreCase {
			searchKw = strings.ToLower(keyword)
		}

		for i, line := range lines {
			if len(results) >= maxResults {
				break
			}

			target := line
			if ignoreCase {
				target = strings.ToLower(line)
			}

			if strings.Contains(target, searchKw) {
				start := max(0, i-contextLines)
				end := min(len(lines), i+contextLines+1)
				ctx := make([]string, end-start)
				copy(ctx, lines[start:end])

				results = append(results, SearchResult{
					File:    filepath.ToSlash(relPath),
					Line:    i + 1,
					Content: line,
					Context: ctx,
				})
			}
		}

		return nil
	})

	return results, nil
}
