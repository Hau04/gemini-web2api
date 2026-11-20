package format

import (
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// mediaToolHints are normalised name fragments that identify a client-side media
// generator (image or video).
//
// The point of recognising them: WorkBuddy ships its own image/video generation
// tools, and when the model emits a tool_call for one of those the *client*
// renders the media and bills its own credits — the signed-in Gemini account
// that can do it for free never gets the chance. Removing the tool makes Gemini
// render instead.
//
// Matching is deliberately narrow ("image" alone would catch read_image,
// image_ocr, image_resize …), against names normalised by dropping separators.
var mediaToolHints = []string{
	// image generation
	"generateimage",
	"imagegeneration",
	"imagegen",
	"imagengeneration",
	"createimage",
	"texttoimage",
	"t2i",
	"nanobanana",
	"imagen",
	// video generation
	"generatevideo",
	"videogeneration",
	"videogen",
	"createvideo",
	"texttovideo",
	"t2v",
	"veo",
}

// compactToolName lowercases a tool name and drops every non-alphanumeric byte,
// so generate_image, generateImage and Generate-Image all compare equal.
func compactToolName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsMediaGenerationTool reports whether a client tool name is an image or video
// generator.
func IsMediaGenerationTool(name string) bool {
	normalized := compactToolName(name)
	if normalized == "" {
		return false
	}
	for _, hint := range mediaToolHints {
		if strings.Contains(normalized, hint) {
			return true
		}
	}
	return false
}

// SplitMediaGenerationTools separates the client's media generators from the
// tools that stay available, returning the kept tools and the removed names in
// request order.
func SplitMediaGenerationTools(tools []models.OpenAITool) (kept []models.OpenAITool, removed []string) {
	if len(tools) == 0 {
		return tools, nil
	}
	kept = make([]models.OpenAITool, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			name = strings.TrimSpace(tool.Name)
		}
		if IsMediaGenerationTool(name) {
			removed = append(removed, name)
			continue
		}
		kept = append(kept, tool)
	}
	if len(removed) == 0 {
		return tools, nil
	}
	return kept, removed
}

// ToolChoiceName extracts the function name from an OpenAI tool_choice value
// (either {"type":"function","function":{"name":"x"}} or a bare name).
func ToolChoiceName(choice any) string {
	switch v := choice.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" || trimmed == "auto" || trimmed == "none" || trimmed == "required" {
			return ""
		}
		return trimmed
	case map[string]any:
		if fn, ok := v["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				return strings.TrimSpace(name)
			}
		}
		if name, ok := v["name"].(string); ok {
			return strings.TrimSpace(name)
		}
	}
	return ""
}

// MediaGenerationInstruction tells the model that the client's own media tools
// are gone for this request, so it must render the media itself instead of
// emitting a tool_call the client would bill for.
func MediaGenerationInstruction(removed []string) string {
	names := strings.Join(removed, ", ")
	return "\n\n# Image and video generation (handled by you, not by the client)\n\n" +
		"The client offered its own media tool(s) [" + names + "]. They are disabled for this request: " +
		"do NOT emit a tool_call for them, and do not ask the client to render anything.\n" +
		"If the user asks for an image or a video, create it yourself with your built-in image/video generation tool " +
		"and return it inline. The proxy forwards the file to the client, so a description, an alternative prompt " +
		"or an apology is not an acceptable substitute.\n" +
		"If an earlier turn listed or ran one of those tools, ignore that: they are unavailable now, and offering them " +
		"back to the user is wrong. Never answer that no image tool exists before trying your own generation.\n" +
		"Everything else in this conversation (code, shell, files, connectors) still runs through the client's tools as usual."
}
