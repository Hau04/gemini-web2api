package gemini

import (
	"strings"
	"testing"
)

func TestPromptTooLarge(t *testing.T) {
	// The real body from a live tenant.
	live := []byte(`[{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT",
"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"PROMPT_TOO_LARGE",
"domain":"discoveryengine.googleapis.com"}]}}]`)
	if !promptTooLarge(live) {
		t.Error("the live PROMPT_TOO_LARGE body was not recognised")
	}
	if promptTooLarge([]byte(`{"error":{"code":400,"message":"Unknown name \"session\" at 'add_context_file_request'"}}`)) {
		t.Error("an unrelated 400 must not be read as a size refusal")
	}
	if promptTooLarge([]byte(`{"error":{"code":500,"message":"internal"}}`)) {
		t.Error("a 500 must not be read as a size refusal")
	}
}

func TestFitPromptKeepsHeadAndTail(t *testing.T) {
	head := "# Tool Use\nINSTRUCTIONS: always answer in English.\n"
	middle := strings.Repeat("old history line\n", 5000)
	tail := "[User]: do the next step please\n"
	prompt := head + middle + tail

	fitted, trimmed := fitPromptForUpstream(prompt, 20000)
	if !trimmed {
		t.Fatal("an oversized prompt must be trimmed")
	}
	if len(fitted) > 20000 {
		t.Fatalf("fitted prompt is %d bytes, over the 20000 limit", len(fitted))
	}
	if !strings.HasPrefix(fitted, "# Tool Use\n") || !strings.Contains(fitted, "always answer in English") {
		t.Error("the instructions at the head must survive")
	}
	if !strings.HasSuffix(fitted, "[User]: do the next step please\n") {
		t.Error("the latest user message must survive")
	}
	if !strings.Contains(fitted, "characters of earlier conversation omitted") {
		t.Error("the omission must be stated in the prompt, not silent")
	}
	// Only the tail's worth of middle lines may remain (the head has none).
	if kept := strings.Count(fitted, "old history line"); kept > 1200 {
		t.Errorf("%d history lines survived a 20000-byte limit — the middle was not dropped", kept)
	}
}

func TestFitPromptLeavesSmallPromptsAlone(t *testing.T) {
	prompt := "short prompt"
	if got, trimmed := fitPromptForUpstream(prompt, 20000); trimmed || got != prompt {
		t.Errorf("a prompt under the limit must pass through unchanged, got %q trimmed=%v", got, trimmed)
	}
	if got, trimmed := fitPromptForUpstream(prompt, 0); trimmed || got != prompt {
		t.Error("an unknown limit must not trim anything")
	}
}

func TestFitPromptCutsOnLineBoundaries(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString("line of context content\n")
	}
	prompt := b.String()
	fitted, trimmed := fitPromptForUpstream(prompt, 10000)
	if !trimmed {
		t.Fatal("expected a trim")
	}
	// Everything before the marker must end on a newline.
	idx := strings.Index(fitted, "[... ")
	if idx < 0 {
		t.Fatal("marker missing")
	}
	if fitted[idx-1] != '\n' {
		t.Errorf("the kept head should end at a line boundary, found %q", fitted[idx-3:idx])
	}
}

func TestPromptCeilingUsesConfigAndLearned(t *testing.T) {
	c := &Client{}
	if got := c.promptCeiling(); got != 0 {
		t.Errorf("a fresh client knows no ceiling, got %d", got)
	}
	c.rememberPromptCeiling(90000)
	if got := c.promptCeiling(); got != 90000 {
		t.Errorf("learned ceiling = %d, want 90000", got)
	}
	// A smaller learned value must win over a larger configured one.
	c.Cfg.EnterpriseMaxPromptChars = 120000
	if got := c.promptCeiling(); got != 90000 {
		t.Errorf("the learned, smaller ceiling must win, got %d", got)
	}
	// A larger configured value must not lift a learned refusal.
	c.Cfg.EnterpriseMaxPromptChars = 50000
	if got := c.promptCeiling(); got != 50000 {
		t.Errorf("the configured, smaller ceiling must win, got %d", got)
	}
	c.rememberPromptCeiling(200000)
	if got := c.promptCeiling(); got != 50000 {
		t.Errorf("a larger learned value must not raise the ceiling, got %d", got)
	}
}
