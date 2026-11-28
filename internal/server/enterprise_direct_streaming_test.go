package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func newDirectEnterpriseTestApp(t *testing.T, upstream http.Handler) (*App, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)

	cookieFile := filepath.Join(t.TempDir(), "cookie.json")
	cookiePayload := fmt.Sprintf(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc","base_url":%q}`, srv.URL)
	if err := os.WriteFile(cookieFile, []byte(cookiePayload), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.BaseURL = srv.URL
	cfg.RetryAttempts = 1
	cfg.RetryDelaySec = 0
	cfg.RequestTimeoutSec = 5
	cfg.DiscoveryEngineConfigID = "cfg-1"
	cfg.DiscoveryEngineEndpoint = srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k"
	cfg.DiscoveryEngineUploadEndpoint = srv.URL + "/v1alpha/locations/global/widgetAddContextFile?key=k"
	cfg.DiscoveryEngineProbeModels = false

	app := New(cfg, "test")
	app.Gem.HTTP = srv.Client()
	app.Gem.MarkBardUnsupported()
	app.CookieSync.recordSync()
	return app, srv
}

func TestDirectEnterpriseStreamingWithToolsStreamsSSEAndParsesToolCall(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		flusher, _ := w.(http.Flusher)
		frames := []string{
			`[{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS","replies":[{"groundedContent":{"content":{"thought":true,"text":"planning tool call"}}}]}}},`,
			`{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS","replies":[{"groundedContent":{"content":{"text":"Checking files first.\n"}}}]}}},`,
			"{\"streamAssistResponse\":{\"answer\":{\"state\":\"IN_PROGRESS\",\"replies\":[{\"groundedContent\":{\"content\":{\"text\":\"```tool_call\\n{\\\"name\\\":\\\"read_file\\\",\"}}}]}}},",
			"{\"streamAssistResponse\":{\"answer\":{\"state\":\"SUCCEEDED\",\"replies\":[{\"groundedContent\":{\"content\":{\"text\":\"\\\"arguments\\\":{\\\"path\\\":\\\"main.go\\\"}}\\n```\"}}}]}}}]",
		}
		for _, frame := range frames {
			_, _ = w.Write([]byte(frame))
			if flusher != nil {
				flusher.Flush()
			}
		}
	})

	app, _ := newDirectEnterpriseTestApp(t, upstream)
	handler := app.Handler()

	reqBody := map[string]any{
		"model":  "gemini-4-neon",
		"stream": true,
		"messages": []map[string]any{
			{"role": "user", "content": "Inspect main.go"},
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "read_file",
					"description": "Read a file",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}
	rawBody, _ := json.Marshal(reqBody)

	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	var (
		gotContent   strings.Builder
		gotToolCalls []models.OpenAIToolCall
		gotFinish    string
		sawDone      bool
	)
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk models.OpenAIChatResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON chunk %q: %v", payload, err)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta != nil {
				if s, ok := ch.Delta.Content.(string); ok {
					gotContent.WriteString(s)
				}
				gotToolCalls = append(gotToolCalls, ch.Delta.ToolCalls...)
			}
			if ch.FinishReason != nil {
				gotFinish = *ch.FinishReason
			}
		}
	}

	if !sawDone {
		t.Error("missing [DONE] marker in SSE stream")
	}
	if !strings.Contains(gotContent.String(), "Checking files first.") {
		t.Errorf("streamed leading content = %q, want 'Checking files first.'", gotContent.String())
	}
	if strings.Contains(gotContent.String(), "tool_call") {
		t.Errorf("raw tool_call fence leaked into streamed content: %q", gotContent.String())
	}
	if len(gotToolCalls) != 1 || gotToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("parsed tool calls = %+v, want 1 read_file call", gotToolCalls)
	}
	if !strings.Contains(gotToolCalls[0].Function.Arguments, `"main.go"`) {
		t.Errorf("tool arguments = %q, want main.go", gotToolCalls[0].Function.Arguments)
	}
	if gotFinish != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", gotFinish)
	}
}

func TestDirectEnterpriseStreamingPreStreamFailureReturns502JSON(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"INTERNAL"}}`))
	})

	app, _ := newDirectEnterpriseTestApp(t, upstream)
	handler := app.Handler()

	rawBody := `{"model":"gemini-4-neon","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"ping","parameters":{"type":"object"}}}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 Bad Gateway when upstream fails before SSE starts; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestDirectEnterpriseMultiTurnDeltaContextShrinksFollowUpPromptsAndRecoversOnEviction(t *testing.T) {
	var (
		mu             sync.Mutex
		creates        int
		streamSessions []string
		streamQueries  []string
		session1Full   bool
	)

	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "widgetCreateSession"):
			mu.Lock()
			creates++
			n := creates
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/%d"}}}`, n)))

		case strings.Contains(r.URL.Path, "widgetAddContextFile"):
			raw, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			upSess := ""
			if addReq, ok := payload["addContextFileRequest"].(map[string]any); ok {
				upSess, _ = addReq["session"].(string)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"addContextFileResponse":{"session":%q,"fileId":"f-1"}}`, upSess)))

		default:
			raw, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			sReq, _ := payload["streamAssistRequest"].(map[string]any)
			sess, _ := sReq["session"].(string)
			query := ""
			if q, ok := sReq["query"].(map[string]any); ok {
				if parts, ok := q["parts"].([]any); ok && len(parts) > 0 {
					if p0, ok := parts[0].(map[string]any); ok {
						query, _ = p0["text"].(string)
					}
				}
			}
			mu.Lock()
			streamSessions = append(streamSessions, sess)
			streamQueries = append(streamQueries, query)
			full := session1Full
			mu.Unlock()

			if full && strings.HasSuffix(sess, "sessions/1") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`[{"error":{"code":400,"message":"Invalid argument","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"PROMPT_TOO_LARGE"}]}}]`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ack"}}}]}}}`))
		}
	})

	app, _ := newDirectEnterpriseTestApp(t, upstream)
	handler := app.Handler()

	bigContext := strings.Repeat("LARGE_REPO_FILE_LINE_0123456789\n", 1500) // ~48 KB
	msgs := []map[string]any{
		{"role": "system", "content": "You are a coding assistant. Current time: 2026-11-28T06:00:00Z"},
		{"role": "user", "content": "Here is the file:\n" + bigContext},
	}

	sendTurn := func(messages []map[string]any) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"model":    "gemini-4-neon",
			"stream":   false,
			"messages": messages,
		})
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("chat turn failed (%d): %s", rec.Code, rec.Body.String())
		}
	}

	// Turn 1 sends the full ~48 KB context into sessions/1.
	sendTurn(msgs)

	// Turn 2 updates system clock timestamp (volatile!) and appends assistant + short user question.
	msgs[0]["content"] = "You are a coding assistant. Current time: 2026-11-28T06:05:00Z"
	msgs = append(msgs,
		map[string]any{"role": "assistant", "content": "ack"},
		map[string]any{"role": "user", "content": "Now summarize in one sentence."},
	)
	sendTurn(msgs)

	// Turn 3: sessions/1 hits PROMPT_TOO_LARGE, so direct mode must fall back from
	// delta to FullPrompt and evict sessions/1.
	mu.Lock()
	session1Full = true
	mu.Unlock()
	msgs = append(msgs,
		map[string]any{"role": "assistant", "content": "ack"},
		map[string]any{"role": "user", "content": "What about error handling?"},
	)
	sendTurn(msgs)

	// Turn 4: after eviction, must open a fresh sessions/2 instead of touching sessions/1.
	msgs = append(msgs,
		map[string]any{"role": "assistant", "content": "ack"},
		map[string]any{"role": "user", "content": "Continue on the fresh session."},
	)
	sendTurn(msgs)

	mu.Lock()
	defer mu.Unlock()

	if len(streamQueries) != 5 {
		t.Fatalf("expected 5 streamAssist calls (T1, T2 delta, T3 delta->400, T3 full retry, T4 fresh session), got %d", len(streamQueries))
	}
	if len(streamQueries[0]) < 40000 {
		t.Errorf("turn 1 query len = %d, expected full ~48 KB prompt", len(streamQueries[0]))
	}
	if len(streamQueries[1]) > 2000 {
		t.Errorf("turn 2 query len = %d, expected compact delta prompt (<2000 chars) instead of resending 48 KB", len(streamQueries[1]))
	}
	if strings.Contains(streamQueries[1], "LARGE_REPO_FILE_LINE") {
		t.Error("turn 2 delta prompt must not resend Turn 1's 48 KB payload")
	}
	if len(streamQueries[2]) > 2000 {
		t.Errorf("turn 3 initial attempt len = %d, expected delta prompt before knowing sessions/1 was full", len(streamQueries[2]))
	}
	if !strings.Contains(streamQueries[3], "LARGE_REPO_FILE_LINE") {
		t.Error("turn 3 retry after session eviction must restore FullPrompt so conversation context is preserved")
	}
	if !strings.HasSuffix(streamSessions[4], "sessions/2") {
		t.Errorf("turn 4 session = %q, want fresh sessions/2 after evicting sessions/1", streamSessions[4])
	}
}

func TestDirectEnterpriseAuthFailureMarksCookieSyncUnhealthy(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"UNAUTHENTICATED"}}`))
	})

	app, _ := newDirectEnterpriseTestApp(t, upstream)
	handler := app.Handler()

	// Sanity check: /bridge/cookie-status has no auth_error after extension sync.
	statusReq1 := httptest.NewRequest("GET", "/bridge/cookie-status", nil)
	statusRec1 := httptest.NewRecorder()
	handler.ServeHTTP(statusRec1, statusReq1)
	if statusRec1.Code != http.StatusOK {
		t.Fatalf("cookie-status returned %d", statusRec1.Code)
	}
	var statusBefore map[string]any
	_ = json.Unmarshal(statusRec1.Body.Bytes(), &statusBefore)
	if statusBefore["auth_error"] != nil {
		t.Fatalf("expected auth_error=nil before 401, got %+v", statusBefore)
	}

	rawBody := `{"model":"gemini-4-neon","stream":false,"messages":[{"role":"user","content":"hello"}]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	statusReq2 := httptest.NewRequest("GET", "/bridge/cookie-status", nil)
	statusRec2 := httptest.NewRecorder()
	handler.ServeHTTP(statusRec2, statusReq2)
	var statusAfter map[string]any
	_ = json.Unmarshal(statusRec2.Body.Bytes(), &statusAfter)
	authErr, _ := statusAfter["auth_error"].(string)
	if !strings.Contains(authErr, "401") {
		t.Errorf("/bridge/cookie-status auth_error = %v, want mention of HTTP 401", statusAfter["auth_error"])
	}
}
