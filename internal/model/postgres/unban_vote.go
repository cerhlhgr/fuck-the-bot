package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fuck-the-bot/internal/model"

	"github.com/jackc/pgx/v5"
)

var _ model.UnbanVoteStore = (*Store)(nil)

const unbanVoteColumns = `request_update_id, chat_id, thread_id, request_message_id,
 target_user_id, target_name, target_status, poll_message_id, closes_at,
 yes_votes, no_votes, state`

func scanUnbanVote(row pgx.Row) (model.UnbanVote, error) {
	var vote model.UnbanVote
	var closesAt *time.Time
	err := row.Scan(&vote.RequestUpdateID, &vote.ChatID, &vote.ThreadID, &vote.RequestMessageID,
		&vote.TargetUserID, &vote.TargetName, &vote.TargetStatus, &vote.PollMessageID,
		&closesAt, &vote.YesVotes, &vote.NoVotes, &vote.State)
	if closesAt != nil {
		vote.ClosesAt = *closesAt
	}
	return vote, err
}

func (s *Store) UnbanVote(ctx context.Context, updateID int64) (model.UnbanVote, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	vote, err := scanUnbanVote(s.pool.QueryRow(queryCtx, `SELECT `+unbanVoteColumns+` FROM bot_judge_unban_votes WHERE request_update_id = $1`, updateID))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UnbanVote{}, false, nil
	}
	return vote, err == nil, err
}

func (s *Store) CreateUnbanVote(ctx context.Context, vote model.UnbanVote) (model.UnbanVote, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	row := s.pool.QueryRow(queryCtx, `
		INSERT INTO bot_judge_unban_votes
		(request_update_id, chat_id, thread_id, request_message_id, target_user_id, target_name, target_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING
		RETURNING `+unbanVoteColumns,
		vote.RequestUpdateID, vote.ChatID, vote.ThreadID, vote.RequestMessageID,
		vote.TargetUserID, vote.TargetName, vote.TargetStatus)
	stored, err := scanUnbanVote(row)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.UnbanVote{}, err
	}
	stored, err = scanUnbanVote(s.pool.QueryRow(queryCtx, `SELECT `+unbanVoteColumns+` FROM bot_judge_unban_votes WHERE request_update_id = $1`, vote.RequestUpdateID))
	if err == nil {
		return stored, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return model.UnbanVote{}, model.ErrUnbanVoteActive
	}
	return model.UnbanVote{}, err
}

func (s *Store) SetUnbanVotePoll(ctx context.Context, updateID, pollMessageID int64, closesAt time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `
		UPDATE bot_judge_unban_votes
		SET poll_message_id = $2, closes_at = $3, state = 'open'
		WHERE request_update_id = $1 AND state = 'created'`, updateID, pollMessageID, closesAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("unban vote %d is not awaiting a poll", updateID)
	}
	return nil
}

func (s *Store) DueUnbanVotes(ctx context.Context, now time.Time) ([]model.UnbanVote, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	rows, err := s.pool.Query(queryCtx, `SELECT `+unbanVoteColumns+` FROM bot_judge_unban_votes
		WHERE (state = 'open' AND closes_at <= $1) OR state IN ('closed', 'applied')
		ORDER BY created_at LIMIT 50`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var votes []model.UnbanVote
	for rows.Next() {
		vote, err := scanUnbanVote(rows)
		if err != nil {
			return nil, err
		}
		votes = append(votes, vote)
	}
	return votes, rows.Err()
}

func (s *Store) CloseUnbanVote(ctx context.Context, updateID int64, yes, no int) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `UPDATE bot_judge_unban_votes
		SET yes_votes = $2, no_votes = $3, state = 'closed'
		WHERE request_update_id = $1 AND state = 'open'`, updateID, yes, no)
	if err != nil { return err }
	if tag.RowsAffected() != 1 { return fmt.Errorf("unban vote %d is not open", updateID) }
	return nil
}

func (s *Store) MarkUnbanVoteApplied(ctx context.Context, updateID int64) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `UPDATE bot_judge_unban_votes SET state = 'applied'
		WHERE request_update_id = $1 AND state = 'closed'`, updateID)
	if err != nil { return err }
	if tag.RowsAffected() != 1 { return fmt.Errorf("unban vote %d is not closed", updateID) }
	return nil
}

func (s *Store) CompleteUnbanVote(ctx context.Context, updateID int64) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	tag, err := s.pool.Exec(queryCtx, `UPDATE bot_judge_unban_votes SET state = 'complete'
		WHERE request_update_id = $1 AND state = 'applied'`, updateID)
	if err != nil { return err }
	if tag.RowsAffected() != 1 { return fmt.Errorf("unban vote %d has no result notification pending", updateID) }
	return nil
}

func (s *Store) pruneUnbanVotes(ctx context.Context, now time.Time) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	_, err := s.pool.Exec(queryCtx, `DELETE FROM bot_judge_unban_votes
		WHERE state = 'complete' AND created_at < $1`, now.Add(-30*24*time.Hour))
	return err
}
