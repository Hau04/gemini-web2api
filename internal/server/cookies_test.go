package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func newCookieTestApp(t *testing.T) (*App, string) {
	t.Helper()
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")

	cfg := config.Config{
		CookieFile: cookieFile,
	}
	app := New(cfg, "test")
	return app, cookieFile
}

func TestHandleBridgeCookiesValidPayload(t *testing.T) {
	app, cookieFile := newCookieTestApp(t)

	payload := `{"cookie":"SAPISID=abc123; __Secure-1PSID=xyz","sapisid":"abc123","auth_user":"0"}`
	req := httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(payload))
	w := httptest.NewRecorder()

	app.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if resp["ok"] != true {
		t.Fatalf("expected ok=true, got %v", resp)
	}

	// Verify file was written and is parseable
	data, err := os.ReadFile(cookieFile)
	if err != nil {
		t.Fatalf("cookie file not written: %v", err)
	}
	var stored map[string]string
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("stored cookie file is invalid JSON: %v", err)
	}
	if stored["cookie"] != "SAPISID=abc123; __Secure-1PSID=xyz" {
		t.Errorf("unexpected stored cookie: %q", stored["cookie"])
	}
	if stored["sapisid"] != "abc123" {
		t.Errorf("unexpected stored sapisid: %q", stored["sapisid"])
	}
}

func TestHandleBridgeCookiesRejectsEmpty(t *testing.T) {
	app, _ := newCookieTestApp(t)

	payload := `{"cookie":"","sapisid":""}`
	req := httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(payload))
	w := httptest.NewRecorder()

	app.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleBridgeCookiesRejectsNoSession(t *testing.T) {
	app, _ := newCookieTestApp(t)

	// Cookie header with no SAPISID and no __Secure-1PSID should be rejected
	payload := `{"cookie":"SID=foo; HSID=bar"}`
	req := httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(payload))
	w := httptest.NewRecorder()

	app.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestHandleBridgeCookiesMergesXSRF(t *testing.T) {
	app, cookieFile := newCookieTestApp(t)

	// First push includes an XSRF token
	p1 := `{"cookie":"SAPISID=abc123; __Secure-1PSID=xyz","sapisid":"abc123","xsrf_token":"token-aaa"}`
	req := httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(p1))
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first push failed: %d: %s", w.Code, w.Body.String())
	}

	// Second push without XSRF should preserve the stored one
	p2 := `{"cookie":"SAPISID=abc123; __Secure-1PSID=xyz","sapisid":"abc123"}`
	req = httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(p2))
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("second push failed: %d: %s", w.Code, w.Body.String())
	}

	data, _ := os.ReadFile(cookieFile)
	var stored map[string]string
	_ = json.Unmarshal(data, &stored)
	if stored["xsrf_token"] != "token-aaa" {
		t.Errorf("expected merged xsrf_token 'token-aaa', got %q", stored["xsrf_token"])
	}
}

func TestHandleBridgeFrozen(t *testing.T) {
	app, _ := newCookieTestApp(t)

	if app.Bridge.IsFrozen() {
		t.Fatal("bridge should not start frozen")
	}

	payload := `{"frozen":true,"client_id":"bg_42","tab":"gemini.google.com"}`
	req := httptest.NewRequest(http.MethodPost, "/bridge/frozen", strings.NewReader(payload))
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !app.Bridge.IsFrozen() {
		t.Fatal("bridge should be frozen after frozen=true")
	}

	// A frozen bridge is not connected even if it has a recent heartbeat
	app.Bridge.Touch("gemini.google.com")
	if app.Bridge.IsConnected() {
		t.Fatal("frozen bridge must not report connected")
	}

	payload = `{"frozen":false,"client_id":"bg_42","tab":"gemini.google.com"}`
	req = httptest.NewRequest(http.MethodPost, "/bridge/frozen", strings.NewReader(payload))
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on unfreeze, got %d", w.Code)
	}
	if app.Bridge.IsFrozen() {
		t.Fatal("bridge should be unfrozen after frozen=false")
	}
}

func TestHandleCookieStatus(t *testing.T) {
	app, _ := newCookieTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/bridge/cookie-status", nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["configured"] != false {
		t.Errorf("expected configured=false before any sync, got %v", resp["configured"])
	}

	// Push cookies, then status should show configured=true
	payload := `{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`
	req = httptest.NewRequest(http.MethodPost, "/bridge/cookies", strings.NewReader(payload))
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)

	req = httptest.NewRequest(http.MethodGet, "/bridge/cookie-status", nil)
	w = httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["configured"] != true {
		t.Errorf("expected configured=true after sync, got %v", resp)
	}
	if resp["last_sync"] == nil {
		t.Error("expected last_sync timestamp after sync")
	}
}

func TestBridgeStatusIncludesCookieFields(t *testing.T) {
	app, _ := newCookieTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/bridge/status", nil)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["cookies_ready"] != false {
		t.Errorf("expected cookies_ready=false, got %v", resp["cookies_ready"])
	}
	if resp["frozen"] != false {
		t.Errorf("expected frozen=false, got %v", resp["frozen"])
	}
	if resp["degraded_sec"] != float64(0) {
		t.Errorf("expected degraded_sec=0, got %v", resp["degraded_sec"])
	}
}

func TestDefaultCookieFile(t *testing.T) {
	cfg := config.Config{} // CookieFile intentionally empty
	app := New(cfg, "test")
	if app.Cfg.CookieFile != "cookie.json" {
		t.Errorf("expected default cookie_file 'cookie.json', got %q", app.Cfg.CookieFile)
	}
}
