package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// failingRequester makes every upstream Gemini call fail fast so tests can
// observe routing decisions without a real session.
type failingRequester struct{}

func (failingRequester) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader("stub upstream failure")),
		Header:     make(http.Header),
	}, nil
}

func TestPromptMayReferenceLocalFiles(t *testing.T) {
	user := func(s string) []models.OpenAIMessage {
		return []models.OpenAIMessage{{Role: "user", Content: s}}
	}

	if PromptMayReferenceLocalFiles(nil) {
		t.Error("empty messages should be false")
	}
	if PromptMayReferenceLocalFiles([]models.OpenAIMessage{{Role: "assistant", Content: `see "main.go"`}}) {
		t.Error("assistant turn should be false")
	}
	if PromptMayReferenceLocalFiles(user("just refactor this code please")) {
		t.Error("plain text should be false")
	}
	if !PromptMayReferenceLocalFiles(user(`[Directly attached file(s): code_context.txt. Use it.]`)) {
		t.Error("attachment notice should be true")
	}
	if !PromptMayReferenceLocalFiles(user(`please review /tmp/report.pdf thanks`)) {
		t.Error("unix abs path should be true")
	}
	if !PromptMayReferenceLocalFiles(user(`open C:\work\main.go now`)) {
		t.Error("windows path should be true")
	}
	if !PromptMayReferenceLocalFiles(user(`check "src/app.py" for bugs`)) {
		t.Error("quoted relative path should be true")
	}
}

// When cookies are ready and an active bridge tab is connected, oversized prompts
// stay on the browser bridge for .txt spillover (avoiding Enterprise batchexecute
// body size limits). When the browser is closed (no active bridge), oversized
// prompts route to direct cookie mode.
func TestOversizedPromptRoutesDirectWhenCookiesReady(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 2
	if cfg.BridgeSpilloverChars <= 0 {
		t.Fatalf("test requires spillover enabled by default, got %d", cfg.BridgeSpilloverChars)
	}

	app := New(cfg, "test")
	app.Gem.HTTP = failingRequester{}
	handler := app.Handler()

	// Register an active browser tab so bridge spillover is active.
	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	bridgeGotRequest := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 3*time.Second)
		if err == nil && req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
			bridgeGotRequest <- true
			return
		}
		bridgeGotRequest <- false
	}()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !<-bridgeGotRequest {
		t.Fatal("oversized prompt with active bridge should use the browser bridge for .txt spillover")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from bridge spillover, got %d: %s", rec.Code, rec.Body.String())
	}
}

// When the browser is closed (no active bridge), oversized prompts with cookies
// ready must route to direct cookie mode.
func TestOversizedPromptRoutesDirectWhenBrowserClosed(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 2

	app := New(cfg, "test")
	app.Gem.HTTP = failingRequester{}
	handler := app.Handler()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected direct-mode failure status against stub upstream, got 200: %s", rec.Body.String())
	}
}

// When the browser is closed (no active bridge), even prompts mentioning
// non-existent local paths route to direct cookie mode.
func TestOversizedPromptWithMissingFileRefRoutesDirect(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 2

	app := New(cfg, "test")
	app.Gem.HTTP = failingRequester{}
	handler := app.Handler()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	// References a path that does NOT exist on this machine, with browser closed.
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"please fix /nonexistent/dir_9f8a7b/missing.go and ` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected direct-mode failure status, got 200: %s", rec.Body.String())
	}
}

// When the latest user message references local files, the bridge must keep the
// request even for oversized prompts — only the browser path can attach them.
func TestOversizedPromptWithLocalFileRefStaysOnBridge(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 2

	app := New(cfg, "test")
	app.Gem.HTTP = failingRequester{}
	handler := app.Handler()

	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	// The referenced file must really exist on disk — text that merely mentions
	// a path no longer pins the request to the bridge.
	realFile := filepath.Join(tmpDir, "report.pdf")
	if err := os.WriteFile(realFile, []byte("%PDF-1.4 dummy"), 0o600); err != nil {
		t.Fatalf("write pdf fixture: %v", err)
	}

	bridgeGotRequest := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 3*time.Second)
		if err == nil && req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
			bridgeGotRequest <- true
			return
		}
		bridgeGotRequest <- false
	}()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"please review ` + realFile + ` ` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	go handler.ServeHTTP(rec, req)

	if !<-bridgeGotRequest {
		t.Fatal("oversized prompt with local file reference must stay on the browser bridge")
	}
}

// On Gemini Enterprise origins the Bard web RPC does not exist (404) — direct
// cookie mode must be skipped entirely and oversized prompts stay on the bridge.
func TestOversizedPromptOnEnterpriseRoutesDirectWhenDirectModeUsable(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 2
	// A configured widget endpoint makes direct mode usable even though the
	// origin has no Bard RPC.
	cfg.DiscoveryEngineEndpoint = "https://discoveryengine.clients6.google.com/v1alpha/locations/global/widgetStreamAssist?key=k"
	cfg.DiscoveryEngineConfigID = "cfg-1"

	app := New(cfg, "test")
	app.Gem.HTTP = failingRequester{}
	app.Gem.MarkBardUnsupported() // simulated prior 404 from this origin
	handler := app.Handler()

	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	bridgeGotRequest := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 2*time.Second)
		if err == nil && req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
			bridgeGotRequest <- true
			return
		}
		bridgeGotRequest <- false
	}()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, req)
		close(done)
	}()

	if <-bridgeGotRequest {
		t.Fatal("oversized prompt must not wait on the browser tab when Discovery Engine direct mode can serve it")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
}

// Without any direct transport (no template, no endpoint, no widget csesidx), a
// Discovery Engine origin has nothing but the bridge — the spillover must stay.
func TestOversizedPromptOnEnterpriseStaysOnBridgeWithoutDirectMode(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 2

	app := New(cfg, "test")
	app.Gem.HTTP = failingRequester{}
	app.Gem.MarkBardUnsupported()
	handler := app.Handler()

	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	bridgeGotRequest := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 3*time.Second)
		if err == nil && req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
			bridgeGotRequest <- true
			return
		}
		bridgeGotRequest <- false
	}()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	go handler.ServeHTTP(rec, req)

	if !<-bridgeGotRequest {
		t.Fatal("request must stay on the bridge when there is no direct transport at all")
	}
}

// A fresh cookie sync must re-enable direct mode probing when the session moved
// to a different origin; same-origin syncs keep the latch (no re-probe churn).
func TestCookieSyncNewOriginClearsBardUnsupported(t *testing.T) {
	app, _ := newCookieTestApp(t)

	// Establish a stored Enterprise origin.
	payload1 := `{"cookie":"SAPISID=abc123; __Secure-1PSID=xyz","sapisid":"abc123","origin":"https://vertexaisearch.cloud.google.com"}`
	req1 := httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(payload1))
	w1 := httptest.NewRecorder()
	app.Handler().ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first sync expected 200, got %d: %s", w1.Code, w1.Body.String())
	}

	app.Gem.MarkBardUnsupported()

	// Same-origin sync: latch must survive (periodic syncs are frequent).
	req2 := httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(payload1))
	w2 := httptest.NewRecorder()
	app.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second sync expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	if !app.Gem.BardUnsupported() {
		t.Fatal("same-origin cookie sync must NOT clear the BardUnsupported latch")
	}

	// Different origin: latch must clear so the next request re-probes.
	payload3 := `{"cookie":"SAPISID=abc123; __Secure-1PSID=xyz","sapisid":"abc123","origin":"https://gemini.google.com"}`
	req3 := httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(payload3))
	w3 := httptest.NewRecorder()
	app.Handler().ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("third sync expected 200, got %d: %s", w3.Code, w3.Body.String())
	}
	if app.Gem.BardUnsupported() {
		t.Fatal("cookie sync from a new origin must clear the BardUnsupported latch")
	}
}

// When no browser tab is connected AND direct cookie mode cannot serve the
// request (no cookies, or an Enterprise origin latched as Bard-unsupported),
// the handler must fail fast with an actionable 503 instead of a cryptic
// upstream 502.
func TestBridgeOfflineWithoutDirectModeReturns503(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 2

	app := New(cfg, "test")
	app.Gem.HTTP = failingRequester{}
	app.Gem.MarkBardUnsupported()
	handler := app.Handler()

	// No heartbeat: the bridge reports disconnected.
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when neither bridge nor direct mode is usable, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bridge_offline") {
		t.Fatalf("expected bridge_offline error type, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "open Gemini tab") {
		t.Fatalf("error must tell the user to open the tab, got: %s", rec.Body.String())
	}
}
