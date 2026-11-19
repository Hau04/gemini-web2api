package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// cookieSyncState tracks the health of browser-pushed Gemini session cookies:
// when the extension last synced them, and the most recent upstream auth failure
// observed by the direct cookie-mode client.
type cookieSyncState struct {
	mu              sync.Mutex
	lastSync        time.Time
	lastAuthError   string
	lastAuthErrorAt time.Time
}

func newCookieSyncState() *cookieSyncState {
	return &cookieSyncState{}
}

// RecordAuthError stores the latest upstream authentication failure so the
// status endpoint (and extension popup/badge) can tell the user to re-sign in.
// Returns true when this is a NEW error occurrence (no active error was recorded
// or the message changed) — used to fire the notification webhook only once per
// expiry instead of on every failing request.
func (s *cookieSyncState) RecordAuthError(msg string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	isNew := s.lastAuthError != msg
	s.lastAuthError = msg
	s.lastAuthErrorAt = time.Now()
	return isNew
}

// recordSync marks a fresh successful cookie push and clears any stale auth error.
func (s *cookieSyncState) recordSync() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSync = time.Now()
	s.lastAuthError = ""
	s.lastAuthErrorAt = time.Time{}
}

func (s *cookieSyncState) snapshot() (lastSync time.Time, authErr string, authErrAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSync, s.lastAuthError, s.lastAuthErrorAt
}

// cookieFilePath resolves where session cookies are persisted. When the user did
// not configure cookie_file explicitly, it defaults to cookie.json in the working
// directory — matching the extension's manual "Download cookie.json" workflow.
func (a *App) cookieFilePath() string {
	if strings.TrimSpace(a.Cfg.CookieFile) != "" {
		return a.Cfg.CookieFile
	}
	return "cookie.json"
}

// cookiesReady reports whether usable Gemini session cookies are available for
// the direct cookie mode (either SAPISID or __Secure-1PSID present).
func (a *App) cookiesReady() bool {
	if a.Gem == nil || a.Gem.Cookies == nil {
		return false
	}
	info, err := a.Gem.Cookies.Load()
	if err != nil || strings.TrimSpace(info.Cookie) == "" {
		return false
	}
	return info.SAPISID != "" || strings.Contains(info.Cookie, "__Secure-1PSID")
}

// directModeUsable reports whether direct cookie mode can serve a request:
// session cookies are present (synced by the browser extension). We do NOT gate
// this on the Bard/RPC probe: on Gemini Enterprise origins the model-discovery
// endpoint 404s, but the actual generate RPC can still be reached with valid
// session cookies, so a discovery 404 must not permanently disable cookie mode.
// Use this for routing/fallback decisions; use cookiesReady for session-health
// reporting.
func (a *App) directModeUsable() bool {
	return a.cookiesReady()
}

// sanitizeSyncedOrigin validates an extension-reported origin/base_url before it
// is persisted into the cookie file: https:// URLs only, known Gemini-family
// hosts only (the extension can only harvest XSRF tokens from these origins).
// Returns "" for anything else.
func sanitizeSyncedOrigin(raw string) string {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "gemini.google.com", "vertexaisearch.cloud.google.com", "business.gemini.google":
		return "https://" + host
	}
	return ""
}

// handleBridgeCookies receives session cookies pushed by the browser extension's
// background service worker and persists them to the cookie file. The direct-mode
// client's CookieCache reloads the file on its next use (mtime-based), so fresh
// cookies take effect without a restart. Merges with previously stored XSRF/BL
// values so cookie-only pushes never wipe a good token.
func (a *App) handleBridgeCookies(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Cookie    string `json:"cookie"`
		SAPISID   string `json:"sapisid"`
		AuthUser  string `json:"auth_user"`
		XSRFToken string `json:"xsrf_token"`
		GeminiBL  string `json:"gemini_bl"`
		BaseURL   string `json:"base_url"`
		Origin    string `json:"origin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}

	payload.Cookie = strings.TrimSpace(payload.Cookie)
	if payload.Cookie == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing cookie"})
		return
	}

	if payload.SAPISID == "" {
		// Recover SAPISID from the cookie header string when not passed explicitly.
		for _, pair := range strings.Split(payload.Cookie, ";") {
			kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
			if len(kv) == 2 && kv[0] == "SAPISID" {
				payload.SAPISID = kv[1]
				break
			}
		}
	}
	if payload.SAPISID == "" && !strings.Contains(payload.Cookie, "__Secure-1PSID") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "no usable Google session cookies in payload"})
		return
	}

	// Merge: never let a cookies-only push wipe a previously stored XSRF token,
	// BL value, auth_user, or synced origin that this push did not include.
	if payload.BaseURL == "" {
		payload.BaseURL = payload.Origin
	}
	payload.BaseURL = sanitizeSyncedOrigin(payload.BaseURL)
	prevOrigin := ""
	if a.Gem != nil && a.Gem.Cookies != nil {
		if existing, err := a.Gem.Cookies.Load(); err == nil {
			prevOrigin = existing.BaseURL
			if payload.XSRFToken == "" {
				payload.XSRFToken = existing.XSRFToken
			}
			if payload.GeminiBL == "" {
				payload.GeminiBL = existing.GeminiBL
			}
			if payload.AuthUser == "" {
				payload.AuthUser = existing.AuthUser
			}
			if payload.BaseURL == "" {
				payload.BaseURL = existing.BaseURL
			}
		}
	}

	stored := map[string]string{
		"cookie":  payload.Cookie,
		"sapisid": payload.SAPISID,
	}
	if payload.AuthUser != "" {
		stored["auth_user"] = payload.AuthUser
	}
	if payload.XSRFToken != "" {
		stored["xsrf_token"] = payload.XSRFToken
	}
	if payload.GeminiBL != "" {
		stored["gemini_bl"] = payload.GeminiBL
	}
	if payload.BaseURL != "" {
		stored["base_url"] = payload.BaseURL
	}

	data, err := json.Marshal(stored)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	path := a.cookieFilePath()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		a.Logf("Cookie sync write error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to persist cookies"})
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			a.Logf("Cookie sync write error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to persist cookies"})
			return
		}
		_ = os.Remove(tmp)
	}

	a.CookieSync.recordSync()
	// A session synced from a DIFFERENT origin may support the Bard RPC even if the
	// previous one did not (and vice versa) — reset the latch + discovery backoff
	// so the next request re-probes. Same-origin syncs keep the latch to avoid a
	// probe 404 after every periodic sync.
	if a.Gem != nil && payload.BaseURL != prevOrigin {
		a.Gem.ClearBardUnsupported()
	}
	a.Logf("Session cookies synced from browser extension (%d bytes, xsrf=%t, origin=%s)", len(payload.Cookie), payload.XSRFToken != "", payload.BaseURL)

	_, authErr, authErrAt := a.CookieSync.snapshot()
	resp := map[string]any{
		"ok":          true,
		"synced_at":   time.Now().UTC().Format(time.RFC3339),
		"has_xsrf":    payload.XSRFToken != "" || a.Cfg.XSRFToken != "",
		"needs_xsrf":  payload.XSRFToken == "" && a.Cfg.XSRFToken == "",
		"auth_error":  nil,
		"cookie_file": filepath.Base(path),
	}
	if authErr != "" {
		resp["auth_error"] = authErr
		resp["auth_error_at"] = authErrAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleCookieStatus reports cookie availability, last extension sync time, and
// the latest upstream auth error so the extension popup/badge can surface
// "session expired — open gemini.google.com" states.
func (a *App) handleCookieStatus(w http.ResponseWriter, r *http.Request) {
	lastSync, authErr, authErrAt := a.CookieSync.snapshot()
	resp := map[string]any{
		"configured": a.cookiesReady(),
		"auth_error": nil,
	}
	if a.Gem != nil {
		resp["configured_base_url"] = a.Gem.Cfg.BaseURL
		if a.Gem.Cookies != nil {
			if info, err := a.Gem.Cookies.Load(); err == nil {
				resp["synced_origin"] = info.BaseURL
			}
		}
	}
	if !lastSync.IsZero() {
		resp["last_sync"] = lastSync.UTC().Format(time.RFC3339)
		resp["last_sync_age_sec"] = int(time.Since(lastSync).Seconds())
	}
	if authErr != "" {
		resp["auth_error"] = authErr
		resp["auth_error_at"] = authErrAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}
