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

func newMultiAccountTestApp(t *testing.T) (*App, string) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = filepath.Join(tmpDir, "cookie.json")
	cfg.AccountsDir = filepath.Join(tmpDir, "accounts")
	app := New(cfg, "test-multi")
	return app, tmpDir
}

func TestAccountLifecycleAndCookieIsolation(t *testing.T) {
	app, tmpDir := newMultiAccountTestApp(t)
	h := app.Handler()

	// 1. Create "work" account with JSON cookie payload.
	workBody := `{"id":"work","label":"Work Account","raw":"{\"cookie\":\"SAPISID=work_sap; __Secure-1PSID=work_psid\",\"sapisid\":\"work_sap\",\"xsrf_token\":\"work_xsrf\"}"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(workBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create work: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Create "personal" account with raw Cookie header string.
	personalBody := `{"id":"personal","label":"Personal","raw":"SID=p1; SAPISID=pers_sap; __Secure-1PSID=pers_psid"}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(personalBody)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create personal: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. Verify isolation on disk and in CookieCache.
	workApp, _, ok := app.Accounts.Resolve("work")
	if !ok {
		t.Fatalf("work account not resolved")
	}
	persApp, _, ok := app.Accounts.Resolve("personal")
	if !ok {
		t.Fatalf("personal account not resolved")
	}
	workInfo, err := workApp.Gem.Cookies.Load()
	if err != nil || workInfo.SAPISID != "work_sap" || workInfo.XSRFToken != "work_xsrf" {
		t.Fatalf("unexpected work cookies: %+v err=%v", workInfo, err)
	}
	persInfo, err := persApp.Gem.Cookies.Load()
	if err != nil || persInfo.SAPISID != "pers_sap" || persInfo.XSRFToken != "" {
		t.Fatalf("unexpected personal cookies: %+v err=%v", persInfo, err)
	}
	if app.cookiesReady() {
		t.Fatalf("default account should remain isolated with no cookies")
	}

	// 4. Extension bridge cookie push scoped to /a/personal/bridge/cookies.
	bridgePush := `{"cookie":"SAPISID=pers_sap_2; __Secure-1PSID=pers_psid_2","sapisid":"pers_sap_2","xsrf_token":"pers_xsrf_2"}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/a/personal/bridge/cookies", strings.NewReader(bridgePush)))
	if rec.Code != http.StatusOK {
		t.Fatalf("bridge cookie push failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Gemini-Account"); got != "personal" {
		t.Errorf("expected X-Gemini-Account=personal, got %q", got)
	}
	// Work account must remain completely untouched.
	workInfo2, _ := workApp.Gem.Cookies.Load()
	if workInfo2.SAPISID != "work_sap" {
		t.Fatalf("work account was mutated by personal bridge push: %+v", workInfo2)
	}

	// 5. List accounts and verify status fields.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list accounts: expected 200, got %d", rec.Code)
	}
	var listResp struct {
		Accounts []AccountStatus `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("invalid list JSON: %v", err)
	}
	if len(listResp.Accounts) != 3 {
		t.Fatalf("expected 3 accounts (default, personal, work), got %d", len(listResp.Accounts))
	}

	// 6. Clear work cookies via DELETE /api/accounts/work/cookies.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts/work/cookies", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear work cookies: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if workApp.cookiesReady() {
		t.Fatalf("work account cookies should be cleared")
	}
	if !persApp.cookiesReady() {
		t.Fatalf("personal account cookies should still be ready")
	}

	// 7. Default account cannot be deleted.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts/default", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting default account must return 400, got %d", rec.Code)
	}

	// 8. Delete work account and verify directory removal.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts/work", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete work: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "accounts", "work")); !os.IsNotExist(err) {
		t.Fatalf("expected work directory to be removed, stat err=%v", err)
	}
}

func TestAccountPathAndHeaderRoutingWithAuth(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = filepath.Join(tmpDir, "cookie.json")
	cfg.AccountsDir = filepath.Join(tmpDir, "accounts")
	cfg.APIKeys = []string{"sk-test-123"}
	cfg.BridgeToken = "bridge-secret"
	app := New(cfg, "test-auth")
	if _, err := app.Accounts.Create("work", "Work"); err != nil {
		t.Fatalf("create work: %v", err)
	}
	h := app.Handler()

	// 1. Account-scoped /a/work/v1/models without API key -> 401.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/work/v1/models", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without api key, got %d", rec.Code)
	}

	// 2. Account-scoped /a/work/v1/models with API key -> 200 + X-Gemini-Account header.
	req := httptest.NewRequest(http.MethodGet, "/a/work/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test-123")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with api key, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Gemini-Account"); got != "work" {
		t.Errorf("expected X-Gemini-Account=work, got %q", got)
	}

	// 3. Unknown account -> 404.
	req = httptest.NewRequest(http.MethodGet, "/a/ghost/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test-123")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown account, got %d", rec.Code)
	}

	// 4. Header-based account selection on /v1/models.
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-test-123")
	req.Header.Set("X-Gemini-Account", "work")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Gemini-Account") != "work" {
		t.Fatalf("header routing failed: code=%d header=%q", rec.Code, rec.Header().Get("X-Gemini-Account"))
	}

	// 5. /api/accounts requires bridge_token or api_key when configured.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for /api/accounts without token, got %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/accounts", nil)
	req.Header.Set("X-Bridge-Token", "bridge-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/accounts with bridge token, got %d", rec.Code)
	}
}

func TestAccountManagerReloadsFromDisk(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = filepath.Join(tmpDir, "cookie.json")
	cfg.AccountsDir = filepath.Join(tmpDir, "accounts")

	app1 := New(cfg, "v1")
	acc, err := app1.Accounts.Create("acc2", "Second Account")
	if err != nil {
		t.Fatalf("create acc2: %v", err)
	}
	if err := acc.app.saveAccountCookies(cookiePayload{Raw: "SAPISID=reloaded_sap; __Secure-1PSID=reloaded_psid"}); err != nil {
		t.Fatalf("save cookies: %v", err)
	}

	// Restart server with same config.
	app2 := New(cfg, "v1")
	st, ok := app2.Accounts.GetStatus("acc2")
	if !ok {
		t.Fatalf("acc2 was not reloaded from disk")
	}
	if st.Label != "Second Account" || !st.CookiesReady || st.CookieStatus != "ready" {
		t.Fatalf("unexpected reloaded status: %+v", st)
	}
}

func TestAccountUIServed(t *testing.T) {
	app, _ := newMultiAccountTestApp(t)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /ui, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Gemini Web2API Accounts") {
		t.Fatalf("UI HTML missing expected title")
	}
}
