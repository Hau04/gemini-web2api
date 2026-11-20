package gemini

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

var reSessionField = regexp.MustCompile(`"session":"([^"]*)"`)

type sessionRecorder struct {
	mu        sync.Mutex
	creates   int
	sessions  []string
	createErr int
	logs      []string
}

func (r *sessionRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, format)
}

func (r *sessionRecorder) logText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}

// newSessionServer fakes the widget API: widgetCreateSession hands out a session,
// widgetStreamAssist records the session each call carried.
func newSessionServer(t *testing.T) (*httptest.Server, *sessionRecorder) {
	t.Helper()
	rec := &sessionRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "widgetCreateSession"):
			rec.mu.Lock()
			rec.creates++
			create := rec.creates
			rec.mu.Unlock()
			if rec.createErr != 0 {
				w.WriteHeader(rec.createErr)
				return
			}
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/` + itoa(create) + `"}}}`))
		case strings.Contains(r.URL.Path, "widgetStreamAssist"):
			raw, _ := io.ReadAll(r.Body)
			body := string(raw)
			session := ""
			if m := reSessionField.FindStringSubmatch(body); len(m) == 2 {
				session = m[1]
			}
			rec.mu.Lock()
			rec.sessions = append(rec.sessions, session)
			rec.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, rec
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func sessionTestClient(t *testing.T, srv *httptest.Server, rec *sessionRecorder, mut func(*config.Config)) *Client {
	t.Helper()
	cfg := config.Config{
		CookieFile:                 filepath.Join(t.TempDir(), "cookie.json"),
		BaseURL:                    srv.URL,
		RetryAttempts:              1,
		DiscoveryEngineConfigID:    "cfg-1",
		DiscoveryEngineEndpoint:    srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
		DiscoveryEngineProbeModels: false,
	}
	if mut != nil {
		mut(&cfg)
	}
	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = rec.logf
	return c
}

// Regression for "every message opens a new chat": cookie-authenticated requests
// (bearer=false) never created a session, so each streamAssist was a fresh thread.
func TestCookieOnlyStreamAssistReusesOneSession(t *testing.T) {
	srv, rec := newSessionServer(t)
	defer srv.Close()
	c := sessionTestClient(t, srv, rec, nil)

	for i := 0; i < 3; i++ {
		if err := c.generateViaDiscoveryDirectStream("hello", nil, "", "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
			t.Fatalf("direct stream %d failed: %v", i, err)
		}
	}

	if rec.creates != 1 {
		t.Errorf("widgetCreateSession called %d times, want 1 (one conversation per process)", rec.creates)
	}
	if len(rec.sessions) != 3 {
		t.Fatalf("expected 3 streamAssist calls, got %d", len(rec.sessions))
	}
	for i, session := range rec.sessions {
		if session == "" {
			t.Fatalf("call %d carried no session — the tenant would open a new conversation: %v", i, rec.sessions)
		}
		if session != rec.sessions[0] {
			t.Errorf("call %d used session %q, want the same %q as the first call", i, session, rec.sessions[0])
		}
	}
	if !strings.Contains(rec.logText(), "continuing in assistant session") {
		t.Errorf("log should state the session in use so continuity is verifiable: %s", rec.logText())
	}
}

// A captured template leaves the app's session id replaced by "-": that must be
// treated as "no session", not sent verbatim.
func TestPlaceholderSessionIsReplaced(t *testing.T) {
	srv, rec := newSessionServer(t)
	defer srv.Close()
	c := sessionTestClient(t, srv, rec, nil)

	placeholder := "collections/c/engines/e/sessions/-"
	if err := c.generateViaDiscoveryDirectStream("hi", nil, placeholder, "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("direct stream failed: %v", err)
	}
	if rec.creates != 1 {
		t.Errorf("placeholder session must trigger a real session, creates=%d", rec.creates)
	}
	if len(rec.sessions) != 1 || rec.sessions[0] == placeholder || rec.sessions[0] == "" {
		t.Fatalf("streamAssist carried %v, want a real created session", rec.sessions)
	}
}

// A session the caller pinned (config/template) is used verbatim — no new one.
func TestPinnedSessionIsUsedVerbatim(t *testing.T) {
	srv, rec := newSessionServer(t)
	defer srv.Close()
	c := sessionTestClient(t, srv, rec, nil)

	pinned := "collections/c/engines/e/sessions/pinned"
	if err := c.generateViaDiscoveryDirectStream("hi", nil, pinned, "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("direct stream failed: %v", err)
	}
	if rec.creates != 0 {
		t.Errorf("a pinned session must not create another, creates=%d", rec.creates)
	}
	if len(rec.sessions) != 1 || rec.sessions[0] != pinned {
		t.Fatalf("streamAssist carried %v, want the pinned session", rec.sessions)
	}
}

// When the tenant refuses to create a session, say so instead of silently
// producing a new conversation per message.
func TestSessionCreationFailureIsReported(t *testing.T) {
	srv, rec := newSessionServer(t)
	defer srv.Close()
	rec.createErr = http.StatusForbidden
	c := sessionTestClient(t, srv, rec, nil)

	if err := c.generateViaDiscoveryDirectStream("hi", nil, "", "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("the request should still run without a session: %v", err)
	}
	if !strings.Contains(rec.logText(), "new conversation") {
		t.Errorf("log must warn that requests will look like new conversations: %s", rec.logText())
	}
	if len(rec.sessions) != 1 || rec.sessions[0] != "" {
		t.Fatalf("expected the call to proceed without a session, got %v", rec.sessions)
	}
}

func TestShortSessionName(t *testing.T) {
	if got := shortSessionName("collections/c/engines/e/sessions/abc123"); got != "abc123" {
		t.Errorf("shortSessionName = %q", got)
	}
	if got := shortSessionName("  "); got != "<none>" {
		t.Errorf("empty session should render as <none>, got %q", got)
	}
	if got := shortSessionName("bare"); got != "bare" {
		t.Errorf("shortSessionName = %q", got)
	}
}
