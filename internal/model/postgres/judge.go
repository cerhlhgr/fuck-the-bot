package postgres

import (
	"context"
	"errors"
	"time"

	"fuck-the-bot/internal/model"

	"github.com/jackc/pgx/v5"
)

var _ model.JudgeStore = (*Store)(nil)

func (s *Store) JudgeMode(ctx context.Context, chatID int64) (bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var enabled bool
	err := s.pool.QueryRow(queryCtx, `SELECT enabled FROM bot_judge_modes WHERE chat_id = $1`, chatID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return enabled, err
}

func (s *Store) SetJudgeMode(ctx context.Context, chatID int64, enabled bool) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		INSERT INTO bot_judge_modes (chat_id, enabled) VALUES ($1, $2)
		ON CONFLICT (chat_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`, chatID, enabled)
	return err
}

func (s *Store) JudgeCase(ctx context.Context, updateID int64) (model.JudgeCase, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var c model.JudgeCase
	err := s.pool.QueryRow(queryCtx, `
		SELECT update_id, chat_id, message_id, user_id, article, warning_only, phase, forwarded_message_id
		FROM bot_judge_cases WHERE update_id = $1`, updateID).Scan(
		&c.UpdateID, &c.ChatID, &c.MessageID, &c.UserID, &c.Article, &c.WarningOnly, &c.Phase, &c.ForwardedMessageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.JudgeCase{}, false, nil
	}
	return c, err == nil, err
}

func (s *Store) SaveJudgeCase(ctx context.Context, c model.JudgeCase) (model.JudgeCase, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		INSERT INTO bot_judge_cases (update_id, chat_id, message_id, user_id, article, warning_only)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (update_id) DO NOTHING`,
		c.UpdateID, c.ChatID, c.MessageID, c.UserID, c.Article, c.WarningOnly)
	if err != nil {
		return model.JudgeCase{}, err
	}
	stored, _, err := s.JudgeCase(ctx, c.UpdateID)
	return stored, err
}

func (s *Store) AdvanceJudgeCase(ctx context.Context, updateID int64, phase int) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		UPDATE bot_judge_cases SET phase = GREATEST(phase, $2)
		WHERE update_id = $1`, updateID, phase)
	return err
}

func (s *Store) RecordJudgeForward(ctx context.Context, updateID, forwardedMessageID int64) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `
		UPDATE bot_judge_cases SET forwarded_message_id = $2
		WHERE update_id = $1 AND forwarded_message_id = 0`, updateID, forwardedMessageID)
	return err
}

func (s *Store) pruneJudgeCases(ctx context.Context, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `DELETE FROM bot_judge_cases WHERE phase = 3 AND created_at < $1`, now.Add(-30*24*time.Hour))
	return err
}
