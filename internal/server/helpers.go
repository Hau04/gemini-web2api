package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

func setModelHeaders(w http.ResponseWriter, resolved models.Resolved) {
	w.Header().Set("X-Resolved-Model", resolved.Name)
	if resolved.Fallback {
		w.Header().Set("X-Model-Fallback", "true")
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(data)
}

func marshalNoEscapeHTML(data any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func startSSE(w http.ResponseWriter) bool {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if ok {
		flusher.Flush()
	}
	return ok
}

func writeSSEData(w http.ResponseWriter, data any) error {
	enc, err := marshalNoEscapeHTML(data)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "data: %s\n\n", string(enc))
	if err != nil {
		return err
	}

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeSSEEvent(w http.ResponseWriter, event string, data any) error {
	enc, err := marshalNoEscapeHTML(data)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(enc))
	if err != nil {
		return err
	}

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeSSEDone(w http.ResponseWriter) error {
	_, err := fmt.Fprintf(w, "data: [DONE]\n\n")
	if err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func writeSSEComment(w http.ResponseWriter, comment string) error {
	_, err := fmt.Fprintf(w, ": %s\n\n", comment)
	if err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

func (a *App) uploadImages(images []format.Image) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}

	tokens := a.Tokens.Get()
	var fileRefs []string

	requester := a.Gem.HTTP
	if requester == nil {
		if a.HTTPClient != nil {
			requester = a.HTTPClient
		} else {
			requester = createHTTPClient(a.Cfg)
		}
	}

	for i, img := range images {
		data := img.Data

		// If image has URL but no data, fetch the bytes first.
		if len(data) == 0 && img.URL != "" {
			fetched, err := multimodal.FetchImageBytes(requester, img.URL)
			if err != nil {
				return nil, fmt.Errorf("image %d fetch failed: %w", i, err)
			}
			if len(fetched) == 0 {
				return nil, fmt.Errorf("image %d fetch returned empty data", i)
			}
			data = fetched
		}

		if len(data) == 0 {
			return nil, fmt.Errorf("image %d has no data", i)
		}

		// Detect MIME from magic bytes if not set or generic.
		mime := img.MIME
		if mime == "" || mime == "image/png" {
			mime = multimodal.DetectImageMime(data)
		}

		ref, err := multimodal.UploadImage(requester, tokens, data, mime, a.Gem.Cookies, a.Cfg.AuthUser)
		if err != nil {
			return nil, fmt.Errorf("image %d upload failed: %w", i, err)
		}
		if ref == "" {
			return nil, fmt.Errorf("image %d upload returned empty reference", i)
		}
		fileRefs = append(fileRefs, ref)
	}

	if len(fileRefs) == 0 {
		return nil, nil
	}
	return fileRefs, nil
}

// rawFilesFromImages converts prompt attachments into in-memory files for the
// Gemini Enterprise context-file upload path.
func (a *App) rawFilesFromImages(images []format.Image) []gemini.RawFile {
	if len(images) == 0 {
		return nil
	}

	requester := a.Gem.HTTP
	if requester == nil {
		if a.HTTPClient != nil {
			requester = a.HTTPClient
		} else {
			requester = createHTTPClient(a.Cfg)
		}
	}

	files := make([]gemini.RawFile, 0, len(images))
	for i, img := range images {
		data := img.Data
		if len(data) == 0 && img.URL != "" {
			fetched, err := multimodal.FetchImageBytes(requester, img.URL)
			if err != nil {
				a.Logf("Attachment %d fetch failed for Enterprise upload: %v", i, err)
				continue
			}
			data = fetched
		}
		if len(data) == 0 {
			continue
		}

		name := strings.TrimSpace(img.Filename)
		if name == "" {
			name = fmt.Sprintf("attachment-%d%s", i+1, extensionForMIME(img.MIME))
		}
		mime := img.MIME
		if mime == "" || (mime == "image/png" && !strings.HasPrefix(name, "attachment")) {
			if detected := multimodal.DetectImageMime(data); detected != "" && strings.HasPrefix(detected, "image/") {
				mime = detected
			}
		}
		if mime == "" {
			mime = http.DetectContentType(data)
		}

		files = append(files, gemini.RawFile{Name: name, MIME: mime, Data: data})
	}
	return files
}

func extensionForMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0])) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	default:
		return ""
	}
}

// prepareAttachments readies prompt attachments for the resolved route.
//
// Consumer origins upload through content-push and use the returned file refs.
// Gemini Enterprise has no content-push pipeline: there the files are returned
// as raw bytes so the Gemini client can attach them to the assistant session
// (or inline text ones when the upload endpoint is unavailable).
func (a *App) prepareAttachments(images []format.Image) ([]string, []gemini.RawFile, error) {
	if len(images) == 0 {
		return nil, nil, nil
	}
	if a.Gem.UsesDiscoveryEngine() {
		return nil, a.rawFilesFromImages(images), nil
	}

	fileRefs, err := a.uploadImages(images)
	if err != nil {
		// The origin may only reveal itself as Enterprise after the first
		// request — pass raw files through instead of failing the turn.
		a.Logf("content-push upload failed (%v) — handing attachments to the Discovery Engine path", err)
		return nil, a.rawFilesFromImages(images), nil
	}
	return fileRefs, nil, nil
}
