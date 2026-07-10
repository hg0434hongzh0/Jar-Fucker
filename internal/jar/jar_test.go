package jar

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/safezip"
)

func writeTestJar(t *testing.T, jarPath, entryName, content string) {
	writeTestJarEntries(t, jarPath, map[string]string{entryName: content})
}

func writeTestJarEntries(t *testing.T, jarPath string, entries map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(jarPath), 0755); err != nil {
		t.Fatalf("create JAR parent: %v", err)
	}

	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	for entryName, content := range entries {
		w, err := zw.Create(entryName)
		if err != nil {
			t.Fatalf("create JAR entry: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write JAR entry: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close JAR: %v", err)
	}
	if err := os.WriteFile(jarPath, data.Bytes(), 0600); err != nil {
		t.Fatalf("write JAR: %v", err)
	}
}

func TestExtractAllUsesPathBasenamesAndUniqueDirectories(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "one", "same.jar")
	second := filepath.Join(root, "two", "same.jar")
	collidingSuffix := filepath.Join(root, "three", "same-2.jar")
	writeTestJar(t, first, "first.txt", "first")
	writeTestJar(t, collidingSuffix, "suffix.txt", "suffix")
	writeTestJar(t, second, "second.txt", "second")

	output := filepath.Join(root, "output")
	count, err := ExtractAll([]JarFile{
		{Name: `..\..\attacker-controlled.jar`, Path: first},
		{Name: "ignored.jar", Path: collidingSuffix},
		{Name: "also-ignored.jar", Path: second},
	}, output)
	if err != nil {
		t.Fatalf("ExtractAll() error = %v", err)
	}
	if count != 3 {
		t.Fatalf("ExtractAll() count = %d, want 3", count)
	}

	checks := map[string]string{
		filepath.Join(output, "same", "first.txt"):    "first",
		filepath.Join(output, "same-2", "suffix.txt"): "suffix",
		filepath.Join(output, "same-3", "second.txt"): "second",
	}
	for path, want := range checks {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("%s content = %q, want %q", path, data, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "attacker-controlled")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("client-supplied JarFile.Name affected output: %v", err)
	}
}

func TestExtractAllPropagatesArchiveErrors(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid.jar")
	writeTestJar(t, valid, "valid.txt", "ok")

	count, err := ExtractAll([]JarFile{
		{Path: valid},
		{Path: filepath.Join(root, "missing.jar")},
	}, filepath.Join(root, "output"))
	if err == nil {
		t.Fatal("ExtractAll() error = nil, want archive error")
	}
	if count != 1 {
		t.Fatalf("ExtractAll() count = %d, want completed count 1", count)
	}
}

func TestAnalyzeLimitsManifest(t *testing.T) {
	jarPath := filepath.Join(t.TempDir(), "manifest.jar")
	writeTestJar(t, jarPath, "META-INF/MANIFEST.MF", strings.Repeat("x", maxManifestBytes+1))

	_, err := Analyze(jarPath)
	if !errors.Is(err, safezip.ErrLimitExceeded) {
		t.Fatalf("Analyze() error = %v, want ErrLimitExceeded", err)
	}
}

func TestAnalyzeCapsPackageResponse(t *testing.T) {
	jarPath := filepath.Join(t.TempDir(), "packages.jar")
	entries := make(map[string]string, MaxAnalyzedPackages+17)
	for i := 0; i < MaxAnalyzedPackages+17; i++ {
		entries[fmt.Sprintf("pkg/%03d/Type.class", i)] = "class"
	}
	writeTestJarEntries(t, jarPath, entries)

	info, err := Analyze(jarPath)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if info.PackageCount != MaxAnalyzedPackages+17 {
		t.Fatalf("PackageCount = %d, want %d", info.PackageCount, MaxAnalyzedPackages+17)
	}
	if len(info.Packages) != MaxAnalyzedPackages {
		t.Fatalf("len(Packages) = %d, want %d", len(info.Packages), MaxAnalyzedPackages)
	}
	if !info.PackagesTruncated {
		t.Fatal("PackagesTruncated = false, want true")
	}
	if info.Packages[0] != "pkg.000" || info.Packages[len(info.Packages)-1] != "pkg.249" {
		t.Fatalf("package page bounds = %q ... %q", info.Packages[0], info.Packages[len(info.Packages)-1])
	}
}

func TestBuildTreeRejectsInvalidRoots(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, err := BuildTree(filepath.Join(t.TempDir(), "missing"))
		if err == nil || !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("BuildTree() error = %v, want os.ErrNotExist", err)
		}
	})

	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file.txt")
		if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildTree(path); err == nil {
			t.Fatal("BuildTree() accepted a file root")
		}
	})
}

func TestListTreePaginatesOneDirectory(t *testing.T) {
	root := t.TempDir()
	emptyDir := filepath.Join(root, "a-empty")
	fullDir := filepath.Join(root, "b-full")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fullDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fullDir, "nested.java"), []byte("class Nested {}"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.java", "z.java"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}

	first, err := ListTree(root, "", 0, 2)
	if err != nil {
		t.Fatalf("ListTree(first) error = %v", err)
	}
	if first.TotalChildren != 4 || first.Offset != 0 || first.Limit != 2 || !first.HasMore {
		t.Fatalf("first page metadata = %+v", first)
	}
	if len(first.Children) != 2 || first.Children[0].Name != "a-empty" || first.Children[1].Name != "b-full" {
		t.Fatalf("first page children = %+v", first.Children)
	}
	if first.Children[0].HasChildren || !first.Children[1].HasChildren {
		t.Fatalf("directory child flags = %+v", first.Children)
	}

	second, err := ListTree(root, "", 2, 2)
	if err != nil {
		t.Fatalf("ListTree(second) error = %v", err)
	}
	if second.HasMore || len(second.Children) != 2 || second.Children[0].Name != "a.java" || second.Children[1].Name != "z.java" {
		t.Fatalf("second page = %+v", second)
	}

	nested, err := ListTree(root, "b-full", 0, 10)
	if err != nil {
		t.Fatalf("ListTree(nested) error = %v", err)
	}
	if nested.Path != "b-full" || nested.TotalChildren != 1 || nested.Children[0].Name != "nested.java" {
		t.Fatalf("nested page = %+v", nested)
	}
}

func TestListTreeBoundsLimitOffsetAndPath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.java"), []byte("class File {}"), 0600); err != nil {
		t.Fatal(err)
	}

	page, err := ListTree(root, "", math.MaxInt, MaxTreePageSize+100)
	if err != nil {
		t.Fatalf("ListTree() error = %v", err)
	}
	if page.Offset != 1 || page.Limit != MaxTreePageSize || len(page.Children) != 0 || page.HasMore {
		t.Fatalf("bounded page = %+v", page)
	}
	if _, err := ListTree(root, "../outside", 0, 1); err == nil {
		t.Fatal("ListTree() accepted a path outside root")
	}
}

func TestListTreeContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ListTreeContext(ctx, t.TempDir(), "", 0, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListTreeContext() error = %v, want context.Canceled", err)
	}
}

func TestSearchValidatesInputAndCapsResults(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("needle\n", MaxSearchResults+50)
	if err := os.WriteFile(filepath.Join(root, "Source.java"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	for _, keyword := range []string{"", " \t\r\n"} {
		if _, err := Search(root, keyword, false, 10); err == nil {
			t.Fatalf("Search() accepted empty keyword %q", keyword)
		}
	}
	if _, err := Search(root, "needle", false, 0); err == nil {
		t.Fatal("Search() accepted non-positive maxResults")
	}

	results, err := Search(root, "needle", false, MaxSearchResults*10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != MaxSearchResults {
		t.Fatalf("Search() returned %d results, want cap %d", len(results), MaxSearchResults)
	}
	if results[len(results)-1].Line != MaxSearchResults {
		t.Fatalf("last result line = %d, want %d", results[len(results)-1].Line, MaxSearchResults)
	}
}

func TestSearchReturnsRootErrors(t *testing.T) {
	_, err := Search(filepath.Join(t.TempDir(), "missing"), "needle", false, 10)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Search() error = %v, want os.ErrNotExist", err)
	}
}

func TestSearchSummarizesLongMatchingLines(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("界", 800) + "needle" + strings.Repeat("尾", 800)
	if err := os.WriteFile(filepath.Join(root, "Long.java"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}

	results, err := Search(root, "needle", false, 10)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if got := utf8.RuneCountInString(results[0].Content); got > MaxSearchSnippetRunes {
		t.Fatalf("snippet length = %d, want <= %d", got, MaxSearchSnippetRunes)
	}
	if !strings.Contains(results[0].Content, "needle") {
		t.Fatalf("snippet omitted match: %q", results[0].Content)
	}
	if len(results[0].Context) != 0 {
		t.Fatalf("search context unexpectedly increases payload: %#v", results[0].Context)
	}
	encoded, err := json.Marshal(results[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"context"`)) {
		t.Fatalf("empty context was serialized: %s", encoded)
	}
}
