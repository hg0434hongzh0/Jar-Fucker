package safezip

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

const (
	DefaultMaxEntries                = 100_000
	DefaultMaxFileBytes        int64 = 512 << 20
	DefaultMaxTotalBytes       int64 = 4 << 30
	DefaultMaxCompressionRatio int64 = 200
)

var (
	ErrUnsafePath      = errors.New("unsafe archive path")
	ErrLimitExceeded   = errors.New("archive extraction limit exceeded")
	ErrUnsupportedType = errors.New("unsupported archive entry type")
)

// Limits bounds both the metadata advertised by an archive and the bytes
// observed while extracting it. Zero values use the corresponding defaults.
type Limits struct {
	MaxEntries          int
	MaxFileBytes        int64
	MaxTotalBytes       int64
	MaxCompressionRatio int64
}

type Options struct {
	Limits  Limits
	Include func(*zip.File) bool
	Context context.Context
	// SkipDuplicateFiles keeps the first validated entry for an exact duplicate
	// path. File/directory collisions and all other unsafe paths remain errors.
	SkipDuplicateFiles bool
}

type Result struct {
	Entries int
	Files   int
	Bytes   int64
}

func DefaultLimits() Limits {
	return Limits{
		MaxEntries:          DefaultMaxEntries,
		MaxFileBytes:        DefaultMaxFileBytes,
		MaxTotalBytes:       DefaultMaxTotalBytes,
		MaxCompressionRatio: DefaultMaxCompressionRatio,
	}
}

// ExtractFile securely extracts a ZIP-compatible archive into destDir.
func ExtractFile(archivePath, destDir string, limits Limits) (result Result, err error) {
	return ExtractFileWithOptions(archivePath, destDir, Options{Limits: limits})
}

// ExtractFileWithOptions securely extracts a ZIP-compatible archive into
// destDir. Include is evaluated only after the complete archive is validated.
func ExtractFileWithOptions(archivePath, destDir string, options Options) (result Result, err error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		if errors.Is(err, zip.ErrInsecurePath) {
			err = errors.Join(ErrUnsafePath, err)
		}
		if r != nil {
			err = errors.Join(err, r.Close())
		}
		return Result{}, fmt.Errorf("open archive: %w", err)
	}
	defer func() {
		if closeErr := r.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close archive: %w", closeErr))
		}
	}()

	result, err = ExtractWithOptions(&r.Reader, destDir, options)
	return result, err
}

// Extract validates the complete central directory before creating output.
func Extract(r *zip.Reader, destDir string, limits Limits) (Result, error) {
	return ExtractWithOptions(r, destDir, Options{Limits: limits})
}

// ExtractWithOptions validates the complete central directory before applying
// Include and creating output. Excluded entries still count toward all limits.
func ExtractWithOptions(r *zip.Reader, destDir string, options Options) (Result, error) {
	if r == nil {
		return Result{}, errors.New("archive reader is nil")
	}

	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return Result{}, err
	}
	entries, err := prepareEntries(ctx, r, limits, options.SkipDuplicateFiles)
	if err != nil {
		return Result{}, err
	}
	if options.Include != nil {
		included := entries[:0]
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			file := *entry.file
			if options.Include(&file) {
				included = append(included, entry)
			}
		}
		entries = included
	}

	root, err := prepareRoot(destDir)
	if err != nil {
		return Result{}, err
	}

	var result Result
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if entry.isDir {
			if err := ensureDirectory(root, entry.name); err != nil {
				return result, fmt.Errorf("create archive directory %q: %w", entry.file.Name, err)
			}
			result.Entries++
			continue
		}

		parent := path.Dir(entry.name)
		if parent != "." {
			if err := ensureDirectory(root, parent); err != nil {
				return result, fmt.Errorf("create parent for %q: %w", entry.file.Name, err)
			}
		}

		n, err := extractFileEntry(ctx, root, entry, limits, result.Bytes)
		if err != nil {
			return result, fmt.Errorf("extract archive entry %q: %w", entry.file.Name, err)
		}
		result.Entries++
		result.Files++
		result.Bytes += n
	}

	return result, nil
}

type preparedEntry struct {
	file  *zip.File
	name  string
	isDir bool
}

func normalizeLimits(limits Limits) (Limits, error) {
	defaults := DefaultLimits()
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaults.MaxEntries
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MaxCompressionRatio == 0 {
		limits.MaxCompressionRatio = defaults.MaxCompressionRatio
	}

	if limits.MaxEntries < 0 || limits.MaxFileBytes < 0 || limits.MaxTotalBytes < 0 || limits.MaxCompressionRatio < 0 {
		return Limits{}, errors.New("archive limits must be positive")
	}
	if limits.MaxFileBytes >= math.MaxInt64 || limits.MaxTotalBytes >= math.MaxInt64 {
		return Limits{}, errors.New("archive byte limits are too large")
	}
	return limits, nil
}

func prepareEntries(ctx context.Context, r *zip.Reader, limits Limits, skipDuplicateFiles bool) ([]preparedEntry, error) {
	if len(r.File) > limits.MaxEntries {
		return nil, fmt.Errorf("%w: entries %d exceed %d", ErrLimitExceeded, len(r.File), limits.MaxEntries)
	}

	entries := make([]preparedEntry, 0, len(r.File))
	kinds := make(map[string]bool, len(r.File))
	var total uint64

	for _, file := range r.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, err := cleanArchivePath(file.Name)
		if err != nil {
			return nil, err
		}

		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: symlink %q", ErrUnsupportedType, file.Name)
		}
		if mode&os.ModeType != 0 && !mode.IsDir() {
			return nil, fmt.Errorf("%w: %q has mode %s", ErrUnsupportedType, file.Name, mode)
		}

		normalizedName := strings.ReplaceAll(file.Name, `\`, "/")
		isDir := mode.IsDir() || strings.HasSuffix(normalizedName, "/")
		if isDir && file.UncompressedSize64 != 0 {
			return nil, fmt.Errorf("%w: directory %q contains data", ErrUnsupportedType, file.Name)
		}

		key := archiveKey(name)
		if existingIsDir, exists := kinds[key]; exists {
			if !skipDuplicateFiles || existingIsDir != isDir {
				return nil, fmt.Errorf("%w: duplicate path %q", ErrUnsafePath, file.Name)
			}
			// Some vendor JARs contain the same file more than once. For
			// decompiler staging, keeping the first validated entry is safe and
			// deterministic; file/directory collisions remain forbidden.
			continue
		}
		kinds[key] = isDir

		if !isDir {
			if file.UncompressedSize64 > uint64(limits.MaxFileBytes) {
				return nil, fmt.Errorf("%w: %q expands to %d bytes", ErrLimitExceeded, file.Name, file.UncompressedSize64)
			}
			if file.UncompressedSize64 > uint64(limits.MaxTotalBytes)-total {
				return nil, fmt.Errorf("%w: total expanded size exceeds %d bytes", ErrLimitExceeded, limits.MaxTotalBytes)
			}
			if exceedsRatio(file.UncompressedSize64, file.CompressedSize64, limits.MaxCompressionRatio) {
				return nil, fmt.Errorf("%w: %q exceeds compression ratio %d:1", ErrLimitExceeded, file.Name, limits.MaxCompressionRatio)
			}
			total += file.UncompressedSize64
		}

		entries = append(entries, preparedEntry{file: file, name: name, isDir: isDir})
	}

	for _, entry := range entries {
		for parent := path.Dir(entry.name); parent != "."; parent = path.Dir(parent) {
			if isDir, exists := kinds[archiveKey(parent)]; exists && !isDir {
				return nil, fmt.Errorf("%w: file %q is used as a directory", ErrUnsafePath, parent)
			}
		}
	}

	return entries, nil
}

func cleanArchivePath(name string) (string, error) {
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("%w: invalid name %q", ErrUnsafePath, name)
	}

	normalized := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(normalized, "/") || path.IsAbs(normalized) {
		return "", fmt.Errorf("%w: absolute path %q", ErrUnsafePath, name)
	}

	trimmed := strings.TrimRight(normalized, "/")
	if trimmed == "" {
		return "", fmt.Errorf("%w: empty path %q", ErrUnsafePath, name)
	}
	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: invalid component in %q", ErrUnsafePath, name)
		}
		if strings.Contains(part, ":") {
			return "", fmt.Errorf("%w: Windows drive or alternate data stream in %q", ErrUnsafePath, name)
		}
		if strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") || isWindowsDeviceName(part) {
			return "", fmt.Errorf("%w: non-portable Windows path %q", ErrUnsafePath, name)
		}
	}

	cleaned := path.Clean(trimmed)
	if cleaned != trimmed || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: path traversal %q", ErrUnsafePath, name)
	}
	return cleaned, nil
}

func isWindowsDeviceName(component string) bool {
	base := component
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	base = strings.ToUpper(base)
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) {
		return base[3] >= '1' && base[3] <= '9'
	}
	return false
}

func archiveKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}

func exceedsRatio(uncompressed, compressed uint64, ratio int64) bool {
	if uncompressed == 0 {
		return false
	}
	if compressed == 0 {
		return true
	}
	r := uint64(ratio)
	if compressed > math.MaxUint64/r {
		return false
	}
	return uncompressed > compressed*r
}

func prepareRoot(destDir string) (string, error) {
	root, err := filepath.Abs(destDir)
	if err != nil {
		return "", fmt.Errorf("resolve destination: %w", err)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", fmt.Errorf("create destination: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: destination is a symlink", ErrUnsafePath)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("destination is not a directory: %s", root)
	}
	return root, nil
}

func ensureDirectory(root, archiveName string) error {
	current := root
	for _, component := range strings.Split(archiveName, "/") {
		current = filepath.Join(current, filepath.FromSlash(component))
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0755); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: directory component %s is a symlink", ErrUnsafePath, current)
		}
		if !info.IsDir() {
			return fmt.Errorf("path component is not a directory: %s", current)
		}
	}
	return nil
}

func extractFileEntry(ctx context.Context, root string, entry preparedEntry, limits Limits, totalBytes int64) (int64, error) {
	target, err := containedTarget(root, entry.name)
	if err != nil {
		return 0, err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("%w: target is a symlink", ErrUnsafePath)
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("target is not a regular file: %s", target)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}

	rc, err := entry.file.Open()
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".safezip-*")
	if err != nil {
		return 0, errors.Join(err, rc.Close())
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	allowed := int64(entry.file.UncompressedSize64)
	allowed = min(allowed, limits.MaxFileBytes)
	allowed = min(allowed, limits.MaxTotalBytes-totalBytes)
	allowed = min(allowed, ratioByteLimit(entry.file.CompressedSize64, limits.MaxCompressionRatio))

	n, copyErr := io.Copy(tmp, &contextReader{ctx: ctx, reader: io.LimitReader(rc, allowed+1)})
	closeErr := tmp.Close()
	readerCloseErr := rc.Close()
	if err := errors.Join(copyErr, closeErr, readerCloseErr); err != nil {
		return 0, err
	}
	if n > allowed {
		return 0, fmt.Errorf("%w: expanded data exceeds allowed %d bytes", ErrLimitExceeded, allowed)
	}
	if n != int64(entry.file.UncompressedSize64) {
		return 0, fmt.Errorf("archive size mismatch: expected %d bytes, read %d", entry.file.UncompressedSize64, n)
	}

	mode := entry.file.Mode().Perm()
	if mode == 0 {
		mode = 0644
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return 0, err
	}
	return n, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func containedTarget(root, archiveName string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(archiveName))
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes destination", ErrUnsafePath, archiveName)
	}
	return target, nil
}

func ratioByteLimit(compressed uint64, ratio int64) int64 {
	if compressed == 0 {
		return 0
	}
	r := uint64(ratio)
	if compressed > uint64(math.MaxInt64-1)/r {
		return math.MaxInt64 - 1
	}
	return int64(compressed * r)
}
