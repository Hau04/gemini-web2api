package gemini

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// conversationsApp counts sessions and records which session each streamAssist
// carried, so tests can assert thread identity per client chat.
func conversationsApp(t *testing.T) (*Client, func() []string, func() int) {
	t.Helper()
	var mu sync.Mutex
	var sessions []string
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
		session := ""
		if m := reSessionField.FindStringSubmatch(body); len(m) == 2 {
			session = m[1]
		}
		mu.Lock()
		sessions = append(sessions, session)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
	}))
	t.Cleanup(srv.Close)

	rec := &sessionRecorder{}
	c := sessionTestClient(t, srv, rec, nil)
	return c,
		func() []string { mu.Lock(); defer mu.Unlock(); return append([]string{}, sessions...) },
		func() int { mu.Lock(); defer mu.Unlock(); return creates }
}

func runTurn(t *testing.T, c *Client, key string, hasHistory bool) {
	t.Helper()
	if err := c.generateViaDiscoveryDirectStream("hello", nil, "", ConversationInfo{Key: key, HasHistory: hasHistory}, "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
}

// Continuing one chat keeps one thread.
func TestSameConversationKeepsOneSession(t *testing.T) {
	c, sessions, creates := conversationsApp(t)

	runTurn(t, c, "aaaa1111", false) // first message of the chat
	runTurn(t, c, "aaaa1111", true)  // follow-up, history resends the first message
	runTurn(t, c, "aaaa1111", true)  // and again

	got := sessions()
	if len(got) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(got))
	}
	for i, session := range got {
		if session == "" {
			t.Fatalf("call %d had no session", i)
		}
		if session != got[0] {
			t.Errorf("call %d used %q, want the chat's own %q", i, session, got[0])
		}
	}
	if creates() != 1 {
		t.Errorf("one conversation should open one session, got %d", creates())
	}
}

// The reported bug: opening a new chat reused the previous chat's thread.
func TestNewChatOpensNewSession(t *testing.T) {
	c, sessions, creates := conversationsApp(t)

	runTurn(t, c, "old00001", false) // chat A, first message
	runTurn(t, c, "old00001", true)  // chat A continues
	runTurn(t, c, "new00002", false) // chat B: new chat in the client, one message, no history

	got := sessions()
	if len(got) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(got))
	}
	if got[1] != got[0] {
		t.Errorf("chat A's second turn should reuse its session: %v", got[:2])
	}
	if got[2] == got[0] {
		t.Errorf("a new chat must not continue the old thread: %v", got)
	}
	if creates() != 2 {
		t.Errorf("expected one session per chat, got %d creations", creates())
	}

	// Chat B continues with history and keeps its own thread.
	runTurn(t, c, "new00002", true)
	got = sessions()
	if got[3] != got[2] {
		t.Errorf("chat B's follow-up should reuse its session: %v", got[2:])
	}
}

// Resuming an earlier chat must find its own thread again, not the latest one.
func TestResumingOldChatSwitchesBackToItsSession(t *testing.T) {
	c, sessions, _ := conversationsApp(t)

	runTurn(t, c, "aaa00001", false)
	runTurn(t, c, "aaa00001", true)
	runTurn(t, c, "bbb00002", false)
	runTurn(t, c, "bbb00002", true)
	runTurn(t, c, "aaa00001", true) // back to the first chat

	got := sessions()
	if len(got) != 5 {
		t.Fatalf("expected 5 calls, got %d: %v", len(got), got)
	}
	if got[4] != got[0] {
		t.Errorf("resuming chat A should return to %q, got %q", got[0], got[4])
	}
}

// A stateless client (always one message, no history) must keep its continuity:
// the messages differ, and treating each as a new chat is the bug #65 fixed.
func TestStatelessClientKeepsOneSession(t *testing.T) {
	c, sessions, creates := conversationsApp(t)

	if err := c.generateViaDiscoveryDirectStream("hi", nil, "", ConversationInfo{Key: "hi000001", HasHistory: false}, "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.generateViaDiscoveryDirectStream("whats ur model", nil, "", ConversationInfo{Key: "model002", HasHistory: false}, "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}

	got := sessions()
	if len(got) != 2 || got[0] != got[1] {
		t.Fatalf("a stateless client must keep one thread, got %v", got)
	}
	if creates() != 1 {
		t.Errorf("creates = %d, want 1", creates())
	}
}
