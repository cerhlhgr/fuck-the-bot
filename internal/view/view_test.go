package view

import (
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

func TestPromptAndConversationLimits(t *testing.T) {
	now := time.Now()
	var entries []model.HistoryEntry
	for i := 0; i < 85; i++ {
		entries = append(entries, model.HistoryEntry{Date: now.Add(-time.Duration(i) * time.Minute), Author: "@ivan", Text: model.CompactText(strings.Repeat("Я", 500), model.MaxSavedRunes)})
	}
	conversation := Conversation(entries)
	if len([]rune(conversation)) > MaxContextRunes || strings.Count(conversation, "@ivan:") > model.MaxContextMessages {
		t.Fatalf("context limits exceeded: %d runes", len([]rune(conversation)))
	}
	prompt := SystemPrompt("привет", entries)
	if !strings.Contains(prompt, "ранее был диалог в беседе:\n") || !strings.Contains(prompt, "Текущее сообщение: привет") || !strings.Contains(prompt, `{"reply":"твой ответ"}`) {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestAIReplyAndTelegramChunks(t *testing.T) {
	got, err := ParseAIReply(`{"reply":"Ну привет!"}`)
	if err != nil || got != "Ну привет!" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, invalid := range []string{`{"reply":""}`, "```json\n{\"reply\":\"hi\"}\n```", `{"reply":"hi"} trailing`} {
		if _, err := ParseAIReply(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
	chunks := TelegramChunks(strings.Repeat("😈", 2500))
	if len(chunks) != 2 || len([]rune(chunks[0])) != 2000 || len([]rune(chunks[1])) != 500 {
		t.Fatalf("unexpected chunks: %d", len(chunks))
	}
}
