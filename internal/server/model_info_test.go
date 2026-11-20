package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/config"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func TestEnterpriseModelIDSource(t *testing.T) {
	cfg := config.Default()
	cfg.DiscoveryEngineModels = map[string]string{"gemini-4-hydrogen": "gemini-2.5-flash"}
	c := gemini.NewClient(cfg)
	c.Logf = func(string, ...any) {}

	cases := []struct {
		requested string
		wantID    string
		wantSrc   string
	}{
		{"gemini-4-hydrogen", "gemini-2.5-flash", gemini.ModelIDSourceMapping},
		{"HYDROGEN", "", gemini.ModelIDSourceTenantDefault},
		{"gemini-2.5-pro", "gemini-2.5-pro", gemini.ModelIDSourcePassthrough},
		{"gemini-4-neon", "", gemini.ModelIDSourceTenantDefault},
	}
	for _, tc := range cases {
		id, src := c.EnterpriseModelIDSource(tc.requested)
		if id != tc.wantID || src != tc.wantSrc {
			t.Errorf("EnterpriseModelIDSource(%q) = (%q, %q), want (%q, %q)",
				tc.requested, id, src, tc.wantID, tc.wantSrc)
		}
	}

	cfg.DiscoveryEngineDefaultModelID = "gemini-2.5-flash"
	c = gemini.NewClient(cfg)
	c.Logf = func(string, ...any) {}
	if id, src := c.EnterpriseModelIDSource("gemini-4-neon"); id != "gemini-2.5-flash" || src != gemini.ModelIDSourceConfigured {
		t.Errorf("unmapped alias should fall back to the configured default, got (%q, %q)", id, src)
	}
}

// The endpoint must state plainly that the client's model choice is ignored while
// discovery_engine_models is empty — that is the "why does it answer as Auto" answer.
func TestModelInfoExplainsIgnoredSelection(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	handler := app.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/model-info?model=gemini-4-hydrogen", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/model-info = %d: %s", rec.Code, rec.Body.String())
	}
	var info modelSelectionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Origin != originEnterprise {
		t.Errorf("origin = %q", info.Origin)
	}
	if info.Resolved.Name != "gemini-4-hydrogen" {
		t.Errorf("resolved model = %q", info.Resolved.Name)
	}
	if info.Upstream.Source != gemini.ModelIDSourceTenantDefault || info.Upstream.SelectionLive {
		t.Errorf("upstream = %+v, want tenant-default and not live", info.Upstream)
	}
	if len(info.Warnings) == 0 || !strings.Contains(strings.Join(info.Warnings, " "), "INACTIVE") {
		t.Fatalf("expected an INACTIVE warning, got %v", info.Warnings)
	}

	// With a mapping the selection becomes live and the warning disappears.
	mapped := newEnterpriseApp(t, func(cfg *config.Config) {
		cfg.DiscoveryEngineModels = map[string]string{"gemini-4-hydrogen": "gemini-2.5-flash"}
	})
	rec = httptest.NewRecorder()
	mapped.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/model-info?model=gemini-4-hydrogen", nil))
	info = modelSelectionInfo{}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Upstream.ModelID != "gemini-2.5-flash" || info.Upstream.Source != gemini.ModelIDSourceMapping || !info.Upstream.SelectionLive {
		t.Errorf("mapped upstream = %+v", info.Upstream)
	}
	if len(info.Warnings) != 0 {
		t.Errorf("mapped model should not warn, got %v", info.Warnings)
	}
}

func TestModelInfoCoversConsumerOrigin(t *testing.T) {
	cfg := config.Default()
	cfg.LogRequests = false
	cfg.BaseURL = "https://gemini.google.com"
	app := New(cfg, "test")

	info := app.describeModelSelection("gemini-4-hydrogen", mustResolve(t, "gemini-4-hydrogen"))
	if info.Origin != originConsumer {
		t.Errorf("origin = %q", info.Origin)
	}
	if info.Upstream.ModelID != "gemini-4-hydrogen" || info.Upstream.Source != "alias" || !info.Upstream.SelectionLive {
		t.Errorf("consumer upstream = %+v", info.Upstream)
	}
	if len(info.Warnings) != 0 {
		t.Errorf("consumer origin should not carry Enterprise warnings: %v", info.Warnings)
	}
}

// Response headers must carry the effective upstream model so clients can show it.
func TestReportModelSelectionSetsHeaders(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	resolved := mustResolve(t, "gemini-4-hydrogen")
	rec := httptest.NewRecorder()
	app.reportModelSelection(rec, "gemini-4-hydrogen", resolved)

	if got := rec.Header().Get("X-Upstream-Model"); got != "tenant-default" {
		t.Errorf("X-Upstream-Model = %q, want tenant-default", got)
	}
	if got := rec.Header().Get("X-Model-Selection"); got != "ignored-by-tenant" {
		t.Errorf("X-Model-Selection = %q, want ignored-by-tenant", got)
	}

	mapped := newEnterpriseApp(t, func(cfg *config.Config) {
		cfg.DiscoveryEngineModels = map[string]string{"gemini-4-hydrogen": "gemini-2.5-flash"}
	})
	rec = httptest.NewRecorder()
	mapped.reportModelSelection(rec, "gemini-4-hydrogen", resolved)
	if got := rec.Header().Get("X-Upstream-Model"); got != "gemini-2.5-flash" {
		t.Errorf("X-Upstream-Model = %q", got)
	}
	if got := rec.Header().Get("X-Model-Selection"); got != "live" {
		t.Errorf("X-Model-Selection = %q, want live", got)
	}
}

// A tab that cannot switch to the requested model must be reported, not guessed.
func TestBridgeModelStateIsStoredAndExposed(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	var logs []string
	app.Logf = func(format string, args ...any) {
		logs = append(logs, strings.TrimSpace(strings.ReplaceAll(format, "%q", "\"")))
	}
	handler := app.Handler()

	body := `{"requested":"gemini-4-hydrogen","applied":"Auto","ok":false,"tab":"vertexaisearch.cloud.google.com"}`
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/bridge/modelstate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /bridge/modelstate = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/bridge/status", nil))
	var status map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	sel, _ := status["model_selection"].(map[string]any)
	if sel == nil || sel["requested"] != "gemini-4-hydrogen" || sel["applied"] != "Auto" || sel["ok"] != false {
		t.Fatalf("model_selection = %v", status["model_selection"])
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "could not select") {
		t.Errorf("expected a model-selection warning, got: %s", joined)
	}
}

// withCookies writes a synced cookie file so direct mode is genuinely available;
// routing decisions then depend on the model, not on a missing cookie jar.
func withCookies(t *testing.T, app *App) {
	t.Helper()
	payload := `{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`
	if err := os.WriteFile(app.Cfg.CookieFile, []byte(payload), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}
}

func mustResolve(t *testing.T, name string) models.Resolved {
	t.Helper()
	resolved, err := models.Resolve(name, "")
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// Direct mode cannot name an alias the tenant has no id for; routing must fall
// back to the bridge picker instead of silently answering as Auto.
func TestDirectCanHonourModel(t *testing.T) {
	unmapped := newEnterpriseApp(t, nil)
	hydrogen := mustResolve(t, "gemini-4-hydrogen")
	if unmapped.directCanHonourModel(hydrogen) {
		t.Error("unmapped alias on an Enterprise origin must not claim direct-mode support")
	}
	passthrough := mustResolve(t, "gemini-2.5-pro")
	if !unmapped.directCanHonourModel(passthrough) {
		t.Error("a tenant model id must be honoured by direct mode")
	}

	mapped := newEnterpriseApp(t, func(cfg *config.Config) {
		cfg.DiscoveryEngineModels = map[string]string{"gemini-4-hydrogen": "gemini-2.5-flash"}
	})
	if !mapped.directCanHonourModel(hydrogen) {
		t.Error("a mapped alias must be honoured by direct mode")
	}

	consumerCfg := config.Default()
	consumerCfg.LogRequests = false
	consumerCfg.BaseURL = "https://gemini.google.com"
	consumer := New(consumerCfg, "test")
	if !consumer.directCanHonourModel(hydrogen) {
		t.Error("consumer origins route the alias itself, so direct mode is fine")
	}
}

// The tab's picker is the transport that actually selects the model, so it must
// show up as a live selection rather than "ignored by tenant".
func TestModelInfoReportsBridgePickerSelection(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	app.Logf = func(string, ...any) {}
	handler := app.Handler()

	hb := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, hb)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/model-info?model=gemini-4-hydrogen", nil))
	var info modelSelectionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Upstream.Source != "bridge-picker" || !info.Upstream.SelectionLive {
		t.Fatalf("upstream = %+v, want a live bridge-picker selection", info.Upstream)
	}
	if !strings.Contains(strings.Join(info.Warnings, " "), "model picker") {
		t.Errorf("warning should explain the picker dependency, got %v", info.Warnings)
	}
}

// End to end: an unmapped Enterprise alias with a live tab must be served by the
// bridge (which selects the model), not by direct mode (which cannot).
func TestEnterpriseUnmappedModelRoutesThroughBridge(t *testing.T) {
	app := newEnterpriseApp(t, nil)
	app.Logf = func(string, ...any) {}
	app.Gem.HTTP = failingRequester{}
	withCookies(t, app)
	handler := app.Handler()

	hb := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, hb)
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat = %d", rec.Code)
	}

	got := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 3*time.Second)
		if err == nil && req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
			got <- true
			return
		}
		got <- false
	}()

	body := `{"model":"gemini-4-hydrogen","messages":[{"role":"user","content":"hello there"}]}`
	rec = httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		close(done)
	}()

	if !<-got {
		t.Fatal("request must go through the bridge so the tab's picker can select the model")
	}
	<-done // the handler must finish before the recorder is read (-race)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 via bridge, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A mapped alias must keep using direct mode (no tab dependency).
func TestEnterpriseMappedModelStaysDirect(t *testing.T) {
	app := newEnterpriseApp(t, func(cfg *config.Config) {
		cfg.DiscoveryEngineModels = map[string]string{"gemini-4-hydrogen": "gemini-2.5-flash"}
	})
	app.Logf = func(string, ...any) {}
	app.Gem.HTTP = failingRequester{}
	withCookies(t, app)
	handler := app.Handler()

	hb := httptest.NewRequest("POST", "/bridge/heartbeat", strings.NewReader(`{"tab":"vertexaisearch.cloud.google.com","client_id":"tab_1"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, hb)

	taken := make(chan bool, 1)
	go func() {
		req, err := app.Bridge.WaitForNextRequestForClient(context.Background(), "vertexaisearch.cloud.google.com", "tab_1", 1500*time.Millisecond)
		if err == nil && req != nil {
			_ = app.Bridge.SubmitChunk(bridge.Chunk{ID: req.ID, Text: "ok", Done: true})
			taken <- true
			return
		}
		taken <- false
	}()

	body := `{"model":"gemini-4-hydrogen","messages":[{"role":"user","content":"hello there"}]}`
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if <-taken {
		t.Fatal("a mapped model must not wait on the browser tab")
	}
}
