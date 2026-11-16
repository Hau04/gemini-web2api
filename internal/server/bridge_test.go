package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestPartialToolFenceSuffixLen(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello world", 0},
		{"hello `", 1},
		{"hello ``", 2},
		{"hello ```", 3},
		{"hello ```tool_cal", 11},
		{"hello ```tool_call", 0},
	}
	for _, tc := range cases {
		if got := partialToolFenceSuffixLen(tc.in); got != tc.want {
			t.Errorf("partialToolFenceSuffixLen(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestGenerateFastTitleJSONUTF8(t *testing.T) {
	raw := generateFastTitleJSON("你好 世界")
	if !utf8.ValidString(raw) {
		t.Fatalf("generateFastTitleJSON returned invalid UTF-8: %q", raw)
	}
	var parsed struct {
		IsNewTopic bool   `json:"isNewTopic"`
		Title      string `json:"title"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, raw)
	}
	if parsed.Title != "你好 世界" {
		t.Errorf("got title %q, want %q", parsed.Title, "你好 世界")
	}
}

func TestBridgeStreamChatWithToolsAndValidDeltas(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	app := New(cfg, "test")
	handler := app.Handler()

	// Register active browser tab via heartbeat
	hbReq := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	hbRec := httptest.NewRecorder()
	handler.ServeHTTP(hbRec, hbReq)
	if hbRec.Code != http.StatusOK {
		t.Fatalf("heartbeat failed: %d", hbRec.Code)
	}

	// Simulate browser tab polling and streaming answer chunks
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 2*time.Second)
		if err != nil || req == nil {
			return
		}
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "Hello! "})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "How can I help you today?"})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Done: true})
	}()

	chatBody := `{
		"model": "gemini-4-hydrogen",
		"stream": true,
		"tools": [{"type":"function","function":{"name":"read_file","description":"Read a file"}}],
		"messages": [{"role":"user","content":"greetings"}]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	<-workerDone

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var combined strings.Builder
	sawDone := false
	sawStop := false

	scanner := bufio.NewScanner(bytes.NewReader(rec.Body.Bytes()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		if strings.Contains(payload, `"role":""`) {
			t.Errorf("SSE chunk must not contain empty role field: %s", payload)
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON chunk %q: %v", payload, err)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c0, _ := choices[0].(map[string]any)
		if delta, ok := c0["delta"].(map[string]any); ok {
			if content, ok := delta["content"].(string); ok {
				combined.WriteString(content)
			}
		}
		if fr, ok := c0["finish_reason"].(string); ok && fr == "stop" {
			sawStop = true
		}
	}

	if !sawDone {
		t.Errorf("expected data: [DONE] terminator")
	}
	if !sawStop {
		t.Errorf("expected finish_reason=stop")
	}
	if combined.String() != "Hello! How can I help you today?" {
		t.Errorf("got streamed content %q, want %q", combined.String(), "Hello! How can I help you today?")
	}
}

func TestBridgeStreamChatToolCallIndexed(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.Heartbeat("vertexaisearch.cloud.google.com", "tab_2")

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_2", 2*time.Second)
		if err != nil || req == nil {
			return
		}
		// Split opening ``` across chunks to verify fence buffering
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "```"})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"main.go\"}}\n```"})
		_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Done: true})
	}()

	chatBody := `{
		"model": "gemini-4-hydrogen",
		"stream": true,
		"tools": [{"type":"function","function":{"name":"read_file","description":"Read a file"}}],
		"messages": [{"role":"user","content":"read main.go"}]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	<-workerDone

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	sawToolCallIndex0 := false
	sawToolCallsFinish := false
	var leakedContent strings.Builder

	scanner := bufio.NewScanner(bytes.NewReader(rec.Body.Bytes()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON: %v", err)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		c0, _ := choices[0].(map[string]any)
		if delta, ok := c0["delta"].(map[string]any); ok {
			if content, ok := delta["content"].(string); ok {
				leakedContent.WriteString(content)
			}
			if tcs, ok := delta["tool_calls"].([]any); ok && len(tcs) == 1 {
				tc0, _ := tcs[0].(map[string]any)
				if idx, ok := tc0["index"].(float64); ok && idx == 0 {
					sawToolCallIndex0 = true
				}
			}
		}
		if fr, ok := c0["finish_reason"].(string); ok && fr == "tool_calls" {
			sawToolCallsFinish = true
		}
	}

	if leakedContent.String() != "" {
		t.Errorf("unexpected leaked content before tool call: %q", leakedContent.String())
	}
	if !sawToolCallIndex0 {
		t.Errorf("expected tool_calls delta with index=0")
	}
	if !sawToolCallsFinish {
		t.Errorf("expected finish_reason=tool_calls")
	}
}

func TestBridgeLocalPDFAutoUploadAndDedup(t *testing.T) {
	tmpDir := t.TempDir()
	pdfPath := filepath.Join(tmpDir, "annual report.pdf")
	goPath := filepath.Join(tmpDir, "main.go")
	secretPath := filepath.Join(tmpDir, "cookies_secret.pdf")

	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 sample pdf content"), 0600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	if err := os.WriteFile(goPath, []byte("package main"), 0600); err != nil {
		t.Fatalf("write go: %v", err)
	}
	if err := os.WriteFile(secretPath, []byte("%PDF-1.4 secret"), 0600); err != nil {
		t.Fatalf("write secret pdf: %v", err)
	}

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_pdf", "1")

	// 1. Remote (non-loopback) request must NOT auto-attach local files
	remoteDone := make(chan *bridge.Request, 1)
	go func() {
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_pdf", "1", 2*time.Second)
		remoteDone <- req
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "remote ok", Done: true})
		}
	}()

	bodyObj := map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": `Summarize "` + pdfPath + `" and ` + goPath + ` and ` + secretPath,
			},
		},
	}
	bodyBytes, _ := json.Marshal(bodyObj)

	remoteReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(bodyBytes))
	remoteReq.RemoteAddr = "203.0.113.10:44321"
	remoteReq.Header.Set("Content-Type", "application/json")
	recRemote := httptest.NewRecorder()
	handler.ServeHTTP(recRemote, remoteReq)

	gotRemote := <-remoteDone
	if gotRemote == nil {
		t.Fatal("expected bridge request for remote call")
	}
	if len(gotRemote.Attachments) != 0 {
		t.Fatalf("remote caller must not auto-upload local files, got %d", len(gotRemote.Attachments))
	}

	// 2. Localhost request auto-attaches ONLY "annual report.pdf" (skipping .go and sensitive secret filename)
	localDone1 := make(chan *bridge.Request, 1)
	go func() {
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_pdf", "1", 2*time.Second)
		localDone1 <- req
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "PDF summarized!", Done: true})
		}
	}()

	localReq1 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(bodyBytes))
	localReq1.RemoteAddr = "127.0.0.1:54321"
	localReq1.Header.Set("Content-Type", "application/json")
	recLocal1 := httptest.NewRecorder()
	handler.ServeHTTP(recLocal1, localReq1)

	gotLocal1 := <-localDone1
	if gotLocal1 == nil {
		t.Fatal("expected bridge request for localhost turn 1")
	}
	if len(gotLocal1.Attachments) != 1 {
		t.Fatalf("expected 1 safe PDF attachment, got %d: %+v", len(gotLocal1.Attachments), gotLocal1.Attachments)
	}
	if gotLocal1.Attachments[0].Filename != "annual report.pdf" || gotLocal1.Attachments[0].MIME != "application/pdf" {
		t.Errorf("unexpected attachment metadata: %+v", gotLocal1.Attachments[0])
	}

	// 3. Follow-up turn referencing the same PDF in the same tab session must NOT re-upload it
	localDone2 := make(chan *bridge.Request, 1)
	go func() {
		req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_pdf", "1", 2*time.Second)
		localDone2 <- req
		if req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "Follow up answer", Done: true})
		}
	}()

	followUpBytes, _ := json.Marshal(map[string]any{
		"model":  "gemini-4-hydrogen",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "user", "content": `Summarize "` + pdfPath + `"`},
			map[string]any{"role": "assistant", "content": "PDF summarized!"},
			map[string]any{"role": "user", "content": `Check page 2 of "` + pdfPath + `"`},
		},
	})
	localReq2 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(followUpBytes))
	localReq2.RemoteAddr = "127.0.0.1:54322"
	localReq2.Header.Set("Content-Type", "application/json")
	recLocal2 := httptest.NewRecorder()
	handler.ServeHTTP(recLocal2, localReq2)

	gotLocal2 := <-localDone2
	if gotLocal2 == nil {
		t.Fatal("expected bridge request for localhost turn 2")
	}
	if len(gotLocal2.Attachments) != 0 {
		t.Errorf("expected 0 duplicate attachments on follow-up turn, got %d", len(gotLocal2.Attachments))
	}
}

func TestBridgeDeltaContextAndConvReset(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	cfg.BridgeDeltaContext = true
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_delta", "1")

	tools := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "Read a file from disk",
			},
		},
	}

	runTurn := func(convGen string, msgs []any, replyText string) *bridge.Request {
		ch := make(chan *bridge.Request, 1)
		go func() {
			req, _ := app.Bridge.WaitForNextRequestForSession(context.Background(), "vertexaisearch.cloud.google.com", "tab_delta", convGen, 2*time.Second)
			ch <- req
			if req != nil {
				_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: replyText, Done: true})
			}
		}()

		body, _ := json.Marshal(map[string]any{
			"model":    "gemini-4-hydrogen",
			"stream":   false,
			"tools":    tools,
			"messages": msgs,
		})
		httpReq := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httpReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return <-ch
	}

	// Turn 1: full prompt with # Tool Use and system prompt
	msgs1 := []any{
		map[string]any{"role": "system", "content": "You are WorkBuddy."},
		map[string]any{"role": "user", "content": "Check main.go"},
	}
	req1 := runTurn("1", msgs1, "```tool_call\n{\"name\":\"read_file\",\"arguments\":{\"path\":\"main.go\"}}\n```")
	if req1 == nil || !strings.Contains(req1.Prompt, "# Tool Use") || !strings.Contains(req1.Prompt, "You are WorkBuddy.") {
		t.Fatalf("turn 1 should receive full prompt, got %+v", req1)
	}

	// Turn 2 (same conv_gen=1): compact delta prompt with ONLY the tool result
	msgs2 := append(append([]any{}, msgs1...),
		map[string]any{
			"role": "assistant",
			"tool_calls": []any{
				map[string]any{
					"id":   "call_1",
					"type": "function",
					"function": map[string]any{
						"name":      "read_file",
						"arguments": `{"path":"main.go"}`,
					},
				},
			},
		},
		map[string]any{"role": "tool", "name": "read_file", "content": "package main // ok"},
	)
	req2 := runTurn("1", msgs2, "main.go looks good!")
	if req2 == nil {
		t.Fatal("expected turn 2 bridge request")
	}
	if strings.Contains(req2.Prompt, "# Tool Use") || strings.Contains(req2.Prompt, "You are WorkBuddy.") {
		t.Errorf("turn 2 should use compact delta prompt without repeating system/tools, got %q", req2.Prompt)
	}
	if !strings.Contains(req2.Prompt, "[Tool result for read_file]: package main // ok") {
		t.Errorf("turn 2 delta prompt missing tool result, got %q", req2.Prompt)
	}

	// Turn 3 (after browser started New Chat -> conv_gen=2): must resend full prompt!
	app.Bridge.UpdateTabSession("vertexaisearch.cloud.google.com", "tab_delta", "2")
	msgs3 := append(append([]any{}, msgs2...),
		map[string]any{"role": "assistant", "content": "main.go looks good!"},
		map[string]any{"role": "user", "content": "Now explain it"},
	)
	req3 := runTurn("2", msgs3, "Here is the explanation.")
	if req3 == nil || !strings.Contains(req3.Prompt, "# Tool Use") || !strings.Contains(req3.Prompt, "You are WorkBuddy.") {
		t.Errorf("turn 3 after conv_gen change should resend full prompt, got %+v", req3)
	}
}

func TestBridgeAntiBanCooldown(t *testing.T) {
	hub := bridge.NewHub()
	hub.SetMinInterval(80 * time.Millisecond)
	hub.UpdateTabSession("gemini.google.com", "tab_cool", "1")

	dispatchTimes := make([]time.Time, 0, 2)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for i := 0; i < 2; i++ {
			req, err := hub.WaitForNextRequestForSession(context.Background(), "gemini.google.com", "tab_cool", "1", 2*time.Second)
			if err != nil || req == nil {
				return
			}
			dispatchTimes = append(dispatchTimes, time.Now())
			_ = hub.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
		}
	}()

	if _, err := hub.Generate(context.Background(), "first", "gemini-4-hydrogen"); err != nil {
		t.Fatalf("first generate failed: %v", err)
	}
	if _, err := hub.Generate(context.Background(), "second", "gemini-4-hydrogen"); err != nil {
		t.Fatalf("second generate failed: %v", err)
	}
	<-workerDone

	if len(dispatchTimes) != 2 {
		t.Fatalf("expected 2 dispatches, got %d", len(dispatchTimes))
	}
	gap := dispatchTimes[1].Sub(dispatchTimes[0])
	if gap < 65*time.Millisecond {
		t.Errorf("anti-ban cooldown not respected: gap = %v, want >= 65ms", gap)
	}
}

func TestBridgeOpenAIFileAttachmentForwardedWithoutCookies(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BridgeMinIntervalMs = 0
	app := New(cfg, "test")
	handler := app.Handler()

	app.Bridge.UpdateTabSession("gemini.google.com", "tab_file", "1")

	pollResult := make(chan map[string]any, 1)
	go func() {
		pollReq := httptest.NewRequest("GET", "/bridge/poll?tab=gemini.google.com&client=tab_file&conv_gen=1", nil)
		pollRec := httptest.NewRecorder()
		handler.ServeHTTP(pollRec, pollReq)
		var parsed map[string]any
		_ = json.Unmarshal(pollRec.Body.Bytes(), &parsed)
		pollResult <- parsed
		if id, ok := parsed["id"].(string); ok && id != "" {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: id, Text: "Read your attached PDF directly!", Done: true})
		}
	}()

	// Give poll goroutine a moment to register before sending chat request
	time.Sleep(25 * time.Millisecond)

	chatBody := `{
		"model": "gemini-4-hydrogen",
		"stream": false,
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Summarize this PDF"},
				{"type": "file", "file": {"filename": "deck.pdf", "file_data": "data:application/pdf;base64,JVBERi0xLjQK"}}
			]
		}]
	}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 without cookies in bridge mode, got %d: %s", rec.Code, rec.Body.String())
	}

	polled := <-pollResult
	atts, ok := polled["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("expected 1 attachment in /bridge/poll JSON, got %#v", polled["attachments"])
	}
	att0, _ := atts[0].(map[string]any)
	if att0["filename"] != "deck.pdf" || att0["mime"] != "application/pdf" {
		t.Errorf("unexpected polled attachment: %#v", att0)
	}
}
