package gemini

import (
	"encoding/json"
	"strings"
)

// DefaultEnterpriseToolsSpec is what a captured Gemini Enterprise widget body
// carries. It is the only tools value the proxy can be sure about, because
// StreamAssistRequest.ToolsSpec has no registry field at all (it holds the Vertex
// AI Search, web-grounding, image and video specs) — the tenant's own assistant
// carries its tools, including the actions imported from an MCP connector.
const DefaultEnterpriseToolsSpec = `{"toolRegistry":"default_tool_registry"}`

// enterpriseToolsSpec decides which toolsSpec the direct streamAssist call sends:
//
//  1. discovery_engine_tools_spec, when set — paste exactly what the real Gemini
//     Enterprise app sends once its MCP actions are enabled;
//  2. the toolsSpec baked into the captured request template, if there is one —
//     that body came from the app itself, so it is the app's own tool config;
//  3. the captured default otherwise.
func (c *Client) enterpriseToolsSpec() (spec, source string) {
	if configured := strings.TrimSpace(c.Cfg.DiscoveryEngineToolsSpec); configured != "" {
		// "none" sends no toolsSpec at all, leaving the assistant's own
		// configuration in charge. That is the A/B test for "does pinning a tool
		// registry hide the tenant's MCP actions?".
		if strings.EqualFold(configured, "none") || configured == "-" {
			return "", "omitted"
		}
		if !json.Valid([]byte(configured)) {
			c.Logf("Enterprise: discovery_engine_tools_spec is not valid JSON (%q) — ignoring it and using the captured default", configured)
		} else {
			return configured, "config"
		}
	}
	if spec, ok := c.templateToolsSpec(); ok {
		return spec, "captured template"
	}
	return DefaultEnterpriseToolsSpec, "default"
}

// templateToolsSpec reads the toolsSpec out of the captured replay template.
func (c *Client) templateToolsSpec() (string, bool) {
	tpl := LoadDiscoveryTemplate(c.Cfg.CookieFile)
	if tpl == nil || strings.TrimSpace(tpl.Body) == "" {
		return "", false
	}
	return extractToolsSpec(tpl.Body)
}

// extractToolsSpec finds the toolsSpec object inside a widget or REST
// streamAssist body and returns it as JSON.
func extractToolsSpec(body string) (string, bool) {
	var payload any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", false
	}
	request := findAssistRequest(payload)
	if request == nil {
		return "", false
	}
	raw, ok := request["toolsSpec"]
	if !ok {
		raw, ok = request["tools_spec"]
	}
	if !ok {
		return "", false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "", false
	}
	spec := strings.TrimSpace(string(encoded))
	if spec == "" || spec == "null" || spec == "{}" {
		return "", false
	}
	return spec, true
}

// logToolsSpec reports which tool configuration a turn is asking the tenant for.
// It is how "did my MCP actions even get asked for?" becomes a log line.
func (c *Client) logToolsSpec(spec, source string) {
	if spec == "" {
		c.Logf("Enterprise: no toolsSpec sent (discovery_engine_tools_spec=none) — the assistant's own configured actions decide")
		return
	}
	shown := spec
	if len(shown) > 300 {
		shown = shown[:300] + "…"
	}
	c.Logf("Enterprise: toolsSpec from %s: %s", source, shown)
}
