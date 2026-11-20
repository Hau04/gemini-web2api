package gemini

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/config"
)

// The live widget rejects fields it does not know by name:
//
//	{"error":{"code":400,"message":"Invalid JSON payload received. Unknown name
//	 \"session\" at 'add_context_file_request': Cannot find field.", ...}}
//
// Guessing which subset a tenant accepts wastes a round trip per message; the
// error names the field, so drop it and retry, then remember it.
func TestUploadDropsFieldTheWidgetRejects(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	rejected := map[string]bool{"session": true}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/7"}}}`))
			return
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		var payload struct {
			AddContextFileRequest map[string]any `json:"addContextFileRequest"`
		}
		_ = json.Unmarshal(raw, &payload)
		for _, field := range []string{"session", "name"} {
			if rejected[field] {
				if _, present := payload.AddContextFileRequest[field]; present {
					// Faithful to google.rpc: the field name arrives JSON-escaped.
					msg := `Unknown name \"` + field + `\" at 'add_context_file_request': Cannot find field.`
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Invalid JSON payload received. ` + msg + `","status":"INVALID_ARGUMENT"}}`))
					return
				}
			}
		}
		_, _ = w.Write([]byte(`{"addContextFileResponse":{"fileId":"f-1"}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:                    filepath.Join(t.TempDir(), "cookie.json"),
		BaseURL:                       srv.URL,
		DiscoveryEngineConfigID:       "cfg-1",
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
	})
	c.HTTP = srv.Client()
	var logs []string
	c.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	fileID, _, err := c.UploadEnterpriseFile(RawFile{Name: "a.txt", MIME: "text/plain", Data: []byte("hello")})
	if err != nil {
		t.Fatalf("upload should recover from an unknown field: %v", err)
	}
	if fileID != "f-1" {
		t.Errorf("fileID = %q", fileID)
	}
	if len(bodies) < 2 {
		t.Fatalf("expected a retry after the unknown-field rejection, got %d requests", len(bodies))
	}
	if strings.Contains(bodies[1], `"session"`) {
		t.Errorf("the rejected field was sent again: %s", bodies[1])
	}
	if !strings.Contains(strings.Join(logs, "\n"), `does not accept "session"`) {
		t.Errorf("the log should name the dropped field: %v", logs)
	}

	// A second upload starts from the accepted shape: no extra round trip.
	before := len(bodies)
	if _, _, err := c.UploadEnterpriseFile(RawFile{Name: "b.txt", MIME: "text/plain", Data: []byte("second")}); err != nil {
		t.Fatalf("second upload failed: %v", err)
	}
	if len(bodies) != before+1 {
		t.Errorf("dropped fields were not remembered: %d requests for one upload", len(bodies)-before)
	}
	if strings.Contains(bodies[len(bodies)-1], `"session"`) {
		t.Errorf("remembered field leaked back in: %s", bodies[len(bodies)-1])
	}
}

func TestUploadKeepsSessionWhenTheWidgetAcceptsIt(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "widgetCreateSession") {
			_, _ = w.Write([]byte(`{"createSessionResponse":{"session":{"name":"collections/c/engines/e/sessions/9"}}}`))
			return
		}
		body = string(raw)
		_, _ = w.Write([]byte(`{"addContextFileResponse":{"fileId":"f-2"}}`))
	}))
	defer srv.Close()

	c := NewClient(config.Config{
		CookieFile:                    filepath.Join(t.TempDir(), "cookie.json"),
		BaseURL:                       srv.URL,
		DiscoveryEngineConfigID:       "cfg-1",
		DiscoveryEngineUploadEndpoint: srv.URL + "/v1alpha/locations/global/widgetAddContextFile",
	})
	c.HTTP = srv.Client()
	c.Logf = func(string, ...any) {}

	if _, _, err := c.UploadEnterpriseFile(RawFile{Name: "a.txt", MIME: "text/plain", Data: []byte("hello")}); err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	if !strings.Contains(body, `"session":"collections/c/engines/e/sessions/9"`) {
		t.Errorf("a tenant that accepts the session must still receive it: %s", body)
	}
}

func TestUnknownJSONField(t *testing.T) {
	body := []byte(`{"error":{"code":400,"message":"Invalid JSON payload received. Unknown name \"session\" at 'add_context_file_request': Cannot find field."}}`)
	if field, ok := unknownJSONField(body); !ok || field != "session" {
		t.Fatalf("unknownJSONField = %q, %v", field, ok)
	}
	if _, ok := unknownJSONField([]byte(`{"error":{"code":500,"message":"boom"}}`)); ok {
		t.Error("a non-field error must not be treated as an unknown field")
	}
}
