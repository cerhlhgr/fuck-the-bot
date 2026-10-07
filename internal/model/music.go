package model

import (
	"context"
	"errors"
)

var ErrMusicTaskNotFound = errors.New("music task not found")
var ErrMusicTaskMismatch = errors.New("music task ID mismatch")

type MusicRequest struct {
	Mode         string `json:"mode"`
	Prompt       string `json:"prompt,omitempty"`
	Title        string `json:"title,omitempty"`
	Style        string `json:"style,omitempty"`
	Instrumental bool   `json:"instrumental"`
	NegativeTags string `json:"negative_tags,omitempty"`
	VocalGender  string `json:"vocal_gender,omitempty"`
}

type MusicTrack struct {
	ID               int64
	ChatID           int64
	ThreadID         int64
	ReplyToMessageID int64
	AudioID          string
	AudioURL         string
	Title            string
}

type MusicCallback struct {
	TaskID string
	Stage  string
	Code   int
	Error  string
	Tracks []MusicTrack
}

type MusicStore interface {
	CreateMusicTask(context.Context, string, Message, MusicRequest) (bool, error)
	BindMusicTask(context.Context, string, string) error
	FailMusicTask(context.Context, string, string) error
	ApplyMusicCallback(context.Context, string, MusicCallback) error
	PendingMusicTracks(context.Context, int) ([]MusicTrack, error)
	MarkMusicTrackDelivered(context.Context, int64) error
	RetryMusicTrack(context.Context, int64, string) error
	TryMusicDeliveryLock(context.Context) (func() error, bool, error)
}
