package gemini

import (
	"fmt"
	"strings"
	"testing"
)

// The observed loop: a 600 KB prompt plus a session is refused, the retry without
// the session succeeds, and the next turn attaches the session again and pays for
// the same refusal. A big prompt must not attach a session at all.
func TestBigPromptDoesNotAttachASession(t *testing.T) {
	srv, prompts := sessionTooLargeServer(t)

	c := NewClient(configForEndpoint(srv.URL))
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	c.markSessionRefused("") // no-op, keeps the nil map path honest

	prompt := "INSTRUCTIONS: keep me.\n" + strings.Repeat("context line\n", 20000) + "[User]: go\n"
	if err := c.generateViaDiscoveryDirectStream(prompt, nil, "", ConversationInfo{Key: "conv0001", HasHistory: true}, "gemini-4-argon", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("a large prompt must be sent sessionless, got %v", err)
	}

	if got := *prompts; len(got) != 1 {
		t.Fatalf("expected a single attempt, got %d (the session should never have been attached)", len(got))
	}
	if !strings.Contains(strings.Join(logs, "\n"), "the prompt already carries the conversation") {
		t.Errorf("the skip should be explained, got:\n%s", strings.Join(logs, "\n"))
	}
}

// A conversation that was refused once must not be refused again every turn.
func TestRefusedConversationNeverAttachesAgain(t *testing.T) {
	srv, prompts := sessionTooLargeServer(t)

	c := NewClient(configForEndpoint(srv.URL))
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	// Small prompts under the threshold: the first turn is the one that discovers
	// the tenant will not take a session.
	if err := c.generateViaDiscoveryDirectStream("first question", nil, "", ConversationInfo{Key: "conv0002"}, "gemini-4-argon", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	before := len(*prompts)
	if before != 2 {
		t.Fatalf("first turn should be refused once and retried once, got %d attempts", before)
	}
	if err := c.generateViaDiscoveryDirectStream("second question", nil, "", ConversationInfo{Key: "conv0002"}, "gemini-4-argon", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	after := len(*prompts) - before
	if after != 1 {
		t.Errorf("the refusal should be remembered, got %d attempts on the next turn", after)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "already refused a session for this conversation") {
		t.Errorf("the remembered refusal should be logged, got:\n%s", strings.Join(logs, "\n"))
	}
}

func TestSkipSessionForPromptThreshold(t *testing.T) {
	c := &Client{}
	if skip, _ := c.skipSessionForPrompt(1000, "abc"); skip {
		t.Error("a small prompt should keep its session")
	}
	if skip, why := c.skipSessionForPrompt(DefaultSessionMaxPromptChars+1, "abc"); !skip || why == "" {
		t.Errorf("a prompt over the threshold should skip the session (skip=%v why=%q)", skip, why)
	}
	// A configured threshold wins, in both directions.
	small := 100
	c.Cfg.EnterpriseSessionMaxPromptChars = small
	if skip, _ := c.skipSessionForPrompt(200, "abc"); !skip {
		t.Error("the configured threshold should apply")
	}
	c.Cfg.EnterpriseSessionMaxPromptChars = 4 << 20
	if skip, _ := c.skipSessionForPrompt(200000, "abc"); skip {
		t.Error("a raised threshold should keep the session")
	}
	// A conversation with no key cannot be remembered, and must not crash.
	c.markSessionRefused("")
	if skip, _ := c.skipSessionForPrompt(10, ""); skip {
		t.Error("an unknown conversation should not be treated as refused")
	}
}

func TestMarkSessionRefused(t *testing.T) {
	c := &Client{}
	c.markSessionRefused("conv9")
	if skip, why := c.skipSessionForPrompt(10, "conv9"); !skip || !strings.Contains(why, "already refused") {
		t.Errorf("skip=%v why=%q", skip, why)
	}
	if skip, _ := c.skipSessionForPrompt(10, "other"); skip {
		t.Error("another conversation must not inherit the refusal")
	}
}
