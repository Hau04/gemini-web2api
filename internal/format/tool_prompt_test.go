package format

import (
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func TestToolPromptsDiscourageGeminiBuiltinTools(t *testing.T) {
	openAIReq := models.OpenAIChatRequest{
		Messages: []models.OpenAIMessage{{Role: "user", Content: "check my local repo with gh"}},
		Tools: []models.OpenAITool{{
			Function: models.OpenAIFunction{Name: "Bash", Description: "Run a shell command"},
		}},
		ToolChoice: "auto",
	}
	prompt, _, err := MessagesToPrompt(openAIReq)
	if err != nil {
		t.Fatalf("MessagesToPrompt() error = %v", err)
	}
	if !strings.Contains(prompt, "Do NOT use Gemini's built-in web search, Deep Research") {
		t.Error("OpenAI tool prompt missing built-in-tool warning")
	}
	if !strings.Contains(prompt, "```tool_call") || !strings.Contains(prompt, `"name"`) {
		t.Error("OpenAI tool prompt missing tool_call format")
	}

	googlePrompt := BuildToolPrompt([]models.GoogleFunctionDeclaration{{Name: "Bash", Description: "Run a shell command"}})
	if !strings.Contains(googlePrompt, "Do NOT use Gemini's built-in web search, Deep Research") {
		t.Error("Google tool prompt missing built-in-tool warning")
	}
}
