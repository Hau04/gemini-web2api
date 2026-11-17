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

// FileFromPart parses OpenAI-style file/document content parts (`file`, `input_file`, `document`)
// into an Image/Attachment struct so PDFs and other documents can be uploaded directly via the bridge.
func FileFromPart(mapItem map[string]any) (Image, bool) {
	iType, _ := mapItem["type"].(string)

	switch iType {
	case "file":
		fileObj, ok := mapItem["file"].(map[string]any)
		if !ok {
			fileObj = mapItem
		}
		filename, _ := fileObj["filename"].(string)
		mimeHint := partMime(fileObj)
		if mimeHint == "" {
			mimeHint = partMime(mapItem)
		}
		if fileData, ok := fileObj["file_data"].(string); ok && fileData != "" {
			return decodeFilePayload(fileData, filename, mimeHint)
		}
		if fileURL, ok := fileObj["file_url"].(string); ok && fileURL != "" {
			img, ok := imageFromURLString(fileURL, mimeHint)
			if ok {
				img.Filename = filename
				return img, true
			}
		}
	case "input_file":
		filename, _ := mapItem["filename"].(string)
		mimeHint := partMime(mapItem)
		if fileData, ok := mapItem["file_data"].(string); ok && fileData != "" {
			return decodeFilePayload(fileData, filename, mimeHint)
		}
		if fileURL, ok := mapItem["file_url"].(string); ok && fileURL != "" {
			img, ok := imageFromURLString(fileURL, mimeHint)
			if ok {
				img.Filename = filename
				return img, true
			}
		}
	case "document":
		filename, _ := mapItem["filename"].(string)
		if filename == "" {
			filename, _ = mapItem["title"].(string)
		}
		if source, ok := mapItem["source"].(map[string]any); ok {
			mimeHint := partMime(source)
			if dataStr, ok := source["data"].(string); ok && dataStr != "" {
				return decodeFilePayload(dataStr, filename, mimeHint)
			}
			if urlStr, ok := source["url"].(string); ok && urlStr != "" {
				img, ok := imageFromURLString(urlStr, mimeHint)
				if ok {
					img.Filename = filename
					return img, true
				}
			}
		}
	}

	return Image{}, false
}

func decodeFilePayload(payload, filename, mimeHint string) (Image, bool) {
	if strings.HasPrefix(payload, "data:") {
		data, decMime, err := DecodeDataURL(payload)
		if err != nil {
			return Image{}, false
		}
		if mimeHint == "" {
			mimeHint = decMime
		}
		return Image{Data: data, MIME: mimeHint, Filename: filename}, true
	}
	decoded, err := multimodal.DecodeBase64Raw(payload)
	if err != nil {
		return Image{}, false
	}
	if mimeHint == "" {
		if len(decoded) >= 4 && string(decoded[:4]) == "%PDF" {
			mimeHint = "application/pdf"
		} else {
			mimeHint = "application/octet-stream"
		}
	}
	return Image{Data: decoded, MIME: mimeHint, Filename: filename}, true
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
	return fmt.Sprintf(
		"[Local client bridge active: to use local tools or connected WorkBuddy apps/connectors (GitHub, Gmail, files, shell, etc.), output a ```tool_call\n{\"name\": \"...\", \"arguments\": {...}}\n``` block — the user's local app executes it automatically and returns the result. Available tools: %s]",
		list,
	)
}

func MessagesToPrompt(req models.OpenAIChatRequest) (string, []Image, error) {
	var parts []string
	var images []Image

	strChoice, isStr := req.ToolChoice.(string)
	if (!isStr || strChoice != "none") && len(req.Tools) > 0 {
		var toolDefs []models.OpenAIFunction
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

			toolDefs = append(toolDefs, fn)
		}

		if len(toolDefs) > 0 {
			constraint := BuildToolChoiceInstruction(req.ToolChoice)
			defsJSON := compactToolDefinitions(toolDefs)
			parts = append(parts, fmt.Sprintf(
				"# Tool Use\n\n"+
					"An external local client bridge (WorkBuddy) is connected to this chat. Whenever you output a ```tool_call``` block, the client automatically runs that local tool or connected app connector (e.g. GitHub, Gmail, local files, shell) on the user's behalf and returns the output in the next turn as `[Tool result for <name>]`.\n"+
					"- You DO have execution access to ALL tools & connectors listed below through this bridge. NEVER refuse by saying you are in a cloud environment or cannot access local files, shell, GitHub, email, or external apps — call the matching declared tools below instead.\n"+
					"- Call format (strict JSON object with \"name\" and \"arguments\"):\n"+
					"```tool_call\n{\"name\": \"func_name\", \"arguments\": {...}}\n```\n"+
					"- Output ONLY the tool_call block(s) when calling tools. Batch independent calls in one response.\n"+
					"- If a file or image is already attached directly to this message, read it natively instead of calling local tools to re-read it.\n\n"+
					"Available tools:\n%s%s",
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

func decodeToolCallPayload(content string, requireAllowed bool, allowed map[string]bool) []models.OpenAIToolCall {
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
					if requireAllowed {
						return nil
					}
					continue
				}
				if requireAllowed && !allowed[name] {
					return nil
				}
				argsVal := data.resolvedArgs()
				var argsStr string
				if str, ok := argsVal.(string); ok {
					argsStr = str
				} else if argsVal != nil {
					b, _ := json.Marshal(argsVal)
					argsStr = string(b)
				} else {
					argsStr = "{}"
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

func extractBlocksWithRegex(text string, re *regexp.Regexp, requireAllowed bool, allowed map[string]bool) (string, []models.OpenAIToolCall) {
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}

	var toolCalls []models.OpenAIToolCall
	var cleanParts []string
	lastEnd := 0

	for _, m := range matches {
		blockCalls := decodeToolCallPayload(text[m[2]:m[3]], requireAllowed, allowed)
		if len(blockCalls) == 0 {
			// Leave non-tool-call blocks (e.g. ordinary ```json``` snippets) intact in text
			if requireAllowed {
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

	if len(toolCalls) == 0 && requireAllowed {
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

// ParseToolCallsWithAllowed extracts structured tool calls from model output.
// In addition to ```tool_call``` and <tool_call> blocks, if allowedTools is non-empty
// and no explicit tool_call blocks were found, it also accepts ```json``` blocks whose
// "name" matches a declared tool in allowedTools.
func ParseToolCallsWithAllowed(text string, allowedTools map[string]bool) (string, []models.OpenAIToolCall) {
	clean, calls := extractBlocksWithRegex(text, reToolCall, false, nil)
	if len(calls) > 0 {
		return clean, calls
	}

	cleanXML, xmlCalls := extractBlocksWithRegex(text, reXMLToolCall, false, nil)
	if len(xmlCalls) > 0 {
		return cleanXML, xmlCalls
	}

	if len(allowedTools) > 0 {
		cleanJSON, jsonCalls := extractBlocksWithRegex(text, reJSONToolCall, true, allowedTools)
		if len(jsonCalls) > 0 {
			return cleanJSON, jsonCalls
		}
	}

	return text, nil
}
