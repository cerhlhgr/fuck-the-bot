package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"fuck-the-bot/internal/model"

	"github.com/jackc/pgx/v5"
)

const musicDeliveryLockKey int64 = 0x6d75736963626f74

var _ model.MusicStore = (*Store)(nil)

func (s *Store) TryMusicDeliveryLock(ctx context.Context) (func() error, bool, error) {
	return s.tryLock(ctx, musicDeliveryLockKey)
}

func (s *Store) CreateMusicTask(ctx context.Context, tokenHash string, msg model.Message, request model.MusicRequest) (bool, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return false, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `
		INSERT INTO bot_music_tasks (token_hash, chat_id, thread_id, source_message_id, request)
		VALUES ($1, $2, $3, $4, $5::jsonb)
		ON CONFLICT (chat_id, source_message_id) DO UPDATE SET
			token_hash = EXCLUDED.token_hash,
			request = EXCLUDED.request,
			status = 'pending',
			error = '',
			updated_at = now()
		WHERE bot_music_tasks.status = 'failed' AND bot_music_tasks.task_id IS NULL`,
		tokenHash, msg.Chat.ID, msg.MessageThreadID, msg.MessageID, string(data))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) BindMusicTask(ctx context.Context, tokenHash, taskID string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `
		UPDATE bot_music_tasks
		SET task_id = $2, status = CASE WHEN status = 'pending' THEN 'submitted' ELSE status END, updated_at = now()
		WHERE token_hash = $1 AND (task_id IS NULL OR task_id = $2)`, tokenHash, taskID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return model.ErrMusicTaskMismatch
	}
	return nil
}

func (s *Store) FailMusicTask(ctx context.Context, tokenHash, reason string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		UPDATE bot_music_tasks SET status = 'failed', error = $2, updated_at = now()
		WHERE token_hash = $1 AND status <> 'done'`, tokenHash, reason)
	return err
}

func (s *Store) ApplyMusicCallback(ctx context.Context, tokenHash string, callback model.MusicCallback) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tx, err := s.pool.Begin(queryCtx)
	if err != nil {
		return err
	}
	defer tx.Rollback(queryCtx)
	var id int64
	var taskID, status string
	err = tx.QueryRow(queryCtx, `
		SELECT id, COALESCE(task_id, ''), status FROM bot_music_tasks
		WHERE token_hash = $1 FOR UPDATE`, tokenHash).Scan(&id, &taskID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ErrMusicTaskNotFound
	}
	if err != nil {
		return err
	}
	if taskID != "" && taskID != callback.TaskID {
		return model.ErrMusicTaskMismatch
	}
	if taskID == "" {
		if _, err := tx.Exec(queryCtx, `UPDATE bot_music_tasks SET task_id = $2 WHERE id = $1`, id, callback.TaskID); err != nil {
			return err
		}
	}
	if callback.Code != 200 || callback.Stage == "error" {
		if status != "done" {
			if _, err := tx.Exec(queryCtx, `
				UPDATE bot_music_tasks SET status = 'failed', error = $2, updated_at = now()
				WHERE id = $1`, id, callback.Error); err != nil {
				return err
			}
		}
		return tx.Commit(queryCtx)
	}
	if callback.Stage != "complete" {
		return tx.Commit(queryCtx)
	}
	for _, track := range callback.Tracks {
		if track.AudioID == "" || track.AudioURL == "" {
			continue
		}
		if _, err := tx.Exec(queryCtx, `
			INSERT INTO bot_music_tracks (music_task_id, audio_id, audio_url, title)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (music_task_id, audio_id) DO NOTHING`,
			id, track.AudioID, track.AudioURL, track.Title); err != nil {
			return err
		}
	}
	var count int
	if err := tx.QueryRow(queryCtx, `SELECT COUNT(*) FROM bot_music_tracks WHERE music_task_id = $1`, id).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err = tx.Exec(queryCtx, `UPDATE bot_music_tasks SET status = 'failed', error = 'complete callback contained no usable tracks', updated_at = now() WHERE id = $1`, id)
	} else {
		_, err = tx.Exec(queryCtx, `UPDATE bot_music_tasks SET status = 'done', error = '', updated_at = now() WHERE id = $1`, id)
	}
	if err != nil {
		return err
	}
	return tx.Commit(queryCtx)
}

func (s *Store) PendingMusicTracks(ctx context.Context, limit int) ([]model.MusicTrack, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(queryCtx, `
		SELECT t.id, g.chat_id, g.thread_id, g.source_message_id, t.audio_id, t.audio_url, t.title
		FROM bot_music_tracks t JOIN bot_music_tasks g ON g.id = t.music_task_id
		WHERE g.status = 'done' AND t.delivered_at IS NULL AND t.next_attempt_at <= now()
		ORDER BY t.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tracks []model.MusicTrack
	for rows.Next() {
		var track model.MusicTrack
		if err := rows.Scan(&track.ID, &track.ChatID, &track.ThreadID, &track.ReplyToMessageID, &track.AudioID, &track.AudioURL, &track.Title); err != nil {
			return nil, err
		}
		tracks = append(tracks, track)
	}
	return tracks, rows.Err()
}

func (s *Store) MarkMusicTrackDelivered(ctx context.Context, id int64) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `UPDATE bot_music_tracks SET delivered_at = now(), last_error = '' WHERE id = $1 AND delivered_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("music track %d was already delivered or missing", id)
	}
	return nil
}

func (s *Store) RetryMusicTrack(ctx context.Context, id int64, reason string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		UPDATE bot_music_tracks SET attempts = attempts + 1,
		    next_attempt_at = now() + INTERVAL '30 seconds', last_error = $2
		WHERE id = $1 AND delivered_at IS NULL`, id, reason)
	return err
}

func (s *Store) pruneMusic(ctx context.Context, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		DELETE FROM bot_music_tasks
		WHERE status IN ('done', 'failed') AND updated_at < $1
		AND NOT EXISTS (SELECT 1 FROM bot_music_tracks t WHERE t.music_task_id = bot_music_tasks.id AND t.delivered_at IS NULL)`, now.Add(-30*24*time.Hour))
	return err
}
