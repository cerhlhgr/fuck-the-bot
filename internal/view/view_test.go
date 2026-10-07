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
	if !strings.Contains(prompt, "последний час") || !strings.Contains(prompt, "message_id=85") || !strings.Contains(prompt, `"actions":[]`) || !strings.Contains(prompt, `"action":"poll"`) || !strings.Contains(prompt, `"action":"image"`) || !strings.Contains(prompt, `"action":"reaction"`) {
		t.Fatal("system prompt is missing the context or available actions")
	}
}

func TestSystemPromptIncludesPermanentContext(t *testing.T) {
	request := model.DecisionRequest{
		BotUsername:      "MyBot",
		CurrentMessageID: 9,
		NewMessageIDs:    []int64{8, 9},
		Important: []model.ImportantEntry{
			{SourceMessageID: 3, SourceDate: time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC), Author: "@ivan", Summary: "Не писать в чат", Kind: "instruction"},
			{SourceMessageID: 4, SourceDate: time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC), Author: "@ivan", Summary: "Можно снова писать в чат", Kind: "instruction"},
		},
	}
	prompt := SystemPrompt(request)
	memory := ImportantContext(request.Important)
	if !strings.Contains(prompt, `"important_updates"`) || !strings.Contains(prompt, `"forget_important_ids"`) || !strings.Contains(prompt, `message_id=[8,9]`) || !strings.Contains(memory, `"source_message_id"`) || !strings.Contains(memory, `"kind"`) || !strings.Contains(memory, `2026-10-06T20:00:00Z`) || !strings.Contains(memory, `2026-10-07T08:00:00Z`) || strings.Contains(memory, `source_text`) || strings.Index(memory, request.Important[0].Summary) >= strings.Index(memory, request.Important[1].Summary) || !strings.Contains(prompt, "сравни полные дату и время") {
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

func TestSystemPromptLinksReplyToBotWithUserMessage(t *testing.T) {
	prompt := SystemPrompt(model.DecisionRequest{
		BotUsername: "MyBot", NewMessageIDs: []int64{5508}, NewReplyToBotIDs: []int64{5508},
		RepliedToBotMessages: []model.RepliedToBotMessage{{UserMessageID: 5508, BotMessageID: 5507, BotText: "Ответ бота"}},
	})
	if !strings.Contains(prompt, `"user_message_id":5508`) || !strings.Contains(prompt, `"bot_message_id":5507`) || !strings.Contains(prompt, `"bot_text":"Ответ бота"`) || !strings.Contains(prompt, "укажи user_message_id") {
		t.Fatal("prompt does not explain how to answer a reply to the bot")
	}
}

func TestMentionActionUsesContactLookup(t *testing.T) {
	prompt := SystemPrompt(model.DecisionRequest{BotUsername: "mybot"})
	if !strings.Contains(prompt, `"action":"mention"`) || !strings.Contains(prompt, "contact_query") {
		t.Fatalf("contact lookup not described in prompt")
	}
	decision, err := ParseAIDecision(`{"actions":[{"action":"mention","contact_query":"Сергей","reply":"Вот:","reply_to_message_id":123}]}`)
	if err != nil || len(decision.Actions) != 1 || decision.Actions[0].ContactQuery != "Сергей" || decision.Actions[0].ReplyToMessageID != 123 {
		t.Fatalf("mention action = %+v, %v", decision, err)
	}
	if _, err := ParseAIDecision(`{"actions":[{"action":"mention","contact_query":"","reply_to_message_id":123}]}`); err == nil {
		t.Fatal("empty contact query accepted")
	}
}

func TestParseAIDecision(t *testing.T) {
	batchMemory, err := ParseAIDecision(`{"action":"silence","important_updates":[{"source_message_id":10,"summary":"Встреча в пятницу","kind":"fact"},{"source_message_id":11,"summary":"Не писать до утра","kind":"instruction"}]}`)
	if err != nil || len(batchMemory.ImportantUpdates) != 2 || batchMemory.ImportantUpdates[0].SourceMessageID != 10 || batchMemory.ImportantUpdates[1].Kind != "instruction" {
		t.Fatalf("batch memory = %+v, %v", batchMemory, err)
	}
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
	forgotten, err := ParseAIDecision(`{"action":"silence","forget_important_ids":[3,4]}`)
	if err != nil || len(forgotten.ForgetImportantIDs) != 2 || forgotten.ForgetImportantIDs[0] != 3 || forgotten.ForgetImportantIDs[1] != 4 || forgotten.Important != "" {
		t.Fatalf("forgotten context decision = %+v, %v", forgotten, err)
	}
	replaced, err := ParseAIDecision(`{"action":"silence","forget_important_ids":[3],"important":"Можно писать","important_type":"instruction"}`)
	if err != nil || len(replaced.ForgetImportantIDs) != 1 || replaced.Important != "Можно писать" || replaced.ImportantKind != "instruction" {
		t.Fatalf("replacement decision = %+v, %v", replaced, err)
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
		`{"action":"silence","forget_important_ids":[0]}`,
		`{"action":"silence","forget_important_ids":[3,3]}`,
		`{"action":"silence","important_updates":[{"source_message_id":0,"summary":"Встреча","kind":"fact"}]}`,
		`{"action":"silence","important_updates":[{"source_message_id":1,"summary":"Встреча","kind":"fact"},{"source_message_id":1,"summary":"Повтор","kind":"fact"}]}`,
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

func TestParseAIBatchActions(t *testing.T) {
	decision, err := ParseAIDecision(`{"actions":[{"action":"reply","reply":"Привет","reply_to_message_id":10},{"action":"poll","poll":{"question":"Куда?","options":["Домой","В кино"]},"reply_to_message_id":11}],"important_updates":[{"source_message_id":11,"summary":"Встреча 12 октября в 18:00","kind":"fact"}]}`)
	if err != nil || len(decision.Actions) != 2 || decision.Actions[0].Action != "reply" || decision.Actions[0].ReplyToMessageID != 10 || decision.Actions[1].Poll == nil || len(decision.ImportantUpdates) != 1 {
		t.Fatalf("batch decision = %+v, %v", decision, err)
	}
	silent, err := ParseAIDecision(`{"actions":[],"forget_important_ids":[9]}`)
	if err != nil || len(silent.Actions) != 0 || len(silent.ForgetImportantIDs) != 1 {
		t.Fatalf("empty action batch = %+v, %v", silent, err)
	}
	for _, input := range []string{
		`{"actions":null}`,
		`{"actions":{}}`,
		`{"actions":[{"action":"silence"}]}`,
		`{"actions":[{"action":"reply","reply":"Привет","reply_to_message_id":10,"important_updates":[{"source_message_id":10,"summary":"Факт","kind":"fact"}]}]}`,
		`{"actions":[{"action":"reply","reply":"Привет","reply_to_message_id":10}],"action":"message"}`,
		`{"actions":[{"action":"reply","reply":"Привет","reply_to_message_id":10},{"action":"voice","voice":{"text":""},"reply_to_message_id":11}]}`,
	} {
		if _, err := ParseAIDecision(input); err == nil {
			t.Fatalf("accepted invalid batch: %s", input)
		}
	}
}

func TestMusicDecision(t *testing.T) {
	for _, tc := range []struct {
		input string
		mode  string
	}{
		{`{"action":"music","music":{"mode":"simple","prompt":"панк-рок про поездку"},"reply_to_message_id":17}`, "simple"},
		{`{"action":"music","music":{"mode":"custom","title":"Дорога","style":"punk rock","prompt":"[Verse] Мы едем","vocal_gender":"m"},"reply_to_message_id":17}`, "custom"},
		{`{"action":"music","music":{"mode":"custom","title":"Дорога","style":"ambient","instrumental":true},"reply_to_message_id":17}`, "custom"},
	} {
		got, err := ParseAIDecision(tc.input)
		if err != nil || got.Action != "music" || got.ReplyToMessageID != 17 || got.Music == nil || got.Music.Mode != tc.mode {
			t.Fatalf("music decision: %+v, %v", got, err)
		}
	}
	for _, input := range []string{
		`{"action":"music","music":{"mode":"simple","prompt":"трек"}}`,
		`{"action":"music","music":{"mode":"simple","prompt":""},"reply_to_message_id":17}`,
		`{"action":"music","music":{"mode":"custom","title":"Трек","style":"rock"},"reply_to_message_id":17}`,
		`{"action":"music","music":{"mode":"custom","title":"Трек","style":"rock","prompt":"текст","vocal_gender":"other"},"reply_to_message_id":17}`,
		`{"action":"reply","reply":"ок","reply_to_message_id":17,"music":{"mode":"simple","prompt":"трек"}}`,
	} {
		if _, err := ParseAIDecision(input); err == nil {
			t.Fatalf("accepted invalid music decision %s", input)
		}
	}
	prompt := SystemPrompt(model.DecisionRequest{BotUsername: "test", MusicEnabled: true})
	if !strings.Contains(prompt, `"action":"music"`) || !strings.Contains(prompt, "Генерация музыки в этом запуске доступна: true") {
		t.Fatal("music prompt missing action or availability")
	}
}

func TestVoiceDecision(t *testing.T) {
	decision, err := ParseAIDecision(`{"action":"voice","voice":{"text":" Ну что, собрались? ","speaker":"onyx","instructions":"С лёгкой ехидцей"},"reply_to_message_id":17}`)
	if err != nil || decision.Voice == nil || decision.Voice.Text != "Ну что, собрались?" || decision.Voice.Speaker != "onyx" || decision.ReplyToMessageID != 17 {
		t.Fatalf("voice decision: %+v, %v", decision, err)
	}
	defaultSpeaker, err := ParseAIDecision(`{"action":"voice","voice":{"text":"Привет"},"reply_to_message_id":17}`)
	if err != nil || defaultSpeaker.Voice == nil || defaultSpeaker.Voice.Speaker != model.DefaultVoiceSpeaker {
		t.Fatalf("default speaker: %+v, %v", defaultSpeaker, err)
	}
	for _, input := range []string{
		`{"action":"voice","voice":{"text":"Привет"}}`,
		`{"action":"voice","voice":{"text":"  "},"reply_to_message_id":17}`,
		`{"action":"voice","voice":{"text":"Привет","speaker":"unknown"},"reply_to_message_id":17}`,
		`{"action":"voice","voice":{"text":"` + strings.Repeat("я", model.MaxVoiceTextRunes+1) + `"},"reply_to_message_id":17}`,
		`{"action":"reply","reply":"Привет","voice":{"text":"Привет"},"reply_to_message_id":17}`,
	} {
		if _, err := ParseAIDecision(input); err == nil {
			t.Fatalf("accepted invalid voice decision: %s", input)
		}
	}
	prompt := SystemPrompt(model.DecisionRequest{BotUsername: "test", VoiceEnabled: true})
	if !strings.Contains(prompt, `"action":"voice"`) || !strings.Contains(prompt, "Синтез голосовых сообщений доступен: true") {
		t.Fatal("voice action missing from prompt")
	}
}

func TestTelegramChunks(t *testing.T) {
	chunks := TelegramChunks(strings.Repeat("😈", 2500))
	if len(chunks) != 2 || len([]rune(chunks[0])) != 2000 || len([]rune(chunks[1])) != 500 {
		t.Fatalf("unexpected chunks: %d", len(chunks))
	}
}
