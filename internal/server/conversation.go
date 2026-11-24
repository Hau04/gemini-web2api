package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ikhsan3adi/gemini-web2api/internal/format"
	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

const (
	responseConversationTTL     = 24 * time.Hour
	maxResponseConversationIDs  = 4096
	maxClientConversationIDSize = 512
)

type responseConversationEntry struct {
	clientConversationID string
	createdAt            time.Time
}

var conversationIDFields = []string{
	"conversation_id", "conversationId",
	"chat_id", "chatId",
	"thread_id", "threadId",
	"session_id", "sessionId",
}

// clientConversationIDFromPayload reads common client conversation-id fields
// and headers. Responses API chains use previous_response_id when no explicit
// conversation id is supplied.
func (a *App) clientConversationIDFromPayload(r *http.Request, payload map[string]any) string {
	if id := conversationIDFromPayload(payload); id != "" {
		return "client:" + id
	}
	if r != nil {
		for _, name := range []string{
			"OpenAI-Conversation-ID",
			"X-Conversation-ID",
			"X-Chat-ID",
			"X-Thread-ID",
			"X-Session-ID",
			"X-Chat-Session-ID",
		} {
			if id := cleanClientConversationID(r.Header.Get(name)); id != "" {
				return "client:" + id
			}
		}
	}

	previousResponseID, _ := payload["previous_response_id"].(string)
	previousResponseID = cleanClientConversationID(previousResponseID)
	if previousResponseID == "" {
		return ""
	}
	if id := a.responseConversationForID(previousResponseID); id != "" {
		return id
	}
	// A previous response id unknown to this process still provides a stable
	// anchor for this chain; remember it under the next response id.
	return "response:" + previousResponseID
}

func (a *App) clientConversationFromPayload(r *http.Request, payload map[string]any) (string, bool) {
	return a.clientConversationIDFromPayload(r, payload), hasPreviousResponseID(payload)
}

func (a *App) clientConversationFromBody(r *http.Request, body []byte) (string, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", false
	}
	return a.clientConversationFromPayload(r, payload)
}

func responseConversationAnchor(req models.OpenAIChatRequest) string {
	if clientID := cleanClientConversationID(req.ClientConversationID); clientID != "" {
		return clientID
	}
	if transcriptKey, _ := format.ConversationID(req.Messages); transcriptKey != "" {
		// The first user message stays stable if the client resends its transcript.
		return "responses:transcript:" + transcriptKey
	}
	return "responses:root:" + format.RandHex(16)
}

func hasPreviousResponseID(payload map[string]any) bool {
	previousResponseID, _ := payload["previous_response_id"].(string)
	return cleanClientConversationID(previousResponseID) != ""
}

func conversationIDFromPayload(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	for _, key := range conversationIDFields {
		if id := cleanClientConversationID(valueAsString(payload[key])); id != "" {
			return id
		}
	}
	if id := conversationValueID(payload["conversation"]); id != "" {
		return id
	}
	if metadata, ok := payload["metadata"].(map[string]any); ok {
		for _, key := range conversationIDFields {
			if id := cleanClientConversationID(valueAsString(metadata[key])); id != "" {
				return id
			}
		}
		if id := conversationValueID(metadata["conversation"]); id != "" {
			return id
		}
	}
	return ""
}

func conversationValueID(value any) string {
	if id := cleanClientConversationID(valueAsString(value)); id != "" {
		return id
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if id := cleanClientConversationID(valueAsString(obj["id"])); id != "" {
		return id
	}
	for _, key := range conversationIDFields {
		if id := cleanClientConversationID(valueAsString(obj[key])); id != "" {
			return id
		}
	}
	return ""
}

func valueAsString(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func cleanClientConversationID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > maxClientConversationIDSize {
		return ""
	}
	return id
}

func openAIConversationInfo(req models.OpenAIChatRequest) (string, bool) {
	key, hasHistory := format.ConversationID(req.Messages)
	if clientID := cleanClientConversationID(req.ClientConversationID); clientID != "" {
		const responseTranscriptPrefix = "responses:transcript:"
		if strings.HasPrefix(clientID, responseTranscriptPrefix) {
			return strings.TrimPrefix(clientID, responseTranscriptPrefix), true
		}
		return format.ConversationIDFromClientID(clientID), true
	}
	return key, hasHistory
}

func (a *App) responseConversationForID(responseID string) string {
	responseID = strings.TrimSpace(responseID)
	if responseID == "" {
		return ""
	}
	a.responseConversationMu.Lock()
	defer a.responseConversationMu.Unlock()
	entry, ok := a.responseConversations[responseID]
	if !ok {
		return ""
	}
	if time.Since(entry.createdAt) > responseConversationTTL {
		delete(a.responseConversations, responseID)
		return ""
	}
	return entry.clientConversationID
}

func (a *App) rememberResponseConversation(responseID, clientConversationID string) {
	responseID = strings.TrimSpace(responseID)
	clientConversationID = strings.TrimSpace(clientConversationID)
	if responseID == "" || clientConversationID == "" {
		return
	}
	a.responseConversationMu.Lock()
	defer a.responseConversationMu.Unlock()
	if a.responseConversations == nil {
		a.responseConversations = make(map[string]responseConversationEntry)
	}
	now := time.Now()
	if _, exists := a.responseConversations[responseID]; !exists && len(a.responseConversations) >= maxResponseConversationIDs {
		oldestID := ""
		oldestAt := now
		for id, entry := range a.responseConversations {
			if now.Sub(entry.createdAt) > responseConversationTTL {
				delete(a.responseConversations, id)
				continue
			}
			if oldestID == "" || entry.createdAt.Before(oldestAt) {
				oldestID = id
				oldestAt = entry.createdAt
			}
		}
		if len(a.responseConversations) >= maxResponseConversationIDs && oldestID != "" {
			delete(a.responseConversations, oldestID)
		}
	}
	a.responseConversations[responseID] = responseConversationEntry{
		clientConversationID: clientConversationID,
		createdAt:            now,
	}
}
