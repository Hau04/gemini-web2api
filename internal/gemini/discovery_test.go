package gemini

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

func TestComputeCapacity(t *testing.T) {
	tests := []struct {
		name      string
		tier      []any
		capab     []any
		wantCap   int
		wantField int
	}{
		{
			name:      "Tier 21 override field 13",
			tier:      []any{float64(21)},
			capab:     []any{},
			wantCap:   1,
			wantField: 13,
		},
		{
			name:      "Plus accounts (capab 115)",
			tier:      []any{float64(8)},
			capab:     []any{float64(115)},
			wantCap:   4,
			wantField: 12,
		},
		{
			name:      "Pro accounts (tier 16 or capab 106)",
			tier:      []any{float64(16)},
			capab:     []any{},
			wantCap:   3,
			wantField: 12,
		},
		{
			name:      "Pro accounts (tier 8 or capab 19)",
			tier:      []any{float64(8), float64(0)},
			capab:     []any{float64(2), float64(140)},
			wantCap:   2,
			wantField: 12,
		},
		{
			name:      "Free accounts fallback",
			tier:      []any{float64(1)},
			capab:     []any{float64(2)},
			wantCap:   1,
			wantField: 12,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCap, gotField := computeCapacity(tt.tier, tt.capab)
			if gotCap != tt.wantCap || gotField != tt.wantField {
				t.Errorf("computeCapacity() = (%d, %d), want (%d, %d)", gotCap, gotField, tt.wantCap, tt.wantField)
			}
		})
	}
}

func TestResolveModelHeader(t *testing.T) {
	tmpDir := t.TempDir()
	cookiePath := filepath.Join(tmpDir, "gemini-auth.json")
	if err := os.WriteFile(cookiePath, []byte(`{"cookie":"dummy_cookie=xyz; SAPISID=abc123"}`), 0600); err != nil {
		t.Fatalf("failed to create temp cookie file: %v", err)
	}

	client := NewClient(config.Default())
	client.Cookies = NewCookieCache(cookiePath)
	client.accountCap = 2
	client.accountCapFld = 12
	client.discovered = []DiscoveredModel{
		{
			ID:          "95a4c0abcba24364",
			Category:    "Argon",
			Display:     "4 Argon",
			Desc:        "Complex tasks",
			Number:      8,
			Capacity:    2,
			CapacityFld: 12,
		},
		{
			ID:          "56fdd199312815e2",
			Category:    "Flash",
			Display:     "3.8 Flash",
			Desc:        "All-around help",
			Number:      1,
			Capacity:    2,
			CapacityFld: 12,
		},
	}

	reqUUID := "TEST-UUID-1234"

	// Case 1: Authenticated session matching discovered model (Argon)
	optArgon := ModelOptions{
		ModelName:   "gemini-4-argon",
		ModelID:     8,
		ThinkMode:   4,
		ModelHeader: "95a4c0abcba24364",
	}
	hdr, effMode := client.resolveModelHeader(optArgon, reqUUID)
	if effMode != 8 {
		t.Errorf("effMode = %d, want 8", effMode)
	}
	if !strings.Contains(hdr, `"95a4c0abcba24364"`) {
		t.Errorf("expected header to contain model ID, got %s", hdr)
	}
	if !strings.Contains(hdr, `TEST-UUID-1234`) {
		t.Errorf("expected header to contain reqUUID, got %s", hdr)
	}

	// Case 2: Static fallback when discovered list is empty but ModelHeader is configured
	client.discovered = nil
	hdrStatic, effModeStatic := client.resolveModelHeader(optArgon, reqUUID)
	if effModeStatic != 8 {
		t.Errorf("effModeStatic = %d, want 8", effModeStatic)
	}
	if !strings.Contains(hdrStatic, `"95a4c0abcba24364"`) {
		t.Errorf("expected fallback header to contain model ID, got %s", hdrStatic)
	}

	// Case 3: Anonymous session (no cookie) must NOT attach model header even for Argon
	anonClient := NewClient(config.Default())
	anonClient.Cookies = nil
	hdrAnon, effModeAnon := anonClient.resolveModelHeader(optArgon, reqUUID)
	if hdrAnon != "" {
		t.Errorf("expected empty header for anonymous session, got %s", hdrAnon)
	}
	if effModeAnon != 8 {
		t.Errorf("effModeAnon = %d, want 8", effModeAnon)
	}

	// Case 4: Standard / legacy model without ModelHeader (e.g. gemini-3.5-flash-thinking)
	// Must NOT attach model header and must preserve its own ModelID
	optThinking := ModelOptions{
		ModelName:   "gemini-3.5-flash-thinking",
		ModelID:     1,
		ThinkMode:   2,
		ModelHeader: "",
		Extra:       map[int]any{80: 2},
	}
	hdrThinking, effModeThinking := client.resolveModelHeader(optThinking, reqUUID)
	if hdrThinking != "" {
		t.Errorf("expected empty header for model without ModelHeader, got %s", hdrThinking)
	}
	if effModeThinking != 1 {
		t.Errorf("effModeThinking = %d, want 1", effModeThinking)
	}

	// Case 5: Thinking tail logic
	optThinkingEnabled := ModelOptions{
		ModelName:   "gemini-4-argon",
		ModelID:     8,
		ModelHeader: "95a4c0abcba24364",
		Extra:       map[int]any{80: 2},
	}
	hdrThinkingEnabled, _ := client.resolveModelHeader(optThinkingEnabled, reqUUID)
	if !strings.Contains(hdrThinkingEnabled, ",2,\"TEST-UUID-1234\"]") {
		t.Errorf("expected thinking tail 2 in header, got %s", hdrThinkingEnabled)
	}
}

func TestResolveModelHeaderMatchesDiscoveredByName(t *testing.T) {
	tmpDir := t.TempDir()
	cookiePath := filepath.Join(tmpDir, "gemini-auth.json")
	if err := os.WriteFile(cookiePath, []byte(`{"cookie":"dummy_cookie=xyz; SAPISID=abc123"}`), 0600); err != nil {
		t.Fatalf("failed to create temp cookie file: %v", err)
	}

	client := NewClient(config.Default())
	client.Cookies = NewCookieCache(cookiePath)
	// Registry reuses hydrogen's header ID for every 4.x model, but discovery
	// knows each model's true upstream ID (Enterprise picker order).
	client.discovered = []DiscoveredModel{
		{ID: "aaaaaaaaaaaaaaaa", Display: "4 Hydrogen", Category: "Hydrogen", Number: 8, Capacity: 2, CapacityFld: 12},
		{ID: "bbbbbbbbbbbbbbbb", Display: "4 Carbon", Category: "Carbon", Number: 9, Capacity: 2, CapacityFld: 12},
		{ID: "cccccccccccccccc", Display: "4 Argon", Category: "Argon", Number: 10, Capacity: 2, CapacityFld: 12},
		{ID: "dddddddddddddddd", Display: "3.8 Flash", Category: "Flash", Number: 1, Capacity: 2, CapacityFld: 12},
		{ID: "eeeeeeeeeeeeeeee", Display: "3.5 Flash", Category: "Flash", Number: 2, Capacity: 2, CapacityFld: 12},
		{ID: "ffffffffffffffff", Display: "2.5 Pro", Category: "Pro", Number: 3, Capacity: 2, CapacityFld: 12},
	}

	// Argon requested with the shared registry header: must route to argon's
	// discovered ID, NOT hydrogen's static one.
	hdr, mode := client.resolveModelHeader(ModelOptions{
		ModelName: "gemini-4-argon", ModelID: 8, ThinkMode: 4, ModelHeader: "aaaaaaaaaaaaaaaa",
	}, "UUID-1")
	if !strings.Contains(hdr, `"cccccccccccccccc"`) || mode != 10 {
		t.Errorf("argon must route to discovered argon ID, got header=%s mode=%d", hdr, mode)
	}

	// Carbon likewise.
	hdr, mode = client.resolveModelHeader(ModelOptions{
		ModelName: "carbon", ModelID: 8, ThinkMode: 4, ModelHeader: "aaaaaaaaaaaaaaaa",
	}, "UUID-2")
	if !strings.Contains(hdr, `"bbbbbbbbbbbbbbbb"`) || mode != 9 {
		t.Errorf("carbon must route to discovered carbon ID, got header=%s mode=%d", hdr, mode)
	}

	// Versioned flash models are told apart (3.8 vs 3.5).
	hdr, _ = client.resolveModelHeader(ModelOptions{
		ModelName: "gemini-3.8-flash", ModelID: 1, ThinkMode: 4, ModelHeader: "",
	}, "UUID-3")
	if !strings.Contains(hdr, `"dddddddddddddddd"`) {
		t.Errorf("3.8-flash must attach the discovered 3.8 header, got %s", hdr)
	}
	hdr, _ = client.resolveModelHeader(ModelOptions{
		ModelName: "gemini-3.5-flash", ModelID: 1, ThinkMode: 4, ModelHeader: "",
	}, "UUID-4")
	if !strings.Contains(hdr, `"eeeeeeeeeeeeeeee"`) {
		t.Errorf("3.5-flash must attach the discovered 3.5 header, got %s", hdr)
	}

	// Headerless pro model picks up its discovered upstream ID by name.
	hdr, mode = client.resolveModelHeader(ModelOptions{
		ModelName: "gemini-2.5-pro", ModelID: 3, ThinkMode: 4, ModelHeader: "",
	}, "UUID-5")
	if !strings.Contains(hdr, `"ffffffffffffffff"`) || mode != 3 {
		t.Errorf("2.5-pro must route via discovered ID, got header=%s mode=%d", hdr, mode)
	}

	// Unknown name: no name match, static registry header preserved.
	hdr, _ = client.resolveModelHeader(ModelOptions{
		ModelName: "gemini-4", ModelID: 8, ThinkMode: 4, ModelHeader: "aaaaaaaaaaaaaaaa",
	}, "UUID-6")
	if !strings.Contains(hdr, `"aaaaaaaaaaaaaaaa"`) {
		t.Errorf("indistinguishable alias must keep its static header, got %s", hdr)
	}

	// Hydrogen requested: name-matches its own discovered entry (same ID the
	// registry happens to carry — the value the old code always used).
	hdr, _ = client.resolveModelHeader(ModelOptions{
		ModelName: "gemini-4-hydrogen", ModelID: 8, ThinkMode: 4, ModelHeader: "aaaaaaaaaaaaaaaa",
	}, "UUID-7")
	if !strings.Contains(hdr, `"aaaaaaaaaaaaaaaa"`) {
		t.Errorf("hydrogen must name-match its discovered entry, got %s", hdr)
	}
}

func TestBuildHeadersUsesSyncedOriginOverConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := tmpDir + "/cookie.json"
	content := `{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc","xsrf_token":"tok","base_url":"https://vertexaisearch.cloud.google.com"}`
	if err := os.WriteFile(cookieFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Config{
		CookieFile: cookieFile,
		BaseURL:    "https://gemini.google.com",
	}
	c := NewClient(cfg)
	c.Cookies = NewCookieCache(cookieFile)

	h := c.buildHeaders()
	if got := h.Get("Origin"); got != "https://vertexaisearch.cloud.google.com" {
		t.Errorf("Origin = %q, want synced vertexaisearch origin (config base_url must not win)", got)
	}
	if got := h.Get("Referer"); !strings.HasPrefix(got, "https://vertexaisearch.cloud.google.com/") {
		t.Errorf("Referer = %q, want vertexaisearch origin", got)
	}
}

func TestBuildHeadersFallsBackToConfigBaseURL(t *testing.T) {
	tmpDir := t.TempDir()
	cookieFile := tmpDir + "/cookie.json"
	content := `{"cookie":"SAPISID=abc; __Secure-1PSID=xyz","sapisid":"abc"}`
	if err := os.WriteFile(cookieFile, []byte(content), 0o600); err != nil {
		t.Fatalf("write cookie file: %v", err)
	}

	cfg := config.Config{
		CookieFile: cookieFile,
		BaseURL:    "https://vertexaisearch.cloud.google.com",
	}
	c := NewClient(cfg)
	c.Cookies = NewCookieCache(cookieFile)

	h := c.buildHeaders()
	if got := h.Get("Origin"); got != "https://vertexaisearch.cloud.google.com" {
		t.Errorf("Origin = %q, want configured base_url when no synced origin", got)
	}
}

func TestTriageStatus404LatchesBardUnsupported(t *testing.T) {
	c := NewClient(config.Config{})
	if c.BardUnsupported() {
		t.Fatal("fresh client must not be marked unsupported")
	}
	resp := &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}
	err := c.triageStatus(resp)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !c.BardUnsupported() {
		t.Fatal("404 must latch BardUnsupported")
	}
	c.ClearBardUnsupported()
	if c.BardUnsupported() {
		t.Fatal("ClearBardUnsupported must reset the latch")
	}
}

func TestTriageStatus500DoesNotLatchBardUnsupported(t *testing.T) {
	c := NewClient(config.Config{})
	resp := &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("err")), Header: make(http.Header)}
	_ = c.triageStatus(resp)
	if c.BardUnsupported() {
		t.Fatal("non-404 status must not latch BardUnsupported")
	}
}
