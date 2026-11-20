package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	modelNames := make([]string, 0, len(models.MODELS))
	for k := range models.MODELS {
		modelNames = append(modelNames, k)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"features": map[string]any{
			"chat_completions":      true,
			"responses_api":         true,
			"google_generate":       true,
			"tools":                 true,
			"connectors":            true,
			"mcp":                   true,
			"vision":                true,
			"attachments":           a.Gem != nil && a.Gem.CanUploadEnterpriseFiles(),
			"parallel_tool_calls":   true,
			"prevent_deep_research": a.Cfg.PreventDeepResearch,
			"model_probe":           true,
		},
		"status":  "ok",
		"version": a.Version,
		"models":  modelNames,
	})
}

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	data := make([]map[string]any, 0, len(models.MODELS))
	for name, m := range models.MODELS {
		data = append(data, modelDescriptor(name, m))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

// handleModelGet serves GET /v1/models/{id}. Agent runtimes (WorkBuddy, MCP
// clients, the OpenAI SDKs) probe it to validate a configured model id and read
// its capabilities before enabling tools or vision.
func (a *App) handleModelGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "model id is required"}})
		return
	}
	for name, m := range models.MODELS {
		if strings.EqualFold(name, id) {
			writeJSON(w, http.StatusOK, modelDescriptor(name, m))
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"message": fmt.Sprintf("model %q not found; GET /v1/models lists the available ids", id),
			"type":    "invalid_request_error",
			"code":    "model_not_found",
		},
	})
}

// modelDescriptor renders one model entry for the OpenAI-compatible listing.
func modelDescriptor(name string, m models.Model) map[string]any {
	return map[string]any{
		"id":                name,
		"object":            "model",
		"created":           1700000000,
		"owned_by":          "google",
		"description":       m.Desc,
		"context_window":    contextWindowFor(name),
		"max_output_tokens": 65536,
		"premium":           m.ResearchProne,
		"research_prone":    m.ResearchProne,
		"capabilities": map[string]any{
			// Text/tool plumbing every model here supports (tool calling runs
			// through the client bridge, so connectors and MCP tools work too).
			"streaming":   true,
			"tools":       true,
			"connectors":  true,
			"mcp":         true,
			"vision":      true,
			"attachments": true,
			"json_mode":   true,
		},
	}
}

// contextWindowFor reports the context window advertised for a model id. The
// upstream does not publish exact numbers per alias, so family defaults are used.
func contextWindowFor(name string) int {
	switch {
	case strings.Contains(name, "2.5-pro"):
		return 2097152
	case strings.Contains(name, "flash-lite"):
		return 1048576
	default:
		return 1048576
	}
}

func (a *App) handleGoogleModels(w http.ResponseWriter, r *http.Request) {
	var modelList []map[string]any
	for name, m := range models.MODELS {
		modelList = append(modelList, map[string]any{
			"name":                       "models/" + name,
			"displayName":                name,
			"description":                m.Desc,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models": modelList,
	})
}
