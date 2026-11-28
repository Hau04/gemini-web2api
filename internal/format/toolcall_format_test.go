package format

import (
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func toolPromptForTest(t *testing.T) string {
	t.Helper()
	req := models.OpenAIChatRequest{
		Model: "gemini-4-argon",
		Tools: []models.OpenAITool{{
			Type: "function",
			Function: models.OpenAIFunction{
				Name:        "read_file",
				Description: "Read a file",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}},
		Messages: []models.OpenAIMessage{{Role: "user", Content: "read main.go"}},
	}
	prompt, _, err := MessagesToPrompt(req)
	if err != nil {
		t.Fatalf("MessagesToPrompt: %v", err)
	}
	return prompt
}

// The client's own prompt requires narration before a tool call; ours used to
// forbid it, and the model resolved the conflict by writing its deliberation into
// the answer. One rule, not two.
func TestToolPromptAllowsOneLineBeforeTheBlock(t *testing.T) {
	prompt := toolPromptForTest(t)
	if strings.Contains(prompt, "Output ONLY the tool_call block(s)") {
		t.Error("the absolute \"output only\" rule contradicts clients that require narration")
	}
	if !strings.Contains(prompt, "must END with the tool_call block(s)") {
		t.Error("the block must be required to end the message")
	}
	if !strings.Contains(prompt, "A single short sentence before the block is fine") {
		t.Error("a one-line narration must be explicitly allowed")
	}
	if !strings.Contains(prompt, "never write your own planning") && !strings.Contains(prompt, "Never write your own planning") {
		t.Error("leaked deliberation must be forbidden")
	}
	if !strings.Contains(prompt, "```tool_call") {
		t.Error("the block format itself must stay in the prompt")
	}
}

// A draft block the model narrates itself into must not become a tool call by
// accident: only a block that carries the declared tool name runs.
func TestNarratedDraftBlockIsNotAToolCall(t *testing.T) {
	tools := []models.OpenAITool{{Type: "function", Function: models.OpenAIFunction{Name: "read_file"}}}
	text := "I will read the file first.\n\n```tool_call\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"main.go\"}}\n```\n"
	_, calls := ParseToolCallsWithContext(text, tools, nil)
	if len(calls) != 1 || calls[0].Function.Name != "read_file" {
		t.Fatalf("expected exactly one read_file call, got %+v", calls)
	}

	// Prose before the block is kept as content; the block itself is the call.
	draft := "I will read the file first.\n\n```tool_call\n{\"name\": \"read_file\", \"arguments\": {\"path\": \"a.go\"}}\n```\n"
	clean, calls := ParseToolCallsWithContext(draft, tools, nil)
	if len(calls) != 1 {
		t.Fatalf("expected the block to parse, got %+v", calls)
	}
	if !strings.Contains(clean, "I will read the file first.") {
		t.Errorf("the narration must survive as content, got %q", clean)
	}
	if strings.Contains(clean, "read_file") {
		t.Errorf("the block itself must not stay in the text, got %q", clean)
	}

	// An explicit block naming a tool this request never declared still passes
	// through (clients may proxy arbitrary tools) — the server warns about it.
	undeclared := "```tool_call\n{\"name\": \"not_a_declared_tool\"}\n```\n"
	_, calls = ParseToolCallsWithContext(undeclared, tools, nil)
	if len(calls) != 1 || calls[0].Function.Name != "not_a_declared_tool" {
		t.Errorf("explicit blocks pass through by design, got %+v", calls)
	}
}
