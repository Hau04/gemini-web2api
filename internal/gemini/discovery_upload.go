package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Gemini Enterprise (Discovery Engine) attaches files to a *session* instead of
// the consumer content-push pipeline that gemini.google.com uses:
//
//	POST {widget}/v1alpha/locations/{loc}/widgetAddContextFile
//	{"location":"locations/global","configId":"<widget config>",
//	 "addContextFileRequest":{"name":".../sessions/-","fileName":"a.txt",
//	                          "mimeType":"text/plain","fileContents":"<base64>"}}
//	<- {"addContextFileResponse":{"session":".../sessions/123","fileId":"456"}}
//
// The file is then referenced from streamAssist with "fileIds":["456"] plus the
// matching "session". Because the widget endpoint is undocumented and private
// to the Gemini Enterprise web app, the extension captures one real upload
// request and this file replays it with fresh cookies — the same trick
// discovery_template.json already uses for prompts.

// Placeholders substituted into a captured upload request body.
const (
	UploadFileNamePlaceholder = "__W2A_FILE_NAME__"
	UploadFileMimePlaceholder = "__W2A_FILE_MIME__"
	UploadFileB64Placeholder  = "__W2A_FILE_B64__"
)

const (
	// MaxEnterpriseUploadBytes caps a single context-file upload.
	MaxEnterpriseUploadBytes = 20 << 20
	// Inline text attachments are capped so a prompt never explodes.
	maxInlineFileBytes  = 200 << 10
	maxInlineTotalBytes = 400 << 10
)

// RawFile is an in-memory attachment that still has to be uploaded upstream.
// The consumer origin uploads it through content-push; Gemini Enterprise
// uploads it into the assistant session instead.
type RawFile struct {
	Name string
	MIME string
	Data []byte
}

// IsTextLike reports whether the file can be inlined into the prompt when the
// origin refuses context-file uploads.
func (f RawFile) IsTextLike() bool {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(f.MIME, ";")[0]))
	switch {
	case strings.HasPrefix(mime, "text/"):
		return true
	case strings.HasSuffix(mime, "+json"), strings.HasSuffix(mime, "+xml"):
		return true
	case mime == "", mime == "application/octet-stream":
		return looksLikeText(f.Data)
	}
	switch mime {
	case "application/json", "application/xml", "application/javascript",
		"application/typescript", "application/x-sh", "application/x-yaml",
		"application/yaml", "application/toml", "application/sql",
		"application/x-httpd-php", "application/x-python", "application/rtf",
		"application/csv", "application/x-ndjson":
		return true
	}
	return false
}

func looksLikeText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	sample := data
	if len(sample) > 4096 {
		sample = sample[:4096]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return false
	}
	return utf8.Valid(sample)
}

// DiscoveryUploadTemplate is one captured context-file upload request.
type DiscoveryUploadTemplate struct {
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	CapturedAt time.Time         `json:"captured_at,omitempty"`
}

func discoveryUploadTemplatePath(cookieFile string) string {
	if strings.TrimSpace(cookieFile) == "" {
		cookieFile = "cookie.json"
	}
	return filepath.Join(filepath.Dir(cookieFile), "discovery_upload_template.json")
}

// LoadDiscoveryUploadTemplate reads a persisted upload template (nil if absent).
func LoadDiscoveryUploadTemplate(cookieFile string) *DiscoveryUploadTemplate {
	data, err := os.ReadFile(discoveryUploadTemplatePath(cookieFile))
	if err != nil {
		return nil
	}
	var tpl DiscoveryUploadTemplate
	if err := json.Unmarshal(data, &tpl); err != nil {
		return nil
	}
	if strings.TrimSpace(tpl.URL) == "" || strings.TrimSpace(tpl.Body) == "" {
		return nil
	}
	return &tpl
}

// SaveDiscoveryUploadTemplate persists a captured upload template.
func SaveDiscoveryUploadTemplate(cookieFile string, tpl *DiscoveryUploadTemplate) error {
	data, err := json.MarshalIndent(tpl, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(discoveryUploadTemplatePath(cookieFile), data, 0o600)
}

// HasDiscoveryUploadTemplate reports whether a replayable upload template exists.
func (c *Client) HasDiscoveryUploadTemplate() bool {
	return LoadDiscoveryUploadTemplate(c.Cfg.CookieFile) != nil
}

// SubstituteFile returns the captured upload body with our file spliced in.
// Captures that already carry the placeholder markers are handled by plain
// string replacement; raw captures are rewritten structurally by JSON key.
func (t *DiscoveryUploadTemplate) SubstituteFile(file RawFile) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(file.Data)
	if strings.Contains(t.Body, UploadFileB64Placeholder) {
		out := strings.ReplaceAll(t.Body, UploadFileB64Placeholder, b64)
		out = strings.ReplaceAll(out, UploadFileMimePlaceholder, file.MIME)
		out = strings.ReplaceAll(out, UploadFileNamePlaceholder, jsonEscapeInner(file.Name))
		return out, nil
	}

	var obj any
	if err := json.Unmarshal([]byte(t.Body), &obj); err != nil {
		return "", fmt.Errorf("captured upload template is not JSON — attach one file through the Gemini Enterprise tab to refresh it")
	}

	changed := false
	var walk func(node any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			for k, val := range v {
				switch strings.ToLower(k) {
				case "filecontents", "file_contents", "filedata", "file_data", "content", "data":
					if _, isStr := val.(string); isStr {
						v[k] = b64
						changed = true
						continue
					}
					walk(val)
				case "filename", "file_name":
					v[k] = file.Name
					changed = true
				case "mimetype", "mime_type", "contenttype", "content_type":
					v[k] = file.MIME
					changed = true
				default:
					walk(val)
				}
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(obj)
	if !changed {
		return "", fmt.Errorf("captured upload template has no file field — attach one file through the Gemini Enterprise tab to refresh it")
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

var reLocationSegment = regexp.MustCompile(`/locations/([^/?#]+)`)

// enterpriseLocation returns the "locations/{location}" resource for the widget
// API, derived from the configured endpoint when possible.
func (c *Client) enterpriseLocation() string {
	for _, endpoint := range []string{c.Cfg.DiscoveryEngineUploadEndpoint, c.Cfg.DiscoveryEngineEndpoint} {
		if m := reLocationSegment.FindStringSubmatch(endpoint); len(m) > 1 && m[1] != "" {
			return "locations/" + m[1]
		}
	}
	return "locations/global"
}

// swapWidgetMethod rewrites the widget method segment of a Discovery Engine
// widget URL: .../locations/global/widgetStreamAssist -> .../widgetAddContextFile.
func swapWidgetMethod(endpoint, method string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	path := u.Path
	idx := strings.LastIndex(path, "/widget")
	if idx < 0 {
		idx = strings.LastIndex(path, "/v1")
		if idx < 0 {
			return ""
		}
		u.Path = strings.TrimRight(path, "/") + "/" + method
		return u.String()
	}
	u.Path = path[:idx] + "/" + method
	u.RawQuery = ""
	return u.String()
}

// enterpriseUploadEndpoint resolves the addContextFile URL to use.
func (c *Client) enterpriseUploadEndpoint(tpl *DiscoveryUploadTemplate) string {
	if tpl != nil && strings.TrimSpace(tpl.URL) != "" {
		return tpl.URL
	}
	if ep := strings.TrimSpace(c.Cfg.DiscoveryEngineUploadEndpoint); ep != "" {
		return ep
	}
	if ep := strings.TrimSpace(c.Cfg.DiscoveryEngineEndpoint); ep != "" {
		if swapped := swapWidgetMethod(ep, "widgetAddContextFile"); swapped != "" {
			return swapped
		}
	}
	return ""
}

// enterpriseSessionName returns the (sanitized) session resource used for the
// upload. The trailing session id stays "-" so the backend creates a session and
// returns its id, which is then reused for the following streamAssist call.
func (c *Client) enterpriseSessionName() string {
	if s := strings.TrimSpace(c.Cfg.DiscoveryEngineSession); s != "" {
		return s
	}
	if tpl := LoadDiscoveryTemplate(c.Cfg.CookieFile); tpl != nil {
		if s := sessionFromBody(tpl.Body); s != "" {
			return replaceSessionID(s, "-")
		}
	}
	if tpl := LoadDiscoveryUploadTemplate(c.Cfg.CookieFile); tpl != nil {
		if s := sessionFromBody(tpl.Body); s != "" {
			return replaceSessionID(s, "-")
		}
	}
	return ""
}

// enterpriseConfigID resolves the WidgetConfig id the widget API requires.
func (c *Client) enterpriseConfigID(tpl *DiscoveryUploadTemplate) string {
	if id := strings.TrimSpace(c.Cfg.DiscoveryEngineConfigID); id != "" {
		return id
	}
	if tpl != nil {
		if id := configIDFromBody(tpl.Body); id != "" {
			return id
		}
	}
	if tpl := LoadDiscoveryTemplate(c.Cfg.CookieFile); tpl != nil {
		if id := configIDFromBody(tpl.Body); id != "" {
			return id
		}
	}
	return ""
}

// discoveryHeaders builds the cookie-authenticated header set for a Gemini
// Enterprise (Discovery Engine widget) request to the given endpoint.
func (c *Client) discoveryHeaders(endpoint string) http.Header {
	cookieInfo, _ := c.Cookies.Load()

	origin := cookieInfo.BaseURL
	if origin == "" {
		origin = c.Cfg.BaseURL
	}
	if origin == "" && endpoint != "" {
		if u, err := url.Parse(endpoint); err == nil && u.Scheme != "" && u.Host != "" {
			origin = u.Scheme + "://" + u.Host
		}
	}
	if origin == "" {
		origin = "https://gemini.google.com"
	}

	authUser := c.Cfg.AuthUser
	if authUser == "" && cookieInfo.AuthUser != "" {
		authUser = cookieInfo.AuthUser
	}

	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", origin)
	headers.Set("Referer", origin+AccountPrefix(authUser)+"/app")
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	headers.Set("X-Goog-Api-Client", "gemini-enterprise-classic/1.0.0")
	if authUser != "" {
		headers.Set("X-Goog-AuthUser", authUser)
	} else {
		headers.Set("X-Goog-AuthUser", "0")
	}
	if cookieInfo.Cookie != "" {
		headers.Set("Cookie", cookieInfo.Cookie)
	}
	if cookieInfo.SAPISID != "" {
		if auth := SAPISIDHashWithOrigin(cookieInfo.SAPISID, origin); auth != "" {
			headers.Set("Authorization", auth)
		}
	}
	if c.Cfg.DiscoveryEngineServerToken != "" {
		headers.Set("X-Server-Token", c.Cfg.DiscoveryEngineServerToken)
	}
	return headers
}

// buildEnterpriseUploadBody constructs a widgetAddContextFile request body for
// origins where no upload request has been captured yet.
func (c *Client) buildEnterpriseUploadBody(file RawFile) string {
	req := map[string]any{
		"fileName":     file.Name,
		"mimeType":     file.MIME,
		"fileContents": base64.StdEncoding.EncodeToString(file.Data),
	}
	if session := c.enterpriseSessionName(); session != "" {
		req["name"] = session
	}
	payload := map[string]any{
		"location":              c.enterpriseLocation(),
		"addContextFileRequest": req,
	}
	if id := c.enterpriseConfigID(nil); id != "" {
		payload["configId"] = id
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// UploadEnterpriseFile pushes one file into the assistant session and returns
// its file id plus the session resource the following streamAssist call must
// reference.
func (c *Client) UploadEnterpriseFile(file RawFile) (fileID string, session string, err error) {
	if len(file.Data) == 0 {
		return "", "", fmt.Errorf("attachment %q is empty", file.Name)
	}
	if len(file.Data) > MaxEnterpriseUploadBytes {
		return "", "", fmt.Errorf("attachment %q is %d bytes; Gemini Enterprise accepts at most %d bytes per context file",
			file.Name, len(file.Data), MaxEnterpriseUploadBytes)
	}
	if strings.TrimSpace(file.MIME) == "" {
		file.MIME = "application/octet-stream"
	}

	tpl := LoadDiscoveryUploadTemplate(c.Cfg.CookieFile)
	endpoint := c.enterpriseUploadEndpoint(tpl)
	if endpoint == "" {
		return "", "", &UpstreamError{
			Status: http.StatusNotFound,
			Kind:   "unsupported",
			Msg: "this Gemini Enterprise origin needs an attachment upload endpoint: set \"discovery_engine_upload_endpoint\" " +
				"(the widgetAddContextFile URL from the Gemini Enterprise tab's DevTools Network tab), or attach one file " +
				"through the tab once with the extension enabled so it is captured for replay",
		}
	}

	var body string
	if tpl != nil {
		body, err = tpl.SubstituteFile(file)
		if err != nil {
			return "", "", err
		}
	} else {
		body = c.buildEnterpriseUploadBody(file)
	}
	if strings.TrimSpace(body) == "" {
		return "", "", fmt.Errorf("could not build the context-file upload body")
	}

	headers := c.discoveryHeaders(endpoint)
	if tpl != nil {
		for k, v := range tpl.Headers {
			switch strings.ToLower(k) {
			case "authorization", "cookie", "content-length", "host", "origin", "referer", "x-server-token":
				continue
			default:
				headers.Set(k, v)
			}
		}
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header = headers

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", &UpstreamError{Kind: "transport", Msg: fmt.Sprintf("context-file upload failed: %v", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			hint = " — the synced cookies or the captured upload request may be stale; refresh them from the Gemini Enterprise tab"
		}
		return "", "", &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "http",
			Msg:    fmt.Sprintf("context-file upload rejected (HTTP %d)%s: %s", resp.StatusCode, hint, summarizeBody(respBody)),
		}
	}

	parsed := parseUploadResponse(respBody)
	if parsed.fileID == "" {
		return "", "", &UpstreamError{
			Status: http.StatusBadGateway,
			Kind:   "http",
			Msg:    fmt.Sprintf("context-file upload response had no fileId: %s", summarizeBody(respBody)),
		}
	}
	if parsed.session == "" {
		parsed.session = c.enterpriseSessionName()
	}
	c.Logf("Enterprise context file %q uploaded (fileId=%s session=%s)", file.Name, parsed.fileID, parsed.session)
	return parsed.fileID, parsed.session, nil
}

type uploadResponse struct {
	fileID  string
	session string
}

func parseUploadResponse(body []byte) uploadResponse {
	cleaned := strings.TrimSpace(string(body))
	cleaned = strings.TrimPrefix(cleaned, ")]}'")
	var obj any
	if err := json.Unmarshal([]byte(cleaned), &obj); err != nil {
		return uploadResponse{}
	}
	out := uploadResponse{
		fileID:  deepFindString(obj, "fileId", "file_id", "fileID"),
		session: deepFindString(obj, "session"),
	}
	if out.fileID == "" {
		// Some widget responses return the id under a nested tool result.
		out.fileID = deepFindString(obj, "contextFileId", "context_file_id")
	}
	return out
}

// deepFindString walks a decoded JSON value and returns the first string stored
// under any of the given keys (case-insensitive on the first key spelling).
func deepFindString(node any, keys ...string) string {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[strings.ToLower(k)] = true
	}
	var found string
	var walk func(node any) bool
	walk = func(node any) bool {
		switch v := node.(type) {
		case map[string]any:
			for k, val := range v {
				if want[strings.ToLower(k)] {
					if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
						found = strings.TrimSpace(s)
						return true
					}
				}
			}
			for _, val := range v {
				if walk(val) {
					return true
				}
			}
		case []any:
			for _, item := range v {
				if walk(item) {
					return true
				}
			}
		}
		return false
	}
	walk(node)
	return found
}

func summarizeBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	text = strings.TrimPrefix(text, ")]}'")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 240 {
		text = text[:240] + "…"
	}
	return text
}

// sessionFromBody extracts a session resource from a captured request body.
func sessionFromBody(body string) string {
	var obj any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return ""
	}
	return deepFindString(obj, "session")
}

// configIDFromBody extracts the widget config id from a captured request body.
func configIDFromBody(body string) string {
	var obj any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return ""
	}
	return deepFindString(obj, "configId", "config_id")
}

// replaceSessionID rewrites the trailing session id of a (sanitized) session
// resource, e.g. .../sessions/123 -> .../sessions/-.
func replaceSessionID(session, id string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(session), "/")
	idx := strings.LastIndex(trimmed, "/sessions/")
	if idx < 0 {
		return trimmed
	}
	return trimmed[:idx] + "/sessions/" + id
}

// InjectAttachmentContext rewrites a streamAssist request body so it carries the
// uploaded file ids and the session they belong to. Both the captured template
// and the locally built widget body are supported.
func InjectAttachmentContext(body string, fileIDs []string, session string) (string, error) {
	if len(fileIDs) == 0 {
		return body, nil
	}
	var obj any
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		return "", fmt.Errorf("streamAssist body is not JSON: %w", err)
	}
	target := findAssistRequest(obj)
	if target == nil {
		return "", fmt.Errorf("streamAssist body has no recognisable request object")
	}
	ids := make([]any, len(fileIDs))
	for i, id := range fileIDs {
		ids[i] = id
	}
	target["fileIds"] = ids
	if s := strings.TrimSpace(session); s != "" {
		target["session"] = s
	}
	encoded, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// findAssistRequest locates the StreamAssistRequest-shaped object inside a
// widget or REST streamAssist body.
func findAssistRequest(node any) map[string]any {
	switch v := node.(type) {
	case map[string]any:
		for _, key := range []string{"streamAssistRequest", "stream_assist_request", "assistRequest", "assist_request"} {
			if nested, ok := v[key].(map[string]any); ok {
				return nested
			}
		}
		if _, ok := v["query"]; ok {
			return v
		}
		for _, val := range v {
			if found := findAssistRequest(val); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range v {
			if found := findAssistRequest(item); found != nil {
				return found
			}
		}
	}
	return nil
}

// prepareEnterpriseAttachments uploads the request's files into the assistant
// session. Files that cannot be uploaded but are text-like are inlined into the
// prompt instead, so a browserless Enterprise setup still works for code and
// documents even when the upload endpoint is not configured.
func (c *Client) prepareEnterpriseAttachments(prompt string, files []RawFile) (string, []string, string, error) {
	if len(files) == 0 {
		return prompt, nil, "", nil
	}

	var (
		ids      []string
		session  string
		inline   strings.Builder
		uploaded int
	)
	inlineBudget := maxInlineTotalBytes

	for _, file := range files {
		fileID, sess, err := c.UploadEnterpriseFile(file)
		if err == nil {
			ids = append(ids, fileID)
			if sess != "" {
				session = sess
			}
			uploaded++
			continue
		}

		if !file.IsTextLike() {
			return prompt, nil, "", &UpstreamError{
				Status: http.StatusBadGateway,
				Kind:   "unsupported",
				Msg: fmt.Sprintf("Gemini Enterprise could not accept attachment %q (%s) and it is not text that can be inlined: %v",
					file.Name, file.MIME, err),
			}
		}
		if inlineBudget <= 0 {
			c.Logf("Skipping inline attachment %q: inline budget exhausted", file.Name)
			continue
		}

		body := file.Data
		truncated := false
		limit := maxInlineFileBytes
		if limit > inlineBudget {
			limit = inlineBudget
		}
		if len(body) > limit {
			body = body[:limit]
			truncated = true
		}
		inlineBudget -= len(body)
		c.Logf("Enterprise upload unavailable for %q (%v) — inlining its text into the prompt", file.Name, err)

		name := file.Name
		if name == "" {
			name = "attachment"
		}
		inline.WriteString("\n\n--- Attached file: ")
		inline.WriteString(name)
		inline.WriteString(" (")
		inline.WriteString(file.MIME)
		inline.WriteString(") ---\n")
		inline.Write(body)
		if truncated {
			inline.WriteString("\n...[truncated]")
		}
		inline.WriteString("\n--- end of ")
		inline.WriteString(name)
		inline.WriteString(" ---")
	}

	if inline.Len() > 0 {
		prompt += inline.String()
	}
	if uploaded > 0 {
		c.Logf("Enterprise attachments: %d uploaded as context files, %d inlined", uploaded, len(files)-uploaded)
	}
	return prompt, ids, session, nil
}

// enterpriseGenerate runs the Gemini Enterprise (Discovery Engine) path with
// attachment support: context files are uploaded into the assistant session (or
// inlined when they cannot be uploaded), then the prompt is replayed through the
// captured streamAssist template or the configured endpoint.
func (c *Client) enterpriseGenerate(prompt string, opt ModelOptions) (string, error) {
	var sb strings.Builder
	if err := c.enterpriseGenerateStream(prompt, opt, func(delta string) error {
		sb.WriteString(delta)
		return nil
	}); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func (c *Client) enterpriseGenerateStream(prompt string, opt ModelOptions, emit func(string) error) error {
	prompt, fileIDs, session, err := c.prepareEnterpriseAttachments(prompt, opt.Files)
	if err != nil {
		return err
	}

	// Captured template first (exact shape of the app's own request).
	if LoadDiscoveryTemplate(c.Cfg.CookieFile) != nil {
		c.Logf("Enterprise: replaying captured Discovery Engine template (files=%d)", len(fileIDs))
		return c.generateViaDiscoveryStream(prompt, fileIDs, session, emit)
	}
	// Then the configured streamAssist endpoint.
	if strings.TrimSpace(c.Cfg.DiscoveryEngineEndpoint) != "" {
		c.Logf("Enterprise: calling configured Discovery Engine streamAssist endpoint (files=%d)", len(fileIDs))
		return c.generateViaDiscoveryDirectStream(prompt, fileIDs, session, emit)
	}
	return &UpstreamError{
		Status: http.StatusNotFound,
		Kind:   "unsupported",
		Msg: "direct cookie mode is unavailable on this Gemini Enterprise origin: no Discovery Engine streamAssist endpoint configured " +
			"and no request template captured — set discovery_engine_endpoint in config.json, or send one prompt through the open Gemini tab " +
			"so the extension can capture a streamAssist template",
	}
}

// UsesDiscoveryEngine reports whether requests must go through the Discovery
// Engine (Gemini Enterprise) path, where the consumer Bard web RPC and the
// content-push upload pipeline do not exist.
func (c *Client) UsesDiscoveryEngine() bool {
	if c.BardUnsupported() {
		return true
	}
	origin := ""
	if c.Cookies != nil {
		if info, err := c.Cookies.Load(); err == nil {
			origin = info.BaseURL
		}
	}
	if origin == "" {
		origin = c.Cfg.BaseURL
	}
	origin = strings.ToLower(origin)
	for _, marker := range []string{
		"vertexaisearch",
		"discoveryengine",
		"agentspace",
		"gemini-business",
		"business.gemini.google",
	} {
		if strings.Contains(origin, marker) {
			return true
		}
	}
	return false
}
