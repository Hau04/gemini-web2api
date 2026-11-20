package gemini

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The tenant dropped a session it handed out earlier:
//
//	HTTP 404 Session with name "projects/…/sessions/10740851950609687161"
//	does not exist.
//
// That must not fail the turn, and the dead id must not be reused.
func TestStaleSessionIsDroppedAndRequestRetried(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	creates := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			mu.Lock()
			creates++
			n := creates
			mu.Unlock()
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/` + itoa(n) + `"}}}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		// The first session is the one the tenant has forgotten.
		if strings.Contains(body, "sessions/1") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Session with name \"projects/282342753066/locations/global/collections/default_collection/engines/e/sessions/1\" does not exist.","status":"NOT_FOUND"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
	}))
	defer srv.Close()

	rec := &sessionRecorder{}
	c := sessionTestClient(t, srv, rec, nil)
	// Prime the cache with the session the tenant will reject.
	c.enterpriseSession = "collections/c/engines/e/sessions/1"
	creates = 1

	if err := c.generateViaDiscoveryDirectStream("hello", nil, "", "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("a stale session must not fail the request: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("expected the rejected request plus one retry, got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], "sessions/1") {
		t.Errorf("first attempt should carry the cached session: %s", truncateBody(bodies[0]))
	}
	if strings.Contains(bodies[1], `"session"`) {
		t.Errorf("the retry must go out without a session: %s", truncateBody(bodies[1]))
	}
	if !strings.Contains(strings.Join(rec.logs, "\n"), "no longer knows session") {
		t.Errorf("the log must say the session was dropped: %v", rec.logs)
	}

	// The dead id must be forgotten, so the next turn starts from a fresh session.
	if got := c.enterpriseSessionName(); strings.Contains(got, "sessions/1") {
		t.Errorf("stale session is still cached: %q", got)
	}
}

// A 404 that is not about the session (wrong endpoint, bad configId) still fails.
func TestNonSession404StillFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/9"}}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Requested entity was not found."}}`))
	}))
	defer srv.Close()

	rec := &sessionRecorder{}
	c := sessionTestClient(t, srv, rec, nil)

	err := c.generateViaDiscoveryDirectStream("hello", nil, "", "gemini-4-hydrogen", nil, func(string) error { return nil })
	if err == nil {
		t.Fatal("a 404 unrelated to the session must surface as an error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSessionNotFound(t *testing.T) {
	cases := map[string]bool{
		`{"error":{"code":404,"message":"Session with name \"projects/1/locations/global/collections/c/engines/e/sessions/5\" does not exist.","status":"NOT_FOUND"}}`: true,
		`{"error":{"code":404,"message":"Session not found","status":"NOT_FOUND"}}`:                                                                                    true,
		`{"error":{"code":404,"message":"Requested entity was not found."}}`:                                                                                           false,
		`{"error":{"code":400,"message":"Invalid JSON payload received. Unknown name \"session\""}}`:                                                                   false,
	}
	for body, want := range cases {
		if got := sessionNotFound([]byte(body)); got != want {
			t.Errorf("sessionNotFound(%.60s…) = %v, want %v", body, got, want)
		}
	}
}

func truncateBody(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
