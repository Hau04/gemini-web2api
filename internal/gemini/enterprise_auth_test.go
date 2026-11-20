package gemini

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

// hs256Key is a fixed base64url HMAC key standing in for the portal xsrfToken.
var hs256Key = base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))

func TestMintEnterpriseJWTClaimsAndSignature(t *testing.T) {
	now := time.Unix(1700000000, 0)
	token, err := mintEnterpriseJWT(hs256Key, "kid-42", "https://vertexaisearch.cloud.google.com",
		"https://content-us-discoveryengine.googleapis.com", "735410566", now)
	if err != nil {
		t.Fatalf("mintEnterpriseJWT() error = %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token does not have three segments: %q", token)
	}

	// Header carries the kid the portal handed out.
	rawHeader, err := decodeBase64URL(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]any
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header["alg"] != "HS256" || header["kid"] != "kid-42" {
		t.Errorf("unexpected JWT header: %v", header)
	}

	claims, err := renderEnterpriseJWTPayload(token)
	if err != nil {
		t.Fatalf("renderEnterpriseJWTPayload() error = %v", err)
	}
	wantClaims := map[string]any{
		"iss": "https://vertexaisearch.cloud.google.com",
		"aud": "https://content-us-discoveryengine.googleapis.com",
		"sub": "csesidx/735410566",
		"iat": float64(now.Unix()),
		"exp": float64(now.Unix() + 300),
		"nbf": float64(now.Unix()),
	}
	for key, want := range wantClaims {
		if claims[key] != want {
			t.Errorf("claim %q = %v, want %v", key, claims[key], want)
		}
	}

	// The signature must verify with the raw XSRF key, otherwise Discovery
	// Engine rejects the call with 401/403.
	key, _ := decodeBase64URL(hs256Key)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	wantSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if parts[2] != wantSig {
		t.Errorf("signature = %q, want %q", parts[2], wantSig)
	}
}

func TestEnterpriseBearerMintsFromXSRFAndCaches(t *testing.T) {
	var xsrfCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/getoxsrf" {
			http.NotFound(w, r)
			return
		}
		xsrfCalls++
		if got := r.URL.Query().Get("csesidx"); got != "735410566" {
			t.Errorf("csesidx query = %q, want 735410566", got)
		}
		if !strings.Contains(r.Header.Get("Cookie"), "__Secure-1PSID=abc") {
			t.Errorf("XSRF request did not carry the session cookies: %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(")]}'\n{\"xsrfToken\":\"" + hs256Key + "\",\"keyId\":\"kid-7\"}"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.CookieFile = filepath.Join(dir, "cookie.json")
	cfg.BaseURL = srv.URL
	cfg.DiscoveryEngineCsesidx = "735410566"
	cfg.DiscoveryEngineLocation = "us"
	cfg.DiscoveryEngineConfigID = "cfg-1"
	if err := writeCookieFile(cfg.CookieFile, "__Secure-1PSID=abc"); err != nil {
		t.Fatal(err)
	}

	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	token, err := c.enterpriseBearer()
	if err != nil {
		t.Fatalf("enterpriseBearer() error = %v", err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("expected a JWT, got %q", token)
	}
	if _, err := c.enterpriseBearer(); err != nil {
		t.Fatalf("cached enterpriseBearer() error = %v", err)
	}
	if xsrfCalls != 1 {
		t.Errorf("XSRF endpoint called %d times, want 1 (token must be cached)", xsrfCalls)
	}
}

func TestEnterpriseBearerWithoutCsesidxSelectsLegacyPath(t *testing.T) {
	c := NewClient(config.Default())
	c.Logf = func(string, ...any) {}
	if _, err := c.enterpriseBearer(); err == nil {
		t.Fatal("expected an error when no csesidx is available")
	} else if err != errNoCsesidx {
		t.Fatalf("err = %v, want errNoCsesidx", err)
	}
}

func TestEnterpriseWidgetLocationResolution(t *testing.T) {
	c := NewClient(config.Default())
	if got := c.enterpriseWidgetLocation(); got != "us" {
		t.Errorf("default location = %q, want us", got)
	}

	cfg := config.Default()
	cfg.DiscoveryEngineEndpoint = "https://discoveryengine.clients6.google.com/v1alpha/locations/global/widgetStreamAssist?key=k"
	c = NewClient(cfg)
	if got := c.enterpriseWidgetLocation(); got != "us" {
		t.Errorf("global endpoint location = %q, want us fallback", got)
	}

	cfg.DiscoveryEngineEndpoint = "https://content-eu-discoveryengine.googleapis.com/v1alpha/locations/eu/widgetStreamAssist"
	c = NewClient(cfg)
	if got := c.enterpriseWidgetLocation(); got != "eu" {
		t.Errorf("eu endpoint location = %q, want eu", got)
	}

	cfg.DiscoveryEngineLocation = "global"
	c = NewClient(cfg)
	if got := c.enterpriseWidgetLocation(); got != "global" {
		t.Errorf("explicit location = %q, want global", got)
	}
}

// TestEnterpriseStreamAssistUsesBearerAndSession covers the full widget flow:
// XSRF -> JWT -> widgetCreateSession -> widgetStreamAssist with a session.
func TestEnterpriseStreamAssistUsesBearerAndSession(t *testing.T) {
	var (
		assistAuth    string
		assistBody    string
		assistOrigin  string
		createSession int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/getoxsrf":
			_, _ = w.Write([]byte(`{"xsrfToken":"` + hs256Key + `","keyId":"kid-9"}`))
		case strings.HasSuffix(r.URL.Path, "/widgetCreateSession"):
			createSession++
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("widgetCreateSession missing bearer: %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"session":{"name":"collections/default_collection/engines/e/sessions/777"}}`))
		case strings.HasSuffix(r.URL.Path, "/widgetStreamAssist"):
			assistAuth = r.Header.Get("Authorization")
			assistOrigin = r.Header.Get("Origin")
			raw, _ := io.ReadAll(r.Body)
			assistBody = string(raw)
			_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"hello from enterprise"}}}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.CookieFile = filepath.Join(dir, "cookie.json")
	cfg.BaseURL = srv.URL
	cfg.DiscoveryEngineCsesidx = "735410566"
	cfg.DiscoveryEngineConfigID = "cfg-42"
	cfg.RetryAttempts = 1
	if err := writeCookieFile(cfg.CookieFile, "__Secure-1PSID=abc"); err != nil {
		t.Fatal(err)
	}

	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}
	c.MarkBardUnsupported()

	// Point the widget host at the test server instead of content-us-*.
	origHost := contentWidgetHostFunc
	contentWidgetHostFunc = func(string) string { return srv.URL }
	defer func() { contentWidgetHostFunc = origHost }()

	var out strings.Builder
	if err := c.enterpriseGenerateStream("hi", ModelOptions{ModelName: "gemini-4-hydrogen"}, func(d string) error {
		out.WriteString(d)
		return nil
	}); err != nil {
		t.Fatalf("enterpriseGenerateStream() error = %v", err)
	}
	if out.String() != "hello from enterprise" {
		t.Errorf("answer = %q", out.String())
	}
	if createSession != 1 {
		t.Errorf("widgetCreateSession called %d times, want 1", createSession)
	}
	if !strings.HasPrefix(assistAuth, "Bearer ") {
		t.Errorf("streamAssist Authorization = %q, want a Bearer JWT", assistAuth)
	}
	if assistOrigin != srv.URL {
		t.Errorf("streamAssist Origin = %q, want %q", assistOrigin, srv.URL)
	}
	for _, want := range []string{
		`"configId":"cfg-42"`,
		`"additionalParams":{"token":"-","origin":"ORIGIN_UNSPECIFIED"}`,
		`"session":"collections/default_collection/engines/e/sessions/777"`,
		`"answerGenerationMode":"NORMAL"`,
		`"query":{"parts":[{"text":"hi"}]}`,
	} {
		if !strings.Contains(assistBody, want) {
			t.Errorf("streamAssist body missing %q: %s", want, assistBody)
		}
	}
}

func TestEnterpriseStreamAssistRequiresConfigID(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.CookieFile = filepath.Join(dir, "cookie.json")
	cfg.RetryAttempts = 1
	cfg.DiscoveryEngineEndpoint = "https://discoveryengine.clients6.google.com/v1alpha/locations/global/widgetStreamAssist?key=k"
	c := NewClient(cfg)
	c.HTTP = http.DefaultClient
	c.Logf = func(string, ...any) {}
	c.MarkBardUnsupported()

	err := c.enterpriseGenerateStream("hi", ModelOptions{}, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected an error without a config id")
	}
	if !strings.Contains(err.Error(), "discovery_engine_config_id") {
		t.Errorf("error should point at discovery_engine_config_id: %v", err)
	}
}

// writeCookieFile is a test helper for seeding a synced cookie file.
func writeCookieFile(path, cookie string) error {
	payload := map[string]string{"cookie": cookie, "sapisid": "", "base_url": ""}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func TestEnterpriseModelIDResolution(t *testing.T) {
	cfg := config.Default()
	cfg.DiscoveryEngineModels = map[string]string{
		"gemini-4-hydrogen": "gemini-2.5-flash",
		"Argon":             "gemini-2.5-pro",
		"gemini-4-neon":     "gemini-2.5-pro",
	}
	cfg.DiscoveryEngineDefaultModelID = "gemini-2.5-flash"
	c := NewClient(cfg)
	c.Logf = func(string, ...any) {}

	cases := map[string]string{
		"gemini-4-hydrogen": "gemini-2.5-flash", // mapped
		"ARGON":             "gemini-2.5-pro",   // mapped, case-insensitive
		"gemini-2.5-pro":    "gemini-2.5-pro",   // already an upstream id, passed through
		"gemini-3-pro":      "gemini-3-pro",     // upstream-looking id passes through
		"hydrogen":          "gemini-2.5-flash", // local alias, not mapped -> default id
		"gemini-4-neon":     "gemini-2.5-pro",   // neon is a first-class alias, mapped
		"neon":              "gemini-2.5-flash", // short alias is never passed through as an upstream id
		"":                  "gemini-2.5-flash", // no model requested -> default id
	}
	for requested, want := range cases {
		if got := c.EnterpriseModelID(requested); got != want {
			t.Errorf("EnterpriseModelID(%q) = %q, want %q", requested, got, want)
		}
	}

	plain := NewClient(config.Default())
	if got := plain.EnterpriseModelID("gemini-4-hydrogen"); got != "" {
		t.Errorf("without mapping EnterpriseModelID() = %q, want empty (tenant default)", got)
	}
}

func TestEnterpriseStreamAssistSendsModelID(t *testing.T) {
	var assistBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth/getoxsrf":
			_, _ = w.Write([]byte(`{"xsrfToken":"` + hs256Key + `","keyId":"kid-1"}`))
		case strings.HasSuffix(r.URL.Path, "/widgetCreateSession"):
			_, _ = w.Write([]byte(`{"session":{"name":"collections/c/engines/e/sessions/1"}}`))
		case strings.HasSuffix(r.URL.Path, "/widgetStreamAssist"):
			raw, _ := io.ReadAll(r.Body)
			assistBody = string(raw)
			_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.CookieFile = filepath.Join(dir, "cookie.json")
	cfg.BaseURL = srv.URL
	cfg.DiscoveryEngineCsesidx = "42"
	cfg.DiscoveryEngineConfigID = "cfg-1"
	cfg.DiscoveryEngineModels = map[string]string{"gemini-4-hydrogen": "gemini-2.5-flash"}
	cfg.RetryAttempts = 1
	if err := writeCookieFile(cfg.CookieFile, "__Secure-1PSID=abc"); err != nil {
		t.Fatal(err)
	}

	c := NewClient(cfg)
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}
	c.MarkBardUnsupported()

	origHost := contentWidgetHostFunc
	contentWidgetHostFunc = func(string) string { return srv.URL }
	defer func() { contentWidgetHostFunc = origHost }()

	if err := c.enterpriseGenerateStream("hi", ModelOptions{ModelName: "gemini-4-hydrogen"}, func(string) error { return nil }); err != nil {
		t.Fatalf("enterpriseGenerateStream() error = %v", err)
	}
	if !strings.Contains(assistBody, `"assistGenerationConfig":{"modelId":"gemini-2.5-flash"}`) {
		t.Errorf("streamAssist body must carry the mapped model id: %s", assistBody)
	}
}

func TestInjectTemplateModelID(t *testing.T) {
	withModel := `{"configId":"c","streamAssistRequest":{"assistGenerationConfig":{"modelId":"old"},"query":{"parts":[{"text":"q"}]}}}`
	got := injectTemplateModelID(withModel, "gemini-2.5-pro")
	if !strings.Contains(got, `"modelId":"gemini-2.5-pro"`) || strings.Contains(got, `"old"`) {
		t.Errorf("existing modelId not replaced: %s", got)
	}

	withoutModel := `{"configId":"c","streamAssistRequest":{"query":{"parts":[{"text":"q"}]}}}`
	got = injectTemplateModelID(withoutModel, "gemini-2.5-flash")
	if !strings.Contains(got, `"assistGenerationConfig":{"modelId":"gemini-2.5-flash"}`) {
		t.Errorf("modelId not inserted: %s", got)
	}
	if !strings.Contains(got, `"query":{"parts":[{"text":"q"}]}`) {
		t.Errorf("query lost while inserting modelId: %s", got)
	}

	if got := injectTemplateModelID(withoutModel, ""); got != withoutModel {
		t.Errorf("empty model id must leave the body untouched")
	}
}
