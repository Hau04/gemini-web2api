package gemini

import (
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
)

// tempCookieDir keeps cookie files out of the repository.
func tempCookieDir() string {
	dir, err := os.MkdirTemp("", "gemini-prompt-limit")
	if err != nil {
		return "."
	}
	return dir
}

// configForEndpoint is a cookie-authenticated Enterprise client for endpoint.
// Session ids are disabled so the test server only ever sees prompt traffic.
func configForEndpoint(endpoint string) config.Config {
	return config.Config{
		CookieFile:              filepath.Join(tempCookieDir(), "cookie.json"),
		BaseURL:                 endpoint,
		RetryAttempts:           3,
		RetryDelaySec:           0,
		DiscoveryEngineConfigID: "cfg-1",
		DiscoveryEngineEndpoint: endpoint + "/v1alpha/locations/global/widgetStreamAssist?key=k",
	}
}

// promptLimitServer rejects any query longer than limit chars the way the tenant
// does, and records every query it received.
func promptLimitServer(t *testing.T, limit int) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		query := ""
		var payload map[string]any
		if json.Unmarshal(raw, &payload) == nil {
			if req, ok := payload["streamAssistRequest"].(map[string]any); ok {
				if q, ok := req["query"].(map[string]any); ok {
					if parts, ok := q["parts"].([]any); ok && len(parts) > 0 {
						if part, ok := parts[0].(map[string]any); ok {
							query, _ = part["text"].(string)
						}
					}
				}
			}
		}
		if query == "" {
			// Not a streamAssist body (e.g. widgetCreateSession): not a prompt.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		mu.Lock()
		prompts = append(prompts, query)
		mu.Unlock()
		if len(query) > limit {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`[{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"PROMPT_TOO_LARGE","domain":"discoveryengine.googleapis.com"}]}}]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &prompts
}

// The reported failure: a 305 KB prompt is refused outright. The turn must still
// be answered, by dropping the oldest turns instead of the whole request.
func TestPromptTooLargeIsRetriedWithATrimmedPrompt(t *testing.T) {
	srv, prompts := promptLimitServer(t, 120000)

	c := NewClient(configForEndpoint(srv.URL))
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	head := "INSTRUCTIONS: always answer in English.\n"
	tail := "[User]: what is next?\n"
	prompt := head + strings.Repeat("old conversation turn\n", 12000) + tail

	if err := c.generateViaDiscoveryDirectStream(prompt, nil, "", ConversationInfo{}, "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("the turn should survive PROMPT_TOO_LARGE, got %v", err)
	}

	got := *prompts
	if len(got) < 2 {
		t.Fatalf("expected a retry after the refusal, got %d attempt(s)", len(got))
	}
	last := got[len(got)-1]
	if len(last) > 120000 {
		t.Errorf("the retried prompt is still %d chars, over the tenant limit", len(last))
	}
	if !strings.Contains(last, "always answer in English") {
		t.Error("retrying must not lose the instructions")
	}
	if !strings.Contains(last, "what is next?") {
		t.Error("retrying must not lose the current question")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "rejected the prompt as too large") {
		t.Errorf("the refusal should be logged, got:\n%s", joined)
	}
	// The ceiling is now known, so the next turn is trimmed before it is sent.
	if c.promptCeiling() == 0 {
		t.Error("the tenant's limit should have been remembered")
	}
}

// Once the ceiling is known, no doomed request is sent at all.
func TestKnownCeilingTrimsBeforeSending(t *testing.T) {
	srv, prompts := promptLimitServer(t, 50000)

	c := NewClient(configForEndpoint(srv.URL))
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}
	c.rememberPromptCeiling(50000)

	prompt := "INSTRUCTIONS: keep me.\n" + strings.Repeat("context\n", 20000) + "[User]: go\n"
	if err := c.generateViaDiscoveryDirectStream(prompt, nil, "", ConversationInfo{}, "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("a fitted prompt should be accepted, got %v", err)
	}
	got := *prompts
	if len(got) != 1 {
		t.Fatalf("a known ceiling must avoid a wasted round trip, got %d attempt(s)", len(got))
	}
	if len(got[0]) > 50000 {
		t.Errorf("prompt sent at %d chars, over the known ceiling", len(got[0]))
	}
}

// sessionTooLargeServer refuses every streamAssist that carries a session, the way
// a tenant does once the session's own history has grown past its limit.
func sessionTooLargeServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var prompts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if json.Unmarshal(raw, &payload) != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		req, ok := payload["streamAssistRequest"].(map[string]any)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		query := ""
		if q, ok := req["query"].(map[string]any); ok {
			if parts, ok := q["parts"].([]any); ok && len(parts) > 0 {
				if part, ok := parts[0].(map[string]any); ok {
					query, _ = part["text"].(string)
				}
			}
		}
		mu.Lock()
		prompts = append(prompts, query)
		mu.Unlock()
		if session, _ := req["session"].(string); session != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`[{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"PROMPT_TOO_LARGE"}]}}]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &prompts
}

// The reported failure: the session had grown too large, so every turn was
// refused no matter how much of the prompt was cut. Dropping the session keeps
// the turn — and the user's content — intact.
func TestPromptTooLargeDropsTheSessionFirst(t *testing.T) {
	srv, prompts := sessionTooLargeServer(t)

	c := NewClient(configForEndpoint(srv.URL))
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	prompt := "INSTRUCTIONS: keep me.\n" + strings.Repeat("context line\n", 20000) + "[User]: go\n"
	if err := c.generateViaDiscoveryDirectStream(prompt, nil, "", ConversationInfo{}, "gemini-4-argon", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("the turn should survive by dropping the oversized session, got %v", err)
	}

	got := *prompts
	if len(got) != 2 {
		t.Fatalf("expected one refused attempt plus one retry, got %d", len(got))
	}
	if len(got[1]) != len(got[0]) {
		t.Errorf("dropping the session must not trim the prompt: %d -> %d chars", len(got[0]), len(got[1]))
	}
	if !strings.Contains(got[1], "keep me.") || !strings.Contains(got[1], "[User]: go") {
		t.Error("the retried prompt must keep the instructions and the question")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "accumulated history") {
		t.Errorf("the log should name the session history as the cause, got:\n%s", strings.Join(logs, "\n"))
	}
	// The prompt was never the problem, so no ceiling may have been learned.
	if ceiling := c.promptCeiling(); ceiling != 0 {
		t.Errorf("a session-side refusal must not set a prompt ceiling, got %d", ceiling)
	}
}

// A refusal of a tiny prompt cannot be about prompt size: fail loudly instead of
// trimming the conversation to nothing and repeating it forever.
func TestTinyPromptRefusalIsReportedNotShrunk(t *testing.T) {
	srv, prompts := promptLimitServer(t, 10) // everything is "too large"

	c := NewClient(configForEndpoint(srv.URL))
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	err := c.generateViaDiscoveryDirectStream("what is next?", nil, "", ConversationInfo{}, "gemini-4-argon", nil, func(string) error { return nil })
	if err == nil {
		t.Fatal("a refusal that cannot be about size must surface as an error")
	}
	if len(*prompts) > 3 {
		t.Errorf("a tiny prompt must not be retried in a loop, got %d attempts", len(*prompts))
	}
	if c.promptCeiling() != 0 {
		t.Errorf("the ceiling must not be poisoned by a nonsensical refusal, got %d", c.promptCeiling())
	}
	if !strings.Contains(strings.Join(logs, "\n"), "cannot be the prompt size") {
		t.Errorf("the log should say the size cannot be the cause, got:\n%s", strings.Join(logs, "\n"))
	}
}

// The cap that produced the 247-char prompts in the wild.
func TestCeilingIsNeverLearnedBelowTheFloor(t *testing.T) {
	c := &Client{}
	c.rememberPromptCeiling(185)
	if got := c.promptCeiling(); got != 0 {
		t.Fatalf("a 185-char ceiling must be rejected, got %d", got)
	}
	c.rememberPromptCeiling(120000)
	c.rememberPromptCeiling(500)
	if got := c.promptCeiling(); got != 120000 {
		t.Fatalf("an implausible smaller value must not lower the ceiling, got %d", got)
	}
}

func TestFitPromptNeverExceedsTheLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("a line of conversation content\n")
	}
	prompt := b.String()
	for _, limit := range []int{1, 50, 200, 241, 1000, 5000, 40000} {
		fitted, _ := fitPromptForUpstream(prompt, limit)
		if len(fitted) > limit {
			t.Errorf("limit %d: fitted prompt is %d chars, over the limit", limit, len(fitted))
		}
	}
	// A limit with no room for a notice keeps the end of the prompt: the question.
	fitted, _ := fitPromptForUpstream("INSTRUCTIONS\n...\n[User]: the actual question\n", 40)
	if !strings.HasSuffix(fitted, "the actual question\n") {
		t.Errorf("a tiny limit should keep the latest text, got %q", fitted)
	}
}
