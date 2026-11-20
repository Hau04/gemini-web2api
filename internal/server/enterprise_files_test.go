package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func newEnterpriseApp(t *testing.T, mutate func(*config.Config)) *App {
	t.Helper()
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = filepath.Join(t.TempDir(), "cookie.json")
	cfg.BaseURL = "https://vertexaisearch.cloud.google.com"
	cfg.DiscoveryEngineEndpoint = "https://discoveryengine.clients6.google.com/v1alpha/locations/global/widgetStreamAssist?key=k"
	cfg.DiscoveryEngineConfigID = "cfg-1"
	if mutate != nil {
		mutate(&cfg)
	}
	return New(cfg, "test")
}

// An oversized prompt on Enterprise must become a context-file attachment instead
// of being re-inlined into every replayed streamAssist body.
func TestAttachEnterpriseContextOffloadsOversizedPrompt(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	prompt := "# Tool Use\n" + strings.Repeat("func longCode() { /* padding */ }\n", app.Cfg.BridgeSpilloverChars/20)

	gotPrompt, images := app.attachEnterpriseContext(nil, prompt, nil)
	if len(gotPrompt) >= len(prompt) {
		t.Fatalf("prompt was not compacted: %d -> %d chars", len(prompt), len(gotPrompt))
	}
	if len(images) != 1 {
		t.Fatalf("expected one spilled attachment, got %d", len(images))
	}
	if !strings.Contains(images[0].Filename, "context.txt") {
		t.Errorf("attachment filename = %q, want a *context.txt spillover file", images[0].Filename)
	}
	if len(images[0].Data) == 0 {
		t.Error("spilled attachment carries no data")
	}
	if !strings.Contains(string(images[0].Data), "longCode") {
		t.Error("spilled attachment does not contain the moved prompt content")
	}
}

// Without any upload transport the content must stay inline: an attachment that
// cannot be uploaded would silently drop the user's context.
func TestAttachEnterpriseContextKeepsInliningWithoutUploadTransport(t *testing.T) {
	app := newEnterpriseApp(t, func(cfg *config.Config) {
		cfg.DiscoveryEngineEndpoint = ""
		cfg.DiscoveryEngineUploadEndpoint = ""
		cfg.DiscoveryEngineCsesidx = ""
	})
	prompt := strings.Repeat("x", app.Cfg.BridgeSpilloverChars+500)

	gotPrompt, images := app.attachEnterpriseContext(nil, prompt, nil)
	if gotPrompt != prompt {
		t.Errorf("prompt must stay untouched without an upload transport (%d -> %d chars)", len(prompt), len(gotPrompt))
	}
	if len(images) != 0 {
		t.Errorf("no attachments expected, got %d", len(images))
	}
}

// Consumer Gemini Web keeps its existing composer/spillover behaviour.
func TestAttachEnterpriseContextIgnoresConsumerOrigin(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = filepath.Join(t.TempDir(), "cookie.json")
	cfg.BaseURL = "https://gemini.google.com"
	app := New(cfg, "test")

	prompt := strings.Repeat("x", cfg.BridgeSpilloverChars+500)
	gotPrompt, images := app.attachEnterpriseContext(nil, prompt, nil)
	if gotPrompt != prompt || len(images) != 0 {
		t.Errorf("consumer origin must be untouched, got %d chars and %d attachments", len(gotPrompt), len(images))
	}
}

// Files referenced by path are read and attached so their contents never have to
// be re-inlined by the client's tools on later turns.
func TestAppendLocalFileAttachmentsReadsReferencedFiles(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 fake pdf body"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := newEnterpriseApp(t, nil)
	messages := []models.OpenAIMessage{{Role: "user", Content: "please summarise " + pdfPath}}

	prompt, images := app.attachEnterpriseContext(messages, "please summarise "+pdfPath, nil)
	if len(images) != 1 {
		t.Fatalf("expected the referenced PDF to be attached, got %d attachments", len(images))
	}
	if images[0].Filename != "report.pdf" {
		t.Errorf("attachment filename = %q", images[0].Filename)
	}
	if images[0].MIME != "application/pdf" {
		t.Errorf("attachment MIME = %q, want application/pdf", images[0].MIME)
	}
	if !strings.Contains(string(images[0].Data), "%PDF-1.4") {
		t.Error("attachment does not carry the file bytes")
	}
	if prompt != "please summarise "+pdfPath {
		t.Error("prompt must not be rewritten when files are simply referenced")
	}

	// Already-attached files are not attached twice.
	_, again := app.attachEnterpriseContext(messages, "please summarise "+pdfPath, images)
	if len(again) != 1 {
		t.Errorf("duplicate attachment: got %d, want 1", len(again))
	}
}

// The feature can be turned off, e.g. when a deployment wants verbatim inline
// prompts for audit.
func TestAttachEnterpriseContextRespectsConfig(t *testing.T) {
	app := newEnterpriseApp(t, func(cfg *config.Config) {
		cfg.EnterpriseAutoAttachLocalFiles = false
		cfg.EnterpriseAttachLargePrompt = false
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("note\n", 4000)), 0o600); err != nil {
		t.Fatal(err)
	}
	messages := []models.OpenAIMessage{{Role: "user", Content: "read " + path}}
	prompt := strings.Repeat("y", app.Cfg.BridgeSpilloverChars+500)

	gotPrompt, images := app.attachEnterpriseContext(messages, prompt, nil)
	if gotPrompt != prompt || len(images) != 0 {
		t.Errorf("config disabled attachments but got %d chars / %d attachments", len(gotPrompt), len(images))
	}
	if got := len([]format.Image(nil)); got != 0 {
		t.Fatal("unreachable")
	}
}
