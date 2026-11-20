package format

import (
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func toolNamed(name string) models.OpenAITool {
	return models.OpenAITool{Type: "function", Function: models.OpenAIFunction{Name: name}}
}

func TestIsMediaGenerationTool(t *testing.T) {
	media := []string{
		"generate_image", "generateImage", "image_generation", "image-gen",
		"create_image", "text_to_image", "nano_banana", "imagen_3", "ImageGen", "image_gen",
		"generate_video", "createVideo", "video_generation", "text_to_video", "veo_3",
	}
	for _, name := range media {
		if !IsMediaGenerationTool(name) {
			t.Errorf("%q should be recognised as a media generator", name)
		}
	}

	ordinary := []string{
		"read_file", "write_file", "bash", "image_ocr", "read_image", "resize_image",
		"web_search", "ToolSearch", "DeferExecuteTool", "mcp__github__create_pr",
		"video_transcript", "get_video_metadata", "",
	}
	for _, name := range ordinary {
		if IsMediaGenerationTool(name) {
			t.Errorf("%q must stay available — it is not a media generator", name)
		}
	}
}

func TestSplitMediaGenerationTools(t *testing.T) {
	tools := []models.OpenAITool{
		toolNamed("read_file"),
		toolNamed("generate_image"),
		toolNamed("bash"),
		toolNamed("create_video"),
	}
	kept, removed := SplitMediaGenerationTools(tools)
	if len(removed) != 2 || removed[0] != "generate_image" || removed[1] != "create_video" {
		t.Fatalf("removed = %v", removed)
	}
	if len(kept) != 2 || kept[0].Function.Name != "read_file" || kept[1].Function.Name != "bash" {
		t.Fatalf("kept = %+v", kept)
	}

	// Nothing to remove: the original slice is returned untouched.
	plain := []models.OpenAITool{toolNamed("read_file")}
	if kept, removed := SplitMediaGenerationTools(plain); len(removed) != 0 || len(kept) != 1 {
		t.Fatalf("plain tools were modified: %v %v", kept, removed)
	}
}

func TestToolChoiceName(t *testing.T) {
	cases := map[string]any{
		"generate_image": "generate_image",
		"auto":           "",
		"none":           "",
		"required":       "",
		"":               "",
	}
	for input, want := range cases {
		if got := ToolChoiceName(input); got != want {
			t.Errorf("ToolChoiceName(%q) = %q, want %q", input, got, want)
		}
	}

	object := map[string]any{"type": "function", "function": map[string]any{"name": "generate_video"}}
	if got := ToolChoiceName(object); got != "generate_video" {
		t.Errorf("object tool_choice name = %q", got)
	}
	if got := ToolChoiceName(map[string]any{"name": "nano_banana"}); got != "nano_banana" {
		t.Errorf("flat tool_choice name = %q", got)
	}
	if got := ToolChoiceName(42); got != "" {
		t.Errorf("unexpected tool_choice type should be empty, got %q", got)
	}
}

func TestMediaGenerationInstruction(t *testing.T) {
	instruction := MediaGenerationInstruction([]string{"generate_image", "create_video"})
	for _, want := range []string{"generate_image", "create_video", "do NOT emit a tool_call", "built-in image/video generation tool", "proxy forwards"} {
		if !strings.Contains(instruction, want) {
			t.Errorf("instruction should contain %q: %s", want, instruction)
		}
	}
}
