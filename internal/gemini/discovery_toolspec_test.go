package gemini

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func toolspecClient(t *testing.T, mutate func(*config.Config)) (*Client, *[]string) {
	t.Helper()
	cfg := config.Config{CookieFile: filepath.Join(t.TempDir(), "cookie.json")}
	if mutate != nil {
		mutate(&cfg)
	}
	logs := &[]string{}
	c := NewClient(cfg)
	c.Logf = func(format string, args ...any) {
		*logs = append(*logs, fmt.Sprintf(format, args...))
	}
	return c, logs
}

// The direct path used to hardcode one toolsSpec guess. The app's own value wins.
func TestToolsSpecFromConfig(t *testing.T) {
	c, _ := toolspecClient(t, func(cfg *config.Config) {
		cfg.DiscoveryEngineToolsSpec = `{"toolRegistry":"projects/p/locations/global/toolRegistries/rea"}`
	})
	spec, source := c.enterpriseToolsSpec()
	if source != "config" {
		t.Errorf("source = %q, want config", source)
	}
	if !strings.Contains(spec, "toolRegistries/rea") {
		t.Errorf("spec = %q, want the configured registry", spec)
	}
}

func TestToolsSpecInvalidConfigFallsBack(t *testing.T) {
	c, logs := toolspecClient(t, func(cfg *config.Config) {
		cfg.DiscoveryEngineToolsSpec = `{"toolRegistry":` // truncated paste
	})
	spec, source := c.enterpriseToolsSpec()
	if spec != DefaultEnterpriseToolsSpec || source != "default" {
		t.Errorf("an invalid override must not be sent: spec=%q source=%q", spec, source)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "not valid JSON") {
		t.Errorf("the invalid override should be reported, got %v", *logs)
	}
}

// A captured template is the app's own body, so its toolsSpec is authoritative.
func TestToolsSpecFromCapturedTemplate(t *testing.T) {
	dir := t.TempDir()
	cookieFile := filepath.Join(dir, "cookie.json")
	tpl := `{"url":"https://discoveryengine.clients6.google.com/x","method":"POST","body":` +
		`"{\"configId\":\"cfg\",\"streamAssistRequest\":{\"query\":{\"parts\":[{\"text\":\"hi\"}]},\"toolsSpec\":{\"toolRegistry\":\"projects/p/locations/global/toolRegistries/rea-mcp\"}}}"}`
	if err := os.WriteFile(discoveryTemplatePath(cookieFile), []byte(tpl), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewClient(config.Config{CookieFile: cookieFile})
	c.Logf = func(string, ...any) {}
	spec, source := c.enterpriseToolsSpec()
	if source != "captured template" {
		t.Fatalf("source = %q, want captured template", source)
	}
	if !strings.Contains(spec, "rea-mcp") {
		t.Errorf("spec = %q, want the app's own registry", spec)
	}
}

func TestToolsSpecDefaultWithoutTemplate(t *testing.T) {
	c, _ := toolspecClient(t, nil)
	spec, source := c.enterpriseToolsSpec()
	if spec != DefaultEnterpriseToolsSpec || source != "default" {
		t.Errorf("spec=%q source=%q, want the captured default", spec, source)
	}
}

func TestExtractToolsSpec(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{"widget wrapper", `{"configId":"c","streamAssistRequest":{"query":{"parts":[{"text":"hi"}]},"toolsSpec":{"toolRegistry":"r"}}}`, `{"toolRegistry":"r"}`, true},
		{"rest snake case", `{"query":{"parts":[{"text":"hi"}]},"tools_spec":{"web_grounding_spec":{}}}`, `{"web_grounding_spec":{}}`, true},
		{"no toolsSpec", `{"query":{"parts":[{"text":"hi"}]}}`, "", false},
		{"empty toolsSpec", `{"query":{"parts":[{"text":"hi"}]},"toolsSpec":{}}`, "", false},
		{"not json", `not json at all`, "", false},
	}
	for _, tc := range cases {
		got, ok := extractToolsSpec(tc.body)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("%s: spec = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The log must say which toolsSpec was asked for — that is how a missing MCP
// tool becomes diagnosable instead of a guess about the model.
func TestToolsSpecIsLogged(t *testing.T) {
	c, logs := toolspecClient(t, nil)
	spec, source := c.enterpriseToolsSpec()
	c.logToolsSpec(spec, source)
	joined := strings.Join(*logs, "\n")
	if !strings.Contains(joined, "toolsSpec from default") || !strings.Contains(joined, "toolRegistry") {
		t.Errorf("toolsSpec log missing: %v", *logs)
	}
}
