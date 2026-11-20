package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)


// The 401 must teach the caller how to authenticate instead of only saying
// "invalid api key" — every client that forgets the header hits this.
func TestAuthFailureExplainsHowToAuthenticate(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.APIKeys = []string{"sk-test-key"}
	app := New(cfg, "test")
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/model-probe", strings.NewReader(`{"model":"gemini-4-hydrogen"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a key, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Authorization: Bearer", "x-api-key", "?key=<key>", "invalid_api_key"} {
		if !strings.Contains(body, want) {
			t.Errorf("401 body should mention %q: %s", want, body)
		}
	}

	// All four documented forms must actually work for the probe endpoint.
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer sk-test-key") },
		func(r *http.Request) { r.Header.Set("x-api-key", "sk-test-key") },
		func(r *http.Request) { r.Header.Set("x-goog-api-key", "sk-test-key") },
	} {
		req := httptest.NewRequest("POST", "/v1/model-probe", strings.NewReader(`{}`))
		mutate(req)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Errorf("a documented auth form was rejected: %s", rec.Body.String())
		}
	}
	// ?key= is honoured too (kept from the Google-compatible surface).
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models?key=sk-test-key", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("?key= must authenticate, got %d", rec.Code)
	}
}
