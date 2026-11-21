package format

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
	"github.com/ikhsan3adi/gemini-web2api/internal/multimodal"
)

var reDataURL = regexp.MustCompile(`(?s)^data:([^;,]+)?(;base64)?,(.*)$`)

func RandHex(n int) string {
	bytes := make([]byte, (n+1)/2)
	_, _ = rand.Read(bytes)
	return hex.EncodeToString(bytes)[:n]
}

// DecodeDataURL decodes a `data:<mime>[;base64],<payload>` URL into bytes and MIME.
// Mirrors upstream _decode_data_url: base64 payloads are strictly decoded,
// non-base64 payloads are percent-decoded.
func DecodeDataURL(dataURL string) ([]byte, string, error) {
	m := reDataURL.FindStringSubmatch(dataURL)
	if m == nil {
		return nil, "", fmt.Errorf("not a data URL")
	}
	mime := m[1]
	if mime == "" {
		mime = "image/png"
	}
	payload := m[3]
	if m[2] != "" {
		decoded, err := multimodal.DecodeBase64Raw(payload)
		if err != nil {
			return nil, "", fmt.Errorf("base64 decode failed: %w", err)
		}
		if m[1] == "" {
			mime = multimodal.DetectImageMime(decoded)
		}
		return decoded, mime, nil
	}
	decodedStr, err := url.PathUnescape(payload)
	if err != nil {
		return nil, "", fmt.Errorf("data URL percent-decode failed: %w", err)
	}
	decoded := []byte(decodedStr)
	if m[1] == "" {
		mime = multimodal.DetectImageMime(decoded)
	}
	return decoded, mime, nil
}

// ImageFromURL returns an Image with URL set for remote passthrough.
func ImageFromURL(rawURL string) Image {
	// Try to extract MIME from URL extension
	mime := "image/png"
	if extIdx := strings.LastIndex(rawURL, "."); extIdx != -1 {
		ext := strings.ToLower(rawURL[extIdx:])
		// Strip query params
		if qIdx := strings.Index(ext, "?"); qIdx != -1 {
			ext = ext[:qIdx]
		}
		switch ext {
		case ".jpg", ".jpeg":
			mime = "image/jpeg"
		case ".png":
			mime = "image/png"
		case ".gif":
			mime = "image/gif"
		case ".webp":
			mime = "image/webp"
		case ".bmp":
			mime = "image/bmp"
		case ".svg":
			mime = "image/svg+xml"
		}
	}
	return Image{URL: rawURL, MIME: mime}
}

// imageFromURLString resolves a URL string (remote or data:) into an Image.
func imageFromURLString(rawURL, mimeHint string) (Image, bool) {
	if rawURL == "" {
		return Image{}, false
	}
	if strings.HasPrefix(rawURL, "data:") {
		data, mime, err := DecodeDataURL(rawURL)
		if err != nil {
			return Image{}, false
		}
		return Image{Data: data, MIME: mime}, true
	}
	img := ImageFromURL(rawURL)
	if mimeHint != "" {
		img.MIME = mimeHint
	}
	return img, true
}

func partMime(m map[string]any) string {
	for _, k := range []string{"mime_type", "mime", "media_type"} {
		if s, _ := m[k].(string); s != "" {
			return s
		}
	}
	return ""
}

// ImageFromPart parses an OpenAI-style content part (image_url, input_image, image) into an Image.
// Mirrors upstream _image_from_part, including image_url as a plain string.
func ImageFromPart(mapItem map[string]any) (Image, bool) {
	iType, _ := mapItem["type"].(string)

	switch iType {
	case "image_url":
		raw := mapItem["image_url"]
		if urlObj, ok := raw.(map[string]any); ok {
			urlStr, _ := urlObj["url"].(string)
			mimeHint := partMime(urlObj)
			return imageFromURLString(urlStr, mimeHint)
		}
		if urlStr, ok := raw.(string); ok {
			return imageFromURLString(urlStr, "")
		}
	case "input_image", "image":
		var urlRaw any
		if u, ok := mapItem["image_url"]; ok {
			urlRaw = u
		} else if u, ok := mapItem["url"]; ok {
			urlRaw = u
		}
		if urlObj, ok := urlRaw.(map[string]any); ok {
			urlStr, _ := urlObj["url"].(string)
			mimeHint := partMime(urlObj)
			if mimeHint == "" {
				mimeHint = partMime(mapItem)
			}
			if urlStr != "" {
				return imageFromURLString(urlStr, mimeHint)
			}
		} else if urlStr, ok := urlRaw.(string); ok && urlStr != "" {
			if mimeHint := partMime(mapItem); mimeHint != "" {
				return imageFromURLString(urlStr, mimeHint)
			}
			return imageFromURLString(urlStr, "")
		}
		var imageData string
		if s, ok := mapItem["data"].(string); ok && s != "" {
			imageData = s
		} else if s, ok := mapItem["base64"].(string); ok && s != "" {
			imageData = s
		}
		if imageData != "" {
			mime := partMime(mapItem)
			if strings.HasPrefix(imageData, "data:") {
				data, decMime, err := DecodeDataURL(imageData)
				if err != nil {
					return Image{}, false
				}
				if mime == "" {
					mime = decMime
				}
				return Image{Data: data, MIME: mime}, true
			}
			decoded, err := multimodal.DecodeBase64Raw(imageData)
			if err != nil {
				return Image{}, false
			}
			if mime == "" {
				mime = multimodal.DetectImageMime(decoded)
			}
			return Image{Data: decoded, MIME: mime}, true
		}
	}

	return Image{}, false
}

// FileFromPart parses OpenAI-style file/document content parts (`file`, `input_file`, `document`,
// `attachment`, `directly_attached_file`, `attached_file`, `file_attachment`) into an
// Image/Attachment struct so PDFs, text files, and other documents can be uploaded directly
// via the bridge. Handles WorkBuddy attachment schemas where data may be in `file_data`,
// `data`, `base64`, `content`, or `bytes` fields, and URLs in `file_url`, `url`, or `path`.
func FileFromPart(mapItem map[string]any) (Image, bool) {
	iType, _ := mapItem["type"].(string)

	// Normalize WorkBuddy attachment type aliases to file handling.
	normalizedType := iType
	switch iType {
	case "attachment", "directly_attached_file", "attached_file", "file_attachment", "direct_file":
		normalizedType = "file"
	}

	switch normalizedType {
	case "file":
		// Try nested object first (OpenAI style: {"type":"file","file":{...}}),
		// then flat (WorkBuddy style: {"type":"attachment","filename":...,"data":...}).
		var fileObj map[string]any
		if nested, ok := mapItem["file"].(map[string]any); ok {
			fileObj = nested
		} else if nested, ok := mapItem["attachment"].(map[string]any); ok {
			fileObj = nested
		} else {
			fileObj = mapItem
		}
		filename, _ := fileObj["filename"].(string)
		if filename == "" {
			filename, _ = fileObj["name"].(string)
		}
		if filename == "" {
			filename, _ = mapItem["filename"].(string)
		}
		mimeHint := partMime(fileObj)
		if mimeHint == "" {
			mimeHint = partMime(mapItem)
		}
		// Try all known data fields in priority order.
		for _, field := range []string{"file_data", "data", "base64", "content", "bytes", "file_content"} {
			if fileData, ok := fileObj[field].(string); ok && fileData != "" {
				if img, ok := decodeFilePayload(fileData, filename, mimeHint); ok {
					return img, true
				}
			}
			if fileData, ok := mapItem[field].(string); ok && fileData != "" {
				if img, ok := decodeFilePayload(fileData, filename, mimeHint); ok {
					return img, true
				}
			}
		}
		// Try URL/path fields (may be file:// or local path — handled downstream).
		for _, field := range []string{"file_url", "url", "path", "file_path", "local_path"} {
			if fileURL, ok := fileObj[field].(string); ok && fileURL != "" {
				img, ok := imageFromURLString(fileURL, mimeHint)
				if ok {
					img.Filename = filename
					return img, true
				}
			}
			if fileURL, ok := mapItem[field].(string); ok && fileURL != "" {
				img, ok := imageFromURLString(fileURL, mimeHint)
				if ok {
					img.Filename = filename
					return img, true
				}
			}
		}
	case "input_file":
		filename, _ := mapItem["filename"].(string)
		if filename == "" {
			filename, _ = mapItem["name"].(string)
		}
		mimeHint := partMime(mapItem)
		for _, field := range []string{"file_data", "data", "base64", "content", "bytes", "file_content"} {
			if fileData, ok := mapItem[field].(string); ok && fileData != "" {
				if img, ok := decodeFilePayload(fileData, filename, mimeHint); ok {
					return img, true
				}
			}
		}
		for _, field := range []string{"file_url", "url", "path", "file_path", "local_path"} {
			if fileURL, ok := mapItem[field].(string); ok && fileURL != "" {
				img, ok := imageFromURLString(fileURL, mimeHint)
				if ok {
					img.Filename = filename
					return img, true
				}
			}
		}
	case "document":
		filename, _ := mapItem["filename"].(string)
		if filename == "" {
			filename, _ = mapItem["title"].(string)
		}
		if filename == "" {
			filename, _ = mapItem["name"].(string)
		}
		if source, ok := mapItem["source"].(map[string]any); ok {
			mimeHint := partMime(source)
			for _, field := range []string{"data", "file_data", "base64", "content", "bytes"} {
				if dataStr, ok := source[field].(string); ok && dataStr != "" {
					if img, ok := decodeFilePayload(dataStr, filename, mimeHint); ok {
						return img, true
					}
				}
			}
			for _, field := range []string{"url", "file_url", "path"} {
				if urlStr, ok := source[field].(string); ok && urlStr != "" {
					img, ok := imageFromURLString(urlStr, mimeHint)
					if ok {
						img.Filename = filename
						return img, true
					}
				}
			}
		}
		// Flat document fields (WorkBuddy may send document without nested source).
		mimeHint := partMime(mapItem)
		for _, field := range []string{"data", "file_data", "base64", "content", "bytes"} {
			if dataStr, ok := mapItem[field].(string); ok && dataStr != "" {
				if img, ok := decodeFilePayload(dataStr, filename, mimeHint); ok {
					return img, true
				}
			}
		}
	}

	return Image{}, false
}

func decodeFilePayload(payload, filename, mimeHint string) (Image, bool) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return Image{}, false
	}
	if strings.HasPrefix(trimmed, "data:") {
		data, decMime, err := DecodeDataURL(trimmed)
		if err != nil {
			return Image{}, false
		}
		if mimeHint == "" {
			mimeHint = decMime
		}
		return Image{Data: data, MIME: mimeHint, Filename: filename}, true
	}
	// WorkBuddy may send raw UTF-8 text (not base64) for .txt/.md/.code attachments.
	// Heuristic: if payload contains newlines or non-base64 chars and is valid UTF-8,
	// treat as raw text bytes. Base64 payloads are single-line without spaces.
	// Use original payload (not trimmed) to preserve exact file bytes including
	// trailing newlines.
	if isLikelyRawText(payload) {
		data := []byte(payload)
		if mimeHint == "" {
			mimeHint = mimeForFilename(filename)
		}
		return Image{Data: data, MIME: mimeHint, Filename: filename}, true
	}
	decoded, err := multimodal.DecodeBase64Raw(trimmed)
	if err != nil {
		// Last resort: treat as raw text if it looks like text.
		if isLikelyRawText(payload) {
			data := []byte(payload)
			if mimeHint == "" {
				mimeHint = mimeForFilename(filename)
			}
			return Image{Data: data, MIME: mimeHint, Filename: filename}, true
		}
		return Image{}, false
	}
	if mimeHint == "" {
		if len(decoded) >= 4 && string(decoded[:4]) == "%PDF" {
			mimeHint = "application/pdf"
		} else {
			mimeHint = mimeForFilename(filename)
		}
	}
	return Image{Data: decoded, MIME: mimeHint, Filename: filename}, true
}

func isLikelyRawText(s string) bool {
	// Base64 is typically one long line of [A-Za-z0-9+/=] without spaces/newlines.
	// Raw text files contain newlines, spaces, or punctuation.
	if strings.Contains(s, "\n") || strings.Contains(s, " ") {
		return true
	}
	// Short payloads with punctuation are likely text, not base64.
	if len(s) < 200 && strings.ContainsAny(s, "{}[]()<>#@!$%^&*") {
		return true
	}
	return false
}

func mimeForFilename(filename string) string {
	ext := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(ext, ".pdf"):
		return "application/pdf"
	case strings.HasSuffix(ext, ".txt"), strings.HasSuffix(ext, ".log"):
		return "text/plain"
	case strings.HasSuffix(ext, ".md"), strings.HasSuffix(ext, ".markdown"):
		return "text/markdown"
	case strings.HasSuffix(ext, ".json"):
		return "application/json"
	case strings.HasSuffix(ext, ".csv"):
		return "text/csv"
	case strings.HasSuffix(ext, ".html"), strings.HasSuffix(ext, ".htm"):
		return "text/html"
	case strings.HasSuffix(ext, ".png"):
		return "image/png"
	case strings.HasSuffix(ext, ".jpg"), strings.HasSuffix(ext, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(ext, ".gif"):
		return "image/gif"
	case strings.HasSuffix(ext, ".webp"):
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// BuildToolChoiceInstruction returns a prompt suffix based on the tool_choice parameter.
func BuildToolChoiceInstruction(toolChoice any) string {
	if strChoice, ok := toolChoice.(string); ok {
		if strChoice == "none" {
			return "\n\nIMPORTANT: Do NOT call any tools. Respond with text only."
		}
		if strChoice == "required" {
			return "\n\nIMPORTANT: You MUST call at least one tool. Do not respond with text only."
		}
	} else if mapChoice, ok := toolChoice.(map[string]any); ok {
		if fn, ok := mapChoice["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				return fmt.Sprintf("\n\nIMPORTANT: You MUST call the tool \"%s\". Do not call other tools.", name)
			}
		}
	}
	return ""
}

// ExtractMessageContent extracts text and multimodal attachments (images + files) from one OpenAI message.
func ExtractMessageContent(msg models.OpenAIMessage) (string, []Image) {
	var attachments []Image

	if strContent, ok := msg.Content.(string); ok {
		return strContent, nil
	}

	contentList, ok := msg.Content.([]any)
	if !ok {
		return "", nil
	}

	var textParts []string
	for _, item := range contentList {
		if mapItem, ok := item.(map[string]any); ok {
			iType, _ := mapItem["type"].(string)
			if iType == "text" || iType == "input_text" || iType == "output_text" {
				if t, ok := mapItem["text"].(string); ok {
					textParts = append(textParts, t)
				}
			} else if img, ok := ImageFromPart(mapItem); ok {
				attachments = append(attachments, img)
				textParts = append(textParts, "[Image attached]")
			} else if fileAtt, ok := FileFromPart(mapItem); ok {
				attachments = append(attachments, fileAtt)
				if fileAtt.Filename != "" {
					textParts = append(textParts, fmt.Sprintf("[File attached: %s]", fileAtt.Filename))
				} else {
					textParts = append(textParts, "[File attached]")
				}
			}
		} else if s, ok := item.(string); ok && s != "" {
			textParts = append(textParts, s)
		}
	}

	return strings.Join(textParts, " "), attachments
}

// BuildToolCallNameMap maps OpenAI tool_call_id values from assistant messages to their function names,
// so subsequent role="tool" messages that omit `name` still include the tool name in the prompt.
func BuildToolCallNameMap(messages []models.OpenAIMessage) map[string]string {
	var names map[string]string
	for _, msg := range messages {
		if len(msg.ToolCalls) == 0 {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				if names == nil {
					names = make(map[string]string)
				}
				names[tc.ID] = tc.Function.Name
			}
		}
	}
	return names
}

func formatMessagePartWithNames(msg models.OpenAIMessage, contentStr string, toolCallNames map[string]string) string {
	role := msg.Role
	if role == "" {
		role = "user"
	}

	switch role {
	case "system", "developer":
		return fmt.Sprintf("[System instruction]: %s", contentStr)
	case "assistant":
		if len(msg.ToolCalls) > 0 {
			var tcStrs []string
			for _, tc := range msg.ToolCalls {
				argsStr := tc.Function.Arguments
				if argsStr == "" {
					argsStr = "{}"
				}
				tcStrs = append(tcStrs, fmt.Sprintf("```tool_call\n{\"name\": \"%s\", \"arguments\": %s}\n```", tc.Function.Name, argsStr))
			}
			return fmt.Sprintf("[Assistant]: %s\n%s", contentStr, strings.Join(tcStrs, "\n"))
		}
		return fmt.Sprintf("[Assistant]: %s", contentStr)
	case "tool":
		toolName := strings.TrimSpace(msg.Name)
		if toolName == "" && msg.ToolCallID != "" && toolCallNames != nil {
			toolName = toolCallNames[msg.ToolCallID]
		}
		return fmt.Sprintf("[Tool result for %s]: %s", toolName, contentStr)
	default:
		return contentStr
	}
}

func truncateOneLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-1]) + "…"
}

func requiredSetFromSchema(schema map[string]any) map[string]bool {
	required := make(map[string]bool)
	if schema == nil {
		return required
	}
	if reqRaw, ok := schema["required"]; ok {
		switch arr := reqRaw.(type) {
		case []any:
			for _, item := range arr {
				if name, ok := item.(string); ok {
					required[name] = true
				}
			}
		case []string:
			for _, name := range arr {
				required[name] = true
			}
		}
	}
	return required
}

// compactSchemaType formats a JSON Schema property type compactly while preserving
// enum options, array element types, union (anyOf/oneOf) types, and nested object keys
// required by MCP / WorkBuddy connector tools (e.g. GitHub PRs, Gmail).
func compactSchemaType(pMap map[string]any, depth int) string {
	if pMap == nil {
		return "any"
	}

	// Extract explicit type (may be string or []any like ["string","null"])
	pType := ""
	switch tv := pMap["type"].(type) {
	case string:
		pType = tv
	case []any:
		var nonNull []string
		for _, item := range tv {
			if s, ok := item.(string); ok && s != "" && s != "null" {
				nonNull = append(nonNull, s)
			}
		}
		if len(nonNull) > 0 {
			pType = strings.Join(nonNull, "|")
		}
	}

	// Handle anyOf / oneOf schemas commonly emitted by Zod / Pydantic MCP connectors
	if pType == "" {
		for _, unionKey := range []string{"anyOf", "oneOf"} {
			if unionList, ok := pMap[unionKey].([]any); ok && len(unionList) > 0 {
				var unionTypes []string
				seen := make(map[string]bool)
				for _, branch := range unionList {
					bm, ok := branch.(map[string]any)
					if !ok {
						continue
					}
					if bt, _ := bm["type"].(string); bt == "null" {
						continue
					}
					sub := compactSchemaType(bm, depth+1)
					if sub != "" && !seen[sub] {
						seen[sub] = true
						unionTypes = append(unionTypes, sub)
					}
				}
				if len(unionTypes) > 0 {
					return strings.Join(unionTypes, "|")
				}
			}
		}
	}

	if pType == "" {
		if _, hasProps := pMap["properties"].(map[string]any); hasProps {
			pType = "object"
		} else if _, hasItems := pMap["items"]; hasItems {
			pType = "array"
		} else {
			pType = "any"
		}
	}

	// Preserve enum values compactly (e.g. string(open|closed))
	if enumList, ok := pMap["enum"].([]any); ok && len(enumList) > 0 {
		var vals []string
		limit := len(enumList)
		if limit > 8 {
			limit = 8
		}
		for i := 0; i < limit; i++ {
			vStr := truncateOneLine(fmt.Sprintf("%v", enumList[i]), 24)
			if vStr != "" {
				vals = append(vals, vStr)
			}
		}
		if len(vals) > 0 {
			suffix := strings.Join(vals, "|")
			if len(enumList) > limit {
				suffix += "|…"
			}
			return fmt.Sprintf("%s(%s)", pType, suffix)
		}
	}

	// Preserve array item types (e.g. array[string], array[{path*,content*}])
	if pType == "array" && depth < 2 {
		if itemMap, ok := pMap["items"].(map[string]any); ok {
			return fmt.Sprintf("array[%s]", compactSchemaType(itemMap, depth+1))
		}
	}

	// Preserve nested object property keys & required markers (e.g. {owner*,repo*,head*,base*,title*})
	if pType == "object" && depth < 2 {
		if nestedProps, ok := pMap["properties"].(map[string]any); ok && len(nestedProps) > 0 {
			nestedReq := requiredSetFromSchema(pMap)
			keys := make([]string, 0, len(nestedProps))
			for k := range nestedProps {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			limit := len(keys)
			if limit > 12 {
				limit = 12
			}
			parts := make([]string, 0, limit)
			for i := 0; i < limit; i++ {
				k := keys[i]
				label := k
				if nestedReq[k] {
					label += "*"
				}
				if subMap, ok := nestedProps[k].(map[string]any); ok && depth < 1 {
					subType := compactSchemaType(subMap, depth+1)
					if subType != "" && subType != "any" {
						label += ":" + subType
					}
				}
				parts = append(parts, label)
			}
			if len(keys) > limit {
				parts = append(parts, "…")
			}
			return "{" + strings.Join(parts, ",") + "}"
		}
	}

	return pType
}

type compactToolDef struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
}

// compactToolDefinitions keeps every tool's name, concise description, parameter
// names/types/enums/nested keys, and required (*) markers while shrinking verbose
// OpenAI JSON Schemas by ~70-85% so the entire # Tool Use header stays inline.
func compactToolDefinitions(toolDefs []models.OpenAIFunction) []byte {
	fullJSON, _ := json.Marshal(toolDefs)
	if len(fullJSON) <= 10000 {
		return fullJSON
	}

	buildCompact := func(maxDesc, maxParamDesc int) []byte {
		out := make([]compactToolDef, len(toolDefs))
		for i, fn := range toolDefs {
			paramsMap, _ := fn.Parameters.(map[string]any)
			required := requiredSetFromSchema(paramsMap)

			var params map[string]string
			if props, ok := paramsMap["properties"].(map[string]any); ok && len(props) > 0 {
				params = make(map[string]string, len(props))
				for pName, pRaw := range props {
					pType := "any"
					pDesc := ""
					if pMap, ok := pRaw.(map[string]any); ok {
						pType = compactSchemaType(pMap, 0)
						if maxParamDesc > 0 {
							if d, ok := pMap["description"].(string); ok && d != "" {
								pDesc = truncateOneLine(d, maxParamDesc)
							}
						}
					}
					spec := pType
					if required[pName] {
						spec += "*"
					}
					if pDesc != "" {
						spec += " - " + pDesc
					}
					params[pName] = spec
				}
			}

			out[i] = compactToolDef{
				Name:        fn.Name,
				Description: truncateOneLine(fn.Description, maxDesc),
				Params:      params,
			}
		}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(out)
		return bytes.TrimSpace(buf.Bytes())
	}

	compacted := buildCompact(160, 60)
	if len(compacted) > 22000 {
		compacted = buildCompact(100, 32)
	}
	if len(compacted) > 28000 {
		compacted = buildCompact(72, 0)
	}
	log.Printf("Compacted %d tool definitions from %d bytes to %d bytes (preserving parameter names & nested/enum schemas)", len(toolDefs), len(fullJSON), len(compacted))
	return compacted
}

// CompactToolDefinitionsJSON converts OpenAI tool declarations into the compact
// schema JSON representation used in # Tool Use and additive mid-chat tool deltas.
func CompactToolDefinitionsJSON(tools []models.OpenAITool) string {
	if len(tools) == 0 {
		return ""
	}
	defs := make([]models.OpenAIFunction, 0, len(tools))
	for _, tool := range tools {
		fn := tool.Function
		if fn.Name == "" {
			fn.Name = tool.Name
		}
		if fn.Description == "" {
			fn.Description = tool.Description
		}
		if fn.Parameters == nil {
			fn.Parameters = tool.Parameters
		}
		if fn.Parameters == nil {
			fn.Parameters = map[string]any{}
		}
		if fn.Name != "" {
			defs = append(defs, fn)
		}
	}
	if len(defs) == 0 {
		return ""
	}
	return string(compactToolDefinitions(defs))
}

// AllowedToolNames extracts the set of declared tool function names from an OpenAI request.
func AllowedToolNames(tools []models.OpenAITool) map[string]bool {
	if len(tools) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			name = strings.TrimSpace(tool.Name)
		}
		if name != "" {
			allowed[name] = true
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

// DeferredToolConfig describes WorkBuddy's deferred connector/MCP loading tools
// (ToolSearch + DeferExecuteTool) when present in an OpenAI request.
type DeferredToolConfig struct {
	HasToolSearch bool
	HasDeferExec  bool
	DeferExecName string
	ToolNameKey   string
	ParamsKey     string
}

// DetectDeferredToolConfig inspects declared OpenAI tools to detect WorkBuddy's
// ToolSearch + DeferExecuteTool connector bridge and its exact schema parameter keys.
func DetectDeferredToolConfig(tools []models.OpenAITool) DeferredToolConfig {
	cfg := DeferredToolConfig{
		DeferExecName: "DeferExecuteTool",
		ToolNameKey:   "toolName",
		ParamsKey:     "params",
	}
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			name = strings.TrimSpace(tool.Name)
		}
		switch name {
		case "ToolSearch":
			cfg.HasToolSearch = true
		case "DeferExecuteTool":
			cfg.HasDeferExec = true
			cfg.DeferExecName = name
			paramsMap, _ := tool.Function.Parameters.(map[string]any)
			if paramsMap == nil {
				paramsMap, _ = tool.Parameters.(map[string]any)
			}
			if props, ok := paramsMap["properties"].(map[string]any); ok {
				for _, k := range []string{"toolName", "tool_name", "name", "tool"} {
					if _, exists := props[k]; exists {
						cfg.ToolNameKey = k
						break
					}
				}
				for _, k := range []string{"params", "arguments", "parameters", "args", "input"} {
					if _, exists := props[k]; exists {
						cfg.ParamsKey = k
						break
					}
				}
			}
		}
	}
	return cfg
}

var (
	reMCPToolIdent          = regexp.MustCompile(`\bmcp__[A-Za-z0-9_.-]+\b`)
	reDeferredToolsTagBlock = regexp.MustCompile(`(?is)<available[-_]deferred[-_]tools>\s*(.*?)\s*</available[-_]deferred[-_]tools>`)
	reJSONToolNameField     = regexp.MustCompile(`"(?:name|toolName|tool_name)"\s*:\s*"([A-Za-z][A-Za-z0-9_.:-]{2,128})"`)
	reSimpleToolIdentLine   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{2,128}$`)
)

// ExtractDeferredToolNames discovers deferred connector/MCP tool names mentioned in
// system prompts (<available-deferred-tools> or mcp__* identifiers) and prior
// ToolSearch tool results so direct calls to those tools can be auto-wrapped into
// DeferExecuteTool even when they don't use an `mcp__` prefix.
func ExtractDeferredToolNames(messages []models.OpenAIMessage) map[string]bool {
	if len(messages) == 0 {
		return nil
	}
	toolCallNames := BuildToolCallNameMap(messages)
	var discovered map[string]bool
	addName := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || n == "ToolSearch" || n == "DeferExecuteTool" {
			return
		}
		if discovered == nil {
			discovered = make(map[string]bool)
		}
		discovered[n] = true
	}

	for _, msg := range messages {
		text, _ := ExtractMessageContent(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(msg.Role))

		// Always capture explicit mcp__* names in system prompts or tool results
		if role == "system" || role == "developer" || role == "tool" {
			for _, m := range reMCPToolIdent.FindAllString(text, -1) {
				addName(m)
			}
		}

		// Parse <available-deferred-tools> lists in system prompts
		if role == "system" || role == "developer" {
			for _, block := range reDeferredToolsTagBlock.FindAllStringSubmatch(text, -1) {
				for _, line := range strings.Split(block[1], "\n") {
					line = strings.TrimSpace(strings.TrimLeft(line, "-*• "))
					if idx := strings.IndexAny(line, " \t:("); idx != -1 {
						line = strings.TrimSpace(line[:idx])
					}
					if reSimpleToolIdentLine.MatchString(line) {
						addName(line)
					}
				}
			}
		}

		// Parse ToolSearch results (even for deferred tools that don't start with mcp__)
		if role == "tool" {
			toolName := strings.TrimSpace(msg.Name)
			if toolName == "" && msg.ToolCallID != "" {
				toolName = toolCallNames[msg.ToolCallID]
			}
			if toolName == "ToolSearch" {
				for _, match := range reJSONToolNameField.FindAllStringSubmatch(text, -1) {
					addName(match[1])
				}
			}
		}
	}
	return discovered
}

// BuildToolSearchFollowUpReminder returns a concise one-line execution reminder
// to append right after a delta ToolSearch tool result so Gemini invokes discovered
// WorkBuddy connector tools via DeferExecuteTool with exact target verification.
func BuildToolSearchFollowUpReminder(tools []models.OpenAITool, toolChoice any) string {
	if strChoice, ok := toolChoice.(string); ok && strChoice == "none" {
		return ""
	}
	cfg := DetectDeferredToolConfig(tools)
	if !cfg.HasToolSearch || !cfg.HasDeferExec {
		return ""
	}
	return fmt.Sprintf(
		"[Invoke discovered connector/MCP tools using ```tool_call\n{\"name\": %q, \"arguments\": {%q: \"<exact_tool_name>\", %q: {...}}}\n``` — verify exact target identifiers (e.g. exact GitHub owner/repo, never an upstream parent repo unless asked) before mutative actions.]",
		cfg.DeferExecName, cfg.ToolNameKey, cfg.ParamsKey,
	)
}

// BuildDeltaToolReminder returns a compact one-line reminder for follow-up user messages
// in a delta-context session so Gemini Enterprise keeps emitting ```tool_call``` blocks
// for both local tools and connected WorkBuddy apps/connectors (GitHub, Gmail, etc.).
func BuildDeltaToolReminder(tools []models.OpenAITool, toolChoice any) string {
	if strChoice, ok := toolChoice.(string); ok && strChoice == "none" {
		return ""
	}
	if len(tools) == 0 {
		return ""
	}
	const maxListed = 128
	var names []string
	extra := 0
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			name = strings.TrimSpace(tool.Name)
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if len(names) < maxListed {
			names = append(names, name)
		} else {
			extra++
		}
	}
	if len(names) == 0 {
		return ""
	}
	list := strings.Join(names, ", ")
	if extra > 0 {
		list += fmt.Sprintf(" (+%d more)", extra)
	}
	cfg := DetectDeferredToolConfig(tools)
	extraHint := ""
	if cfg.HasToolSearch && cfg.HasDeferExec {
		extraHint = fmt.Sprintf(
			" For WorkBuddy connectors/MCP tools (Gmail, GitHub, mcp__*), call ToolSearch first, then %s with {%q: \"<exact_name>\", %q: {...}} — always verify exact target repo/owner before write actions.",
			cfg.DeferExecName, cfg.ToolNameKey, cfg.ParamsKey,
		)
	}
	return fmt.Sprintf(
		"[Local client bridge active: to use local tools or connected WorkBuddy apps/connectors (GitHub, Gmail, files, shell, etc.), output a ```tool_call\n{\"name\": \"...\", \"arguments\": {...}}\n``` block — the user's local app executes it automatically and returns the result.%s Available tools: %s]",
		extraHint,
		list,
	)
}

func MessagesToPrompt(req models.OpenAIChatRequest) (string, []Image, error) {
	var parts []string
	var images []Image

	strChoice, isStr := req.ToolChoice.(string)
	if (!isStr || strChoice != "none") && len(req.Tools) > 0 {
		var toolDefs []models.OpenAIFunction
		seenTool := make(map[string]bool, len(req.Tools))
		for _, tool := range req.Tools {
			fn := tool.Function
			if fn.Name == "" {
				fn.Name = tool.Name
			}
			if fn.Description == "" {
				fn.Description = tool.Description
			}
			if fn.Parameters == nil {
				fn.Parameters = tool.Parameters
			}
			if fn.Parameters == nil {
				fn.Parameters = map[string]any{}
			}
			if name := strings.TrimSpace(fn.Name); name != "" {
				// MCP servers (and merged connector sets) happily declare the same
				// tool twice; a duplicate preamble only wastes prompt budget.
				if seenTool[name] {
					continue
				}
				seenTool[name] = true
			}
			if strings.TrimSpace(fn.Name) == "" {
				// Built-in tool declarations such as {"type":"web_search"} carry no
				// function name; emitting them would put a nameless entry in the
				// prompt and widen the allowed-name set.
				continue
			}

			toolDefs = append(toolDefs, fn)
		}

		if len(toolDefs) > 0 {
			deferCfg := DetectDeferredToolConfig(req.Tools)
			connectorNote := ""
			if deferCfg.HasToolSearch && deferCfg.HasDeferExec {
				connectorNote = fmt.Sprintf(
					"- WorkBuddy connectors & MCP tools (e.g. Gmail, GitHub, Drive, mcp__*): call `ToolSearch` first to load the tool's exact schema, then invoke it via `%s` with `{%q: \"<exact_name>\", %q: {...}}` (do not guess parameters or call deferred tools directly).\n"+
						"- Connector safety: before mutative external actions (creating PRs, deleting/sending emails, editing remote repos), verify exact target identifiers (e.g. exact GitHub `owner`/`repo` — never open a PR against an upstream/parent fork repo unless explicitly requested).\n",
					deferCfg.DeferExecName, deferCfg.ToolNameKey, deferCfg.ParamsKey,
				)
			}
			guard := ""
			if req.ResearchGuard {
				guard = ExecutionModeLines("")
			}
			if req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
				guard += "- The client disabled parallel tool calls: emit exactly ONE tool_call block per turn, then wait for its result.\n"
			}
			constraint := BuildToolChoiceInstruction(req.ToolChoice)
			defsJSON := compactToolDefinitions(toolDefs)
			parts = append(parts, fmt.Sprintf(
				"# Tool Use\n\n"+
					"An external local client bridge (WorkBuddy) is connected to this chat. Whenever you output a ```tool_call``` block, the client automatically runs that local tool or connected app connector (e.g. GitHub, Gmail, local files, shell) on the user's behalf and returns the output in the next turn as `[Tool result for <name>]`.\n"+
					"- You DO have execution access to ALL tools & connectors listed below through this bridge. NEVER refuse by saying you are in a cloud environment or cannot access local files, shell, GitHub, email, or external apps — call the matching declared tools below instead.\n"+
					"- Do NOT use Gemini's built-in web search, Deep Research, or any built-in tools for these tasks. Local files, shell commands, GitHub repos and email must be handled by emitting the declared tool_call blocks below — never research or guess their contents yourself.\n"+
					"- Host-side tools are a different set: if this session exposes its own tools or actions (an MCP connector, a document generator), they execute on the host and are allowed — use one when it is the right tool for a step, and never claim you have no tools other than this list.\n"+
					"%s"+
					"%s"+
					"- Call format (strict JSON object with \"name\" and \"arguments\"):\n"+
					"```tool_call\n{\"name\": \"func_name\", \"arguments\": {...}}\n```\n"+
					"- Output ONLY the tool_call block(s) when calling tools. Batch ALL independent calls (e.g. multiple reads, searches, or checks) into one response to minimize round-trips.\n"+
					"- If a file or image is already attached directly to this message, read it natively instead of calling local tools to re-read it (attached files are virtual uploads, not on disk — do not call Read for them).\n\n"+
					"Available tools:\n%s%s",
				connectorNote,
				guard,
				string(defsJSON),
				constraint,
			))
		}
	}

	toolCallNames := BuildToolCallNameMap(req.Messages)
	for _, msg := range req.Messages {
		contentStr, msgAtts := ExtractMessageContent(msg)
		if len(msgAtts) > 0 {
			images = append(images, msgAtts...)
		}
		if formatted := formatMessagePartWithNames(msg, contentStr, toolCallNames); formatted != "" {
			parts = append(parts, formatted)
		}
	}

	return strings.Join(parts, "\n\n"), images, nil
}

// MessagesToDeltaPrompt formats only newly appended messages for an ongoing browser-bridge
// conversation where the open Gemini tab already holds the earlier turns and tool definitions.
func MessagesToDeltaPrompt(deltaMessages []models.OpenAIMessage, toolChoice any) (string, []Image) {
	return MessagesToDeltaPromptWithNames(deltaMessages, toolChoice, nil)
}

// MessagesToDeltaPromptWithNames formats newly appended delta messages while resolving
// role="tool" tool_call_id references against earlier assistant tool_calls.
func MessagesToDeltaPromptWithNames(deltaMessages []models.OpenAIMessage, toolChoice any, priorToolNames map[string]string) (string, []Image) {
	if len(deltaMessages) == 0 {
		return "", nil
	}

	// Merge prior tool_call_id -> name mappings with any assistant tool_calls in deltaMessages
	toolCallNames := make(map[string]string, len(priorToolNames))
	for k, v := range priorToolNames {
		toolCallNames[k] = v
	}
	for k, v := range BuildToolCallNameMap(deltaMessages) {
		toolCallNames[k] = v
	}

	// If the first delta message is the assistant turn Gemini itself just generated,
	// skip it when followed by new tool/user messages so we do not echo Gemini's own reply back.
	startIdx := 0
	if deltaMessages[0].Role == "assistant" && len(deltaMessages) > 1 {
		startIdx = 1
	}

	var parts []string
	var attachments []Image
	for _, msg := range deltaMessages[startIdx:] {
		contentStr, msgAtts := ExtractMessageContent(msg)
		if len(msgAtts) > 0 {
			attachments = append(attachments, msgAtts...)
		}
		if formatted := formatMessagePartWithNames(msg, contentStr, toolCallNames); formatted != "" {
			parts = append(parts, formatted)
		}
	}

	prompt := strings.Join(parts, "\n\n")
	if constraint := strings.TrimSpace(BuildToolChoiceInstruction(toolChoice)); constraint != "" && prompt != "" {
		prompt += "\n\n" + constraint
	}
	return prompt, attachments
}

var (
	reToolCall           = regexp.MustCompile("(?s)\\x60\\x60\\x60(?:tool_calls?|function_call)[ \\t]*\\r?\\n(.*?)\\s*\\x60\\x60\\x60")
	reXMLToolCall        = regexp.MustCompile(`(?is)<tool_call>\s*(.*?)\s*</tool_call>`)
	reJSONToolCall       = regexp.MustCompile("(?s)\\x60\\x60\\x60json[ \\t]*\\r?\\n(.*?)\\s*\\x60\\x60\\x60")
	reTrailingJSONCommas = regexp.MustCompile(`,\s*([}\]])`)
)

type rawToolCallFunction struct {
	Name       string `json:"name"`
	Arguments  any    `json:"arguments"`
	Parameters any    `json:"parameters"`
	Args       any    `json:"args"`
	Input      any    `json:"input"`
}

type rawToolCallPayload struct {
	Name       string               `json:"name"`
	Tool       string               `json:"tool"`
	ToolName   string               `json:"tool_name"`
	Arguments  any                  `json:"arguments"`
	Parameters any                  `json:"parameters"`
	Args       any                  `json:"args"`
	Input      any                  `json:"input"`
	Function   *rawToolCallFunction `json:"function"`
	ToolCalls  []rawToolCallPayload `json:"tool_calls"`
	Calls      []rawToolCallPayload `json:"calls"`
}

func (p rawToolCallPayload) resolvedName() string {
	for _, candidate := range []string{p.Name, p.Tool, p.ToolName} {
		if s := strings.TrimSpace(candidate); s != "" {
			return s
		}
	}
	if p.Function != nil {
		return strings.TrimSpace(p.Function.Name)
	}
	return ""
}

func (p rawToolCallPayload) resolvedArgs() any {
	for _, candidate := range []any{p.Arguments, p.Parameters, p.Args, p.Input} {
		if candidate != nil {
			return candidate
		}
	}
	if p.Function != nil {
		for _, candidate := range []any{p.Function.Arguments, p.Function.Parameters, p.Function.Args, p.Function.Input} {
			if candidate != nil {
				return candidate
			}
		}
	}
	return nil
}

func decodeToolCallItems(raw json.RawMessage) ([]rawToolCallPayload, bool) {
	rawTrim := strings.TrimSpace(string(raw))
	if rawTrim == "" {
		return nil, true
	}

	tryUnmarshal := func(data []byte) ([]rawToolCallPayload, bool) {
		trimmed := strings.TrimSpace(string(data))
		if strings.HasPrefix(trimmed, "[") {
			var items []rawToolCallPayload
			if err := json.Unmarshal(data, &items); err == nil {
				return items, true
			}
			return nil, false
		}
		var single rawToolCallPayload
		if err := json.Unmarshal(data, &single); err == nil {
			if len(single.ToolCalls) > 0 {
				return single.ToolCalls, true
			}
			if len(single.Calls) > 0 {
				return single.Calls, true
			}
			return []rawToolCallPayload{single}, true
		}
		return nil, false
	}

	if items, ok := tryUnmarshal(raw); ok {
		return items, true
	}
	cleaned := reTrailingJSONCommas.ReplaceAll(raw, []byte("$1"))
	if string(cleaned) != string(raw) {
		return tryUnmarshal(cleaned)
	}
	return nil, false
}

type toolParseOpts struct {
	requireAllowed bool
	allowed        map[string]bool
	deferCfg       DeferredToolConfig
	deferredNames  map[string]bool
}

func unwrapJSONObject(val any) any {
	if val == nil {
		return map[string]any{}
	}
	if str, ok := val.(string); ok {
		trimmed := strings.TrimSpace(str)
		if trimmed == "" {
			return map[string]any{}
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			return parsed
		}
		cleaned := reTrailingJSONCommas.ReplaceAllString(trimmed, "$1")
		if cleaned != trimmed {
			if err := json.Unmarshal([]byte(cleaned), &parsed); err == nil {
				return parsed
			}
		}
		return str
	}
	return val
}

func marshalArgsValue(val any) string {
	if str, ok := val.(string); ok {
		if strings.TrimSpace(str) == "" {
			return "{}"
		}
		return str
	}
	if val != nil {
		b, _ := json.Marshal(val)
		return string(b)
	}
	return "{}"
}

func shouldAutoWrapDeferred(name string, opts toolParseOpts) bool {
	if !opts.deferCfg.HasDeferExec || name == "" || name == opts.deferCfg.DeferExecName {
		return false
	}
	if opts.allowed != nil && opts.allowed[name] {
		return false
	}
	if strings.HasPrefix(name, "mcp__") || strings.Contains(name, "__") {
		return true
	}
	if opts.deferredNames != nil && opts.deferredNames[name] {
		return true
	}
	return false
}

func normalizeDeferExecuteArgs(rawArgs any, cfg DeferredToolConfig) string {
	unwrapped := unwrapJSONObject(rawArgs)
	m, ok := unwrapped.(map[string]any)
	if !ok {
		return marshalArgsValue(rawArgs)
	}

	nameKeys := []string{cfg.ToolNameKey, "toolName", "tool_name", "name", "tool", "function_name", "function"}
	paramKeys := []string{cfg.ParamsKey, "params", "arguments", "parameters", "args", "input"}

	targetName := ""
	matchedNameKey := ""
	for _, k := range nameKeys {
		if k == "" {
			continue
		}
		if v, exists := m[k]; exists {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				targetName = strings.TrimSpace(s)
				matchedNameKey = k
				break
			}
		}
	}
	if targetName == "" {
		return marshalArgsValue(rawArgs)
	}

	var paramsVal any
	hasParamsKey := false
	for _, k := range paramKeys {
		if k == "" || k == matchedNameKey {
			continue
		}
		if v, exists := m[k]; exists {
			paramsVal = unwrapJSONObject(v)
			hasParamsKey = true
			break
		}
	}

	// If Gemini flattened the connector's parameters alongside toolName instead of
	// nesting them inside `params` (e.g. {"toolName":"mcp__github__...","owner":"Hau04","repo":"..."}),
	// automatically collect those remaining fields into `params`.
	if !hasParamsKey {
		extra := make(map[string]any)
		for k, v := range m {
			if k == matchedNameKey {
				continue
			}
			extra[k] = v
		}
		paramsVal = extra
	}
	if paramsVal == nil {
		paramsVal = map[string]any{}
	}

	b, _ := json.Marshal(map[string]any{
		cfg.ToolNameKey: targetName,
		cfg.ParamsKey:   paramsVal,
	})
	return string(b)
}

func decodeToolCallPayload(content string, opts toolParseOpts) []models.OpenAIToolCall {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	decodeStream := func(src string) []models.OpenAIToolCall {
		var out []models.OpenAIToolCall
		dec := json.NewDecoder(strings.NewReader(src))
		for {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				if err == io.EOF {
					break
				}
				return nil
			}
			items, ok := decodeToolCallItems(raw)
			if !ok {
				return nil
			}

			for _, data := range items {
				name := data.resolvedName()
				if name == "" {
					if opts.requireAllowed {
						return nil
					}
					continue
				}
				canWrapDeferred := shouldAutoWrapDeferred(name, opts)
				if opts.requireAllowed && !opts.allowed[name] && !canWrapDeferred {
					return nil
				}

				argsVal := data.resolvedArgs()
				if canWrapDeferred {
					wrapped, _ := json.Marshal(map[string]any{
						opts.deferCfg.ToolNameKey: name,
						opts.deferCfg.ParamsKey:   unwrapJSONObject(argsVal),
					})
					out = append(out, models.OpenAIToolCall{
						ID:   fmt.Sprintf("call_%s", RandHex(8)),
						Type: "function",
						Function: models.OpenAIToolCallFunction{
							Name:      opts.deferCfg.DeferExecName,
							Arguments: string(wrapped),
						},
					})
					continue
				}

				var argsStr string
				if opts.deferCfg.HasDeferExec && name == opts.deferCfg.DeferExecName {
					argsStr = normalizeDeferExecuteArgs(argsVal, opts.deferCfg)
				} else {
					argsStr = marshalArgsValue(argsVal)
				}
				out = append(out, models.OpenAIToolCall{
					ID:   fmt.Sprintf("call_%s", RandHex(8)),
					Type: "function",
					Function: models.OpenAIToolCallFunction{
						Name:      name,
						Arguments: argsStr,
					},
				})
			}
		}
		return out
	}

	if calls := decodeStream(trimmed); len(calls) > 0 {
		return calls
	}
	cleaned := reTrailingJSONCommas.ReplaceAllString(trimmed, "$1")
	if cleaned != trimmed {
		return decodeStream(cleaned)
	}
	return nil
}

func extractBlocksWithRegex(text string, re *regexp.Regexp, opts toolParseOpts) (string, []models.OpenAIToolCall) {
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	var toolCalls []models.OpenAIToolCall
	var cleanParts []string
	lastEnd := 0

	for _, m := range matches {
		blockCalls := decodeToolCallPayload(text[m[2]:m[3]], opts)
		if len(blockCalls) == 0 {
			// Leave non-tool-call blocks (e.g. ordinary ```json``` snippets) intact in text
			if opts.requireAllowed {
				continue
			}
			cleanParts = append(cleanParts, text[lastEnd:m[0]])
			lastEnd = m[1]
			continue
		}
		cleanParts = append(cleanParts, text[lastEnd:m[0]])
		lastEnd = m[1]
		toolCalls = append(toolCalls, blockCalls...)
	}

	if len(toolCalls) == 0 && opts.requireAllowed {
		return text, nil
	}

	cleanParts = append(cleanParts, text[lastEnd:])
	clean := strings.TrimSpace(strings.Join(cleanParts, ""))
	return clean, toolCalls
}

// ParseToolCalls extracts tool calls from the raw string output of the model.
// Supports ```tool_call``` blocks (single object, array, or multiple JSON objects)
// as well as <tool_call>...</tool_call> wrappers.
func ParseToolCalls(text string) (string, []models.OpenAIToolCall) {
	return ParseToolCallsWithAllowed(text, nil)
}

func parseToolCallsInternal(text string, allowedTools map[string]bool, deferCfg DeferredToolConfig, deferredNames map[string]bool) (string, []models.OpenAIToolCall) {
	explicitOpts := toolParseOpts{
		requireAllowed: false,
		allowed:        allowedTools,
		deferCfg:       deferCfg,
		deferredNames:  deferredNames,
	}

	clean, calls := extractBlocksWithRegex(text, reToolCall, explicitOpts)
	if len(calls) > 0 {
		return clean, calls
	}

	cleanXML, xmlCalls := extractBlocksWithRegex(text, reXMLToolCall, explicitOpts)
	if len(xmlCalls) > 0 {
		return cleanXML, xmlCalls
	}

	if len(allowedTools) > 0 {
		jsonOpts := explicitOpts
		jsonOpts.requireAllowed = true
		cleanJSON, jsonCalls := extractBlocksWithRegex(text, reJSONToolCall, jsonOpts)
		if len(jsonCalls) > 0 {
			return cleanJSON, jsonCalls
		}
	}

	return text, nil
}

// ParseToolCallsWithAllowed extracts structured tool calls from model output.
// In addition to ```tool_call``` and <tool_call> blocks, if allowedTools is non-empty
// and no explicit tool_call blocks were found, it also accepts ```json``` blocks whose
// "name" matches a declared tool (or deferred mcp__* connector tool when DeferExecuteTool is allowed).
func ParseToolCallsWithAllowed(text string, allowedTools map[string]bool) (string, []models.OpenAIToolCall) {
	deferCfg := DeferredToolConfig{
		DeferExecName: "DeferExecuteTool",
		ToolNameKey:   "toolName",
		ParamsKey:     "params",
	}
	if allowedTools != nil && allowedTools["DeferExecuteTool"] {
		deferCfg.HasDeferExec = true
	}
	return parseToolCallsInternal(text, allowedTools, deferCfg, nil)
}

// ParseToolCallsWithContext extracts structured tool calls using the full declared
// OpenAI tools and conversation messages, enabling dynamic DeferExecuteTool schema
// detection, normalization, and auto-wrapping of both `mcp__*` and ToolSearch-discovered
// deferred connector tools.
func ParseToolCallsWithContext(text string, tools []models.OpenAITool, messages []models.OpenAIMessage) (string, []models.OpenAIToolCall) {
	allowedTools := AllowedToolNames(tools)
	deferCfg := DetectDeferredToolConfig(tools)
	deferredNames := ExtractDeferredToolNames(messages)
	return parseToolCallsInternal(text, allowedTools, deferCfg, deferredNames)
}
