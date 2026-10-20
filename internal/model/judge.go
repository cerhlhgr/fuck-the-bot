package model

import "context"

type ChatMember struct {
	Status             string `json:"status"`
	CanRestrictMembers bool   `json:"can_restrict_members"`
	CanDeleteMessages  bool   `json:"can_delete_messages"`
	CanSendMessages    bool   `json:"can_send_messages"`
}

func (m ChatMember) IsAdministrator() bool {
	return m.Status == "administrator" || m.Status == "creator"
}

type JudgeRequest struct {
	MessageID     int64
	Author        string
	Text          string
	History       []HistoryEntry
	ReplyToUserID int64
	ReplyToAuthor string
	Contacts      []Contact
}

// Article 0 means no violation. Articles 1–3 map to the fixed rules in view.
type JudgeDecision struct {
	Article           int
	EvidenceMessageID int64
	Reason            string
	UnbanQuery        string
}

type JudgeCase struct {
	UpdateID           int64
	ChatID             int64
	MessageID          int64
	UserID             int64
	Article            int
	WarningOnly        bool
	Phase              int
	ForwardedMessageID int64
}

type JudgeStore interface {
	JudgeMode(context.Context, int64) (bool, error)
	SetJudgeMode(context.Context, int64, bool) error
	JudgeCase(context.Context, int64) (JudgeCase, bool, error)
	SaveJudgeCase(context.Context, JudgeCase) (JudgeCase, error)
	RecordJudgeForward(context.Context, int64, int64) error
	AdvanceJudgeCase(context.Context, int64, int) error
}
