package format

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

// ConversationID identifies which client conversation a request belongs to, so
// the upstream can keep one thread per chat instead of one thread for everything.
//
// When a client doesn't provide an explicit conversation ID, the identity is
// the first user message: continuing a chat resends it verbatim as the first
// element of the history. This is a fallback only; it cannot reliably identify
// a new chat for stateless clients that send only the latest user message.
//
// hasHistory reports whether the client sent assistant/tool turns — the
// difference between "a real conversation is under way" and "a single message",
// which the caller needs before it can tell a new chat from a stateless client.
func ConversationID(messages []models.OpenAIMessage) (key string, hasHistory bool) {
	firstUser := ""
	for _, msg := range messages {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "assistant", "tool", "function":
			hasHistory = true
		case "", "user", "system", "developer":
			if firstUser == "" && (strings.EqualFold(msg.Role, "") || strings.EqualFold(msg.Role, "user")) {
				text, _ := ExtractMessageContent(msg)
				firstUser = strings.TrimSpace(text)
			}
		}
	}
	if firstUser == "" {
		return "", hasHistory
	}
	sum := sha256.Sum256([]byte(firstUser))
	return hex.EncodeToString(sum[:4]), hasHistory
}

// ConversationIDFromClientID returns a stable, non-reversible key for a client
// supplied conversation identifier. Hashing keeps arbitrary client IDs out of
// logs and upstream payloads while preserving a stable session key.
func ConversationIDFromClientID(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(clientID))
	return hex.EncodeToString(sum[:])
}

// GoogleConversationID is ConversationID for a Google-shaped request: the first
// user turn identifies the conversation, any model/tool turn means a real
// exchange is under way.
func GoogleConversationID(req models.GoogleGenerateRequest) (key string, hasHistory bool) {
	firstUser := ""
	for _, content := range req.Contents {
		role := strings.ToLower(strings.TrimSpace(content.Role))
		switch role {
		case "model", "function":
			hasHistory = true
		default:
			if firstUser != "" {
				continue
			}
			var text strings.Builder
			for _, part := range content.Parts {
				if part.Text != "" {
					text.WriteString(part.Text)
				}
			}
			firstUser = strings.TrimSpace(text.String())
		}
	}
	if firstUser == "" {
		return "", hasHistory
	}
	sum := sha256.Sum256([]byte(firstUser))
	return hex.EncodeToString(sum[:4]), hasHistory
}
