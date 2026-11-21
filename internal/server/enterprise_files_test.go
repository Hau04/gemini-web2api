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

// An oversized prompt becomes a context-file attachment instead of being
// re-inlined into every replayed streamAssist body — but only when the model can
// read that attachment back, i.e. with mirroring switched off.
func TestAttachEnterpriseContextOffloadsOversizedPrompt(t *testing.T) {
	app := newEnterpriseApp(t, func(cfg *config.Config) {
		cfg.EnterpriseMirrorTextInline = false
	})
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

// By default the prompt keeps its own text: on this tenant a context file is not
// readable by the model, so spilling would lose the instructions.
func TestAttachEnterpriseContextKeepsPromptInlineByDefault(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	prompt := "# Tool Use\n" + strings.Repeat("func longCode() { /* padding */ }\n", app.Cfg.BridgeSpilloverChars/20)

	gotPrompt, images := app.attachEnterpriseContext(nil, prompt, nil)
	if len(images) != 0 {
		t.Fatalf("expected no spillover attachment by default, got %d", len(images))
	}
	if gotPrompt != prompt {
		t.Fatalf("prompt must stay inline by default: %d -> %d chars", len(prompt), len(gotPrompt))
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
	if !strings.HasSuffix(prompt, "please summarise "+pdfPath) {
		t.Errorf("the user's own request must survive verbatim at the end, got: %.200q", prompt)
	}
	if !strings.Contains(prompt, "report.pdf") || !strings.Contains(prompt, "do not re-read") {
		t.Errorf("prompt should carry the attachment inventory, got: %.200q", prompt)
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

// An edit loop must not need a read after every write: the prompt says the
// attachment is the current on-disk version and forbids a verification fetch.
func TestAttachEnterpriseContextTellsModelNotToReReadAttachedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handlers.ts")
	var b strings.Builder
	for i := 0; i < 300; i++ {
		b.WriteString("export function handler")
		b.WriteString(strings.Repeat("x", 6))
		b.WriteString("(req: Request) { return req.body; }\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	app := newEnterpriseApp(t, nil)
	messages := []models.OpenAIMessage{{Role: "user", Content: "refactor the error handling in " + path}}

	prompt, images := app.attachEnterpriseContext(messages, "refactor the error handling in "+path, nil)
	if len(images) != 1 {
		t.Fatalf("expected the source file to be attached, got %d attachments", len(images))
	}
	if !strings.HasPrefix(prompt, "# Attached workspace files") {
		t.Errorf("prompt should open with the attachment inventory, got: %.120q", prompt)
	}
	for _, want := range []string{"handlers.ts", "300 lines", "read_file", "re-attached automatically", "str_replace"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("inventory should mention %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "refactor the error handling") {
		t.Error("the user's own instruction must survive")
	}
}

// After the client writes the model's patch, the next request carries the new
// bytes automatically — so the agent never has to spend a tool call reading it.
func TestAttachEnterpriseContextReattachesEditedFileWithoutToolCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.py")
	v1 := strings.Repeat("# padding line to pass the large-code threshold\n", 300) + "def run(): return 1\n"
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}

	app := newEnterpriseApp(t, nil)
	messages := []models.OpenAIMessage{{Role: "user", Content: "fix run() in " + path}}

	_, first := app.attachEnterpriseContext(messages, "fix run() in "+path, nil)
	if len(first) != 1 || !strings.Contains(string(first[0].Data), "def run(): return 1") {
		t.Fatalf("first turn should attach the current file, got %d attachments", len(first))
	}

	// The agent's edit lands on disk (as WorkBuddy's write tool would).
	v2 := strings.Replace(v1, "def run(): return 1", "def run(): return 2  # fixed", 1)
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}

	_, second := app.attachEnterpriseContext(messages, "fix run() in "+path, nil)
	if len(second) != 1 {
		t.Fatalf("second turn attachments = %d", len(second))
	}
	if !strings.Contains(string(second[0].Data), "return 2  # fixed") {
		t.Error("second turn must attach the freshly edited bytes, not a cached copy")
	}
}
