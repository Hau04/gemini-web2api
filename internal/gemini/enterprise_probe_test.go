package gemini

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

// A tenant that accepts the candidate id must make it stick: probe succeeds,
// the id is cached, and EnterpriseModelIDSource reports it as learned.
func TestProbeAndLearnModelCachesAcceptedID(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seenBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:              cookieFile,
		BaseURL:                 srv.URL,
		DiscoveryEngineConfigID: "cfg-1",
		DiscoveryEngineEndpoint: srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
	})
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, format) }

	learned, err := c.ProbeAndLearnModel("gemini-4-hydrogen", "gemini-4-hydrogen")
	if err != nil {
		t.Fatalf("probe should succeed against an accepting tenant: %v", err)
	}
	if learned != "gemini-4-hydrogen" {
		t.Errorf("learned = %q", learned)
	}
	if !strings.Contains(seenBody, `"assistGenerationConfig":{"modelId":"gemini-4-hydrogen"}`) {
		t.Errorf("probe must send the candidate as the modelId, body: %s", seenBody)
	}

	id, source := c.EnterpriseModelIDSource("gemini-4-hydrogen")
	if id != "gemini-4-hydrogen" || source != ModelIDSourceLearned {
		t.Fatalf("EnterpriseModelIDSource() = (%q, %q), want the learned id", id, source)
	}
	// The same alias in different case still hits the cache.
	if id, _ := c.EnterpriseModelIDSource("GEMINI-4-HYDROGEN"); id != "gemini-4-hydrogen" {
		t.Errorf("alias lookup should be case-insensitive, got %q", id)
	}

	raw, err := os.ReadFile(filepath.Join(dir, enterpriseModelCacheFile))
	if err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
	var cached map[string]string
	if err := json.Unmarshal(raw, &cached); err != nil || cached["gemini-4-hydrogen"] != "gemini-4-hydrogen" {
		t.Errorf("cache contents = %s", raw)
	}
}

// A rejected candidate must not be cached, and the error must quote what the
// tenant said so the right id can be tried next.
func TestProbeRejectedModelIsNotCached(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Invalid model id","status":"INVALID_ARGUMENT"}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:              cookieFile,
		BaseURL:                 srv.URL,
		DiscoveryEngineConfigID: "cfg-1",
		DiscoveryEngineEndpoint: srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
	})
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	_, err := c.ProbeAndLearnModel("gemini-4-hydrogen", "nope-1")
	if err == nil {
		t.Fatal("expected the rejection to surface")
	}
	if !strings.Contains(err.Error(), "Invalid model id") || !strings.Contains(err.Error(), "nope-1") {
		t.Errorf("error should quote the tenant's message and the candidate: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, enterpriseModelCacheFile)); statErr == nil {
		t.Error("a rejected candidate must not be cached")
	}
	if _, source := c.EnterpriseModelIDSource("gemini-4-hydrogen"); source == ModelIDSourceLearned {
		t.Error("rejected candidate must not become the resolved source")
	}
}

// The widget needs a session on the upload; without a bearer token it must still
// be created, and the session must travel inside the addContextFileRequest.
func TestUploadCreatesSessionForCookieAuth(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var createCalls, uploadCalls int
	var uploadBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "widgetCreateSession"):
			createCalls++
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/42"}}}`))
		case strings.Contains(path, "widgetAddContextFile"):
			uploadCalls++
			raw, _ := io.ReadAll(r.Body)
			uploadBody = string(raw)
			_, _ = w.Write([]byte(`{"addContextFileResponse":{"session":"collections/c/engines/e/sessions/42","fileId":"f-1"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:                    cookieFile,
		BaseURL:                       srv.URL,
		DiscoveryEngineConfigID:       "cfg-1",
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
	})
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	if _, _, err := c.UploadEnterpriseFile(RawFile{Name: "a.txt", MIME: "text/plain", Data: []byte("x")}); err != nil {
		t.Fatalf("upload should succeed with a session: %v", err)
	}
	if createCalls != 1 {
		t.Errorf("cookie-only uploads must still create a session, got %d createSession calls", createCalls)
	}
	if uploadCalls != 1 {
		t.Errorf("upload calls = %d", uploadCalls)
	}
	if !strings.Contains(uploadBody, `"session":"collections/c/engines/e/sessions/42"`) {
		t.Errorf("upload body must carry the session, got: %s", uploadBody)
	}
}

// A direct-mode request for an unmapped alias must ask the tenant once, then use
// the learned id — that is what stops Enterprise from answering "Auto".
func TestDirectStreamProbesUnmappedAliasOnce(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Session bootstrap is not a streamAssist call; only count those.
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		if i := strings.Index(body, `"modelId":"`); i >= 0 {
			rest := body[i+len(`"modelId":"`):]
			models = append(models, rest[:strings.Index(rest, `"`)])
		} else {
			models = append(models, "")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:                 cookieFile,
		BaseURL:                    srv.URL,
		RetryAttempts:              1,
		DiscoveryEngineConfigID:    "cfg-1",
		DiscoveryEngineEndpoint:    srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
		DiscoveryEngineProbeModels: true,
	})
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	if err := c.generateViaDiscoveryDirectStream("hello", nil, "", "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("direct stream failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected one probe plus the real request, got %d calls: %v", len(models), models)
	}
	if models[0] != "gemini-4-hydrogen" || models[1] != "gemini-4-hydrogen" {
		t.Errorf("both calls should carry the learned model id, got %v", models)
	}

	// Second request: the id is cached, so no further probe.
	models = nil
	if err := c.generateViaDiscoveryDirectStream("again", nil, "", "gemini-4-hydrogen", nil, func(string) error { return nil }); err != nil {
		t.Fatalf("second direct stream failed: %v", err)
	}
	if len(models) != 1 || models[0] != "gemini-4-hydrogen" {
		t.Errorf("cached alias must not be probed again, calls = %v", models)
	}
}

// A tenant that rejects the alias must not be probed again, and the request must
// still go through (with no modelId) so the user is not blocked.
func TestDirectStreamFallsBackWhenProbeRejected(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")

	var calls, probes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Session bootstrap is not a streamAssist call; only count those.
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/1"}}}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		calls++
		if strings.Contains(string(raw), `"assistGenerationConfig"`) {
			probes++
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Invalid model","status":"INVALID_ARGUMENT"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"streamAssistResponse":{"answer":{"state":"SUCCEEDED","replies":[{"groundedContent":{"content":{"text":"ok"}}}]}}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:                 cookieFile,
		BaseURL:                    srv.URL,
		RetryAttempts:              1,
		DiscoveryEngineConfigID:    "cfg-1",
		DiscoveryEngineEndpoint:    srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
		DiscoveryEngineProbeModels: true,
	})
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, format) }

	for i := 0; i < 2; i++ {
		if err := c.generateViaDiscoveryDirectStream("hi", nil, "", "gemini-4-neon", nil, func(string) error { return nil }); err != nil {
			t.Fatalf("request %d should still succeed without a model id: %v", i, err)
		}
	}
	if probes != 1 {
		t.Errorf("a rejected alias must be probed only once, got %d", probes)
	}
	if calls != 3 { // 1 probe + 2 requests
		t.Errorf("calls = %d, want 3", calls)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "does not accept") {
		t.Errorf("log should say the tenant rejected the alias, got %v", logs)
	}
	// Probing must be switchable off.
	off := NewClient(config.Config{
		CookieFile:              cookieFile,
		BaseURL:                 srv.URL,
		RetryAttempts:           1,
		DiscoveryEngineConfigID: "cfg-1",
		DiscoveryEngineEndpoint: srv.URL + "/v1alpha/locations/global/widgetStreamAssist?key=k",
	})
	off.HTTP = srv.Client()
	off.Logf = func(string, ...any) {}
	probes = 0
	if err := off.generateViaDiscoveryDirectStream("hi", nil, "", "gemini-4-carbon", nil, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if probes != 0 {
		t.Errorf("discovery_engine_probe_models=false must disable probing, got %d", probes)
	}
}
