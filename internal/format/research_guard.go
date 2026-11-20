package format

import (
	"fmt"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// builtinResearchTools are the names clients use for Gemini's/OpenAI's own
// research and browsing tools. They are served by the upstream provider, not by
// the local client, so on a premium model they pull the answer into Deep
// Research — a mode that reads the public web only and cannot invoke any of the
// caller's tools, connectors or MCP servers.
var builtinResearchTools = map[string]bool{
	"google_search":           true,
	"google_search_retrieval": true,
	"googlesearch":            true,
	"google_search_tool":      true,
	"web_search":              true,
	"web_search_preview":      true,
	"web_search_tool":         true,
	"websearch":               true,
	"search_web":              true,
	"web_fetch":               true,
	"webfetch":                true,
	"url_context":             true,
	"browse":                  true,
	"browser":                 true,
	"web_browse":              true,
	"deep_research":           true,
	"deepresearch":            true,
	"deep_research_tool":      true,
	"research":                true,
}

// normalizeToolName lowercases a tool name and folds '-' into '_' so that
// "Web-Search" and "web_search" compare equal.
func normalizeToolName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, "-", "_")
	return n
}

// IsBuiltinResearchTool reports whether a declared tool is an upstream research
// or browsing tool rather than something the connected client can execute.
//
// Namespaced names are never treated as built-ins: "mcp__browser__web_search" is
// a Playwright/MCP tool that the client really runs, so it must survive the strip.
func IsBuiltinResearchTool(name string) bool {
	n := normalizeToolName(name)
	if n == "" {
		return false
	}
	if strings.Contains(n, "__") || strings.Contains(n, ".") || strings.Contains(n, ":") || strings.Contains(n, "/") {
		return false
	}
	return builtinResearchTools[n]
}

// StripBuiltinResearchTools removes upstream research/browsing tools from a
// declared tool list, returning the surviving tools and the removed names.
func StripBuiltinResearchTools(tools []models.OpenAITool) ([]models.OpenAITool, []string) {
	if len(tools) == 0 {
		return tools, nil
	}
	kept := make([]models.OpenAITool, 0, len(tools))
	var removed []string
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			name = strings.TrimSpace(tool.Name)
		}
		if IsBuiltinResearchTool(name) {
			removed = append(removed, name)
			continue
		}
		kept = append(kept, tool)
	}
	return kept, removed
}

// ExecutionModeLines returns the compact "tools, not research" lines that are
// folded into the "# Tool Use" preamble for a premium, research-prone model.
//
// It lives inside the tool block on purpose: that block is what the spillover
// compactor keeps inline, so the guard survives prompt compaction instead of
// competing with the tool instructions for the compacted budget.
func ExecutionModeLines(modelName string) string {
	name := strings.TrimSpace(modelName)
	if name == "" {
		name = "this premium model"
	}
	return fmt.Sprintf(
		"- EXECUTION MODE (%s): Deep Research, browsing-only agents and built-in web search are DISABLED — they read "+
			"the public web only and cannot run the declared tools, so a research answer is a failed answer. Facts about "+
			"the user's machine, repo, files, inbox or apps MUST come from a tool_call; if data is missing, emit the "+
			"tool_call that fetches it instead of researching or asking the user to paste it.\n",
		name,
	)
}
