package gemini

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestDiscoveryUploadTemplateSubstituteFile(t *testing.T) {
	tpl := &DiscoveryUploadTemplate{
		URL: "https://vertexaisearch.cloud.google.com/v1alpha/locations/global/widgetAddContextFile",
		Body: `{"location":"locations/global","configId":"cfg","addContextFileRequest":{` +
			`"name":"collections/default_collection/engines/e/sessions/42",` +
			`"fileName":"__W2A_FILE_NAME__","mimeType":"__W2A_FILE_MIME__","fileContents":"__W2A_FILE_B64__"}}`,
	}
	body, err := tpl.SubstituteFile(RawFile{Name: `notes "v2".txt`, MIME: "text/plain", Data: []byte("hello enterprise")})
	if err != nil {
		t.Fatalf("SubstituteFile() error = %v", err)
	}
	if strings.Contains(body, UploadFileB64Placeholder) || strings.Contains(body, UploadFileMimePlaceholder) {
		t.Errorf("placeholders were not substituted: %s", body)
	}
	wantB64 := base64.StdEncoding.EncodeToString([]byte("hello enterprise"))
	if !strings.Contains(body, wantB64) {
		t.Errorf("body missing base64 content: %s", body)
	}
	if !strings.Contains(body, "text/plain") {
		t.Errorf("body missing mime type: %s", body)
	}
	if !strings.Contains(body, `notes \"v2\".txt`) {
		t.Errorf("file name was not JSON-escaped into the body: %s", body)
	}
}

func TestDiscoveryUploadTemplateStructuralFallback(t *testing.T) {
	// A template captured without placeholders must still be usable.
	tpl := &DiscoveryUploadTemplate{
		URL: "https://example.googleapis.com/v1alpha/locations/global/widgetAddContextFile",
		Body: `{"configId":"cfg","addContextFileRequest":{"name":"collections/c/engines/e/sessions/7",` +
			`"fileName":"old.txt","mimeType":"text/plain","fileContents":"b2xk"}}`,
	}
	body, err := tpl.SubstituteFile(RawFile{Name: "new.md", MIME: "text/markdown", Data: []byte("# hi")})
	if err != nil {
		t.Fatalf("SubstituteFile() error = %v", err)
	}
	if !strings.Contains(body, `"fileName":"new.md"`) || !strings.Contains(body, `"mimeType":"text/markdown"`) {
		t.Errorf("structural substitution did not replace name/mime: %s", body)
	}
	if !strings.Contains(body, base64.StdEncoding.EncodeToString([]byte("# hi"))) {
		t.Errorf("structural substitution did not replace content: %s", body)
	}
	if strings.Contains(body, "old.txt") {
		t.Errorf("stale file name survived: %s", body)
	}
}

func TestParseUploadResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want uploadResponse
	}{
		{
			name: "widget wrapper",
			body: `{"addContextFileResponse":{"session":"collections/c/engines/e/sessions/42","fileId":"9876","tokenCount":"12"}}`,
			want: uploadResponse{fileID: "9876", session: "collections/c/engines/e/sessions/42"},
		},
		{
			name: "anti-xssi prefix",
			body: ")]}'\n" + `{"fileId":"55"}`,
			want: uploadResponse{fileID: "55"},
		},
		{
			name: "snake case",
			body: `{"context_file_id":"77"}`,
			want: uploadResponse{fileID: "77"},
		},
		{
			name: "no id",
			body: `{"error":{"message":"nope"}}`,
			want: uploadResponse{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseUploadResponse([]byte(tc.body))
			if got.fileID != tc.want.fileID || (tc.want.session != "" && got.session != tc.want.session) {
				t.Errorf("parseUploadResponse() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestInjectAttachmentContext(t *testing.T) {
	widget := `{"configId":"cfg","streamAssistRequest":{"query":{"parts":[{"text":"__W2A_PROMPT__"}]},"fileIds":[]}}`
	out, err := InjectAttachmentContext(widget, []string{"f1", "f2"}, "collections/c/engines/e/sessions/9")
	if err != nil {
		t.Fatalf("InjectAttachmentContext() error = %v", err)
	}
	for _, want := range []string{`"f1"`, `"f2"`, "collections/c/engines/e/sessions/9", "__W2A_PROMPT__"} {
		if !strings.Contains(out, want) {
			t.Errorf("injected body missing %q: %s", want, out)
		}
	}

	// Flat REST shape (no widget wrapper).
	rest := `{"session":"old","query":{"text":"hi"},"fileIds":[]}`
	out, err = InjectAttachmentContext(rest, []string{"abc"}, "")
	if err != nil {
		t.Fatalf("InjectAttachmentContext(rest) error = %v", err)
	}
	if !strings.Contains(out, `"abc"`) || !strings.Contains(out, `"session":"old"`) {
		t.Errorf("flat body not injected correctly: %s", out)
	}

	// No files is a no-op.
	if out, err := InjectAttachmentContext(rest, nil, ""); err != nil || out != rest {
		t.Errorf("expected no-op for empty file list, got %q (%v)", out, err)
	}

	if _, err := InjectAttachmentContext(`not json`, []string{"a"}, ""); err == nil {
		t.Error("expected an error for a non-JSON body")
	}
}

func TestSwapWidgetMethod(t *testing.T) {
	got := swapWidgetMethod("https://vertexaisearch.cloud.google.com/v1alpha/locations/global/widgetStreamAssist", "widgetAddContextFile")
	want := "https://vertexaisearch.cloud.google.com/v1alpha/locations/global/widgetAddContextFile"
	if got != want {
		t.Errorf("swapWidgetMethod() = %q, want %q", got, want)
	}
	if got := swapWidgetMethod("not a url", "widgetAddContextFile"); got != "" {
		t.Errorf("expected empty result for invalid endpoint, got %q", got)
	}
}

func TestRawFileIsTextLike(t *testing.T) {
	cases := []struct {
		file RawFile
		want bool
	}{
		{RawFile{Name: "a.go", MIME: "text/x-go", Data: []byte("package main")}, true},
		{RawFile{Name: "a.json", MIME: "application/json", Data: []byte("{}")}, true},
		{RawFile{Name: "a.bin", MIME: "application/octet-stream", Data: []byte("plain text")}, true},
		{RawFile{Name: "a.png", MIME: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}, false},
		{RawFile{Name: "a.bin", MIME: "application/octet-stream", Data: []byte{0x00, 0x01, 0x02}}, false},
	}
	for _, tc := range cases {
		if got := tc.file.IsTextLike(); got != tc.want {
			t.Errorf("IsTextLike(%s/%s) = %t, want %t", tc.file.Name, tc.file.MIME, got, tc.want)
		}
	}
}

func TestPrepareEnterpriseAttachmentsInlinesText(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")
	c := NewClient(config.Config{CookieFile: cookieFile, BaseURL: "https://vertexaisearch.cloud.google.com"})
	c.Logf = func(string, ...any) {}

	prompt, ids, session, err := c.prepareEnterpriseAttachments("base prompt", []RawFile{
		{Name: "main.go", MIME: "text/x-go", Data: []byte("package main\n")},
	})
	if err != nil {
		t.Fatalf("prepareEnterpriseAttachments() error = %v", err)
	}
	if len(ids) != 0 || session != "" {
		t.Errorf("expected no uploaded files without an endpoint, got ids=%v session=%q", ids, session)
	}
	if !strings.HasPrefix(prompt, "base prompt") {
		t.Errorf("prompt prefix was not preserved: %q", prompt)
	}
	if !strings.Contains(prompt, "Attached file: main.go") || !strings.Contains(prompt, "package main") {
		t.Errorf("expected the text file to be inlined, got %q", prompt)
	}

	// Binary attachments must fail loudly instead of being silently dropped.
	if _, _, _, err := c.prepareEnterpriseAttachments("p", []RawFile{
		{Name: "scan.pdf", MIME: "application/pdf", Data: []byte{0x25, 0x50, 0x44, 0x46}},
	}); err == nil {
		t.Error("expected an error for a binary attachment without an upload endpoint")
	}
}

func TestUploadEnterpriseFileReplay(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"addContextFileResponse":{"session":"collections/c/engines/e/sessions/123","fileId":"abc123"}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineEndpoint:       srv.URL + "/v1alpha/locations/global/widgetStreamAssist",
		DiscoveryEngineConfigID:       "cfg-1",
		DiscoveryEngineServerToken:    "server-token",
		DiscoveryEngineUploadEndpoint: "",
	}
	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	fileID, session, err := c.UploadEnterpriseFile(RawFile{Name: "notes.txt", MIME: "text/plain", Data: []byte("hello")})
	if err != nil {
		t.Fatalf("UploadEnterpriseFile() error = %v", err)
	}
	if fileID != "abc123" || session != "collections/c/engines/e/sessions/123" {
		t.Errorf("UploadEnterpriseFile() = (%q, %q), want (abc123, .../sessions/123)", fileID, session)
	}
	if gotPath != "/v1alpha/locations/global/widgetAddContextFile" {
		t.Errorf("upload path = %q, want the widgetAddContextFile endpoint derived from the streamAssist endpoint", gotPath)
	}
	for _, want := range []string{`"fileName":"notes.txt"`, `"mimeType":"text/plain"`, base64.StdEncoding.EncodeToString([]byte("hello")), `"configId":"cfg-1"`} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("upload body missing %q: %s", want, gotBody)
		}
	}
}

func TestEnterpriseGenerateStreamSendsFileIDs(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var uploadCalled bool
	var assistBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(r.URL.Path, "widgetAddContextFile"):
			uploadCalled = true
			_, _ = w.Write([]byte(`{"addContextFileResponse":{"session":"collections/c/engines/e/sessions/555","fileId":"file-77"}}`))
		case strings.Contains(r.URL.Path, "widgetStreamAssist"):
			assistBody = string(raw)
			_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","name":"answers/1","replies":[{"groundedContent":{"content":{"role":"model","text":"attached file read"}}}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config.Config{
		CookieFile:              cookieFile,
		BaseURL:                 srv.URL,
		DiscoveryEngineEndpoint: srv.URL + "/v1alpha/locations/global/widgetStreamAssist",
		DiscoveryEngineConfigID: "cfg-9",
		RetryAttempts:           1,
	}
	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}
	c.MarkBardUnsupported()

	var out strings.Builder
	err := c.GenerateStream("summarize this", ModelOptions{
		ModelName: "gemini-4-hydrogen",
		Files:     []RawFile{{Name: "doc.md", MIME: "text/markdown", Data: []byte("# Title")}},
	}, nil, func(delta string) error {
		out.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatalf("GenerateStream() error = %v", err)
	}
	if !uploadCalled {
		t.Fatal("expected the context-file upload endpoint to be called")
	}
	if !strings.Contains(assistBody, `"fileIds":["file-77"]`) {
		t.Errorf("streamAssist body missing fileIds: %s", assistBody)
	}
	if !strings.Contains(assistBody, "collections/c/engines/e/sessions/555") {
		t.Errorf("streamAssist body missing the upload session: %s", assistBody)
	}
	if !strings.Contains(out.String(), "attached file read") {
		t.Errorf("unexpected answer text: %q", out.String())
	}
}

func TestLoadAndSaveDiscoveryUploadTemplate(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")
	if tpl := LoadDiscoveryUploadTemplate(cookieFile); tpl != nil {
		t.Fatalf("expected no template initially, got %+v", tpl)
	}
	want := &DiscoveryUploadTemplate{
		URL:     "https://vertexaisearch.cloud.google.com/v1alpha/locations/global/widgetAddContextFile",
		Method:  "POST",
		Headers: map[string]string{"x-server-token": "t"},
		Body:    `{"addContextFileRequest":{"fileName":"__W2A_FILE_NAME__"}}`,
	}
	if err := SaveDiscoveryUploadTemplate(cookieFile, want); err != nil {
		t.Fatalf("SaveDiscoveryUploadTemplate() error = %v", err)
	}
	got := LoadDiscoveryUploadTemplate(cookieFile)
	if got == nil || got.URL != want.URL || got.Body != want.Body {
		t.Fatalf("LoadDiscoveryUploadTemplate() = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "discovery_upload_template.json")); err != nil {
		t.Errorf("template file was not created next to the cookie file: %v", err)
	}
}
