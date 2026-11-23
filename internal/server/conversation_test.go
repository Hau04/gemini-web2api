package server

import (
	"net/http/httptest"
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func TestConversationIDExtractionFromPayloadAndHeaders(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	id := app.clientConversationIDFromPayload(req, map[string]any{
		"metadata": map[string]any{"thread_id": "workbuddy-thread-7"},
	})
	if id != "client:workbuddy-thread-7" {
		t.Fatalf("metadata thread id = %q, want %q", id, "client:workbuddy-thread-7")
	}

	req.Header.Set("X-Chat-ID", "workbuddy-chat-9")
	id = app.clientConversationIDFromPayload(req, nil)
	if id != "client:workbuddy-chat-9" {
		t.Fatalf("header chat id = %q, want %q", id, "client:workbuddy-chat-9")
	}

	id = app.clientConversationIDFromPayload(req, map[string]any{
		"conversation": map[string]any{"id": "conv-object-3"},
	})
	if id != "client:conv-object-3" {
		t.Fatalf("conversation object id = %q, want %q", id, "client:conv-object-3")
	}
}

func TestClientConversationIDFromBody(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	id, continuation := app.clientConversationFromBody(req, []byte(`{"metadata":{"conversation_id":"wb-chat-14"}}`))
	if id != "client:wb-chat-14" || continuation {
		t.Fatalf("body conversation id = %q (continuation=%v), want client:wb-chat-14", id, continuation)
	}
}

func TestResponsesResponseIDsPreserveConversationAnchor(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	oneShot := models.OpenAIChatRequest{Messages: []models.OpenAIMessage{{Role: "user", Content: "first turn"}}}

	// An input-only request without an explicit conversation id stays on the
	// history heuristic; it must not be treated as a different chat just because
	// the next user's prompt is different.
	firstID, firstContinuation := app.clientConversationFromPayload(req, nil)
	if firstID != "" || firstContinuation {
		t.Fatalf("unidentified input-only request should use the history heuristic, got %q (%v)", firstID, firstContinuation)
	}

	root := responseConversationAnchor(oneShot)
	if root == "" || root != responseConversationAnchor(oneShot) {
		t.Fatalf("same first user message should give a stable response-chain anchor: %q", root)
	}
	otherRoot := responseConversationAnchor(models.OpenAIChatRequest{Messages: []models.OpenAIMessage{{Role: "user", Content: "a different new chat"}}})
	if root == otherRoot {
		t.Fatalf("different first messages should produce distinct response-chain anchors: %q", root)
	}
	app.rememberResponseConversation("resp_root_a", root)

	followUp, isContinuation := app.clientConversationFromPayload(req, map[string]any{
		"previous_response_id": "resp_root_a",
	})
	if followUp != root || !isContinuation {
		t.Fatalf("previous_response_id should preserve its conversation: got %q (continuation=%v), want %q", followUp, isContinuation, root)
	}

	history := []models.OpenAIMessage{
		{Role: "user", Content: "first turn"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "follow-up"},
	}
	if historyAnchor := responseConversationAnchor(models.OpenAIChatRequest{Messages: history}); historyAnchor != root {
		t.Fatalf("resending full history should keep the same response-chain anchor: %q vs %q", historyAnchor, root)
	}
}

func TestResponsesPreviousResponseIDKeepsConversationAnchor(t *testing.T) {
	app := &App{}
	app.rememberResponseConversation("resp_previous", "client:workbuddy-chat-1")

	req := httptest.NewRequest("POST", "/v1/responses", nil)
	got := app.clientConversationIDFromPayload(req, map[string]any{
		"previous_response_id": "resp_previous",
	})
	if got != "client:workbuddy-chat-1" {
		t.Fatalf("known previous response id resolved to %q, want %q", got, "client:workbuddy-chat-1")
	}

	unknown := app.clientConversationIDFromPayload(req, map[string]any{
		"previous_response_id": "resp_from_another_instance",
	})
	if unknown != "response:resp_from_another_instance" {
		t.Fatalf("unknown previous response id resolved to %q", unknown)
	}
}

func TestOpenAIConversationInfoUsesClientIDForStableKey(t *testing.T) {
	messages := []models.OpenAIMessage{{Role: "user", Content: "first turn"}}
	key, hasHistory := openAIConversationInfo(models.OpenAIChatRequest{Messages: messages})
	linkedKey, linkedHasHistory := openAIConversationInfo(models.OpenAIChatRequest{
		ClientConversationID: "responses:transcript:" + key,
		Messages:             []models.OpenAIMessage{{Role: "user", Content: "follow-up"}},
	})
	if linkedKey != key || !linkedHasHistory {
		t.Fatalf("previous_response_id anchor should keep the first-turn session key: %q vs %q", linkedKey, key)
	}

	req := models.OpenAIChatRequest{
		ClientConversationID: "client:workbuddy-chat-1",
		Messages:             messages,
	}
	key, hasHistory = openAIConversationInfo(req)
	if key == "" || !hasHistory {
		t.Fatalf("explicit client conversation should produce a stable key and count as a conversation: key=%q hasHistory=%v", key, hasHistory)
	}
	key2, _ := openAIConversationInfo(models.OpenAIChatRequest{
		ClientConversationID: "client:workbuddy-chat-1",
		Messages:             []models.OpenAIMessage{{Role: "user", Content: "follow-up"}},
	})
	if key != key2 {
		t.Fatalf("same client conversation id must produce same key: %q vs %q", key, key2)
	}
}
