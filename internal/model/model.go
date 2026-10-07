package model

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	HistoryLifetime = 24 * time.Hour
	ContextLifetime = time.Hour
)

var ErrChatMigrated = errors.New("Telegram group migrated to a supergroup")

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	IsBot     bool   `json:"is_bot"`
}

type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

type Message struct {
	MessageID       int64 `json:"message_id"`
	MessageThreadID int64 `json:"message_thread_id"`
	IsTopicMessage  bool  `json:"is_topic_message"`
	Date            int64 `json:"date"`
	Chat            struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From             *User       `json:"from"`
	Text             string      `json:"text"`
	Entities         []Entity    `json:"entities"`
	Caption          string      `json:"caption"`
	CaptionEntities  []Entity    `json:"caption_entities"`
	Photo            []PhotoSize `json:"photo"`
	PhotoDescription string      `json:"-"`
	ReplyToMessage   *Message    `json:"reply_to_message"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

func (m *Message) NormalizeThread() {
	if !m.IsTopicMessage {
		m.MessageThreadID = 0
	}
}

type HistoryEntry struct {
	Date             time.Time
	MessageID        int64
	ReplyToMessageID int64
	Author           string
	Text             string
	Bot              bool
}

type ImportantEntry struct {
	SourceMessageID int64
	SourceDate      time.Time
	Author          string
	Summary         string
	Kind            string
}

type ImportantUpdate struct {
	SourceMessageID int64
	Summary         string
	Kind            string
}

type RepliedToBotMessage struct {
	UserMessageID int64  `json:"user_message_id"`
	BotMessageID  int64  `json:"bot_message_id"`
	BotText       string `json:"bot_text"`
}

type Decision struct {
	Actions            []Decision
	Action             string
	Important          string
	ImportantKind      string
	ImportantUpdates   []ImportantUpdate
	ForgetImportantIDs []int64
	ReplyToMessageID   int64
	Reply              string
	Poll               *Poll
	Music              *MusicRequest
	Voice              *VoiceRequest
	ImageQuery         string
	Caption            string
	Reaction           string
}

type Poll struct {
	Question string
	Options  []string
}

type DecisionRequest struct {
	BotUsername             string
	CurrentMessageID        int64
	CurrentReplyToMessageID int64
	CurrentRepliedToBot     bool
	NewMessageIDs           []int64
	NewReplyToBotIDs        []int64
	RepliedToBotMessages    []RepliedToBotMessage
	History                 []HistoryEntry
	Important               []ImportantEntry
	MusicEnabled            bool
	VoiceEnabled            bool
}

type Repository interface {
	EnqueueUpdate(context.Context, Update, []byte) error
	PendingUpdates(context.Context, int64, int) ([]Update, error)
	MarkUpdatesProcessed(context.Context, []int64) error
	TryDecisionLock(context.Context) (func() error, bool, error)
	Prune(context.Context, time.Time) error
	AddIncoming(context.Context, Message, time.Time) error
	AddBotReply(context.Context, int64, int64, int64, string, string, time.Time) error
	Conversation(context.Context, int64, int64, time.Time) ([]HistoryEntry, error)
	ImportantContext(context.Context, int64, int64) ([]ImportantEntry, error)
	ApplyImportantBatch(context.Context, []Message, []ImportantUpdate, []int64, time.Time) error
}

func MentionedText(msg Message, username string) (string, bool) {
	content, entities := msg.Text, msg.Entities
	if content == "" {
		content, entities = msg.Caption, msg.CaptionEntities
	}
	for _, e := range entities {
		if e.Type != "mention" {
			continue
		}
		start, end, ok := utf16ByteRange(content, e.Offset, e.Length)
		if !ok || !strings.EqualFold(content[start:end], "@"+username) {
			continue
		}
		text := strings.TrimSpace(content[:start] + content[end:])
		if text == "" {
			text = "Пользователь просто позвал тебя по имени. Ответь коротко."
		}
		return text, true
	}
	return "", false
}

func utf16ByteRange(s string, offset, length int) (int, int, bool) {
	if offset < 0 || length <= 0 || offset > int(^uint(0)>>1)-length || !utf8.ValidString(s) {
		return 0, 0, false
	}
	start, end, units := -1, -1, 0
	for i, r := range s {
		if units == offset {
			start = i
		}
		if units == offset+length {
			end = i
		}
		units += UTF16RuneLength(r)
	}
	if units == offset {
		start = len(s)
	}
	if units == offset+length {
		end = len(s)
	}
	return start, end, start >= 0 && end >= start
}

func UTF16RuneLength(r rune) int {
	if r > 0xffff {
		return 2
	}
	return 1
}

func AuthorName(u *User) string {
	if u == nil {
		return "Участник"
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		return CompactText(name, 80)
	}
	return "user#" + strconv.FormatInt(u.ID, 10)
}

func MessageHistoryText(msg Message) string {
	content := msg.Text
	if content == "" {
		content = msg.Caption
	}
	if len(msg.Photo) > 0 {
		photoText := "[Фото: содержимое недоступно для анализа]"
		if description := strings.TrimSpace(msg.PhotoDescription); description != "" {
			photoText = "[На фото: " + description + "]"
		}
		if content != "" {
			content += "\n"
		}
		content += photoText
	}
	if content == "" {
		return "[сообщение без текста]"
	}
	return content
}

func CompactText(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-1]) + "…"
}
