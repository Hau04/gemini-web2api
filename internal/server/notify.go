package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// buildNotifyPayload renders the webhook request body & content type for the
// configured notify_format:
//   - "ntfy"    (default): raw text body + Title header (https://ntfy.sh/<topic>)
//   - "discord":           {"content": "..."} (also works for Slack-compatible URLs)
//   - "slack":             {"text": "..."}
//   - "generic":           {"text": "...", "title": "..."}
func buildNotifyPayload(format, title, msg string) (body []byte, contentType string, headers map[string]string) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "discord":
		b, _ := json.Marshal(map[string]string{"content": fmt.Sprintf("**%s**\n%s", title, msg)})
		return b, "application/json", nil
	case "slack":
		b, _ := json.Marshal(map[string]string{"text": fmt.Sprintf("*%s*\n%s", title, msg)})
		return b, "application/json", nil
	case "generic":
		b, _ := json.Marshal(map[string]string{"title": title, "text": msg})
		return b, "application/json", nil
	default: // ntfy
		return []byte(msg), "text/plain; charset=utf-8", map[string]string{"Title": title, "Priority": "high", "Tags": "warning,gemini"}
	}
}

// notifyAuthExpiry pushes a "session expired" alert to the configured webhook
// (config notify_webhook / notify_format). Runs async with a short timeout and
// never blocks or fails the request path.
func (a *App) notifyAuthExpiry(msg string) {
	webhook := strings.TrimSpace(a.Cfg.NotifyWebhook)
	if webhook == "" {
		return
	}
	format := a.Cfg.NotifyFormat
	title := "gemini-web2api: Gemini session expired"

	go func() {
		body, contentType, extraHeaders := buildNotifyPayload(format, title, msg)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
		if err != nil {
			a.Logf("Notify webhook error: %v", err)
			return
		}
		req.Header.Set("Content-Type", contentType)
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		resp, err := a.HTTPClient.Do(req)
		if err != nil {
			a.Logf("Notify webhook error: %v", err)
			return
		}
		_ = resp.Body.Close()
		a.Logf("Session-expiry notification sent via webhook (%s -> %d)", format, resp.StatusCode)
	}()
}

// onGeminiAuthError is wired into the direct-mode client: it records the failure
// for status reporting and pushes a webhook notification once per occurrence
// (a fresh cookie sync clears the error and re-arms notifications).
func (a *App) onGeminiAuthError(msg string) {
	if a.CookieSync == nil {
		return
	}
	if a.CookieSync.RecordAuthError(msg) {
		a.Logf("Session auth error recorded: %s", msg)
		a.notifyAuthExpiry(msg + " — open gemini.google.com in your browser and the extension will re-sync automatically.")
	}
}
