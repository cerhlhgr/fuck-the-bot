package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"fuck-the-bot/internal/model"
)

type JudgeAI interface {
	Judge(context.Context, model.JudgeRequest) (model.JudgeDecision, error)
}

type JudgeTelegram interface {
	GetChatMember(context.Context, int64, int64) (model.ChatMember, error)
	ForwardMessage(context.Context, model.Message) (int64, error)
	RestrictChatMember(context.Context, int64, int64, time.Time) error
	KickChatMember(context.Context, int64, int64) error
	DeleteMessage(context.Context, int64, int64) error
	SendUnbanPoll(context.Context, model.Message, string) (int64, error)
	StopUnbanPoll(context.Context, int64, int64) (model.UnbanPollResult, error)
	UnrestrictChatMember(context.Context, int64, int64) error
	UnbanChatMember(context.Context, int64, int64) error
}

func judgeCommand(msg model.Message) (bool, bool) {
	command := strings.ToLower(strings.Join(strings.Fields(msg.Text), " "))
	switch command {
	case "режим судьи":
		return true, true
	case "режим судьи отключить":
		return false, true
	default:
		return false, false
	}
}

func judgeArticle(article int) (title, sentence string, duration time.Duration) {
	switch article {
	case 1:
		return "Статья 1 — угрозы и сексуальное насилие", "запрет писать на 24 часа", 24 * time.Hour
	case 2:
		return "Статья 2 — травля и целенаправленные оскорбления", "запрет писать на 1 час", time.Hour
	case 3:
		return "Статья 3 — разглашение чужих личных данных", "исключение из беседы", 0
	default:
		return "", "", 0
	}
}

// processJudgeUpdate reports whether this update was handled by moderation and
// must be excluded from the ordinary conversation decision.
func (w *Worker) processJudgeUpdate(ctx context.Context, item model.Update, enabled bool) (bool, error) {
	if item.Message == nil {
		return false, nil
	}
	msg := *item.Message
	msg.NormalizeThread()
	if msg.From != nil && msg.From.IsBot {
		return false, nil
	}
	if turnOn, ok := judgeCommand(msg); ok {
		return true, w.applyJudgeCommand(ctx, msg, turnOn, enabled)
	}
	if !enabled || msg.From == nil || msg.From.ID <= 0 || msg.SenderChat != nil {
		return false, nil
	}
	if msg.Date != 0 && time.Unix(msg.Date, 0).Before(time.Now().Add(-48*time.Hour)) {
		log.Printf("judge skipped old message update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
		return false, nil
	}
	caseRecord, found, err := w.JudgeStore.JudgeCase(ctx, item.UpdateID)
	if err != nil {
		return false, fmt.Errorf("load judge case: %w", err)
	}
	if !found {
		if len(msg.Photo) > 0 {
			msg = w.describeCandidatePhoto(ctx, msg, item.UpdateID, time.Now())
		}
		if err := w.Repo.AddIncoming(ctx, msg, time.Now()); err != nil {
			return false, fmt.Errorf("store judge message: %w", err)
		}
		history, err := w.Repo.Conversation(ctx, msg.Chat.ID, msg.MessageThreadID, time.Now())
		if err != nil {
			return false, fmt.Errorf("load judge context: %w", err)
		}
		log.Printf("judge review started update_id=%d chat_id=%d thread_id=%d message_id=%d context_messages=%d", item.UpdateID, msg.Chat.ID, msg.MessageThreadID, msg.MessageID, len(history))
		request := model.JudgeRequest{
			MessageID:     msg.MessageID,
			Author:        model.AuthorName(msg.From),
			Text:          model.MessageHistoryText(msg),
			History:       history,
			ReplyToUserID: replyToUserID(msg),
			ReplyToAuthor: replyToAuthor(msg),
		}
		if likelyUnbanRequest(msg.Text) {
			request.Contacts, err = w.Repo.RecentContacts(ctx, msg.Chat.ID, 100)
			if err != nil {
				return false, fmt.Errorf("load unban vote contacts: %w", err)
			}
		}
		decision, err := w.JudgeAI.Judge(ctx, request)
		if err != nil {
			return false, fmt.Errorf("judge AI update_id=%d: %w", item.UpdateID, err)
		}
		if decision.UnbanQuery != "" {
			return true, w.startUnbanVote(ctx, item.UpdateID, msg, decision.UnbanQuery)
		}
		if decision.Article == 0 {
			log.Printf("judge no violation update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
			return false, nil
		}
		if decision.Article < 1 || decision.Article > 3 || decision.EvidenceMessageID != msg.MessageID {
			return false, fmt.Errorf("invalid judge decision update_id=%d", item.UpdateID)
		}
		if decision.Article == 2 {
			knownTarget, err := w.knownBullyingTarget(ctx, msg, decision.Target)
			if err != nil {
				return false, fmt.Errorf("verify bullying target: %w", err)
			}
			if !knownTarget || decision.BullyingBasis != "direct_aggression" || !model.JudgeQuoteInMessage(decision.AggressionQuote, request.Text) {
				log.Printf("judge violation skipped update_id=%d chat_id=%d message_id=%d reason=unverified_direct_aggression", item.UpdateID, msg.Chat.ID, msg.MessageID)
				return false, nil
			}
		}
		member, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, msg.From.ID)
		if err != nil {
			return false, fmt.Errorf("check accused member: %w", err)
		}
		if member.Status == "left" || member.Status == "kicked" {
			log.Printf("judge sanction skipped update_id=%d chat_id=%d reason=member_not_eligible", item.UpdateID, msg.Chat.ID)
			return false, nil
		}
		adminWarning := member.IsAdministrator()
		if !adminWarning {
			botMember, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, w.BotID)
			if err != nil {
				return false, fmt.Errorf("check judge bot permissions: %w", err)
			}
			if !botMember.IsAdministrator() || (botMember.Status != "creator" && !botMember.CanRestrictMembers) {
				return false, fmt.Errorf("judge bot lacks can_restrict_members in chat_id=%d", msg.Chat.ID)
			}
		}
		caseRecord, err = w.JudgeStore.SaveJudgeCase(ctx, model.JudgeCase{
			UpdateID: item.UpdateID, ChatID: msg.Chat.ID, MessageID: msg.MessageID,
			UserID: msg.From.ID, Article: decision.Article, WarningOnly: adminWarning, AdminWarning: adminWarning,
		})
		if err != nil {
			return false, fmt.Errorf("save judge case: %w", err)
		}
		log.Printf("judge violation recorded update_id=%d chat_id=%d message_id=%d user_id=%d article=%d warning_only=%t", item.UpdateID, msg.Chat.ID, msg.MessageID, msg.From.ID, caseRecord.Article, caseRecord.WarningOnly)
	}
	if caseRecord.ChatID != msg.Chat.ID || caseRecord.MessageID != msg.MessageID || caseRecord.UserID != msg.From.ID {
		return false, fmt.Errorf("judge case does not match update_id=%d", item.UpdateID)
	}
	return true, w.executeJudgeCase(ctx, msg, caseRecord)
}

func (w *Worker) knownBullyingTarget(ctx context.Context, msg model.Message, target string) (bool, error) {
	target = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(target), "@"))
	if target == "" || len([]rune(target)) > 80 {
		return false, nil
	}
	var targetID int64
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		repliedTo := msg.ReplyToMessage.From
		if repliedTo.ID > 0 && repliedTo.ID != msg.From.ID && !repliedTo.IsBot && judgeTargetMatches(target, repliedTo.Username, model.ContactName(repliedTo)) {
			targetID = repliedTo.ID
		}
	}
	if targetID == 0 {
		contacts, err := w.Repo.FindContacts(ctx, msg.Chat.ID, target)
		if err != nil {
			return false, err
		}
		for _, contact := range contacts {
			if contact.UserID == msg.From.ID || !judgeTargetMatches(target, contact.Username, contact.Name) {
				continue
			}
			if targetID != 0 && targetID != contact.UserID {
				return false, nil
			}
			targetID = contact.UserID
		}
	}
	if targetID == 0 {
		return false, nil
	}
	member, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, targetID)
	if err != nil {
		return false, err
	}
	if member.Status == "left" || member.Status == "kicked" {
		return false, nil
	}
	return true, nil
}

func judgeTargetMatches(target, username, name string) bool {
	target = strings.TrimPrefix(strings.TrimSpace(target), "@")
	if username != "" && strings.EqualFold(target, username) {
		return true
	}
	return name != "" && strings.EqualFold(target, name)
}

func (w *Worker) applyJudgeCommand(ctx context.Context, msg model.Message, turnOn, wasEnabled bool) error {
	if msg.From == nil || msg.From.ID <= 0 || msg.SenderChat != nil {
		return nil
	}
	member, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, msg.From.ID)
	if err != nil {
		return fmt.Errorf("check judge command author: %w", err)
	}
	if !member.IsAdministrator() {
		log.Printf("judge command ignored chat_id=%d message_id=%d reason=not_admin", msg.Chat.ID, msg.MessageID)
		return w.Telegram.SendMessage(ctx, msg, "Включить или отключить режим судьи может только администратор беседы.")
	}
	if turnOn {
		if msg.Chat.Type != "supergroup" {
			return w.Telegram.SendMessage(ctx, msg, "Режим судьи требует супергруппу Telegram: только там можно временно запретить участнику писать.")
		}
		botMember, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, w.BotID)
		if err != nil {
			return fmt.Errorf("check judge bot permissions: %w", err)
		}
		if !botMember.IsAdministrator() || (botMember.Status != "creator" && (!botMember.CanRestrictMembers || !botMember.CanDeleteMessages)) {
			return w.Telegram.SendMessage(ctx, msg, "Для режима судьи дай боту права администратора: блокировать участников и удалять сообщения.")
		}
	}
	if wasEnabled == turnOn {
		if turnOn {
			return w.Telegram.SendMessage(ctx, msg, "Режим судьи уже включён. Проверяю правила для всех сообщений и отвечаю на обращения к боту.")
		}
		return w.Telegram.SendMessage(ctx, msg, "Режим судьи уже выключен.")
	}
	if err := w.JudgeStore.SetJudgeMode(ctx, msg.Chat.ID, turnOn); err != nil {
		return fmt.Errorf("save judge mode: %w", err)
	}
	status := "Режим судьи выключен. Возвращаюсь к обычной работе."
	if turnOn {
		status = "Режим судьи включён. Проверяю новые сообщения по правилам беседы и отвечаю на обращения к боту. За первое нарушение статьи предупрежу, за повторное в течение часа применю наказание. Администраторов не ограничиваю."
	}
	log.Printf("judge mode changed chat_id=%d enabled=%t by_user_id=%d", msg.Chat.ID, turnOn, msg.From.ID)
	return w.Telegram.SendMessage(ctx, msg, status)
}

func (w *Worker) executeJudgeCase(ctx context.Context, msg model.Message, c model.JudgeCase) error {
	title, sentence, duration := judgeArticle(c.Article)
	if title == "" {
		return fmt.Errorf("invalid judge article %d", c.Article)
	}
	if c.Phase == 0 && c.ForwardedMessageID == 0 {
		forwardedID := int64(-1)
		if c.Article != 3 {
			var err error
			forwardedID, err = w.JudgeTG.ForwardMessage(ctx, msg)
			if errors.Is(err, model.ErrMessageCannotForward) {
				log.Printf("judge evidence forwarding unavailable update_id=%d chat_id=%d message_id=%d error=%v", c.UpdateID, c.ChatID, c.MessageID, err)
				forwardedID = -1
			} else if err != nil {
				return fmt.Errorf("forward judge evidence: %w", err)
			}
		}
		if err := w.JudgeStore.RecordJudgeForward(ctx, c.UpdateID, forwardedID); err != nil {
			return fmt.Errorf("record judge evidence forwarding: %w", err)
		}
		log.Printf("judge evidence forwarding recorded update_id=%d chat_id=%d source_message_id=%d forwarded_message_id=%d", c.UpdateID, c.ChatID, c.MessageID, forwardedID)
	}
	if c.Phase < 1 {
		warning := title + ". Предупреждение: нарушение зафиксировано. При повторном нарушении этой статьи в течение часа будет применено наказание."
		if !c.WarningOnly {
			warning = title + ". Предупреждение: повторное нарушение зафиксировано. Сейчас будет применено наказание."
		}
		if c.AdminWarning {
			warning = title + ". Предупреждение администратору: нарушение зафиксировано. Ограничение не применяется."
		}
		if err := w.Telegram.SendMessage(ctx, msg, warning); err != nil {
			return fmt.Errorf("send judge warning: %w", err)
		}
		if err := w.JudgeStore.AdvanceJudgeCase(ctx, c.UpdateID, 1); err != nil {
			return fmt.Errorf("record judge warning: %w", err)
		}
	}
	if c.WarningOnly {
		if c.Phase < 3 {
			if err := w.JudgeStore.AdvanceJudgeCase(ctx, c.UpdateID, 3); err != nil {
				return fmt.Errorf("record admin warning completion: %w", err)
			}
		}
		log.Printf("judge warning completed update_id=%d chat_id=%d user_id=%d article=%d", c.UpdateID, c.ChatID, c.UserID, c.Article)
		return nil
	}
	if c.Phase < 2 {
		if err := w.Telegram.SendMessage(ctx, msg, title+". Приговор: "+sentence+". Исполняю."); err != nil {
			return fmt.Errorf("send judge sentence: %w", err)
		}
		if err := w.JudgeStore.AdvanceJudgeCase(ctx, c.UpdateID, 2); err != nil {
			return fmt.Errorf("record judge sentence: %w", err)
		}
	}
	if c.Phase < 3 {
		member, err := w.JudgeTG.GetChatMember(ctx, c.ChatID, c.UserID)
		if err != nil {
			return fmt.Errorf("recheck accused member before sentence: %w", err)
		}
		if member.IsAdministrator() || member.Status == "left" || member.Status == "kicked" {
			log.Printf("judge sentence cancelled update_id=%d chat_id=%d reason=member_not_eligible", c.UpdateID, c.ChatID)
			return w.JudgeStore.AdvanceJudgeCase(ctx, c.UpdateID, 3)
		}
		if c.Article == 3 {
			err = w.JudgeTG.KickChatMember(ctx, c.ChatID, c.UserID)
		} else {
			err = w.JudgeTG.RestrictChatMember(ctx, c.ChatID, c.UserID, time.Now().Add(duration))
		}
		if err != nil {
			return fmt.Errorf("execute judge sentence article=%d chat_id=%d user_id=%d: %w", c.Article, c.ChatID, c.UserID, err)
		}
		if err := w.JudgeStore.AdvanceJudgeCase(ctx, c.UpdateID, 3); err != nil {
			return fmt.Errorf("record judge sentence execution: %w", err)
		}
		if err := w.JudgeTG.DeleteMessage(ctx, c.ChatID, c.MessageID); err != nil {
			log.Printf("judge message deletion failed update_id=%d chat_id=%d message_id=%d error=%v", c.UpdateID, c.ChatID, c.MessageID, err)
		}
		log.Printf("judge sentence executed update_id=%d chat_id=%d user_id=%d article=%d", c.UpdateID, c.ChatID, c.UserID, c.Article)
	}
	return nil
}
