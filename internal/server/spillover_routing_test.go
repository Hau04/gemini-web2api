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
	chatBody := `{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"[Directly attached file(s): notes.txt.] ` + big + `"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	rec := httptest.NewRecorder()
	go handler.ServeHTTP(rec, req)

	if !<-bridgeGotRequest {
		t.Fatal("oversized prompt with local file reference must stay on the browser bridge")
	}
}
