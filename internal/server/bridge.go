package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
)

func (a *App) buildBridgeStatusPayload() map[string]any {
	connected := a.Bridge != nil && a.Bridge.IsConnected()
	tab := ""
	frozen := false
	degradedSec := 0
	var snap bridge.PacingSnapshot
	if a.Bridge != nil {
		tab = a.Bridge.TabName()
		frozen = a.Bridge.IsFrozen()
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
		"cookies_ready": a.cookiesReady(),
	}
	lastSync, authErr, authErrAt := a.CookieSync.snapshot()
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

	a.Bridge.SetFrozen(payload.Frozen)
	if payload.Frozen {
		a.Logf("Browser tab reported FROZEN (%s) — routing new requests via direct cookie mode until it resumes", payload.Tab)
	} else {
		a.Logf("Browser tab resumed (%s) — browser bridge available again", payload.Tab)
		a.Bridge.ClearDegraded()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"frozen":    a.Bridge.IsFrozen(),
		"connected": a.Bridge.IsConnected(),
	})
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
