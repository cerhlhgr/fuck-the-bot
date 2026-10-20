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

func TestJudgeModeAndCasePersistence(t *testing.T) {
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
	chatID := -time.Now().UnixNano()
	updateID := -chatID
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_cases WHERE update_id = $1`, updateID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_modes WHERE chat_id = $1`, chatID)
	}()
	store := postgres.New(pool)
	active, err := store.JudgeMode(ctx, chatID)
	if err != nil || active {
		t.Fatalf("unexpected initial mode: %t, %v", active, err)
	}
	if err := store.SetJudgeMode(ctx, chatID, true); err != nil {
		t.Fatal(err)
	}
	active, err = postgres.New(pool).JudgeMode(ctx, chatID)
	if err != nil || !active {
		t.Fatalf("mode was not persisted: %t, %v", active, err)
	}
	caseRecord := model.JudgeCase{UpdateID: updateID, ChatID: chatID, MessageID: 7, UserID: 42, Article: 2}
	stored, err := store.SaveJudgeCase(ctx, caseRecord)
	if err != nil || stored.Article != 2 || stored.Phase != 0 {
		t.Fatalf("wrong saved case: %+v, %v", stored, err)
	}
	if err := store.AdvanceJudgeCase(ctx, updateID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordJudgeForward(ctx, updateID, 99); err != nil {
		t.Fatal(err)
	}
	stored, found, err := postgres.New(pool).JudgeCase(ctx, updateID)
	if err != nil || !found || stored.Phase != 2 || stored.ForwardedMessageID != 99 {
		t.Fatalf("case phase was not persisted: %+v, %t, %v", stored, found, err)
	}
	if err := store.SetJudgeMode(ctx, chatID, false); err != nil {
		t.Fatal(err)
	}
	active, err = store.JudgeMode(ctx, chatID)
	if err != nil || active {
		t.Fatalf("mode was not disabled: %t, %v", active, err)
	}
}
