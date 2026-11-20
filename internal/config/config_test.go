package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadToolModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"tool_model":"gemini-4-hydrogen"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ToolModel != "gemini-4-hydrogen" {
		t.Errorf("ToolModel = %q, want gemini-4-hydrogen", cfg.ToolModel)
	}
}
