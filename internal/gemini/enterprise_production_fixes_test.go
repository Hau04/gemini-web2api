package gemini

import (
	"context"
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
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func extractStreamAssistInfo(raw []byte) (session, query string, fileIDs []string, isStreamAssist bool) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return "", "", nil, false
	}
	req, ok := payload["streamAssistRequest"].(map[string]any)
	if !ok {
		return "", "", nil, false
	}
	session, _ = req["session"].(string)
	if q, ok := req["query"].(map[string]any); ok {
		if parts, ok := q["parts"].([]any); ok && len(parts) > 0 {
			if part, ok := parts[0].(map[string]any); ok {
				query, _ = part["text"].(string)
			}
		}
	}
	if ids, ok := req["fileIds"].([]any); ok {
		for _, id := range ids {
			if s, ok := id.(string); ok {
				fileIDs = append(fileIDs, s)
			}
		}
	}
	return session, query, fileIDs, true
}

// Reproduces the session-poisoning bug: once a keyed conversation's session hits
// HTTP 400 PROMPT_TOO_LARGE, evicting only c.enterpriseSession while leaving
// c.enterpriseSessions[convKey] and uploadCache intact caused every subsequent
// turn to switch back to the full session forever.
func TestPromptTooLargeEvictsConversationSessionAndRotatesCleanlyOnNextTurn(t *testing.T) {
	var (
		mu             sync.Mutex
		creates        int
		uploads        int
		uploadSessions []string
		streamSessions []string
		streamQueries  []string
		streamFiles    [][]string
		session1Full   bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "widgetCreateSession"):
			mu.Lock()
			creates++
			n := creates
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/%d"}}}`, n)))
			return

		case strings.Contains(r.URL.Path, "widgetAddContextFile"):
			raw, _ := io.ReadAll(r.Body)
			var payload map[string]any
			_ = json.Unmarshal(raw, &payload)
			upSess := ""
			if addReq, ok := payload["addContextFileRequest"].(map[string]any); ok {
				upSess, _ = addReq["session"].(string)
			}
			mu.Lock()
			uploads++
			u := uploads
			uploadSessions = append(uploadSessions, upSess)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fmt.Sprintf(`{"addContextFileResponse":{"session":"%s","fileId":"file-%d"}}`, upSess, u)))
			return

		default:
			raw, _ := io.ReadAll(r.Body)
			sess, query, files, ok := extractStreamAssistInfo(raw)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			streamSessions = append(streamSessions, sess)
			streamQueries = append(streamQueries, query)
			streamFiles = append(streamFiles, files)
			full := session1Full
			mu.Unlock()

			if full && strings.HasSuffix(sess, "sessions/1") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`[{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"PROMPT_TOO_LARGE"}]}}]`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
		}
	}))
	defer srv.Close()

	cookieFile := filepath.Join(t.TempDir(), "cookie.json")
	_ = os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc; __Secure-1PSID=xyz"}`), 0o600)
	cfg := config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		RetryAttempts:                 3,
		RetryDelaySec:                 0,
		DiscoveryEngineConfigID:       "cfg-1",
		DiscoveryEngineEndpoint:       srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile?key=k",
		DiscoveryEngineUploadCacheSec: 1800,
	}
	c := NewClient(cfg)
	c.bardUnsupported.Store(true)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	file := RawFile{Name: "notes.txt", MIME: "text/plain", Data: []byte("project notes")}

	// Turn 1 + normal follow-up of chat A establish sessions/1 as a multi-turn chat.
	if _, err := c.Generate("turn 1 full", ModelOptions{
		ModelName:              "gemini-4-neon",
		ConversationKey:        "chatA001",
		ConversationHasHistory: false,
		Files:                  []RawFile{file},
	}, nil); err != nil {
		t.Fatalf("turn 1 failed: %v", err)
	}
	if _, err := c.Generate("turn 1b followup", ModelOptions{
		ModelName:              "gemini-4-neon",
		ConversationKey:        "chatA001",
		ConversationHasHistory: true,
		Files:                  []RawFile{file},
	}, nil); err != nil {
		t.Fatalf("turn 1b failed: %v", err)
	}
	c.RecordEnterpriseSessionDelta("collections/c/engines/e/sessions/1", []string{"sys", "u1", "a1"}, nil)

	// Unrelated chat B opens sessions/2 so c.enterpriseSession now points at B.
	if _, err := c.Generate("hello from B", ModelOptions{
		ModelName:              "gemini-4-neon",
		ConversationKey:        "chatB002",
		ConversationHasHistory: false,
	}, nil); err != nil {
		t.Fatalf("chat B failed: %v", err)
	}

	// Mark sessions/1 full on Google's side.
	mu.Lock()
	session1Full = true
	mu.Unlock()

	// Turn 2 of chat A carries a delta prompt + FullPrompt fallback and hits
	// HTTP 400 PROMPT_TOO_LARGE on sessions/1.
	if _, err := c.Generate("delta turn 2", ModelOptions{
		ModelName:              "gemini-4-neon",
		ConversationKey:        "chatA001",
		ConversationHasHistory: true,
		FullPrompt:             "FULL TRANSCRIPT THROUGH TURN 2",
		DeltaBaseSession:       "collections/c/engines/e/sessions/1",
		Files:                  []RawFile{file},
	}, nil); err != nil {
		t.Fatalf("turn 2 should recover after dropping full sessions/1: %v", err)
	}

	if owned := c.OwnedEnterpriseSession("chatA001"); owned != "" {
		t.Fatalf("evicted session must be removed from chatA001 ownership, still got %q", owned)
	}
	if prefix, _ := c.MatchEnterpriseSessionDelta("collections/c/engines/e/sessions/1", []string{"sys", "u1", "a1"}, nil); prefix != 0 {
		t.Fatalf("evicted session's delta state must be cleared, got matchedPrefix=%d", prefix)
	}

	// Turn 3 of chat A must NOT reuse sessions/1 and must NOT hijack chat B's
	// sessions/2 — it must create a fresh sessions/3 and re-upload notes.txt into it.
	if _, err := c.Generate("turn 3 full", ModelOptions{
		ModelName:              "gemini-4-neon",
		ConversationKey:        "chatA001",
		ConversationHasHistory: true,
		Files:                  []RawFile{file},
	}, nil); err != nil {
		t.Fatalf("turn 3 failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Expected streamAssist calls:
	// 0: Turn 1 chat A -> sessions/1
	// 1: Turn 1b chat A -> sessions/1 (reuses cached upload)
	// 2: Turn 1 chat B -> sessions/2
	// 3: Turn 2 chat A attempt 1 -> sessions/1 (400 PROMPT_TOO_LARGE, delta prompt)
	// 4: Turn 2 chat A retry -> session="" (promoted FULL TRANSCRIPT, dangling fileIDs stripped)
	// 5: Turn 3 chat A -> sessions/3 (fresh session, NOT sessions/1 and NOT sessions/2!)
	if len(streamSessions) != 6 {
		t.Fatalf("expected 6 streamAssist requests, got %d: %v", len(streamSessions), streamSessions)
	}
	if !strings.Contains(streamQueries[4], "FULL TRANSCRIPT THROUGH TURN 2") {
		t.Errorf("retry after session drop must promote FullPrompt, got %q", streamQueries[4])
	}
	if len(streamFiles[4]) != 0 {
		t.Errorf("retry without session must strip dead session's fileIds, got %v", streamFiles[4])
	}
	if !strings.HasSuffix(streamSessions[5], "sessions/3") {
		t.Errorf("turn 3 after eviction used session %q, want fresh sessions/3", streamSessions[5])
	}
	if len(uploadSessions) != 2 || !strings.HasSuffix(uploadSessions[1], "sessions/3") {
		t.Errorf("turn 3 should re-upload context file into sessions/3 after cache eviction, got uploads=%v", uploadSessions)
	}
}

func TestActivityTimeoutKeepsStreamingThoughtAliveAndAbortsStalledStream(t *testing.T) {
	t.Run("active_thinking_exceeding_idle_timeout_succeeds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "widgetCreateSession") {
				_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("["))
			flusher.Flush()
			// RequestTimeoutSec is 1s, so 5 chunks * 300ms = 1.5s total duration
			// would fail under a fixed 1s wall-clock timeout, but succeeds under an
			// activity-resetting 1s idle timeout.
			for i := 0; i < 4; i++ {
				time.Sleep(300 * time.Millisecond)
				_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"IN_PROGRESS","replies":[{"groundedContent":{"content":{"thought":true,"text":"thinking..."}}}]}}},`))
				flusher.Flush()
			}
			time.Sleep(250 * time.Millisecond)
			_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"final answer"}}}]}}}]`))
			flusher.Flush()
		}))
		defer srv.Close()

		cfg := configForEndpoint(srv.URL)
		cfg.RequestTimeoutSec = 1
		cfg.RetryAttempts = 1
		c := NewClient(cfg)
		c.Logf = func(string, ...any) {}

		var got strings.Builder
		err := c.generateViaDiscoveryDirectStream("solve hard problem", nil, "", ConversationInfo{}, "gemini-4-neon", nil, func(delta string) error {
			got.WriteString(delta)
			return nil
		})
		if err != nil {
			t.Fatalf("active thinking stream should not time out when chunks arrive every 300ms: %v", err)
		}
		if got.String() != "final answer" {
			t.Errorf("got %q, want %q", got.String(), "final answer")
		}
	})

	t.Run("stalled_stream_aborts_after_idle_timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "widgetCreateSession") {
				_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write([]byte("["))
			flusher.Flush()
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()

		cfg := configForEndpoint(srv.URL)
		cfg.RequestTimeoutSec = 1
		cfg.RetryAttempts = 1
		c := NewClient(cfg)
		c.Logf = func(string, ...any) {}

		start := time.Now()
		err := c.generateViaDiscoveryDirectStream("hang", nil, "", ConversationInfo{}, "gemini-4-neon", nil, func(string) error { return nil })
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("stalled stream must fail after idle timeout")
		}
		if elapsed > 2500*time.Millisecond {
			t.Errorf("stalled stream took %v, expected abort near 1s idle timeout", elapsed)
		}
	})
}

func TestClientContextCancellationAbortsStreamAssistWithoutRetrying(t *testing.T) {
	var (
		mu       sync.Mutex
		calls    int
		stopWait = make(chan struct{})
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("["))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-stopWait:
		}
	}))
	defer func() {
		close(stopWait)
		srv.Close()
	}()

	cfg := configForEndpoint(srv.URL)
	cfg.RetryAttempts = 3
	cfg.RetryDelaySec = 2
	cfg.RequestTimeoutSec = 10
	c := NewClient(cfg)
	c.Logf = func(string, ...any) {}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := c.generateViaDiscoveryDirectStream("hello", nil, "", ConversationInfo{Context: ctx}, "gemini-4-neon", nil, func(string) error { return nil })
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("cancellation took %v, expected immediate abort without retry backoff", elapsed)
	}
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 1 {
		t.Errorf("disconnected client must not trigger upstream retries: got %d calls, want 1", gotCalls)
	}
}

func TestStreamAssist401ReMintsBearerLogsAndFiresOnAuthError(t *testing.T) {
	t.Run("stale_bearer_is_reminted_inside_retry_loop", func(t *testing.T) {
		var (
			mu          sync.Mutex
			xsrfCalls   int
			authHeaders []string
		)
		var srvURL string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/auth/getoxsrf":
				mu.Lock()
				xsrfCalls++
				n := xsrfCalls
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(fmt.Sprintf(")]}'\n{\"xsrfToken\":%q,\"keyId\":\"kid-fresh-%d\"}", hs256Key, n)))
			case strings.Contains(r.URL.Path, "widgetCreateSession"):
				_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			default:
				auth := r.Header.Get("Authorization")
				mu.Lock()
				authHeaders = append(authHeaders, auth)
				callNum := len(authHeaders)
				mu.Unlock()
				if callNum == 1 {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":{"code":401,"message":"UNAUTHENTICATED"}}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"recovered"}}}]}}}`))
			}
		}))
		defer srv.Close()
		srvURL = srv.URL

		cookieFile := filepath.Join(t.TempDir(), "cookie.json")
		_ = os.WriteFile(cookieFile, []byte(`{"cookie":"__Secure-1PSID=abc; SAPISID=xyz"}`), 0o600)
		cfg := config.Config{
			CookieFile:              cookieFile,
			BaseURL:                 srvURL,
			RetryAttempts:           3,
			RetryDelaySec:           0,
			DiscoveryEngineCsesidx:  "735410566",
			DiscoveryEngineConfigID: "cfg-1",
			DiscoveryEngineEndpoint: srvURL + "/v1alpha/locations/global/widgetStreamAssist",
		}
		c := NewClient(cfg)
		// Redirect content-discoveryengine host calls to our local test server.
		c.HTTP = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = "http"
			clone.URL.Host = strings.TrimPrefix(srvURL, "http://")
			return srv.Client().Do(clone)
		})
		var logs []string
		c.Logf = func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
		authErrFired := 0
		c.OnAuthError = func(string) { authErrFired++ }

		// Pre-populate a stale cached bearer token that will fail on attempt 1.
		c.enterpriseJWT = "stale-jwt-token"
		c.enterpriseJWTExp = time.Now().Add(4 * time.Minute)
		c.enterpriseSession = "collections/c/engines/e/sessions/1"

		var out strings.Builder
		err := c.generateViaDiscoveryDirectStream("ping", nil, "collections/c/engines/e/sessions/1", ConversationInfo{}, "gemini-4-neon", nil, func(s string) error {
			out.WriteString(s)
			return nil
		})
		if err != nil {
			t.Fatalf("expected 401 recovery after re-minting bearer, got %v", err)
		}
		if out.String() != "recovered" {
			t.Errorf("got %q, want %q", out.String(), "recovered")
		}
		mu.Lock()
		gotAuths := append([]string{}, authHeaders...)
		mu.Unlock()
		if len(gotAuths) != 2 {
			t.Fatalf("expected 2 streamAssist attempts, got %d", len(gotAuths))
		}
		if gotAuths[0] != "Bearer stale-jwt-token" {
			t.Errorf("attempt 1 auth = %q, want stale token", gotAuths[0])
		}
		if gotAuths[1] == gotAuths[0] || !strings.HasPrefix(gotAuths[1], "Bearer eyJ") {
			t.Errorf("attempt 2 must re-mint a fresh JWT instead of resending stale token, got %q", gotAuths[1])
		}
		if authErrFired != 0 {
			t.Errorf("OnAuthError should not fire when retry succeeds, got %d", authErrFired)
		}
		if !strings.Contains(strings.Join(logs, "\n"), "streamAssist rejected (HTTP 401") {
			t.Errorf("expected explicit 401 log entry, got:\n%s", strings.Join(logs, "\n"))
		}
	})

	t.Run("persistent_401_fires_on_auth_error_and_logs", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Credentials expired"}}`))
		}))
		defer srv.Close()

		cfg := configForEndpoint(srv.URL)
		cfg.RetryAttempts = 2
		cfg.RetryDelaySec = 0
		c := NewClient(cfg)
		c.HTTP = srv.Client()
		var logs []string
		c.Logf = func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
		var authMsg string
		c.OnAuthError = func(msg string) { authMsg = msg }
		c.enterpriseSession = "collections/c/engines/e/sessions/1"

		err := c.generateViaDiscoveryDirectStream("ping", nil, "collections/c/engines/e/sessions/1", ConversationInfo{}, "gemini-4-neon", nil, func(string) error { return nil })
		if err == nil {
			t.Fatal("expected 401 failure")
		}
		if authMsg == "" {
			t.Fatal("OnAuthError must be called when all auth retries fail")
		}
		if !strings.Contains(strings.Join(logs, "\n"), "streamAssist rejected (HTTP 401") {
			t.Errorf("expected 401 log line, got:\n%s", strings.Join(logs, "\n"))
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}
