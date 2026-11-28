package server

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

type App struct {
	AccountID  string
	Cfg        config.Config
	Gem        *gemini.Client
	Tokens     *multimodal.TokenCache
	Bridge     *bridge.Hub
	CookieSync *cookieSyncState
	Accounts   *AccountManager
	HTTPClient *http.Client
	Logf       func(format string, args ...any)
	Version    string

	coreOnce sync.Once
	core     http.Handler

	// modelState is the last model-selection outcome reported by the extension.
	modelStateMu sync.Mutex
	modelState   bridgeModelState

	// responseConversations links Responses API ids to a stable client chat id,
	// allowing previous_response_id chains to remain on the same Gemini thread.
	responseConversationMu  sync.Mutex
	responseConversations   map[string]responseConversationEntry

	// mediaGenerate is a test seam for the media endpoints; nil means "call the
	// real Gemini client".
	mediaGenerate func(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error)
}

func createHTTPClient(cfg config.Config) *http.Client {
	transport := &http.Transport{}
	if cfg.Proxy != "" {
		if proxyURL, err := url.Parse(cfg.Proxy); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}
}

func New(cfg config.Config, version string) *App {
	app := newAccountApp(cfg, version, DefaultAccountID)
	app.Accounts = newAccountManager(app, resolveAccountsDir(app.Cfg))
	return app
}

func newAccountApp(cfg config.Config, version, accountID string) *App {
	// Default the cookie file so browser-pushed session cookies (extension auto
	// sync) and the manual "Download cookie.json" workflow work out of the box.
	if cfg.CookieFile == "" {
		cfg.CookieFile = "cookie.json"
	}
	gemClient := gemini.NewClient(cfg)

	prefix := ""
	if accountID != "" && accountID != DefaultAccountID {
		prefix = "[" + accountID + "] "
	}
	logFn := func(format string, args ...any) {
		if cfg.LogRequests {
			log.Printf(prefix+format, args...)
		}
	}

	httpClient := createHTTPClient(cfg)
	hub := bridge.NewHub()
	hub.SetPacing(
		time.Duration(cfg.BridgeMinIntervalMs)*time.Millisecond,
		time.Duration(cfg.BridgeJitterMs)*time.Millisecond,
		cfg.BridgeMaxPer10Min,
	)
	hub.SetHourlyLimit(cfg.BridgeMaxPerHour)
	hub.SetNoContentTimeout(time.Duration(cfg.BridgeNoContentTimeoutSec) * time.Second)
	hub.SetPickupTimeout(time.Duration(cfg.BridgePickupTimeoutSec) * time.Second)
	if cfg.BridgePacingStateFile != "" {
		hub.SetPacingStateFile(cfg.BridgePacingStateFile)
	}

	app := &App{
		AccountID:  accountID,
		Cfg:        cfg,
		Gem:        gemClient,
		Tokens:     multimodal.NewTokenCache(cfg, gemClient.Cookies, httpClient),
		Bridge:     hub,
		CookieSync: newCookieSyncState(),
		HTTPClient: httpClient,
		Logf:       logFn,
		Version:    version,
	}
	gemClient.OnAuthError = app.onGeminiAuthError

	return app
}

func (a *App) registerAccountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.handleHealth)
	mux.HandleFunc("GET /v1/models", a.handleModels)
	mux.HandleFunc("GET /v1/models/{id}", a.handleModelGet)
	mux.HandleFunc("GET /v1/model-info", a.handleModelInfo)
	mux.HandleFunc("POST /v1/model-probe", a.handleModelProbe)
	mux.HandleFunc("POST /v1/chat/completions", a.handleChat)
	mux.HandleFunc("POST /v1/responses", a.handleResponses)
	mux.HandleFunc("POST /v1/images/generations", a.handleImageGenerations)
	mux.HandleFunc("POST /v1/videos/generations", a.handleVideoGenerations)

	mux.HandleFunc("GET /v1beta/models", a.handleGoogleModels)
	mux.HandleFunc("POST /v1beta/models/{target}", a.handleGoogleGenerate)

	mux.HandleFunc("GET /bridge/status", a.handleBridgeStatus)
	mux.HandleFunc("POST /bridge/pause", a.handleBridgePause)
	mux.HandleFunc("POST /bridge/heartbeat", a.handleBridgeHeartbeat)
	mux.HandleFunc("POST /bridge/modelstate", a.handleBridgeModelState)
	mux.HandleFunc("GET /bridge/poll", a.handleBridgePoll)
	mux.HandleFunc("POST /bridge/chunk", a.handleBridgeChunk)
	mux.HandleFunc("POST /bridge/frozen", a.handleBridgeFrozen)
	mux.HandleFunc("POST /bridge/cookies", a.handleBridgeCookies)
	mux.HandleFunc("GET /bridge/cookie-status", a.handleCookieStatus)
	mux.HandleFunc("POST /bridge/chatmode", a.handleBridgeChatMode)
	mux.HandleFunc("POST /bridge/reqtemplate", a.handleBridgeReqTemplate)
	mux.HandleFunc("GET /bridge/reqtemplate", a.handleBridgeReqTemplate)
	mux.HandleFunc("POST /bridge/uploadtemplate", a.handleBridgeUploadTemplate)
	mux.HandleFunc("GET /bridge/uploadtemplate", a.handleBridgeUploadTemplate)
}

func (a *App) coreHandler() http.Handler {
	a.coreOnce.Do(func() {
		mux := http.NewServeMux()
		a.registerAccountRoutes(mux)
		a.core = a.withAuthAndLogging(mux)
	})
	return a.core
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.registerAccountRoutes(mux)

	// Account Management GUI and REST API
	mux.HandleFunc("GET /ui", a.handleAccountUI)
	mux.HandleFunc("GET /ui/", a.handleAccountUI)
	mux.HandleFunc("GET /api/accounts", a.handleAccountsList)
	mux.HandleFunc("POST /api/accounts", a.handleAccountsCreate)
	mux.HandleFunc("GET /api/accounts/{id}", a.handleAccountGet)
	mux.HandleFunc("DELETE /api/accounts/{id}", a.handleAccountDelete)
	mux.HandleFunc("PUT /api/accounts/{id}/cookies", a.handleAccountCookiesUpdate)
	mux.HandleFunc("POST /api/accounts/{id}/cookies", a.handleAccountCookiesUpdate)
	mux.HandleFunc("DELETE /api/accounts/{id}/cookies", a.handleAccountCookiesClear)

	rootHandler := a.withAuthAndLogging(mux)

	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Path-based account isolation: /a/{account_id}/v1/... or /a/{account_id}/bridge/...
		if accountID, remainder, ok := splitAccountPath(r.URL.Path); ok {
			if a.Accounts == nil {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": map[string]any{
						"message": "account management is not enabled",
						"type":    "invalid_request_error",
						"code":    "account_not_found",
					},
				})
				return
			}
			targetApp, normID, found := a.Accounts.Resolve(accountID)
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error": map[string]any{
						"message": "account \"" + accountID + "\" not found — create it at /ui or POST /api/accounts",
						"type":    "invalid_request_error",
						"code":    "account_not_found",
					},
				})
				return
			}
			r2 := r.Clone(r.Context())
			u2 := *r.URL
			u2.Path = remainder
			u2.RawPath = ""
			r2.URL = &u2
			w.Header().Set("X-Gemini-Account", normID)
			targetApp.coreHandler().ServeHTTP(w, r2)
			return
		}

		// 2. Optional header/query selector on unprefixed API/bridge routes.
		if a.Accounts != nil && (strings.HasPrefix(r.URL.Path, "/v1") || strings.HasPrefix(r.URL.Path, "/bridge/")) {
			sel := strings.TrimSpace(r.Header.Get("X-Gemini-Account"))
			if sel == "" {
				sel = strings.TrimSpace(r.URL.Query().Get("account"))
			}
			if sel != "" {
				targetApp, normID, found := a.Accounts.Resolve(sel)
				if !found {
					writeJSON(w, http.StatusNotFound, map[string]any{
						"error": map[string]any{
							"message": "account \"" + sel + "\" not found — create it at /ui or POST /api/accounts",
							"type":    "invalid_request_error",
							"code":    "account_not_found",
						},
					})
					return
				}
				w.Header().Set("X-Gemini-Account", normID)
				targetApp.coreHandler().ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-Gemini-Account", DefaultAccountID)
		}

		rootHandler.ServeHTTP(w, r)
	})

	return a.withCORS(router)
}
