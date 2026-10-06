package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	historyLifetime    = 24 * time.Hour
	maxSavedRunes      = 320
	maxContextMessages = 80
	maxContextRunes    = 12000
	dbTimeout          = 5 * time.Second
)

type historyEntry struct {
	Date   time.Time
	Author string
	Text   string
}

type historyStore struct {
	pool *pgxpool.Pool
}

func openHistoryStore(ctx context.Context, databaseURL string) (*historyStore, error) {
	setupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(setupCtx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	h := &historyStore{pool: pool}
	if err := pool.Ping(setupCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if err := h.migrate(setupCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create history table: %w", err)
	}
	if err := h.prune(setupCtx, time.Now()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("prune history: %w", err)
	}
	return h, nil
}

func (h *historyStore) close() { h.pool.Close() }

func (h *historyStore) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS bot_history (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			chat_id BIGINT NOT NULL,
			thread_id BIGINT NOT NULL DEFAULT 0,
			message_id BIGINT,
			sent_at TIMESTAMPTZ NOT NULL,
			author TEXT NOT NULL,
			body TEXT NOT NULL,
			bot BOOLEAN NOT NULL DEFAULT FALSE,
			CONSTRAINT bot_history_chat_message_unique UNIQUE (chat_id, message_id)
		)`,
		`CREATE INDEX IF NOT EXISTS bot_history_context_idx
			ON bot_history (chat_id, thread_id, sent_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS bot_history_expiry_idx ON bot_history (sent_at)`,
	}
	for _, statement := range statements {
		if _, err := h.pool.Exec(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (h *historyStore) addIncoming(ctx context.Context, msg message, now time.Time) error {
	content := msg.Text
	if content == "" {
		content = msg.Caption
	}
	content = compactText(content, maxSavedRunes)
	if content == "" {
		return nil
	}
	date := time.Unix(msg.Date, 0)
	if msg.Date == 0 || date.After(now) {
		date = now
	}
	if date.Before(now.Add(-historyLifetime)) {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := h.pool.Exec(queryCtx, `
		INSERT INTO bot_history (chat_id, thread_id, message_id, sent_at, author, body, bot)
		VALUES ($1, $2, $3, $4, $5, $6, FALSE)
		ON CONFLICT (chat_id, message_id) DO NOTHING`,
		msg.Chat.ID, msg.MessageThreadID, msg.MessageID, date, authorName(msg.From), content)
	return err
}

func (h *historyStore) addBotReply(ctx context.Context, chatID, threadID int64, username, answer string, now time.Time) error {
	content := compactText(answer, maxSavedRunes)
	if content == "" {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := h.pool.Exec(queryCtx, `
		INSERT INTO bot_history (chat_id, thread_id, sent_at, author, body, bot)
		VALUES ($1, $2, $3, $4, $5, TRUE)`,
		chatID, threadID, now, "@"+username, content)
	return err
}

func (h *historyStore) conversation(ctx context.Context, chatID, threadID int64, now time.Time) (string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := h.pool.Query(queryCtx, `
		SELECT sent_at, author, body
		FROM bot_history
		WHERE chat_id = $1 AND thread_id = $2 AND sent_at >= $3 AND sent_at <= $4
		ORDER BY sent_at DESC, id DESC
		LIMIT $5`, chatID, threadID, now.Add(-historyLifetime), now, maxContextMessages)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var entries []historyEntry
	for rows.Next() {
		var entry historyEntry
		if err := rows.Scan(&entry.Date, &entry.Author, &entry.Text); err != nil {
			return "", err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return formatConversation(entries), nil
}

// entries are ordered newest first by the database query.
func formatConversation(entries []historyEntry) string {
	var lines []string
	used := 0
	for _, entry := range entries {
		line := entry.Date.Local().Format("02.01 15:04") + " " + entry.Author + ": " + entry.Text
		size := len([]rune(line))
		if len(lines) > 0 {
			size++ // Newline between conversation entries.
		}
		if used+size > maxContextRunes {
			break
		}
		lines = append(lines, line)
		used += size
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func (h *historyStore) prune(ctx context.Context, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := h.pool.Exec(queryCtx, `DELETE FROM bot_history WHERE sent_at < $1`, now.Add(-historyLifetime))
	return err
}

func authorName(u *user) string {
	if u == nil {
		return "Участник"
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return compactText(name, 80)
	}
	return "user#" + strconv.FormatInt(u.ID, 10)
}

func compactText(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-1]) + "…"
}
