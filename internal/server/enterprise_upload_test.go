package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
)

func TestBridgeUploadTemplateCapture(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.CookieFile = filepath.Join(dir, "cookie.json")
	app := New(cfg, "test")
	handler := app.Handler()

	// Nothing captured yet.
	req := httptest.NewRequest(http.MethodGet, "/bridge/uploadtemplate", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"have_template":false`) {
		t.Fatalf("expected have_template=false, got %d %s", rec.Code, rec.Body.String())
	}

	body := `{"location":"locations/global","configId":"cfg","addContextFileRequest":{` +
		`"name":"collections/default_collection/engines/e/sessions/1",` +
		`"fileName":"__W2A_FILE_NAME__","mimeType":"__W2A_FILE_MIME__","fileContents":"__W2A_FILE_B64__"}}`
	payload, _ := json.Marshal(map[string]any{
		"url":     "https://vertexaisearch.cloud.google.com/v1alpha/locations/global/widgetAddContextFile",
		"method":  "POST",
		"headers": map[string]string{"x-server-token": "t"},
		"body":    body,
		"tab":     "vertexaisearch.cloud.google.com",
	})

	req = httptest.NewRequest(http.MethodPost, "/bridge/uploadtemplate", strings.NewReader(string(payload)))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 storing the template, got %d: %s", rec.Code, rec.Body.String())
	}

	tpl := gemini.LoadDiscoveryUploadTemplate(cfg.CookieFile)
	if tpl == nil {
		t.Fatal("template was not persisted next to the cookie file")
	}
	if !strings.Contains(tpl.URL, "widgetAddContextFile") || tpl.Body != body {
		t.Fatalf("unexpected stored template: %+v", tpl)
	}

	req = httptest.NewRequest(http.MethodGet, "/bridge/uploadtemplate", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"have_template":true`) {
		t.Fatalf("expected have_template=true after capture, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestBridgeUploadTemplateRejectsNonGoogleOrUntemplated(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.CookieFile = filepath.Join(dir, "cookie.json")
	app := New(cfg, "test")
	handler := app.Handler()

	cases := map[string]string{
		"non-google host": `{"url":"https://evil.example.com/upload","body":"{\"fileContents\":\"__W2A_FILE_B64__\"}"}`,
		"no placeholder":  `{"url":"https://vertexaisearch.cloud.google.com/v1alpha/locations/global/widgetAddContextFile","body":"{\"fileContents\":\"abc\"}"}`,
		"missing body":    `{"url":"https://vertexaisearch.cloud.google.com/x"}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/bridge/uploadtemplate", strings.NewReader(payload))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if gemini.LoadDiscoveryUploadTemplate(cfg.CookieFile) != nil {
		t.Error("invalid payloads must not persist a template")
	}
}

func TestPrepareAttachmentsSkipsContentPushOnEnterprise(t *testing.T) {
	cfg := config.Default()
	cfg.CookieFile = filepath.Join(t.TempDir(), "cookie.json")
	cfg.BaseURL = "https://vertexaisearch.cloud.google.com"
	app := New(cfg, "test")

	// UsesDiscoveryEngine() short-circuits before any content-push upload, so
	// raw files come back even though no cookies/network are available.
	refs, files, err := app.prepareAttachments(nil)
	if err != nil || refs != nil || files != nil {
		t.Fatalf("expected no-op for empty attachments, got refs=%v files=%v err=%v", refs, files, err)
	}
	if !app.Gem.UsesDiscoveryEngine() {
		t.Fatal("expected the Enterprise origin to be detected from base_url")
	}
}
