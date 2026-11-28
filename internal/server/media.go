package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

const (
	mediaKindImage = "image"
	mediaKindVideo = "video"

	// Markers the prompt asks the model to reply with when it cannot produce
	// media. Without them "no image" is indistinguishable from "the model
	// described the image in prose", and both would be reported the same way.
	mediaUnavailableImage = "IMAGE GENERATION UNAVAILABLE"
	mediaUnavailableVideo = "VIDEO GENERATION UNAVAILABLE"
)

// mediaRequestBody is the OpenAI images/generations shape plus the few extras
// Gemini's video models accept. Unknown fields are ignored on purpose so any
// client that speaks the OpenAI schema can call these endpoints unchanged.
type mediaRequestBody struct {
	Model            string `json:"model"`
	Prompt           string `json:"prompt"`
	Input            string `json:"input"`
	N                int    `json:"n"`
	Size             string `json:"size"`
	ResponseFormat   string `json:"response_format"`
	AspectRatio      string `json:"aspect_ratio"`
	DurationSeconds  int    `json:"duration_seconds"`
	PersonGeneration string `json:"person_generation"`
}

// handleImageGenerations serves POST /v1/images/generations.
func (app *App) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	app.handleMediaGeneration(w, r, mediaKindImage)
}

// handleVideoGenerations serves POST /v1/videos/generations.
func (app *App) handleVideoGenerations(w http.ResponseWriter, r *http.Request) {
	app.handleMediaGeneration(w, r, mediaKindVideo)
}

func (app *App) handleMediaGeneration(w http.ResponseWriter, r *http.Request, kind string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"message": "could not read request body: " + err.Error(), "type": "invalid_request_error",
		}})
		return
	}
	var req mediaRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"message": "invalid JSON: " + err.Error(), "type": "invalid_request_error",
		}})
		return
	}

	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = strings.TrimSpace(req.Input)
	}
	if prompt == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"message": "prompt is required", "type": "invalid_request_error", "code": "missing_prompt",
		}})
		return
	}

	responseFormat := strings.ToLower(strings.TrimSpace(req.ResponseFormat))
	if responseFormat == "" {
		responseFormat = "url"
	}
	if responseFormat != "url" && responseFormat != "b64_json" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{
			"message": `response_format must be "url" or "b64_json"`, "type": "invalid_request_error",
		}})
		return
	}

	opt, modelLabel := app.mediaModelOptions(req.Model)
	instruction := mediaInstruction(kind, prompt, req)

	started := time.Now()
	text, assets, err := app.runMediaGeneration(instruction, opt)
	if app.Logf != nil {
		app.Logf("MEDIA %s: model=%q assets=%d elapsed=%s", kind, modelLabel, len(assets), time.Since(started).Round(time.Millisecond))
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
			"message": err.Error(), "type": "upstream_error", "code": "generation_failed", "model": modelLabel,
		}})
		return
	}

	assets = gemini.FilterMedia(assets, kind)
	if len(assets) == 0 {
		// Report what the model actually said. Claiming success here would be the
		// same class of lie as reporting a model that never ran.
		message := fmt.Sprintf("the model answered but produced no %s", kind)
		if marker := mediaUnavailableMarker(kind); marker != "" && strings.Contains(strings.ToUpper(text), marker) {
			message = fmt.Sprintf("this model/tenant cannot generate %ss: %s", kind, marker)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
			"code":    "no_media",
			"model":   modelLabel,
			"text":    summarizeMediaText(text),
			"hint": "Gemini web only returns media when the signed-in account/model can generate it; " +
				"the answer text above is what the model replied instead",
		}})
		return
	}

	data := make([]map[string]any, 0, len(assets))
	for _, asset := range assets {
		item := map[string]any{"kind": asset.Kind}
		if asset.MIME != "" {
			item["mime_type"] = asset.MIME
		}
		if asset.URL != "" {
			item["url"] = asset.URL
		}
		b64 := asset.DataB64
		if b64 == "" && responseFormat == "b64_json" {
			// The asset arrived as a link; inline it so the caller gets what it
			// asked for. Failure is not fatal — the URL is still returned.
			if inline, fetchErr := app.fetchMediaAsBase64(asset.URL); fetchErr == nil {
				b64 = inline
			} else if app.Logf != nil {
				app.Logf("MEDIA: could not inline %s: %v", asset.URL, fetchErr)
			}
		}
		if b64 != "" {
			item["b64_json"] = b64
		}
		if asset.URL == "" && b64 != "" {
			item["url"] = "data:" + asset.MIME + ";base64," + b64
		}
		data = append(data, item)
	}

	if req.N > len(data) && app.Logf != nil {
		app.Logf("MEDIA %s: client asked for n=%d, Gemini web returned %d asset(s) in one turn", kind, req.N, len(data))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"created": time.Now().Unix(),
		"model":   modelLabel,
		"kind":    kind,
		"data":    data,
	})
}

// runMediaGeneration is the single seam between the HTTP shape and the upstream
// call, so tests can exercise the endpoint without a tenant.
func (app *App) runMediaGeneration(prompt string, opt gemini.ModelOptions) (string, []gemini.MediaAsset, error) {
	if app.mediaGenerate != nil {
		return app.mediaGenerate(prompt, opt)
	}
	if app.Gem == nil {
		return "", nil, fmt.Errorf("no Gemini client is configured")
	}

	var raw strings.Builder
	opt.MediaSink = func(chunk string) {
		raw.WriteString(chunk)
	}

	text, err := app.Gem.Generate(prompt, opt, nil)
	assets := gemini.MergeMedia(gemini.ExtractMedia(raw.String()), gemini.ExtractMedia(text))
	if err != nil {
		return text, assets, err
	}
	return text, assets, nil
}

// mediaModelOptions maps the requested model onto the registry. Image and video
// model names (nano-banana, veo-3, …) are not chat models, so an unknown name is
// passed through to the upstream model-id resolution with the default model's
// transport settings instead of being rejected.
func (app *App) mediaModelOptions(requested string) (gemini.ModelOptions, string) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = strings.TrimSpace(app.Cfg.DefaultModel)
	}
	if name == "" {
		name = models.DefaultModelName
	}

	resolved, err := models.Resolve(name, app.Cfg.DefaultModel)
	if err == nil && !resolved.Fallback {
		return gemini.ModelOptions{
			ModelName:   resolved.Name,
			ModelID:     resolved.Mode,
			ThinkMode:   resolved.Think,
			ModelHeader: resolved.Header,
			Extra:       resolved.Extra,
		}, resolved.Name
	}

	// Unknown alias (image/video model names are not chat models): the name still
	// travels upstream — Gemini Enterprise maps it, probes it, or refuses it with
	// the tenant's own words — but the request needs a transport mode to be built
	// at all, so the resolved default's settings are reused.
	if err != nil {
		if fallback, fallbackErr := models.Resolve(app.Cfg.DefaultModel, ""); fallbackErr == nil {
			resolved = fallback
		}
	}
	if resolved.Mode == 0 {
		return gemini.ModelOptions{ModelName: name}, name
	}
	if app.Logf != nil {
		app.Logf("MEDIA: %q is not a chat model in the registry — sending it upstream with the default transport settings (mode=%d) so the tenant can accept or reject it", name, resolved.Mode)
	}
	return gemini.ModelOptions{
		ModelName:   name,
		ModelID:     resolved.Mode,
		ThinkMode:   resolved.Think,
		ModelHeader: resolved.Header,
		Extra:       resolved.Extra,
	}, name
}

// mediaInstruction wraps the caller's prompt so Gemini uses its media tool
// instead of describing the result. The availability marker makes "cannot
// generate" a distinguishable outcome rather than a plausible-looking essay.
func mediaInstruction(kind, prompt string, req mediaRequestBody) string {
	var b strings.Builder
	switch kind {
	case mediaKindVideo:
		b.WriteString("Generate a video for this request using your built-in video generation tool.\n\n")
		b.WriteString("Request: ")
		b.WriteString(prompt)
		b.WriteString("\n\n")
		if req.AspectRatio != "" {
			b.WriteString("Aspect ratio: " + req.AspectRatio + "\n")
		}
		if req.DurationSeconds > 0 {
			b.WriteString(fmt.Sprintf("Duration: %d seconds\n", req.DurationSeconds))
		}
		b.WriteString("Render the video itself. Do not answer with a description, a script, a storyboard or an apology. ")
		b.WriteString("If you cannot generate video, reply with exactly " + mediaUnavailableVideo + " and nothing else.")
	default:
		b.WriteString("Generate an image for this request using your built-in image generation tool.\n\n")
		b.WriteString("Request: ")
		b.WriteString(prompt)
		b.WriteString("\n\n")
		if req.Size != "" {
			b.WriteString("Target size: " + req.Size + "\n")
		}
		b.WriteString("Render the image itself. Do not answer with a description, an alternative prompt or an apology. ")
		b.WriteString("If you cannot generate images, reply with exactly " + mediaUnavailableImage + " and nothing else.")
	}
	return b.String()
}

func mediaUnavailableMarker(kind string) string {
	if kind == mediaKindVideo {
		return mediaUnavailableVideo
	}
	return mediaUnavailableImage
}

// fetchMediaAsBase64 downloads an asset link so response_format=b64_json works
// when the transport only produced a URL. Only Google asset hosts are fetched:
// a URL scraped out of model output must never become a server-side request to
// an arbitrary host.
func (app *App) fetchMediaAsBase64(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("unparseable asset URL")
	}
	host := strings.ToLower(parsed.Hostname())
	allowed := false
	for _, suffix := range []string{"googleusercontent.com", "ggpht.com", "googleapis.com", "gstatic.com"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", fmt.Errorf("refusing to fetch non-Google asset host %q", host)
	}

	client := app.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asset host returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("asset body was empty")
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

// summarizeMediaText keeps the model's own words so a "no image" error is
// actionable instead of a dead end.
func summarizeMediaText(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) > 600 {
		trimmed = trimmed[:600] + "…"
	}
	return trimmed
}

// maxInlineChatMediaChars caps a base64 asset inlined into chat text. A chat reply
// is not the right place for a 40 MB blob; the URL form is used when available.
const maxInlineChatMediaChars = 8 << 20

// interceptClientMediaTools removes the client's own image/video generators from
// a chat request. Left in place they would be executed by the client (and billed
// there) instead of being rendered by the signed-in Gemini account.
func interceptClientMediaTools(req models.OpenAIChatRequest) (models.OpenAIChatRequest, []string) {
	kept, removed := format.SplitMediaGenerationTools(req.Tools)
	if len(removed) == 0 {
		return req, nil
	}
	req.Tools = kept

	// A tool_choice naming a removed tool would force a call that no longer
	// exists, so fall back to automatic choice (other tools may remain).
	if name := format.ToolChoiceName(req.ToolChoice); name != "" {
		for _, gone := range removed {
			if strings.EqualFold(name, gone) {
				req.ToolChoice = "auto"
				break
			}
		}
	}
	if len(kept) == 0 {
		if choice, ok := req.ToolChoice.(string); ok && strings.EqualFold(choice, "required") {
			req.ToolChoice = "none"
		}
	}
	return req, removed
}

// chatMediaSuffix turns media found in a captured upstream payload into markdown
// the chat client can render. Empty when the payload carried no media.
//
// It logs the outcome either way: "the model answered with text and no image" is
// the difference between a tenant that cannot render media and a transport that
// dropped it, and that must be visible without guessing.
func (a *App) chatMediaSuffix(raw, model string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	assets := gemini.ExtractMedia(raw)
	if len(assets) == 0 {
		if a.Logf != nil {
			a.Logf("MEDIA: %q answered with text but no image/video in the payload — the tenant/model did not render it", model)
		}
		return ""
	}
	if a.Logf != nil {
		a.Logf("MEDIA: %q answered with %d asset(s)", model, len(assets))
	}
	return mediaChatMarkdown(assets)
}

// mediaChatMarkdown renders assets as markdown images/links. Inline bytes become
// a data: URI (bounded), so the client never has to fetch an authenticated URL.
func mediaChatMarkdown(assets []gemini.MediaAsset) string {
	var b strings.Builder
	for _, asset := range assets {
		src := asset.URL
		if src == "" && asset.DataB64 != "" {
			if len(asset.DataB64) > maxInlineChatMediaChars {
				continue
			}
			src = "data:" + asset.MIME + ";base64," + asset.DataB64
		}
		if src == "" {
			continue
		}
		if asset.Kind == "video" {
			b.WriteString("\n\n[generated video](" + src + ")")
			continue
		}
		b.WriteString("\n\n![generated image](" + src + ")")
	}
	return b.String()
}

// stripGoogleMediaTools removes client-side media generators from a
// Google-shaped request and returns their names. The declaration list is rebuilt
// in place; a tool entry that only held media declarations is dropped entirely.
func stripGoogleMediaTools(req *models.GoogleGenerateRequest) []string {
	if req == nil || len(req.Tools) == 0 {
		return nil
	}
	var removed []string
	keptTools := make([]models.GoogleTool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		decls := tool.FunctionDeclarations
		if len(decls) == 0 {
			keptTools = append(keptTools, tool)
			continue
		}
		keptDecls := make([]models.GoogleFunctionDeclaration, 0, len(decls))
		for _, decl := range decls {
			if format.IsMediaGenerationTool(decl.Name) {
				removed = append(removed, decl.Name)
				continue
			}
			keptDecls = append(keptDecls, decl)
		}
		if len(keptDecls) == 0 {
			continue
		}
		tool.FunctionDeclarations = keptDecls
		keptTools = append(keptTools, tool)
	}
	if len(removed) == 0 {
		return nil
	}
	req.Tools = keptTools
	return removed
}

// warnUndeclaredToolCalls names tool calls the model invented.
//
// An explicit ```tool_call block is passed through even when its name is not in
// the request's tool list (clients may proxy arbitrary tools), so a hallucinated
// name reaches the client as a call it cannot run — the turn looks broken with no
// explanation anywhere. One log line makes that visible.
func (a *App) warnUndeclaredToolCalls(tools []models.OpenAITool, calls []models.OpenAIToolCall) {
	if a == nil || a.Logf == nil || len(calls) == 0 || len(tools) == 0 {
		return
	}
	declared := format.AllowedToolNames(tools)
	for _, call := range calls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" || declared[name] {
			continue
		}
		a.Logf("MODEL TOOL WARNING: the model called %q, which this request did not declare (%d declared: %s) — the client cannot run it; check the tool list or the prompt",
			name, len(declared), strings.Join(sortedKeys(declared, 8), ", "))
	}
}

// sortedKeys returns up to limit map keys, sorted, for a readable log line.
func sortedKeys(set map[string]bool, limit int) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = append(keys[:limit], "…")
	}
	return keys
}
