package server

import (
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

// The prompt must never be replaced by a pointer to a context file on a tenant
// that does not hand the body back: that is what made the model answer "the
// context file can no longer be read" and ask for a new chat.
func TestLargePromptIsNotSpilledWhileMirroringIsOn(t *testing.T) {
	app := &App{Cfg: config.Default(), Logf: func(string, ...any) {}}
	body := strings.Repeat("# Instructions\nDo the thing carefully.\n", 4000)

	prompt, images := app.spillLargePromptIfEnabled(body, nil)

	if len(images) != 0 {
		t.Fatalf("mirroring is on: nothing should be spilled into an attachment, got %d", len(images))
	}
	if prompt != body {
		t.Errorf("the prompt must keep its instructions inline (got %d chars, want %d)", len(prompt), len(body))
	}
}

// With mirroring off the old spill behaviour must still work.
func TestLargePromptSpillsWhenMirroringIsOff(t *testing.T) {
	cfg := config.Default()
	cfg.EnterpriseMirrorTextInline = false
	app := &App{Cfg: cfg, Logf: func(string, ...any) {}}
	body := strings.Repeat("# Instructions\nDo the thing carefully.\n", 4000)

	prompt, images := app.spillLargePromptIfEnabled(body, nil)

	if len(images) == 0 {
		t.Fatal("mirroring is off: the oversized prompt should be spilled into one attachment")
	}
	if len(prompt) >= len(body) {
		t.Errorf("the compact prompt should be shorter than the original: %d vs %d", len(prompt), len(body))
	}
}
