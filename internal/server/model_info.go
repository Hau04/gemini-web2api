package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// modelSelectionInfo explains, for one request, which model will actually answer
// and — the question behind "I selected Hydrogen but it says Auto" — where that
// decision was made.
type modelSelectionInfo struct {
	Requested string            `json:"requested"`
	Resolved  resolvedModel     `json:"resolved"`
	Origin    string            `json:"origin"`
	Upstream  upstreamModel     `json:"upstream"`
	Transport []string          `json:"transport"`
	Mappings  map[string]string `json:"discovery_engine_models,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
}

type resolvedModel struct {
	Name          string `json:"name"`
	Mode          int    `json:"mode"`
	Think         int    `json:"think"`
	Fallback      bool   `json:"fallback"`
	ResearchProne bool   `json:"research_prone"`
}

type upstreamModel struct {
	ModelID       string `json:"model_id"`
	Source        string `json:"source"`
	SelectionLive bool   `json:"selection_live"`
}

const (
	originEnterprise = "discovery_engine"
	originConsumer   = "consumer"
)

// modelWarnSeen keeps the model-selection warnings to one log line each, so a
// misconfigured mapping does not bury the rest of the log.
var modelWarnSeen sync.Map

// describeModelSelection builds the selection report for a resolved model.
func (a *App) describeModelSelection(requested string, resolved models.Resolved) modelSelectionInfo {
	info := modelSelectionInfo{
		Requested: strings.TrimSpace(requested),
		Resolved: resolvedModel{
			Name:          resolved.Name,
			Mode:          resolved.Mode,
			Think:         resolved.Think,
			Fallback:      resolved.Fallback,
			ResearchProne: resolved.ResearchProne,
		},
		Origin: originConsumer,
	}
	if a.Gem != nil && a.Gem.UsesDiscoveryEngine() {
		info.Origin = originEnterprise
	}

	if a.Bridge != nil && a.Bridge.IsConnected() {
		info.Transport = append(info.Transport, "bridge")
	}
	if a.directModeUsable() {
		info.Transport = append(info.Transport, "direct")
	}
	if len(info.Transport) == 0 {
		info.Transport = append(info.Transport, "none")
	}

	if info.Origin == originEnterprise && a.Gem != nil {
		id, source := a.Gem.EnterpriseModelIDSource(resolved.Name)
		info.Upstream = upstreamModel{
			ModelID: id,
			Source:  source,
			SelectionLive: source == gemini.ModelIDSourceMapping ||
				source == gemini.ModelIDSourcePassthrough ||
				source == gemini.ModelIDSourceCaptured,
		}
		if len(a.Cfg.DiscoveryEngineModels) > 0 {
			info.Mappings = make(map[string]string, len(a.Cfg.DiscoveryEngineModels))
			for k, v := range a.Cfg.DiscoveryEngineModels {
				info.Mappings[k] = v
			}
		}
		if source == gemini.ModelIDSourceTenantDefault {
			// No tenant id: direct mode cannot name the model, so a live bridge tab
			// is what makes selection possible at all.
			if a.Bridge != nil && a.Bridge.IsConnected() {
				info.Upstream = upstreamModel{ModelID: "(tab picker)", Source: "bridge-picker", SelectionLive: true}
				info.Warnings = append(info.Warnings, fmt.Sprintf(
					"direct mode has no tenant model id for %q, so requests are routed through the browser bridge and %q is selected in the tab's model picker — "+
						"the Gemini Enterprise tab must stay open and awake. Map the alias in discovery_engine_models to let direct mode honour it too.",
					resolved.Name, resolved.Name))
				return info
			}
			info.Warnings = append(info.Warnings, fmt.Sprintf(
				"no browser tab is connected and direct mode has no tenant model id for %q, so this request would be answered by the tenant's auto/default model — "+
					"open (or wake) the Gemini Enterprise tab, or map the alias in discovery_engine_models.", resolved.Name))
		}
		info.Warnings = append(info.Warnings, a.modelSelectionWarnings(resolved, id, source)...)
		return info
	}

	// Consumer origins: the alias itself is the routing decision. In bridge mode
	// the extension then clicks that entry in the tab's picker.
	info.Upstream = upstreamModel{ModelID: resolved.Name, Source: "alias", SelectionLive: true}
	return info
}

// modelSelectionWarnings renders the actionable diagnosis for a gap between the
// model the client asked for and the model the tenant will answer with.
func (a *App) modelSelectionWarnings(resolved models.Resolved, modelID, source string) []string {
	switch source {
	case gemini.ModelIDSourceTenantDefault:
		if len(a.Cfg.DiscoveryEngineModels) == 0 {
			return []string{fmt.Sprintf(
				"Gemini Enterprise model selection is INACTIVE: discovery_engine_models is empty, so every request is answered by the tenant's auto/default model and %q is ignored. "+
					"Send one message in the Gemini Enterprise tab with the extension enabled (its widgetStreamAssist request is captured and its modelId is reused automatically), "+
					"or copy the id from DevTools → assistGenerationConfig.modelId and map it, e.g. {\"gemini-4-hydrogen\": \"<tenant model id>\"}. "+
					"Until then the reply's own \"Auto Mode\" description is accurate.",
				resolved.Name)}
		}
		return []string{fmt.Sprintf(
			"%q is not in discovery_engine_models (configured keys: %s) and discovery_engine_default_model_id is empty, so the tenant answers with its auto/default model.",
			resolved.Name, strings.Join(a.mappingKeys(), ", "))}
	case gemini.ModelIDSourceCaptured:
		return []string{fmt.Sprintf(
			"%q has no mapping, so this request uses modelId %q learned from the tab's captured streamAssist request — a model id the tenant accepts, "+
				"but not necessarily the one you asked for. For exact control add {\"%s\": \"<tenant model id>\"} to discovery_engine_models.",
			resolved.Name, modelID, resolved.Name)}
	case gemini.ModelIDSourceConfigured:
		return []string{fmt.Sprintf(
			"%q is not in discovery_engine_models, so the request falls back to discovery_engine_default_model_id %q.",
			resolved.Name, modelID)}
	}
	return nil
}

func (a *App) mappingKeys() []string {
	keys := make([]string, 0, len(a.Cfg.DiscoveryEngineModels))
	for k := range a.Cfg.DiscoveryEngineModels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// reportModelSelection publishes the effective upstream model on the response and
// logs the diagnosis once, so a client never has to guess from the answer text.
func (a *App) reportModelSelection(w http.ResponseWriter, requested string, resolved models.Resolved) modelSelectionInfo {
	info := a.describeModelSelection(requested, resolved)
	if w != nil {
		if info.Upstream.ModelID != "" {
			w.Header().Set("X-Upstream-Model", info.Upstream.ModelID)
		} else {
			w.Header().Set("X-Upstream-Model", "tenant-default")
		}
		w.Header().Set("X-Upstream-Model-Source", info.Upstream.Source)
		if !info.Upstream.SelectionLive {
			w.Header().Set("X-Model-Selection", "ignored-by-tenant")
		} else {
			w.Header().Set("X-Model-Selection", "live")
		}
	}
	for _, warning := range info.Warnings {
		if _, seen := modelWarnSeen.LoadOrStore(warning, true); seen {
			continue
		}
		if a.Logf != nil {
			a.Logf("MODEL SELECTION WARNING: %s", warning)
		}
	}
	return info
}

// handleModelInfo serves GET /v1/model-info[?model=...]: the same diagnosis the
// response headers carry, without having to send a chat request first.
func (a *App) handleModelInfo(w http.ResponseWriter, r *http.Request) {
	requested := strings.TrimSpace(r.URL.Query().Get("model"))
	if requested == "" {
		requested = a.Cfg.DefaultModel
	}
	resolved, err := models.Resolve(requested, a.Cfg.DefaultModel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	info := a.describeModelSelection(requested, resolved)
	writeJSON(w, http.StatusOK, info)
}

// handleModelProbe serves POST /v1/model-probe {"model":"gemini-4-hydrogen",
// "candidate":"gemini-4-hydrogen"}: it asks the tenant whether that model id is
// accepted, and caches it for the alias when it is. This is how a tenant id is
// discovered without reading DevTools by hand.
func (a *App) handleModelProbe(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Model     string `json:"model"`
		Candidate string `json:"candidate"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	if payload.Model == "" {
		payload.Model = r.URL.Query().Get("model")
	}
	if payload.Candidate == "" {
		payload.Candidate = r.URL.Query().Get("candidate")
	}
	if payload.Model == "" {
		payload.Model = a.Cfg.DefaultModel
	}
	if payload.Candidate == "" {
		payload.Candidate = payload.Model
	}
	if a.Gem == nil || !a.Gem.UsesDiscoveryEngine() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "model probing is only meaningful on a Gemini Enterprise origin",
		})
		return
	}

	learned, err := a.Gem.ProbeAndLearnModel(payload.Model, payload.Candidate)
	if err != nil {
		if a.Logf != nil {
			a.Logf("Model probe: %q -> %q failed: %v", payload.Model, payload.Candidate, err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        false,
			"model":     payload.Model,
			"candidate": payload.Candidate,
			"error":     err.Error(),
			"hint": "try another candidate id (the tenant's assistGenerationConfig.modelId, visible in the tab's own widgetStreamAssist request), " +
				"or keep the alias unmapped and let the browser bridge select it in the picker",
		})
		return
	}

	resolved, resolveErr := models.Resolve(payload.Model, a.Cfg.DefaultModel)
	if resolveErr != nil {
		resolved = models.Resolved{Name: payload.Model}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"model":     payload.Model,
		"model_id":  learned,
		"source":    gemini.ModelIDSourceLearned,
		"cached_in": "enterprise_models.json",
		"model_info": a.describeModelSelection(payload.Model, resolved),
	})
}
