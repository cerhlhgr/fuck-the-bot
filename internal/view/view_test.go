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
	if !strings.Contains(prompt, "последний час") || !strings.Contains(prompt, "message_id=85") || !strings.Contains(prompt, `"action":"silence"`) || !strings.Contains(prompt, `"action":"poll"`) || !strings.Contains(prompt, `"action":"image"`) || !strings.Contains(prompt, `"action":"reaction"`) {
		t.Fatal("system prompt is missing the context or available actions")
	}
}

func TestParseAIDecision(t *testing.T) {
	reply, err := ParseAIDecision(`{"reply":"Ну привет!","reply_to_message_id":17}`)
	if err != nil || reply.Reply != "Ну привет!" || reply.ReplyToMessageID != 17 {
		t.Fatalf("reply = %+v, %v", reply, err)
	}
	silent, err := ParseAIDecision(`{"reply":null,"reply_to_message_id":null}`)
	if err != nil || silent.Reply != "" || silent.Poll != nil || silent.ReplyToMessageID != 0 {
		t.Fatalf("silence = %+v, %v", silent, err)
	}
	poll, err := ParseAIDecision(`{"reply":null,"poll":{"question":"Куда идём?","options":[" В кино ","Домой"]},"reply_to_message_id":17}`)
	if err != nil || poll.Poll == nil || poll.Poll.Question != "Куда идём?" || len(poll.Poll.Options) != 2 || poll.Poll.Options[0] != "В кино" || poll.ReplyToMessageID != 17 {
		t.Fatalf("poll = %+v, %v", poll, err)
	}
	for _, test := range []struct {
		input  string
		action string
	}{
		{`{"action":"silence"}`, "silence"},
		{`{"action":"reply","reply":"Ну привет!","reply_to_message_id":17}`, "reply"},
		{`{"action":"message","reply":"Всем привет!","reply_to_message_id":null}`, "message"},
		{`{"action":"poll","poll":{"question":"Куда?","options":["Туда","Сюда"]},"reply_to_message_id":null}`, "poll"},
		{`{"action":"image","image_query":"cat in sunglasses","caption":"Держи","reply_to_message_id":17}`, "image"},
		{`{"action":"reaction","reaction":"🤡","reply_to_message_id":17}`, "reaction"},
	} {
		got, err := ParseAIDecision(test.input)
		if err != nil || got.Action != test.action {
			t.Errorf("%s: %+v, %v", test.input, got, err)
		}
	}
	for _, invalid := range []string{
		`{"reply":"hi","reply_to_message_id":null}`,
		`{"reply":null,"reply_to_message_id":17}`,
		"```json\n{\"reply\":\"hi\"}\n```",
		`{"reply":"hi","reply_to_message_id":17} trailing`,
		`{"reply":"hi","poll":{"question":"A?","options":["A","B"]},"reply_to_message_id":17}`,
		`{"reply":null,"poll":{"question":"A?","options":["A"]},"reply_to_message_id":17}`,
		`{"reply":null,"poll":{"question":"A?","options":["A","a"]},"reply_to_message_id":17}`,
		`{"reply":null,"poll":{"question":"A?","options":["A","B"]},"reply_to_message_id":null}`,
		`{"reply":null,"poll":{"question":"","options":["A","B"]},"reply_to_message_id":17}`,
		`{"reply":null,"poll":{"question":"A?","options":["","B"]},"reply_to_message_id":17}`,
		`{"reply":null,"poll":{"question":"A?","options":["` + strings.Repeat("я", 101) + `","B"]},"reply_to_message_id":17}`,
		`{"reply":null,"poll":{"question":"` + strings.Repeat("я", 301) + `","options":["A","B"]},"reply_to_message_id":17}`,
		`{"action":"nonsense"}`,
		`{"action":"silence","reply_to_message_id":17}`,
		`{"action":"reply","reply":"hello"}`,
		`{"action":"image","image_query":""}`,
		`{"action":"image","image_query":"cat","reply":"hello"}`,
		`{"action":"reaction","reaction":"🍕","reply_to_message_id":17}`,
		`{"action":"reaction","reaction":"🤡"}`,
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
