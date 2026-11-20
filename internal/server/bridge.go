package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
)

func (a *App) buildBridgeStatusPayload() map[string]any {
	connected := a.Bridge != nil && a.Bridge.IsConnected()
	tab := ""
	frozen := false
	degradedSec := 0
	chatMode := "auto"
	var snap bridge.PacingSnapshot
	if a.Bridge != nil {
		tab = a.Bridge.TabName()
		frozen = a.Bridge.IsFrozen()
		chatMode = a.Bridge.ChatMode()
		snap = a.Bridge.DetailedPacingStats()
		if rem := a.Bridge.DegradedRemaining(); rem > 0 {
			degradedSec = int(rem.Round(time.Second).Seconds())
		}
	}
	payload := map[string]any{
		"connected":     connected,
		"tab":           tab,
		"paused":        snap.Paused,
		"frozen":        frozen,
		"degraded_sec":  degradedSec,
		"cooldown_sec":  snap.CooldownSec,
		"recent_10m":    snap.Recent10m,
		"max_per_10m":   snap.MaxPer10m,
		"recent_1h":     snap.Recent1h,
		"max_per_hour":  snap.MaxPerHour,
		"chat_mode":     chatMode,
		"cookies_ready": a.cookiesReady(),
	}
	lastSync, authErr, authErrAt := a.CookieSync.snapshot()
	if a.Gem != nil {
		payload["configured_base_url"] = a.Gem.Cfg.BaseURL
		if a.Gem.Cookies != nil {
			if info, err := a.Gem.Cookies.Load(); err == nil && info.BaseURL != "" {
				payload["synced_origin"] = info.BaseURL
			}
		}
	}
	if !lastSync.IsZero() {
		payload["cookie_last_sync"] = lastSync.UTC().Format(time.RFC3339)
		payload["cookie_last_sync_age_sec"] = int(time.Since(lastSync).Seconds())
	}
	if authErr != "" {
		payload["cookie_auth_error"] = authErr
		payload["cookie_auth_error_at"] = authErrAt.UTC().Format(time.RFC3339)
	}
	return payload
}

func (a *App) handleBridgeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.buildBridgeStatusPayload())
}

func (a *App) handleBridgePause(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}
	var payload struct {
		Paused *bool `json:"paused"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	if payload.Paused != nil {
		a.Bridge.SetPaused(*payload.Paused)
	} else {
		a.Bridge.SetPaused(!a.Bridge.IsPaused())
	}
	if a.Bridge.IsPaused() {
		a.Logf("Browser bridge PAUSED by user via /bridge/pause")
	} else {
		a.Logf("Browser bridge RESUMED by user via /bridge/pause")
	}
	writeJSON(w, http.StatusOK, a.buildBridgeStatusPayload())
}

func (a *App) handleBridgeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	var payload struct {
		Tab      string `json:"tab"`
		ClientID string `json:"client_id"`
		ConvGen  string `json:"conv_gen"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)

	tabName := payload.Tab
	if tabName == "" {
		tabName = r.URL.Query().Get("tab")
	}
	clientID := payload.ClientID
	if clientID == "" {
		clientID = r.URL.Query().Get("client")
	}
	convGen := payload.ConvGen
	if convGen == "" {
		convGen = r.URL.Query().Get("conv_gen")
	}

	a.Bridge.UpdateTabSession(tabName, clientID, convGen)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleBridgePoll(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	tabName := r.URL.Query().Get("tab")
	if tabName == "" {
		tabName = "active-browser-tab"
	}
	clientID := r.URL.Query().Get("client")
	convGen := r.URL.Query().Get("conv_gen")

	req, err := a.Bridge.WaitForNextRequestForSession(r.Context(), tabName, clientID, convGen, 20*time.Second)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if req == nil {
		writeJSON(w, http.StatusOK, map[string]any{"timeout": true})
		return
	}

	resp := map[string]any{
		"id":     req.ID,
		"prompt": req.Prompt,
		"model":  req.Model,
	}
	if len(req.Attachments) > 0 {
		resp["attachments"] = req.Attachments
	}
	if req.NewChat {
		resp["new_chat"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleBridgeFrozen receives freeze/resume lifecycle events from the content
// script. Chrome freezes background tabs (Memory Saver / Energy Saver), which
// halts the content script entirely — while frozen, requests must route via the
// direct cookie mode instead of waiting on a tab that cannot respond.
func (a *App) handleBridgeFrozen(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	var payload struct {
		Frozen   bool   `json:"frozen"`
		ClientID string `json:"client_id"`
		Tab      string `json:"tab"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)

	changed := a.Bridge.IsFrozen() != payload.Frozen
	a.Bridge.SetFrozen(payload.Frozen)
	if payload.Frozen {
		if changed {
			a.Logf("Browser tab reported FROZEN (%s) — routing new requests via direct cookie mode until it resumes", payload.Tab)
		}
	} else {
		if changed {
			a.Logf("Browser tab resumed (%s) — browser bridge available again", payload.Tab)
		}
		a.Bridge.ClearDegraded()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"frozen":    a.Bridge.IsFrozen(),
		"connected": a.Bridge.IsConnected(),
	})
}

// handleBridgeChatMode sets the chat-session override from the extension popup:
// "auto" (default heuristic), "new" (every request starts a fresh chat), or
// "continue" (requests append to the chat currently open in the browser tab —
// e.g. an old chat the user clicked open from the history sidebar).
func (a *App) handleBridgeChatMode(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	var payload struct {
		Mode string `json:"mode"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)

	a.Bridge.SetChatMode(payload.Mode)
	mode := a.Bridge.ChatMode()
	a.Logf("Chat mode set to %q via extension popup", mode)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chat_mode": mode})
}

func (a *App) handleBridgeChunk(w http.ResponseWriter, r *http.Request) {
	if a.Bridge == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "bridge not initialized"})
		return
	}

	var chunk bridge.Chunk
	if err := json.NewDecoder(r.Body).Decode(&chunk); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid chunk payload"})
		return
	}

	if chunk.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing chunk id"})
		return
	}

	if err := a.Bridge.SubmitChunk(chunk); err != nil {
		writeJSON(w, http.StatusGone, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBridgeReqTemplate stores (or reports) a captured Discovery Engine
// streamAssist request template from the extension. On Gemini Enterprise
// origins the server replays this template with fresh cookies and a new prompt
// — the direct cookie mode path that works with the browser closed.
func (a *App) handleBridgeReqTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		tpl := gemini.LoadDiscoveryTemplate(a.cookieFilePath())
		if tpl == nil {
			writeJSON(w, http.StatusOK, map[string]any{"have_template": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"have_template": true,
			"url":           tpl.URL,
			"body_size":     len(tpl.Body),
			"captured_at":   tpl.CapturedAt,
		})
		return
	}

	var payload struct {
		URL        string            `json:"url"`
		Method     string            `json:"method"`
		Headers    map[string]string `json:"headers"`
		Body       string            `json:"body"`
		PromptHint string            `json:"prompt_hint"`
		Tab        string            `json:"tab"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" || strings.TrimSpace(payload.Body) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing url or body"})
		return
	}
	if len(payload.Body) > 2<<20 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "template body too large"})
		return
	}
	if !strings.HasPrefix(payload.URL, "https://") ||
		(!strings.Contains(payload.URL, "googleapis.com") && !strings.Contains(payload.URL, "google.com")) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "template url must be a Google https endpoint"})
		return
	}
	if payload.Method == "" {
		payload.Method = http.MethodPost
	}
	if payload.Headers == nil {
		payload.Headers = map[string]string{}
	}

	tpl := &gemini.DiscoveryTemplate{
		URL:        payload.URL,
		Method:     payload.Method,
		Headers:    payload.Headers,
		Body:       payload.Body,
		PromptHint: strings.TrimSpace(payload.PromptHint),
		CapturedAt: time.Now(),
	}
	if err := gemini.SaveDiscoveryTemplate(a.cookieFilePath(), tpl); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.Logf("Captured Discovery Engine request template from browser tab %q (%s, %d bytes) — Enterprise direct replay ready", payload.Tab, tpl.URL, len(tpl.Body))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleBridgeUploadTemplate stores (or reports) a captured Gemini Enterprise
// context-file upload request (Discovery Engine addContextFile). Replaying it
// with fresh cookies is what lets direct cookie mode attach files with the
// browser closed, where the consumer content-push pipeline does not exist.
func (a *App) handleBridgeUploadTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		tpl := gemini.LoadDiscoveryUploadTemplate(a.cookieFilePath())
		if tpl == nil {
			writeJSON(w, http.StatusOK, map[string]any{"have_template": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"have_template": true,
			"url":           tpl.URL,
			"body_size":     len(tpl.Body),
			"captured_at":   tpl.CapturedAt,
		})
		return
	}

	var payload struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Tab     string            `json:"tab"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON payload"})
		return
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" || strings.TrimSpace(payload.Body) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing url or body"})
		return
	}
	if len(payload.Body) > 4<<20 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "template body too large"})
		return
	}
	if !strings.HasPrefix(payload.URL, "https://") ||
		(!strings.Contains(payload.URL, "googleapis.com") && !strings.Contains(payload.URL, "google.com")) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "template url must be a Google https endpoint"})
		return
	}
	hasPlaceholder := strings.Contains(payload.Body, gemini.UploadFileB64Placeholder) ||
		strings.Contains(payload.Body, gemini.UploadFileNamePlaceholder)
	if !hasPlaceholder {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "template body has no file placeholder — the extension could not identify the uploaded file fields"})
		return
	}
	if payload.Method == "" {
		payload.Method = http.MethodPost
	}
	if payload.Headers == nil {
		payload.Headers = map[string]string{}
	}

	tpl := &gemini.DiscoveryUploadTemplate{
		URL:        payload.URL,
		Method:     payload.Method,
		Headers:    payload.Headers,
		Body:       payload.Body,
		CapturedAt: time.Now(),
	}
	if err := gemini.SaveDiscoveryUploadTemplate(a.cookieFilePath(), tpl); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	a.Logf("Captured Gemini Enterprise context-file upload template from browser tab %q (%s, %d bytes) — browserless attachments ready", payload.Tab, tpl.URL, len(tpl.Body))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
