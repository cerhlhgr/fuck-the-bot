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
	entries[84].ReplyToMessageID = 17
	entries[84].Bot = true
	var transcript struct {
		Columns []string            `json:"columns"`
		Rows    [][]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal([]byte(Conversation(entries)), &transcript); err != nil {
		t.Fatal(err)
	}
	if len(transcript.Columns) != 6 || len(transcript.Rows) != 85 {
		t.Fatalf("history was truncated: count=%d", len(transcript.Rows))
	}
	var firstID, lastID int64
	var lastReplyTo int64
	var lastText string
	var lastBot bool
	if err := json.Unmarshal(transcript.Rows[0][1], &firstID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(transcript.Rows[84][1], &lastID); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(transcript.Rows[84][4], &lastText); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(transcript.Rows[84][2], &lastReplyTo); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(transcript.Rows[84][5], &lastBot); err != nil {
		t.Fatal(err)
	}
	if firstID != 1 || lastID != 85 || lastReplyTo != 17 || !lastBot || len([]rune(lastText)) != 500 {
		t.Fatalf("history data was lost: first=%d last=%d reply_to=%d bot=%t text_length=%d", firstID, lastID, lastReplyTo, lastBot, len([]rune(lastText)))
	}
	prompt := SystemPrompt(model.DecisionRequest{BotUsername: "MyBot", CurrentMessageID: 85, History: entries})
	if !strings.Contains(prompt, "последний час") || !strings.Contains(prompt, "message_id=85") || !strings.Contains(prompt, `"action":"silence"`) || !strings.Contains(prompt, `"action":"poll"`) || !strings.Contains(prompt, `"action":"image"`) || !strings.Contains(prompt, `"action":"reaction"`) {
		t.Fatal("system prompt is missing the context or available actions")
	}
}

func TestSystemPromptIncludesPermanentContext(t *testing.T) {
	request := model.DecisionRequest{
		BotUsername:      "MyBot",
		CurrentMessageID: 9,
		Important: []model.ImportantEntry{
			{SourceMessageID: 3, SourceDate: time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC), Author: "@ivan", Summary: "Не писать в чат", Kind: "instruction"},
			{SourceMessageID: 4, SourceDate: time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC), Author: "@ivan", Summary: "Можно снова писать в чат", Kind: "instruction"},
		},
	}
	prompt := SystemPrompt(request)
	memory := ImportantContext(request.Important)
	if !strings.Contains(prompt, `"important_type":"instruction"`) || !strings.Contains(memory, `"source_message_id"`) || !strings.Contains(memory, `"kind"`) || !strings.Contains(memory, `2026-10-06T20:00:00Z`) || !strings.Contains(memory, `2026-10-07T08:00:00Z`) || strings.Contains(memory, `source_text`) || strings.Index(memory, request.Important[0].Summary) >= strings.Index(memory, request.Important[1].Summary) || !strings.Contains(prompt, "сравни полные дату и время") {
		t.Fatalf("prompt is missing permanent context or output schema: %s", prompt)
	}
	other := request
	other.Important = nil
	other.History = []model.HistoryEntry{{Text: "Другая переписка"}}
	otherPrompt := SystemPrompt(other)
	marker := "Постоянная память этой беседы"
	start, otherStart := strings.Index(prompt, marker), strings.Index(otherPrompt, marker)
	if start < 0 || otherStart < 0 || prompt[:start] != otherPrompt[:otherStart] {
		t.Fatal("static prompt prefix changes with conversation context")
	}
}

func TestParseAIDecision(t *testing.T) {
	remembered, err := ParseAIDecision(`{"action":"silence","important":" Встреча в пятницу в 19:00. "}`)
	if err != nil || remembered.Action != "silence" || remembered.Important != "Встреча в пятницу в 19:00." {
		t.Fatalf("silent memory decision = %+v, %v", remembered, err)
	}
	if remembered.ImportantKind != "fact" {
		t.Fatalf("memory without type should be a fact: %+v", remembered)
	}
	instruction, err := ParseAIDecision(`{"action":"silence","important":"Не писать в чат","important_type":"instruction"}`)
	if err != nil || instruction.ImportantKind != "instruction" || instruction.Important != "Не писать в чат" {
		t.Fatalf("instruction decision = %+v, %v", instruction, err)
	}
	rememberedReply, err := ParseAIDecision(`{"action":"reply","reply":"Принято","reply_to_message_id":17,"important":"Правило беседы"}`)
	if err != nil || rememberedReply.Reply != "Принято" || rememberedReply.Important != "Правило беседы" {
		t.Fatalf("reply with memory = %+v, %v", rememberedReply, err)
	}
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
		`{"action":"silence","important_type":"instruction"}`,
		`{"action":"silence","important":"Не писать","important_type":"other"}`,
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
