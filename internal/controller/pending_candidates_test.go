package controller

import (
	"context"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestMentionReviewsEarlierUnlinkedRequestOnce(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	first := model.Message{MessageID: 100, Text: "Сделай опрос про встречу"}
	first.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 1, Message: &first}, nil); err != nil {
		t.Fatal(err)
	}
	ai := &fakeAI{decision: model.Decision{Actions: []model.Decision{
		{Action: "reply", Reply: "Сделаю опрос", ReplyToMessageID: 100},
		{Action: "reply", Reply: "И тебе привет", ReplyToMessageID: 101},
	}}}
	tg := &fakeTelegram{}
	plans := &fakeActionPlans{}
	worker := Worker{Repo: repo, ActionPlans: plans, AI: ai, Telegram: tg, Username: "mybot"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 0 || len(repo.queued) != 0 || repo.consideredUpdates[1] {
		t.Fatalf("unlinked message was handled before trigger: calls=%d considered=%v", ai.calls, repo.consideredUpdates)
	}
	trigger := model.Message{MessageID: 101, Text: "Привет"}
	mentionTestMessage(&trigger, "mybot")
	trigger.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 2, Message: &trigger}, nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(ai.request.NewMessageIDs) != 2 || ai.request.NewMessageIDs[0] != 100 || ai.request.NewMessageIDs[1] != 101 || len(ai.request.TriggerMessageIDs) != 1 || ai.request.TriggerMessageIDs[0] != 101 || len(tg.messages) != 2 || tg.messages[0].MessageID != 100 || tg.messages[1].MessageID != 101 || !repo.consideredUpdates[1] || !repo.consideredUpdates[2] {
		t.Fatalf("earlier request was lost: new=%v triggers=%v targets=%+v considered=%v", ai.request.NewMessageIDs, ai.request.TriggerMessageIDs, tg.messages, repo.consideredUpdates)
	}
	secondTrigger := model.Message{MessageID: 102, Text: "Ещё вопрос"}
	mentionTestMessage(&secondTrigger, "mybot")
	secondTrigger.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 3, Message: &secondTrigger}, nil); err != nil {
		t.Fatal(err)
	}
	ai.decision = model.Decision{Action: "silence"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 2 || len(ai.request.NewMessageIDs) != 1 || ai.request.NewMessageIDs[0] != 102 || len(tg.messages) != 2 {
		t.Fatalf("old request reconsidered: new=%v targets=%+v", ai.request.NewMessageIDs, tg.messages)
	}
}
