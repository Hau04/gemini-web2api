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

// Oversized prompts used to be spilled into a .txt attachment and uploaded
// through the browser's file UI, which stalls on some Gemini Enterprise
// frontends. With cookies ready and no local files referenced, the request
// must go straight to direct cookie mode (prompt inlined in the POST body).
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

	// Register an active browser tab so bridge routing would normally win.
	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	// A bridge worker waits for work — it must receive NOTHING.
	bridgeGotRequest := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 2*time.Second)
		bridgeGotRequest <- err == nil && req != nil
	}()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if <-bridgeGotRequest {
		t.Fatal("oversized prompt was routed to the browser bridge; want direct cookie mode")
	}
	// Direct mode ran and failed against the failing stub — any non-200 proves
	// the request left the bridge path.
	if rec.Code == http.StatusOK {
		t.Fatalf("expected direct-mode failure status, got 200: %s", rec.Body.String())
	}
}

// Coding-agent prompts constantly mention paths (config.json, src/app.py …) that
// do not exist on the server's disk. Such mentions must NOT pin the request to
// the bridge: nothing could be uploaded anyway, and the browser upload UI stalls
// on some Gemini Enterprise frontends.
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

	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	bridgeGotRequest := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 2*time.Second)
		bridgeGotRequest <- err == nil && req != nil
	}()

	big := strings.Repeat("x", cfg.BridgeSpilloverChars+1000)
	// References a path that does NOT exist on this machine.
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"please fix /nonexistent/dir_9f8a7b/missing.go and ` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if <-bridgeGotRequest {
		t.Fatal("prompt referencing a non-existent file was routed to the bridge; want direct cookie mode")
	}
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
func TestOversizedPromptStaysOnBridgeWhenBardUnsupported(t *testing.T) {
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
		t.Fatal("request must stay on the bridge when the origin lacks the Bard RPC (direct mode would 404)")
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
	if !strings.Contains(rec.Body.String(), "open Gemini in Chrome") {
		t.Fatalf("error must tell the user to open the tab, got: %s", rec.Body.String())
	}
}
