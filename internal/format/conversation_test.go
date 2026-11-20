package format

import (
	"testing"

	"github.com/ikhsan3adi/gemini-web2api/internal/models"
)

func TestConversationIDStableForSameChat(t *testing.T) {
	first := []models.OpenAIMessage{{Role: "user", Content: "hi"}}
	resumed := []models.OpenAIMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "what is your model?"},
	}

	keyA, hasA := ConversationID(first)
	keyB, hasB := ConversationID(resumed)
	if keyA == "" || keyA != keyB {
		t.Fatalf("the same chat must keep one identity: %q vs %q", keyA, keyB)
	}
	if hasA {
		t.Error("a lone message has no history")
	}
	if !hasB {
		t.Error("a request with an assistant turn is a conversation with history")
	}
}

func TestConversationIDDiffersForNewChat(t *testing.T) {
	a, _ := ConversationID([]models.OpenAIMessage{{Role: "user", Content: "hi"}})
	b, _ := ConversationID([]models.OpenAIMessage{{Role: "user", Content: "write a function"}})
	if a == b {
		t.Fatal("different first messages must produce different conversation ids")
	}
}

func TestConversationIDIgnoresSystemPrompt(t *testing.T) {
	plain, _ := ConversationID([]models.OpenAIMessage{{Role: "user", Content: "hi"}})
	withSystem, _ := ConversationID([]models.OpenAIMessage{
		{Role: "system", Content: "you are a helpful agent"},
		{Role: "user", Content: "hi"},
	})
	if plain != withSystem {
		t.Errorf("the system prompt must not change the conversation identity: %q vs %q", plain, withSystem)
	}
}

func TestConversationIDHandlesToolHistory(t *testing.T) {
	msgs := []models.OpenAIMessage{
		{Role: "user", Content: "read main.go"},
		{Role: "assistant", Content: "", ToolCalls: []models.OpenAIToolCall{{ID: "c1", Type: "function", Function: models.OpenAIToolCallFunction{Name: "read_file", Arguments: "{}"}}}},
		{Role: "tool", Content: "package main", ToolCallID: "c1"},
	}
	key, hasHistory := ConversationID(msgs)
	if key == "" || !hasHistory {
		t.Fatalf("tool history must count as history: key=%q hasHistory=%v", key, hasHistory)
	}
}

func TestGoogleConversationID(t *testing.T) {
	first := models.GoogleGenerateRequest{Contents: []models.GoogleContent{
		{Role: "user", Parts: []models.GooglePart{{Text: "hi"}}},
	}}
	keyA, hasA := GoogleConversationID(first)
	if keyA == "" || hasA {
		t.Fatalf("key=%q hasHistory=%v", keyA, hasA)
	}

	ongoing := models.GoogleGenerateRequest{Contents: []models.GoogleContent{
		{Role: "user", Parts: []models.GooglePart{{Text: "hi"}}},
		{Role: "model", Parts: []models.GooglePart{{Text: "hello"}}},
		{Role: "user", Parts: []models.GooglePart{{Text: "model?"}}},
	}}
	keyB, hasB := GoogleConversationID(ongoing)
	if keyA != keyB || !hasB {
		t.Fatalf("same chat must keep its id: %q vs %q (hasHistory=%v)", keyA, keyB, hasB)
	}

	other := models.GoogleGenerateRequest{Contents: []models.GoogleContent{
		{Role: "user", Parts: []models.GooglePart{{Text: "different"}}},
	}}
	if keyC, _ := GoogleConversationID(other); keyC == keyA {
		t.Fatal("a different chat must get a different id")
	}
}
