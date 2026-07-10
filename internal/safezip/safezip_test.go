package safezip

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractHonorsCanceledContextBeforeWriting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "output")
	_, err := ExtractWithOptions(testZipReader(t, testEntry{name: "file.txt", data: "content"}), dest, Options{Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExtractWithOptions() error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("destination was created after cancellation: %v", err)
	}
}

type testEntry struct {
	name   string
	data   string
	method uint16
	mode   os.FileMode
}

func testZipData(t *testing.T, entries ...testEntry) []byte {
	t.Helper()

	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	for _, entry := range entries {
		method := entry.method
		if method == 0 {
			method = zip.Deflate
		}
		header := &zip.FileHeader{Name: entry.name, Method: method}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP entry %q: %v", entry.name, err)
		}
		if _, err := writer.Write([]byte(entry.data)); err != nil {
			t.Fatalf("write ZIP entry %q: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close ZIP writer: %v", err)
	}
	return append([]byte(nil), data.Bytes()...)
}

func testZipReader(t *testing.T, entries ...testEntry) *zip.Reader {
	t.Helper()

	data := testZipData(t, entries...)
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		t.Fatalf("open generated ZIP: %v", err)
	}
	if r == nil {
		t.Fatal("zip.NewReader returned a nil reader")
	}
	return r
}

func TestExtractFileClosesReaderOnInsecurePathError(t *testing.T) {
	t.Setenv("GODEBUG", "zipinsecurepath=0")
	archivePath := filepath.Join(t.TempDir(), "unsafe.zip")
	if err := os.WriteFile(archivePath, testZipData(t, testEntry{name: "../escape.txt", data: "bad"}), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := ExtractFile(archivePath, filepath.Join(t.TempDir(), "output"), Limits{})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("ExtractFile() error = %v, want ErrUnsafePath", err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatalf("archive remained open after error: %v", err)
	}
}

func TestExtract(t *testing.T) {
	r := testZipReader(t,
		testEntry{name: "pkg/", method: zip.Store, mode: os.ModeDir | 0755},
		testEntry{name: "pkg/Hello.class", data: "hello"},
		testEntry{name: "empty.txt", method: zip.Store},
	)
	dest := filepath.Join(t.TempDir(), "output")

	result, err := Extract(r, dest, Limits{})
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if result.Entries != 3 || result.Files != 2 || result.Bytes != 5 {
		t.Fatalf("Extract() result = %+v", result)
	}
	content, err := os.ReadFile(filepath.Join(dest, "pkg", "Hello.class"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("extracted content = %q", content)
	}
}

func TestExtractRejectsUnsafePathsBeforeWriting(t *testing.T) {
	unsafeNames := []string{
		"../escape.txt",
		"safe/../../escape.txt",
		"/absolute.txt",
		`\absolute.txt`,
		`C:\escape.txt`,
		`C:escape.txt`,
		`\\server\share\file.txt`,
		"dir/file.txt:stream",
		"dir/./file.txt",
		"dir//file.txt",
		"CON.txt",
		"trailing.",
		"trailing ",
	}

	for _, name := range unsafeNames {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			r := testZipReader(t,
				testEntry{name: "valid.txt", data: "must not be written"},
				testEntry{name: name, data: "bad"},
			)
			dest := filepath.Join(t.TempDir(), "output")

			_, err := Extract(r, dest, Limits{})
			if !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Extract() error = %v, want ErrUnsafePath", err)
			}
			if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination was created before validation completed: %v", statErr)
			}
		})
	}
}

func TestExtractRejectsSymlinkEntry(t *testing.T) {
	r := testZipReader(t, testEntry{
		name:   "link",
		data:   "target.txt",
		method: zip.Store,
		mode:   os.ModeSymlink | 0777,
	})
	dest := filepath.Join(t.TempDir(), "output")

	_, err := Extract(r, dest, Limits{})
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("Extract() error = %v, want ErrUnsupportedType", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination was created for symlink archive: %v", statErr)
	}
}

func TestExtractRejectsExistingSymlinkComponent(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "output")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(dest, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "linked")); err != nil {
		t.Skipf("creating symlinks is unavailable: %v", err)
	}

	r := testZipReader(t, testEntry{name: "linked/escape.txt", data: "bad"})
	_, err := Extract(r, dest, Limits{})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Extract() error = %v, want ErrUnsafePath", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "escape.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("archive escaped through symlink: %v", statErr)
	}
}

func TestExtractEnforcesLimitsBeforeWriting(t *testing.T) {
	tests := []struct {
		name    string
		entries []testEntry
		limits  Limits
	}{
		{
			name: "entry count",
			entries: []testEntry{
				{name: "one.txt", data: "1"},
				{name: "two.txt", data: "2"},
			},
			limits: Limits{MaxEntries: 1},
		},
		{
			name:    "single file",
			entries: []testEntry{{name: "large.txt", data: "1234", method: zip.Store}},
			limits:  Limits{MaxFileBytes: 3},
		},
		{
			name: "total size",
			entries: []testEntry{
				{name: "one.txt", data: "123", method: zip.Store},
				{name: "two.txt", data: "456", method: zip.Store},
			},
			limits: Limits{MaxTotalBytes: 5},
		},
		{
			name:    "compression ratio",
			entries: []testEntry{{name: "compressed.bin", data: strings.Repeat("0", 4096)}},
			limits:  Limits{MaxCompressionRatio: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := testZipReader(t, tt.entries...)
			dest := filepath.Join(t.TempDir(), "output")
			_, err := Extract(r, dest, tt.limits)
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("Extract() error = %v, want ErrLimitExceeded", err)
			}
			if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination was created before limit validation: %v", statErr)
			}
		})
	}
}

func TestExtractWithOptionsFiltersOnlyAfterValidation(t *testing.T) {
	t.Run("selected entries", func(t *testing.T) {
		r := testZipReader(t,
			testEntry{name: "keep.class", data: "class"},
			testEntry{name: "drop.class", data: "drop"},
			testEntry{name: "resource.txt", data: "resource"},
		)
		dest := filepath.Join(t.TempDir(), "output")
		result, err := ExtractWithOptions(r, dest, Options{
			Include: func(file *zip.File) bool { return file.Name != "drop.class" },
		})
		if err != nil {
			t.Fatalf("ExtractWithOptions() error = %v", err)
		}
		if result.Files != 2 {
			t.Fatalf("ExtractWithOptions() files = %d, want 2", result.Files)
		}
		if _, err := os.Stat(filepath.Join(dest, "drop.class")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("excluded file exists: %v", err)
		}
	})

	t.Run("filter cannot mutate validated metadata", func(t *testing.T) {
		r := testZipReader(t, testEntry{name: "safe.txt", data: "safe"})
		dest := filepath.Join(t.TempDir(), "output")
		_, err := ExtractWithOptions(r, dest, Options{Include: func(file *zip.File) bool {
			file.Name = "../escape.txt"
			file.UncompressedSize64 = math.MaxUint64
			return true
		}})
		if err != nil {
			t.Fatalf("ExtractWithOptions() error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, "safe.txt")); err != nil {
			t.Fatalf("validated file was not extracted: %v", err)
		}
	})

	t.Run("excluded unsafe entry", func(t *testing.T) {
		r := testZipReader(t, testEntry{name: "../excluded.txt", data: "bad"})
		dest := filepath.Join(t.TempDir(), "output")
		_, err := ExtractWithOptions(r, dest, Options{Include: func(*zip.File) bool { return false }})
		if !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("ExtractWithOptions() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("excluded oversized entry", func(t *testing.T) {
		r := testZipReader(t, testEntry{name: "excluded.txt", data: "1234", method: zip.Store})
		dest := filepath.Join(t.TempDir(), "output")
		_, err := ExtractWithOptions(r, dest, Options{
			Limits:  Limits{MaxFileBytes: 3},
			Include: func(*zip.File) bool { return false },
		})
		if !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("ExtractWithOptions() error = %v, want ErrLimitExceeded", err)
		}
	})
}

func TestExtractRejectsPathTypeConflict(t *testing.T) {
	r := testZipReader(t,
		testEntry{name: "parent", data: "file"},
		testEntry{name: "parent/child.txt", data: "child"},
	)
	dest := filepath.Join(t.TempDir(), "output")

	_, err := Extract(r, dest, Limits{})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Extract() error = %v, want ErrUnsafePath", err)
	}
}

func TestExtractRejectsDuplicatePaths(t *testing.T) {
	r := testZipReader(t,
		testEntry{name: "duplicate.txt", data: "first"},
		testEntry{name: "duplicate.txt", data: "second"},
	)
	_, err := Extract(r, filepath.Join(t.TempDir(), "output"), Limits{})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Extract() error = %v, want ErrUnsafePath", err)
	}
}

func TestExtractPropagatesDestinationErrors(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dest, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	r := testZipReader(t, testEntry{name: "file.txt", data: "content"})
	if _, err := Extract(r, dest, Limits{}); err == nil {
		t.Fatal("Extract() error = nil, want destination error")
	}
}

func TestExtractPropagatesCopyErrorsWithoutClobberingTarget(t *testing.T) {
	r := testZipReader(t, testEntry{name: "file.txt", data: "new content", method: zip.Store})
	r.File[0].CRC32++
	dest := t.TempDir()
	target := filepath.Join(dest, "file.txt")
	if err := os.WriteFile(target, []byte("old content"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := Extract(r, dest, Limits{}); err == nil {
		t.Fatal("Extract() error = nil, want checksum error")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old content" {
		t.Fatalf("target was clobbered after failed copy: %q", content)
	}
}
