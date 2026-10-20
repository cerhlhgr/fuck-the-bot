package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/model/postgres"
)

func TestUnbanVotePersistence(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to test PostgreSQL")
	}
	ctx := context.Background()
	pool, err := postgres.OpenPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.New(pool)
	id := time.Now().UnixNano()
	chatID := -id
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM bot_judge_unban_votes WHERE chat_id = $1`, chatID) }()
	vote, err := store.CreateUnbanVote(ctx, model.UnbanVote{RequestUpdateID: id, ChatID: chatID, ThreadID: 7, RequestMessageID: 10, TargetUserID: 42, TargetName: "@vasya", TargetStatus: "restricted"})
	if err != nil || vote.State != "created" {
		t.Fatalf("create vote: %+v, %v", vote, err)
	}
	if _, err := store.CreateUnbanVote(ctx, model.UnbanVote{RequestUpdateID: id + 1, ChatID: chatID, TargetUserID: 42, TargetName: "@vasya", TargetStatus: "restricted"}); err != model.ErrUnbanVoteActive {
		t.Fatalf("duplicate active vote accepted: %v", err)
	}
	closesAt := time.Now().Add(-time.Second)
	if err := store.SetUnbanVotePoll(ctx, id, 50, closesAt); err != nil {
		t.Fatal(err)
	}
	due, err := store.DueUnbanVotes(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range due {
		if item.RequestUpdateID == id {
			found = true
			if item.PollMessageID != 50 || item.State != "open" || item.TargetUserID != 42 {
				t.Fatalf("wrong due vote: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("due vote missing")
	}
	if err := store.CloseUnbanVote(ctx, id, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUnbanVoteApplied(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteUnbanVote(ctx, id); err != nil {
		t.Fatal(err)
	}
	newVote, err := store.CreateUnbanVote(ctx, model.UnbanVote{RequestUpdateID: id + 2, ChatID: chatID, ThreadID: 7, RequestMessageID: 11, TargetUserID: 42, TargetName: "@vasya", TargetStatus: "restricted"})
	if err != nil || newVote.State != "created" {
		t.Fatalf("completed vote still blocks a new vote: %+v, %v", newVote, err)
	}
}
