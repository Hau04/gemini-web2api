package gemini

// Gemini Enterprise (vertexaisearch.cloud.google.com) widget API authentication.
//
// The Discovery Engine widget endpoints (widgetStreamAssist / widgetCreateSession
// / widgetAddContextFile) do NOT accept the consumer-style SAPISIDHASH header.
// The app obtains a short-lived HS256 JWT instead:
//
//	GET  {origin}/auth/getoxsrf?csesidx={id}   (cookies)
//	  -> {"xsrfToken":"<base64url HMAC key>","keyId":"<kid>"}
//	header  {"alg":"HS256","typ":"JWT","kid":"<kid>"}
//	payload {"iss":"https://vertexaisearch.cloud.google.com",
//	         "aud":"https://content-us-discoveryengine.googleapis.com",
//	         "sub":"csesidx/<id>","iat":..,"exp":+300,"nbf":..}
//	POST https://content-us-discoveryengine.googleapis.com/v1alpha/locations/us/widgetStreamAssist
//	     Authorization: Bearer <jwt>
//
// Every widget call also carries {"additionalParams":{"token":"-",
// "origin":"ORIGIN_UNSPECIFIED"}} and a real session, created up front through
// widgetCreateSession. Sending a cookie session with configId "default" (the old
// direct path) is what made Enterprise streaming answer 502.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	defaultEnterpriseOrigin   = "https://vertexaisearch.cloud.google.com"
	defaultEnterpriseLocation = "us"
	enterpriseJWTTTL          = 300 * time.Second
)

// enterpriseOrigin returns the Gemini Enterprise app origin used for the widget
// JWT issuer/referer: the synced tab origin when available, else the config.
func (c *Client) enterpriseOrigin() string {
	if c.Cookies != nil {
		if info, err := c.Cookies.Load(); err == nil && strings.TrimSpace(info.BaseURL) != "" {
			return strings.TrimRight(strings.TrimSpace(info.BaseURL), "/")
		}
	}
	if base := strings.TrimRight(strings.TrimSpace(c.Cfg.BaseURL), "/"); base != "" {
		return base
	}
	return defaultEnterpriseOrigin
}

// enterpriseWidgetLocation resolves the bare Discovery Engine location used to
// build the regional widget host. "global" is not a regional widget frontend, so
// it falls back to the default location unless configured explicitly.
func (c *Client) enterpriseWidgetLocation() string {
	if loc := strings.TrimSpace(c.Cfg.DiscoveryEngineLocation); loc != "" {
		return loc
	}
	for _, endpoint := range []string{c.Cfg.DiscoveryEngineEndpoint, c.Cfg.DiscoveryEngineUploadEndpoint} {
		if loc := locationFromEndpoint(endpoint); loc != "" && !strings.EqualFold(loc, "global") {
			return loc
		}
	}
	return defaultEnterpriseLocation
}

func locationFromEndpoint(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Path == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "locations" && parts[i+1] != "" {
			return parts[i+1]
		}
	}
	return ""
}

// enterpriseLocationFromURL also honours a location encoded in a captured URL.
func (c *Client) enterpriseLocationFromURL(captured string) string {
	if loc := locationFromEndpoint(captured); loc != "" && strings.TrimSpace(c.Cfg.DiscoveryEngineLocation) == "" {
		return loc
	}
	return c.enterpriseWidgetLocation()
}

// contentWidgetEndpoint builds the regional Discovery Engine "content" widget
// URL that serves the JWT-authenticated widget methods for a location.
// contentWidgetHostFunc is swapped in tests to point the widget host at a stub.
var contentWidgetHostFunc = contentWidgetHost

func contentWidgetHost(location string) string {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = defaultEnterpriseLocation
	}
	return fmt.Sprintf("https://content-%s-discoveryengine.googleapis.com", loc)
}

func contentWidgetEndpoint(location, method string) string {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = defaultEnterpriseLocation
	}
	return fmt.Sprintf("%s/v1alpha/locations/%s/%s", strings.TrimRight(contentWidgetHostFunc(loc), "/"), loc, method)
}

// enterpriseCSESIDX resolves the conversation/config sequence id (csesidx) that
// scopes the XSRF token. It comes from config, the cookie file (captured by the
// extension from the tab URL), or a captured widget request URL.
func (c *Client) enterpriseCSESIDX() string {
	if id := strings.TrimSpace(c.Cfg.DiscoveryEngineCsesidx); id != "" {
		return id
	}
	if c.Cookies != nil {
		if info, err := c.Cookies.Load(); err == nil && strings.TrimSpace(info.Csesidx) != "" {
			return strings.TrimSpace(info.Csesidx)
		}
	}
	for _, tpl := range []string{"req", "upload"} {
		var body, endpoint string
		if tpl == "req" {
			if t := LoadDiscoveryTemplate(c.Cfg.CookieFile); t != nil {
				body, endpoint = t.Body, t.URL
			}
		} else if t := LoadDiscoveryUploadTemplate(c.Cfg.CookieFile); t != nil {
			body, endpoint = t.Body, t.URL
		}
		if id := csesidxFromURL(endpoint); id != "" {
			return id
		}
		if id := csesidxFromBody(body); id != "" {
			return id
		}
	}
	return ""
}

func csesidxFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.RawQuery == "" {
		return ""
	}
	return strings.TrimSpace(u.Query().Get("csesidx"))
}

var csesidxBodyRe = regexp.MustCompile(`"csesidx"\s*:\s*"?([0-9]+)"?`)

func csesidxFromBody(body string) string {
	if m := csesidxBodyRe.FindStringSubmatch(body); len(m) == 2 {
		return m[1]
	}
	return ""
}

// enterpriseBearer returns a cached (or freshly minted) widget JWT. It returns
// "" when no csesidx is known, which selects the legacy cookie path.
func (c *Client) enterpriseBearer() (string, error) {
	csesidx := c.enterpriseCSESIDX()
	if csesidx == "" {
		return "", errNoCsesidx
	}
	key := csesidx + "\x00" + c.enterpriseWidgetLocation()
	c.enterpriseAuthMu.Lock()
	if c.enterpriseJWT != "" && time.Now().Before(c.enterpriseJWTExp) {
		token := c.enterpriseJWT
		c.enterpriseAuthMu.Unlock()
		return token, nil
	}
	origin := c.enterpriseOrigin()
	audience := contentWidgetHost(c.enterpriseWidgetLocation())
	c.enterpriseAuthMu.Unlock()

	xsrfToken, keyID, err := c.fetchEnterpriseXSRF(csesidx)
	if err != nil {
		return "", err
	}
	token, err := mintEnterpriseJWT(xsrfToken, keyID, origin, audience, csesidx, time.Now())
	if err != nil {
		return "", err
	}

	c.enterpriseAuthMu.Lock()
	c.enterpriseJWT = token
	c.enterpriseJWTExp = time.Now().Add(enterpriseJWTTTL - 30*time.Second)
	c.enterpriseJWTKey = key
	c.enterpriseAuthMu.Unlock()
	c.Logf("Gemini Enterprise: minted widget JWT for csesidx=%s (location=%s, kid=%s)", csesidx, c.enterpriseWidgetLocation(), keyID)
	return token, nil
}

func (c *Client) resetEnterpriseAuth() {
	c.enterpriseAuthMu.Lock()
	c.enterpriseJWT = ""
	c.enterpriseJWTExp = time.Time{}
	c.enterpriseSession = ""
	c.enterpriseAuthMu.Unlock()
}

// reuseOrCreateEnterpriseSession pins the conversation for the coming request.
//
// Gemini Enterprise opens a brand-new conversation for every streamAssist that
// carries no session, which is exactly what "every message opens a new chat in
// Gemini" looks like. Session creation used to happen only when a widget JWT was
// available, so cookie-authenticated setups (bearer=false) never got one and
// every single message started a fresh thread.
func (c *Client) reuseOrCreateEnterpriseSession(session string, opt ModelOptions) string {
	if s := strings.TrimSpace(session); s != "" && !strings.HasSuffix(s, "/-") {
		return s
	}
	configID := strings.TrimSpace(c.enterpriseConfigID(nil))
	if configID == "" {
		// No config id means widgetCreateSession cannot be called at all; the
		// caller reports that separately.
		return strings.TrimSpace(session)
	}

	switch c.sessionAction(opt) {
	case sessionActionNewChat:
		c.clearEnterpriseSession()
	}
	created, err := c.ensureEnterpriseSession(configID)
	if err != nil {
		c.Logf("Gemini Enterprise: could not create an assistant session (%v) — the tenant will show every request as a new conversation", err)
		return ""
	}
	return created
}

type sessionAction int

const (
	// sessionActionContinue reuses whatever session is cached: either the same
	// client conversation, or a stateless client whose next message we cannot
	// tell apart from a continuation.
	sessionActionContinue sessionAction = iota
	// sessionActionNewChat starts a fresh thread: the client opened a new chat.
	sessionActionNewChat
)

// sessionAction decides whether the coming request belongs to the cached
// conversation. The payload carries no conversation id, so the decision uses the
// two signals it does carry: the first user message (stable while a chat is being
// continued) and whether assistant/tool turns are present.
func (c *Client) sessionAction(opt ModelOptions) sessionAction {
	c.enterpriseAuthMu.Lock()
	defer c.enterpriseAuthMu.Unlock()

	key := strings.TrimSpace(opt.ConversationKey)
	action := sessionActionContinue

	switch {
	case opt.ConversationHasHistory && key != "":
		// A real conversation: it owns a session, and only that session is right.
		if owned, ok := c.enterpriseSessions[key]; ok {
			if owned != c.enterpriseSession {
				c.Logf("Enterprise: switching back to the session owned by conversation %s", key)
				c.enterpriseSession = owned
			}
			c.enterpriseSessionKey = key
		} else if c.enterpriseSession == "" {
			action = sessionActionNewChat
		} else if c.enterprisePendingKey == key {
			// First turn of this chat was the lone message that created the
			// cached session — adopt it instead of opening another thread.
			if c.enterpriseSessions == nil {
				c.enterpriseSessions = make(map[string]string)
			}
			c.enterpriseSessions[key] = c.enterpriseSession
			c.enterpriseSessionKey = key
			c.enterprisePendingKey = ""
		} else if c.enterpriseSessionKey != "" && c.enterpriseSessionKey != key {
			action = sessionActionNewChat
		}
	case !opt.ConversationHasHistory:
		// A lone message. Right after a multi-turn exchange it is the first
		// message of a new chat ("open a new chat" in the client). Right after
		// another lone message it is a stateless client continuing — keep it.
		if c.enterpriseLastHadHistory {
			action = sessionActionNewChat
		}
		if key != "" {
			c.enterprisePendingKey = key
		}
	}

	c.enterpriseLastHadHistory = opt.ConversationHasHistory
	return action
}

// recordEnterpriseSession logs and remembers the session chosen for a request.
func (c *Client) recordEnterpriseSession(session string, opt ModelOptions) {
	key := strings.TrimSpace(opt.ConversationKey)
	c.enterpriseAuthMu.Lock()
	if c.enterpriseSessions == nil {
		c.enterpriseSessions = make(map[string]string)
	}
	if key != "" {
		c.enterpriseSessions[key] = session
		c.enterpriseSessionKey = key
		if c.enterprisePendingKey == key {
			c.enterprisePendingKey = ""
		}
	}
	c.enterpriseAuthMu.Unlock()

	if key == "" {
		c.Logf("Enterprise: continuing in assistant session %s (reused for every turn of this conversation)", shortSessionName(session))
		return
	}
	c.Logf("Enterprise: continuing in assistant session %s for conversation %s", shortSessionName(session), key)
}

// clearEnterpriseSession forgets the cached assistant session. The tenant can
// drop a session at any time (expiry, engine rebuild, a different backend pod),
// and a stale id must never fail an otherwise healthy request.
func (c *Client) clearEnterpriseSession() {
	c.enterpriseAuthMu.Lock()
	c.enterpriseSession = ""
	c.enterpriseAuthMu.Unlock()
}

// shortSessionName keeps session logs to one readable line.
func shortSessionName(session string) string {
	s := strings.TrimSpace(session)
	if s == "" {
		return "<none>"
	}
	if i := strings.LastIndex(s, "/"); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// fetchEnterpriseXSRF asks the GE portal for the HMAC key used to sign JWTs.
func (c *Client) fetchEnterpriseXSRF(csesidx string) (xsrfToken, keyID string, err error) {
	origin := c.enterpriseOrigin()
	endpoint := fmt.Sprintf("%s/auth/getoxsrf?csesidx=%s", origin, url.QueryEscape(csesidx))

	req, reqErr := http.NewRequest(http.MethodGet, endpoint, nil)
	if reqErr != nil {
		return "", "", reqErr
	}
	cookieInfo, _ := c.Cookies.Load()
	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	req.Header.Set("Cookie", cookieInfo.Cookie)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("Accept", "*/*")

	resp, doErr := c.HTTP.Do(req)
	if doErr != nil {
		return "", "", &UpstreamError{Kind: "transport", Msg: fmt.Sprintf("Gemini Enterprise XSRF fetch failed: %v", doErr)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	body := strings.TrimSpace(string(raw))
	body = strings.TrimPrefix(body, ")]}'")

	if resp.StatusCode != http.StatusOK {
		return "", "", &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "http",
			Msg: fmt.Sprintf("Gemini Enterprise rejected the XSRF request (HTTP %d) — the session cookies may be expired or csesidx=%q is wrong: %s",
				resp.StatusCode, csesidx, summarizeBody([]byte(body))),
		}
	}

	var parsed struct {
		XSRFToken string `json:"xsrfToken"`
		KeyID     string `json:"keyId"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return "", "", fmt.Errorf("could not parse the Gemini Enterprise XSRF response: %s", summarizeBody([]byte(body)))
	}
	if parsed.XSRFToken == "" || parsed.KeyID == "" {
		return "", "", fmt.Errorf("Gemini Enterprise XSRF response has no xsrfToken/keyId: %s", summarizeBody([]byte(body)))
	}
	return parsed.XSRFToken, parsed.KeyID, nil
}

// mintEnterpriseJWT signs the widget JWT with the XSRF key (HS256).
func mintEnterpriseJWT(xsrfToken, keyID, issuer, audience, csesidx string, now time.Time) (string, error) {
	key, err := decodeBase64URL(xsrfToken)
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("Gemini Enterprise xsrfToken is not a usable HMAC key: %v", err)
	}
	header := map[string]any{"alg": "HS256", "typ": "JWT", "kid": keyID}
	headerJSON, _ := json.Marshal(header)
	nowTS := now.Unix()
	payload := map[string]any{
		"iss": issuer,
		"aud": audience,
		"sub": "csesidx/" + csesidx,
		"iat": nowTS,
		"exp": nowTS + int64(enterpriseJWTTTL.Seconds()),
		"nbf": nowTS,
	}
	payloadJSON, _ := json.Marshal(payload)

	signing := encodeBase64URL(headerJSON) + "." + encodeBase64URL(payloadJSON)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signing))
	return signing + "." + encodeBase64URL(mac.Sum(nil)), nil
}

func encodeBase64URL(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeBase64URL(data string) ([]byte, error) {
	trimmed := strings.TrimSpace(data)
	if decoded, err := base64.RawURLEncoding.DecodeString(trimmed); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(trimmed)
}

// renderEnterpriseJWTPayload is a test/verification helper: it returns the
// decoded claims of a JWT minted by mintEnterpriseJWT.
func renderEnterpriseJWTPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT: %q", token)
	}
	raw, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// widgetAdditionalParams matches the app's own payload; the widget rejects some
// tenants' requests without it.
func widgetAdditionalParams() map[string]string {
	return map[string]string{"token": "-", "origin": "ORIGIN_UNSPECIFIED"}
}

var errNoCsesidx = fmt.Errorf("no csesidx configured")

// CanUploadEnterpriseFiles reports whether a context-file upload transport is
// resolvable: a captured upload template, a configured upload endpoint, an
// endpoint to derive one from, or the widget JWT flow (which can call
// widgetAddContextFile directly once a csesidx is known).
func (c *Client) CanUploadEnterpriseFiles() bool {
	if tpl := LoadDiscoveryUploadTemplate(c.Cfg.CookieFile); tpl != nil && strings.TrimSpace(tpl.URL) != "" {
		return true
	}
	if strings.TrimSpace(c.Cfg.DiscoveryEngineUploadEndpoint) != "" {
		return true
	}
	if strings.TrimSpace(c.Cfg.DiscoveryEngineEndpoint) != "" {
		return true
	}
	return c.HasEnterpriseWidgetAuth()
}

// HasEnterpriseWidgetAuth reports whether the widget JWT transport is available
// (a csesidx has been configured or captured).
func (c *Client) HasEnterpriseWidgetAuth() bool {
	return c.enterpriseCSESIDX() != ""
}

// EnterpriseWidgetAuthStatus describes the widget auth transport for logs.
func (c *Client) EnterpriseWidgetAuthStatus() string {
	csesidx := c.enterpriseCSESIDX()
	if csesidx == "" {
		return ""
	}
	return fmt.Sprintf("widget JWT (csesidx=%s, location=%s)", csesidx, c.enterpriseWidgetLocation())
}

// ensureEnterpriseSession returns a reusable session resource, creating one with
// widgetCreateSession when no session is configured or captured. Every widget
// call (context-file upload + streamAssist) must reference the same session.
func (c *Client) ensureEnterpriseSession(configID string) (string, error) {
	if session := c.enterpriseSessionName(); session != "" && !strings.HasSuffix(session, "/-") {
		return session, nil
	}
	c.enterpriseAuthMu.Lock()
	if cached := c.enterpriseSession; cached != "" {
		c.enterpriseAuthMu.Unlock()
		return cached, nil
	}
	c.enterpriseAuthMu.Unlock()

	token, tokenErr := c.enterpriseBearer()
	usingBearer := tokenErr == nil
	location := c.enterpriseWidgetLocation()
	endpoint := ""
	if usingBearer {
		endpoint = contentWidgetEndpoint(location, "widgetCreateSession")
	} else {
		// Cookie-authenticated widget calls can create a session too: the app's
		// own request carries nothing but cookies and the ?key= parameter.
		for _, base := range []string{c.Cfg.DiscoveryEngineEndpoint, c.Cfg.DiscoveryEngineUploadEndpoint} {
			if strings.TrimSpace(base) == "" {
				continue
			}
			if swapped := swapWidgetMethod(base, "widgetCreateSession"); swapped != "" {
				endpoint = swapped
				break
			}
		}
		if endpoint == "" {
			return "", tokenErr
		}
	}
	payload := map[string]any{
		"configId":         configID,
		"additionalParams": widgetAdditionalParams(),
		"createSessionRequest": map[string]any{
			"session": map[string]string{"name": "-", "displayName": ""},
		},
	}
	body, _ := json.Marshal(payload)

	req, reqErr := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if reqErr != nil {
		return "", reqErr
	}
	if usingBearer {
		c.applyEnterpriseWidgetHeaders(req, token)
	} else {
		req.Header = c.discoveryHeaders(endpoint)
	}

	resp, doErr := c.HTTP.Do(req)
	if doErr != nil {
		return "", &UpstreamError{Kind: "transport", Msg: fmt.Sprintf("widgetCreateSession failed: %v", doErr)}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return "", &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "http",
			Msg: fmt.Sprintf("Gemini Enterprise widgetCreateSession failed (HTTP %d) with configId=%q: %s",
				resp.StatusCode, configID, summarizeBody(raw)),
		}
	}
	session := parseCreateSessionName(raw)
	if session == "" {
		return "", fmt.Errorf("widgetCreateSession returned no session name: %s", summarizeBody(raw))
	}
	c.enterpriseAuthMu.Lock()
	c.enterpriseSession = session
	c.enterpriseAuthMu.Unlock()
	c.Logf("Gemini Enterprise: created assistant session %s (configId=%s)", session, configID)
	return session, nil
}

// applyEnterpriseWidgetHeaders sets the header set the widget API expects for a
// Bearer-authenticated call (no consumer cookies are sent cross-site).
func (c *Client) applyEnterpriseWidgetHeaders(req *http.Request, bearer string) {
	origin := c.enterpriseOrigin()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("X-Goog-Api-Client", "gemini-enterprise-classic/1.0.0")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36")
}

// EnterpriseModelID resolves the Discovery Engine modelId for a requested model.
// Gemini Enterprise runs whatever model the assistant is configured with unless
// the request carries assistGenerationConfig.modelId, so a client asking for
// "gemini-4-hydrogen" would otherwise silently get the tenant default.
//
// Resolution order:
//  1. discovery_engine_models[<requested>] (case-insensitive)
//  2. the requested name itself when it already looks like a Discovery Engine
//     id ("gemini-2.5-pro", "gemini-2.5-flash", ...), so clients can target a
//     tenant model directly
//  3. discovery_engine_default_model_id
func (c *Client) EnterpriseModelID(requested string) string {
	id, _ := c.EnterpriseModelIDSource(requested)
	return id
}

// Discovery Engine modelId resolution sources, reported in logs, response
// headers and GET /v1/model-info so "why is it answering as auto?" is answerable.
const (
	ModelIDSourceMapping       = "mapping"            // discovery_engine_models hit
	ModelIDSourcePassthrough   = "passthrough"        // caller already used a tenant id
	ModelIDSourceConfigured    = "configured-default" // discovery_engine_default_model_id
	ModelIDSourceLearned       = "learned"            // verified by a probe, cached per alias
	ModelIDSourceCaptured      = "captured"           // learned from the tab's captured request
	ModelIDSourceTenantDefault = "tenant-default"     // nothing selected: tenant decides
)

// EnterpriseModelIDSource resolves the modelId AND reports where it came from.
func (c *Client) EnterpriseModelIDSource(requested string) (string, string) {
	name := strings.TrimSpace(requested)
	lower := strings.ToLower(name)
	if key, value, ok := c.lookupDiscoveryModel(lower); ok {
		_ = key
		return value, ModelIDSourceMapping
	}
	if looksLikeDiscoveryEngineModel(lower) {
		return name, ModelIDSourcePassthrough
	}
	if def := strings.TrimSpace(c.Cfg.DiscoveryEngineDefaultModelID); def != "" {
		return def, ModelIDSourceConfigured
	}
	// A probe-verified id beats the generic captured id: it is known to be
	// accepted for THIS alias and selectable in direct mode.
	if learned := c.learnedModelID(name); learned != "" {
		return learned, ModelIDSourceLearned
	}
	// The tab's own captured streamAssist request carries a modelId the tenant
	// accepts. Using it is strictly better than sending nothing: it is a real
	// tenant model, and if the tab was showing the model the user wants, it is
	// exactly that model.
	if tpl := LoadDiscoveryTemplate(c.Cfg.CookieFile); tpl != nil {
		if id := modelIDFromBody(tpl.Body); id != "" {
			return id, ModelIDSourceCaptured
		}
	}
	return "", ModelIDSourceTenantDefault
}

// modelIDFromBody extracts assistGenerationConfig.modelId from a captured
// streamAssist body.
func modelIDFromBody(body string) string {
	re := regexp.MustCompile(`"assistGenerationConfig"\s*:\s*\{[^{}]*"modelId"\s*:\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(body); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// lookupDiscoveryModel finds a discovery_engine_models entry case-insensitively
// and reports the configured key so diagnostics can name it.
func (c *Client) lookupDiscoveryModel(lower string) (string, string, bool) {
	for key, value := range c.Cfg.DiscoveryEngineModels {
		if strings.ToLower(strings.TrimSpace(key)) == lower {
			if v := strings.TrimSpace(value); v != "" {
				return strings.TrimSpace(key), v, true
			}
		}
	}
	return "", "", false
}

// looksLikeDiscoveryEngineModel reports whether a name is a plain upstream
// model id rather than one of this server's aliases (neon/hydrogen/argon/carbon).
func looksLikeDiscoveryEngineModel(name string) bool {
	if name == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "auto", "gemini-auto", "neon", "hydrogen", "argon", "carbon", "gemini-4",
		"gemini-4-neon", "gemini-4-neon-thinking", "gemini-4-argon", "gemini-4-hydrogen", "gemini-4-carbon":
		return false
	}
	return strings.HasPrefix(name, "gemini-") && !strings.Contains(name, "-4-")
}

// enterpriseAssistGenerationConfig renders the optional assistGenerationConfig
// fragment for the widget request body.
func (c *Client) enterpriseAssistGenerationConfig(requested string) string {
	modelID := c.EnterpriseModelID(requested)
	if modelID == "" {
		return ""
	}
	return fmt.Sprintf(`"assistGenerationConfig":{"modelId":%q},`, modelID)
}

// injectTemplateModelID rewrites (or adds) assistGenerationConfig.modelId in a
// captured streamAssist body so replayed templates honour the model the client
// asked for instead of whatever the browser app had selected.
func injectTemplateModelID(body, modelID string) string {
	if strings.TrimSpace(modelID) == "" {
		return body
	}
	idJSON, err := json.Marshal(modelID)
	if err != nil {
		return body
	}
	if re := regexp.MustCompile(`"assistGenerationConfig"\s*:\s*\{[^{}]*\}`); re.MatchString(body) {
		return re.ReplaceAllStringFunc(body, func(match string) string {
			if inner := regexp.MustCompile(`"modelId"\s*:\s*"[^"]*"`); inner.MatchString(match) {
				return inner.ReplaceAllString(match, `"modelId":`+string(idJSON))
			}
			return strings.TrimSuffix(match, "}") + `,"modelId":` + string(idJSON) + "}"
		})
	}
	// Insert next to the streamAssistRequest object so the field lands where the
	// widget expects it.
	needle := `"streamAssistRequest":{`
	idx := strings.Index(body, needle)
	if idx < 0 {
		return body
	}
	insertAt := idx + len(needle)
	return body[:insertAt] + `"assistGenerationConfig":{"modelId":` + string(idJSON) + "}," + body[insertAt:]
}

// parseCreateSessionName extracts the session resource from a widgetCreateSession
// response. The widget wraps it as createSessionResponse.session.name, while some
// tenants answer with a bare session object or a plain string.
func parseCreateSessionName(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	nameFrom := func(v any) string {
		switch t := v.(type) {
		case string:
			return strings.TrimSpace(t)
		case map[string]any:
			if name, ok := t["name"].(string); ok {
				return strings.TrimSpace(name)
			}
		}
		return ""
	}
	if name := nameFrom(payload["session"]); name != "" {
		return name
	}
	if inner, ok := payload["createSessionResponse"].(map[string]any); ok {
		if name := nameFrom(inner["session"]); name != "" {
			return name
		}
		if name := nameFrom(inner["sessionName"]); name != "" {
			return name
		}
	}
	if name := nameFrom(payload["sessionName"]); name != "" {
		return name
	}
	return ""
}
