package server

import (
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
			ModelID:       id,
			Source:        source,
			SelectionLive: source == gemini.ModelIDSourceMapping || source == gemini.ModelIDSourcePassthrough,
		}
		if len(a.Cfg.DiscoveryEngineModels) > 0 {
			info.Mappings = make(map[string]string, len(a.Cfg.DiscoveryEngineModels))
			for k, v := range a.Cfg.DiscoveryEngineModels {
				info.Mappings[k] = v
			}
		}
		info.Warnings = a.modelSelectionWarnings(resolved, id, source)
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
					"Copy the tenant's real model id (DevTools → the tab's own widgetStreamAssist request → assistGenerationConfig.modelId) and map it, "+
					"e.g. {\"gemini-4-hydrogen\": \"<tenant model id>\"}. Until then the reply's own \"Auto Mode\" description is accurate.",
				resolved.Name)}
		}
		return []string{fmt.Sprintf(
			"%q is not in discovery_engine_models (configured keys: %s) and discovery_engine_default_model_id is empty, so the tenant answers with its auto/default model.",
			resolved.Name, strings.Join(a.mappingKeys(), ", "))}
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
