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

func TestPostgresActionPlanPersistsProgress(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to test PostgreSQL")
	}
	ctx := context.Background()
	pool, err := postgres.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.New(pool)
	chatID := -time.Now().UnixNano()
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM bot_action_plans WHERE chat_id = $1`, chatID) }()
	firstUpdateID := -chatID
	plan := model.ActionPlan{UpdateIDs: []int64{firstUpdateID, firstUpdateID + 1}, Actions: []model.Decision{
		{Action: "reply", ReplyToMessageID: 10, Reply: "Первый ответ"},
		{Action: "poll", ReplyToMessageID: 11, Poll: &model.Poll{Question: "Куда?", Options: []string{"Домой", "В кино"}}},
	}}
	if loaded, found, err := store.LoadActionPlan(ctx, chatID, 7, firstUpdateID); err != nil || found || len(loaded.Actions) != 0 {
		t.Fatalf("unexpected existing plan: %+v, %t, %v", loaded, found, err)
	}
	saved, err := store.SaveActionPlan(ctx, chatID, 7, firstUpdateID, plan)
	if err != nil || len(saved.Actions) != 2 || saved.Actions[1].Poll == nil || len(saved.UpdateIDs) != 2 {
		t.Fatalf("save plan: %+v, %v", saved, err)
	}
	other := model.ActionPlan{UpdateIDs: []int64{999}, Actions: []model.Decision{{Action: "message", Reply: "Дубликат"}}}
	saved, err = store.SaveActionPlan(ctx, chatID, 7, firstUpdateID, other)
	if err != nil || len(saved.Actions) != 2 || saved.UpdateIDs[0] != firstUpdateID {
		t.Fatalf("plan changed after duplicate save: %+v, %v", saved, err)
	}
	if err := store.MarkActionCompleted(ctx, chatID, 7, firstUpdateID, 1); err == nil {
		t.Fatal("advanced plan without completing first action")
	}
	if err := store.MarkActionCompleted(ctx, chatID, 7, firstUpdateID, 0); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := store.LoadActionPlan(ctx, chatID, 7, firstUpdateID)
	if err != nil || !found || loaded.Completed != 1 {
		t.Fatalf("saved progress: %+v, %t, %v", loaded, found, err)
	}
	if err := store.MarkActionCompleted(ctx, chatID, 7, firstUpdateID, 1); err != nil {
		t.Fatal(err)
	}
	loaded, found, err = store.LoadActionPlan(ctx, chatID, 7, firstUpdateID)
	if err != nil || !found || loaded.Completed != 2 {
		t.Fatalf("completed plan: %+v, %t, %v", loaded, found, err)
	}
}
