package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"fuck-the-bot/internal/model"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const dbTimeout = 5 * time.Second

type Store struct {
	pool *pgxpool.Pool
}

var _ model.Repository = (*Store)(nil)

func OpenPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	setupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(setupCtx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(setupCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return pool, nil
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) EnqueueUpdate(ctx context.Context, item model.Update, body []byte) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		INSERT INTO bot_updates (update_id, payload) VALUES ($1, $2::jsonb)
		ON CONFLICT (update_id) DO NOTHING`, item.UpdateID, string(body))
	return err
}

func (s *Store) NextUpdate(ctx context.Context) (model.Update, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var item model.Update
	var raw string
	err := s.pool.QueryRow(queryCtx, `
		SELECT update_id, payload::text FROM bot_updates
		WHERE processed_at IS NULL ORDER BY update_id LIMIT 1`).Scan(&item.UpdateID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Update{}, model.ErrNoUpdates
	}
	if err != nil {
		return model.Update{}, err
	}
	id := item.UpdateID
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return model.Update{}, fmt.Errorf("decode queued update %d: %w", id, err)
	}
	if item.UpdateID != id {
		return model.Update{}, fmt.Errorf("queued update %d has mismatched payload ID", id)
	}
	return item, nil
}

func (s *Store) MarkUpdateProcessed(ctx context.Context, updateID int64) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `UPDATE bot_updates SET processed_at = now() WHERE update_id = $1`, updateID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("queued update %d not found", updateID)
	}
	return nil
}

func (s *Store) Prune(ctx context.Context, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	if _, err := s.pool.Exec(queryCtx, `DELETE FROM bot_history WHERE sent_at < $1`, now.Add(-model.HistoryLifetime)); err != nil {
		return err
	}
	_, err := s.pool.Exec(queryCtx, `DELETE FROM bot_updates WHERE processed_at < $1`, now.Add(-48*time.Hour))
	return err
}

func (s *Store) AddIncoming(ctx context.Context, msg model.Message, now time.Time) error {
	content := msg.Text
	if content == "" {
		content = msg.Caption
	}
	if content == "" {
		content = "[сообщение без текста]"
	}
	date := time.Unix(msg.Date, 0)
	if msg.Date == 0 || date.After(now) {
		date = now
	}
	if date.Before(now.Add(-model.HistoryLifetime)) {
		return nil
	}
	replyToMessageID := int64(0)
	if msg.ReplyToMessage != nil {
		replyToMessageID = msg.ReplyToMessage.MessageID
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		INSERT INTO bot_history (chat_id, thread_id, message_id, reply_to_message_id, sent_at, author, body, bot)
		VALUES ($1, $2, $3, NULLIF($4::bigint, 0), $5, $6, $7, FALSE)
		ON CONFLICT (chat_id, message_id) DO NOTHING`,
		msg.Chat.ID, msg.MessageThreadID, msg.MessageID, replyToMessageID, date, model.AuthorName(msg.From), content)
	return err
}

func (s *Store) AddBotReply(ctx context.Context, chatID, threadID, replyToMessageID int64, username, answer string, now time.Time) error {
	if answer == "" {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		INSERT INTO bot_history (chat_id, thread_id, reply_to_message_id, sent_at, author, body, bot)
		VALUES ($1, $2, NULLIF($3::bigint, 0), $4, $5, $6, TRUE)`,
		chatID, threadID, replyToMessageID, now, "@"+username, answer)
	return err
}

func (s *Store) Conversation(ctx context.Context, chatID, threadID int64, now time.Time) ([]model.HistoryEntry, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(queryCtx, `
		SELECT sent_at, COALESCE(message_id, 0), COALESCE(reply_to_message_id, 0), author, body, bot
		FROM bot_history
		WHERE chat_id = $1 AND thread_id = $2 AND sent_at >= $3 AND sent_at <= $4
		ORDER BY sent_at ASC, id ASC`, chatID, threadID, now.Add(-model.ContextLifetime), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []model.HistoryEntry
	for rows.Next() {
		var entry model.HistoryEntry
		if err := rows.Scan(&entry.Date, &entry.MessageID, &entry.ReplyToMessageID, &entry.Author, &entry.Text, &entry.Bot); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
