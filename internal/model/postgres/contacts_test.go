package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/model/postgres"
)

func TestContactsAreRememberedPerChatAndUpdated(t *testing.T) {
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
		_, _ = pool.Exec(ctx, `DELETE FROM bot_contacts WHERE chat_id IN ($1, $2)`, chatID, otherChatID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_history WHERE chat_id IN ($1, $2)`, chatID, otherChatID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_updates WHERE update_id = $1`, updateID)
	}()
	now := time.Now().UTC().Truncate(time.Second)
	msg := model.Message{MessageID: 1, Date: now.Add(-time.Minute).Unix(), From: &model.User{ID: 123, Username: "oldname", FirstName: "Сергей"}, Text: "Привет"}
	msg.Chat.ID = chatID
	item := model.Update{UpdateID: updateID, Message: &msg}
	raw, _ := json.Marshal(item)
	if err := store.EnqueueUpdate(ctx, item, raw); err != nil {
		t.Fatal(err)
	}
	contacts, err := store.FindContacts(ctx, chatID, "@OLDNAME")
	if err != nil || len(contacts) != 1 || contacts[0].UserID != 123 || contacts[0].Name != "Сергей" || contacts[0].Link != "tg://user?id=123" {
		t.Fatalf("webhook did not save contact: %+v, %v", contacts, err)
	}
	msg.MessageID = 2
	msg.Date = now.Unix()
	msg.From.Username = "newname"
	msg.From.FirstName = "Серёжа"
	if err := store.AddIncoming(ctx, msg, now); err != nil {
		t.Fatal(err)
	}
	msg.MessageID = 3
	msg.Date = now.Add(-2 * time.Minute).Unix()
	msg.From.Username = "oldname"
	msg.From.FirstName = "Сергей"
	if err := store.AddIncoming(ctx, msg, now); err != nil {
		t.Fatal(err)
	}
	contacts, err = store.FindContacts(ctx, chatID, "сережа")
	if err != nil || len(contacts) != 1 || contacts[0].Username != "newname" {
		t.Fatalf("newer contact data lost: %+v, %v", contacts, err)
	}
	other := model.Message{MessageID: 4, Date: now.Unix(), From: &model.User{ID: 456, FirstName: "Серёжа"}}
	other.Chat.ID = otherChatID
	if err := store.AddIncoming(ctx, other, now); err != nil {
		t.Fatal(err)
	}
	contacts, err = store.FindContacts(ctx, otherChatID, "СЕРЁЖА")
	if err != nil || len(contacts) != 1 || contacts[0].UserID != 456 || contacts[0].Link != "tg://user?id=456" {
		t.Fatalf("chat contacts mixed or id-only link missing: %+v, %v", contacts, err)
	}
	bot := model.Message{MessageID: 5, Date: now.Unix(), From: &model.User{ID: 789, IsBot: true, FirstName: "Бот"}}
	bot.Chat.ID = chatID
	if err := store.AddIncoming(ctx, bot, now); err != nil {
		t.Fatal(err)
	}
	contacts, err = store.FindContacts(ctx, chatID, "Бот")
	if err != nil || len(contacts) != 0 {
		t.Fatalf("bot was stored as human contact: %+v, %v", contacts, err)
	}
}
