package server

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func requestWithTools(names ...string) models.OpenAIChatRequest {
	tools := make([]models.OpenAITool, 0, len(names))
	for _, name := range names {
		tools = append(tools, toolNamed(name))
	}
	return models.OpenAIChatRequest{Tools: tools}
}

// fakePNGB64 is a PNG-signature payload big enough to pass the media size floor.
func fakePNGB64() string {
	buf := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	for len(buf) < 2048 {
		buf = append(buf, 0)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// mediaChatApp builds an app on the Enterprise direct path with a fake upstream
// that answers with text plus one inline image.
func mediaChatApp(t *testing.T, answerText string) (*App, *[]string) {
	t.Helper()
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")
	if err := os.WriteFile(cookieFile, []byte(`{"cookie":"SAPISID=abc","sapisid":"abc"}`), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	var mu sync.Mutex
	var prompts []string
	png := fakePNGB64()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		mu.Lock()
		prompts = append(prompts, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{` +
			`"groundedContent":{"content":{"text":"` + answerText + `"}},` +
			`"inlineData":{"mimeType":"image/png","data":"` + png + `"}}]}}}`))
	}))
	t.Cleanup(srv.Close)

	cfg := config.Default()
	cfg.LogRequests = false
	cfg.CookieFile = cookieFile
	cfg.RetryAttempts = 1
	cfg.RequestTimeoutSec = 5
	cfg.DiscoveryEngineConfigID = "cfg-1"
	cfg.DiscoveryEngineEndpoint = srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k"
	cfg.DiscoveryEngineProbeModels = false

	app := New(cfg, "test")
	app.Logf = func(string, ...any) {}
	app.Gem.HTTP = srv.Client()
	app.Gem.MarkBardUnsupported()
	return app, &prompts
}

// The descriptions double as markers: whatever reaches the model shows up in the
// prompt, so the test can prove exactly which definitions survived.
const mediaToolRequest = `{"model":"gemini-4-hydrogen","messages":[{"role":"user","content":"draw a red cube"}],` +
	`"tools":[{"type":"function","function":{"name":"generate_image","description":"CLIENT_IMAGE_TOOL_MARKER","parameters":{}}},` +
	`{"type":"function","function":{"name":"read_file","description":"READ_TOOL_MARKER","parameters":{}}}]}`

// The core of the report: a client-side image tool must never be offered to the
// model, and the image Gemini renders must reach the client.
func TestChatStripsClientMediaToolsAndReturnsGeminiMedia(t *testing.T) {
	app, prompts := mediaChatApp(t, "Here you go")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(mediaToolRequest))
	req.Header.Set("Content-Type", "application/json")
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Media-Tools-Intercepted"); got != "generate_image" {
		t.Errorf("X-Media-Tools-Intercepted = %q, want generate_image", got)
	}

	// The prompt must not describe the client's image tool, and must tell Gemini
	// to render the media itself.
	if len(*prompts) == 0 {
		t.Fatal("no upstream request was sent")
	}
	upstream := (*prompts)[len(*prompts)-1]
	if strings.Contains(upstream, "CLIENT_IMAGE_TOOL_MARKER") {
		t.Errorf("the client media tool definition leaked into the upstream request: %s", truncateForTest(upstream))
	}
	if !strings.Contains(upstream, "handled by you, not by the client") {
		t.Errorf("the media instruction is missing from the upstream prompt")
	}
	// Ordinary tools stay available.
	if !strings.Contains(upstream, "READ_TOOL_MARKER") {
		t.Errorf("non-media tools must survive the interception")
	}

	// The reply carries the image Gemini produced.
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices in response: %s", rec.Body.String())
	}
	content := resp.Choices[0].Message.Content
	if !strings.Contains(content, "![generated image](data:image/png;base64,") {
		t.Errorf("the generated image was not returned to the client: %s", truncateForTest(content))
	}
	if !strings.Contains(content, "Here you go") {
		t.Errorf("the model text was lost: %s", truncateForTest(content))
	}
}

func TestChatKeepsToolsWhenNoMediaToolIsOffered(t *testing.T) {
	app, prompts := mediaChatApp(t, "plain answer")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"gemini-4-hydrogen","messages":[{"role":"user","content":"hi"}],`+
			`"tools":[{"type":"function","function":{"name":"read_file","parameters":{}}}]}`))
	req.Header.Set("Content-Type", "application/json")
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Media-Tools-Intercepted"); got != "" {
		t.Errorf("no media tool was offered, yet header says %q", got)
	}
	upstream := (*prompts)[len(*prompts)-1]
	if strings.Contains(upstream, "handled by you, not by the client") {
		t.Error("the media instruction must not appear for a non-media request")
	}
	if rec.Body.String() != "" && strings.Contains(rec.Body.String(), "![generated image]") {
		t.Error("media must not be appended when the payload has none")
	}
}

func TestInterceptClientMediaToolsRewritesForcedToolChoice(t *testing.T) {
	req := requestWithTools("generate_image", "read_file")
	req.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": "generate_image"}}

	got, removed := interceptClientMediaTools(req)
	if len(removed) != 1 || removed[0] != "generate_image" {
		t.Fatalf("removed = %v", removed)
	}
	if got.ToolChoice != "auto" {
		t.Errorf("a forced tool_choice on a removed tool must become auto, got %v", got.ToolChoice)
	}

	// Only media tools present + required → nothing can be called.
	only := requestWithTools("create_video")
	only.ToolChoice = "required"
	got, _ = interceptClientMediaTools(only)
	if got.ToolChoice != "none" {
		t.Errorf("required with no tools left must become none, got %v", got.ToolChoice)
	}
	if len(got.Tools) != 0 {
		t.Errorf("all media tools should be removed, left %d", len(got.Tools))
	}
}

func truncateForTest(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// WorkBuddy's agent surface speaks the Responses API, and its image tool is
// named ImageGen. If only /v1/chat/completions were covered, that endpoint would
// keep handing the tool to the model — exactly the reported behaviour.
func TestResponsesStripsClientMediaTools(t *testing.T) {
	app, prompts := mediaChatApp(t, "Here you go")

	body := `{"model":"gemini-4-hydrogen","input":[{"role":"user","content":"draw a red cube"}],` +
		`"tools":[{"type":"function","name":"ImageGen","description":"CLIENT_IMAGE_TOOL_MARKER","parameters":{}},` +
		`{"type":"function","name":"read_file","description":"READ_TOOL_MARKER","parameters":{}}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Media-Tools-Intercepted"); got != "ImageGen" {
		t.Errorf("X-Media-Tools-Intercepted = %q, want ImageGen", got)
	}
	upstream := (*prompts)[len(*prompts)-1]
	if strings.Contains(upstream, "CLIENT_IMAGE_TOOL_MARKER") {
		t.Errorf("ImageGen definition reached the model: %s", truncateForTest(upstream))
	}
	if !strings.Contains(upstream, "READ_TOOL_MARKER") {
		t.Error("read_file must survive the interception")
	}
	if !strings.Contains(upstream, "handled by you, not by the client") {
		t.Error("the media instruction is missing from the Responses prompt")
	}
	if !strings.Contains(rec.Body.String(), "![generated image](data:image/png;base64,") {
		t.Errorf("the generated image was not returned: %s", truncateForTest(rec.Body.String()))
	}
}

func TestGoogleGenerateStripsClientMediaTools(t *testing.T) {
	app, prompts := mediaChatApp(t, "Here you go")

	body := `{"contents":[{"role":"user","parts":[{"text":"draw a red cube"}]}],` +
		`"tools":[{"functionDeclarations":[{"name":"ImageGen","description":"CLIENT_IMAGE_TOOL_MARKER"},` +
		`{"name":"read_file","description":"READ_TOOL_MARKER"}]}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-4-hydrogen:generateContent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	app.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Media-Tools-Intercepted"); got != "ImageGen" {
		t.Errorf("X-Media-Tools-Intercepted = %q, want ImageGen", got)
	}
	upstream := (*prompts)[len(*prompts)-1]
	if strings.Contains(upstream, "CLIENT_IMAGE_TOOL_MARKER") {
		t.Errorf("ImageGen declaration reached the model: %s", truncateForTest(upstream))
	}
	if !strings.Contains(upstream, "READ_TOOL_MARKER") {
		t.Error("read_file must survive the interception")
	}
}

func TestStripGoogleMediaToolsDropsEmptyToolEntry(t *testing.T) {
	req := models.GoogleGenerateRequest{Tools: []models.GoogleTool{
		{FunctionDeclarations: []models.GoogleFunctionDeclaration{{Name: "ImageGen"}}},
		{FunctionDeclarations: []models.GoogleFunctionDeclaration{{Name: "read_file"}}},
	}}
	removed := stripGoogleMediaTools(&req)
	if len(removed) != 1 || removed[0] != "ImageGen" {
		t.Fatalf("removed = %v", removed)
	}
	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("a tool entry holding only media declarations must be dropped: %+v", req.Tools)
	}
	if req.Tools[0].FunctionDeclarations[0].Name != "read_file" {
		t.Errorf("wrong declaration survived: %+v", req.Tools[0].FunctionDeclarations)
	}

	// No media tools: nothing is touched.
	plain := models.GoogleGenerateRequest{Tools: []models.GoogleTool{
		{FunctionDeclarations: []models.GoogleFunctionDeclaration{{Name: "read_file"}}},
	}}
	if removed := stripGoogleMediaTools(&plain); len(removed) != 0 || len(plain.Tools) != 1 {
		t.Fatalf("plain tools were modified: %v %+v", removed, plain.Tools)
	}
}
