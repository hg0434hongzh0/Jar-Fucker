package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hg0434hongzh0/Jar-Fucker/internal/cfr"
	"github.com/hg0434hongzh0/Jar-Fucker/internal/jar"
)

const testToken = "test-session-token"

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	web := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("ok")},
	}
	var webFS fs.FS = web
	return New(webFS, Options{
		SessionToken: testToken,
		ConfigPath:   filepath.Join(t.TempDir(), "config.json"),
	})
}

func request(t *testing.T, h http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:9527"
	if token != "" {
		req.Header.Set("X-Jar-Fucker-Token", token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIRequiresSessionToken(t *testing.T) {
	h := testHandler(t)
	rec := request(t, h, http.MethodGet, "/api/browse", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	rec = request(t, h, http.MethodGet, "/api/browse", "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestSessionEndpointValidatesCandidateWithoutReturningToken(t *testing.T) {
	h := testHandler(t)
	rec := request(t, h, http.MethodGet, "/api/session", "", testToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("valid token status = %d, want %d; body=%s", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if rec.Body.Len() != 0 || strings.Contains(rec.Body.String(), testToken) {
		t.Fatalf("session endpoint leaked content: %q", rec.Body.String())
	}

	rec = request(t, h, http.MethodGet, "/api/session", "", "wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if strings.Contains(rec.Body.String(), testToken) {
		t.Fatalf("invalid-token response leaked session token: %q", rec.Body.String())
	}
}

func TestDecompileProgressUsesMeasuredPhasesWithoutCompletingEarly(t *testing.T) {
	updates := []cfr.Progress{
		{Phase: cfr.ProgressExtracting},
		{Phase: cfr.ProgressDecompiling, ClassFiles: 100},
		{Phase: cfr.ProgressDecompiling, CompletedUnits: 50, ClassFiles: 100, Detail: "demo/Half"},
		{Phase: cfr.ProgressFinalizing, CompletedUnits: 73, ClassFiles: 100},
	}
	wantPercents := []int{8, 17, 53, 93}
	last := -1
	for i, update := range updates {
		event := mapDecompileProgress(update, "demo.jar", 1, 1, 8, 98)
		if event.Percent != wantPercents[i] {
			t.Fatalf("phase %s percent = %d, want %d", update.Phase, event.Percent, wantPercents[i])
		}
		if event.Percent < last || event.Percent >= 98 {
			t.Fatalf("phase %s reported invalid early progress %d after %d", update.Phase, event.Percent, last)
		}
		last = event.Percent
	}
	heartbeat := decompileHeartbeat(updates[2], "demo.jar", 1, 1, last, 3*time.Second)
	if heartbeat.Type != "heartbeat" || heartbeat.Percent != last || heartbeat.Elapsed != "3s" {
		t.Fatalf("heartbeat = %+v", heartbeat)
	}
}

func TestOfferDecompileProgressKeepsLatestUpdate(t *testing.T) {
	ch := make(chan cfr.Progress, 1)
	offerDecompileProgress(ch, cfr.Progress{CompletedUnits: 1})
	offerDecompileProgress(ch, cfr.Progress{CompletedUnits: 2})
	if got := (<-ch).CompletedUnits; got != 2 {
		t.Fatalf("coalesced progress = %d, want 2", got)
	}
}

func TestRejectsNonLoopbackHostAndCrossOrigin(t *testing.T) {
	h := testHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/browse", nil)
	req.Host = "attacker.example"
	req.Header.Set("X-Jar-Fucker-Token", testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/browse", nil)
	req.Host = "127.0.0.1:9527"
	req.Header.Set("Origin", "https://attacker.example")
	req.Header.Set("X-Jar-Fucker-Token", testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestConfigRejectsUnknownFieldsAndSavesAtomically(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "nested", "config.json")
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}
	h := New(web, Options{SessionToken: testToken, ConfigPath: configPath})

	rec := request(t, h, http.MethodPut, "/api/config", `{"javaPath":"","cfrPath":"","unexpected":true}`, testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	rec = request(t, h, http.MethodPut, "/api/config", `{"javaPath":"","cfrPath":""}`, testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("save status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	var cfg appConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode saved config: %v", err)
	}
	if cfg.JavaPath != "" || cfg.CfrPath != "" {
		t.Fatalf("saved config = %#v, want empty auto-detect paths", cfg)
	}
}

func TestLoopbackHostParsing(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1:9527": true,
		"[::1]:9527":     true,
		"localhost:9527": true,
		"example.test":   false,
		"127.0.0.1.evil": false,
	}
	for host, want := range tests {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestFilePreviewIsLimitedToFiveMiB(t *testing.T) {
	if maxViewerFileBytes != 5<<20 {
		t.Fatalf("maxViewerFileBytes = %d, want 5 MiB", maxViewerFileBytes)
	}
	h := testHandler(t)
	root := t.TempDir()

	smallPath := filepath.Join(root, "Small.java")
	if err := os.WriteFile(smallPath, []byte("class Small {}"), 0600); err != nil {
		t.Fatal(err)
	}
	rec := request(t, h, http.MethodGet, "/api/file?path="+url.QueryEscape(smallPath), "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("small file status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	largePath := filepath.Join(root, "Large.java")
	f, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(largePath, maxViewerFileBytes+1); err != nil {
		t.Fatal(err)
	}
	rec = request(t, h, http.MethodGet, "/api/file?path="+url.QueryEscape(largePath), "", testToken)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large file status = %d, want %d; body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "5 MiB") {
		t.Fatalf("large file response does not describe limit: %s", rec.Body.String())
	}
}

func TestTreeEndpointReturnsBoundedPage(t *testing.T) {
	h := testHandler(t)
	root := t.TempDir()
	for _, name := range []string{"a.java", "b.java"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}

	target := "/api/tree?root=" + url.QueryEscape(root) + "&limit=1"
	rec := request(t, h, http.MethodGet, target, "", testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var page jar.TreeNode
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode tree response: %v", err)
	}
	if page.TotalChildren != 2 || len(page.Children) != 1 || !page.HasMore || page.Limit != 1 {
		t.Fatalf("tree page = %+v", page)
	}

	rec = request(t, h, http.MethodGet, "/api/tree?root="+url.QueryEscape(root)+"&offset=-1", "", testToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative offset status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestVSCodeWorkspaceEndpointCreatesJavaAuditWorkspace(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	jarPath := filepath.Join(root, "demo.jar")
	if err := os.WriteFile(jarPath, []byte("jar"), 0644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"outputDir": output,
		"jars":      []string{jarPath},
		"sourceDir": root,
		"open":      false,
	})
	rec := request(t, testHandler(t), http.MethodPost, "/api/vscode-workspace", string(body), testToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		WorkspaceFile string   `json:"workspaceFile"`
		Libraries     []string `json:"libraries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.WorkspaceFile); err != nil {
		t.Fatalf("workspace not created: %v", err)
	}
	if len(result.Libraries) != 1 {
		t.Fatalf("libraries = %#v", result.Libraries)
	}
}

func buildTestJAR(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	manifest, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Write([]byte("Manifest-Version: 1.0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func multipartUploadRequest(t *testing.T, files []struct {
	name string
	data []byte
}) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("files", file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Host = "127.0.0.1:9527"
	req.Header.Set("X-Jar-Fucker-Token", testToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestUploadStreamsValidJARsAndIgnoresOtherFiles(t *testing.T) {
	h := testHandler(t)
	jarData := buildTestJAR(t)
	req := multipartUploadRequest(t, []struct {
		name string
		data []byte
	}{
		{name: "first.jar", data: jarData},
		{name: "notes.txt", data: []byte("ignored")},
		{name: "second.JAR", data: jarData},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response struct {
		TempDir string        `json:"tempDir"`
		Files   []jar.JarFile `json:"files"`
		Total   int           `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Total != 2 || len(response.Files) != 2 {
		t.Fatalf("response = %+v, want two JARs", response)
	}
	for _, file := range response.Files {
		if _, err := os.Stat(file.Path); err != nil {
			t.Fatalf("uploaded file %s is unavailable: %v", file.Path, err)
		}
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/upload?dir="+url.QueryEscape(response.TempDir), nil)
	deleteReq.Host = "127.0.0.1:9527"
	deleteReq.Header.Set("X-Jar-Fucker-Token", testToken)
	deleteRec := httptest.NewRecorder()
	h.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("cleanup status = %d; body=%s", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestUploadRejectsMoreThanConfiguredJARLimit(t *testing.T) {
	h := testHandler(t)
	jarData := buildTestJAR(t)
	files := make([]struct {
		name string
		data []byte
	}, maxUploadedFiles+1)
	for i := range files {
		files[i].name = fmt.Sprintf("lib-%04d.jar", i)
		files[i].data = jarData
	}
	req := multipartUploadRequest(t, files)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "4096") {
		t.Fatalf("limit response did not include configured limit: %s", rec.Body.String())
	}
}

func TestUploadSizeLimitHasClearError(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.writeUploadReadError(rec, &http.MaxBytesError{Limit: maxUploadBody})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(rec.Body.String(), "2 GiB") {
		t.Fatalf("size-limit response is unclear: %s", rec.Body.String())
	}
}

func TestBackgroundDecompileTaskRetainsProgressAcrossRequests(t *testing.T) {
	now := time.Now()
	task := &backgroundDecompileTask{
		id:        "persistent-task",
		status:    "running",
		createdAt: now,
		updatedAt: now,
	}
	task.send(decompileEvent{Type: "warning", Message: "已跳过 broken.jar", Percent: 50})
	first := task.snapshot()
	if first.Status != "running" || len(first.Warnings) != 1 || first.Event == nil {
		t.Fatalf("running snapshot = %+v", first)
	}

	result := &cfr.Result{OutputDir: t.TempDir(), JavaFiles: 12, SucceededJars: 1, FailedJars: 1}
	task.send(decompileEvent{Type: "done", Message: "反编译完成", Percent: 100, Result: result})
	finished := task.snapshot()
	if finished.Status != "done" || finished.Result == nil || finished.Result.FailedJars != 1 {
		t.Fatalf("finished snapshot = %+v", finished)
	}
	if len(finished.Warnings) != 1 {
		t.Fatalf("warnings were not retained: %+v", finished.Warnings)
	}
}
