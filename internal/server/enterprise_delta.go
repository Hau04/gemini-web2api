package server

import (
	"fmt"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func shortEnterpriseSessionID(session string) string {
	s := strings.TrimSpace(session)
	if i := strings.LastIndex(s, "/"); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// prepareDirectEnterpriseTurn prepares prompt + attachments for a direct-mode
// request. On consumer origins it is a pass-through. On Gemini Enterprise
// (Discovery Engine) origins, assistant sessions are stateful on Google's side:
// re-sending the entire 350 KB multi-turn transcript on every turn bloats the
// session quadratically until Discovery Engine rejects it with PROMPT_TOO_LARGE
// and forces slow models (e.g. gemini-4-neon) past the 300s timeout.
//
// When BridgeDeltaContext is enabled and convKey already owns a session whose
// committed message fingerprints match the leading prefix of req.Messages, only
// the newly appended turn(s) are sent, while fullFallback preserves the full
// formatted transcript in case the session was forgotten or full and has to be
// evicted mid-turn.
func (a *App) prepareDirectEnterpriseTurn(
	req models.OpenAIChatRequest,
	convKey string,
	rawPrompt string,
	rawImages []format.Image,
	mediaSuffix string,
) (prompt string, images []format.Image, fullFallback string, deltaBaseSession string, commit func(usedSession string)) {
	if a.Gem == nil || !a.Gem.UsesDiscoveryEngine() {
		return rawPrompt + mediaSuffix, rawImages, "", "", nil
	}

	fingerprints := ConversationFingerprints(req)
	currentToolNames := format.AllowedToolNames(req.Tools)
	commit = func(usedSession string) {
		if a.Cfg.BridgeDeltaContext && strings.TrimSpace(usedSession) != "" {
			a.Gem.RecordEnterpriseSessionDelta(usedSession, fingerprints, currentToolNames)
		}
	}

	if a.Cfg.BridgeDeltaContext && convKey != "" && len(req.Messages) > 1 {
		if owned := a.Gem.OwnedEnterpriseSession(convKey); owned != "" {
			matchedPrefix, addedToolNames := a.Gem.MatchEnterpriseSessionDelta(owned, fingerprints, currentToolNames)
			if matchedPrefix > 1 && matchedPrefix-1 < len(req.Messages) {
				matchedMsgs := matchedPrefix - 1
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

					lastMsg := req.Messages[len(req.Messages)-1]
					lastRole := lastMsg.Role
					if len(req.Tools) > 0 && (lastRole == "user" || lastRole == "") {
						if reminder := format.BuildDeltaToolReminder(req.Tools, req.ToolChoice); reminder != "" {
							deltaPrompt = deltaPrompt + "\n\n" + reminder
						}
					} else if len(req.Tools) > 0 && lastRole == "tool" {
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

					deltaPrompt, deltaAtts = a.attachEnterpriseContext(req.Messages[matchedMsgs:], deltaPrompt, deltaAtts)
					deltaPrompt += mediaSuffix
					fallback := rawPrompt + mediaSuffix
					a.Logf("Enterprise delta context active for session %s: sending %d/%d new message(s) (%d chars vs %d full, +%d added tools)",
						shortEnterpriseSessionID(owned), len(req.Messages)-matchedMsgs, len(req.Messages), len(deltaPrompt), len(fallback), len(addedToolNames))
					return deltaPrompt, deltaAtts, fallback, owned, commit
				}
			}
		}
	}

	fullPrompt, fullImages := a.attachEnterpriseContext(req.Messages, rawPrompt, rawImages)
	fullPrompt += mediaSuffix
	return fullPrompt, fullImages, "", "", commit
}
