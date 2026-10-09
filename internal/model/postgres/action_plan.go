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

var _ model.ActionPlanStore = (*Store)(nil)

func (s *Store) LoadActionPlan(ctx context.Context, chatID, threadID, firstUpdateID int64) (model.ActionPlan, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	var plan model.ActionPlan
	var actionsJSON []byte
	err := s.pool.QueryRow(queryCtx, `
		SELECT update_ids, considered_update_ids, actions, completed FROM bot_action_plans
		WHERE chat_id = $1 AND thread_id = $2 AND first_update_id = $3`,
		chatID, threadID, firstUpdateID).Scan(&plan.UpdateIDs, &plan.ConsideredUpdateIDs, &actionsJSON, &plan.Completed)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ActionPlan{}, false, nil
	}
	if err != nil {
		return model.ActionPlan{}, false, err
	}
	if err := json.Unmarshal(actionsJSON, &plan.Actions); err != nil {
		return model.ActionPlan{}, false, fmt.Errorf("decode action plan: %w", err)
	}
	if plan.Completed < 0 || plan.Completed > len(plan.Actions) {
		return model.ActionPlan{}, false, errors.New("invalid action plan progress")
	}
	return plan, true, nil
}

func (s *Store) SaveActionPlan(ctx context.Context, chatID, threadID, firstUpdateID int64, plan model.ActionPlan) (model.ActionPlan, error) {
	if len(plan.UpdateIDs) == 0 || len(plan.Actions) > 32 {
		return model.ActionPlan{}, errors.New("invalid action plan size")
	}
	if plan.Actions == nil {
		plan.Actions = []model.Decision{}
	}
	if plan.ConsideredUpdateIDs == nil {
		plan.ConsideredUpdateIDs = []int64{}
	}
	actionsJSON, err := json.Marshal(plan.Actions)
	if err != nil {
		return model.ActionPlan{}, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err = s.pool.Exec(queryCtx, `
		INSERT INTO bot_action_plans (chat_id, thread_id, first_update_id, update_ids, considered_update_ids, actions)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
		ON CONFLICT (chat_id, thread_id, first_update_id) DO NOTHING`,
		chatID, threadID, firstUpdateID, plan.UpdateIDs, plan.ConsideredUpdateIDs, string(actionsJSON))
	if err != nil {
		return model.ActionPlan{}, err
	}
	saved, found, err := s.LoadActionPlan(ctx, chatID, threadID, firstUpdateID)
	if err != nil {
		return model.ActionPlan{}, err
	}
	if !found {
		return model.ActionPlan{}, errors.New("action plan missing after insert")
	}
	return saved, nil
}

func (s *Store) MarkActionCompleted(ctx context.Context, chatID, threadID, firstUpdateID int64, index int) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `
		UPDATE bot_action_plans SET completed = GREATEST(completed, $4), updated_at = now()
		WHERE chat_id = $1 AND thread_id = $2 AND first_update_id = $3
		AND completed >= $4 - 1 AND $4 <= jsonb_array_length(actions)`,
		chatID, threadID, firstUpdateID, index+1)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("action plan progress mismatch at index %d", index)
	}
	return nil
}

func (s *Store) pruneActionPlans(ctx context.Context, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `DELETE FROM bot_action_plans WHERE created_at < $1`, now.Add(-30*24*time.Hour))
	return err
}
