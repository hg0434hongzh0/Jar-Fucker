package cfr

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseJavaMajor(t *testing.T) {
	tests := []struct {
		output string
		want   int
	}{
		{`java version "21.0.6" 2025-01-21 LTS`, 21},
		{`openjdk version "17.0.12" 2024-07-16`, 17},
		{`java version "1.8.0_431"`, 8},
		{`openjdk 23.0.1 2024-10-15`, 23},
	}
	for _, tt := range tests {
		got, err := parseJavaMajor(tt.output)
		if err != nil {
			t.Fatalf("parseJavaMajor(%q): %v", tt.output, err)
		}
		if got != tt.want {
			t.Errorf("parseJavaMajor(%q) = %d, want %d", tt.output, got, tt.want)
		}
	}
	if _, err := parseJavaMajor("not a java version"); err == nil {
		t.Fatal("parseJavaMajor accepted invalid output")
	}
}

func TestLimitedBufferKeepsTail(t *testing.T) {
	buf := newLimitedBuffer(5)
	for _, value := range []string{"abc", "def", "gh"} {
		if _, err := buf.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if got := buf.String(); got != "defgh" {
		t.Fatalf("buffer = %q, want %q", got, "defgh")
	}
}

func TestLimitedBufferSupportsConcurrentCommandOutput(t *testing.T) {
	buf := newLimitedBuffer(128)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := buf.Write([]byte("command output\n")); err != nil {
					t.Errorf("Write() error = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := len(buf.String()); got > 128 {
		t.Fatalf("buffer length = %d, want <= 128", got)
	}
}

func TestFernflowerProgressWriterParsesAndThrottlesClassLines(t *testing.T) {
	var updates []Progress
	w := newFernflowerProgressWriter(1000, func(update Progress) {
		updates = append(updates, update)
	})
	for i := 1; i <= 25; i++ {
		line := fmt.Sprintf("INFO:  Decompiling class demo/Class%d\nINFO:  ... done\n", i)
		mid := len(line) / 2
		if _, err := w.Write([]byte(line[:mid])); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(line[mid:])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write([]byte("INFO:  Decompiling class demo/Class30\n")); err != nil {
		t.Fatal(err)
	}
	w.Flush()

	wantCompleted := []int{1, 10, 20}
	if len(updates) != len(wantCompleted) {
		t.Fatalf("updates = %+v, want completions %v", updates, wantCompleted)
	}
	for i, want := range wantCompleted {
		if updates[i].Phase != ProgressDecompiling || updates[i].CompletedUnits != want || updates[i].ClassFiles != 1000 {
			t.Fatalf("updates[%d] = %+v", i, updates[i])
		}
	}
	if updates[0].Detail != "demo/Class1" {
		t.Fatalf("first detail = %q", updates[0].Detail)
	}
}

func TestFernflowerProgressWriterCapsUpdatesAroundHundred(t *testing.T) {
	var updates []Progress
	w := newFernflowerProgressWriter(199, func(update Progress) {
		updates = append(updates, update)
	})
	for i := 1; i <= 199; i++ {
		_, _ = fmt.Fprintf(w, "INFO:  Decompiling class demo/Class%d\nINFO:  ... done\n", i)
	}
	if len(updates) != 100 {
		t.Fatalf("updates = %d, want 100", len(updates))
	}
	if updates[0].CompletedUnits != 1 || updates[len(updates)-1].CompletedUnits != 198 {
		t.Fatalf("unexpected completion range: first=%d last=%d", updates[0].CompletedUnits, updates[len(updates)-1].CompletedUnits)
	}
}

func TestValidateFernflowerJar(t *testing.T) {
	valid := writeZip(t, map[string]string{
		strings.ReplaceAll(fernflowerMainClass, ".", "/") + ".class": "bytecode",
	})
	if err := validateFernflowerJar(valid); err != nil {
		t.Fatalf("valid Fernflower JAR rejected: %v", err)
	}

	invalid := writeZip(t, map[string]string{"demo/Main.class": "bytecode"})
	if err := validateFernflowerJar(invalid); err == nil {
		t.Fatal("unrelated JAR accepted as Fernflower")
	}
}

func TestBundledFernflowerAsset(t *testing.T) {
	if len(bundledFernflowerJar) < 100_000 {
		t.Fatalf("bundled Fernflower asset is unexpectedly small: %d bytes", len(bundledFernflowerJar))
	}
	path := filepath.Join(t.TempDir(), "fernflower.jar")
	if err := os.WriteFile(path, bundledFernflowerJar, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateFernflowerJar(path); err != nil {
		t.Fatalf("bundled Fernflower is invalid: %v", err)
	}
}

func TestExtractJarForFernflowerFiltersClasses(t *testing.T) {
	jarPath := writeZip(t, map[string]string{
		"com/acme/App.class":   "selected",
		"org/example/No.class": "filtered",
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\n",
	})
	out, classFiles, err := extractJarForFernflowerWithStats(context.Background(), jarPath, "com.acme")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(out)
	if classFiles != 1 {
		t.Fatalf("selected class count = %d, want 1", classFiles)
	}

	if _, err := os.Stat(filepath.Join(out, "com", "acme", "App.class")); err != nil {
		t.Fatalf("selected class missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "org", "example", "No.class")); !os.IsNotExist(err) {
		t.Fatalf("filtered class exists or returned unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "META-INF", "MANIFEST.MF")); err != nil {
		t.Fatalf("resource missing: %v", err)
	}
}

func TestExtractJarForFernflowerCountsNestedArchivesLikeFernflower(t *testing.T) {
	deepJar := writeZip(t, map[string]string{"deep/NotScanned.class": "bytecode"})
	deepData, err := os.ReadFile(deepJar)
	if err != nil {
		t.Fatal(err)
	}
	innerJar := writeZip(t, map[string]string{
		"lib/Dependency.class":                      "bytecode",
		"META-INF/versions/21/lib/Dependency.class": "bytecode",
		"lib/deep.jar":                              string(deepData),
	})
	innerData, err := os.ReadFile(innerJar)
	if err != nil {
		t.Fatal(err)
	}
	innerZip := writeZip(t, map[string]string{"ziplib/FromZip.class": "bytecode"})
	zipData, err := os.ReadFile(innerZip)
	if err != nil {
		t.Fatal(err)
	}
	jarPath := writeZip(t, map[string]string{
		"com/acme/App.class":             "bytecode",
		"com/acme/NotAClass.CLASS":       "resource",
		"BOOT-INF/lib/dependency.jar":    string(innerData),
		"BOOT-INF/lib/dependency.zip":    string(zipData),
		"BOOT-INF/lib/ignored-upper.JAR": string(innerData),
	})

	out, classFiles, err := extractJarForFernflowerWithStats(context.Background(), jarPath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(out)
	if classFiles != 3 {
		t.Fatalf("Fernflower class-file upper bound = %d, want 3", classFiles)
	}
}

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.jar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
