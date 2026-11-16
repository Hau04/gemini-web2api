package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

const (
	maxBridgeAttachmentBytes = 25 << 20 // 25 MB per file
	maxLocalAutoUploadFiles  = 5        // max auto-detected local files per turn
)

// allowedUploadExts restricts local auto-upload strictly to rich document and media files
// (excluding source code, configs, keys, and secrets).
var allowedUploadExts = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".doc":  "application/msword",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".ppt":  "application/vnd.ms-powerpoint",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xls":  "application/vnd.ms-excel",
	".csv":  "text/csv",
	".rtf":  "application/rtf",
	".odt":  "application/vnd.oasis.opendocument.text",
	".epub": "application/epub+zip",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".bmp":  "image/bmp",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".m4a":  "audio/mp4",
}

const allowedExtPattern = `(?:pdf|docx|doc|pptx|ppt|xlsx|xls|csv|rtf|odt|epub|png|jpe?g|webp|gif|bmp|mp3|wav|m4a)`

var (
	reQuotedPath   = regexp.MustCompile("(?i)[\"'`]" + `([^"'` + "`" + `\r\n]+?\.` + allowedExtPattern + `)` + "[\"'`]")
	reFileURI      = regexp.MustCompile(`(?i)file://([^\s"'<>` + "`" + `]+?\.` + allowedExtPattern + `)`)
	reWindowsPath  = regexp.MustCompile(`(?i)([A-Za-z]:[\\/][^\r\n"'<>|*?\t` + "`" + `]+?\.` + allowedExtPattern + `)`)
	reUnixAbsPath  = regexp.MustCompile(`(?i)(?:^|[\s(\['"@,;=])(/(?:[^\s"'<>|*?` + "`" + `]+/)*[^\s"'<>|*?` + "`" + `]+\.` + allowedExtPattern + `)`)
	reRelativePath = regexp.MustCompile(`(?i)(?:^|[\s(\['"@,:;=])((?:[^\s"'<>|*?/:\\` + "`" + `]+[\\/])*[^\s"'<>|*?/:\\@` + "`" + `]+\.` + allowedExtPattern + `)`)
	reWorkspaceDir = regexp.MustCompile(`(?im)(?:working\s+directory|workspace(?:\s+(?:path|dir|directory|root))?|current\s+(?:working\s+)?directory|cwd)\s*[:=]\s*["'` + "`" + `]?([^\r\n"'` + "`" + `<>]+)["'` + "`" + `]?|<cwd>([^<]+)</cwd>`)
)

// isLoopbackRequest returns true only for direct localhost/loopback HTTP clients (never proxied).
func isLoopbackRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ExtractWorkspaceDir looks for a working directory hint in system or user messages
// (e.g. "Working directory: C:\Users\..." or "<cwd>/home/user/project</cwd>").
func ExtractWorkspaceDir(messages []models.OpenAIMessage) string {
	for _, msg := range messages {
		if msg.Role != "system" && msg.Role != "developer" && msg.Role != "user" {
			continue
		}
		text, _ := format.ExtractMessageContent(msg)
		if text == "" {
			continue
		}
		matches := reWorkspaceDir.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			cand := strings.TrimSpace(m[1])
			if cand == "" && len(m) > 2 {
				cand = strings.TrimSpace(m[2])
			}
			cand = strings.TrimRight(cand, ".,;)")
			if cand == "" {
				continue
			}
			if info, err := os.Stat(cand); err == nil && info.IsDir() {
				return cand
			}
		}
	}
	return ""
}

// ExtractCandidatePaths extracts candidate local document/media file paths mentioned in text.
func ExtractCandidatePaths(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`+"`()[]<>,.;:")
		p = strings.TrimPrefix(p, "@")
		if p == "" {
			return
		}
		// Skip HTTP/HTTPS URLs
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			return
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}

	for _, m := range reQuotedPath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reFileURI.FindAllStringSubmatch(text, -1) {
		raw := m[1]
		if unesc, err := url.PathUnescape(raw); err == nil {
			raw = unesc
		}
		// Convert file:///C:/... -> C:/... on Windows-style URIs
		if len(raw) >= 3 && raw[0] == '/' && raw[2] == ':' {
			raw = raw[1:]
		}
		add(raw)
	}
	for _, m := range reWindowsPath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reUnixAbsPath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range reRelativePath.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}

	return out
}

func isSensitiveFilename(base string) bool {
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, ".") {
		return true
	}
	for _, bad := range []string{"cookie", "secret", "credential", "password", "id_rsa", "private_key"} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

func resolveAndReadLocalFile(candidate string, baseDirs []string) (absPath string, att bridge.Attachment, ok bool) {
	ext := strings.ToLower(filepath.Ext(candidate))
	mime, allowed := allowedUploadExts[ext]
	if !allowed {
		return "", bridge.Attachment{}, false
	}

	var tryPaths []string
	if filepath.IsAbs(candidate) {
		tryPaths = append(tryPaths, candidate)
	} else {
		for _, dir := range baseDirs {
			if dir != "" {
				tryPaths = append(tryPaths, filepath.Join(dir, candidate))
			}
		}
	}

	for _, p := range tryPaths {
		cleaned, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			continue
		}
		base := filepath.Base(cleaned)
		if isSensitiveFilename(base) {
			continue
		}
		info, err := os.Stat(cleaned)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() <= 0 || info.Size() > maxBridgeAttachmentBytes {
			continue
		}
		data, err := os.ReadFile(cleaned)
		if err != nil || len(data) == 0 {
			continue
		}
		return cleaned, bridge.Attachment{
			Filename:   base,
			MIME:       mime,
			DataBase64: base64.StdEncoding.EncodeToString(data),
			Size:       len(data),
			Source:     "local_auto",
		}, true
	}

	return "", bridge.Attachment{}, false
}

// DiscoverLocalUploadFiles inspects the latest message (when it is a user turn) for references
// to existing local PDFs, Office documents, or media files and reads them for direct
// upload through the browser bridge. Already-uploaded paths in this tab session are skipped.
func DiscoverLocalUploadFiles(messages []models.OpenAIMessage, alreadyUploaded map[string]bool) ([]bridge.Attachment, []string) {
	if len(messages) == 0 {
		return nil, nil
	}
	last := messages[len(messages)-1]
	if last.Role != "user" && last.Role != "" {
		return nil, nil
	}
	latestUserText, _ := format.ExtractMessageContent(last)
	if strings.TrimSpace(latestUserText) == "" {
		return nil, nil
	}

	candidates := ExtractCandidatePaths(latestUserText)
	if len(candidates) == 0 {
		return nil, nil
	}

	var baseDirs []string
	if ws := ExtractWorkspaceDir(messages); ws != "" {
		baseDirs = append(baseDirs, ws)
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		baseDirs = append(baseDirs, cwd)
	}

	seenAbs := make(map[string]bool)
	var attachments []bridge.Attachment
	var uploadedPaths []string

	for _, cand := range candidates {
		if len(attachments) >= maxLocalAutoUploadFiles {
			break
		}
		absPath, att, ok := resolveAndReadLocalFile(cand, baseDirs)
		if !ok {
			continue
		}
		if seenAbs[absPath] || (alreadyUploaded != nil && alreadyUploaded[absPath]) {
			continue
		}
		seenAbs[absPath] = true
		attachments = append(attachments, att)
		uploadedPaths = append(uploadedPaths, absPath)
	}

	return attachments, uploadedPaths
}

// FormatAttachmentsToBridge converts explicit API attachments (image_url, file, input_file, document)
// into bridge.Attachment payloads for the browser extension.
func FormatAttachmentsToBridge(items []format.Image, fetchRemote func(string) ([]byte, error)) []bridge.Attachment {
	if len(items) == 0 {
		return nil
	}

	var out []bridge.Attachment
	for idx, item := range items {
		data := item.Data
		mime := item.MIME

		if len(data) == 0 && item.URL != "" && fetchRemote != nil {
			fetched, err := fetchRemote(item.URL)
			if err != nil || len(fetched) == 0 {
				continue
			}
			data = fetched
		}

		if len(data) == 0 || len(data) > maxBridgeAttachmentBytes {
			continue
		}
		if mime == "" {
			if len(data) >= 4 && string(data[:4]) == "%PDF" {
				mime = "application/pdf"
			} else {
				mime = multimodal.DetectImageMime(data)
			}
		}

		filename := strings.TrimSpace(item.Filename)
		if filename == "" {
			filename = fmt.Sprintf("attachment_%d%s", idx+1, extForMIME(mime))
		}

		out = append(out, bridge.Attachment{
			Filename:   filename,
			MIME:       mime,
			DataBase64: base64.StdEncoding.EncodeToString(data),
			Size:       len(data),
			Source:     "api",
		})
	}
	return out
}

func extForMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "application/pdf":
		return ".pdf"
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "text/csv":
		return ".csv"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx"
	default:
		return ".bin"
	}
}

// ConversationFingerprints returns a deterministic slice of SHA-256 fingerprints
// starting with the tool-definition digest followed by one digest per message.
// Matching prefixes allow the bridge hub to send compact delta turns safely only
// when both the tool definitions and earlier messages are unchanged.
func ConversationFingerprints(req models.OpenAIChatRequest) []string {
	fps := make([]string, 0, len(req.Messages)+1)

	toolsHash := sha256.New()
	if len(req.Tools) > 0 {
		if b, err := json.Marshal(req.Tools); err == nil {
			toolsHash.Write(b)
		}
	}
	fps = append(fps, "tools:"+hex.EncodeToString(toolsHash.Sum(nil)))

	for _, msg := range req.Messages {
		h := sha256.New()
		h.Write([]byte(msg.Role))
		h.Write([]byte{0})
		h.Write([]byte(msg.Name))
		h.Write([]byte{0})
		text, atts := format.ExtractMessageContent(msg)
		h.Write([]byte(text))
		for _, att := range atts {
			h.Write([]byte{1})
			h.Write([]byte(att.Filename))
			h.Write([]byte(att.MIME))
			h.Write([]byte(att.URL))
			if len(att.Data) > 0 {
				sum := sha256.Sum256(att.Data)
				h.Write(sum[:])
			}
		}
		for _, tc := range msg.ToolCalls {
			h.Write([]byte{2})
			h.Write([]byte(tc.Function.Name))
			h.Write([]byte(tc.Function.Arguments))
		}
		fps = append(fps, hex.EncodeToString(h.Sum(nil)))
	}
	return fps
}

type preparedBridgeTurn struct {
	Prompt        string
	Attachments   []bridge.Attachment
	SessionClient string
	SessionConv   string
	Fingerprints  []string
	UploadedPaths []string
}

func (a *App) prepareBridgeRequest(r *http.Request, req models.OpenAIChatRequest, fullPrompt string, apiAttachments []format.Image) preparedBridgeTurn {
	sessClient, sessConvGen, alreadyUploaded := a.Bridge.ActiveSession()
	fingerprints := ConversationFingerprints(req)

	bridgePrompt := fullPrompt
	activeAtts := apiAttachments

	// Delta-context optimization: if the active browser tab already holds the exact
	// tool definitions and leading conversation turns, send only the newly appended turns.
	if a.Cfg.BridgeDeltaContext {
		matchedPrefix := a.Bridge.MatchDeltaPrefix(fingerprints)
		// Element 0 is the tools digest, so matchedPrefix > 1 means at least 1 message matched.
		if matchedPrefix > 1 {
			matchedMsgs := matchedPrefix - 1
			if matchedMsgs < len(req.Messages) {
				deltaPrompt, deltaAtts := format.MessagesToDeltaPrompt(req.Messages[matchedMsgs:], req.ToolChoice)
				if strings.TrimSpace(deltaPrompt) != "" {
					bridgePrompt = deltaPrompt
					activeAtts = deltaAtts
					a.Logf("Bridge delta context active: sending %d/%d new message(s) (%d chars vs %d full)",
						len(req.Messages)-matchedMsgs, len(req.Messages), len(deltaPrompt), len(fullPrompt))
				}
			}
		}
	}

	var requester *http.Client
	if a.Gem != nil {
		requester = a.Gem.HTTP
	}
	if requester == nil {
		requester = a.HTTPClient
	}
	var fetchFn func(string) ([]byte, error)
	if requester != nil {
		fetchFn = func(rawURL string) ([]byte, error) {
			return multimodal.FetchImageBytes(requester, rawURL)
		}
	}

	attachments := FormatAttachmentsToBridge(activeAtts, fetchFn)
	var localPaths []string

	// Local document auto-upload: for localhost requests (e.g. WorkBuddy on the same machine),
	// automatically attach referenced local PDFs/Office docs/images directly to the Gemini tab
	// so Gemini reads them natively instead of running multi-turn extraction tool loops.
	if a.Cfg.BridgeAutoUploadLocalFiles && isLoopbackRequest(r) {
		localAtts, discoveredPaths := DiscoverLocalUploadFiles(req.Messages, alreadyUploaded)
		if len(localAtts) > 0 {
			var names []string
			for _, att := range localAtts {
				names = append(names, fmt.Sprintf("%s (%d KB)", att.Filename, (att.Size+1023)/1024))
			}
			a.Logf("Bridge auto-attaching %d local file(s) directly to Gemini: %s", len(localAtts), strings.Join(names, ", "))
			attachments = append(attachments, localAtts...)
			localPaths = append(localPaths, discoveredPaths...)
		}
	}

	return preparedBridgeTurn{
		Prompt:        bridgePrompt,
		Attachments:   attachments,
		SessionClient: sessClient,
		SessionConv:   sessConvGen,
		Fingerprints:  fingerprints,
		UploadedPaths: localPaths,
	}
}
