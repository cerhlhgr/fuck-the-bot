package model

import (
	"context"
	"errors"
	"time"
)

var ErrUnbanVoteActive = errors.New("unban vote already active for this participant")

type UnbanVote struct {
	RequestUpdateID  int64
	ChatID           int64
	ThreadID         int64
	RequestMessageID int64
	TargetUserID     int64
	TargetName       string
	TargetStatus     string
	PollMessageID    int64
	ClosesAt         time.Time
	YesVotes         int
	NoVotes          int
	State            string
}

type UnbanVoteStore interface {
	UnbanVote(context.Context, int64) (UnbanVote, bool, error)
	CreateUnbanVote(context.Context, UnbanVote) (UnbanVote, error)
	SetUnbanVotePoll(context.Context, int64, int64, time.Time) error
	DueUnbanVotes(context.Context, time.Time) ([]UnbanVote, error)
	CloseUnbanVote(context.Context, int64, int, int) error
	MarkUnbanVoteApplied(context.Context, int64) error
	CompleteUnbanVote(context.Context, int64) error
}

type UnbanPollResult struct {
	Yes int
	No  int
}
