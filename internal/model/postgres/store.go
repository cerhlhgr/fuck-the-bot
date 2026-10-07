package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"fuck-the-bot/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const dbTimeout = 5 * time.Second
const decisionLockKey int64 = 0x6465636973696f6e

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

func (s *Store) TryDecisionLock(ctx context.Context) (func() error, bool, error) {
	return s.tryLock(ctx, decisionLockKey)
}

func (s *Store) tryLock(ctx context.Context, key int64) (func() error, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	conn, err := s.pool.Acquire(queryCtx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRow(queryCtx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	return func() error {
		defer conn.Release()
		unlockCtx, stop := context.WithTimeout(context.Background(), dbTimeout)
		defer stop()
		var unlocked bool
		if err := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, key).Scan(&unlocked); err != nil {
			return err
		}
		if !unlocked {
			return errors.New("decision advisory lock was not held")
		}
		return nil
	}, true, nil
}

func (s *Store) EnqueueUpdate(ctx context.Context, item model.Update, body []byte) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tx, err := s.pool.Begin(queryCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(queryCtx)
	tag, err := tx.Exec(queryCtx, `
		INSERT INTO bot_updates (update_id, payload) VALUES ($1, $2::jsonb)
		ON CONFLICT (update_id) DO NOTHING`, item.UpdateID, string(body))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 && item.Message != nil {
		if err := insertIncoming(queryCtx, tx, *item.Message, time.Now(), false); err != nil {
			return err
		}
	}
	return tx.Commit(queryCtx)
}

func (s *Store) PendingUpdates(ctx context.Context, afterID int64, limit int) ([]model.Update, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(queryCtx, `
		SELECT update_id, payload::text FROM bot_updates
		WHERE processed_at IS NULL AND update_id > $1
		ORDER BY update_id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var updates []model.Update
	for rows.Next() {
		var update model.Update
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &update); err != nil {
			return nil, fmt.Errorf("decode queued update %d: %w", id, err)
		}
		if update.UpdateID != id {
			return nil, fmt.Errorf("queued update %d has mismatched payload ID", id)
		}
		updates = append(updates, update)
	}
	return updates, rows.Err()
}

func (s *Store) MarkUpdatesProcessed(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `UPDATE bot_updates SET processed_at = now() WHERE update_id = ANY($1) AND processed_at IS NULL`, ids)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != int64(len(ids)) {
		return fmt.Errorf("marked %d of %d updates processed", tag.RowsAffected(), len(ids))
	}
	return nil
}

func (s *Store) Prune(ctx context.Context, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	if _, err := s.pool.Exec(queryCtx, `DELETE FROM bot_history WHERE sent_at < $1`, now.Add(-model.HistoryLifetime)); err != nil {
		return err
	}
	if _, err := s.pool.Exec(queryCtx, `DELETE FROM bot_updates WHERE processed_at < $1`, now.Add(-48*time.Hour)); err != nil {
		return err
	}
	return s.pruneMusic(ctx, now)
}

func (s *Store) AddIncoming(ctx context.Context, msg model.Message, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	return insertIncoming(queryCtx, s.pool, msg, now, msg.PhotoDescription != "")
}

type incomingExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertIncoming(ctx context.Context, db incomingExecer, msg model.Message, now time.Time, updateDescription bool) error {
	content := model.MessageHistoryText(msg)
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
	_, err := db.Exec(ctx, `
		INSERT INTO bot_history (chat_id, thread_id, message_id, reply_to_message_id, sent_at, author, body, bot)
		VALUES ($1, $2, $3, NULLIF($4::bigint, 0), $5, $6, $7, FALSE)
		ON CONFLICT (chat_id, message_id) DO UPDATE SET body = EXCLUDED.body
		WHERE $8::boolean`,
		msg.Chat.ID, msg.MessageThreadID, msg.MessageID, replyToMessageID, date, model.AuthorName(msg.From), content, updateDescription)
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

func (s *Store) ApplyImportant(ctx context.Context, msg model.Message, summary, kind string, forgetIDs []int64, now time.Time) error {
	var updates []model.ImportantUpdate
	if summary != "" {
		updates = append(updates, model.ImportantUpdate{SourceMessageID: msg.MessageID, Summary: summary, Kind: kind})
	}
	return s.ApplyImportantBatch(ctx, []model.Message{msg}, updates, forgetIDs, now)
}

func (s *Store) ApplyImportantBatch(ctx context.Context, messages []model.Message, updates []model.ImportantUpdate, forgetIDs []int64, now time.Time) error {
	if len(updates) == 0 && len(forgetIDs) == 0 {
		return nil
	}
	if len(messages) == 0 {
		return errors.New("important context update has no source messages")
	}
	first := messages[0]
	byID := make(map[int64]model.Message, len(messages))
	for _, msg := range messages {
		if msg.Chat.ID != first.Chat.ID || msg.MessageThreadID != first.MessageThreadID {
			return errors.New("important context update spans multiple dialogues")
		}
		byID[msg.MessageID] = msg
	}
	for _, update := range updates {
		if _, ok := byID[update.SourceMessageID]; !ok {
			return fmt.Errorf("important context source message %d is not in the batch", update.SourceMessageID)
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tx, err := s.pool.Begin(queryCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(queryCtx)
	if len(forgetIDs) > 0 {
		if _, err := tx.Exec(queryCtx, `
			DELETE FROM bot_important_context
			WHERE chat_id = $1 AND thread_id = $2 AND source_message_id = ANY($3)`,
			first.Chat.ID, first.MessageThreadID, forgetIDs); err != nil {
			return err
		}
	}
	for _, update := range updates {
		msg := byID[update.SourceMessageID]
		date := time.Unix(msg.Date, 0)
		if msg.Date == 0 || date.After(now) {
			date = now
		}
		if _, err := tx.Exec(queryCtx, `
		INSERT INTO bot_important_context
		    (chat_id, thread_id, source_message_id, source_sent_at, author, source_body, summary, kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (chat_id, source_message_id) DO NOTHING`,
			msg.Chat.ID, msg.MessageThreadID, msg.MessageID, date, model.AuthorName(msg.From), model.MessageHistoryText(msg), update.Summary, update.Kind); err != nil {
			return err
		}
	}
	return tx.Commit(queryCtx)
}

func (s *Store) ImportantContext(ctx context.Context, chatID, threadID int64) ([]model.ImportantEntry, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(queryCtx, `
		SELECT source_message_id, source_sent_at, author, summary, kind
		FROM bot_important_context
		WHERE chat_id = $1 AND thread_id = $2
		ORDER BY source_sent_at ASC, source_message_id ASC, id ASC`, chatID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []model.ImportantEntry
	for rows.Next() {
		var entry model.ImportantEntry
		if err := rows.Scan(&entry.SourceMessageID, &entry.SourceDate, &entry.Author, &entry.Summary, &entry.Kind); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}
