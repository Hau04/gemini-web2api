package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
)

const DefaultAccountID = "default"

var (
	accountIDRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	reservedAccounts = map[string]bool{
		"default":  true,
		"admin":    true,
		"accounts": true,
		"api":      true,
		"ui":       true,
		"v1":       true,
		"v1beta":   true,
		"bridge":   true,
		"a":        true,
	}
)

// AccountMeta is persisted as account.json inside each isolated account directory.
type AccountMeta struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type managedAccount struct {
	meta AccountMeta
	dir  string
	app  *App
}

// AccountManager maintains isolated per-account App instances, each with its own
// cookie.json, Discovery Engine templates, learned model cache, bridge hub, and
// conversation/session state.
type AccountManager struct {
	mu         sync.RWMutex
	dir        string
	defaultApp *App
	accounts   map[string]*managedAccount
}

// AccountStatus is the JSON representation returned by /api/accounts.
type AccountStatus struct {
	ID                   string `json:"id"`
	Label                string `json:"label"`
	IsDefault            bool   `json:"is_default"`
	CreatedAt            string `json:"created_at,omitempty"`
	APIBasePath          string `json:"api_base_path"`
	GoogleBasePath       string `json:"google_base_path"`
	BridgeBasePath       string `json:"bridge_base_path"`
	CookieStatus         string `json:"cookie_status"` // "ready", "expired", "missing"
	CookiesReady         bool   `json:"cookies_ready"`
	DirectModeUsable     bool   `json:"direct_mode_usable"`
	HasSAPISID           bool   `json:"has_sapisid"`
	HasPSID              bool   `json:"has_psid"`
	HasXSRF              bool   `json:"has_xsrf"`
	HasCsesidx           bool   `json:"has_csesidx"`
	HasServerToken       bool   `json:"has_server_token"`
	CookieFile           string `json:"cookie_file"`
	CookieUpdatedAt      string `json:"cookie_updated_at,omitempty"`
	CookieLastSync       string `json:"cookie_last_sync,omitempty"`
	CookieLastSyncAgeSec *int   `json:"cookie_last_sync_age_sec,omitempty"`
	AuthError            string `json:"auth_error,omitempty"`
	AuthErrorAt          string `json:"auth_error_at,omitempty"`
	SyncedOrigin         string `json:"synced_origin,omitempty"`
	ConfiguredBaseURL    string `json:"configured_base_url,omitempty"`
	AuthUser             string `json:"auth_user,omitempty"`
	BridgeConnected      bool   `json:"bridge_connected"`
	BridgeTab            string `json:"bridge_tab,omitempty"`
	BridgePaused         bool   `json:"bridge_paused"`
	BridgeFrozen         bool   `json:"bridge_frozen"`
	HasDiscoveryTemplate bool   `json:"has_discovery_template"`
	HasUploadTemplate    bool   `json:"has_upload_template"`
}

func resolveAccountsDir(cfg config.Config) string {
	if d := strings.TrimSpace(cfg.AccountsDir); d != "" {
		return d
	}
	cookiePath := strings.TrimSpace(cfg.CookieFile)
	if cookiePath == "" {
		cookiePath = "cookie.json"
	}
	return filepath.Join(filepath.Dir(cookiePath), "accounts")
}

func validateAccountID(id string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(id))
	if norm == "" {
		return "", fmt.Errorf("account id is required")
	}
	if reservedAccounts[norm] {
		return "", fmt.Errorf("account id %q is reserved", norm)
	}
	if !accountIDRe.MatchString(norm) {
		return "", fmt.Errorf("account id %q must be 1-48 lowercase letters, digits, '-' or '_' (starting with letter or digit)", norm)
	}
	return norm, nil
}

func newAccountManager(defaultApp *App, dir string) *AccountManager {
	m := &AccountManager{
		dir:        dir,
		defaultApp: defaultApp,
		accounts:   make(map[string]*managedAccount),
	}
	m.loadExisting()
	return m
}

func (m *AccountManager) loadExisting() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, err := validateAccountID(entry.Name())
		if err != nil {
			continue
		}
		accDir := filepath.Join(m.dir, id)
		meta := AccountMeta{ID: id, Label: id}
		if data, err := os.ReadFile(filepath.Join(accDir, "account.json")); err == nil {
			var loaded AccountMeta
			if json.Unmarshal(data, &loaded) == nil {
				meta = loaded
				meta.ID = id
			}
		}
		if meta.Label == "" {
			meta.Label = id
		}
		accApp := m.buildAccountApp(id, accDir)
		m.accounts[id] = &managedAccount{
			meta: meta,
			dir:  accDir,
			app:  accApp,
		}
	}
}

func (m *AccountManager) buildAccountApp(id, accDir string) *App {
	cfg := m.defaultApp.Cfg
	cfg.CookieFile = filepath.Join(accDir, "cookie.json")
	if strings.TrimSpace(cfg.BridgePacingStateFile) != "" {
		cfg.BridgePacingStateFile = filepath.Join(accDir, ".bridge_pacing_state.json")
	}
	return newAccountApp(cfg, m.defaultApp.Version, id)
}

// Resolve returns the isolated App for the requested account ID (or the default
// App when id is empty or "default").
func (m *AccountManager) Resolve(id string) (*App, string, bool) {
	norm := strings.ToLower(strings.TrimSpace(id))
	if norm == "" || norm == DefaultAccountID {
		return m.defaultApp, DefaultAccountID, true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc, ok := m.accounts[norm]
	if !ok {
		return nil, norm, false
	}
	return acc.app, acc.meta.ID, true
}

// Count returns the number of named accounts (excluding the default account).
func (m *AccountManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.accounts)
}

// List returns status snapshots for the default account followed by all named accounts.
func (m *AccountManager) List() []AccountStatus {
	m.mu.RLock()
	named := make([]*managedAccount, 0, len(m.accounts))
	for _, acc := range m.accounts {
		named = append(named, acc)
	}
	m.mu.RUnlock()

	sort.Slice(named, func(i, j int) bool {
		return named[i].meta.ID < named[j].meta.ID
	})

	out := make([]AccountStatus, 0, len(named)+1)
	out = append(out, buildAccountStatus(AccountMeta{ID: DefaultAccountID, Label: "Default"}, true, m.defaultApp))
	for _, acc := range named {
		out = append(out, buildAccountStatus(acc.meta, false, acc.app))
	}
	return out
}

// GetStatus returns the status snapshot for one account (including "default").
func (m *AccountManager) GetStatus(id string) (AccountStatus, bool) {
	norm := strings.ToLower(strings.TrimSpace(id))
	if norm == "" || norm == DefaultAccountID {
		return buildAccountStatus(AccountMeta{ID: DefaultAccountID, Label: "Default"}, true, m.defaultApp), true
	}
	m.mu.RLock()
	acc, ok := m.accounts[norm]
	m.mu.RUnlock()
	if !ok {
		return AccountStatus{}, false
	}
	return buildAccountStatus(acc.meta, false, acc.app), true
}

// Create creates a new isolated account directory and App instance.
func (m *AccountManager) Create(id, label string) (*managedAccount, error) {
	norm, err := validateAccountID(id)
	if err != nil {
		return nil, err
	}
	label = strings.TrimSpace(label)
	if label == "" {
		label = norm
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.accounts[norm]; exists {
		return nil, fmt.Errorf("account %q already exists", norm)
	}

	accDir := filepath.Join(m.dir, norm)
	if err := os.MkdirAll(accDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create account directory: %w", err)
	}

	meta := AccountMeta{
		ID:        norm,
		Label:     label,
		CreatedAt: time.Now().UTC(),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(accDir, "account.json"), metaBytes, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write account metadata: %w", err)
	}

	acc := &managedAccount{
		meta: meta,
		dir:  accDir,
		app:  m.buildAccountApp(norm, accDir),
	}
	m.accounts[norm] = acc
	return acc, nil
}

// Delete removes a named account and its isolated data directory from disk.
func (m *AccountManager) Delete(id string) error {
	norm := strings.ToLower(strings.TrimSpace(id))
	if norm == DefaultAccountID {
		return fmt.Errorf("the default account cannot be deleted")
	}

	m.mu.Lock()
	acc, ok := m.accounts[norm]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("account %q not found", norm)
	}
	delete(m.accounts, norm)
	m.mu.Unlock()

	if acc.dir != "" {
		if err := os.RemoveAll(acc.dir); err != nil {
			return fmt.Errorf("failed to remove account directory: %w", err)
		}
	}
	return nil
}

func buildAccountStatus(meta AccountMeta, isDefault bool, app *App) AccountStatus {
	st := AccountStatus{
		ID:        meta.ID,
		Label:     meta.Label,
		IsDefault: isDefault,
	}
	if st.Label == "" {
		st.Label = meta.ID
	}
	if !meta.CreatedAt.IsZero() {
		st.CreatedAt = meta.CreatedAt.UTC().Format(time.RFC3339)
	}
	if isDefault {
		st.APIBasePath = "/v1"
		st.GoogleBasePath = "/v1beta"
		st.BridgeBasePath = ""
	} else {
		st.APIBasePath = "/a/" + meta.ID + "/v1"
		st.GoogleBasePath = "/a/" + meta.ID + "/v1beta"
		st.BridgeBasePath = "/a/" + meta.ID
	}

	if app == nil {
		st.CookieStatus = "missing"
		return st
	}

	cookiePath := app.cookieFilePath()
	st.CookieFile = cookiePath
	if fi, err := os.Stat(cookiePath); err == nil {
		st.CookieUpdatedAt = fi.ModTime().UTC().Format(time.RFC3339)
	}

	st.CookiesReady = app.cookiesReady()
	st.DirectModeUsable = app.directModeUsable()

	if app.Gem != nil {
		st.ConfiguredBaseURL = app.Gem.Cfg.BaseURL
		st.HasDiscoveryTemplate = app.Gem.HasDiscoveryTemplate()
		st.HasUploadTemplate = app.Gem.HasDiscoveryUploadTemplate()
		if app.Gem.Cookies != nil {
			if info, err := app.Gem.Cookies.Load(); err == nil {
				st.HasSAPISID = info.SAPISID != ""
				st.HasPSID = strings.Contains(info.Cookie, "__Secure-1PSID") || strings.Contains(info.Cookie, "__Secure-3PSID")
				st.HasXSRF = info.XSRFToken != "" || app.Cfg.XSRFToken != ""
				st.HasCsesidx = info.Csesidx != "" || app.Cfg.DiscoveryEngineCsesidx != ""
				st.HasServerToken = info.ServerToken != "" || app.Cfg.DiscoveryEngineServerToken != ""
				st.SyncedOrigin = info.BaseURL
				st.AuthUser = info.AuthUser
			}
		}
	}

	if app.CookieSync != nil {
		lastSync, authErr, authErrAt := app.CookieSync.snapshot()
		if !lastSync.IsZero() {
			st.CookieLastSync = lastSync.UTC().Format(time.RFC3339)
			age := int(time.Since(lastSync).Seconds())
			st.CookieLastSyncAgeSec = &age
		}
		if authErr != "" {
			st.AuthError = authErr
			st.AuthErrorAt = authErrAt.UTC().Format(time.RFC3339)
		}
	}

	if app.Bridge != nil {
		st.BridgeConnected = app.Bridge.IsConnected()
		st.BridgeTab = app.Bridge.TabName()
		st.BridgePaused = app.Bridge.IsPaused()
		st.BridgeFrozen = app.Bridge.IsFrozen()
	}

	switch {
	case st.AuthError != "":
		st.CookieStatus = "expired"
	case st.CookiesReady:
		st.CookieStatus = "ready"
	default:
		st.CookieStatus = "missing"
	}

	return st
}

// cookiePayload holds parsed fields accepted when saving cookies via API/GUI.
type cookiePayload struct {
	Cookie      string `json:"cookie"`
	SAPISID     string `json:"sapisid"`
	AuthUser    string `json:"auth_user"`
	XSRFToken   string `json:"xsrf_token"`
	GeminiBL    string `json:"gemini_bl"`
	BaseURL     string `json:"base_url"`
	Origin      string `json:"origin"`
	ServerToken string `json:"server_token"`
	Csesidx     string `json:"csesidx"`
	Raw         string `json:"raw"`
}

func parseFlexibleCookiePayload(p cookiePayload) (cookiePayload, error) {
	raw := strings.TrimSpace(p.Raw)
	if raw != "" && strings.TrimSpace(p.Cookie) == "" {
		if strings.HasPrefix(raw, "{") {
			var nested struct {
				Cookie      string          `json:"cookie"`
				SAPISID     string          `json:"sapisid"`
				AuthUser    json.RawMessage `json:"auth_user"`
				XSRFToken   string          `json:"xsrf_token"`
				GeminiBL    string          `json:"gemini_bl"`
				BaseURL     string          `json:"base_url"`
				Origin      string          `json:"origin"`
				ServerToken string          `json:"server_token"`
				Csesidx     string          `json:"csesidx"`
			}
			if err := json.Unmarshal([]byte(raw), &nested); err != nil {
				return p, fmt.Errorf("invalid cookie JSON: %w", err)
			}
			p.Cookie = nested.Cookie
			if p.SAPISID == "" {
				p.SAPISID = nested.SAPISID
			}
			if p.AuthUser == "" && len(nested.AuthUser) > 0 {
				u := strings.Trim(strings.TrimSpace(string(nested.AuthUser)), `"`)
				if u != "null" {
					p.AuthUser = u
				}
			}
			if p.XSRFToken == "" {
				p.XSRFToken = nested.XSRFToken
			}
			if p.GeminiBL == "" {
				p.GeminiBL = nested.GeminiBL
			}
			if p.BaseURL == "" {
				p.BaseURL = nested.BaseURL
			}
			if p.Origin == "" {
				p.Origin = nested.Origin
			}
			if p.ServerToken == "" {
				p.ServerToken = nested.ServerToken
			}
			if p.Csesidx == "" {
				p.Csesidx = nested.Csesidx
			}
		} else {
			p.Cookie = raw
		}
	}
	p.Cookie = strings.TrimSpace(p.Cookie)
	if p.Cookie == "" {
		return p, fmt.Errorf("missing cookie")
	}
	if p.SAPISID == "" {
		for _, pair := range strings.Split(p.Cookie, ";") {
			kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
			if len(kv) == 2 && kv[0] == "SAPISID" {
				p.SAPISID = kv[1]
				break
			}
		}
	}
	if p.SAPISID == "" && !strings.Contains(p.Cookie, "__Secure-1PSID") && !strings.Contains(p.Cookie, "__Secure-3PSID") {
		return p, fmt.Errorf("no usable Google session cookies (need SAPISID or __Secure-1PSID)")
	}
	return p, nil
}

// saveAccountCookies persists parsed cookies to an App's isolated cookie.json.
func (a *App) saveAccountCookies(p cookiePayload) error {
	p, err := parseFlexibleCookiePayload(p)
	if err != nil {
		return err
	}
	if p.BaseURL == "" {
		p.BaseURL = p.Origin
	}
	p.BaseURL = sanitizeSyncedOrigin(p.BaseURL)

	prevOrigin := ""
	if a.Gem != nil && a.Gem.Cookies != nil {
		if existing, err := a.Gem.Cookies.Load(); err == nil {
			prevOrigin = existing.BaseURL
			if p.XSRFToken == "" {
				p.XSRFToken = existing.XSRFToken
			}
			if p.GeminiBL == "" {
				p.GeminiBL = existing.GeminiBL
			}
			if p.AuthUser == "" {
				p.AuthUser = existing.AuthUser
			}
			if p.BaseURL == "" {
				p.BaseURL = existing.BaseURL
			}
			if p.ServerToken == "" {
				p.ServerToken = existing.ServerToken
			}
			if p.Csesidx == "" {
				p.Csesidx = existing.Csesidx
			}
		}
	}

	stored := map[string]string{
		"cookie":  p.Cookie,
		"sapisid": p.SAPISID,
	}
	if p.AuthUser != "" {
		stored["auth_user"] = p.AuthUser
	}
	if p.XSRFToken != "" {
		stored["xsrf_token"] = p.XSRFToken
	}
	if p.GeminiBL != "" {
		stored["gemini_bl"] = p.GeminiBL
	}
	if p.BaseURL != "" {
		stored["base_url"] = p.BaseURL
	}
	if p.ServerToken != "" {
		stored["server_token"] = p.ServerToken
	}
	p.Csesidx = sanitizeCsesidx(p.Csesidx)
	if p.Csesidx != "" {
		stored["csesidx"] = p.Csesidx
	}

	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}

	path := a.cookieFilePath()
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("failed to persist cookies: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return fmt.Errorf("failed to persist cookies: %w", err)
		}
		_ = os.Remove(tmp)
	}

	if a.Gem != nil && a.Gem.Cookies != nil {
		a.Gem.Cookies.Reset()
	}
	if a.CookieSync != nil {
		a.CookieSync.recordSync()
	}
	if a.Gem != nil && p.BaseURL != prevOrigin {
		a.Gem.ClearBardUnsupported()
	}
	return nil
}

// clearAccountCookies removes the account's cookie.json and clears in-memory caches.
func (a *App) clearAccountCookies() error {
	path := a.cookieFilePath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if a.Gem != nil && a.Gem.Cookies != nil {
		a.Gem.Cookies.Reset()
	}
	if a.CookieSync != nil {
		a.CookieSync.reset()
	}
	return nil
}

// -------------------- Account Management HTTP Handlers --------------------

func (a *App) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	if a.Accounts == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"accounts": []AccountStatus{buildAccountStatus(AccountMeta{ID: DefaultAccountID, Label: "Default"}, true, a)},
			"version":  a.Version,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": a.Accounts.List(),
		"version":  a.Version,
	})
}

func (a *App) handleAccountsCreate(w http.ResponseWriter, r *http.Request) {
	if a.Accounts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "account manager not initialized"})
		return
	}
	var payload struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		cookiePayload
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}

	// Validate cookie payload before creating the directory if cookies were provided.
	hasCookies := strings.TrimSpace(payload.Cookie) != "" || strings.TrimSpace(payload.Raw) != ""
	if hasCookies {
		if _, err := parseFlexibleCookiePayload(payload.cookiePayload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}

	acc, err := a.Accounts.Create(payload.ID, payload.Label)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}

	if hasCookies {
		if err := acc.app.saveAccountCookies(payload.cookiePayload); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
	}

	a.Logf("Account created: id=%q label=%q cookies=%t", acc.meta.ID, acc.meta.Label, hasCookies)
	st, _ := a.Accounts.GetStatus(acc.meta.ID)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "account": st})
}

func (a *App) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Accounts == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "account not found"})
		return
	}
	st, ok := a.Accounts.GetStatus(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("account %q not found", id)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": st})
}

func (a *App) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Accounts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "account manager not initialized"})
		return
	}
	if err := a.Accounts.Delete(id); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	a.Logf("Account deleted: id=%q", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

func (a *App) handleAccountCookiesUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Accounts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "account manager not initialized"})
		return
	}
	targetApp, normID, ok := a.Accounts.Resolve(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("account %q not found", id)})
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(body))) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty cookie payload"})
		return
	}

	var payload cookiePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		// Allow posting a raw cookie string directly as plain text.
		payload.Raw = string(body)
	}

	if err := targetApp.saveAccountCookies(payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	a.Logf("Account cookies updated via API/GUI: id=%q", normID)
	st, _ := a.Accounts.GetStatus(normID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": st})
}

func (a *App) handleAccountCookiesClear(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Accounts == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "account manager not initialized"})
		return
	}
	targetApp, normID, ok := a.Accounts.Resolve(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("account %q not found", id)})
		return
	}
	if err := targetApp.clearAccountCookies(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.Logf("Account cookies cleared via API/GUI: id=%q", normID)
	st, _ := a.Accounts.GetStatus(normID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": st})
}

// splitAccountPath extracts (accountID, remainderPath, matched) from paths of
// the form "/a/{accountID}/{rest...}".
func splitAccountPath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, "/a/") {
		return "", path, false
	}
	rest := strings.TrimPrefix(path, "/a/")
	if rest == "" {
		return "", path, false
	}
	slash := strings.IndexByte(rest, '/')
	if slash == -1 {
		return rest, "/", true
	}
	return rest[:slash], rest[slash:], true
}

// authorizeAdmin protects /api/accounts/* when either bridge_token or api_keys
// is configured. When neither is set (localhost default), access stays open.
func authorizeAdmin(r *http.Request, cfg config.Config) bool {
	if cfg.BridgeToken == "" && len(cfg.APIKeys) == 0 {
		return true
	}
	if cfg.BridgeToken != "" && authorizeBridge(r, cfg.BridgeToken) {
		return true
	}
	if len(cfg.APIKeys) > 0 && authorize(r, cfg.APIKeys) {
		return true
	}
	return false
}

// Ensure unused import guard is never triggered if gemini helpers evolve.
var _ = gemini.LoadDiscoveryTemplate
