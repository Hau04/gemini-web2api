package gemini

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

type UpstreamError struct {
	Status int
	Kind   string
	Msg    string
}

func (e *UpstreamError) Error() string {
	return e.Msg
}

// IsUnsupported reports whether err means the origin lacks the Bard web RPC
// (Gemini Enterprise) — such errors are permanent for the session and must
// never be retried.
func IsUnsupported(err error) bool {
	var ue *UpstreamError
	return errors.As(err, &ue) && ue.Kind == "unsupported"
}

type Requester interface {
	Do(req *http.Request) (*http.Response, error)
}

type ModelOptions struct {
	ModelName   string
	ModelID     int
	ThinkMode   int
	ModelHeader string
	Extra       map[int]any
	// Files carries in-memory attachments. Consumer origins ignore it (the
	// server uploads through content-push and passes the resulting refs in
	// fileRefs); Gemini Enterprise uploads them into the assistant session
	// instead, because content-push does not exist on that origin.
	Files []RawFile
}

type Client struct {
	Cfg     config.Config
	HTTP    Requester
	Cookies *CookieCache
	Logf    func(format string, args ...any)
	// OnAuthError is invoked when Google rejects the session cookies (redirect to
	// sign-in, HTTP 401/403) so the server can surface "session expired" state.
	OnAuthError func(msg string)
	blMu        sync.Mutex
	bl          string

	modelsMu      sync.RWMutex
	discovered    []DiscoveredModel
	accountCap    int
	accountCapFld int
	discMu        sync.Mutex
	lastDiscFail  time.Time

	// bardUnsupported latches true when the current origin answers the Bard web
	// RPC paths with HTTP 404 — Gemini Enterprise (vertexaisearch.cloud.google.com)
	// speaks the Discovery Engine streamAssist API instead, so direct cookie mode
	// can never work there and requests must use the browser bridge.
	bardUnsupported atomic.Bool

	// uploadCache remembers the fileId returned for a context-file upload so a
	// later turn that resends the same bytes can reference it directly instead
	// of uploading again (Enterprise files stay in their session).
	uploadMu    sync.Mutex
	uploadCache map[string]cachedUpload

	// Gemini Enterprise widget auth: an HS256 JWT minted from the portal XSRF
	// token, plus the assistant session every widget call must reference.
	enterpriseAuthMu  sync.Mutex
	enterpriseJWT     string
	enterpriseJWTExp  time.Time
	enterpriseJWTKey  string
	enterpriseSession string
}

func NewClient(cfg config.Config) *Client {
	var req Requester
	if cfg.Impersonate != "" && cfg.Proxy == "" {
		tlsAdapter, err := getTLSClient(cfg.Impersonate, cfg.RequestTimeoutSec)
		if err != nil {
			log.Printf("Failed to create TLS client for profile %s: %v, falling back to stdlib", cfg.Impersonate, err)
		} else {
			req = tlsAdapter
		}
	}

	if req == nil {
		transport := &http.Transport{
			DisableCompression: true,
		}

		if cfg.Proxy != "" {
			if proxyURL, err := url.Parse(cfg.Proxy); err == nil {
				transport.Proxy = http.ProxyURL(proxyURL)
			}
		} else {
			transport.Proxy = http.ProxyFromEnvironment
		}

		req = &http.Client{
			Transport: transport,
			Timeout:   time.Duration(cfg.RequestTimeoutSec) * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	logFn := func(format string, args ...any) {
		if cfg.LogRequests {
			log.Printf(format, args...)
		}
	}

	return &Client{
		Cfg:     cfg,
		HTTP:    req,
		Cookies: NewCookieCache(cfg.CookieFile),
		Logf:    logFn,
		bl:      cfg.GeminiBL,
	}
}

// CurrentBL returns the current BL value, preferring the mutable field over config.
func (c *Client) CurrentBL() string {
	c.blMu.Lock()
	defer c.blMu.Unlock()
	if c.bl != "" {
		return c.bl
	}
	return c.Cfg.GeminiBL
}

// SetBL updates the BL value.
func (c *Client) SetBL(bl string) {
	c.blMu.Lock()
	defer c.blMu.Unlock()
	c.bl = bl
}

// effectiveBaseURL returns the origin direct-mode requests must target: the
// origin of the tab that most recently synced cookies (its XSRF token and
// SAPISID hash are only valid there) wins over the static config base_url.
func (c *Client) effectiveBaseURL() string {
	if c.Cookies != nil {
		if info, err := c.Cookies.Load(); err == nil && info.BaseURL != "" {
			return info.BaseURL
		}
	}
	if c.Cfg.BaseURL != "" {
		return c.Cfg.BaseURL
	}
	return "https://gemini.google.com"
}

// UpdateBLIfNeeded fetches the latest BL from Gemini and updates it if different from current.
// Returns (newBL, changed, error).
func (c *Client) UpdateBLIfNeeded() (string, bool, error) {
	newBL, err := FetchLatestBLWithBaseURL(c.HTTP, c.effectiveBaseURL())
	if err != nil {
		return "", false, err
	}
	if newBL == "" {
		return "", false, fmt.Errorf("empty BL fetched")
	}
	oldBL := c.CurrentBL()
	if newBL == oldBL {
		return newBL, false, nil
	}
	c.SetBL(newBL)
	return newBL, true, nil
}

func (c *Client) buildHeaders() http.Header {
	cookieInfo, _ := c.Cookies.Load()
	authUser := c.Cfg.AuthUser
	if authUser == "" && cookieInfo.AuthUser != "" {
		authUser = cookieInfo.AuthUser
	}

	baseURL := cookieInfo.BaseURL
	if baseURL == "" {
		baseURL = c.Cfg.BaseURL
	}
	if baseURL == "" {
		baseURL = "https://gemini.google.com"
	}

	prefix := AccountPrefix(authUser)
	h := make(http.Header)
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Origin", baseURL)
	h.Set("Referer", fmt.Sprintf("%s%s/app", baseURL, prefix))
	h.Set("X-Same-Domain", "1")
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	if prefix != "" && authUser != "" {
		h.Set("X-Goog-AuthUser", authUser)
	}

	if cookieInfo.Cookie != "" {
		h.Set("Cookie", cookieInfo.Cookie)
	}
	if cookieInfo.SAPISID != "" {
		hash := SAPISIDHashWithOrigin(cookieInfo.SAPISID, baseURL)
		if hash != "" {
			h.Set("Authorization", hash)
		}
	}

	if c.Cfg.Impersonate != "" {
		h.Set("Accept-Encoding", "identity")
	}

	return h
}

// BardUnsupported reports whether the synced origin lacks the Bard web RPC
// (HTTP 404 observed) — i.e. a Gemini Enterprise deployment where direct
// cookie mode cannot work and the browser bridge is the only path.
func (c *Client) BardUnsupported() bool {
	return c.bardUnsupported.Load()
}

// MarkBardUnsupported latches the no-Bard-RPC state (logged once per latch).
func (c *Client) MarkBardUnsupported() {
	if c.bardUnsupported.CompareAndSwap(false, true) {
		log.Printf("Origin does not serve the Bard web RPC (HTTP 404) — Gemini Enterprise detected; direct mode switches to Discovery Engine replay (when a template is captured) and the browser bridge")
	}
}

// ClearBardUnsupported resets the latch — called when a fresh session syncs from
// a DIFFERENT origin. Also resets the discovery backoff so the next request
// re-probes immediately instead of waiting out the previous failure window.
func (c *Client) ClearBardUnsupported() {
	c.bardUnsupported.Store(false)
	c.discMu.Lock()
	c.lastDiscFail = time.Time{}
	c.discMu.Unlock()
}

func (c *Client) triageStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusNotFound {
		c.MarkBardUnsupported()
		return &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "unsupported",
			Msg:    "origin does not serve the Bard web RPC (HTTP 404) — Gemini Enterprise deployment; use the browser bridge (keep the Gemini tab open)",
		}
	}
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		// A redirect to accounts.google.com means the session cookies expired or the
		// account was signed out — distinct from an IP-level sorry/index block.
		if loc := resp.Header.Get("Location"); strings.Contains(loc, "accounts.google.com") {
			err := &UpstreamError{
				Status: resp.StatusCode,
				Kind:   "auth",
				Msg:    "session cookies expired or signed out (redirected to Google sign-in); open gemini.google.com in the browser to refresh the session",
			}
			if c.OnAuthError != nil {
				c.OnAuthError(err.Msg)
			}
			return err
		}
		return &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "http",
			Msg:    "upstream blocked (redirected to sorry/index); IP may be flagged - retry later or use a different proxy/IP",
		}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		err := &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "auth",
			Msg:    fmt.Sprintf("session cookies rejected (HTTP %d); cookies may be expired - open gemini.google.com in the browser to refresh the session", resp.StatusCode),
		}
		if c.OnAuthError != nil {
			c.OnAuthError(err.Msg)
		}
		return err
	}
	if resp.StatusCode == http.StatusBadRequest {
		// A 400 carrying an xsrf error means the page XSRF token (SNlM0e) rotated —
		// the session cookies may still be valid, but the token must be refreshed by
		// opening Gemini in the browser again (the extension re-pushes it automatically).
		var sniff [4096]byte
		n, _ := resp.Body.Read(sniff[:])
		bodyLower := strings.ToLower(string(sniff[:n]))
		if strings.Contains(bodyLower, "xsrf") {
			err := &UpstreamError{
				Status: resp.StatusCode,
				Kind:   "auth",
				Msg:    "XSRF token expired (HTTP 400 xsrf); open gemini.google.com in the browser once to refresh the session token",
			}
			if c.OnAuthError != nil {
				c.OnAuthError(err.Msg)
			}
			return err
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "http",
			Msg:    "upstream rate limited (HTTP 429); retry later",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return &UpstreamError{
			Status: resp.StatusCode,
			Kind:   "http",
			Msg:    fmt.Sprintf("upstream HTTP %d", resp.StatusCode),
		}
	}
	return nil
}

func (c *Client) Generate(prompt string, opt ModelOptions, fileRefs []string) (string, error) {
	c.EnsureModelsDiscovered()
	// Origins without the Bard web RPC (Gemini Enterprise) cannot use
	// batchexecute — replay the captured Discovery Engine streamAssist request
	// instead (works with the browser closed), or fail with an actionable error.
	if c.BardUnsupported() {
		return c.enterpriseGenerate(prompt, opt)
	}

	cookieInfo, _ := c.Cookies.Load()
	cfg := c.Cfg
	if cfg.XSRFToken == "" && cookieInfo.XSRFToken != "" {
		cfg.XSRFToken = cookieInfo.XSRFToken
	}
	if cfg.AuthUser == "" && cookieInfo.AuthUser != "" {
		cfg.AuthUser = cookieInfo.AuthUser
	}
	if cookieInfo.BaseURL != "" {
		cfg.BaseURL = cookieInfo.BaseURL
	}
	c.Logf("Routing direct cookie-mode request to %s (model=%s)", cfg.BaseURL, opt.ModelName)

	reqUUID := strings.ToUpper(uuidV4())
	extra := make(map[int]any, len(opt.Extra)+1)
	for k, v := range opt.Extra {
		extra[k] = v
	}
	extra[59] = reqUUID

	modelHdr, effectiveMode := c.resolveModelHeader(opt, reqUUID)
	bodyStr := BuildBody(prompt, effectiveMode, opt.ThinkMode, fileRefs, extra, cfg)
	reqURL := BuildURL(cfg, c.CurrentBL())
	headers := c.buildHeaders()
	headers.Set(HeaderUUIDJspb, fmt.Sprintf(`["%s",1]`, reqUUID))
	if modelHdr != "" {
		headers.Set(ModelHeaderKey, modelHdr)
		headers.Set(HeaderJspb89, "[0]")
		headers.Set(HeaderJspb90, "[0,0,0]")
	}

	blRefreshed := false
	var lastErr error

	for attempt := 0; attempt < c.Cfg.RetryAttempts; attempt++ {
		req, err := http.NewRequest("POST", reqURL, strings.NewReader(bodyStr))
		if err != nil {
			return "", err
		}
		req.Header = headers.Clone()

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = &UpstreamError{Kind: "transport", Msg: err.Error()}
		} else {
			if err := c.triageStatus(resp); err != nil {
				_ = resp.Body.Close()
				// 405 = BL expired. Refresh once and retry without consuming an attempt.
				if !blRefreshed && resp.StatusCode == http.StatusMethodNotAllowed {
					blRefreshed = true
					if _, changed, blErr := c.UpdateBLIfNeeded(); blErr == nil && changed {
						c.Logf("BL auto-updated, retrying request")
						bodyStr = BuildBody(prompt, effectiveMode, opt.ThinkMode, fileRefs, extra, cfg)
						reqURL = BuildURL(cfg, c.CurrentBL())
						attempt-- // retry without consuming an attempt
						continue
					}
				}
				lastErr = err
				if IsUnsupported(err) {
					// Enterprise origin detected — fall back to Discovery Engine
					// streamAssist (captured template or direct endpoint call)
					break
				}
			} else {
				rawBytes, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil {
					lastErr = &UpstreamError{Kind: "transport", Msg: err.Error()}
				} else {
					text, err := ExtractResponseText(string(rawBytes))
					if err != nil {
						lastErr = &UpstreamError{Kind: "bard", Msg: err.Error()}
					} else {
						return text, nil
					}
				}
			}
		}

		if attempt < c.Cfg.RetryAttempts-1 {
			c.Logf("Retry %d/%d: %v", attempt+1, c.Cfg.RetryAttempts, lastErr)
			time.Sleep(time.Duration(c.Cfg.RetryDelaySec) * time.Second)
		}
	}

	// Batchexecute failed with unsupported (404) — Enterprise origin.
	// Fall back to Discovery Engine streamAssist: captured template first,
	// then direct endpoint call with session cookies.
	if IsUnsupported(lastErr) {
		c.Logf("Enterprise batchexecute returned 404, falling back to Discovery Engine streamAssist")
		return c.enterpriseGenerate(prompt, opt)
	}

	return "", lastErr
}

func (c *Client) GenerateStream(prompt string, opt ModelOptions, fileRefs []string, emit func(string) error) error {
	c.EnsureModelsDiscovered()
	// Same routing as Generate: no Bard web RPC means the Discovery Engine
	// streamAssist replay path (or an actionable error).
	if c.BardUnsupported() {
		return c.enterpriseGenerateStream(prompt, opt, emit)
	}

	cookieInfo, _ := c.Cookies.Load()
	cfg := c.Cfg
	if cfg.XSRFToken == "" && cookieInfo.XSRFToken != "" {
		cfg.XSRFToken = cookieInfo.XSRFToken
	}
	if cfg.AuthUser == "" && cookieInfo.AuthUser != "" {
		cfg.AuthUser = cookieInfo.AuthUser
	}
	if cookieInfo.BaseURL != "" {
		cfg.BaseURL = cookieInfo.BaseURL
	}

	reqUUID := strings.ToUpper(uuidV4())
	extra := make(map[int]any, len(opt.Extra)+1)
	for k, v := range opt.Extra {
		extra[k] = v
	}
	extra[59] = reqUUID

	modelHdr, effectiveMode := c.resolveModelHeader(opt, reqUUID)
	bodyStr := BuildBody(prompt, effectiveMode, opt.ThinkMode, fileRefs, extra, cfg)
	reqURL := BuildURL(cfg, c.CurrentBL())
	headers := c.buildHeaders()
	headers.Set(HeaderUUIDJspb, fmt.Sprintf(`["%s",1]`, reqUUID))
	if modelHdr != "" {
		headers.Set(ModelHeaderKey, modelHdr)
		headers.Set(HeaderJspb89, "[0]")
		headers.Set(HeaderJspb90, "[0,0,0]")
	}

	blRefreshed := false
	parser := NewStreamParser()
	var lastErr error

	for attempt := 0; attempt < c.Cfg.RetryAttempts; attempt++ {
		parser.ResetBuffer()

		req, err := http.NewRequest("POST", reqURL, strings.NewReader(bodyStr))
		if err != nil {
			return err
		}
		req.Header = headers.Clone()

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = &UpstreamError{Kind: "transport", Msg: err.Error()}
		} else {
			if err := c.triageStatus(resp); err != nil {
				_ = resp.Body.Close()
				// 405 = BL expired. Refresh once and retry without consuming an attempt.
				if !blRefreshed && resp.StatusCode == http.StatusMethodNotAllowed {
					blRefreshed = true
					if _, changed, blErr := c.UpdateBLIfNeeded(); blErr == nil && changed {
						c.Logf("BL auto-updated, retrying stream request")
						bodyStr = BuildBody(prompt, effectiveMode, opt.ThinkMode, fileRefs, extra, cfg)
						reqURL = BuildURL(cfg, c.CurrentBL())
						attempt-- // retry without consuming an attempt
						continue
					}
				}
				lastErr = err
				if IsUnsupported(err) {
					break
				}
			} else {
				err = c.streamAttempt(resp.Body, parser, emit)
				_ = resp.Body.Close()
				if err == nil {
					return nil
				}
				if isClientDisconnect(err) {
					return err
				}
				lastErr = err
			}
		}

		if attempt < c.Cfg.RetryAttempts-1 {
			c.Logf("Stream retry %d/%d: %v", attempt+1, c.Cfg.RetryAttempts, lastErr)
			time.Sleep(time.Duration(c.Cfg.RetryDelaySec) * time.Second)
		}
	}

	// Batchexecute failed with unsupported (404) — Enterprise origin.
	// Fall back to Discovery Engine streamAssist: captured template first,
	// then direct endpoint call with session cookies.
	if IsUnsupported(lastErr) {
		c.Logf("Falling back to Discovery Engine streamAssist for Enterprise origin (stream)")
		return c.enterpriseGenerateStream(prompt, opt, emit)
	}

	return lastErr
}

func (c *Client) streamAttempt(body io.Reader, parser *StreamParser, emit func(string) error) error {
	reader := bufio.NewReader(body)
	for {
		lineBytes, err := reader.ReadBytes('\n')
		if len(lineBytes) > 0 {
			deltas, parseErr := parser.Feed(string(lineBytes))
			if parseErr != nil {
				return &UpstreamError{Kind: "bard", Msg: parseErr.Error()}
			}
			for _, delta := range deltas {
				if err := emit(delta); err != nil {
					return err
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return &UpstreamError{Kind: "transport", Msg: err.Error()}
		}
	}
}

// isClientDisconnect helps distinguish between upstream server errors (which we might want to retry)
// and actual client-side disconnections or non-retryable transport errors.
// If the client disconnected, we should abort the retry loop immediately to save resources.
func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*UpstreamError); ok {
		return false
	}
	return true
}
