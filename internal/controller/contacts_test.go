package controller

import (
	"context"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestMentionRequestLoadsChatContactsAndHandlesSeveralPeople(t *testing.T) {
	repo := &fakeRepo{contacts: map[int64][]model.Contact{
		-42: {
			{UserID: 1, Username: "igor", Name: "Игорь", Link: "tg://user?id=1"},
			{UserID: 2, Username: "alexey", Name: "Алексей", Link: "tg://user?id=2"},
		},
		-43: {{UserID: 3, Username: "igor", Name: "Другой Игорь", Link: "tg://user?id=3"}},
	}}
	msg := model.Message{MessageID: 81, Text: "Линкани Игорька и Лёху"}
	mentionTestMessage(&msg, "mybot")
	msg.Chat.ID = -42
	ai := &fakeAI{decision: model.Decision{Actions: []model.Decision{
		{Action: "mention", ContactQuery: "igor", ReplyToMessageID: 81},
		{Action: "mention", ContactQuery: "alexey", ReplyToMessageID: 81},
	}}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Username: "mybot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 1, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if repo.recentContactsCalls != 1 || len(ai.request.Contacts) != 2 || len(tg.mentionedContacts) != 2 || tg.mentionedContacts[0].UserID != 1 || tg.mentionedContacts[1].UserID != 2 {
		t.Fatalf("contact request failed: calls=%d context=%+v mentions=%+v", repo.recentContactsCalls, ai.request.Contacts, tg.mentionedContacts)
	}
}

func TestNormalRequestDoesNotLoadContacts(t *testing.T) {
	repo := &fakeRepo{}
	msg := model.Message{MessageID: 82, Text: "Какая сегодня погода?"}
	mentionTestMessage(&msg, "mybot")
	msg.Chat.ID = -42
	ai := &fakeAI{decision: model.Decision{Action: "silence"}}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Username: "mybot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 2, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if repo.recentContactsCalls != 0 || len(ai.request.Contacts) != 0 {
		t.Fatalf("contacts loaded for unrelated request: calls=%d contacts=%+v", repo.recentContactsCalls, ai.request.Contacts)
	}
}
