package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHistoryPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to test PostgreSQL storage")
	}
	ctx := context.Background()
	h, err := openHistoryStore(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != nil {
			h.close()
		}
	}()
	chatID := -time.Now().UnixNano()
	otherChatID := chatID - 1
	defer func() {
		if h != nil {
			_, _ = h.pool.Exec(ctx, `DELETE FROM bot_history WHERE chat_id IN ($1, $2)`, chatID, otherChatID)
		}
	}()
	now := time.Now().UTC().Truncate(time.Second)

	first := message{MessageID: 1, Date: now.Add(-time.Hour).Unix(), From: &user{Username: "ivan"}, Text: "Привет,\n как   дела?"}
	first.Chat.ID = chatID
	if err := h.addIncoming(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	if err := h.addIncoming(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	if err := h.addBotReply(ctx, chatID, 0, "MyBot", "Нормально!", now); err != nil {
		t.Fatal(err)
	}
	otherTopic := message{MessageID: 2, MessageThreadID: 99, Date: now.Unix(), Text: "секретная тема"}
	otherTopic.Chat.ID = chatID
	if err := h.addIncoming(ctx, otherTopic, now); err != nil {
		t.Fatal(err)
	}
	otherChat := message{MessageID: 3, Date: now.Unix(), Text: "другой чат"}
	otherChat.Chat.ID = otherChatID
	if err := h.addIncoming(ctx, otherChat, now); err != nil {
		t.Fatal(err)
	}
	old := message{MessageID: 4, Date: now.Add(-25 * time.Hour).Unix(), Text: "устарело"}
	old.Chat.ID = chatID
	if err := h.addIncoming(ctx, old, now); err != nil {
		t.Fatal(err)
	}

	// Reopen the pool to verify that the conversation survives a restart.
	h.close()
	h = nil
	h, err = openHistoryStore(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.conversation(ctx, chatID, 0, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "@ivan: Привет, как дела?") || !strings.Contains(got, "@MyBot: Нормально!") || strings.Contains(got, "секретная тема") || strings.Contains(got, "другой чат") || strings.Contains(got, "устарело") {
		t.Fatalf("unexpected conversation: %q", got)
	}
	if strings.Index(got, "@ivan") > strings.Index(got, "@MyBot") {
		t.Fatalf("conversation is not chronological: %q", got)
	}
	var count int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM bot_history WHERE chat_id = $1`, chatID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 { // Duplicate and expired incoming messages were not inserted.
		t.Fatalf("got %d stored messages, want 3", count)
	}
	_, err = h.pool.Exec(ctx, `INSERT INTO bot_history (chat_id, thread_id, sent_at, author, body) VALUES ($1, 0, $2, 'old', 'expired')`, chatID, now.Add(-25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.prune(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM bot_history WHERE chat_id = $1`, chatID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expired message was not pruned: %d rows", count)
	}
}

func TestHistoryShortensAndLimitsContext(t *testing.T) {
	text := compactText(strings.Repeat("Я", 500), maxSavedRunes)
	if len([]rune(text)) != maxSavedRunes || !strings.HasSuffix(text, "…") {
		t.Fatalf("message was not shortened: %d runes", len([]rune(text)))
	}
	now := time.Now()
	var entries []historyEntry
	for i := 0; i < 85; i++ {
		entries = append(entries, historyEntry{Date: now.Add(-time.Duration(i) * time.Minute), Author: "Участник", Text: text})
	}
	got := formatConversation(entries)
	if len([]rune(got)) > maxContextRunes || strings.Count(got, "Участник:") > maxContextMessages {
		t.Fatalf("context limits exceeded: %d runes", len([]rune(got)))
	}
}
