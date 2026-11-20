package gemini

// Probing the tenant for a model id.
//
// A Discovery Engine tenant accepts a model id in assistGenerationConfig.modelId,
// but the id it accepts is not always the name this server (or the client) uses
// ("gemini-4-hydrogen" may be exactly right, or it may be rejected). Guessing is
// worse than asking: one tiny streamAssist call answers it, and a verified id is
// cached per alias so every later request can select the model in direct mode.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// enterpriseModelCacheFile stores verified alias → tenant model id pairs next to
// the cookie file, so a probe only has to succeed once.
const enterpriseModelCacheFile = "enterprise_models.json"

var enterpriseModelCacheMu sync.Mutex

func enterpriseModelCachePath(cookieFile string) string {
	dir := filepath.Dir(cookieFile)
	if strings.TrimSpace(cookieFile) == "" {
		dir = "."
	}
	return filepath.Join(dir, enterpriseModelCacheFile)
}

// LoadLearnedModelIDs returns the verified alias → model id pairs.
func LoadLearnedModelIDs(cookieFile string) map[string]string {
	enterpriseModelCacheMu.Lock()
	defer enterpriseModelCacheMu.Unlock()
	return loadLearnedModelIDsLocked(cookieFile)
}

func loadLearnedModelIDsLocked(cookieFile string) map[string]string {
	data, err := os.ReadFile(enterpriseModelCachePath(cookieFile))
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil || len(m) == 0 {
		return nil
	}
	return m
}

// SetLearnedModelID records a verified model id for an alias.
func SetLearnedModelID(cookieFile, alias, modelID string) error {
	alias = strings.TrimSpace(alias)
	modelID = strings.TrimSpace(modelID)
	if alias == "" || modelID == "" {
		return fmt.Errorf("alias and model id are required")
	}

	enterpriseModelCacheMu.Lock()
	defer enterpriseModelCacheMu.Unlock()

	current := loadLearnedModelIDsLocked(cookieFile)
	if current == nil {
		current = make(map[string]string)
	}
	current[alias] = modelID
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(enterpriseModelCachePath(cookieFile), data, 0o600)
}

// learnedModelID looks up a verified id for an alias (case-insensitive).
func (c *Client) learnedModelID(alias string) string {
	lower := strings.ToLower(strings.TrimSpace(alias))
	for key, value := range LoadLearnedModelIDs(c.Cfg.CookieFile) {
		if strings.ToLower(strings.TrimSpace(key)) == lower && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// ProbeEnterpriseWireModel sends a minimal streamAssist request carrying
// candidate as assistGenerationConfig.modelId and reports whether the tenant
// accepted it. A nil error means the id works.
func (c *Client) ProbeEnterpriseWireModel(candidate string) error {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return fmt.Errorf("candidate model id is required")
	}
	configID := strings.TrimSpace(c.enterpriseConfigID(nil))
	if configID == "" {
		return fmt.Errorf("the widget configId is unknown: capture a request from the Gemini Enterprise tab or set discovery_engine_config_id")
	}

	bearer, bearerErr := c.enterpriseBearer()
	usingBearer := bearerErr == nil
	endpoint := ""
	session := strings.TrimSpace(c.enterpriseSessionName())
	if usingBearer {
		endpoint = contentWidgetEndpoint(c.enterpriseWidgetLocation(), "widgetStreamAssist")
		if session == "" || strings.HasSuffix(session, "/-") {
			if created, err := c.ensureEnterpriseSession(configID); err == nil {
				session = created
			}
		}
	} else if pinned := strings.TrimSpace(c.Cfg.DiscoveryEngineEndpoint); pinned != "" {
		endpoint = pinned
	} else {
		return fmt.Errorf("direct mode is unavailable: set discovery_engine_endpoint or capture a template from the tab")
	}

	sessionJSON := ""
	if s := strings.TrimSpace(session); s != "" && !strings.HasSuffix(s, "/-") {
		sessionJSON = fmt.Sprintf(`"session":%q,`, s)
	}
	body := fmt.Sprintf(
		`{"configId":%q,"additionalParams":{"token":"-","origin":"ORIGIN_UNSPECIFIED"},"streamAssistRequest":{%s%s"assistGenerationConfig":{"modelId":%q},"query":{"parts":[{"text":"hi"}]},"answerGenerationMode":"NORMAL","assistSkippingMode":"REQUEST_ASSIST","fileIds":[],"filter":"","languageCode":"en-US","userMetadata":{"timeZone":"UTC"}}}`,
		configID, sessionJSON, "", candidate,
	)

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	c.applyEnterpriseWidgetHeaders(req, bearer)
	if !usingBearer {
		req.Header = c.discoveryHeaders(endpoint)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &UpstreamError{Kind: "transport", Msg: fmt.Sprintf("model probe failed: %v", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "http",
			Msg:    fmt.Sprintf("the tenant rejected model id %q (HTTP %d): %s", candidate, resp.StatusCode, summarizeBody(raw)),
		}
	}
	if len(raw) == 0 {
		return fmt.Errorf("the tenant returned an empty response for model id %q", candidate)
	}
	return nil
}

// ProbeAndLearnModel verifies candidate for alias and, on success, caches it so
// direct mode can select it from now on.
func (c *Client) ProbeAndLearnModel(alias, candidate string) (string, error) {
	if err := c.ProbeEnterpriseWireModel(candidate); err != nil {
		return "", err
	}
	learned := strings.TrimSpace(candidate)
	if err := SetLearnedModelID(c.Cfg.CookieFile, alias, learned); err != nil {
		c.Logf("Gemini Enterprise: verified model id %q for %q but could not cache it: %v", learned, alias, err)
	}
	c.Logf("Gemini Enterprise: tenant accepted model id %q for %q — cached in %s, direct mode will now select it",
		learned, alias, enterpriseModelCacheFile)
	return learned, nil
}

// maybeProbeEnterpriseModel learns a tenant model id for alias by asking the
// tenant once. Failures are remembered for the process so a request never pays
// for the same probe twice.
func (c *Client) maybeProbeEnterpriseModel(alias string) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return
	}
	if _, source := c.EnterpriseModelIDSource(alias); source != ModelIDSourceTenantDefault {
		return
	}

	c.enterpriseAuthMu.Lock()
	if c.probedModels == nil {
		c.probedModels = make(map[string]bool)
	}
	lower := strings.ToLower(alias)
	if c.probedModels[lower] {
		c.enterpriseAuthMu.Unlock()
		return
	}
	c.probedModels[lower] = true
	c.enterpriseAuthMu.Unlock()

	if _, err := c.ProbeAndLearnModel(alias, alias); err != nil {
		c.Logf("Gemini Enterprise: tenant does not accept %q as a model id (%v) — falling back to the tenant's auto/default model; "+
			"map it explicitly in discovery_engine_models to pin a model", alias, err)
	}
}
