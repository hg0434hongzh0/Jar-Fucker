package handler

import (
	"encoding/json"
	"io/fs"
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
