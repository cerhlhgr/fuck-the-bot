package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/model/postgres"
)

func TestPostgresHistoryAndInbox(t *testing.T) {
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
	otherChatID := chatID - 1
	updateID := -chatID
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bot_history WHERE chat_id IN ($1, $2)`, chatID, otherChatID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_updates WHERE update_id = $1`, updateID)
	}()
	now := time.Now().UTC().Truncate(time.Second)
	msg := model.Message{MessageID: 7, Date: now.Add(-time.Hour).Unix(), Text: "Привет,\n как дела?", From: &model.User{Username: "ivan"}}
	msg.Chat.ID = chatID
	if err := store.AddIncoming(ctx, msg, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddIncoming(ctx, msg, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddBotReply(ctx, chatID, 0, "MyBot", "Нормально!", now); err != nil {
		t.Fatal(err)
	}
	otherTopic := model.Message{MessageID: 8, MessageThreadID: 99, Date: now.Unix(), Text: "другая тема"}
	otherTopic.Chat.ID = chatID
	if err := store.AddIncoming(ctx, otherTopic, now); err != nil {
		t.Fatal(err)
	}
	otherChat := model.Message{MessageID: 9, Date: now.Unix(), Text: "другой чат"}
	otherChat.Chat.ID = otherChatID
	if err := store.AddIncoming(ctx, otherChat, now); err != nil {
		t.Fatal(err)
	}
	prior, err := store.Conversation(ctx, chatID, 0, 999, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(prior) != 2 || prior[0].Author != "@MyBot" || prior[1].Author != "@ivan" || prior[1].Text != "Привет, как дела?" {
		t.Fatalf("wrong chat/topic history: %+v", prior)
	}
	withoutCurrent, err := store.Conversation(ctx, chatID, 0, msg.MessageID, now.Add(time.Second))
	if err != nil || len(withoutCurrent) != 1 || withoutCurrent[0].Author != "@MyBot" {
		t.Fatalf("current message leaked into retry context: %+v, %v", withoutCurrent, err)
	}
	item := model.Update{UpdateID: updateID, Message: &msg}
	raw, _ := json.Marshal(item)
	if err := store.EnqueueUpdate(ctx, item, raw); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueUpdate(ctx, item, raw); err != nil {
		t.Fatal(err)
	}
	next, err := store.NextUpdate(ctx)
	if err != nil || next.UpdateID != updateID {
		t.Fatalf("queued update = %+v, %v", next, err)
	}
	if err := store.MarkUpdateProcessed(ctx, updateID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextUpdate(ctx); !errors.Is(err, model.ErrNoUpdates) {
		t.Fatalf("processed update still pending: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_updates WHERE update_id = $1`, updateID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate webhook delivery created %d rows: %v", count, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO bot_history (chat_id, thread_id, sent_at, author, body) VALUES ($1, 0, $2, 'old', 'expired')`, chatID, now.Add(-25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history WHERE chat_id = $1`, chatID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("expired row not pruned: %d, %v", count, err)
	}
}
