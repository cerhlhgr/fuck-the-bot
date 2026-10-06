package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

func TestConversationIncludesAllMessages(t *testing.T) {
	now := time.Now()
	entries := make([]model.HistoryEntry, 85)
	for i := range entries {
		entries[i] = model.HistoryEntry{Date: now.Add(time.Duration(i) * time.Minute), MessageID: int64(i + 1), Author: "@ivan", Text: strings.Repeat("Я", 500)}
	}
	var transcript []struct {
		MessageID int64  `json:"message_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal([]byte(Conversation(entries)), &transcript); err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 85 || transcript[0].MessageID != 1 || transcript[84].MessageID != 85 || len([]rune(transcript[84].Text)) != 500 {
		t.Fatalf("history was truncated: count=%d", len(transcript))
	}
	prompt := SystemPrompt(model.DecisionRequest{BotUsername: "MyBot", CurrentMessageID: 85, History: entries})
	if !strings.Contains(prompt, "последние 2 часа") || !strings.Contains(prompt, "message_id=85") || !strings.Contains(prompt, `"reply":null`) {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
}

func TestParseAIDecision(t *testing.T) {
	reply, err := ParseAIDecision(`{"reply":"Ну привет!","reply_to_message_id":17}`)
	if err != nil || reply.Reply != "Ну привет!" || reply.ReplyToMessageID != 17 {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	silent, err := ParseAIDecision(`{"reply":null,"reply_to_message_id":null}`)
	if err != nil || silent.Reply != "" || silent.ReplyToMessageID != 0 {
		t.Fatalf("silence = %+v, %v", silent, err)
	}
	for _, invalid := range []string{
		`{"reply":"hi","reply_to_message_id":null}`,
		`{"reply":null,"reply_to_message_id":17}`,
		"```json\n{\"reply\":\"hi\"}\n```",
		`{"reply":"hi","reply_to_message_id":17} trailing`,
	} {
		if _, err := ParseAIDecision(invalid); err == nil {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestTelegramChunks(t *testing.T) {
	chunks := TelegramChunks(strings.Repeat("😈", 2500))
	if len(chunks) != 2 || len([]rune(chunks[0])) != 2000 || len([]rune(chunks[1])) != 500 {
		t.Fatalf("unexpected chunks: %d", len(chunks))
	}
}
