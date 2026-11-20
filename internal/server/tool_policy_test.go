package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func toolNamed(name string) models.OpenAITool {
	return models.OpenAITool{Type: "function", Function: models.OpenAIFunction{Name: name, Parameters: map[string]any{}}}
}

func TestResolveNeonModel(t *testing.T) {
	for _, name := range []string{"gemini-4-neon", "neon", "gemini-4-neon-thinking"} {
		resolved, err := models.Resolve(name, "")
		if err != nil {
			t.Fatalf("Resolve(%q): %v", name, err)
		}
		if resolved.Fallback {
			t.Errorf("Resolve(%q) fell back to %q — Neon must be a first-class model", name, resolved.Name)
		}
		if !resolved.ResearchProne {
			t.Errorf("Resolve(%q) should be flagged research-prone", name)
		}
	}
}

// A tool-enabled request to a premium model must not be able to drift into Deep
// Research: upstream research tools are dropped and the guard is armed.
func TestApplyToolPolicyStripsResearchToolsForPremium(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	req := models.OpenAIChatRequest{Tools: []models.OpenAITool{
		toolNamed("web_search"),
		toolNamed("deep_research"),
		toolNamed("google-search"),
		toolNamed("mcp__browser__web_search"),
		toolNamed("mcp__github__create_issue"),
		toolNamed("edit_file"),
	}}
	resolved, err := models.Resolve("gemini-4-neon", "")
	if err != nil {
		t.Fatal(err)
	}

	if !app.applyToolPolicy(&req, resolved) {
		t.Fatal("guard should be armed for a premium model with tools")
	}
	if !req.ResearchGuard {
		t.Error("req.ResearchGuard should be set so the tool preamble carries the guard")
	}
	var names []string
	for _, tool := range req.Tools {
		names = append(names, tool.Function.Name)
	}
	joined := strings.Join(names, ",")
	for _, gone := range []string{"web_search", "deep_research", "google-search"} {
		if strings.Contains(joined, gone) && !strings.Contains(joined, "mcp__browser__"+gone) {
			t.Errorf("research tool %q should have been dropped, kept %v", gone, names)
		}
	}
	// MCP-namespaced tools are executed by the client, not by Gemini.
	for _, keep := range []string{"mcp__browser__web_search", "mcp__github__create_issue", "edit_file"} {
		found := false
		for _, n := range names {
			if n == keep {
				found = true
			}
		}
		if !found {
			t.Errorf("client tool %q was dropped, kept %v", keep, names)
		}
	}
}

// Plain chat, non-premium models and disabled policy must all stay untouched.
func TestApplyToolPolicySkipsWhenNotApplicable(t *testing.T) {
	flash, _ := models.Resolve("gemini-3.8-flash", "")
	premium, _ := models.Resolve("gemini-4-hydrogen", "")

	app := newEnterpriseApp(t, nil)
	plain := models.OpenAIChatRequest{}
	if app.applyToolPolicy(&plain, premium) {
		t.Error("a request without tools must not be guarded")
	}

	flashReq := models.OpenAIChatRequest{Tools: []models.OpenAITool{toolNamed("web_search"), toolNamed("edit_file")}}
	if app.applyToolPolicy(&flashReq, flash) {
		t.Error("non-premium models must not be guarded")
	}
	if len(flashReq.Tools) != 2 {
		t.Errorf("non-premium tools must be untouched, got %d", len(flashReq.Tools))
	}

	// The guard disarms itself when it would leave nothing to call.
	researchOnly := models.OpenAIChatRequest{Tools: []models.OpenAITool{toolNamed("web_search")}}
	if app.applyToolPolicy(&researchOnly, premium) {
		t.Error("guard should not arm when the request only declared research tools")
	}

	off := newEnterpriseApp(t, func(cfg *config.Config) { cfg.PreventDeepResearch = false })
	kept := models.OpenAIChatRequest{Tools: []models.OpenAITool{toolNamed("web_search"), toolNamed("edit_file")}}
	if off.applyToolPolicy(&kept, premium) {
		t.Error("prevent_deep_research=false must disable the policy")
	}
	if len(kept.Tools) != 2 || kept.ResearchGuard {
		t.Errorf("policy off: tools=%d guard=%v", len(kept.Tools), kept.ResearchGuard)
	}
}

// The guard text must be inside the "# Tool Use" block (that is what the
// spillover compactor keeps inline) and must forbid research explicitly.
func TestMessagesToPromptFoldsGuardIntoPreamble(t *testing.T) {
	req := models.OpenAIChatRequest{
		Messages:      []models.OpenAIMessage{{Role: "user", Content: "fix the failing test"}},
		Tools:         []models.OpenAITool{toolNamed("edit_file")},
		ToolChoice:    "auto",
		ResearchGuard: true,
	}
	prompt, _, err := format.MessagesToPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	toolBlock := prompt[:strings.Index(prompt, "Available tools:")]
	for _, want := range []string{"EXECUTION MODE", "Deep Research", "DISABLED", "MUST come from a tool_call"} {
		if !strings.Contains(toolBlock, want) {
			t.Errorf("tool preamble should mention %q:\n%s", want, toolBlock)
		}
	}

	req.ResearchGuard = false
	plain, _, _ := format.MessagesToPrompt(req)
	if strings.Contains(plain, "EXECUTION MODE") {
		t.Error("guard must only appear when requested")
	}
}

func TestMessagesToPromptDedupesAndSkipsNamelessTools(t *testing.T) {
	req := models.OpenAIChatRequest{
		Messages: []models.OpenAIMessage{{Role: "user", Content: "hi"}},
		Tools: []models.OpenAITool{
			toolNamed("edit_file"),
			toolNamed("edit_file"),
			{Type: "web_search_preview"},
		},
	}
	prompt, _, err := format.MessagesToPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(prompt, `"name":"edit_file"`); n != 1 {
		t.Errorf("duplicate tool declaration should collapse to one, got %d:\n%s", n, prompt)
	}
	if strings.Contains(prompt, `"name":""`) {
		t.Error("nameless built-in tool declaration leaked into the prompt")
	}
}

func TestMessagesToPromptHonoursParallelToolCallsFalse(t *testing.T) {
	off := false
	req := models.OpenAIChatRequest{
		Messages:          []models.OpenAIMessage{{Role: "user", Content: "hi"}},
		Tools:             []models.OpenAITool{toolNamed("edit_file")},
		ParallelToolCalls: &off,
	}
	prompt, _, _ := format.MessagesToPrompt(req)
	if !strings.Contains(prompt, "exactly ONE tool_call block per turn") {
		t.Errorf("parallel_tool_calls=false should be enforced in the prompt:\n%s", prompt)
	}
}

func TestModelsEndpointExposesCompatMetadata(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/models = %d", rec.Code)
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var neon map[string]any
	for _, m := range list.Data {
		if m["id"] == "gemini-4-neon" {
			neon = m
		}
	}
	if neon == nil {
		t.Fatal("gemini-4-neon missing from /v1/models")
	}
	caps, _ := neon["capabilities"].(map[string]any)
	for _, cap := range []string{"tools", "vision", "attachments", "mcp", "connectors", "streaming"} {
		if caps[cap] != true {
			t.Errorf("capability %q should be advertised, got %v", cap, caps[cap])
		}
	}
	if neon["research_prone"] != true || neon["premium"] != true {
		t.Errorf("neon should be marked premium/research_prone: %v", neon)
	}
	if w, _ := neon["context_window"].(float64); w <= 0 {
		t.Errorf("context_window should be positive, got %v", neon["context_window"])
	}

	// Single-model lookup, as probed by agent runtimes.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/gemini-4-neon", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gemini-4-neon") {
		t.Errorf("GET /v1/models/gemini-4-neon = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown model should 404, got %d", rec.Code)
	}
}
