package controller

import (
	"context"
	"fmt"
	"testing"

	"fuck-the-bot/internal/model"
)

type fakeActionPlans struct {
	plans map[int64]model.ActionPlan
}

func TestMigratedGroupActionPlanStopsRetrying(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	msg := model.Message{MessageID: 121, Text: "@mybot привет"}
	msg.Chat.ID = -5081451629
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 478934937, Message: &msg}, nil); err != nil {
		t.Fatal(err)
	}
	plans := &fakeActionPlans{plans: map[int64]model.ActionPlan{478934937: {
		UpdateIDs: []int64{478934937},
		Actions: []model.Decision{
			{Action: "reply", Reply: "Привет", ReplyToMessageID: 121},
			{Action: "message", Reply: "Ещё сообщение"},
		},
	}}}
	telegram := &fakeTelegram{sendMessageErr: fmt.Errorf("sendMessage: %w", model.ErrChatMigrated)}
	worker := Worker{Repo: repo, ActionPlans: plans, AI: &fakeAI{}, Telegram: telegram, Username: "mybot"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repo.queued) != 0 || plans.plans[478934937].Completed != 2 || telegram.sendMessageCalls != 1 {
		t.Fatalf("migrated plan kept retrying: queued=%d completed=%d sends=%d", len(repo.queued), plans.plans[478934937].Completed, telegram.sendMessageCalls)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if telegram.sendMessageCalls != 1 {
		t.Fatalf("migrated plan repeated send: %d", telegram.sendMessageCalls)
	}
}

func (f *fakeActionPlans) LoadActionPlan(_ context.Context, _, _, firstID int64) (model.ActionPlan, bool, error) {
	plan, found := f.plans[firstID]
	return plan, found, nil
}
func (f *fakeActionPlans) SaveActionPlan(_ context.Context, _, _, firstID int64, plan model.ActionPlan) (model.ActionPlan, error) {
	if f.plans == nil {
		f.plans = make(map[int64]model.ActionPlan)
	}
	if existing, found := f.plans[firstID]; found {
		return existing, nil
	}
	f.plans[firstID] = plan
	return plan, nil
}
func (f *fakeActionPlans) MarkActionCompleted(_ context.Context, _, _, firstID int64, index int) error {
	plan := f.plans[firstID]
	plan.Completed = index + 1
	f.plans[firstID] = plan
	return nil
}

func TestScheduledBatchResumesRemainingActionsWithoutNewAICall(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	for _, input := range []struct{ updateID, messageID int64 }{{1, 10}, {2, 11}} {
		msg := model.Message{MessageID: input.messageID, Text: "Сделай что-нибудь"}
		mentionTestMessage(&msg, "mybot")
		msg.Chat.ID = -42
		if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: input.updateID, Message: &msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ai := &fakeAI{decision: model.Decision{
		Actions: []model.Decision{
			{Action: "reply", Reply: "Первому", ReplyToMessageID: 10},
			{Action: "reply", Reply: "Второму", ReplyToMessageID: 11},
		},
		ImportantUpdates: []model.ImportantUpdate{{SourceMessageID: 10, Summary: "Встреча 12 октября", Kind: "fact"}},
	}}
	telegram := &fakeTelegram{sendMessageErrorAt: 2}
	plans := &fakeActionPlans{}
	worker := Worker{Repo: repo, ActionPlans: plans, AI: ai, Telegram: telegram, Username: "mybot"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(telegram.answers) != 1 || telegram.answers[0] != "Первому" || len(repo.queued) != 2 || plans.plans[1].Completed != 1 || len(repo.important) != 1 || repo.consideredUpdates[1] || repo.consideredUpdates[2] {
		t.Fatalf("first attempt: AI=%d sent=%+v queued=%d plan=%+v memory=%+v", ai.calls, telegram.answers, len(repo.queued), plans.plans[1], repo.important)
	}
	third := model.Message{MessageID: 12, Text: "Новое сообщение"}
	mentionTestMessage(&third, "mybot")
	third.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 3, Message: &third}, nil); err != nil {
		t.Fatal(err)
	}
	telegram.sendMessageErrorAt = 0
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(telegram.answers) != 2 || telegram.answers[1] != "Второму" || len(repo.queued) != 1 || repo.queued[0].UpdateID != 3 || plans.plans[1].Completed != 2 || !repo.consideredUpdates[1] || !repo.consideredUpdates[2] {
		t.Fatalf("resume repeated or lost actions: AI=%d sent=%+v queued=%+v plan=%+v", ai.calls, telegram.answers, repo.queued, plans.plans[1])
	}
	ai.decision = model.Decision{Action: "silence"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 2 || len(repo.queued) != 0 || len(repo.important) != 1 {
		t.Fatalf("new update was not handled separately: AI=%d queued=%+v memory=%+v", ai.calls, repo.queued, repo.important)
	}
}
