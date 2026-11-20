package gemini

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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

func TestPrepareEnterpriseAttachmentsCachesAndDedupes(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var uploads int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = w.Write([]byte(`{"addContextFileResponse":{"session":"collections/c/engines/e/sessions/9","fileId":"f-` + strconv.Itoa(uploads) + `"}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineEndpoint:       srv.URL + "/v1alpha/locations/global/widgetStreamAssist",
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
		DiscoveryEngineUploadCacheSec: 60,
	}
	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	same := []byte("identical payload")
	prompt, ids, session, err := c.prepareEnterpriseAttachments("q", []RawFile{
		{Name: "a.txt", MIME: "text/plain", Data: same},
		{Name: "b.txt", MIME: "text/plain", Data: same},
	})
	if err != nil {
		t.Fatalf("prepareEnterpriseAttachments() error = %v", err)
	}
	if uploads != 1 {
		t.Errorf("identical files in one request uploaded %d times, want 1", uploads)
	}
	if len(ids) != 1 || ids[0] != "f-1" {
		t.Errorf("ids = %v, want a single deduped [f-1]", ids)
	}
	if session != "collections/c/engines/e/sessions/9" {
		t.Errorf("session = %q, want the upload session", session)
	}
	if prompt != "q" {
		t.Errorf("prompt = %q, want it untouched", prompt)
	}

	// A later turn resending the same bytes must reuse the cached fileId and
	// must not fall back to inlining.
	_, ids, _, err = c.prepareEnterpriseAttachments("q2", []RawFile{
		{Name: "a.txt", MIME: "text/plain", Data: same},
	})
	if err != nil {
		t.Fatalf("second prepareEnterpriseAttachments() error = %v", err)
	}
	if uploads != 1 {
		t.Errorf("cached upload was repeated (%d uploads), want 1", uploads)
	}
	if len(ids) != 1 || ids[0] != "f-1" {
		t.Errorf("cached ids = %v, want [f-1]", ids)
	}

	// Disabling the cache (0) forces a fresh upload.
	c2 := NewClient(config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineEndpoint:       srv.URL + "/v1alpha/locations/global/widgetStreamAssist",
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
		DiscoveryEngineUploadCacheSec: 0,
	})
	c2.HTTP = srv.Client()
	c2.Logf = func(string, ...any) {}
	if _, _, _, err := c2.prepareEnterpriseAttachments("q", []RawFile{{Name: "a.txt", MIME: "text/plain", Data: same}}); err != nil {
		t.Fatalf("uncached prepareEnterpriseAttachments() error = %v", err)
	}
	if uploads != 2 {
		t.Errorf("cache disabled but uploads = %d, want 2", uploads)
	}
}

func TestPrepareEnterpriseAttachmentsNotesSkippedFiles(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	// The upload endpoint 500s, so text files fall back to inlining; the inline
	// budget only fits one file, the rest must be reported to the model.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
	}
	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	big := bytes.Repeat([]byte("x"), maxInlineFileBytes)
	prompt, ids, _, err := c.prepareEnterpriseAttachments("q", []RawFile{
		{Name: "keep.txt", MIME: "text/plain", Data: big},
		{Name: "keep2.txt", MIME: "text/plain", Data: big},
		{Name: "drop.txt", MIME: "text/plain", Data: []byte("y")},
	})
	if err != nil {
		t.Fatalf("prepareEnterpriseAttachments() error = %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("ids = %v, want none when every upload failed", ids)
	}
	if !strings.Contains(prompt, "--- Attached file: keep.txt") {
		t.Errorf("expected the first text file to be inlined: %.120s", prompt)
	}
	if !strings.Contains(prompt, "could not be attached - inline budget exhausted: drop.txt") {
		t.Errorf("expected a note naming the skipped attachment: %s", prompt[len(prompt)-200:])
	}
}

// Images must reach the widget API byte-for-byte with an image MIME type — that
// is what makes the model able to actually see them (the same call the Gemini
// Enterprise app makes when you paste a picture into the composer).
func TestUploadEnterpriseFileSendsImageBytes(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32))
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = w.Write([]byte(`{"addContextFileResponse":{"session":"collections/c/engines/e/sessions/7","fileId":"img-1"}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
	}
	var logs []string
	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	fileID, session, err := c.UploadEnterpriseFile(RawFile{Name: "diagram.png", MIME: "image/png", Data: png})
	if err != nil {
		t.Fatalf("UploadEnterpriseFile() error = %v", err)
	}
	if fileID != "img-1" || session != "collections/c/engines/e/sessions/7" {
		t.Errorf("upload = (%q, %q)", fileID, session)
	}
	wantB64 := base64.StdEncoding.EncodeToString(png)
	if !strings.Contains(gotBody, `"mimeType":"image/png"`) {
		t.Errorf("upload body lost the image MIME type: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"fileName":"diagram.png"`) {
		t.Errorf("upload body lost the filename: %s", gotBody)
	}
	if !strings.Contains(gotBody, wantB64) {
		t.Error("upload body does not carry the exact image bytes")
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "image/png") || !strings.Contains(joined, "fileId=img-1") {
		t.Errorf("upload log should report the MIME type and fileId, got: %s", joined)
	}
}

// A binary attachment that cannot be uploaded must fail with an actionable
// message instead of being silently turned into prompt text.
func TestPrepareEnterpriseAttachmentsBinaryFailureIsActionable(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
	})
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	_, _, _, err := c.prepareEnterpriseAttachments("describe this", []RawFile{
		{Name: "photo.jpg", MIME: "image/jpeg", Data: []byte("\xff\xd8\xff\xe0fakejpeg")},
	})
	if err == nil {
		t.Fatal("expected an error for an un-uploadable image")
	}
	msg := err.Error()
	for _, want := range []string{"photo.jpg", "image/jpeg", "cannot be inlined", "discovery_engine_csesidx"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message should mention %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "prompt") && !strings.Contains(msg, "cannot be inlined") {
		t.Errorf("image must not be reported as inlined text: %s", msg)
	}
}

// With nothing configured, the tenant modelId from the tab's captured request is
// reused instead of sending no model at all — a real id beats "auto".
func TestEnterpriseModelIDLearnsFromCapturedTemplate(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")
	tpl := &DiscoveryTemplate{
		URL:  "https://discoveryengine.clients6.google.com/v1alpha/locations/global/widgetStreamAssist?key=k",
		Body: `{"configId":"cfg-9","streamAssistRequest":{"query":{"parts":[{"text":"hi"}]},"assistGenerationConfig":{"modelId":"gemini-2.5-pro"}}}`,
	}
	if err := SaveDiscoveryTemplate(cookieFile, tpl); err != nil {
		t.Fatal(err)
	}

	c := NewClient(config.Config{CookieFile: cookieFile})
	c.Logf = func(string, ...any) {}

	id, source := c.EnterpriseModelIDSource("gemini-4-hydrogen")
	if id != "gemini-2.5-pro" || source != ModelIDSourceCaptured {
		t.Errorf("EnterpriseModelIDSource() = (%q, %q), want (gemini-2.5-pro, captured)", id, source)
	}
	// An explicit default still wins over the learned id.
	c2 := NewClient(config.Config{CookieFile: cookieFile, DiscoveryEngineDefaultModelID: "gemini-2.5-flash"})
	c2.Logf = func(string, ...any) {}
	if id, source := c2.EnterpriseModelIDSource("gemini-4-hydrogen"); id != "gemini-2.5-flash" || source != ModelIDSourceConfigured {
		t.Errorf("configured default must win, got (%q, %q)", id, source)
	}
}

// A derived upload endpoint without a configId is guaranteed to 400, so fail with
// the fix instead of spending the round trip.
func TestUploadWithoutConfigIDFailsFast(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:              cookieFile,
		BaseURL:                 srv.URL,
		DiscoveryEngineEndpoint: srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
	})
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	_, _, err := c.UploadEnterpriseFile(RawFile{Name: "code_context.txt", MIME: "text/plain", Data: []byte("hello")})
	if err == nil {
		t.Fatal("expected an error without a configId")
	}
	if !strings.Contains(err.Error(), "configId") || !strings.Contains(err.Error(), "discovery_engine_config_id") {
		t.Errorf("error should name the missing configId and the fix: %v", err)
	}
	if hits != 0 {
		t.Errorf("no upload request should be attempted, got %d", hits)
	}
}

// A 400 from the widget must say which fields we actually sent.
func TestUploadBadRequestListsSentFields(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"One or more required fields are missing.","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineConfigID:       "cfg-1",
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
	})
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	_, _, err := c.UploadEnterpriseFile(RawFile{Name: "a.txt", MIME: "text/plain", Data: []byte("x")})
	if err == nil {
		t.Fatal("expected the upstream 400 to surface")
	}
	msg := err.Error()
	for _, want := range []string{"sent fields:", "configId", "addContextFileRequest", "fileContents"} {
		if !strings.Contains(msg, want) {
			t.Errorf("400 diagnostics should mention %q: %s", want, msg)
		}
	}
}

// The request log must name the modelId that is actually sent, not the alias.
func TestWidgetStreamAssistLogReportsEffectiveModel(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")
	if err := SaveDiscoveryTemplate(cookieFile, &DiscoveryTemplate{
		URL:  "https://discoveryengine.clients6.google.com/v1alpha/locations/global/widgetStreamAssist?key=k",
		Body: `{"configId":"cfg-9","streamAssistRequest":{"assistGenerationConfig":{"modelId":"gemini-2.5-pro"}}}`,
	}); err != nil {
		t.Fatal(err)
	}

	var logs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"replies":[{"groundedContent":{"text":"ok"}}]}}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:              cookieFile,
		BaseURL:                 srv.URL,
		DiscoveryEngineEndpoint: srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
	})
	c.HTTP = srv.Client()
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	_ = c.enterpriseGenerateStream("hi", ModelOptions{ModelName: "gemini-4-hydrogen"}, func(string) error { return nil })

	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, `modelId="gemini-2.5-pro"`) || !strings.Contains(joined, "source=captured") {
		t.Fatalf("log should report the effective modelId and source, got:\n%s", joined)
	}
	if strings.Contains(joined, `model="gemini-4-hydrogen"`) {
		t.Errorf("log still implies the alias was sent:\n%s", joined)
	}
}
