package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestBridgeTokenProtectsBridgeEndpoints(t *testing.T) {
	cfg := config.Default()
	cfg.BridgeToken = "secret-token-123"
	app := New(cfg, "test")
	handler := app.Handler()

	// No token -> 401
	req := httptest.NewRequest(http.MethodGet, "/bridge/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}

	// Wrong token -> 401
	req = httptest.NewRequest(http.MethodGet, "/bridge/status", nil)
	req.Header.Set("X-Bridge-Token", "wrong")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong token, got %d", w.Code)
	}

	// Correct X-Bridge-Token -> 200
	req = httptest.NewRequest(http.MethodGet, "/bridge/status", nil)
	req.Header.Set("X-Bridge-Token", "secret-token-123")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with correct token, got %d", w.Code)
	}

	// Correct Bearer token also accepted
	req = httptest.NewRequest(http.MethodGet, "/bridge/status", nil)
	req.Header.Set("Authorization", "Bearer secret-token-123")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with bearer token, got %d", w.Code)
	}

	// Cookie push without token -> 401 (cookies cannot be overwritten by strangers)
	req = httptest.NewRequest(http.MethodPost, "/bridge/cookies",
		strings.NewReader(`{"cookie":"SAPISID=x","sapisid":"x"}`))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on cookie push without token, got %d", w.Code)
	}
}

func TestBridgeTokenUnsetLeavesBridgeOpen(t *testing.T) {
	cfg := config.Default()
	cfg.BridgeToken = ""
	app := New(cfg, "test")
	handler := app.Handler()

	req := httptest.NewRequest(http.MethodGet, "/bridge/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 with no token configured, got %d", w.Code)
	}
}

func TestBuildNotifyPayloadFormats(t *testing.T) {
	// ntfy: raw text + Title header
	body, ct, headers := buildNotifyPayload("ntfy", "Test Title", "hello world")
	if string(body) != "hello world" {
		t.Errorf("ntfy body = %q, want raw text", body)
	}
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("ntfy content-type = %q", ct)
	}
	if headers["Title"] != "Test Title" {
		t.Errorf("ntfy Title header = %q", headers["Title"])
	}

	// discord: {"content": ...}
	body, ct, _ = buildNotifyPayload("discord", "T", "msg")
	if !strings.Contains(string(body), `"content"`) || !strings.Contains(string(body), "T") {
		t.Errorf("discord body = %q", body)
	}
	if ct != "application/json" {
		t.Errorf("discord content-type = %q", ct)
	}

	// slack: {"text": ...}
	body, _, _ = buildNotifyPayload("slack", "T", "msg")
	if !strings.Contains(string(body), `"text"`) {
		t.Errorf("slack body = %q", body)
	}

	// generic: {"title": ..., "text": ...}
	body, _, _ = buildNotifyPayload("generic", "T", "msg")
	if !strings.Contains(string(body), `"title"`) || !strings.Contains(string(body), `"text"`) {
		t.Errorf("generic body = %q", body)
	}

	// unknown format falls back to ntfy-style raw text
	body, _, _ = buildNotifyPayload("weird", "T", "msg")
	if string(body) != "msg" {
		t.Errorf("unknown format body = %q, want raw text fallback", body)
	}
}

func TestAuthErrorNotifyDedupes(t *testing.T) {
	cfg := config.Default()
	app := New(cfg, "test")

	// First occurrence -> true (would notify)
	if !app.CookieSync.RecordAuthError("session expired") {
		t.Fatal("first auth error should be reported as new")
	}
	// Same error again -> false (no duplicate notification)
	if app.CookieSync.RecordAuthError("session expired") {
		t.Fatal("identical repeated auth error should not be reported as new")
	}
	// Different error -> true
	if !app.CookieSync.RecordAuthError("xsrf expired") {
		t.Fatal("changed auth error should be reported as new")
	}
	// Fresh cookie sync clears the error and re-arms notifications
	app.CookieSync.recordSync()
	if !app.CookieSync.RecordAuthError("session expired") {
		t.Fatal("auth error after cookie re-sync should be reported as new")
	}
}
