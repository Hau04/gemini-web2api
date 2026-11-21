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
