package controller

import (
	"context"
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestVerdictReferencesShowMessagesInsteadOfBareIDs(t *testing.T) {
	history := []model.HistoryEntry{
		{MessageID: 9705, Author: "@igor", Text: "Ты снова меня оскорбляешь"},
		{MessageID: 9706, Author: "@alex", Text: "Отстань от меня"},
	}
	answer := "Вердикт: 9705 — оскорбление, 9706 — хамство."
	got := clarifyMessageReferences(answer, history)
	for _, expected := range []string{"«Ты снова меня оскорбляешь» (@igor)", "«Отстань от меня» (@alex)"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing human-readable reference %q: %q", expected, got)
		}
	}
	if strings.Contains(got, "9705") || strings.Contains(got, "9706") {
		t.Fatalf("bare message IDs remained: %q", got)
	}
	if got := clarifyMessageReferences("Встреча в 2026 году", history); got != "Встреча в 2026 году" {
		t.Fatalf("unrelated answer changed: %q", got)
	}
}

func TestWorkerClarifiesVerdictReferencesBeforeSending(t *testing.T) {
	repo := &fakeRepo{history: []model.HistoryEntry{
		{MessageID: 9705, Author: "@igor", Text: "Ты снова меня оскорбляешь"},
		{MessageID: 9706, Author: "@alex", Text: "Отстань от меня"},
	}}
	msg := model.Message{MessageID: 9707, Text: "Вынеси вердикт"}
	mentionTestMessage(&msg, "mybot")
	msg.Chat.ID = -42
	ai := &fakeAI{decision: model.Decision{
		Action:           "reply",
		Reply:            "Вердикт: 9705 — оскорбление, 9706 — хамство.",
		ReplyToMessageID: 9707,
	}}
	telegram := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: telegram, Username: "mybot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 1, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(telegram.answers) != 1 {
		t.Fatalf("expected one answer, got %v", telegram.answers)
	}
	answer := telegram.answers[0]
	if !strings.Contains(answer, "«Ты снова меня оскорбляешь» (@igor)") || !strings.Contains(answer, "«Отстань от меня» (@alex)") || strings.Contains(answer, "9705") || strings.Contains(answer, "9706") {
		t.Fatalf("verdict did not identify source messages: %q", answer)
	}
}
