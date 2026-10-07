package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
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
		_, _ = pool.Exec(ctx, `DELETE FROM bot_important_context WHERE chat_id IN ($1, $2)`, chatID, otherChatID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_history WHERE chat_id IN ($1, $2)`, chatID, otherChatID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_updates WHERE update_id = $1`, updateID)
	}()
	now := time.Now().UTC().Truncate(time.Second)
	longText := "Привет,\n как дела?" + strings.Repeat("Я", 400)
	msg := model.Message{MessageID: 7, Date: now.Add(-30 * time.Minute).Unix(), Text: longText, From: &model.User{Username: "ivan"}, ReplyToMessage: &model.Message{MessageID: 6}}
	msg.Chat.ID = chatID
	if err := store.AddIncoming(ctx, msg, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddIncoming(ctx, msg, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddBotReply(ctx, chatID, 0, msg.MessageID, "MyBot", "Нормально!", now); err != nil {
		t.Fatal(err)
	}
	older := model.Message{MessageID: 10, Date: now.Add(-3 * time.Hour).Unix(), Text: "хранится сутки"}
	older.Chat.ID = chatID
	if err := store.AddIncoming(ctx, older, now); err != nil {
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
	for _, item := range []struct {
		message model.Message
		summary string
		kind    string
	}{
		{older, "Старый важный факт", "fact"},
		{older, "Старый важный факт", "fact"},
		{otherTopic, "Факт другой темы", "instruction"},
		{otherChat, "Факт другого чата", "fact"},
	} {
		if err := store.ApplyImportant(ctx, item.message, item.summary, item.kind, nil, now); err != nil {
			t.Fatal(err)
		}
	}
	important, err := store.ImportantContext(ctx, chatID, 0)
	if err != nil || len(important) != 1 || important[0].SourceMessageID != older.MessageID || important[0].Summary != "Старый важный факт" || important[0].Kind != "fact" {
		t.Fatalf("important context crossed chat/topic boundaries or was duplicated: %+v, %v", important, err)
	}
	var storedSource string
	if err := pool.QueryRow(ctx, `SELECT source_body FROM bot_important_context WHERE chat_id = $1 AND source_message_id = $2`, chatID, older.MessageID).Scan(&storedSource); err != nil || storedSource != older.Text {
		t.Fatalf("full source message was not retained: text=%q error=%v", storedSource, err)
	}
	base := now.Add(-48 * time.Hour).Truncate(24 * time.Hour)
	mute := model.Message{MessageID: 11, Date: base.Add(20 * time.Hour).Unix(), Text: "Бот, не пиши"}
	mute.Chat.ID = chatID
	unmute := model.Message{MessageID: 12, Date: base.Add(32 * time.Hour).Unix(), Text: "Бот, пиши"}
	unmute.Chat.ID = chatID
	if err := store.ApplyImportant(ctx, unmute, "Можно снова писать в чат", "instruction", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyImportant(ctx, mute, "Не писать в чат", "instruction", nil, now); err != nil {
		t.Fatal(err)
	}
	important, err = store.ImportantContext(ctx, chatID, 0)
	if err != nil || len(important) != 3 || important[0].SourceMessageID != mute.MessageID || important[1].SourceMessageID != unmute.MessageID || important[0].Kind != "instruction" || important[1].Kind != "instruction" || !important[0].SourceDate.Before(important[1].SourceDate) {
		t.Fatalf("instructions were not loaded in source time order: %+v, %v", important, err)
	}
	prior, err := store.Conversation(ctx, chatID, 0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(prior) != 2 || prior[0].Author != "@ivan" || prior[0].Text != longText || prior[0].MessageID != 7 || prior[0].ReplyToMessageID != 6 || prior[1].Author != "@MyBot" || prior[1].ReplyToMessageID != 7 || !prior[1].Bot {
		t.Fatalf("wrong chat/topic history: %+v", prior)
	}
	repeated, err := store.Conversation(ctx, chatID, 0, now.Add(time.Second))
	if err != nil || len(repeated) != 2 {
		t.Fatalf("duplicate message changed context: %+v, %v", repeated, err)
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
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bot_history WHERE chat_id = $1`, chatID).Scan(&count); err != nil || count != 4 {
		t.Fatalf("expired row not pruned: %d, %v", count, err)
	}
	important, err = store.ImportantContext(ctx, chatID, 0)
	if err != nil || len(important) != 3 || important[2].Summary != "Старый важный факт" {
		t.Fatalf("important context was pruned: %+v, %v", important, err)
	}
	failed := model.Message{MessageID: 13, Date: now.Unix(), Text: "Испорченная замена"}
	failed.Chat.ID = chatID
	if err := store.ApplyImportant(ctx, failed, "Недопустимая запись", "invalid", []int64{mute.MessageID}, now); err == nil {
		t.Fatal("invalid replacement should roll back forgotten context")
	}
	important, err = store.ImportantContext(ctx, chatID, 0)
	if err != nil || len(important) != 3 {
		t.Fatalf("failed replacement deleted context: %+v, %v", important, err)
	}
	replacement := model.Message{MessageID: 14, Date: now.Unix(), Text: "Новая инструкция"}
	replacement.Chat.ID = chatID
	if err := store.ApplyImportant(ctx, replacement, "Писать в чат", "instruction", []int64{mute.MessageID, unmute.MessageID, otherTopic.MessageID, otherChat.MessageID}, now); err != nil {
		t.Fatal(err)
	}
	important, err = store.ImportantContext(ctx, chatID, 0)
	if err != nil || len(important) != 2 || important[0].SourceMessageID != older.MessageID || important[1].SourceMessageID != replacement.MessageID {
		t.Fatalf("replacement did not forget previous instructions: %+v, %v", important, err)
	}
	otherTopicContext, err := store.ImportantContext(ctx, chatID, otherTopic.MessageThreadID)
	if err != nil || len(otherTopicContext) != 1 || otherTopicContext[0].SourceMessageID != otherTopic.MessageID {
		t.Fatalf("replacement deleted another topic: %+v, %v", otherTopicContext, err)
	}
	otherChatContext, err := store.ImportantContext(ctx, otherChatID, 0)
	if err != nil || len(otherChatContext) != 1 || otherChatContext[0].SourceMessageID != otherChat.MessageID {
		t.Fatalf("replacement deleted another chat: %+v, %v", otherChatContext, err)
	}
	cancellation := model.Message{MessageID: 15, Date: now.Unix(), Text: "Старый факт больше не актуален"}
	cancellation.Chat.ID = chatID
	if err := store.ApplyImportant(ctx, cancellation, "", "", []int64{older.MessageID}, now); err != nil {
		t.Fatal(err)
	}
	important, err = store.ImportantContext(ctx, chatID, 0)
	if err != nil || len(important) != 1 || important[0].SourceMessageID != replacement.MessageID {
		t.Fatalf("cancelled fact remained in permanent context: %+v, %v", important, err)
	}
}
