package server

import (
	"fmt"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// resolveRequestModel resolves the model requested by a client and, when the
// request declares callable tools, optionally redirects it to cfg.ToolModel.
//
// Some Gemini 4 models (notably Argon) prefer Gemini's built-in Deep Research /
// web tools and can ignore the injected "# Tool Use" prompt, while Hydrogen
// reliably emits the ```tool_call``` blocks an agent such as WorkBuddy needs.
// Setting "tool_model": "gemini-4-hydrogen" makes every tool-enabled request
// use that model without forcing the client to change its configured model id.
// Plain chat requests are unaffected.
//
// Per-request directives (@new / @continue / @think=N) from the originally
// requested model are carried over to the replacement so chat-session and
// thinking-depth behaviour is preserved.
func (a *App) resolveRequestModel(requested string, hasTools bool) (models.Resolved, bool, error) {
	resolved, err := models.Resolve(requested, a.Cfg.DefaultModel)
	if err != nil {
		return models.Resolved{}, false, err
	}

	target := strings.TrimSpace(a.Cfg.ToolModel)
	if !hasTools || target == "" {
		return resolved, false, nil
	}

	// Carry over @new/@continue/@think=N unless the override already defines
	// the same directive (an explicit tool_model setting wins).
	target = mergeModelDirectives(target, requested)

	redirected, err := models.Resolve(target, a.Cfg.DefaultModel)
	if err != nil || redirected.Fallback {
		return models.Resolved{}, false, fmt.Errorf("invalid tool_model %q (unknown model)", target)
	}
	if redirected.Name == resolved.Name && redirected.Think == resolved.Think && redirected.ChatMode == resolved.ChatMode {
		return resolved, false, nil
	}

	if a.Logf != nil {
		a.Logf("Tool model override: %q routed to %q for tool-enabled request", requested, redirected.Name)
	}
	return redirected, true, nil
}

// mergeModelDirectives appends request directives (@new, @continue, @think=N)
// to an override model name without duplicating ones the override already has.
func mergeModelDirectives(target, requested string) string {
	i := strings.Index(requested, "@")
	if i < 0 {
		return target
	}

	parts := strings.Split(target, "@")
	out := parts[0]
	seen := make(map[string]bool, len(parts))
	for _, directive := range parts[1:] {
		if directive == "" {
			continue
		}
		out += "@" + directive
		seen[directiveKey(directive)] = true
	}
	for _, directive := range strings.Split(requested[i+1:], "@") {
		key := directiveKey(directive)
		// Ignore unrecognized segments: the override target must stay a known
		// model, so garbage suffixes are dropped instead of propagated.
		if key == "" || seen[key] {
			continue
		}
		out += "@" + directive
		seen[key] = true
	}
	return out
}

// directiveKey classifies a model suffix directive. It returns "" for segments
// that models.Resolve does not understand.
func directiveKey(directive string) string {
	d := strings.ToLower(strings.TrimSpace(directive))
	switch {
	case strings.HasPrefix(d, "think="):
		return "think"
	case d == "new" || d == "newchat" || d == "fresh" || d == "continue" || d == "cont":
		return "session"
	default:
		return ""
	}
}

// applyToolPolicy enforces the "tools, not research" contract for premium,
// research-prone models (Neon / Hydrogen / Carbon / Argon).
//
// Two things happen when the policy is active and the request really declares
// tools:
//   - upstream research/browsing tools (google_search, web_search_preview,
//     deep_research, url_context, …) are dropped, because they are served by
//     Gemini itself and drag the turn into Deep Research, which cannot call the
//     client's tools, connectors or MCP servers;
//   - req.ResearchGuard is set, which folds the execution-mode lines into the
//     "# Tool Use" preamble the model reads before choosing a tool.
//
// It reports whether the guard was applied.
func (a *App) applyToolPolicy(req *models.OpenAIChatRequest, resolved models.Resolved) bool {
	if !a.Cfg.PreventDeepResearch || !resolved.ResearchProne || len(req.Tools) == 0 {
		return false
	}

	kept, removed := format.StripBuiltinResearchTools(req.Tools)
	if len(removed) > 0 {
		req.Tools = kept
		if a.Logf != nil {
			a.Logf("Deep Research disabled for %q: dropped built-in research tool(s) %s — a premium model cannot execute them and they pull the turn away from the client's tools",
				resolved.Name, strings.Join(removed, ", "))
		}
	}
	if len(req.Tools) == 0 {
		return false
	}

	req.ResearchGuard = true
	if a.Logf != nil {
		msg := fmt.Sprintf("Execution-mode guard applied for premium model %q: Deep Research / built-in browsing disabled, %d client tool(s) enforced",
			resolved.Name, len(req.Tools))
		if strings.TrimSpace(a.Cfg.ToolModel) == "" {
			msg += " (tip: set \"tool_model\" if the model still ignores your tools)"
		}
		a.Logf("%s", msg)
	}
	return true
}

// directCanHonourModel reports whether direct mode can actually send the model the
// client asked for.
//
// On a Discovery Engine origin the only lever is assistGenerationConfig.modelId.
// When no id is known for the requested alias, the request carries nothing and the
// tenant answers with its auto/default model — the "I picked Hydrogen but it says
// Auto" complaint. The browser bridge can select that model in the tab's picker,
// so routing must respect this before preferring direct mode.
func (a *App) directCanHonourModel(resolved models.Resolved) bool {
	if a.Gem == nil || !a.Gem.UsesDiscoveryEngine() {
		return true
	}
	_, source := a.Gem.EnterpriseModelIDSource(resolved.Name)
	return source != gemini.ModelIDSourceTenantDefault
}

// warnModelDropped logs once when a request is about to be answered by the
// tenant's auto model because neither direct mode has a model id nor a bridge tab
// is available to select it.
func (a *App) warnModelDropped(resolved models.Resolved) {
	if a.directCanHonourModel(resolved) {
		return
	}
	key := "model-dropped:" + resolved.Name
	if _, seen := modelWarnSeen.LoadOrStore(key, true); seen {
		return
	}
	if a.Logf != nil {
		a.Logf("MODEL SELECTION WARNING: no tenant model id for %q and no usable browser tab, so this turn is answered by the tenant's auto/default model. "+
			"Wake the Gemini Enterprise tab (the picker is the only way to select it) or map the alias in discovery_engine_models.", resolved.Name)
	}
}
