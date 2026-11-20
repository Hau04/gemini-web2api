package server

import (
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestResolveRequestModelToolOverride(t *testing.T) {
	cfg := config.Default()
	cfg.ToolModel = "gemini-4-hydrogen"
	app := &App{Cfg: cfg}

	tests := []struct {
		name      string
		requested string
		hasTools  bool
		wantModel string
		wantRedir bool
		wantThink int
		wantChat  string
	}{
		{name: "plain chat keeps argon", requested: "gemini-4-argon", wantModel: "gemini-4-argon", wantThink: 4},
		{name: "tool call routes to hydrogen", requested: "gemini-4-argon", hasTools: true, wantModel: "gemini-4-hydrogen", wantRedir: true, wantThink: 4},
		{name: "alias routes too", requested: "argon", hasTools: true, wantModel: "gemini-4-hydrogen", wantRedir: true, wantThink: 4},
		{name: "directives preserved", requested: "gemini-4-argon@think=0@new", hasTools: true, wantModel: "gemini-4-hydrogen", wantRedir: true, wantThink: 0, wantChat: "new"},
		{name: "already hydrogen needs no redirect", requested: "gemini-4-hydrogen", hasTools: true, wantModel: "gemini-4-hydrogen", wantThink: 4},
	}

	// An explicit directive in tool_model wins, but the request's session mode
	// is still carried over.
	app.Cfg.ToolModel = "gemini-4-hydrogen@think=2"
	got, redirected, err := app.resolveRequestModel("gemini-4-argon@think=0@new", true)
	if err != nil {
		t.Fatalf("resolveRequestModel() error = %v", err)
	}
	if got.Name != "gemini-4-hydrogen" || !redirected || got.Think != 2 || got.ChatMode != "new" {
		t.Errorf("directive merge = (%s, %t, think=%d, chat=%q), want (gemini-4-hydrogen, true, think=2, chat=%q)",
			got.Name, redirected, got.Think, got.ChatMode, "new")
	}
	app.Cfg.ToolModel = "gemini-4-hydrogen"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, redirected, err := app.resolveRequestModel(tt.requested, tt.hasTools)
			if err != nil {
				t.Fatalf("resolveRequestModel() error = %v", err)
			}
			if got.Name != tt.wantModel || redirected != tt.wantRedir || got.Think != tt.wantThink || got.ChatMode != tt.wantChat {
				t.Errorf("resolveRequestModel(%q, %t) = (%s, %t, think=%d, chat=%q), want (%s, %t, think=%d, chat=%q)",
					tt.requested, tt.hasTools, got.Name, redirected, got.Think, got.ChatMode,
					tt.wantModel, tt.wantRedir, tt.wantThink, tt.wantChat)
			}
		})
	}
}

func TestResolveRequestModelWithoutToolModelConfigured(t *testing.T) {
	app := &App{Cfg: config.Default()}
	got, redirected, err := app.resolveRequestModel("gemini-4-argon", true)
	if err != nil {
		t.Fatalf("resolveRequestModel() error = %v", err)
	}
	if got.Name != "gemini-4-argon" || redirected {
		t.Errorf("unexpected override with empty tool_model: %s (redirected=%t)", got.Name, redirected)
	}
}

func TestResolveRequestModelInvalidToolModel(t *testing.T) {
	cfg := config.Default()
	cfg.ToolModel = "not-a-real-model"
	app := &App{Cfg: cfg}
	if _, _, err := app.resolveRequestModel("gemini-4-argon", true); err == nil {
		t.Error("expected an error for an unknown tool_model")
	}
}
