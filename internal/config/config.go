package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Port                       int      `json:"port"`
	Host                       string   `json:"host"`
	RetryAttempts              int      `json:"retry_attempts"`
	RetryDelaySec              int      `json:"retry_delay_sec"`
	RequestTimeoutSec          int      `json:"request_timeout_sec"`
	GeminiBL                   string   `json:"gemini_bl"`
	AuthUser                   string   `json:"auth_user"`
	XSRFToken                  string   `json:"xsrf_token"`
	DefaultModel               string   `json:"default_model"`
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
	BridgeDeltaContext         bool     `json:"bridge_delta_context"`
	BridgeAutoNewChat          bool     `json:"bridge_auto_new_chat"`
	BridgeNoContentTimeoutSec  int      `json:"bridge_no_content_timeout_sec"`
	BridgePickupTimeoutSec     int      `json:"bridge_pickup_timeout_sec"`
	BridgeToken                string   `json:"bridge_token"`
	NotifyWebhook              string   `json:"notify_webhook"`
	NotifyFormat               string   `json:"notify_format"`
	// DiscoveryEngineEndpoint is the Gemini Enterprise Discovery Engine
	// widgetStreamAssist endpoint URL.
	DiscoveryEngineEndpoint string `json:"discovery_engine_endpoint"`
	// DiscoveryEngineConfigID is the WidgetConfig ID for widgetStreamAssist.
	DiscoveryEngineConfigID string `json:"discovery_engine_config_id"`
	// DiscoveryEngineServerToken is the x-server-token header value for
	// widgetStreamAssist (required to pass reCAPTCHA check).
	DiscoveryEngineServerToken string `json:"discovery_engine_server_token"`
}

func Default() Config {
	return Config{
		Port:                       8081,
		Host:                       "0.0.0.0",
		RetryAttempts:              3,
		RetryDelaySec:              2,
		RequestTimeoutSec:          300,
		GeminiBL:                   "boq_assistant-bard-web-server_20260716.08_p0",
		AuthUser:                   "",
		XSRFToken:                  "",
		DefaultModel:               "gemini-3.6-flash",
		LogRequests:                true,
		CookieFile:                 "",
		Proxy:                      "",
		APIKeys:                    []string{},
		Impersonate:                "",
		TemporaryChats:             false,
		BaseURL:                    "https://gemini.google.com",
		BridgeMinIntervalMs:        3000,
		BridgeJitterMs:             2000,
		BridgeMaxPer10Min:          15,
		BridgeMaxPerHour:           50,
		BridgePacingStateFile:      "",
		BridgeAutoUploadLocalFiles: true,
		BridgeLargeCodeUploadBytes: 8192,
		BridgeSpilloverChars:       20000,
		BridgeDeltaContext:         true,
		BridgeAutoNewChat:          true,
		BridgeNoContentTimeoutSec:  120,
		BridgePickupTimeoutSec:     45,
		BridgeToken:                "",
		NotifyWebhook:              "",
		NotifyFormat:               "ntfy",
		DiscoveryEngineEndpoint:    "",
		DiscoveryEngineConfigID:    "",
		DiscoveryEngineServerToken: "",
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
		Port              *int      `json:"port"`
		Host              *string   `json:"host"`
		RetryAttempts     *int      `json:"retry_attempts"`
		RetryDelaySec     *int      `json:"retry_delay_sec"`
		RequestTimeoutSec *int      `json:"request_timeout_sec"`
		GeminiBL          *string   `json:"gemini_bl"`
		AuthUser          *string   `json:"auth_user"`
		XSRFToken         *string   `json:"xsrf_token"`
		DefaultModel      *string   `json:"default_model"`
		LogRequests       *bool     `json:"log_requests"`
		CookieFile        *string   `json:"cookie_file"`
		Proxy             *string   `json:"proxy"`
		APIKeys           *[]string `json:"api_keys"`
		Impersonate                *string   `json:"impersonate"`
		TemporaryChats             *bool     `json:"temporary_chats"`
		BaseURL                    *string   `json:"base_url"`
		BridgeMinIntervalMs        *int      `json:"bridge_min_interval_ms"`
		BridgeJitterMs             *int      `json:"bridge_jitter_ms"`
		BridgeMaxPer10Min          *int      `json:"bridge_max_per_10min"`
		BridgeMaxPerHour           *int      `json:"bridge_max_per_hour"`
		BridgePacingStateFile      *string   `json:"bridge_pacing_state_file"`
		BridgeAutoUploadLocalFiles *bool     `json:"bridge_auto_upload_local_files"`
		BridgeLargeCodeUploadBytes *int      `json:"bridge_large_code_upload_bytes"`
		BridgeSpilloverChars       *int      `json:"bridge_spillover_chars"`
		BridgeDeltaContext         *bool     `json:"bridge_delta_context"`
		BridgeAutoNewChat          *bool     `json:"bridge_auto_new_chat"`
		BridgeNoContentTimeoutSec  *int      `json:"bridge_no_content_timeout_sec"`
		BridgePickupTimeoutSec     *int      `json:"bridge_pickup_timeout_sec"`
		BridgeToken                *string   `json:"bridge_token"`
		NotifyWebhook              *string   `json:"notify_webhook"`
		NotifyFormat               *string   `json:"notify_format"`
		DiscoveryEngineEndpoint    *string   `json:"discovery_engine_endpoint"`
		DiscoveryEngineConfigID    *string   `json:"discovery_engine_config_id"`
		DiscoveryEngineServerToken *string   `json:"discovery_engine_server_token"`
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
