package gemini

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

// mirrorTestClient builds a client whose upload endpoint always succeeds, so the
// attachment travels as a context file and nothing forces it inline.
func mirrorTestClient(t *testing.T, mirror bool) (*Client, *[]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"addContextFileResponse":{"session":"collections/c/engines/e/sessions/9","fileId":"file-9"}}`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		CookieFile:                    filepath.Join(t.TempDir(), "cookie.json"),
		BaseURL:                       srv.URL,
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
		EnterpriseMirrorTextInline:    mirror,
	}
	logs := &[]string{}
	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(format string, args ...any) { *logs = append(*logs, fmt.Sprintf(format, args...)) }
	return c, logs
}

// The reported failure: the instructions only ever lived in code_context.txt,
// which the tenant's model cannot read. The text must also be in the prompt.
func TestUploadedInstructionsAreAlsoInThePrompt(t *testing.T) {
	c, logs := mirrorTestClient(t, true)
	instructions := "# Instructions\nAlways answer in English.\nStep 1: create the folder."

	prompt, ids, session, err := c.prepareEnterpriseAttachments("base prompt", []RawFile{
		{Name: "code_context.txt", MIME: "text/plain", Data: []byte(instructions)},
	})
	if err != nil {
		t.Fatalf("prepareEnterpriseAttachments() error = %v", err)
	}
	if len(ids) != 1 || session == "" {
		t.Errorf("the file should still upload for the tenant, got ids=%v session=%q", ids, session)
	}
	if !strings.Contains(prompt, "Step 1: create the folder.") {
		t.Errorf("the instructions must be readable from the prompt itself, got %q", prompt)
	}
	if !strings.Contains(prompt, "Attached file: code_context.txt") {
		t.Errorf("mirrored block should name the file, got %q", prompt)
	}
	if !strings.Contains(prompt, "not reliable") {
		t.Errorf("the mirrored copy should say why it is duplicated, got %q", prompt)
	}
	if len(*logs) == 0 || !strings.Contains(strings.Join(*logs, "\n"), "also written into the prompt") {
		t.Errorf("the log should state that the text was mirrored, got %v", *logs)
	}
}

// With mirroring off the request stays upload-only (the old behaviour).
func TestMirrorOffKeepsUploadOnly(t *testing.T) {
	c, _ := mirrorTestClient(t, false)

	prompt, ids, _, err := c.prepareEnterpriseAttachments("base prompt", []RawFile{
		{Name: "code_context.txt", MIME: "text/plain", Data: []byte("secret instructions here")},
	})
	if err != nil {
		t.Fatalf("prepareEnterpriseAttachments() error = %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected the upload to happen, got ids=%v", ids)
	}
	if strings.Contains(prompt, "secret instructions here") {
		t.Errorf("mirroring is off, content must not be inlined: %q", prompt)
	}
}

// Images are not text: mirroring must not try to push their bytes into a prompt.
func TestMirrorSkipsBinaryAttachments(t *testing.T) {
	c, _ := mirrorTestClient(t, true)
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 64))

	prompt, ids, _, err := c.prepareEnterpriseAttachments("base prompt", []RawFile{
		{Name: "diagram.png", MIME: "image/png", Data: png},
	})
	if err != nil {
		t.Fatalf("prepareEnterpriseAttachments() error = %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("the image should still upload, got ids=%v", ids)
	}
	if strings.Contains(prompt, "Attached file: diagram.png") {
		t.Errorf("a binary attachment must not be mirrored into the prompt: %q", prompt)
	}
}

// A file bigger than the inline budget is mirrored up to the budget and marked.
func TestMirrorRespectsTheInlineBudget(t *testing.T) {
	c, _ := mirrorTestClient(t, true)
	big := strings.Repeat("x", maxInlineFileBytes+1024)

	prompt, _, _, err := c.prepareEnterpriseAttachments("base", []RawFile{
		{Name: "huge.txt", MIME: "text/plain", Data: []byte(big)},
	})
	if err != nil {
		t.Fatalf("prepareEnterpriseAttachments() error = %v", err)
	}
	if !strings.Contains(prompt, "...[truncated]") {
		t.Error("an oversized mirror should be marked as truncated")
	}
	if len(prompt) > len(big)+1024 {
		t.Errorf("mirroring must not exceed the budget: prompt is %d bytes", len(prompt))
	}
}
