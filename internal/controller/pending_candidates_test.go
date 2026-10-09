package controller

import (
	"context"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestMentionReviewsEarlierUnlinkedMessageWithFullContext(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	first := model.Message{MessageID: 100, Text: "Кто знает, во сколько встреча?"}
	first.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 1, Message: &first}, nil); err != nil {
		t.Fatal(err)
	}
	ai := &fakeAI{decision: model.Decision{Action: "reply", Reply: "В 19:00", ReplyToMessageID: 101}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, ActionPlans: &fakeActionPlans{}, AI: ai, Telegram: tg, Username: "mybot"}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 0 || len(repo.queued) != 0 || repo.consideredUpdates[1] || len(repo.history) != 1 {
		t.Fatalf("unmentioned message triggered AI or was lost: calls=%d considered=%v history=%d", ai.calls, repo.consideredUpdates, len(repo.history))
	}

	mention := model.Message{MessageID: 101, Text: "Что думаешь?"}
	mentionTestMessage(&mention, "mybot")
	mention.Chat.ID = -42
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 2, Message: &mention}, nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(ai.request.NewMessageIDs) != 2 || ai.request.NewMessageIDs[0] != 100 || ai.request.NewMessageIDs[1] != 101 || len(ai.request.TriggerMessageIDs) != 1 || ai.request.TriggerMessageIDs[0] != 101 || len(ai.request.History) != 2 || len(tg.messages) != 1 || tg.messages[0].MessageID != 101 || !repo.consideredUpdates[1] || !repo.consideredUpdates[2] {
		t.Fatalf("mention missed previous context: calls=%d new=%v direct=%v history=%d sent=%+v", ai.calls, ai.request.NewMessageIDs, ai.request.TriggerMessageIDs, len(ai.request.History), tg.messages)
	}
	if err := worker.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(tg.messages) != 1 {
		t.Fatal("decision was repeated without new messages")
	}
}

func TestScheduledRunProcessesMoreThanOneDecisionBatch(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	for i := int64(1); i <= maxDecisionCandidates+3; i++ {
		msg := model.Message{MessageID: i, Text: "обычная реплика"}
		if i == maxDecisionCandidates || i == maxDecisionCandidates+3 {
			mentionTestMessage(&msg, "mybot")
		}
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
		if id == 72 {
			mentionTestMessage(&msg, "mybot")
		}
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
