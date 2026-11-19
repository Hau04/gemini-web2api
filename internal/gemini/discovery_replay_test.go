package gemini

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestSubstitutePrompt(t *testing.T) {
	tpl := &DiscoveryTemplate{Body: `{"query":{"text":"__W2A_PROMPT__"},"session":"s1"}`}
	out, err := tpl.SubstitutePrompt(`say "hi"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, `"say \"hi\""`) || strings.Contains(out, DiscoveryPromptPlaceholder) {
		t.Fatalf("placeholder not replaced with escaped prompt, got: %s", out)
	}

	// Hint-based: replace the LAST occurrence of the captured prompt.
	tpl = &DiscoveryTemplate{
		Body:       `{"history":["old prompt"],"query":{"text":"old prompt"}}`,
		PromptHint: "old prompt",
	}
	out, err = tpl.SubstitutePrompt("new prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != `{"history":["old prompt"],"query":{"text":"new prompt"}}` {
		t.Fatalf("expected last-occurrence replacement, got: %s", out)
	}

	// No marker and no hint -> actionable error.
	tpl = &DiscoveryTemplate{Body: `{"query":{"text":"?"}}`}
	if _, err := tpl.SubstitutePrompt("x"); err == nil {
		t.Fatal("expected error for template without prompt marker")
	}
}

func TestCleanDiscoveryText(t *testing.T) {
	if got := cleanDiscoveryText("I am transferring your request to BigQuery.\n[[section:routing]]\nReal answer"); !strings.Contains(got, "Real answer") || strings.Contains(got, "section") {
		t.Fatalf("routing section not stripped: %q", got)
	}
	if got := cleanDiscoveryText("[[section:tools]]\nAnswer"); strings.Contains(got, "section") {
		t.Fatalf("standalone section marker not stripped: %q", got)
	}
	if got := cleanDiscoveryText("I am checking the available tools and integrations in this environment"); got != "" {
		t.Fatalf("standalone routing line should be empty, got %q", got)
	}
	if got := cleanDiscoveryText("Just text"); got != "Just text" {
		t.Fatalf("plain text altered: %q", got)
	}
}

func TestDiscoveryStreamParsesStreamAssist(t *testing.T) {
	var got strings.Builder
	s := &discoveryStream{emit: func(d string) error { got.WriteString(d); return nil }}
	p := &discoveryParser{objAt: -1, onObj: s.processObject}

	// Feed a mixed stream: anti-XSSI prefix, SSE framing, cumulative + delta shapes.
	p.feed(")]}'\n")
	p.feed(`{"streamAssistResponse":{"answer":{"state":"RUNNING","replies":[{"groundedContent":{"content":{"role":"model","text":"Hel"}}}]}}}`)
	p.feed(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","name":"a1","replies":[{"groundedContent":{"content":{"role":"model","text":"Hello world"}}}]}}}`)

	if got.String() != "Hello world" {
		t.Fatalf("expected cumulative dedupe to yield 'Hello world', got %q", got.String())
	}
	if !s.hasText || s.failed {
		t.Fatalf("unexpected state: hasText=%v failed=%v", s.hasText, s.failed)
	}

	// Thought replies are skipped; user replies ignored.
	got.Reset()
	s2 := &discoveryStream{emit: func(d string) error { got.WriteString(d); return nil }}
	p2 := &discoveryParser{objAt: -1, onObj: s2.processObject}
	p2.feed(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"role":"user","text":"me"}}},{"groundedContent":{"thought":true,"content":{"text":"rumination"}}},{"groundedContent":{"content":{"role":"model","parts":[{"text":"A"},{"thought":true,"text":"B"},{"text":"C"}]}}}]}}}`)
	if got.String() != "AC" {
		t.Fatalf("expected 'AC' after thought/user filtering, got %q", got.String())
	}
}

func TestGenerateViaDiscoveryReplay(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	cookie := `{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`
	if err := os.WriteFile(cookieFile, []byte(cookie), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	var gotAuth, gotCookie, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotAuth = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"role":"model","text":"replayed ok"}}}]}}}`))
	}))
	defer srv.Close()

	tpl := &DiscoveryTemplate{
		URL:     srv.URL,
		Method:  http.MethodPost,
		Headers: map[string]string{"Content-Type": "application/json", "Authorization": "SAPISIDHASH 1_abc", "X-Custom": "keep"},
		Body:    `{"query":{"text":"__W2A_PROMPT__"}}`,
	}
	if err := SaveDiscoveryTemplate(cookieFile, tpl); err != nil {
		t.Fatalf("save template: %v", err)
	}

	c := NewClient(config.Config{CookieFile: cookieFile, RetryAttempts: 1})
	c.HTTP = http.DefaultClient

	// Latched Enterprise origin + template -> replay works with the "browser closed".
	c.MarkBardUnsupported()
	text, err := c.Generate("hello enterprise", ModelOptions{ModelName: "gemini-4-argon"}, nil)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if text != "replayed ok" {
		t.Fatalf("unexpected replay text: %q", text)
	}
	if !strings.Contains(gotBody, "hello enterprise") || strings.Contains(gotBody, DiscoveryPromptPlaceholder) {
		t.Fatalf("prompt not substituted into replayed body: %s", gotBody)
	}
	if gotAuth == "" {
		t.Fatal("Authorization header missing on replay")
	}
	if !strings.Contains(gotCookie, "SAPISID=abc") {
		t.Fatalf("fresh cookies not sent: %q", gotCookie)
	}

	// HasDiscoveryTemplate reflects the persisted template.
	if !c.HasDiscoveryTemplate() {
		t.Fatal("HasDiscoveryTemplate must be true after SaveDiscoveryTemplate")
	}
}

func TestGenerateViaDiscoveryWithoutTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := filepath.Join(tmpDir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}
	c := NewClient(config.Config{CookieFile: cookieFile, RetryAttempts: 1})
	c.MarkBardUnsupported()

	_, err := c.Generate("hello", ModelOptions{ModelName: "gemini-4-argon"}, nil)
	if err == nil {
		t.Fatal("expected error without template")
	}
	if !IsUnsupported(err) {
		t.Fatalf("error must stay IsUnsupported (routing falls back to bridge/503), got: %v", err)
	}
	if !strings.Contains(err.Error(), "template") {
		t.Fatalf("error must mention the template option, got: %v", err)
	}
}
