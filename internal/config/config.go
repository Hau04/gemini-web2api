package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Port              int    `json:"port"`
	Host              string `json:"host"`
	RetryAttempts     int    `json:"retry_attempts"`
	RetryDelaySec     int    `json:"retry_delay_sec"`
	RequestTimeoutSec int    `json:"request_timeout_sec"`
	GeminiBL          string `json:"gemini_bl"`
	AuthUser          string `json:"auth_user"`
	XSRFToken         string `json:"xsrf_token"`
	DefaultModel      string `json:"default_model"`
	// ToolModel redirects tool-enabled requests to another upstream model so
	// agent tool calls stay local (e.g. gemini-4-hydrogen, whose tool_call
	// output WorkBuddy executes, instead of a model that prefers Gemini's
	// built-in Deep Research / web tools). Empty disables the redirect.
	ToolModel                  string   `json:"tool_model"`
	LogRequests                bool     `json:"log_requests"`
	CookieFile                 string   `json:"cookie_file"`
	Proxy                      string   `json:"proxy"`
	APIKeys                    []string `json:"api_keys"`
	Impersonate                string   `json:"impersonate"`
	TemporaryChats             bool     `json:"temporary_chats"`
	BaseURL                    string   `json:"base_url"`
	BridgeMinIntervalMs        int      `json:"bridge_min_interval_ms"`
	BridgeJitterMs             int      `json:"bridge_jitter_ms"`
	BridgeMaxPer10Min          int      `json:"bridge_max_per_10min"`
	BridgeMaxPerHour           int      `json:"bridge_max_per_hour"`
	BridgePacingStateFile      string   `json:"bridge_pacing_state_file"`
	BridgeAutoUploadLocalFiles bool     `json:"bridge_auto_upload_local_files"`
	BridgeLargeCodeUploadBytes int      `json:"bridge_large_code_upload_bytes"`
	BridgeSpilloverChars       int      `json:"bridge_spillover_chars"`
	// PreventDeepResearch keeps premium (research-prone) Gemini 4 models from
	// drifting into Deep Research / web browsing when the request declares
	// tools. Those modes read the public web only and cannot invoke the client's
	// tools, connectors or MCP servers, so a tool-enabled request would fail.
	PreventDeepResearch       bool   `json:"prevent_deep_research"`
	BridgeDeltaContext        bool   `json:"bridge_delta_context"`
	BridgeAutoNewChat         bool   `json:"bridge_auto_new_chat"`
	BridgeNoContentTimeoutSec int    `json:"bridge_no_content_timeout_sec"`
	BridgePickupTimeoutSec    int    `json:"bridge_pickup_timeout_sec"`
	BridgeToken               string `json:"bridge_token"`
	NotifyWebhook             string `json:"notify_webhook"`
	NotifyFormat              string `json:"notify_format"`
	// DiscoveryEngineEndpoint is the Gemini Enterprise Discovery Engine
	// widgetStreamAssist endpoint URL.
	DiscoveryEngineEndpoint string `json:"discovery_engine_endpoint"`
	// DiscoveryEngineConfigID is the WidgetConfig ID for widgetStreamAssist.
	DiscoveryEngineConfigID string `json:"discovery_engine_config_id"`
	// DiscoveryEngineServerToken is the x-server-token header value for
	// widgetStreamAssist (required to pass reCAPTCHA check).
	DiscoveryEngineServerToken string `json:"discovery_engine_server_token"`
	// DiscoveryEngineUploadEndpoint is the widgetAddContextFile URL used to
	// attach files to a Gemini Enterprise session in direct cookie mode. When
	// empty it is derived from discovery_engine_endpoint, and a request captured
	// by the extension is preferred over both.
	DiscoveryEngineUploadEndpoint string `json:"discovery_engine_upload_endpoint"`
	// DiscoveryEngineSession is the (sanitized) session resource used for
	// context-file uploads, e.g.
	// "collections/default_collection/engines/my-engine/sessions/-". Empty means
	// derive it from a captured request template.
	DiscoveryEngineSession string `json:"discovery_engine_session"`
	// DiscoveryEngineUploadCacheSec caches uploaded context files by content
	// hash for this many seconds so repeated turns that resend the same file
	// skip the upload. 0 disables the cache.
	DiscoveryEngineUploadCacheSec int `json:"discovery_engine_upload_cache_sec"`
	// DiscoveryEngineCsesidx is the Gemini Enterprise conversation/config
	// sequence id ("csesidx") used to mint the widget XSRF/JWT. It is captured
	// automatically by the extension from the tab URL when absent here.
	DiscoveryEngineCsesidx string `json:"discovery_engine_csesidx"`
	// DiscoveryEngineLocation is the Discovery Engine location for the widget
	// host (us, eu, global). Defaults to the value in discovery_engine_endpoint
	// or "us".
	DiscoveryEngineLocation string `json:"discovery_engine_location"`
	// DiscoveryEngineModels maps a requested model name (e.g.
	// "gemini-4-hydrogen") to the Discovery Engine modelId the tenant knows.
	// Gemini Enterprise ignores model choice unless assistGenerationConfig is
	// sent, so this is what makes the client's model selection take effect.
	DiscoveryEngineModels map[string]string `json:"discovery_engine_models"`
	// DiscoveryEngineDefaultModelID is used for requests whose model has no
	// mapping entry. Empty leaves the tenant default (its own "auto").
	DiscoveryEngineDefaultModelID string `json:"discovery_engine_default_model_id"`
	// DiscoveryEngineProbeModels asks the tenant once per alias whether it accepts
	// that alias as a model id and caches the answer. It is how a tenant id gets
	// discovered automatically instead of sending nothing and being answered by
	// the tenant's auto/default model.
	DiscoveryEngineProbeModels bool `json:"discovery_engine_probe_models"`
	// EnterpriseAutoAttachLocalFiles reads files the latest user message
	// references by path and attaches them as context files instead of letting
	// their content be re-inlined on every turn (the usual rate-limit cause for
	// coding agents on Enterprise).
	EnterpriseAutoAttachLocalFiles bool `json:"enterprise_auto_attach_local_files"`
	// EnterpriseAutoAttachMinBytes is the minimum size for a plain-text/source
	// file to be attached rather than left inline. 0 reuses
	// bridge_large_code_upload_bytes.
	EnterpriseAutoAttachMinBytes int `json:"enterprise_auto_attach_min_bytes"`
	// EnterpriseAttachLargePrompt spills an oversized prompt (or its large code
	// blocks) into one .txt context file so the replayed prompt stays small.
	EnterpriseAttachLargePrompt bool `json:"enterprise_attach_large_prompt"`
	// EnterpriseMirrorTextInline also writes a text file's content into the
	// prompt, on top of uploading it as a context file. An upload only becomes
	// readable if the tenant's file reader exposes it; the prompt always is. Set
	// false to keep prompts small and trust the upload path entirely.
	EnterpriseMirrorTextInline bool `json:"enterprise_mirror_text_inline"`
}

func Default() Config {
	return Config{
		Port:                           8081,
		Host:                           "0.0.0.0",
		RetryAttempts:                  3,
		RetryDelaySec:                  2,
		RequestTimeoutSec:              300,
		GeminiBL:                       "boq_assistant-bard-web-server_20260716.08_p0",
		AuthUser:                       "",
		XSRFToken:                      "",
		DefaultModel:                   "gemini-3.6-flash",
		ToolModel:                      "",
		LogRequests:                    true,
		CookieFile:                     "",
		Proxy:                          "",
		APIKeys:                        []string{},
		Impersonate:                    "",
		TemporaryChats:                 false,
		BaseURL:                        "https://gemini.google.com",
		BridgeMinIntervalMs:            3000,
		BridgeJitterMs:                 2000,
		BridgeMaxPer10Min:              15,
		BridgeMaxPerHour:               50,
		BridgePacingStateFile:          "",
		BridgeAutoUploadLocalFiles:     true,
		BridgeLargeCodeUploadBytes:     8192,
		BridgeSpilloverChars:           20000,
		PreventDeepResearch:            true,
		BridgeDeltaContext:             true,
		BridgeAutoNewChat:              true,
		BridgeNoContentTimeoutSec:      120,
		BridgePickupTimeoutSec:         45,
		BridgeToken:                    "",
		NotifyWebhook:                  "",
		NotifyFormat:                   "ntfy",
		DiscoveryEngineEndpoint:        "",
		DiscoveryEngineConfigID:        "",
		DiscoveryEngineServerToken:     "",
		DiscoveryEngineUploadEndpoint:  "",
		DiscoveryEngineSession:         "",
		DiscoveryEngineUploadCacheSec:  1800,
		DiscoveryEngineCsesidx:         "",
		DiscoveryEngineLocation:        "",
		DiscoveryEngineModels:          map[string]string{},
		DiscoveryEngineDefaultModelID:  "",
		DiscoveryEngineProbeModels:     true,
		EnterpriseAutoAttachLocalFiles: true,
		EnterpriseAutoAttachMinBytes:   0,
		EnterpriseAttachLargePrompt:    true,
		EnterpriseMirrorTextInline:     true,
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}

	// Use temporary struct with pointer fields or interface to handle null values from JSON if needed,
	// but standard unmarshal over struct fields handles JSON null for strings by leaving existing or setting zero-value if unmarshaling map/struct.
	// Let's ensure string fields default properly if JSON has explicitly `null`.
	var aux struct {
		Port                           *int              `json:"port"`
		Host                           *string           `json:"host"`
		RetryAttempts                  *int              `json:"retry_attempts"`
		RetryDelaySec                  *int              `json:"retry_delay_sec"`
		RequestTimeoutSec              *int              `json:"request_timeout_sec"`
		GeminiBL                       *string           `json:"gemini_bl"`
		AuthUser                       *string           `json:"auth_user"`
		XSRFToken                      *string           `json:"xsrf_token"`
		DefaultModel                   *string           `json:"default_model"`
		ToolModel                      *string           `json:"tool_model"`
		LogRequests                    *bool             `json:"log_requests"`
		CookieFile                     *string           `json:"cookie_file"`
		Proxy                          *string           `json:"proxy"`
		APIKeys                        *[]string         `json:"api_keys"`
		Impersonate                    *string           `json:"impersonate"`
		TemporaryChats                 *bool             `json:"temporary_chats"`
		BaseURL                        *string           `json:"base_url"`
		BridgeMinIntervalMs            *int              `json:"bridge_min_interval_ms"`
		BridgeJitterMs                 *int              `json:"bridge_jitter_ms"`
		BridgeMaxPer10Min              *int              `json:"bridge_max_per_10min"`
		BridgeMaxPerHour               *int              `json:"bridge_max_per_hour"`
		BridgePacingStateFile          *string           `json:"bridge_pacing_state_file"`
		BridgeAutoUploadLocalFiles     *bool             `json:"bridge_auto_upload_local_files"`
		BridgeLargeCodeUploadBytes     *int              `json:"bridge_large_code_upload_bytes"`
		BridgeSpilloverChars           *int              `json:"bridge_spillover_chars"`
		PreventDeepResearch            *bool             `json:"prevent_deep_research"`
		BridgeDeltaContext             *bool             `json:"bridge_delta_context"`
		BridgeAutoNewChat              *bool             `json:"bridge_auto_new_chat"`
		BridgeNoContentTimeoutSec      *int              `json:"bridge_no_content_timeout_sec"`
		BridgePickupTimeoutSec         *int              `json:"bridge_pickup_timeout_sec"`
		BridgeToken                    *string           `json:"bridge_token"`
		NotifyWebhook                  *string           `json:"notify_webhook"`
		NotifyFormat                   *string           `json:"notify_format"`
		DiscoveryEngineEndpoint        *string           `json:"discovery_engine_endpoint"`
		DiscoveryEngineConfigID        *string           `json:"discovery_engine_config_id"`
		DiscoveryEngineServerToken     *string           `json:"discovery_engine_server_token"`
		DiscoveryEngineUploadEndpoint  *string           `json:"discovery_engine_upload_endpoint"`
		DiscoveryEngineSession         *string           `json:"discovery_engine_session"`
		DiscoveryEngineUploadCacheSec  *int              `json:"discovery_engine_upload_cache_sec"`
		DiscoveryEngineCsesidx         *string           `json:"discovery_engine_csesidx"`
		DiscoveryEngineLocation        *string           `json:"discovery_engine_location"`
		DiscoveryEngineModels          map[string]string `json:"discovery_engine_models"`
		DiscoveryEngineDefaultModelID  *string           `json:"discovery_engine_default_model_id"`
		DiscoveryEngineProbeModels     *bool             `json:"discovery_engine_probe_models"`
		EnterpriseAutoAttachLocalFiles *bool             `json:"enterprise_auto_attach_local_files"`
		EnterpriseAutoAttachMinBytes   *int              `json:"enterprise_auto_attach_min_bytes"`
		EnterpriseAttachLargePrompt    *bool             `json:"enterprise_attach_large_prompt"`
		EnterpriseMirrorTextInline     *bool             `json:"enterprise_mirror_text_inline"`
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return cfg, err
	}

	if aux.Port != nil {
		cfg.Port = *aux.Port
	}
	if aux.Host != nil {
		cfg.Host = *aux.Host
	}
	if aux.RetryAttempts != nil {
		cfg.RetryAttempts = *aux.RetryAttempts
	}
	if aux.RetryDelaySec != nil {
		cfg.RetryDelaySec = *aux.RetryDelaySec
	}
	if aux.RequestTimeoutSec != nil {
		cfg.RequestTimeoutSec = *aux.RequestTimeoutSec
	}
	if aux.GeminiBL != nil {
		cfg.GeminiBL = *aux.GeminiBL
	}
	if aux.AuthUser != nil {
		cfg.AuthUser = *aux.AuthUser
	}
	if aux.XSRFToken != nil {
		cfg.XSRFToken = *aux.XSRFToken
	}
	if aux.DefaultModel != nil {
		cfg.DefaultModel = *aux.DefaultModel
	}
	if aux.ToolModel != nil {
		cfg.ToolModel = *aux.ToolModel
	}
	if aux.LogRequests != nil {
		cfg.LogRequests = *aux.LogRequests
	}
	if aux.CookieFile != nil {
		cfg.CookieFile = *aux.CookieFile
	}
	if aux.Proxy != nil {
		cfg.Proxy = *aux.Proxy
	}
	if aux.APIKeys != nil {
		cfg.APIKeys = *aux.APIKeys
	}
	if aux.Impersonate != nil {
		cfg.Impersonate = *aux.Impersonate
	}
	if aux.TemporaryChats != nil {
		cfg.TemporaryChats = *aux.TemporaryChats
	}
	if aux.BaseURL != nil && *aux.BaseURL != "" {
		cfg.BaseURL = *aux.BaseURL
	}
	if aux.BridgeMinIntervalMs != nil && *aux.BridgeMinIntervalMs >= 0 {
		cfg.BridgeMinIntervalMs = *aux.BridgeMinIntervalMs
	}
	if aux.BridgeJitterMs != nil && *aux.BridgeJitterMs >= 0 {
		cfg.BridgeJitterMs = *aux.BridgeJitterMs
	}
	if aux.BridgeMaxPer10Min != nil && *aux.BridgeMaxPer10Min >= 0 {
		cfg.BridgeMaxPer10Min = *aux.BridgeMaxPer10Min
	}
	if aux.BridgeMaxPerHour != nil && *aux.BridgeMaxPerHour >= 0 {
		cfg.BridgeMaxPerHour = *aux.BridgeMaxPerHour
	}
	if aux.BridgePacingStateFile != nil {
		cfg.BridgePacingStateFile = *aux.BridgePacingStateFile
	}
	if aux.BridgeAutoUploadLocalFiles != nil {
		cfg.BridgeAutoUploadLocalFiles = *aux.BridgeAutoUploadLocalFiles
	}
	if aux.BridgeLargeCodeUploadBytes != nil && *aux.BridgeLargeCodeUploadBytes >= 0 {
		cfg.BridgeLargeCodeUploadBytes = *aux.BridgeLargeCodeUploadBytes
	}
	if aux.PreventDeepResearch != nil {
		cfg.PreventDeepResearch = *aux.PreventDeepResearch
	}
	if aux.BridgeSpilloverChars != nil && *aux.BridgeSpilloverChars >= 0 {
		cfg.BridgeSpilloverChars = *aux.BridgeSpilloverChars
	}
	if aux.BridgeDeltaContext != nil {
		cfg.BridgeDeltaContext = *aux.BridgeDeltaContext
	}
	if aux.BridgeAutoNewChat != nil {
		cfg.BridgeAutoNewChat = *aux.BridgeAutoNewChat
	}
	if aux.BridgeNoContentTimeoutSec != nil && *aux.BridgeNoContentTimeoutSec >= 0 {
		cfg.BridgeNoContentTimeoutSec = *aux.BridgeNoContentTimeoutSec
	}
	if aux.BridgePickupTimeoutSec != nil && *aux.BridgePickupTimeoutSec >= 0 {
		cfg.BridgePickupTimeoutSec = *aux.BridgePickupTimeoutSec
	}
	if aux.BridgeToken != nil {
		cfg.BridgeToken = *aux.BridgeToken
	}
	if aux.NotifyWebhook != nil {
		cfg.NotifyWebhook = *aux.NotifyWebhook
	}
	if aux.NotifyFormat != nil {
		cfg.NotifyFormat = *aux.NotifyFormat
	}
	if aux.DiscoveryEngineEndpoint != nil {
		cfg.DiscoveryEngineEndpoint = *aux.DiscoveryEngineEndpoint
	}
	if aux.DiscoveryEngineConfigID != nil {
		cfg.DiscoveryEngineConfigID = *aux.DiscoveryEngineConfigID
	}
	if aux.DiscoveryEngineServerToken != nil {
		cfg.DiscoveryEngineServerToken = *aux.DiscoveryEngineServerToken
	}
	if aux.DiscoveryEngineUploadEndpoint != nil {
		cfg.DiscoveryEngineUploadEndpoint = *aux.DiscoveryEngineUploadEndpoint
	}
	if aux.DiscoveryEngineSession != nil {
		cfg.DiscoveryEngineSession = *aux.DiscoveryEngineSession
	}
	if aux.DiscoveryEngineUploadCacheSec != nil {
		cfg.DiscoveryEngineUploadCacheSec = *aux.DiscoveryEngineUploadCacheSec
	}
	if aux.DiscoveryEngineCsesidx != nil {
		cfg.DiscoveryEngineCsesidx = *aux.DiscoveryEngineCsesidx
	}
	if aux.DiscoveryEngineLocation != nil {
		cfg.DiscoveryEngineLocation = *aux.DiscoveryEngineLocation
	}
	if len(aux.DiscoveryEngineModels) > 0 {
		cfg.DiscoveryEngineModels = aux.DiscoveryEngineModels
	}
	if aux.DiscoveryEngineDefaultModelID != nil {
		cfg.DiscoveryEngineDefaultModelID = *aux.DiscoveryEngineDefaultModelID
	}
	if aux.DiscoveryEngineProbeModels != nil {
		cfg.DiscoveryEngineProbeModels = *aux.DiscoveryEngineProbeModels
	}
	if aux.EnterpriseAutoAttachLocalFiles != nil {
		cfg.EnterpriseAutoAttachLocalFiles = *aux.EnterpriseAutoAttachLocalFiles
	}
	if aux.EnterpriseAutoAttachMinBytes != nil {
		cfg.EnterpriseAutoAttachMinBytes = *aux.EnterpriseAutoAttachMinBytes
	}
	if aux.EnterpriseMirrorTextInline != nil {
		cfg.EnterpriseMirrorTextInline = *aux.EnterpriseMirrorTextInline
	}
	if aux.EnterpriseAttachLargePrompt != nil {
		cfg.EnterpriseAttachLargePrompt = *aux.EnterpriseAttachLargePrompt
	}

	return cfg, nil
}

func Find() string {
	paths := []string{"./config.json"}
	home, err := os.UserHomeDir()
	if err == nil {
		paths = append(paths, filepath.Join(home, ".config", "gemini-web2api", "config.json"))
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
