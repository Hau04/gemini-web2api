package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
)

func mediaTestApp(t *testing.T, gen func(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error)) *App {
	t.Helper()
	cfg := config.Default()
	cfg.APIKeys = []string{}
	app := New(cfg, "test")
	app.Logf = func(string, ...any) {}
	app.mediaGenerate = gen
	return app
}

func postMedia(t *testing.T, app *App, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestImageGenerationReturnsAssets(t *testing.T) {
	var seenPrompt string
	app := mediaTestApp(t, func(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
		seenPrompt = prompt
		return "Here is your image.", []gemini.MediaAsset{
			{Kind: "image", MIME: "image/png", URL: "https://lh3.googleusercontent.com/gg/abc", Source: "url"},
			{Kind: "video", MIME: "video/mp4", URL: "https://storage.googleapis.com/v/clip.mp4", Source: "url"},
		}, nil
	})

	rec := postMedia(t, app, "/v1/images/generations", `{"model":"gemini-4-hydrogen","prompt":"a red cube on a table","size":"1024x1024"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Created int64 `json:"created"`
		Kind    string
		Model   string
		Data    []map[string]any
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if resp.Kind != "image" || resp.Model != "gemini-4-hydrogen" {
		t.Errorf("wrong envelope: %+v", resp)
	}
	// The video asset must not leak into an image response.
	if len(resp.Data) != 1 {
		t.Fatalf("expected only the image asset, got %+v", resp.Data)
	}
	if resp.Data[0]["url"] != "https://lh3.googleusercontent.com/gg/abc" {
		t.Errorf("URL not returned: %+v", resp.Data[0])
	}
	// The instruction must actually ask for the image tool, not just echo the prompt.
	if !strings.Contains(seenPrompt, "image generation tool") || !strings.Contains(seenPrompt, "a red cube on a table") {
		t.Errorf("instruction prompt does not request image generation: %q", seenPrompt)
	}
	if !strings.Contains(seenPrompt, "1024x1024") {
		t.Errorf("size was not forwarded: %q", seenPrompt)
	}
}

func TestImageGenerationInlinesBase64WhenAsked(t *testing.T) {
	app := mediaTestApp(t, func(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
		return "", []gemini.MediaAsset{{Kind: "image", MIME: "image/png", DataB64: "AAAABBBB", Source: "inline"}}, nil
	})

	rec := postMedia(t, app, "/v1/images/generations", `{"prompt":"draw a cat","response_format":"b64_json"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []map[string]any
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0]["b64_json"] != "AAAABBBB" {
		t.Fatalf("inline asset not returned as b64_json: %+v", resp.Data)
	}
	// A data: URL keeps clients that only understand "url" working.
	if url, _ := resp.Data[0]["url"].(string); !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("expected a data: URL alongside b64_json, got %q", url)
	}
}

func TestMediaGenerationReportsTextInsteadOfInventingMedia(t *testing.T) {
	app := mediaTestApp(t, func(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
		return "I'm sorry, but I can't create images in this chat.", nil, nil
	})

	rec := postMedia(t, app, "/v1/images/generations", `{"prompt":"draw a cat"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when no media was produced, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"no_media", "produced no image", "I'm sorry"} {
		if !strings.Contains(body, want) {
			t.Errorf("error body should contain %q: %s", want, body)
		}
	}
}

func TestMediaGenerationDetectsUnavailableModel(t *testing.T) {
	app := mediaTestApp(t, func(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
		return mediaUnavailableImage, nil, nil
	})

	rec := postMedia(t, app, "/v1/images/generations", `{"prompt":"draw a cat"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "cannot generate images") {
		t.Errorf("marker should be reported as an unsupported model: %s", rec.Body.String())
	}
}

func TestMediaGenerationValidatesInput(t *testing.T) {
	app := mediaTestApp(t, func(string, gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
		t.Fatal("generation must not run for an invalid request")
		return "", nil, nil
	})

	if rec := postMedia(t, app, "/v1/images/generations", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON should be 400, got %d", rec.Code)
	}
	if rec := postMedia(t, app, "/v1/images/generations", `{"prompt":"  "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("blank prompt should be 400, got %d", rec.Code)
	}
	if rec := postMedia(t, app, "/v1/images/generations", `{"prompt":"x","response_format":"png"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown response_format should be 400, got %d", rec.Code)
	}
}

func TestVideoGenerationUsesVideoInstruction(t *testing.T) {
	var seenPrompt string
	app := mediaTestApp(t, func(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
		seenPrompt = prompt
		return "", []gemini.MediaAsset{{Kind: "video", MIME: "video/mp4", URL: "https://storage.googleapis.com/v/clip.mp4"}}, nil
	})

	rec := postMedia(t, app, "/v1/videos/generations", `{"prompt":"a wave crashing","aspect_ratio":"16:9","duration_seconds":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(seenPrompt, "video generation tool") || !strings.Contains(seenPrompt, "16:9") || !strings.Contains(seenPrompt, "8 seconds") {
		t.Errorf("video instruction incomplete: %q", seenPrompt)
	}
	if !strings.Contains(rec.Body.String(), "clip.mp4") {
		t.Errorf("video URL missing from response: %s", rec.Body.String())
	}
}

func TestMediaGenerationPropagatesUpstreamError(t *testing.T) {
	app := mediaTestApp(t, func(string, gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
		return "", nil, errors.New("gemini upstream rejected request: cookie expired")
	})

	rec := postMedia(t, app, "/v1/images/generations", `{"prompt":"draw"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "cookie expired") {
		t.Fatalf("upstream error should surface verbatim: %d %s", rec.Code, rec.Body.String())
	}
}

func TestMediaModelOptionsFallsBackForUnknownAlias(t *testing.T) {
	app := mediaTestApp(t, nil)

	opt, label := app.mediaModelOptions("nano-banana")
	if label != "nano-banana" {
		t.Errorf("requested alias must be preserved for upstream resolution, got %q", label)
	}
	if opt.ModelName != "nano-banana" {
		t.Errorf("ModelName should carry the alias, got %q", opt.ModelName)
	}
	// A transport mode is required to build a consumer request at all.
	if opt.ModelID == 0 {
		t.Errorf("unknown alias should inherit the default transport mode, got %+v", opt)
	}

	opt, label = app.mediaModelOptions("")
	if label == "" || opt.ModelName == "" {
		t.Errorf("empty model should fall back to the configured default, got %q %+v", label, opt)
	}
}

func TestFetchMediaAsBase64RefusesForeignHosts(t *testing.T) {
	app := mediaTestApp(t, nil)

	if _, err := app.fetchMediaAsBase64("https://evil.example.com/pic.png"); err == nil {
		t.Fatal("non-Google asset hosts must not be fetched")
	}
	if _, err := app.fetchMediaAsBase64("not a url"); err == nil {
		t.Fatal("unparseable URLs must not be fetched")
	}
}
