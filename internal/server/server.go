package server

import (
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

type App struct {
	Cfg        config.Config
	Gem        *gemini.Client
	Tokens     *multimodal.TokenCache
	Bridge     *bridge.Hub
	CookieSync *cookieSyncState
	HTTPClient *http.Client
	Logf       func(format string, args ...any)
	Version    string

	// modelState is the last model-selection outcome reported by the extension.
	modelStateMu sync.Mutex
	modelState   bridgeModelState
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
	// Default the cookie file so browser-pushed session cookies (extension auto
	// sync) and the manual "Download cookie.json" workflow work out of the box.
	if cfg.CookieFile == "" {
		cfg.CookieFile = "cookie.json"
	}
	gemClient := gemini.NewClient(cfg)

	logFn := func(format string, args ...any) {
		if cfg.LogRequests {
			log.Printf(format, args...)
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

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", a.handleHealth)
	mux.HandleFunc("GET /v1/models", a.handleModels)
	mux.HandleFunc("GET /v1/models/{id}", a.handleModelGet)
	mux.HandleFunc("GET /v1/model-info", a.handleModelInfo)
	mux.HandleFunc("POST /v1/chat/completions", a.handleChat)
	mux.HandleFunc("POST /v1/responses", a.handleResponses)

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

	handler := a.withAuthAndLogging(mux)
	handler = a.withCORS(handler)

	return handler
}
