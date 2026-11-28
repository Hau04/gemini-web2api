package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Gemini Enterprise (vertexaisearch.cloud.google.com) speaks the Discovery
// Engine streamAssist API instead of the Bard web RPC, so direct cookie mode
// cannot use batchexecute — but it CAN replay the app's own streamAssist
// request using harvested cookies plus a request template captured by the
// extension's net-hook. This unlocks server-side operation with the browser
// closed on Enterprise accounts.

// DiscoveryPromptPlaceholder marks where the user prompt goes in a captured
// request body. The extension substitutes the submitted prompt for this marker
// at capture time; replay injects the new prompt in its place.
const DiscoveryPromptPlaceholder = "__W2A_PROMPT__"

// DiscoveryTemplate is one captured streamAssist request from the browser tab.
type DiscoveryTemplate struct {
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	PromptHint string            `json:"prompt_hint,omitempty"`
	CapturedAt time.Time         `json:"captured_at,omitempty"`
}

// discoveryTemplatePath mirrors the cookie file location: the server persists
// templates next to the cookie file, and the client reads them back from the
// same place.
func discoveryTemplatePath(cookieFile string) string {
	if strings.TrimSpace(cookieFile) == "" {
		cookieFile = "cookie.json"
	}
	return filepath.Join(filepath.Dir(cookieFile), "discovery_template.json")
}

// LoadDiscoveryTemplate reads a persisted request template (nil if absent).
func LoadDiscoveryTemplate(cookieFile string) *DiscoveryTemplate {
	data, err := os.ReadFile(discoveryTemplatePath(cookieFile))
	if err != nil {
		return nil
	}
	var tpl DiscoveryTemplate
	if err := json.Unmarshal(data, &tpl); err != nil {
		return nil
	}
	if strings.TrimSpace(tpl.URL) == "" || strings.TrimSpace(tpl.Body) == "" {
		return nil
	}
	return &tpl
}

// HasDiscoveryTemplate reports whether a usable replay template is persisted.
func (c *Client) HasDiscoveryTemplate() bool {
	return LoadDiscoveryTemplate(c.Cfg.CookieFile) != nil
}

// SaveDiscoveryTemplate persists a captured template next to the cookie file.
func SaveDiscoveryTemplate(cookieFile string, tpl *DiscoveryTemplate) error {
	data, err := json.MarshalIndent(tpl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(discoveryTemplatePath(cookieFile), data, 0o600)
}

// SubstitutePrompt returns the request body with the new prompt spliced in.
// The captured body carries the prompt either as the placeholder marker or,
// for templates captured without correlation, verbatim as PromptHint.
func (t *DiscoveryTemplate) SubstitutePrompt(prompt string) (string, error) {
	escaped := jsonEscapeInner(prompt)
	if strings.Contains(t.Body, DiscoveryPromptPlaceholder) {
		return strings.Replace(t.Body, DiscoveryPromptPlaceholder, escaped, 1), nil
	}
	hint := strings.TrimSpace(t.PromptHint)
	if hint == "" {
		return "", fmt.Errorf("captured request template has no prompt marker — send one prompt through the open Gemini tab to refresh the template")
	}
	// Replace the LAST occurrence: in bodies with inline history the newest user
	// message comes last.
	if idx := strings.LastIndex(t.Body, jsonEscapeInner(hint)); idx >= 0 {
		return t.Body[:idx] + escaped + t.Body[idx+len(jsonEscapeInner(hint)):], nil
	}
	if idx := strings.LastIndex(t.Body, hint); idx >= 0 {
		return t.Body[:idx] + escaped + t.Body[idx+len(hint):], nil
	}
	return "", fmt.Errorf("captured prompt text not found in template body — send one prompt through the open Gemini tab to refresh the template")
}

// jsonEscapeInner returns the JSON string-body encoding of s (no surrounding
// quotes), matching how the prompt is embedded inside the request body.
func jsonEscapeInner(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return string(b[1 : len(b)-1])
}

// ---------------------------------------------------------------------------
// Streaming response parser (port of extension/net-hook.js discovery_json
// processing: balanced-brace JSON extraction + streamAssist answer walking).
// ---------------------------------------------------------------------------

var (
	reDiscoveryRoutingSection = regexp.MustCompile(`(?:^|\n)[ \t]*(?:I am (?:transferring your request|checking the available tools and integrations)[^\n]*\r?\n)?[ \t]*\[\[\/?section(?::[^\]]*)?\]\][ \t]*\r?\n?`)
	reDiscoveryStandalone     = regexp.MustCompile(`(?i)^[ \t]*I am (?:transferring your request to [^\n]+|checking the available tools and integrations in this environment[^\n]*)[ \t]*$`)
)

func cleanDiscoveryText(text string) string {
	if text == "" {
		return ""
	}
	if reDiscoveryStandalone.MatchString(strings.TrimSpace(text)) {
		return ""
	}
	cleaned := reDiscoveryRoutingSection.ReplaceAllString(text, "")
	if cleaned != text {
		return strings.TrimLeft(cleaned, "\n")
	}
	return text
}

// discoveryParser incrementally extracts top-level JSON objects from a mixed
// streaming body (SSE data lines, NDJSON, JSON arrays, anti-XSSI prefixes) and
// feeds each to the answer walker.
type discoveryParser struct {
	buf     string
	scanPos int
	depth   int
	inStr   bool
	esc     bool
	objAt   int
	onObj   func(map[string]any)
}

func (p *discoveryParser) feed(chunk string) {
	if chunk == "" {
		return
	}
	p.buf += chunk
	for i := p.scanPos; i < len(p.buf); i++ {
		ch := p.buf[i]
		if p.depth == 0 {
			if ch == '{' {
				p.objAt = i
				p.depth = 1
				p.inStr = false
				p.esc = false
			}
			continue
		}
		if p.inStr {
			if p.esc {
				p.esc = false
			} else if ch == '\\' {
				p.esc = true
			} else if ch == '"' {
				p.inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			p.inStr = true
		case '{':
			p.depth++
		case '}':
			p.depth--
			if p.depth == 0 && p.objAt >= 0 {
				raw := p.buf[p.objAt : i+1]
				p.objAt = -1
				var obj map[string]any
				if err := json.Unmarshal([]byte(raw), &obj); err == nil && p.onObj != nil {
					p.onObj(obj)
				}
			}
		}
	}
	if p.depth == 0 {
		p.buf = ""
		p.scanPos = 0
	} else if p.objAt > 0 {
		p.buf = p.buf[p.objAt:]
		p.scanPos = len(p.buf)
		p.objAt = 0
	} else {
		p.scanPos = len(p.buf)
	}
}

// discoveryStream accumulates the answer text with the same incremental/cumulative
// dedupe semantics as the extension's stream processor.
type discoveryStream struct {
	accumulated string
	hasText     bool
	thinking    bool
	failed      bool
	failMsg     string
	emit        func(delta string) error
	emitErr     error
	onActivity  func()
}

func (s *discoveryStream) notifyActivity() {
	if !s.failed && s.onActivity != nil {
		s.onActivity()
	}
}

func (s *discoveryStream) emitDelta(delta string) error {
	if delta == "" || s.emit == nil {
		return nil
	}
	if s.emitErr == nil {
		s.emitErr = s.emit(delta)
	}
	return s.emitErr
}

func (s *discoveryStream) incremental(chunkText string, isFinal bool) {
	if chunkText == "" {
		return
	}
	s.notifyActivity()
	delta := chunkText
	if len(s.accumulated) > 0 {
		if isFinal && chunkText == s.accumulated {
			return
		}
		if (isFinal || len(s.accumulated) >= 16) && strings.HasPrefix(chunkText, s.accumulated) {
			delta = chunkText[len(s.accumulated):]
			if delta == "" {
				return
			}
		}
	}
	s.accumulated += delta
	s.hasText = true
	s.thinking = false
	_ = s.emitDelta(delta)
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// processObject walks one decoded JSON object (StreamAssistResponse shape).
func (s *discoveryStream) processObject(obj map[string]any) {
	if obj == nil {
		return
	}
	if errObj := asMap(obj["error"]); errObj != nil {
		if msg := strings.TrimSpace(asString(errObj["message"])); msg != "" {
			s.failed = true
			s.failMsg = strings.Trim(msg, "")
			if len(s.failMsg) > 240 {
				s.failMsg = s.failMsg[:240]
			}
		}
	}

	sar := asMap(obj["streamAssistResponse"])
	if sar == nil {
		sar = asMap(obj["stream_assist_response"])
	}
	if sar == nil {
		sar = obj
	}
	if connErr := asMap(sar["connectorAuthError"]); connErr != nil {
		s.failMsg = "Gemini Enterprise requested connector authorization (e.g., Workspace/Gmail OAuth)"
	} else if connErr := asMap(sar["connector_auth_error"]); connErr != nil {
		s.failMsg = "Gemini Enterprise requested connector authorization (e.g., Workspace/Gmail OAuth)"
	}

	answer := asMap(sar["answer"])
	if answer == nil {
		if aqr := asMap(sar["answerQueryResponse"]); aqr != nil {
			answer = asMap(aqr["answer"])
		}
	}
	if answer != nil {
		s.processAnswer(answer)
		return
	}

	// ConverseConversation: reply.summary.summaryText
	if reply := asMap(obj["reply"]); reply != nil {
		if summary := asMap(reply["summary"]); summary != nil {
			if txt := asString(summary["summaryText"]); txt != "" {
				s.incremental(cleanDiscoveryText(txt), true)
			}
		}
	}
}

func (s *discoveryStream) processAnswer(answer map[string]any) {
	state := strings.ToUpper(asString(answer["state"]))
	isFinal := state == "SUCCEEDED" || state == "FAILED" || state == "SKIPPED" || asString(answer["name"]) != ""

	if replies, ok := answer["replies"].([]any); ok && len(replies) > 0 {
		chunkText := ""
		sawThought := false
		for _, r := range replies {
			reply := asMap(r)
			if reply == nil {
				continue
			}
			gc := asMap(reply["groundedContent"])
			if gc == nil {
				gc = asMap(reply["grounded_content"])
			}
			content := asMap(gc["content"])
			if content == nil {
				continue
			}
			if strings.ToLower(asString(content["role"])) == "user" {
				continue
			}
			if content["thought"] == true || gc["thought"] == true || reply["thought"] == true {
				sawThought = true
				continue
			}
			if txt := asString(content["text"]); txt != "" {
				if cleaned := cleanDiscoveryText(txt); cleaned != "" {
					chunkText += cleaned
				} else {
					sawThought = true
				}
			} else if parts, ok := content["parts"].([]any); ok {
				for _, pv := range parts {
					part := asMap(pv)
					if part == nil {
						continue
					}
					if part["thought"] == true {
						sawThought = true
						continue
					}
					if txt := asString(part["text"]); txt != "" {
						if cleaned := cleanDiscoveryText(txt); cleaned != "" {
							chunkText += cleaned
						} else {
							sawThought = true
						}
					}
				}
			}
		}
		if sawThought && chunkText == "" {
			s.thinking = true
			s.notifyActivity()
		}
		if chunkText != "" {
			s.incremental(chunkText, isFinal)
		}
	}

	at := asString(answer["answerText"])
	if at == "" {
		at = asString(answer["answer_text"])
	}
	if at != "" {
		s.incremental(cleanDiscoveryText(at), true)
	}

	if state == "FAILED" {
		s.failed = true
	}
}

// ---------------------------------------------------------------------------
// Replay
// ---------------------------------------------------------------------------

// replayAuthCandidates builds the Authorization header attempts: the captured
// value first (valid shortly after capture), then fresh SAPISIDHASH values for
// the plausible scopes.
func replayAuthCandidates(tpl *DiscoveryTemplate, sapisid string) []string {
	captured := ""
	for k, v := range tpl.Headers {
		if strings.EqualFold(k, "Authorization") {
			captured = strings.TrimSpace(v)
		}
	}
	candidates := []string{}
	if captured != "" {
		candidates = append(candidates, captured)
	}
	if sapisid == "" || !strings.HasPrefix(captured, "SAPISIDHASH") {
		return candidates
	}
	origin := tpl.URL
	if u, err := url.Parse(tpl.URL); err == nil && u.Scheme != "" && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	for _, scope := range []string{origin, "https://www.googleapis.com/auth/cloud-platform"} {
		if h := SAPISIDHashWithScope(sapisid, scope); h != "" {
			candidates = append(candidates, h)
		}
	}
	return candidates
}

// generateViaDiscovery replays the captured streamAssist request with the new
// prompt and returns the assembled answer text.
func (c *Client) generateViaDiscovery(prompt string, fileIDs []string, session string, requestedModel string) (string, error) {
	var sb strings.Builder
	err := c.generateViaDiscoveryStream(prompt, fileIDs, session, requestedModel, nil, func(delta string) error {
		sb.WriteString(delta)
		return nil
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

func (c *Client) generateViaDiscoveryStream(prompt string, fileIDs []string, session string, requestedModel string, media func(string), emit func(string) error) error {
	return c.generateViaDiscoveryStreamWithContext(context.Background(), prompt, fileIDs, session, requestedModel, media, emit)
}

func (c *Client) generateViaDiscoveryStreamWithContext(parentCtx context.Context, prompt string, fileIDs []string, session string, requestedModel string, media func(string), emit func(string) error) error {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	tpl := LoadDiscoveryTemplate(c.Cfg.CookieFile)
	if tpl == nil {
		return &UpstreamError{
			Status: http.StatusNotFound,
			Kind:   "unsupported",
			Msg:    "direct cookie mode is unavailable on this Gemini Enterprise origin and no request template is captured yet — keep the Gemini tab open (browser bridge), or send one prompt through the open tab to capture a template",
		}
	}
	body, err := tpl.SubstitutePrompt(prompt)
	if err != nil {
		return &UpstreamError{Status: http.StatusBadRequest, Kind: "unsupported", Msg: err.Error()}
	}
	if len(fileIDs) > 0 {
		body, err = InjectAttachmentContext(body, fileIDs, session)
		if err != nil {
			return &UpstreamError{Status: http.StatusBadGateway, Kind: "unsupported", Msg: fmt.Sprintf("could not attach files to the captured streamAssist template: %v", err)}
		}
	}
	// The captured template carries whatever model the browser app had selected;
	// override it so the client's model choice actually reaches the tenant.
	if modelID := c.EnterpriseModelID(requestedModel); modelID != "" {
		if patched := injectTemplateModelID(body, modelID); patched != body {
			c.Logf("Enterprise: template replay using Discovery Engine modelId %q (requested %q)", modelID, requestedModel)
			body = patched
		}
	}

	cookieInfo, _ := c.Cookies.Load()
	auths := replayAuthCandidates(tpl, cookieInfo.SAPISID)

	var lastErr error
	for i, auth := range auths {
		if err := parentCtx.Err(); err != nil {
			return err
		}
		reqCtx, touch, stopAttempt := withActivityTimeout(parentCtx, c.Cfg.RequestTimeoutSec)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, tpl.URL, strings.NewReader(body))
		if err != nil {
			stopAttempt()
			return err
		}
		for k, v := range tpl.Headers {
			switch strings.ToLower(k) {
			case "authorization", "cookie", "content-length", "host":
				continue
			}
			req.Header.Set(k, v)
		}
		if cookieInfo.Cookie != "" {
			req.Header.Set("Cookie", cookieInfo.Cookie)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		serverToken := c.Cfg.DiscoveryEngineServerToken
		if serverToken == "" {
			serverToken = cookieInfo.ServerToken
		}
		if serverToken != "" {
			req.Header.Set("X-Server-Token", serverToken)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			stopAttempt()
			if parentCtx.Err() != nil {
				return parentCtx.Err()
			}
			lastErr = &UpstreamError{Kind: "transport", Msg: err.Error()}
			break
		}
		touch()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			stopAttempt()
			c.Logf("Discovery Engine streamAssist replay rejected (HTTP %d, auth attempt %d/%d): %s",
				resp.StatusCode, i+1, len(auths), strings.TrimSpace(string(bodyText)))
			lastErr = &UpstreamError{
				Status: resp.StatusCode,
				Kind:   "http",
				Msg:    fmt.Sprintf("streamAssist replay rejected (HTTP %d, auth attempt %d/%d) — the captured template's authorization may have expired; send one prompt through the open Gemini tab to refresh it: %s", resp.StatusCode, i+1, len(auths), strings.TrimSpace(string(bodyText))),
			}
			continue // try next auth candidate
		}
		if resp.StatusCode != http.StatusOK {
			bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			stopAttempt()
			return &UpstreamError{
				Status: resp.StatusCode,
				Kind:   "http",
				Msg:    fmt.Sprintf("streamAssist replay failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(bodyText))),
			}
		}

		stream := &discoveryStream{emit: emit}
		parser := &discoveryParser{objAt: -1, onObj: stream.processObject}
		buf := make([]byte, 32*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				touch()
				if media != nil {
					media(string(buf[:n]))
				}
				parser.feed(string(buf[:n]))
			}
			if rerr != nil {
				if rerr != io.EOF {
					_ = resp.Body.Close()
					stopAttempt()
					if parentCtx.Err() != nil {
						return parentCtx.Err()
					}
					return &UpstreamError{Kind: "transport", Msg: rerr.Error()}
				}
				break
			}
		}
		_ = resp.Body.Close()
		stopAttempt()

		if stream.failed {
			msg := stream.failMsg
			if msg == "" {
				msg = "streamAssist answer state FAILED"
			}
			return &UpstreamError{Status: http.StatusBadGateway, Kind: "http", Msg: msg}
		}
		if !stream.hasText {
			return &UpstreamError{Status: http.StatusBadGateway, Kind: "http", Msg: "streamAssist replay returned no answer text"}
		}
		return stream.emitErr
	}
	if lastErr == nil {
		lastErr = &UpstreamError{Status: http.StatusUnauthorized, Kind: "http", Msg: "streamAssist replay has no usable authorization header"}
	}
	var ue *UpstreamError
	if errors.As(lastErr, &ue) && (ue.Status == http.StatusUnauthorized || ue.Status == http.StatusForbidden) && c.OnAuthError != nil {
		c.OnAuthError(lastErr.Error())
	}
	return lastErr
}

// generateViaDiscoveryDirect calls the Discovery Engine streamAssist endpoint
// directly using session cookies, without needing a captured request template.
// This works on Gemini Enterprise origins when the user has configured the
// streamAssist endpoint URL in the server config.
func (c *Client) generateViaDiscoveryDirect(prompt string, fileIDs []string, session string, requestedModel string) (string, error) {
	var sb strings.Builder
	err := c.generateViaDiscoveryDirectStream(prompt, fileIDs, session, ConversationInfo{}, requestedModel, nil, func(delta string) error {
		sb.WriteString(delta)
		return nil
	})
	if err != nil {
		return "", err
	}
	return sb.String(), nil
}

func (c *Client) generateViaDiscoveryDirectStream(prompt string, fileIDs []string, session string, conv ConversationInfo, requestedModel string, media func(string), emit func(string) error) error {
	configID := strings.TrimSpace(c.enterpriseConfigID(nil))
	if configID == "" {
		return &UpstreamError{
			Status: http.StatusBadRequest,
			Kind:   "config",
			Msg: "this Gemini Enterprise origin needs the widget config id: set \"discovery_engine_config_id\" " +
				"(the configId the Gemini Enterprise app sends to widgetStreamAssist) or attach one file in the tab " +
				"so the extension captures it",
		}
	}

	// Preferred transport: mint a widget JWT from the portal XSRF token and talk
	// to the regional content-* Discovery Engine host. That is what the Gemini
	// Enterprise app itself does; the consumer SAPISIDHASH + ?key= form is only a
	// legacy fallback when no csesidx has been captured yet.
	bearer, bearerErr := c.enterpriseBearer()
	usingBearer := bearerErr == nil
	endpoint := ""
	if usingBearer {
		endpoint = contentWidgetEndpoint(c.enterpriseWidgetLocation(), "widgetStreamAssist")
		if pinned := strings.TrimSpace(c.Cfg.DiscoveryEngineEndpoint); pinned != "" && strings.Contains(pinned, "content-") {
			endpoint = pinned
		}
	} else if strings.TrimSpace(c.Cfg.DiscoveryEngineEndpoint) != "" {
		endpoint = c.Cfg.DiscoveryEngineEndpoint
		if !errors.Is(bearerErr, errNoCsesidx) {
			c.Logf("Gemini Enterprise: widget JWT unavailable (%v) — falling back to the cookie-authenticated endpoint", bearerErr)
		}
	} else {
		return &UpstreamError{
			Status: http.StatusNotFound,
			Kind:   "unsupported",
			Msg:    "direct cookie mode is unavailable on this Gemini Enterprise origin: no Discovery Engine streamAssist endpoint configured and no request template captured — set discovery_engine_endpoint in config.json, or send one prompt through the open Gemini tab to capture a template",
		}
	}

	// One conversation per client chat: a pinned session wins, otherwise the
	// session owned by this conversation is reused (or a fresh one is opened when
	// the client started a new chat). Cookie-authenticated requests need this just
	// as much as bearer ones — without it the tenant answers each message in a
	// brand-new conversation.
	parentCtx := conv.context()
	convOpts := ModelOptions{
		ConversationKey:        conv.Key,
		ConversationHasHistory: conv.HasHistory,
		Context:                parentCtx,
	}
	alreadyResolvedSession := strings.TrimSpace(session) != "" && !strings.HasSuffix(strings.TrimSpace(session), "/-")
	session = c.reuseOrCreateEnterpriseSession(session, convOpts)
	if session != "" {
		if alreadyResolvedSession {
			c.rememberEnterpriseSession(session, convOpts)
		} else {
			c.recordEnterpriseSession(session, convOpts)
		}
	}
	// Safety guard: if prompt was delta-encoded against a specific session, never
	// send the bare delta into a different or empty session.
	if conv.FullPrompt != "" && (session == "" || (conv.DeltaBaseSession != "" && !sameEnterpriseSession(session, conv.DeltaBaseSession))) {
		prompt = conv.FullPrompt
		conv.FullPrompt = ""
	}
	// A prompt the tenant refuses as too large fails the whole turn, so the
	// ceiling learned from an earlier refusal is applied before sending.
	prompt = c.fitPrompt(prompt)

	fileIDsJSON := "[]"
	if len(fileIDs) > 0 {
		encoded := make([]string, 0, len(fileIDs))
		for _, id := range fileIDs {
			encoded = append(encoded, fmt.Sprintf("%q", id))
		}
		fileIDsJSON = "[" + strings.Join(encoded, ",") + "]"
	}
	sessionJSON := ""
	if s := strings.TrimSpace(session); s != "" {
		sessionJSON = fmt.Sprintf(`"session":%q,`, s)
	}
	toolsSpec, toolsSource := c.enterpriseToolsSpec()
	c.logToolsSpec(toolsSpec, toolsSource)
	// discovery_engine_tools_spec=none omits the field entirely, so the
	// assistant's own configured actions decide what the model may call.
	toolsSpecField := ""
	if toolsSpec != "" {
		toolsSpecField = `"toolsSpec":` + toolsSpec + ","
	}
	// Without this Gemini Enterprise always answers with the tenant's own
	// default model, regardless of the model the client asked for.
	// With nothing mapped, probe the tenant once per alias: if it accepts the
	// requested name as a model id, cache it and use it from now on. Without this
	// the request carries no model and the tenant answers with its auto model.
	if c.Cfg.DiscoveryEngineProbeModels {
		c.maybeProbeEnterpriseModel(requestedModel)
	}
	modelJSON := ""
	if modelID := c.EnterpriseModelID(requestedModel); modelID != "" {
		modelJSON = fmt.Sprintf(`"assistGenerationConfig":{"modelId":%q},`, modelID)
		if requestedModel != "" && !strings.EqualFold(requestedModel, modelID) {
			c.Logf("Enterprise: requested model %q -> Discovery Engine modelId %q", requestedModel, modelID)
		}
	}
	buildBody := func() string {
		return fmt.Sprintf(
			`{"configId":"%s","additionalParams":{"token":"-","origin":"ORIGIN_UNSPECIFIED"},"streamAssistRequest":{%s%s"query":{"parts":[{"text":"%s"}]},"answerGenerationMode":"NORMAL","assistSkippingMode":"REQUEST_ASSIST","fileIds":%s,"filter":"",%s"languageCode":"en-US","userMetadata":{"timeZone":"UTC"}}}`,
			configID,
			sessionJSON,
			modelJSON,
			jsonEscapeInner(prompt),
			fileIDsJSON,
			toolsSpecField,
		)
	}
	body := buildBody()

	var lastErr error
	authFailed := false
	sessionDropped := false
	promptShrinks := 0
	for attempt := 0; attempt < c.Cfg.RetryAttempts; attempt++ {
		if err := parentCtx.Err(); err != nil {
			return err
		}
		attemptStart := time.Now()
		reqCtx, touch, stopAttempt := withActivityTimeout(parentCtx, c.Cfg.RequestTimeoutSec)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, strings.NewReader(body))
		if err != nil {
			stopAttempt()
			return err
		}
		if usingBearer {
			c.applyEnterpriseWidgetHeaders(req, bearer)
		} else {
			req.Header = c.discoveryHeaders(endpoint)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			stopAttempt()
			elapsedMs := time.Since(attemptStart).Milliseconds()
			if parentCtx.Err() != nil {
				c.Logf("Direct Discovery Engine streamAssist canceled after %dms (client disconnected)", elapsedMs)
				return parentCtx.Err()
			}
			c.Logf("Direct Discovery Engine streamAssist transport error after %dms: %v", elapsedMs, err)
			lastErr = &UpstreamError{Kind: "transport", Msg: err.Error()}
			break
		}
		touch()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			stopAttempt()
			c.resetEnterpriseAuth()
			authFailed = true
			snippet := summarizeBody(bodyText)
			c.Logf("Direct Discovery Engine streamAssist rejected (HTTP %d, attempt %d/%d, bearer=%t) — session cookies or widget JWT refused: %s",
				resp.StatusCode, attempt+1, c.Cfg.RetryAttempts, usingBearer, snippet)
			lastErr = &UpstreamError{
				Status: resp.StatusCode,
				Kind:   "http",
				Msg: fmt.Sprintf("streamAssist direct call rejected (HTTP %d) — the session cookies or the minted widget JWT were refused: %s",
					resp.StatusCode, strings.TrimSpace(string(bodyText))),
			}
			if attempt < c.Cfg.RetryAttempts-1 {
				if err := sleepWithContext(parentCtx, time.Duration(c.Cfg.RetryDelaySec)*time.Second); err != nil {
					return err
				}
				if usingBearer {
					if fresh, fErr := c.enterpriseBearer(); fErr == nil {
						bearer = fresh
					}
				}
				continue
			}
			if c.OnAuthError != nil {
				c.OnAuthError(lastErr.Error())
			}
			return lastErr
		}
		if resp.StatusCode != http.StatusOK {
			bodyText, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			_ = resp.Body.Close()
			stopAttempt()
			// A session the tenant has forgotten (expiry, engine rebuild, another
			// backend pod) must not fail the turn: drop it, evict it from the
			// conversation map so subsequent turns never switch back to it, retry
			// once without it, and let the next request create a fresh one.
			if !sessionDropped && sessionJSON != "" && resp.StatusCode == http.StatusNotFound &&
				sessionNotFound(bodyText) {
				sessionDropped = true
				c.Logf("Gemini Enterprise: the tenant no longer knows session %s (HTTP 404) — retrying without a session; a fresh one is created for the next turn",
					shortSessionName(session))
				c.evictEnterpriseSession(session, conv.Key)
				if conv.FullPrompt != "" {
					prompt = c.fitPrompt(conv.FullPrompt)
					conv.FullPrompt = ""
				}
				fileIDs = nil
				fileIDsJSON = "[]"
				session = ""
				sessionJSON = ""
				body = buildBody()
				attempt-- // do not spend a retry on a stale session
				continue
			}
			// PROMPT_TOO_LARGE: the tenant's cap is smaller than we knew (or is not
			// known yet). Shrink the prompt, remember the new ceiling, and retry —
			// losing the oldest turns beats losing the whole turn.
			if resp.StatusCode == http.StatusBadRequest && promptTooLarge(bodyText) {
				// The tenant counts the session's own history towards the limit, and
				// it grows every turn: a session that has been talking for a while
				// is too large no matter how small this prompt is. Evicting it from
				// both the active slot and c.enterpriseSessions[conv.Key] keeps the
				// turn alive AND prevents every later turn from switching back to
				// the same full session.
				if !sessionDropped && sessionJSON != "" {
					sessionDropped = true
					c.Logf("Direct Discovery Engine refused the prompt as too large (HTTP 400, %d chars) while a session was attached — the session's accumulated history is what became too large, so retrying in a fresh session instead of trimming the prompt", len(prompt))
					c.evictEnterpriseSession(session, conv.Key)
					if conv.FullPrompt != "" {
						prompt = c.fitPrompt(conv.FullPrompt)
						conv.FullPrompt = ""
					}
					fileIDs = nil
					fileIDsJSON = "[]"
					session = ""
					sessionJSON = ""
					body = buildBody()
					attempt-- // not a transport retry
					continue
				}
				// No session to blame: the prompt itself is over the cap. Shrink it —
				// but only while the prompt is big enough for that to be the reason.
				if promptShrinks < maxPromptShrinkRetries && len(prompt) > promptCeilingFloor {
					promptShrinks++
					limit := len(prompt) * 3 / 4
					c.Logf("Direct Discovery Engine rejected the prompt as too large (%d chars) — retrying with the oldest turns omitted (%d chars)", len(prompt), limit)
					if fitted, trimmed := fitPromptForUpstream(prompt, limit); trimmed {
						c.rememberPromptCeiling(limit)
						prompt = fitted
						body = buildBody()
						attempt-- // a shrink is not a transport retry
						continue
					}
				}
				// A prompt this small is not too large: report the tenant's refusal
				// instead of trimming the conversation down to nothing.
				c.Logf("Direct Discovery Engine refused a %d-char prompt as too large and it cannot be the prompt size — check the tenant's own limits (session history, attachments, model) rather than the request", len(prompt))
				promptShrinks = maxPromptShrinkRetries
			}
			c.Logf("Direct Discovery Engine streamAssist returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyText)))
			return &UpstreamError{
				Status: resp.StatusCode,
				Kind:   "http",
				Msg:    fmt.Sprintf("streamAssist direct call failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(bodyText))),
			}
		}

		var (
			startOnce    sync.Once
			startErr     error
			startErrMu   sync.Mutex
			triggerStart = func() {
				if conv.OnStreamStart == nil {
					return
				}
				startOnce.Do(func() {
					if err := conv.OnStreamStart(); err != nil {
						startErrMu.Lock()
						startErr = err
						startErrMu.Unlock()
						stopAttempt()
					}
				})
			}
		)
		var earlyStartTimer *time.Timer
		if conv.OnStreamStart != nil {
			// Commit SSE headers + keepalives after 2s if the upstream 200 stream is
			// still open and thinking before emitting its first frame; stopped
			// immediately if the stream finishes early (e.g. instant FAILED JSON).
			earlyStartTimer = time.AfterFunc(2*time.Second, triggerStart)
		}

		stream := &discoveryStream{emit: emit, onActivity: triggerStart}
		parser := &discoveryParser{objAt: -1, onObj: stream.processObject}
		buf := make([]byte, 32*1024)
		var rawSniff strings.Builder
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				touch()
				if rawSniff.Len() < 4096 {
					rawSniff.Write(buf[:n])
				}
				if media != nil {
					media(string(buf[:n]))
				}
				parser.feed(string(buf[:n]))
			}
			if rerr != nil {
				if earlyStartTimer != nil {
					earlyStartTimer.Stop()
				}
				if rerr != io.EOF {
					_ = resp.Body.Close()
					stopAttempt()
					startErrMu.Lock()
					sErr := startErr
					startErrMu.Unlock()
					if sErr != nil {
						return sErr
					}
					elapsedMs := time.Since(attemptStart).Milliseconds()
					if parentCtx.Err() != nil {
						c.Logf("Direct Discovery Engine streamAssist canceled mid-stream after %dms (client disconnected)", elapsedMs)
						return parentCtx.Err()
					}
					c.Logf("Direct Discovery Engine streamAssist stream read error after %dms: %v", elapsedMs, rerr)
					return &UpstreamError{Kind: "transport", Msg: rerr.Error()}
				}
				break
			}
		}
		if earlyStartTimer != nil {
			earlyStartTimer.Stop()
		}
		_ = resp.Body.Close()
		stopAttempt()

		startErrMu.Lock()
		if startErr != nil {
			err := startErr
			startErrMu.Unlock()
			return err
		}
		startErrMu.Unlock()

		if stream.failed {
			msg := stream.failMsg
			if msg == "" {
				msg = "streamAssist answer state FAILED"
			}
			c.Logf("Direct Discovery Engine streamAssist failed: %s | upstream: %s", msg, summarizeBody([]byte(rawSniff.String())))
			return &UpstreamError{Status: http.StatusBadGateway, Kind: "http", Msg: msg}
		}
		if !stream.hasText {
			snippet := summarizeBody([]byte(rawSniff.String()))
			c.Logf("Direct Discovery Engine streamAssist returned no answer text | upstream: %s", snippet)
			return &UpstreamError{
				Status: http.StatusBadGateway,
				Kind:   "http",
				Msg:    "streamAssist direct call returned no answer text: " + snippet,
			}
		}
		if stream.emitErr == nil && conv.OnSessionUsed != nil {
			conv.OnSessionUsed(session)
		}
		return stream.emitErr
	}
	if authFailed && lastErr != nil && c.OnAuthError != nil {
		c.OnAuthError(lastErr.Error())
	}
	if lastErr == nil {
		lastErr = &UpstreamError{Status: http.StatusInternalServerError, Kind: "http", Msg: "streamAssist direct call failed with no error"}
	}
	return lastErr
}

// sessionNotFound reports whether an upstream body says the session resource is
// gone (rather than the request being wrong in some other way).
func sessionNotFound(body []byte) bool {
	lower := strings.ToLower(string(body))
	if !strings.Contains(lower, "session") {
		return false
	}
	return strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "not_found") ||
		strings.Contains(lower, "not found")
}
