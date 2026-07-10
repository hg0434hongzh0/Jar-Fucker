package jar

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/safezip"
)

type JarFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ScanDir 递归扫描目录内所有 .jar 文件
func ScanDir(dir string) ([]JarFile, error) {
	return ScanDirContext(context.Background(), dir)
}

func ScanDirContext(ctx context.Context, dir string) ([]JarFile, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	fi, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("无法读取目录 %s: %w", absDir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("目录不存在: %s", absDir)
	}

	var jars []JarFile
	err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
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
	if err != nil {
		return jars, fmt.Errorf("扫描目录 %s: %w", absDir, err)
	}
	return jars, nil
}

// ExtractAll 将多个 JAR 包解压到输出目录（每个 JAR 一个子目录）
func ExtractAll(jars []JarFile, outputDir string) (int, error) {
	return ExtractAllContext(context.Background(), jars, outputDir)
}

func ExtractAllContext(ctx context.Context, jars []JarFile, outputDir string) (int, error) {
	absOut, err := filepath.Abs(outputDir)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(absOut, 0755); err != nil {
		return 0, fmt.Errorf("创建输出目录 %s: %w", absOut, err)
	}

	limits := safezip.DefaultLimits()
	usedNames := make(map[string]struct{}, len(jars))
	totalFiles := 0
	totalEntries := 0
	var totalBytes int64
	for _, j := range jars {
		if err := ctx.Err(); err != nil {
			return totalFiles, err
		}
		jarName, err := uniqueExtractionDirName(j.Path, usedNames)
		if err != nil {
			return totalFiles, err
		}
		if totalEntries >= limits.MaxEntries || totalBytes >= limits.MaxTotalBytes {
			return totalFiles, fmt.Errorf("%w: batch extraction budget exhausted", safezip.ErrLimitExceeded)
		}

		subDir := filepath.Join(absOut, jarName)
		result, err := safezip.ExtractFileWithOptions(j.Path, subDir, safezip.Options{
			Context: ctx,
			Limits: safezip.Limits{
				MaxEntries:          limits.MaxEntries - totalEntries,
				MaxFileBytes:        limits.MaxFileBytes,
				MaxTotalBytes:       limits.MaxTotalBytes - totalBytes,
				MaxCompressionRatio: limits.MaxCompressionRatio,
			},
		})
		totalFiles += result.Files
		totalEntries += result.Entries
		totalBytes += result.Bytes
		if err != nil {
			return totalFiles, fmt.Errorf("提取 JAR %s: %w", filepath.Base(j.Path), err)
		}
	}
	return totalFiles, nil
}

func uniqueExtractionDirName(jarPath string, used map[string]struct{}) (string, error) {
	if strings.TrimSpace(jarPath) == "" {
		return "", fmt.Errorf("JAR 路径不能为空")
	}
	fileName := filepath.Base(filepath.Clean(jarPath))
	if fileName == "." || fileName == string(os.PathSeparator) || fileName == "" {
		return "", fmt.Errorf("无效 JAR 路径: %s", jarPath)
	}

	base := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	if base == "" {
		base = "jar"
	}
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		key := strings.ToLower(candidate)
		if _, exists := used[key]; exists {
			continue
		}
		used[key] = struct{}{}
		return candidate, nil
	}
}

type Info struct {
	Path              string   `json:"path"`
	Name              string   `json:"name"`
	Size              int64    `json:"size"`
	TotalFiles        int      `json:"totalFiles"`
	ClassFiles        int      `json:"classFiles"`
	ResourceFiles     int      `json:"resourceFiles"`
	PackageCount      int      `json:"packageCount"`
	Packages          []string `json:"packages"`
	PackagesTruncated bool     `json:"packagesTruncated,omitempty"`
	Manifest          string   `json:"manifest"`
}

type TreeNode struct {
	Name          string      `json:"name"`
	Path          string      `json:"path"`
	Type          string      `json:"type"`
	Size          int64       `json:"size,omitempty"`
	HasChildren   bool        `json:"hasChildren,omitempty"`
	Children      []*TreeNode `json:"children,omitempty"`
	TotalChildren int         `json:"totalChildren,omitempty"`
	Offset        int         `json:"offset,omitempty"`
	Limit         int         `json:"limit,omitempty"`
	HasMore       bool        `json:"hasMore,omitempty"`
}

const (
	maxManifestBytes    = 1 << 20
	MaxAnalyzedPackages = 250
	DefaultTreePageSize = 300
	MaxTreePageSize     = 500
)

func Analyze(jarPath string) (result *Info, err error) {
	return AnalyzeContext(context.Background(), jarPath)
}

func AnalyzeContext(ctx context.Context, jarPath string) (result *Info, err error) {
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
		if r != nil {
			err = errors.Join(err, r.Close())
		}
		return nil, fmt.Errorf("无法打开 JAR 文件: %w", err)
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("关闭 JAR 文件: %w", closeErr))
		}
	}()

	info := &Info{
		Path: absPath,
		Name: filepath.Base(absPath),
		Size: fi.Size(),
	}

	pkgSet := make(map[string]bool)

	for _, f := range r.File {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		info.TotalFiles++

		if strings.HasSuffix(f.Name, ".class") {
			info.ClassFiles++
			dir := pathpkg.Dir(strings.ReplaceAll(f.Name, `\`, "/"))
			if dir != "." {
				pkgSet[strings.ReplaceAll(dir, "/", ".")] = true
			}
		} else {
			info.ResourceFiles++
		}

		if f.Name == "META-INF/MANIFEST.MF" {
			rc, openErr := f.Open()
			if openErr != nil {
				return nil, fmt.Errorf("读取 MANIFEST.MF: %w", openErr)
			}
			data, readErr := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
			closeErr := rc.Close()
			if joinedErr := errors.Join(readErr, closeErr); joinedErr != nil {
				return nil, fmt.Errorf("读取 MANIFEST.MF: %w", joinedErr)
			}
			if len(data) > maxManifestBytes {
				return nil, fmt.Errorf("%w: MANIFEST.MF 超过 %d 字节", safezip.ErrLimitExceeded, maxManifestBytes)
			}
			info.Manifest = string(data)
		}
	}

	for pkg := range pkgSet {
		info.Packages = append(info.Packages, pkg)
	}
	sort.Strings(info.Packages)
	info.PackageCount = len(info.Packages)
	if len(info.Packages) > MaxAnalyzedPackages {
		info.Packages = info.Packages[:MaxAnalyzedPackages]
		info.PackagesTruncated = true
	}

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

	result, err := safezip.ExtractFile(absJar, absOut, safezip.DefaultLimits())
	return result.Files, err
}

func BuildTree(rootDir string) (*TreeNode, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("无法读取根目录 %s: %w", absRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("根路径不是目录: %s", absRoot)
	}

	root := &TreeNode{
		Name: filepath.Base(absRoot),
		Path: "",
		Type: "dir",
	}

	err = buildTreeRecursive(absRoot, absRoot, root, 0, 10)
	return root, err
}

// ListTree returns one bounded page of a single directory. It is intended for
// interactive tree browsing so large decompilations never require one giant
// JSON response or DOM tree.
func ListTree(rootDir, relativePath string, offset, limit int) (*TreeNode, error) {
	return ListTreeContext(context.Background(), rootDir, relativePath, offset, limit)
}

func ListTreeContext(ctx context.Context, rootDir, relativePath string, offset, limit int) (*TreeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, fmt.Errorf("offset 不能小于 0")
	}
	if limit <= 0 {
		limit = DefaultTreePageSize
	}
	if limit > MaxTreePageSize {
		limit = MaxTreePageSize
	}

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("无法读取根目录 %s: %w", absRoot, err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("根路径不是目录: %s", absRoot)
	}

	cleanRelative, err := cleanTreeRelativePath(relativePath)
	if err != nil {
		return nil, err
	}
	target := absRoot
	if cleanRelative != "" {
		target = filepath.Join(absRoot, cleanRelative)
	}
	if err := ensureTreeTargetContained(absRoot, target); err != nil {
		return nil, err
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("无法读取目录 %s: %w", target, err)
	}
	if !targetInfo.IsDir() {
		return nil, fmt.Errorf("树节点不是目录: %s", target)
	}

	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, fmt.Errorf("无法读取目录 %s: %w", target, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ordered := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Type()&os.ModeSymlink == 0 && entry.IsDir() {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Type()&os.ModeSymlink == 0 && !entry.IsDir() {
			ordered = append(ordered, entry)
		}
	}

	if offset > len(ordered) {
		offset = len(ordered)
	}
	end := offset + min(limit, len(ordered)-offset)
	page := &TreeNode{
		Name:          filepath.Base(target),
		Path:          filepath.ToSlash(cleanRelative),
		Type:          "dir",
		TotalChildren: len(ordered),
		Offset:        offset,
		Limit:         limit,
		HasMore:       end < len(ordered),
	}
	for _, entry := range ordered[offset:end] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		childPath := filepath.Join(target, entry.Name())
		relPath, err := filepath.Rel(absRoot, childPath)
		if err != nil {
			return nil, err
		}
		child := &TreeNode{
			Name: entry.Name(),
			Path: filepath.ToSlash(relPath),
			Type: "file",
		}
		if entry.IsDir() {
			child.Type = "dir"
			child.HasChildren, err = directoryHasChildren(childPath)
			if err != nil {
				return nil, err
			}
		} else {
			info, err := entry.Info()
			if err != nil {
				return nil, err
			}
			child.Size = info.Size()
		}
		page.Children = append(page.Children, child)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

func directoryHasChildren(dir string) (hasChildren bool, err error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, f.Close())
	}()
	entries, err := f.ReadDir(1)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return len(entries) > 0, err
}

func cleanTreeRelativePath(relativePath string) (string, error) {
	if strings.ContainsRune(relativePath, '\x00') {
		return "", fmt.Errorf("目录路径包含无效字符")
	}
	if strings.TrimSpace(relativePath) == "" {
		return "", nil
	}
	native := filepath.FromSlash(strings.ReplaceAll(relativePath, `\`, "/"))
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" {
		return "", fmt.Errorf("目录路径必须相对于结果根目录")
	}
	cleaned := filepath.Clean(native)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("目录路径越出结果根目录")
	}
	return cleaned, nil
}

func ensureTreeTargetContained(root, target string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return err
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("目录路径越出结果根目录")
	}
	return nil
}

func buildTreeRecursive(baseDir, currentDir string, node *TreeNode, depth, maxDepth int) error {
	if depth >= maxDepth {
		return nil
	}

	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return err
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
		childPath := filepath.Join(currentDir, d.Name())
		relPath, err := filepath.Rel(baseDir, childPath)
		if err != nil {
			return err
		}
		child := &TreeNode{
			Name: d.Name(),
			Path: filepath.ToSlash(relPath),
			Type: "dir",
		}
		if err := buildTreeRecursive(baseDir, childPath, child, depth+1, maxDepth); err != nil {
			return err
		}
		node.Children = append(node.Children, child)
	}

	for _, f := range files {
		filePath := filepath.Join(currentDir, f.Name())
		relPath, err := filepath.Rel(baseDir, filePath)
		if err != nil {
			return err
		}
		info, err := f.Info()
		if err != nil {
			return err
		}
		node.Children = append(node.Children, &TreeNode{
			Name: f.Name(),
			Path: filepath.ToSlash(relPath),
			Type: "file",
			Size: info.Size(),
		})
	}

	return nil
}

type SearchResult struct {
	File    string   `json:"file"`
	Line    int      `json:"line"`
	Content string   `json:"content"`
	Context []string `json:"context,omitempty"`
}

const MaxSearchResults = 1000

const MaxSearchFileBytes int64 = 16 << 20

const MaxSearchSnippetRunes = 500

func Search(dir, keyword string, ignoreCase bool, maxResults int) ([]SearchResult, error) {
	return SearchContext(context.Background(), dir, keyword, ignoreCase, maxResults)
}

func SearchContext(ctx context.Context, dir, keyword string, ignoreCase bool, maxResults int) ([]SearchResult, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, fmt.Errorf("搜索关键词不能为空")
	}
	if maxResults <= 0 {
		return nil, fmt.Errorf("maxResults 必须大于 0")
	}
	if maxResults > MaxSearchResults {
		maxResults = MaxSearchResults
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("无法读取搜索目录 %s: %w", absDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("搜索路径不是目录: %s", absDir)
	}

	var results []SearchResult
	searchKw := keyword
	if ignoreCase {
		searchKw = strings.ToLower(keyword)
	}

	err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".java") {
			return nil
		}
		if info.Size() > MaxSearchFileBytes {
			return nil
		}
		if len(results) >= maxResults {
			return filepath.SkipAll
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		lines := strings.Split(string(data), "\n")
		relPath, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}

		for i, line := range lines {
			if len(results) >= maxResults {
				break
			}

			target := line
			if ignoreCase {
				target = strings.ToLower(line)
			}

			if matchByte := strings.Index(target, searchKw); matchByte >= 0 {
				results = append(results, SearchResult{
					File:    filepath.ToSlash(relPath),
					Line:    i + 1,
					Content: summarizeSearchLine(line, matchByte),
				})
			}
		}

		return nil
	})
	if err != nil {
		return results, fmt.Errorf("搜索目录 %s: %w", absDir, err)
	}

	return results, nil
}

func summarizeSearchLine(line string, matchByte int) string {
	lineRunes := []rune(line)
	if len(lineRunes) <= MaxSearchSnippetRunes {
		return line
	}
	matchByte = min(max(matchByte, 0), len(line))
	matchRune := utf8.RuneCountInString(line[:matchByte])
	const marker = "..."
	window := MaxSearchSnippetRunes - 2*len([]rune(marker))
	start := max(0, matchRune-window/2)
	start = min(start, len(lineRunes)-window)
	end := start + window

	var summary strings.Builder
	if start > 0 {
		summary.WriteString(marker)
	}
	summary.WriteString(string(lineRunes[start:end]))
	if end < len(lineRunes) {
		summary.WriteString(marker)
	}
	return summary.String()
}
