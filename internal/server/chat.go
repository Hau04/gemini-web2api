package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/bridge"
	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/gemini"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

const toolCallFence = "```tool_call"

var toolCallFences = []string{
	"```tool_call",
	"```function_call",
	"<tool_call>",
	"```json",
}

// partialToolFenceSuffixLen returns the length of the longest suffix of s
// that is a proper prefix of any supported tool-call opening marker
// ("```tool_call", "<tool_call>", "```json"), so streaming holds only a few bytes
// while confirming whether characters begin a structured tool-call block.
func partialToolFenceSuffixLen(s string) int {
	best := 0
	for _, fence := range toolCallFences {
		maxCheck := len(fence) - 1
		if len(s) < maxCheck {
			maxCheck = len(s)
		}
		for k := maxCheck; k > best; k-- {
			if strings.HasSuffix(s, fence[:k]) {
				best = k
				break
			}
		}
	}
	return best
}

func findToolFenceStart(s string) int {
	earliest := -1
	for _, fence := range toolCallFences {
		if idx := strings.Index(s, fence); idx != -1 {
			if earliest == -1 || idx < earliest {
				earliest = idx
			}
		}
	}
	return earliest
}

func indexToolCalls(toolCalls []models.OpenAIToolCall) []models.OpenAIToolCall {
	indexed := make([]models.OpenAIToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		idx := i
		tc.Index = &idx
		indexed[i] = tc
	}
	return indexed
}

func writeBridgeRateLimitError(w http.ResponseWriter, retryAfter time.Duration, msg string) {
	secs := int(retryAfter.Round(time.Second).Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "rate_limit_error",
			"code":    "rate_limit_exceeded",
		},
	})
}

func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil || len(bodyBytes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	var req models.OpenAIChatRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "invalid JSON"}})
		return
	}

	modelStr := req.Model
	if modelStr == "" {
		modelStr = a.Cfg.DefaultModel
	}

	if req.ToolChoice == nil {
		req.ToolChoice = "auto"
	}
	hasTools := len(req.Tools) > 0
	if choice, ok := req.ToolChoice.(string); ok && choice == "none" {
		hasTools = false
	}

	resolved, redirected, err := a.resolveRequestModel(modelStr, hasTools)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": err.Error()}})
		return
	}
	setModelHeaders(w, resolved)
	a.reportModelSelection(w, modelStr, resolved)
	if redirected {
		w.Header().Set("X-Tool-Model-Override", "true")
	}
	if a.applyToolPolicy(&req, resolved) {
		w.Header().Set("X-Deep-Research", "disabled")
	}

	// The client's own image/video generators (WorkBuddy ships several) render the
	// media on the client and spend its credits. Remove them so Gemini's own media
	// tool does the work instead, and tell the model that in the prompt.
	req, clientMediaTools := interceptClientMediaTools(req)
	if len(clientMediaTools) > 0 {
		w.Header().Set("X-Media-Tools-Intercepted", strings.Join(clientMediaTools, ","))
		a.Logf("Client offered its own media tool(s) %v — removed from this request so Gemini renders the media itself (no client credits spent)", clientMediaTools)
	}

	prompt, images, err := format.MessagesToPrompt(req)
	if err == nil {
		// Gemini Enterprise: attach referenced files / spill oversized prompt text
		// so the replayed request stays small instead of re-sending file content
		// every turn.
		prompt, images = a.attachEnterpriseContext(req.Messages, prompt, images)
	}
	if err != nil || strings.TrimSpace(prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"message": "empty prompt"}})
		return
	}
	if len(clientMediaTools) > 0 {
		prompt += format.MediaGenerationInstruction(clientMediaTools)
	}

	gemOpt := gemini.ModelOptions{
		ModelName:   resolved.Name,
		ModelID:     resolved.Mode,
		ThinkMode:   resolved.Think,
		ModelHeader: resolved.Header,
		Extra:       resolved.Extra,
	}

	// Media only exists in the raw payload, and only a request that asked for
	// media can produce it — so capture nothing for ordinary chats.
	var mediaRaw strings.Builder
	if len(clientMediaTools) > 0 {
		gemOpt.MediaSink = func(chunk string) {
			if mediaRaw.Len() < maxInlineChatMediaChars+1 {
				mediaRaw.WriteString(chunk)
			}
		}
	}

	cid := fmt.Sprintf("chatcmpl-%s", format.RandHex(12))

	strChoice, isStr := req.ToolChoice.(string)
	isToolNone := isStr && strChoice == "none"

	// Intercept automatic client title / topic generation requests (e.g. WorkBuddy)
	// and fulfill them instantly without tying up the browser tab or polluting chat history.
	if isTitle, sessionText := isTitleSummaryRequest(prompt); isTitle {
		titleJSON := generateFastTitleResponse(prompt, sessionText)
		a.Logf("Fast-tracking client title generation request locally: %s", titleJSON)

		if req.Stream {
			if !startSSE(w) {
				return
			}
			firstChunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index: 0,
						Delta: &models.OpenAIDelta{
							Role: "assistant",
						},
						FinishReason: nil,
					},
				},
			}
			_ = writeSSEData(w, firstChunk)

			contentChunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index: 0,
						Delta: &models.OpenAIDelta{
							Content: titleJSON,
						},
						FinishReason: nil,
					},
				},
			}
			_ = writeSSEData(w, contentChunk)

			stopReason := "stop"
			endChunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index:        0,
						Delta:        &models.OpenAIDelta{},
						FinishReason: &stopReason,
					},
				},
			}
			_ = writeSSEData(w, endChunk)
			_ = writeSSEDone(w)
			return
		}

		finish := "stop"
		msg := models.OpenAIMessage{
			Role:    "assistant",
			Content: titleJSON,
		}
		resp := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Message:      &msg,
					FinishReason: &finish,
				},
			},
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Route through direct cookie mode whenever session cookies are available
	// (synced by the browser extension). A frozen tab (Chrome Memory Saver)
	// never counts as connected, and a degraded bridge is skipped while cookies
	// are available — we prefer cookies over the live browser tab.
	//
	// The bridge is kept only for oversized prompts that would trigger .txt
	// spillover: prepareBridgeRequest → SpilloverOversizedPrompt uploads the
	// oversized prompt as a .txt attachment through the browser's file composer,
	// avoiding any Enterprise batchexecute body size limits for fully inlined
	// prompts. Normal-sized prompts go through direct cookie mode (prompt inlined).
	bridgeAlive := a.Bridge != nil && a.Bridge.IsConnected()

	// A model direct mode cannot name must go through the bridge: its picker is
	// the only transport that can select it (Enterprise tenant model ids are not
	// always known). Everything else prefers direct mode as before.
	modelNeedsBridge := bridgeAlive && !a.directCanHonourModel(resolved)
	switch {
	case modelNeedsBridge:
		a.Logf("Keeping the browser bridge: direct mode has no tenant model id for %q, so only the tab's model picker can select it (map discovery_engine_models to make direct mode honour it)", resolved.Name)
		w.Header().Set("X-Model-Route", "bridge-picker")
	case a.cookiesReady() && !a.keepBridgeForSpillover(prompt, bridgeAlive):
		bridgeAlive = false
	}
	if !bridgeAlive {
		a.warnModelDropped(resolved)
	}

	if bridgeAlive && a.Bridge.IsDegraded() && a.directModeUsable() && !modelNeedsBridge {
		a.Logf("Browser bridge degraded after recent timeout — routing via direct cookie mode for %ds", int(a.Bridge.DegradedRemaining().Seconds()))
		bridgeAlive = false
	}

	if bridgeAlive {
		if a.Bridge.IsPaused() {
			pErr := &bridge.PausedError{}
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": map[string]any{
					"message": pErr.Error(),
					"type":    "bridge_paused",
				},
			})
			return
		}
		if rem := a.Bridge.CooldownRemaining(); rem > 0 {
			rlErr := &bridge.RateLimitError{RetryAfter: rem}
			a.Logf("Bridge rate-limit circuit breaker active: %v", rlErr)
			writeBridgeRateLimitError(w, rem, rlErr.Error())
			return
		}

		a.Logf("Routing chat completion through active browser bridge (%s)", a.Bridge.TabName())
		prepared := a.prepareBridgeRequest(r, req, prompt, images, resolved.ChatMode)
		bridgeOpts := bridge.GenerateOptions{
			Prompt:      prepared.Prompt,
			Model:       resolved.Name,
			Attachments: prepared.Attachments,
			NewChat:     prepared.NewChat,
			LoopPenalty: prepared.LoopPenalty,
		}

		if req.Stream {
			// SSE is committed (status 200 + role chunk + keepalives) only once a
			// browser tab actually claims the queued request (onPickup below). Until
			// then nothing has been written, so a dead tab (pickup timeout) can be
			// answered with a real HTTP error instead of a 200 stream carrying a
			// hidden error chunk that clients surface as an empty/stuck completion.
			var streamStarted bool
			var stopKeepalive chan struct{}
			var doneKeepalive chan struct{}

			var sseMu sync.Mutex
			safeWriteSSE := func(data any) error {
				sseMu.Lock()
				defer sseMu.Unlock()
				return writeSSEData(w, data)
			}
			safeWriteDone := func() error {
				sseMu.Lock()
				defer sseMu.Unlock()
				return writeSSEDone(w)
			}

			onPickup := func() error {
				if streamStarted {
					return nil
				}
				if !startSSE(w) {
					return errors.New("client disconnected before bridge stream start")
				}
				streamStarted = true

				// Emit first chunk with role to match OpenAI streaming protocol.
				firstChunk := models.OpenAIChatResponse{
					ID:      cid,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   resolved.Name,
					Choices: []models.OpenAIChoice{
						{
							Index: 0,
							Delta: &models.OpenAIDelta{
								Role: "assistant",
							},
							FinishReason: nil,
						},
					},
				}
				_ = safeWriteSSE(firstChunk)

				// Keepalive ticker flushes empty delta chunks ({}) every 5s with mutex synchronization
				// to keep client stream parsers active and prevent idle timeouts.
				stopKeepalive = make(chan struct{})
				doneKeepalive = make(chan struct{})
				keepaliveTicker := time.NewTicker(5 * time.Second)
				go func() {
					defer close(doneKeepalive)
					defer keepaliveTicker.Stop()
					for {
						select {
						case <-keepaliveTicker.C:
							pingChunk := models.OpenAIChatResponse{
								ID:      cid,
								Object:  "chat.completion.chunk",
								Created: time.Now().Unix(),
								Model:   resolved.Name,
								Choices: []models.OpenAIChoice{
									{
										Index: 0,
										Delta: &models.OpenAIDelta{},
									},
								},
							}
							_ = safeWriteSSE(pingChunk)
						case <-stopKeepalive:
							return
						case <-r.Context().Done():
							return
						}
					}
				}()
				return nil
			}

			var fullText strings.Builder
			var bufferedTail strings.Builder
			hasTools := len(req.Tools) > 0 && !isToolNone
			isBufferingToolCall := false
			pending := ""
			streamedAny := false
			servedByDirect := false

			emitContent := func(text string) error {
				if text == "" {
					return nil
				}
				streamedAny = true
				chunk := models.OpenAIChatResponse{
					ID:      cid,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   resolved.Name,
					Choices: []models.OpenAIChoice{
						{
							Index: 0,
							Delta: &models.OpenAIDelta{
								Content: text,
							},
							FinishReason: nil,
						},
					},
				}
				return safeWriteSSE(chunk)
			}

			emitDelta := func(delta string) error {
				fullText.WriteString(delta)

				if !hasTools {
					return emitContent(delta)
				}

				if isBufferingToolCall {
					bufferedTail.WriteString(delta)
					return nil
				}

				pending += delta

				// Detect start of a ```tool_call, <tool_call>, or ```json block; flush any leading prose
				if idx := findToolFenceStart(pending); idx != -1 {
					isBufferingToolCall = true
					bufferedTail.WriteString(pending[idx:])
					if idx > 0 {
						leading := pending[:idx]
						pending = ""
						return emitContent(leading)
					}
					pending = ""
					return nil
				}

				// Hold only a tiny trailing suffix if it could be the opening of a tool fence
				hold := partialToolFenceSuffixLen(pending)
				if len(pending) > hold {
					toEmit := pending[:len(pending)-hold]
					pending = pending[len(pending)-hold:]
					return emitContent(toEmit)
				}
				return nil
			}

			emitErr := a.Bridge.GenerateStreamWithPickupHook(r.Context(), bridgeOpts, onPickup, emitDelta)
			if emitErr != nil && !streamedAny && fullText.Len() == 0 && a.directModeUsable() && r.Context().Err() == nil {
				// The bridge produced nothing before failing (e.g. the tab froze or was
				// discarded mid-turn). Fall back to the direct cookie mode and mark the
				// bridge degraded so subsequent requests skip it until the tab recovers.
				if !streamStarted {
					_ = onPickup()
				}
				a.Logf("Browser bridge failed before streaming (%v) — retrying via direct cookie mode", emitErr)
				a.Bridge.MarkDegraded(10 * time.Minute)
				fullText.Reset()
				var fileRefs []string
				if len(images) > 0 {
					var uErr error
					fileRefs, gemOpt.Files, uErr = a.prepareAttachments(images)
					if uErr != nil {
						emitErr = uErr
					}
				}
				if emitErr == nil {
					emitErr = a.Gem.GenerateStream(prompt, gemOpt, fileRefs, emitDelta)
					if emitErr == nil {
						servedByDirect = true
					}
				}
			}

			if emitErr == nil && !streamStarted {
				// Zero-content turn whose claim raced teardown — commit the stream now
				// so the success chunks below have valid SSE framing.
				_ = onPickup()
			}
			if streamStarted {
				close(stopKeepalive)
				<-doneKeepalive
			}

			if emitErr == nil {
				if strings.TrimSpace(fullText.String()) != "" {
					if servedByDirect {
						a.Bridge.ClearDegraded()
					} else {
						a.recordBridgeTurnSuccess(prepared)
					}
				}
				if hasTools {
					if isBufferingToolCall {
						_, toolCalls := format.ParseToolCallsWithContext(fullText.String(), req.Tools, req.Messages)
						if len(toolCalls) > 0 {
							tcChunk := models.OpenAIChatResponse{
								ID:      cid,
								Object:  "chat.completion.chunk",
								Created: time.Now().Unix(),
								Model:   resolved.Name,
								Choices: []models.OpenAIChoice{
									{
										Index: 0,
										Delta: &models.OpenAIDelta{
											ToolCalls: indexToolCalls(toolCalls),
										},
										FinishReason: nil,
									},
								},
							}
							_ = safeWriteSSE(tcChunk)

							stopReason := "tool_calls"
							endChunk := models.OpenAIChatResponse{
								ID:      cid,
								Object:  "chat.completion.chunk",
								Created: time.Now().Unix(),
								Model:   resolved.Name,
								Choices: []models.OpenAIChoice{
									{
										Index:        0,
										Delta:        &models.OpenAIDelta{},
										FinishReason: &stopReason,
									},
								},
							}
							_ = safeWriteSSE(endChunk)
						} else {
							// Fence did not contain a valid declared tool call (e.g. normal ```json``` snippet);
							// flush the exact buffered remainder verbatim so nothing is dropped or duplicated.
							if tail := bufferedTail.String(); tail != "" {
								_ = emitContent(tail)
							}
							stopReason := "stop"
							endChunk := models.OpenAIChatResponse{
								ID:      cid,
								Object:  "chat.completion.chunk",
								Created: time.Now().Unix(),
								Model:   resolved.Name,
								Choices: []models.OpenAIChoice{
									{
										Index:        0,
										Delta:        &models.OpenAIDelta{},
										FinishReason: &stopReason,
									},
								},
							}
							_ = safeWriteSSE(endChunk)
						}
					} else {
						// Flush any held trailing characters (e.g. a normal markdown closing fence)
						if pending != "" {
							_ = emitContent(pending)
							pending = ""
						}
						stopReason := "stop"
						endChunk := models.OpenAIChatResponse{
							ID:      cid,
							Object:  "chat.completion.chunk",
							Created: time.Now().Unix(),
							Model:   resolved.Name,
							Choices: []models.OpenAIChoice{
								{
									Index:        0,
									Delta:        &models.OpenAIDelta{},
									FinishReason: &stopReason,
								},
							},
						}
						_ = safeWriteSSE(endChunk)
					}
				} else {
					stopReason := "stop"
					endChunk := models.OpenAIChatResponse{
						ID:      cid,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   resolved.Name,
						Choices: []models.OpenAIChoice{
							{
								Index:        0,
								Delta:        &models.OpenAIDelta{},
								FinishReason: &stopReason,
							},
						},
					}
					_ = safeWriteSSE(endChunk)
				}
				_ = safeWriteDone()
			} else {
				if !streamStarted {
					// Nothing has been written — answer with a real HTTP status the
					// client can surface instead of a 200 stream with hidden error.
					a.Logf("Bridge request failed before stream start: %v", emitErr)
					var pErr *bridge.PausedError
					var rlErr *bridge.RateLimitError
					var pkErr *bridge.PickupError
					switch {
					case errors.As(emitErr, &pErr):
						writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": pErr.Error(), "type": "bridge_paused"}})
					case errors.As(emitErr, &rlErr):
						writeBridgeRateLimitError(w, rlErr.RetryAfter, rlErr.Error())
					case errors.As(emitErr, &pkErr):
						writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": fmt.Sprintf("%v", emitErr), "type": "bridge_offline"}})
					default:
						writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("bridge error: %v", emitErr)}})
					}
					return
				}
				a.Logf("Bridge chat stream error: %v", emitErr)
				if r.Context().Err() == nil {
					if !streamedAny {
						_ = emitContent(fmt.Sprintf("[Bridge Error] %v", emitErr))
					}
					stopReason := "stop"
					endChunk := models.OpenAIChatResponse{
						ID:      cid,
						Object:  "chat.completion.chunk",
						Created: time.Now().Unix(),
						Model:   resolved.Name,
						Choices: []models.OpenAIChoice{
							{
								Index:        0,
								Delta:        &models.OpenAIDelta{},
								FinishReason: &stopReason,
							},
						},
					}
					_ = safeWriteSSE(endChunk)
					_ = safeWriteDone()
				}
			}
			return
		}

		text, err := a.Bridge.GenerateWithOptions(r.Context(), bridgeOpts)
		if err != nil {
			var pErr *bridge.PausedError
			if errors.As(err, &pErr) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": pErr.Error(), "type": "bridge_paused"}})
				return
			}
			var rlErr *bridge.RateLimitError
			if errors.As(err, &rlErr) {
				writeBridgeRateLimitError(w, rlErr.RetryAfter, rlErr.Error())
				return
			}
			if a.directModeUsable() && r.Context().Err() == nil {
				// The bridge produced nothing before failing (e.g. the tab froze or
				// was discarded mid-turn). Fall back to the direct cookie mode and
				// mark the bridge degraded so subsequent requests skip it.
				a.Logf("Browser bridge failed (%v) — retrying via direct cookie mode", err)
				a.Bridge.MarkDegraded(10 * time.Minute)
				bridgeErr := err
				var fileRefs []string
				if len(images) > 0 {
					var uErr error
					fileRefs, gemOpt.Files, uErr = a.prepareAttachments(images)
					if uErr != nil {
						writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("bridge error: %v (direct fallback: %v)", bridgeErr, uErr)}})
						return
					}
				}
				text, err = a.Gem.Generate(prompt, gemOpt, fileRefs)
				if err != nil {
					writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("bridge error: %v (direct fallback: %v)", bridgeErr, err)}})
					return
				}
				text += a.chatMediaSuffix(mediaRaw.String(), resolved.Name)
				a.Bridge.ClearDegraded()
			} else {
				var pkErr *bridge.PickupError
				if errors.As(err, &pkErr) {
					writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": fmt.Sprintf("%v", err), "type": "bridge_offline"}})
				} else {
					writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("bridge error: %v", err)}})
				}
				return
			}
		}
		if strings.TrimSpace(text) != "" {
			a.recordBridgeTurnSuccess(prepared)
		}

		var toolCalls []models.OpenAIToolCall
		if len(req.Tools) > 0 && text != "" && !isToolNone {
			text, toolCalls = format.ParseToolCallsWithContext(text, req.Tools, req.Messages)
		}

		msg := models.OpenAIMessage{
			Role:    "assistant",
			Content: text,
		}
		if text == "" {
			msg.Content = nil
		}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}

		finish := "stop"
		if len(toolCalls) > 0 {
			finish = "tool_calls"
		}

		resp := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Message:      &msg,
					FinishReason: &finish,
				},
			},
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// The bridge is not serving this request. Probe Bard-RPC support (60s failure
	// backoff) and fail fast when direct cookie mode cannot work — no session
	// cookies, or a Gemini Enterprise origin without the Bard RPC — with an
	// actionable error instead of burning a doomed upstream attempt.
	if a.cookiesReady() {
		a.Gem.EnsureModelsDiscovered()
	}
	if !a.directModeUsable() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{
			"message": "direct cookie mode is unavailable on this Gemini Enterprise origin: no Discovery Engine streamAssist endpoint configured and no request template captured — set discovery_engine_endpoint in config.json (your streamAssist URL from Google Cloud Console), or send one prompt through the open Gemini tab so the extension can capture a streamAssist template, then close the tab",
			"type":    "bridge_offline",
		}})
		return
	}

	// Direct Gemini API mode: consumer origins upload through content-push,
	// Gemini Enterprise attaches the files to the assistant session instead.
	var fileRefs []string
	if len(images) > 0 {
		fileRefs, gemOpt.Files, err = a.prepareAttachments(images)
		if err != nil {
			a.Logf("Image upload error: %v", err)
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
			return
		}
	}

	if req.Stream && (len(req.Tools) == 0 || isToolNone) {
		if !startSSE(w) {
			return
		}

		// Emit first chunk with role to match OpenAI streaming protocol.
		firstChunk := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index: 0,
					Delta: &models.OpenAIDelta{
						Role: "assistant",
					},
					FinishReason: nil,
				},
			},
		}
		_ = writeSSEData(w, firstChunk)

		emitErr := a.Gem.GenerateStream(prompt, gemOpt, fileRefs, func(delta string) error {
			chunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index: 0,
						Delta: &models.OpenAIDelta{
							Content: delta,
						},
						FinishReason: nil,
					},
				},
			}
			return writeSSEData(w, chunk)
		})

		if emitErr == nil {
			// Media cannot be streamed as text deltas, so it arrives as one final
			// content chunk right before the stop chunk.
			if suffix := a.chatMediaSuffix(mediaRaw.String(), resolved.Name); suffix != "" {
				mediaChunk := models.OpenAIChatResponse{
					ID:      cid,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   resolved.Name,
					Choices: []models.OpenAIChoice{
						{
							Index: 0,
							Delta: &models.OpenAIDelta{
								Content: suffix,
							},
							FinishReason: nil,
						},
					},
				}
				_ = writeSSEData(w, mediaChunk)
			}
			stopReason := "stop"
			endChunk := models.OpenAIChatResponse{
				ID:      cid,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   resolved.Name,
				Choices: []models.OpenAIChoice{
					{
						Index:        0,
						Delta:        &models.OpenAIDelta{},
						FinishReason: &stopReason,
					},
				},
			}
			_ = writeSSEData(w, endChunk)
			_ = writeSSEDone(w)
		} else {
			a.Logf("Chat stream error: %v", emitErr)
		}
		return
	}

	text, err := a.Gem.Generate(prompt, gemOpt, fileRefs)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream error: %v", err)}})
		return
	}
	text += a.chatMediaSuffix(mediaRaw.String(), resolved.Name)

	var toolCalls []models.OpenAIToolCall
	if len(req.Tools) > 0 && text != "" && !isToolNone {
		text, toolCalls = format.ParseToolCallsWithContext(text, req.Tools, req.Messages)
	}

	msg := models.OpenAIMessage{
		Role:    "assistant",
		Content: text,
	}
	if text == "" {
		msg.Content = nil
	}

	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}

	if req.Stream {
		if !startSSE(w) {
			return
		}
		delta := &models.OpenAIDelta{
			Role: "assistant",
		}
		if text != "" {
			delta.Content = text
		}
		if len(toolCalls) > 0 {
			delta.ToolCalls = indexToolCalls(toolCalls)
		}
		chunk := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Delta:        delta,
					FinishReason: nil,
				},
			},
		}
		_ = writeSSEData(w, chunk)
		endChunk := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Delta:        &models.OpenAIDelta{},
					FinishReason: &finish,
				},
			},
		}
		_ = writeSSEData(w, endChunk)
		_ = writeSSEDone(w)
	} else {
		promptTokens := len(prompt) / 4
		completionTokens := len(text) / 4
		resp := models.OpenAIChatResponse{
			ID:      cid,
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   resolved.Name,
			Choices: []models.OpenAIChoice{
				{
					Index:        0,
					Message:      &msg,
					FinishReason: &finish,
				},
			},
			Usage: &models.OpenAIUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			},
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// keepBridgeForSpillover decides whether an oversized prompt should stay on the
// browser bridge so its text can be uploaded as a .txt attachment. That trick
// exists to dodge consumer Gemini Web composer/body limits; on a Discovery Engine
// (Gemini Enterprise) origin the prompt is inlined into the widgetStreamAssist
// JSON body, so a live tab is only worth waiting for when direct mode cannot
// serve the request at all (no template, no endpoint, no widget csesidx).
func (a *App) keepBridgeForSpillover(prompt string, bridgeAlive bool) bool {
	if !bridgeAlive || a.Cfg.BridgeSpilloverChars <= 0 || len(prompt) <= a.Cfg.BridgeSpilloverChars {
		return false
	}
	if a.Gem != nil && a.Gem.UsesDiscoveryEngine() && a.directModeUsable() {
		a.Logf("Oversized prompt on a Gemini Enterprise origin — routing direct (inlined in the widget request) instead of the browser .txt spillover")
		return false
	}
	return true
}
