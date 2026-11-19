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
	"runtime"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

const (
	maxBridgeAttachmentBytes = 25 << 20 // 25 MB per file
	maxLocalAutoUploadFiles  = 5        // max auto-detected local files per turn
)

// allowedBinaryDocExts are always auto-uploaded when referenced on localhost (> 0 bytes).
var allowedBinaryDocExts = map[string]string{
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

// allowedCodeTextExts are auto-uploaded verbatim when their size >= largeCodeMinBytes
// (eliminating chunk-by-chunk read_file tool loops on long code files).
var allowedCodeTextExts = map[string]string{
	".go":       "text/plain",
	".py":       "text/plain",
	".js":       "text/javascript",
	".mjs":      "text/javascript",
	".cjs":      "text/javascript",
	".ts":       "text/plain",
	".tsx":      "text/plain",
	".jsx":      "text/plain",
	".java":     "text/plain",
	".kt":       "text/plain",
	".kts":      "text/plain",
	".cs":       "text/plain",
	".cpp":      "text/plain",
	".cc":       "text/plain",
	".cxx":      "text/plain",
	".c":        "text/plain",
	".h":        "text/plain",
	".hpp":      "text/plain",
	".rs":       "text/plain",
	".rb":       "text/plain",
	".php":      "text/plain",
	".swift":    "text/plain",
	".scala":    "text/plain",
	".dart":     "text/plain",
	".lua":      "text/plain",
	".r":        "text/plain",
	".sh":       "text/plain",
	".bash":     "text/plain",
	".zsh":      "text/plain",
	".ps1":      "text/plain",
	".bat":      "text/plain",
	".sql":      "text/plain",
	".html":     "text/html",
	".htm":      "text/html",
	".css":      "text/css",
	".scss":     "text/plain",
	".sass":     "text/plain",
	".less":     "text/plain",
	".vue":      "text/plain",
	".svelte":   "text/plain",
	".json":     "application/json",
	".jsonl":    "text/plain",
	".yaml":     "text/yaml",
	".yml":      "text/yaml",
	".toml":     "text/plain",
	".xml":      "application/xml",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".txt":      "text/plain",
	".log":      "text/plain",
	".proto":    "text/plain",
	".graphql":  "text/plain",
	".tf":       "text/plain",
	".gradle":   "text/plain",
	".cmake":    "text/plain",
	".ini":      "text/plain",
	".cfg":      "text/plain",
	".diff":     "text/plain",
	".patch":    "text/plain",
	".tsv":      "text/tab-separated-values",
	".ndjson":   "text/plain",
	".svg":      "image/svg+xml",
	".tex":      "text/plain",
	".rst":      "text/plain",
}

const allowedExtPattern = `(?:pdf|docx|doc|pptx|ppt|xlsx|xls|csv|tsv|rtf|odt|epub|png|jpe?g|webp|gif|bmp|svg|mp3|wav|m4a|go|py|mjs|cjs|js|tsx|ts|jsx|java|kts|kt|cs|cpp|cc|cxx|c|hpp|h|rs|rb|php|swift|scala|dart|lua|r|sh|bash|zsh|ps1|bat|sql|html|htm|css|scss|sass|less|vue|svelte|jsonl|ndjson|json|yaml|yml|toml|xml|markdown|md|txt|log|diff|patch|tex|rst|proto|graphql|tf|gradle|cmake|ini|cfg)`

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

// ExtractCandidatePaths extracts candidate local document, media, or code file paths mentioned in text.
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

var reAttachedFilename = regexp.MustCompile(`(?i)(?:\[Directly attached file\(s\):|\[File attached:|\[Attached file:)\s*([^\s\]]+\.(?:` + allowedExtPattern + `))`)

// ExtractAttachedFilenames extracts filenames from WorkBuddy attachment notices like
// "[Directly attached file(s): code_context.txt. ...]" or "[File attached: report.pdf]".
// These bare filenames (without paths) are resolved against workspace/cwd by
// resolveAndReadLocalFile.
func ExtractAttachedFilenames(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, m := range reAttachedFilename.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		name = strings.Trim(name, "\"'`()[]<>,.;:")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func isSensitiveFilename(base string) bool {
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, ".") {
		return true
	}
	for _, bad := range []string{"cookie", "secret", "credential", "password", "id_rsa", "private_key", "token"} {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

func fileVersionSignature(info os.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// canonicalPathKey produces a stable dedup key across symlinks, slash styles,
// and case-insensitive Windows drive/folder paths.
func canonicalPathKey(p string) string {
	trimmed := strings.TrimSpace(p)
	isWindowsDrive := len(trimmed) >= 2 && trimmed[1] == ':' &&
		((trimmed[0] >= 'a' && trimmed[0] <= 'z') || (trimmed[0] >= 'A' && trimmed[0] <= 'Z'))
	if isWindowsDrive && runtime.GOOS != "windows" {
		// Normalize Windows drive paths deterministically even when tested on Linux/macOS
		norm := strings.ReplaceAll(trimmed, `\`, "/")
		norm = strings.ToLower(filepath.Clean(norm))
		return strings.ReplaceAll(norm, "/", `\`)
	}
	cleaned := filepath.Clean(trimmed)
	if abs, err := filepath.Abs(cleaned); err == nil {
		cleaned = abs
	}
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && resolved != "" {
		cleaned = resolved
	}
	cleaned = filepath.Clean(cleaned)
	if runtime.GOOS == "windows" {
		cleaned = strings.ToLower(strings.ReplaceAll(cleaned, "/", `\`))
	}
	return cleaned
}

func attachmentContentHash(att bridge.Attachment) string {
	sum := sha256.Sum256([]byte(att.DataBase64))
	return hex.EncodeToString(sum[:])
}

// DeduplicateBridgeAttachments removes duplicate attachments within a single turn
// by exact content SHA-256 digest (and identical filename+size), ensuring the
// browser bridge never receives two copies of the same file.
func DeduplicateBridgeAttachments(attachments []bridge.Attachment) []bridge.Attachment {
	if len(attachments) <= 1 {
		return attachments
	}
	seenHash := make(map[string]bool, len(attachments))
	seenNameSize := make(map[string]bool, len(attachments))
	out := make([]bridge.Attachment, 0, len(attachments))
	for _, att := range attachments {
		if att.DataBase64 == "" {
			continue
		}
		h := attachmentContentHash(att)
		if seenHash[h] {
			continue
		}
		nameKey := strings.ToLower(strings.TrimSpace(att.Filename))
		nameSizeKey := ""
		if nameKey != "" && att.Size > 0 {
			nameSizeKey = fmt.Sprintf("%s:%d", nameKey, att.Size)
			if seenNameSize[nameSizeKey] {
				continue
			}
		}
		seenHash[h] = true
		if nameSizeKey != "" {
			seenNameSize[nameSizeKey] = true
		}
		out = append(out, att)
	}
	return out
}

func resolveAndReadLocalFile(candidate string, baseDirs []string, largeCodeMinBytes int) (absPath string, sig string, att bridge.Attachment, ok bool) {
	ext := strings.ToLower(filepath.Ext(candidate))
	mime, isDocOrMedia := allowedBinaryDocExts[ext]
	isCodeText := false
	if !isDocOrMedia {
		mime, isCodeText = allowedCodeTextExts[ext]
		if !isCodeText || largeCodeMinBytes <= 0 {
			return "", "", bridge.Attachment{}, false
		}
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
		if isCodeText && info.Size() < int64(largeCodeMinBytes) {
			// Keep short code files inline via normal agent tools; auto-upload only long code files.
			continue
		}
		data, err := os.ReadFile(cleaned)
		if err != nil || len(data) == 0 {
			continue
		}
		src := "local_auto"
		if isCodeText {
			src = "local_large_code"
		}
		return canonicalPathKey(cleaned), fileVersionSignature(info), bridge.Attachment{
			Filename:   base,
			MIME:       mime,
			DataBase64: base64.StdEncoding.EncodeToString(data),
			Size:       len(data),
			Source:     src,
		}, true
	}

	return "", "", bridge.Attachment{}, false
}

func codeFenceLangForFilename(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".diff", ".patch":
		return "diff"
	case ".json", ".jsonl", ".ndjson":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".sql":
		return "sql"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".md", ".markdown":
		return "markdown"
	case ".html", ".htm":
		return "html"
	case ".css", ".scss":
		return "css"
	case ".xml", ".svg":
		return "xml"
	default:
		return ""
	}
}

// PromptMayReferenceLocalFiles reports whether the latest user message contains
// path-like or attachment-notice references that DiscoverLocalUploadFiles would
// try to read from disk and attach through the browser bridge. Pure string
// check — no disk I/O — used for routing decisions before the bridge path runs.
func PromptMayReferenceLocalFiles(messages []models.OpenAIMessage) bool {
	if len(messages) == 0 {
		return false
	}
	last := messages[len(messages)-1]
	if last.Role != "user" && last.Role != "" {
		return false
	}
	text, _ := format.ExtractMessageContent(last)
	if strings.TrimSpace(text) == "" {
		return false
	}
	if len(ExtractCandidatePaths(text)) > 0 {
		return true
	}
	return len(ExtractAttachedFilenames(text)) > 0
}

// DiscoverLocalUploadFiles inspects the latest message (when it is a user turn) for references
// to existing local PDFs, Office documents, media files, or long source code files
// (>= largeCodeMinBytes) and reads them for direct upload through the browser bridge.
// Files already uploaded with the same size+mtime signature in this tab session are skipped.
// When 2+ plain-text/code files are discovered in one turn, they are bundled into ONE single
// workspace_files_bundle.md attachment to minimize browser upload HTTP calls (anti-ban).
func DiscoverLocalUploadFiles(messages []models.OpenAIMessage, alreadyUploaded map[string]string, largeCodeMinBytes int) ([]bridge.Attachment, map[string]string) {
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
	// Also extract filenames from WorkBuddy attachment notices like
	// "[Directly attached file(s): code_context.txt. ...]" which may not match path regexes.
	candidates = append(candidates, ExtractAttachedFilenames(latestUserText)...)
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
	seenContent := make(map[string]bool)
	var binaryAtts []bridge.Attachment
	type codeEntry struct {
		displayName string
		att         bridge.Attachment
	}
	var codeEntries []codeEntry
	uploadedSigs := make(map[string]string)

	for _, cand := range candidates {
		if len(binaryAtts)+len(codeEntries) >= maxLocalAutoUploadFiles {
			break
		}
		absKey, sig, att, ok := resolveAndReadLocalFile(cand, baseDirs, largeCodeMinBytes)
		if !ok {
			continue
		}
		if seenAbs[absKey] {
			continue
		}
		contentKey := "sha256:" + attachmentContentHash(att)
		if seenContent[contentKey] {
			continue
		}
		if alreadyUploaded != nil {
			if prevSig, hasPath := alreadyUploaded[absKey]; hasPath {
				if prevSig == sig {
					continue
				}
			} else if alreadyUploaded[contentKey] == "uploaded" {
				continue
			}
		}
		seenAbs[absKey] = true
		seenContent[contentKey] = true
		uploadedSigs[absKey] = sig
		uploadedSigs[contentKey] = "uploaded"
		if att.Source == "local_large_code" {
			codeEntries = append(codeEntries, codeEntry{displayName: cand, att: att})
		} else {
			binaryAtts = append(binaryAtts, att)
		}
	}

	attachments := make([]bridge.Attachment, 0, len(binaryAtts)+len(codeEntries))
	attachments = append(attachments, binaryAtts...)

	if len(codeEntries) == 1 {
		attachments = append(attachments, codeEntries[0].att)
	} else if len(codeEntries) >= 2 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "# Attached Workspace Files (%d files)\n\n", len(codeEntries))
		for i, ce := range codeEntries {
			rawBytes, err := base64.StdEncoding.DecodeString(ce.att.DataBase64)
			if err != nil {
				continue
			}
			lang := codeFenceLangForFilename(ce.att.Filename)
			label := strings.TrimSpace(ce.displayName)
			if label == "" {
				label = ce.att.Filename
			}
			fmt.Fprintf(&sb, "## File %d: %s\n\n```%s\n%s\n```\n\n", i+1, label, lang, string(rawBytes))
		}
		bundleBytes := []byte(sb.String())
		if len(bundleBytes) > 0 && len(bundleBytes) <= maxBridgeAttachmentBytes {
			bundleAtt := bridge.Attachment{
				Filename:   "workspace_files_bundle.md",
				MIME:       "text/markdown",
				DataBase64: base64.StdEncoding.EncodeToString(bundleBytes),
				Size:       len(bundleBytes),
				Source:     "local_large_code",
			}
			attachments = append(attachments, bundleAtt)
			uploadedSigs["sha256:"+attachmentContentHash(bundleAtt)] = "uploaded"
		} else {
			for _, ce := range codeEntries {
				attachments = append(attachments, ce.att)
			}
		}
	}

	return attachments, uploadedSigs
}

var reSingleToolResultPrefix = regexp.MustCompile(`(?s)^\[Tool result for ([A-Za-z0-9_.:-]+)\]:\s*(.*)$`)

func splitToolUseHeader(prompt string) (header string, body string, hasHeader bool) {
	if !strings.HasPrefix(prompt, "# Tool Use") {
		return "", prompt, false
	}
	availIdx := strings.Index(prompt, "\nAvailable tools:\n")
	if availIdx == -1 {
		return "", prompt, false
	}
	afterAvail := availIdx + len("\nAvailable tools:\n")
	splitRel := strings.Index(prompt[afterAvail:], "\n\n")
	if splitRel == -1 {
		return "", prompt, false
	}
	headerEnd := afterAvail + splitRel
	return prompt[:headerEnd], prompt[headerEnd+2:], true
}

func splitTrailingBridgeReminder(text string) (core string, reminder string) {
	markers := []string{
		"\n\n[Local client bridge active:",
		"\n\n[Invoke discovered connector/MCP tools using",
	}
	bestIdx := -1
	for _, m := range markers {
		if idx := strings.LastIndex(text, m); idx != -1 && idx > bestIdx {
			bestIdx = idx
		}
	}
	if bestIdx == -1 {
		return text, ""
	}
	return strings.TrimSpace(text[:bestIdx]), strings.TrimSpace(text[bestIdx+2:])
}

func sanitizeAttachmentStem(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteByte('_')
		}
		if sb.Len() >= 32 {
			break
		}
	}
	out := strings.Trim(sb.String(), "_-")
	if out == "" {
		return "output"
	}
	return out
}

func classifySpilledDeltaPayload(core string) (filename string, mime string, data []byte) {
	trimmed := strings.TrimSpace(core)
	// If the delta is a single [Tool result for <tool>]: <payload> without another message section,
	// give the attachment a descriptive filename and native .json MIME when the payload is JSON.
	if m := reSingleToolResultPrefix.FindStringSubmatch(trimmed); m != nil && !strings.Contains(m[2], "\n\n[Tool result for ") && !strings.Contains(m[2], "\n\n[User]:") {
		stem := sanitizeAttachmentStem(m[1])
		payload := strings.TrimSpace(m[2])
		if (strings.HasPrefix(payload, "{") || strings.HasPrefix(payload, "[")) && json.Valid([]byte(payload)) {
			return fmt.Sprintf("tool_result_%s.json", stem), "application/json", []byte(payload)
		}
		return fmt.Sprintf("tool_result_%s.txt", stem), "text/plain", []byte(trimmed)
	}
	return "tool_output_context.txt", "text/plain", []byte(trimmed)
}

func inlineFenceFileSpec(lang string) (ext string, mime string) {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "diff", "patch":
		return ".diff", "text/plain"
	case "json", "jsonc":
		return ".json", "application/json"
	case "csv":
		return ".csv", "text/csv"
	case "tsv":
		return ".tsv", "text/tab-separated-values"
	case "md", "markdown":
		return ".md", "text/markdown"
	case "html", "htm":
		return ".html", "text/html"
	case "xml", "svg":
		return ".xml", "application/xml"
	case "yaml", "yml":
		return ".yaml", "text/yaml"
	case "sql":
		return ".sql", "text/plain"
	case "go":
		return ".go", "text/plain"
	case "py", "python":
		return ".py", "text/plain"
	case "js", "javascript", "mjs":
		return ".js", "text/javascript"
	case "ts", "typescript", "tsx":
		return ".ts", "text/plain"
	case "sh", "bash", "zsh", "shell":
		return ".sh", "text/plain"
	case "log":
		return ".log", "text/plain"
	default:
		return ".txt", "text/plain"
	}
}

type fencedBlockSpan struct {
	start int
	end   int
	lang  string
	body  string
}

func findLargeFencedBlocks(text string) []fencedBlockSpan {
	var blocks []fencedBlockSpan
	pos := 0
	inBlock := false
	blockStart := 0
	bodyStart := 0
	fenceLen := 0
	blockLang := ""

	for pos < len(text) {
		lineStart := pos
		nl := strings.IndexByte(text[pos:], '\n')
		var line string
		if nl == -1 {
			line = text[pos:]
			pos = len(text)
		} else {
			line = text[pos : pos+nl]
			pos = pos + nl + 1
		}
		trimmed := strings.TrimRight(line, "\r")
		trimmedLeft := strings.TrimLeft(trimmed, " \t")

		if !inBlock {
			if strings.HasPrefix(trimmedLeft, "```") {
				ticks := 0
				for ticks < len(trimmedLeft) && trimmedLeft[ticks] == '`' {
					ticks++
				}
				lang := strings.TrimSpace(trimmedLeft[ticks:])
				// Skip inline single-line backtick phrases like ```foo```
				if !strings.Contains(lang, "`") {
					inBlock = true
					blockStart = lineStart
					bodyStart = pos
					fenceLen = ticks
					blockLang = lang
				}
			}
		} else {
			if strings.HasPrefix(trimmedLeft, "```") {
				ticks := 0
				for ticks < len(trimmedLeft) && trimmedLeft[ticks] == '`' {
					ticks++
				}
				if ticks >= fenceLen && strings.TrimSpace(trimmedLeft[ticks:]) == "" {
					bodyEnd := lineStart
					if bodyEnd > bodyStart && text[bodyEnd-1] == '\n' {
						bodyEnd--
					}
					if bodyEnd > bodyStart && text[bodyEnd-1] == '\r' {
						bodyEnd--
					}
					blocks = append(blocks, fencedBlockSpan{
						start: blockStart,
						end:   pos,
						lang:  blockLang,
						body:  text[bodyStart:bodyEnd],
					})
					inBlock = false
				}
			}
		}
	}
	return blocks
}

// ExtractLargeInlineBlocksToAttachment replaces large pasted code/diff/JSON/log fenced
// blocks (>= minBytes individually, or multiple >= 2KB blocks totaling >= minBytes) with
// ONE single file attachment while preserving `# Tool Use`, user instructions, and tool
// reminders inline in the chat box.
func ExtractLargeInlineBlocksToAttachment(prompt string, minBytes int) (compactPrompt string, att *bridge.Attachment) {
	if minBytes <= 0 || len(prompt) < minBytes {
		return prompt, nil
	}

	header, body, hasHeader := splitToolUseHeader(prompt)
	core, reminder := splitTrailingBridgeReminder(body)
	spans := findLargeFencedBlocks(core)
	if len(spans) == 0 {
		return prompt, nil
	}

	var candidates []fencedBlockSpan
	totalCandidateBytes := 0
	hasSingleLarge := false
	for _, sp := range spans {
		langLower := strings.ToLower(strings.TrimSpace(sp.lang))
		if langLower == "tool_call" || langLower == "tool_calls" || langLower == "function_call" {
			continue
		}
		if len(sp.body) >= minBytes {
			candidates = append(candidates, sp)
			totalCandidateBytes += len(sp.body)
			hasSingleLarge = true
		} else if len(sp.body) >= 2048 {
			candidates = append(candidates, sp)
			totalCandidateBytes += len(sp.body)
		}
	}
	if !hasSingleLarge && (len(candidates) < 2 || totalCandidateBytes < minBytes) {
		return prompt, nil
	}

	var filename, mime string
	var payload []byte
	if len(candidates) == 1 {
		ext, m := inlineFenceFileSpec(candidates[0].lang)
		filename = "inline_context" + ext
		mime = m
		payload = []byte(candidates[0].body)
	} else {
		filename = "inline_code_blocks.md"
		mime = "text/markdown"
		var sb strings.Builder
		for i, sp := range candidates {
			lang := strings.TrimSpace(sp.lang)
			if lang == "" {
				lang = "text"
			}
			fmt.Fprintf(&sb, "## Block %d (%s)\n\n```%s\n%s\n```\n\n", i+1, lang, lang, sp.body)
		}
		payload = []byte(sb.String())
	}

	if len(payload) == 0 || len(payload) > maxBridgeAttachmentBytes {
		return prompt, nil
	}

	var replaced strings.Builder
	cursor := 0
	for i, sp := range candidates {
		replaced.WriteString(core[cursor:sp.start])
		label := strings.TrimSpace(sp.lang)
		if label == "" {
			label = "code"
		}
		fmt.Fprintf(&replaced, "[Complex %s block #%d (%d chars) attached in %s — read verbatim from attachment]\n",
			label, i+1, len(sp.body), filename)
		cursor = sp.end
	}
	replaced.WriteString(core[cursor:])

	newBody := strings.TrimSpace(replaced.String())
	if reminder != "" {
		newBody = newBody + "\n\n" + reminder
	}
	if hasHeader {
		compactPrompt = header + "\n\n" + newBody
	} else {
		compactPrompt = newBody
	}

	return compactPrompt, &bridge.Attachment{
		Filename:   filename,
		MIME:       mime,
		DataBase64: base64.StdEncoding.EncodeToString(payload),
		Size:       len(payload),
		Source:     "spillover",
	}
}

// SpilloverOversizedPrompt moves oversized code / tool-result prompts (> maxChars)
// into a single UTF-8 file attachment so Gemini receives 100% verbatim text without hitting
// browser composer character limits or triggering giant DOM paste slowdowns.
// IMPORTANT: Never moves `# Tool Use` / `Available tools` or trailing bridge tool-call
// reminders into a file attachment, because Vertex AI Search routes attachment-only prompts
// through its passive file-reader sub-agent and stops emitting structured ```tool_call``` blocks.
func SpilloverOversizedPrompt(prompt string, maxChars int, isDelta bool) (compactPrompt string, att *bridge.Attachment) {
	if maxChars <= 0 || len(prompt) <= maxChars {
		return prompt, nil
	}

	// On a full prompt that starts with # Tool Use, ALWAYS keep the # Tool Use header inline
	// and spill ONLY a genuinely massive conversation/code body to a single file.
	if !isDelta {
		if header, rawBody, hasHeader := splitToolUseHeader(prompt); hasHeader {
			body := strings.TrimSpace(rawBody)
			if len(body) > maxChars/2 {
				// If a massive system/history section precedes a concise final [User] question,
				// keep BOTH # Tool Use and the concise [User] question inline and spill only the
				// heavy context prefix so Gemini immediately sees the task without losing tool mode.
				if lastUserIdx := strings.LastIndex(body, "\n\n[User]:"); lastUserIdx > maxChars/2 {
					prefix := strings.TrimSpace(body[:lastUserIdx])
					lastUser := strings.TrimSpace(body[lastUserIdx+2:])
					if len(lastUser) <= 1200 && len(prefix) <= maxBridgeAttachmentBytes {
						filename := "session_context.txt"
						data := []byte(prefix)
						return fmt.Sprintf(
								"%s\n\n[Full session instructions / context (%d chars) are attached directly in %s. Read the attached file verbatim before responding or calling tools.]\n\n%s",
								header, len(prefix), filename, lastUser,
							), &bridge.Attachment{
								Filename:   filename,
								MIME:       "text/plain",
								DataBase64: base64.StdEncoding.EncodeToString(data),
								Size:       len(data),
								Source:     "spillover",
							}
					}
				}

				data := []byte(body)
				if len(data) <= maxBridgeAttachmentBytes {
					filename := "code_context.txt"
					return fmt.Sprintf(
							"%s\n\n[The full prompt / code context (%d chars) is attached directly in %s. Read the attached file verbatim in full before responding or calling tools.]",
							header, len(body), filename,
						), &bridge.Attachment{
							Filename:   filename,
							MIME:       "text/plain",
							DataBase64: base64.StdEncoding.EncodeToString(data),
							Size:       len(data),
							Source:     "spillover",
						}
				}
			}
			// Never bury `# Tool Use` inside full_prompt_context.txt when the body itself is small.
			return prompt, nil
		}
	}

	// Delta turns (e.g. massive [Tool result for read_file] or connector JSON):
	// keep any trailing bridge/connector tool reminder INLINE in the composer!
	if isDelta {
		core, reminder := splitTrailingBridgeReminder(prompt)
		if len(core) <= maxChars/2 {
			return prompt, nil
		}
		filename, mime, data := classifySpilledDeltaPayload(core)
		if len(data) > maxBridgeAttachmentBytes {
			return prompt, nil
		}
		compact := fmt.Sprintf(
			"[Full tool output / code context (%d chars) is attached directly in %s. Read the attached file verbatim in full and continue — do NOT re-read it with local tools.]",
			len(core), filename,
		)
		if reminder != "" {
			compact = compact + "\n\n" + reminder
		}
		return compact, &bridge.Attachment{
			Filename:   filename,
			MIME:       mime,
			DataBase64: base64.StdEncoding.EncodeToString(data),
			Size:       len(data),
			Source:     "spillover",
		}
	}

	// Oversized plain non-tool prompt:
	data := []byte(prompt)
	if len(data) > maxBridgeAttachmentBytes {
		return prompt, nil
	}
	filename := "full_prompt_context.txt"
	compact := fmt.Sprintf(
		"[Full tool output / code context (%d chars) is attached directly in %s. Read the attached file verbatim in full and continue — do NOT re-read it with local tools.]",
		len(prompt), filename,
	)
	return compact, &bridge.Attachment{
		Filename:   filename,
		MIME:       "text/plain",
		DataBase64: base64.StdEncoding.EncodeToString(data),
		Size:       len(data),
		Source:     "spillover",
	}
}

// FormatAttachmentsToBridge converts explicit API attachments (image_url, file, input_file,
// document, attachment) into bridge.Attachment payloads for the browser extension.
// Handles remote URLs via fetchRemote, plus local file:// URLs and bare local paths
// (WorkBuddy may send attached files as local paths when running on localhost).
func FormatAttachmentsToBridge(items []format.Image, fetchRemote func(string) ([]byte, error)) []bridge.Attachment {
	if len(items) == 0 {
		return nil
	}

	var out []bridge.Attachment
	for idx, item := range items {
		data := item.Data
		mime := item.MIME

		if len(data) == 0 && item.URL != "" {
			// Try remote fetch first (http/https/data:).
			if fetchRemote != nil {
				if fetched, err := fetchRemote(item.URL); err == nil && len(fetched) > 0 {
					data = fetched
				}
			}
			// Fallback: try local file read for file:// URLs or bare paths.
			if len(data) == 0 {
				if localData, err := readLocalFileFromURL(item.URL); err == nil && len(localData) > 0 {
					data = localData
				}
			}
			if len(data) == 0 {
				continue
			}
		}

		if len(data) == 0 || len(data) > maxBridgeAttachmentBytes {
			continue
		}
		if mime == "" {
			if len(data) >= 4 && string(data[:4]) == "%PDF" {
				mime = "application/pdf"
			} else if fromName := mimeFromFilename(item.Filename); fromName != "" {
				// Prefer filename-based MIME for text/code/docs (DetectImageMime
				// defaults to image/png for unknown data, which is wrong for text).
				mime = fromName
			} else {
				mime = multimodal.DetectImageMime(data)
			}
		}

		filename := strings.TrimSpace(item.Filename)
		if filename == "" && item.URL != "" {
			filename = filepath.Base(strings.TrimSpace(item.URL))
			// Strip query params from URL-derived filename.
			if qIdx := strings.Index(filename, "?"); qIdx != -1 {
				filename = filename[:qIdx]
			}
		}
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

// readLocalFileFromURL reads a local file from a file:// URL or bare filesystem path.
// Returns error if the path is not a local file or cannot be read.
func readLocalFileFromURL(rawURL string) ([]byte, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("empty url")
	}
	// Handle file:// URLs.
	if strings.HasPrefix(strings.ToLower(trimmed), "file://") {
		rest := trimmed[7:] // strip "file://"
		// file:///path -> /path ; file://host/path -> /path (ignore host for localhost)
		if strings.HasPrefix(rest, "localhost/") {
			rest = rest[len("localhost"):]
		}
		if unesc, err := url.PathUnescape(rest); err == nil {
			rest = unesc
		}
		// Windows: file:///C:/path -> C:/path
		if len(rest) >= 3 && rest[0] == '/' && rest[2] == ':' {
			rest = rest[1:]
		}
		trimmed = rest
	}
	// Only attempt local read for paths that look like filesystem paths
	// (absolute or relative with extension), not http/https/data: URLs.
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		return nil, fmt.Errorf("not a local path: %s", rawURL)
	}
	// Must have a file extension to be considered a local file path.
	if filepath.Ext(trimmed) == "" {
		return nil, fmt.Errorf("no file extension: %s", rawURL)
	}
	cleaned := filepath.Clean(trimmed)
	info, err := os.Stat(cleaned)
	if err != nil {
		// Try relative to cwd.
		if abs, err2 := filepath.Abs(cleaned); err2 == nil {
			info, err = os.Stat(abs)
			if err == nil {
				cleaned = abs
			}
		}
		if err != nil {
			return nil, err
		}
	}
	if info.IsDir() || info.Size() <= 0 || info.Size() > maxBridgeAttachmentBytes {
		return nil, fmt.Errorf("invalid file size or directory: %s", cleaned)
	}
	return os.ReadFile(cleaned)
}

func mimeFromFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".txt", ".log":
		return "text/plain"
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".tsv":
		return "text/tab-separated-values"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js", ".mjs", ".cjs":
		return "text/javascript"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	default:
		return ""
	}
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
	case "text/plain":
		return ".txt"
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

var (
	reVolatileSystemLine = regexp.MustCompile(`(?im)^\s*(?:current\s+(?:date|time|datetime|timestamp)|today'?s\s+date|local\s+time|server\s+time|timestamp|now)\s*[:=].*$`)
	reVolatileISODate    = regexp.MustCompile(`\b20\d{2}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b`)
	reVolatileClock      = regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2})?\s*(?:AM|PM|UTC|GMT)?\b`)
)

// normalizeSystemPromptForFingerprint strips volatile clock/date/timestamp metadata
// from system prompts so routine per-turn clock updates do not break delta context.
func normalizeSystemPromptForFingerprint(text string) string {
	norm := reVolatileSystemLine.ReplaceAllString(text, "[volatile-time]")
	norm = reVolatileISODate.ReplaceAllString(norm, "[date]")
	norm = reVolatileClock.ReplaceAllString(norm, "[time]")
	return norm
}

// DetectAgentToolLoop checks the trailing autonomous agent turns (since the last user
// message) for 3+ consecutive identical tool calls or tool outputs, helping prevent
// runaway retry storms that risk triggering Google frequency bans.
func DetectAgentToolLoop(messages []models.OpenAIMessage) (repeatCount int, toolName string) {
	if len(messages) < 6 {
		return 0, ""
	}
	toolCallNames := format.BuildToolCallNameMap(messages)

	var callSigs []string
	var callNames []string
	var outSigs []string
	var outNames []string

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "user" || role == "" {
			break
		}
		if role == "assistant" && len(msg.ToolCalls) > 0 {
			var parts []string
			firstName := ""
			for _, tc := range msg.ToolCalls {
				n := strings.TrimSpace(tc.Function.Name)
				if firstName == "" {
					firstName = n
				}
				parts = append(parts, n+":"+strings.TrimSpace(tc.Function.Arguments))
			}
			callSigs = append(callSigs, strings.Join(parts, "|"))
			callNames = append(callNames, firstName)
		} else if role == "tool" {
			tName := strings.TrimSpace(msg.Name)
			if tName == "" && msg.ToolCallID != "" {
				tName = toolCallNames[msg.ToolCallID]
			}
			text, _ := format.ExtractMessageContent(msg)
			sum := sha256.Sum256([]byte(tName + "\x00" + strings.TrimSpace(text)))
			outSigs = append(outSigs, hex.EncodeToString(sum[:]))
			outNames = append(outNames, tName)
		}
	}

	countPrefix := func(sigs []string) int {
		if len(sigs) == 0 {
			return 0
		}
		c := 1
		for i := 1; i < len(sigs); i++ {
			if sigs[i] == sigs[0] {
				c++
			} else {
				break
			}
		}
		return c
	}

	callRep := countPrefix(callSigs)
	outRep := countPrefix(outSigs)
	if callRep >= 3 && callRep >= outRep {
		name := "tool"
		if len(callNames) > 0 && callNames[0] != "" {
			name = callNames[0]
		}
		return callRep, name
	}
	if outRep >= 3 {
		name := "tool"
		if len(outNames) > 0 && outNames[0] != "" {
			name = outNames[0]
		}
		return outRep, name
	}
	return 0, ""
}

// ConversationFingerprints returns a deterministic slice of SHA-256 fingerprints
// starting with the tool-definition digest followed by one digest per message.
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
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		h.Write([]byte(msg.Role))
		h.Write([]byte{0})
		h.Write([]byte(msg.Name))
		h.Write([]byte{0})
		h.Write([]byte(msg.ToolCallID))
		h.Write([]byte{0})
		text, atts := format.ExtractMessageContent(msg)
		if role == "system" || role == "developer" {
			text = normalizeSystemPromptForFingerprint(text)
		}
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
	Prompt           string
	Attachments      []bridge.Attachment
	NewChat          bool
	LoopPenalty      bool
	SessionClient    string
	SessionConv      string
	Fingerprints     []string
	UserFingerprints []string
	ToolNames        map[string]bool
	UploadedSigs     map[string]string
}

// ExtractUserFingerprints returns the per-user-message digests from ConversationFingerprints
// and reports whether the request contains any assistant or tool history.
func ExtractUserFingerprints(req models.OpenAIChatRequest, allFingerprints []string) ([]string, bool) {
	var userFPs []string
	hasAssistantOrTool := false
	for i, msg := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "", "user":
			if i+1 < len(allFingerprints) {
				userFPs = append(userFPs, allFingerprints[i+1])
			}
		case "assistant", "tool":
			hasAssistantOrTool = true
		}
	}
	return userFPs, hasAssistantOrTool
}

func (a *App) recordBridgeTurnSuccess(p preparedBridgeTurn) {
	a.Bridge.RecordTurnSuccessWithTools(
		p.SessionClient,
		p.SessionConv,
		p.Fingerprints,
		p.UserFingerprints,
		p.ToolNames,
		p.UploadedSigs,
		p.NewChat,
	)
}

func (a *App) prepareBridgeRequest(r *http.Request, req models.OpenAIChatRequest, fullPrompt string, apiAttachments []format.Image, chatMode string) preparedBridgeTurn {
	sessClient, sessConvGen, alreadyUploaded := a.Bridge.ActiveSession()
	fingerprints := ConversationFingerprints(req)
	userFPs, hasAssistantOrTool := ExtractUserFingerprints(req, fingerprints)
	currentToolNames := format.AllowedToolNames(req.Tools)

	// Chat-session selection priority:
	//   1. Per-request model suffix (@new / @continue)
	//   2. Extension popup override (POST /bridge/chatmode)
	//   3. Auto heuristic: a brand-new client conversation starts a fresh chat
	if chatMode == "" {
		chatMode = a.Bridge.ChatMode()
	}
	var newChat bool
	switch chatMode {
	case "new":
		newChat = true
		a.Logf("Chat mode 'new' requested — browser tab will start a fresh chat")
	case "continue":
		newChat = false
		a.Logf("Chat mode 'continue' requested — appending to the tab's currently open chat")
	default:
		// Detect when the client (e.g. WorkBuddy) started a brand-new chat while the open
		// browser tab still holds an older conversation, so Gemini starts a clean New Chat
		// with zero stale memory or leftover files from the previous chat.
		newChat = a.Cfg.BridgeAutoNewChat && a.Bridge.IsNewConversation(userFPs, hasAssistantOrTool)
		if newChat {
			a.Logf("Bridge detected new client conversation — instructing browser tab to start a fresh chat")
		}
	}
	if newChat {
		alreadyUploaded = nil
	}

	bridgePrompt := fullPrompt
	activeAtts := apiAttachments
	isDelta := false

	// Delta-context optimization: if the active browser tab already holds the leading
	// conversation turns (including when only new tools were added mid-chat), send only
	// the newly appended turns plus any newly added tool schemas.
	if a.Cfg.BridgeDeltaContext && !newChat {
		matchedPrefix, addedToolNames := a.Bridge.MatchDeltaPrefixWithTools(fingerprints, currentToolNames)
		// Element 0 is the tools digest, so matchedPrefix > 1 means at least 1 message matched.
		if matchedPrefix > 1 {
			matchedMsgs := matchedPrefix - 1
			if matchedMsgs < len(req.Messages) {
				priorToolNames := format.BuildToolCallNameMap(req.Messages[:matchedMsgs])
				deltaPrompt, deltaAtts := format.MessagesToDeltaPromptWithNames(req.Messages[matchedMsgs:], req.ToolChoice, priorToolNames)
				if strings.TrimSpace(deltaPrompt) != "" {
					if len(addedToolNames) > 0 {
						addedSet := make(map[string]bool, len(addedToolNames))
						for _, n := range addedToolNames {
							addedSet[n] = true
						}
						var addedDefs []models.OpenAITool
						for _, t := range req.Tools {
							n := strings.TrimSpace(t.Function.Name)
							if n == "" {
								n = strings.TrimSpace(t.Name)
							}
							if addedSet[n] {
								addedDefs = append(addedDefs, t)
							}
						}
						if compactAdded := format.CompactToolDefinitionsJSON(addedDefs); compactAdded != "" {
							deltaPrompt = fmt.Sprintf("[Newly added tools available in this session]:\n%s\n\n%s", compactAdded, deltaPrompt)
						}
					}
					// On follow-up user turns in tool-enabled sessions (e.g. "use local tools in workbuddy"),
					// include a compact one-line bridge reminder so Gemini Enterprise keeps emitting
					// ```tool_call``` blocks instead of refusing as a cloud-only environment.
					lastMsg := req.Messages[len(req.Messages)-1]
					lastRole := lastMsg.Role
					if len(req.Tools) > 0 && (lastRole == "user" || lastRole == "") {
						if reminder := format.BuildDeltaToolReminder(req.Tools, req.ToolChoice); reminder != "" {
							deltaPrompt = deltaPrompt + "\n\n" + reminder
						}
					} else if len(req.Tools) > 0 && lastRole == "tool" {
						// After ToolSearch loads deferred WorkBuddy connector schemas, include a
						// compact DeferExecuteTool call-format & target-verification reminder.
						allToolNames := format.BuildToolCallNameMap(req.Messages)
						lastToolName := strings.TrimSpace(lastMsg.Name)
						if lastToolName == "" && lastMsg.ToolCallID != "" {
							lastToolName = allToolNames[lastMsg.ToolCallID]
						}
						if lastToolName == "ToolSearch" {
							if reminder := format.BuildToolSearchFollowUpReminder(req.Tools, req.ToolChoice); reminder != "" {
								deltaPrompt = deltaPrompt + "\n\n" + reminder
							}
						}
					}
					bridgePrompt = deltaPrompt
					activeAtts = deltaAtts
					isDelta = true
					a.Logf("Bridge delta context active: sending %d/%d new message(s) (%d chars vs %d full, +%d added tools)",
						len(req.Messages)-matchedMsgs, len(req.Messages), len(deltaPrompt), len(fullPrompt), len(addedToolNames))
				}
			}
		}
	}

	var requester gemini.Requester
	if a.Gem != nil {
		requester = a.Gem.HTTP
	}
	if requester == nil && a.HTTPClient != nil {
		requester = a.HTTPClient
	}
	var fetchFn func(string) ([]byte, error)
	if requester != nil {
		fetchFn = func(rawURL string) ([]byte, error) {
			return multimodal.FetchImageBytes(requester, rawURL)
		}
	}

	rawAPIAttachments := FormatAttachmentsToBridge(activeAtts, fetchFn)
	uploadedSigs := make(map[string]string)

	// If delta-context did not match (e.g. client updated a timestamp in system prompt),
	// avoid re-uploading historical API attachments from earlier turns that are already
	// attached in the active browser tab session.
	var latestMsgHashes map[string]bool
	if !isDelta && len(req.Messages) > 1 && len(rawAPIAttachments) > 0 && len(alreadyUploaded) > 0 {
		_, latestImgs := format.ExtractMessageContent(req.Messages[len(req.Messages)-1])
		if len(latestImgs) > 0 {
			latestMsgHashes = make(map[string]bool, len(latestImgs))
			for _, att := range FormatAttachmentsToBridge(latestImgs, fetchFn) {
				latestMsgHashes[attachmentContentHash(att)] = true
			}
		}
	}

	var attachments []bridge.Attachment
	for _, att := range rawAPIAttachments {
		h := attachmentContentHash(att)
		contentKey := "sha256:" + h
		if !isDelta && len(req.Messages) > 1 && alreadyUploaded != nil && alreadyUploaded[contentKey] == "uploaded" {
			if latestMsgHashes == nil || !latestMsgHashes[h] {
				continue
			}
		}
		attachments = append(attachments, att)
		uploadedSigs[contentKey] = "uploaded"
	}

	// Local file auto-upload: for localhost requests (e.g. WorkBuddy on the same machine),
	// automatically attach referenced local PDFs/Office docs/images AND long source code files
	// (>= bridge_large_code_upload_bytes) directly to the Gemini tab so Gemini reads them natively.
	if a.Cfg.BridgeAutoUploadLocalFiles && isLoopbackRequest(r) {
		// Merge current-turn API attachment hashes into the skip map so a file sent BOTH via
		// OpenAI file/image parts AND mentioned by path in text is never attached twice.
		skipMap := make(map[string]string, len(alreadyUploaded)+len(uploadedSigs))
		for k, v := range alreadyUploaded {
			skipMap[k] = v
		}
		for k, v := range uploadedSigs {
			skipMap[k] = v
		}

		localAtts, discoveredSigs := DiscoverLocalUploadFiles(req.Messages, skipMap, a.Cfg.BridgeLargeCodeUploadBytes)
		if len(localAtts) > 0 {
			var names []string
			for _, att := range localAtts {
				names = append(names, fmt.Sprintf("%s (%d KB)", att.Filename, (att.Size+1023)/1024))
			}
			a.Logf("Bridge auto-attaching %d local file(s) directly to Gemini: %s", len(localAtts), strings.Join(names, ", "))
			attachments = append(attachments, localAtts...)
			for k, v := range discoveredSigs {
				uploadedSigs[k] = v
			}
		}
	}

	// Offload complex/oversized prompts into ONE single file attachment so Gemini gets
	// 100% verbatim content without browser composer truncation, DOM lag, or extra upload calls.
	var spilledAtt *bridge.Attachment
	if a.Cfg.BridgeSpilloverChars > 0 && len(bridgePrompt) > a.Cfg.BridgeSpilloverChars {
		origLen := len(bridgePrompt)
		compactPrompt, spillAtt := SpilloverOversizedPrompt(bridgePrompt, a.Cfg.BridgeSpilloverChars, isDelta)
		if spillAtt != nil {
			bridgePrompt = compactPrompt
			spilledAtt = spillAtt
			a.Logf("Bridge spilled oversized prompt (%d chars) into attachment %s (%d KB)",
				origLen, spillAtt.Filename, (spillAtt.Size+1023)/1024)
		}
	}
	// Even below full spillover, replace large pasted fenced code/diff/JSON/log blocks
	// (>= bridge_large_code_upload_bytes) with a single clean typed attachment while keeping
	// `# Tool Use`, user instructions, and tool reminders inline.
	if spilledAtt == nil && a.Cfg.BridgeLargeCodeUploadBytes > 0 {
		origLen := len(bridgePrompt)
		compactPrompt, inlineAtt := ExtractLargeInlineBlocksToAttachment(bridgePrompt, a.Cfg.BridgeLargeCodeUploadBytes)
		if inlineAtt != nil {
			bridgePrompt = compactPrompt
			spilledAtt = inlineAtt
			a.Logf("Bridge offloaded complex inline block(s) (%d -> %d chars) into attachment %s (%d KB)",
				origLen, len(compactPrompt), inlineAtt.Filename, (inlineAtt.Size+1023)/1024)
		}
	}
	if spilledAtt != nil {
		attachments = append(attachments, *spilledAtt)
		uploadedSigs["sha256:"+attachmentContentHash(*spilledAtt)] = "uploaded"
	}

	// Runaway agent-loop detection: if the last 3+ autonomous tool turns repeated the exact
	// same call or output, inject a clear loop-breaker hint and apply extra anti-ban pacing.
	loopPenalty := false
	if repeatCount, loopTool := DetectAgentToolLoop(req.Messages); repeatCount >= 3 {
		loopPenalty = true
		a.Logf("Bridge detected runaway agent loop (%d identical %s turns) — injecting loop-breaker & extra pacing", repeatCount, loopTool)
		bridgePrompt = bridgePrompt + fmt.Sprintf(
			"\n\n[Loop protection: the last %d consecutive tool turns repeated the exact same %s call/output. Do NOT repeat the same call again — change strategy, inspect what failed, or explain the blocker to the user.]",
			repeatCount, loopTool,
		)
	}

	// Final hard deduplication pass so duplicate bytes/files are never dispatched in a turn
	attachments = DeduplicateBridgeAttachments(attachments)

	if len(uploadedSigs) == 0 {
		uploadedSigs = nil
	}

	return preparedBridgeTurn{
		Prompt:           bridgePrompt,
		Attachments:      attachments,
		NewChat:          newChat,
		LoopPenalty:      loopPenalty,
		SessionClient:    sessClient,
		SessionConv:      sessConvGen,
		Fingerprints:     fingerprints,
		UserFingerprints: userFPs,
		ToolNames:        currentToolNames,
		UploadedSigs:     uploadedSigs,
	}
}
