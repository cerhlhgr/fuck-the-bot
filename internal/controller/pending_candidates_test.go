package controller

import (
	"context"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestUnmentionedMessageIsDecidedOnceWithoutWaitingForMention(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	first := model.Message{MessageID: 100, Text: "Кто знает, во сколько встреча?"}
	first.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 1, Message: &first}, nil); err != nil {
		t.Fatal(err)
	}
	ai := &fakeAI{decision: model.Decision{Action: "reply", Reply: "В 19:00", ReplyToMessageID: 100}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, ActionPlans: &fakeActionPlans{}, AI: ai, Telegram: tg, Username: "mybot"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(ai.request.NewMessageIDs) != 1 || ai.request.NewMessageIDs[0] != 100 || len(ai.request.TriggerMessageIDs) != 0 || len(tg.messages) != 1 || tg.messages[0].MessageID != 100 || !repo.consideredUpdates[1] || len(repo.queued) != 0 {
		t.Fatalf("unmentioned message was not decided: calls=%d new=%v direct=%v sent=%+v considered=%v", ai.calls, ai.request.NewMessageIDs, ai.request.TriggerMessageIDs, tg.messages, repo.consideredUpdates)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(tg.messages) != 1 {
		t.Fatal("decision was repeated without new messages")
	}

	second := model.Message{MessageID: 101, Text: "Спасибо"}
	second.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 2, Message: &second}, nil); err != nil {
		t.Fatal(err)
	}
	ai.decision = model.Decision{Action: "silence"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 2 || len(ai.request.NewMessageIDs) != 1 || ai.request.NewMessageIDs[0] != 101 || len(tg.messages) != 1 || !repo.consideredUpdates[2] {
		t.Fatalf("silent decision or one-time processing failed: calls=%d new=%v sent=%+v", ai.calls, ai.request.NewMessageIDs, tg.messages)
	}
}

func TestScheduledRunProcessesMoreThanOneDecisionBatch(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	for i := int64(1); i <= maxDecisionCandidates+3; i++ {
		msg := model.Message{MessageID: i, Text: "обычная реплика"}
		msg.Chat.ID = -42
		if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: i, Message: &msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ai := &fakeAI{decision: model.Decision{Action: "silence"}}
	worker := Worker{Repo: repo, ActionPlans: &fakeActionPlans{}, AI: ai, Telegram: &fakeTelegram{}, Username: "mybot"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 2 || len(ai.requests[0].NewMessageIDs) != maxDecisionCandidates || len(ai.requests[1].NewMessageIDs) != 3 || len(repo.queued) != 0 {
		t.Fatalf("large batch was dropped: calls=%d batches=%d/%d pending=%d", ai.calls, len(ai.requests[0].NewMessageIDs), len(ai.requests[1].NewMessageIDs), len(repo.queued))
	}
	for id := int64(1); id <= maxDecisionCandidates+3; id++ {
		if !repo.consideredUpdates[id] {
			t.Fatalf("update %d was not considered", id)
		}
	}
}

func TestCurrentMessagesTakePriorityOverOldUnconsideredMessages(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	for id := int64(1); id <= 40; id++ {
		msg := model.Message{MessageID: id, Text: "накопилось до обновления"}
		msg.Chat.ID = -42
		repo.allUpdates = append(repo.allUpdates, model.Update{UpdateID: id, Message: &msg})
		repo.history = append(repo.history, model.HistoryEntry{MessageID: id, Text: msg.Text})
	}
	for id := int64(41); id <= 72; id++ {
		msg := model.Message{MessageID: id, Text: "новая реплика"}
		msg.Chat.ID = -42
		if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: id, Message: &msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ai := &fakeAI{decision: model.Decision{Action: "silence"}}
	worker := Worker{Repo: repo, ActionPlans: &fakeActionPlans{}, AI: ai, Telegram: &fakeTelegram{}, Username: "mybot"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(ai.request.NewMessageIDs) != 32 || ai.request.NewMessageIDs[0] != 41 || ai.request.NewMessageIDs[31] != 72 || len(repo.queued) != 0 {
		t.Fatalf("fresh messages were displaced: calls=%d new=%v pending=%d", ai.calls, ai.request.NewMessageIDs, len(repo.queued))
	}
}
