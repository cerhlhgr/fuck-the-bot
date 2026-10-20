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

func (w *Worker) processJudgeUpdate(ctx context.Context, item model.Update, enabled bool) error {
	if item.Message == nil {
		return nil
	}
	msg := *item.Message
	msg.NormalizeThread()
	if msg.From != nil && msg.From.IsBot {
		return nil
	}
	if turnOn, ok := judgeCommand(msg); ok {
		return w.applyJudgeCommand(ctx, msg, turnOn, enabled)
	}
	if !enabled || msg.From == nil || msg.From.ID <= 0 || msg.SenderChat != nil {
		return nil
	}
	if msg.Date != 0 && time.Unix(msg.Date, 0).Before(time.Now().Add(-48*time.Hour)) {
		log.Printf("judge skipped old message update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
		return nil
	}
	caseRecord, found, err := w.JudgeStore.JudgeCase(ctx, item.UpdateID)
	if err != nil {
		return fmt.Errorf("load judge case: %w", err)
	}
	if !found {
		if len(msg.Photo) > 0 {
			msg = w.describeCandidatePhoto(ctx, msg, item.UpdateID, time.Now())
		}
		if err := w.Repo.AddIncoming(ctx, msg, time.Now()); err != nil {
			return fmt.Errorf("store judge message: %w", err)
		}
		history, err := w.Repo.Conversation(ctx, msg.Chat.ID, msg.MessageThreadID, time.Now().Add(-model.ContextLifetime))
		if err != nil {
			return fmt.Errorf("load judge context: %w", err)
		}
		log.Printf("judge review started update_id=%d chat_id=%d thread_id=%d message_id=%d context_messages=%d", item.UpdateID, msg.Chat.ID, msg.MessageThreadID, msg.MessageID, len(history))
		decision, err := w.JudgeAI.Judge(ctx, model.JudgeRequest{
			MessageID: msg.MessageID,
			Author:    model.AuthorName(msg.From),
			Text:      model.MessageHistoryText(msg),
			History:   history,
		})
		if err != nil {
			return fmt.Errorf("judge AI update_id=%d: %w", item.UpdateID, err)
		}
		if decision.Article == 0 {
			log.Printf("judge no violation update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
			return nil
		}
		if decision.Article < 1 || decision.Article > 3 || decision.EvidenceMessageID != msg.MessageID {
			return fmt.Errorf("invalid judge decision update_id=%d", item.UpdateID)
		}
		member, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, msg.From.ID)
		if err != nil {
			return fmt.Errorf("check accused member: %w", err)
		}
		if member.IsAdministrator() || member.Status == "left" || member.Status == "kicked" {
			log.Printf("judge sanction skipped update_id=%d chat_id=%d reason=member_not_eligible", item.UpdateID, msg.Chat.ID)
			return nil
		}
		botMember, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, w.BotID)
		if err != nil {
			return fmt.Errorf("check judge bot permissions: %w", err)
		}
		if !botMember.IsAdministrator() || (botMember.Status != "creator" && !botMember.CanRestrictMembers) {
			return fmt.Errorf("judge bot lacks can_restrict_members in chat_id=%d", msg.Chat.ID)
		}
		caseRecord, err = w.JudgeStore.SaveJudgeCase(ctx, model.JudgeCase{
			UpdateID: item.UpdateID, ChatID: msg.Chat.ID, MessageID: msg.MessageID,
			UserID: msg.From.ID, Article: decision.Article,
		})
		if err != nil {
			return fmt.Errorf("save judge case: %w", err)
		}
		log.Printf("judge violation recorded update_id=%d chat_id=%d message_id=%d user_id=%d article=%d", item.UpdateID, msg.Chat.ID, msg.MessageID, msg.From.ID, caseRecord.Article)
	}
	if caseRecord.ChatID != msg.Chat.ID || caseRecord.MessageID != msg.MessageID || caseRecord.UserID != msg.From.ID {
		return fmt.Errorf("judge case does not match update_id=%d", item.UpdateID)
	}
	return w.executeJudgeCase(ctx, msg, caseRecord)
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
		return nil
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
		return nil
	}
	if err := w.JudgeStore.SetJudgeMode(ctx, msg.Chat.ID, turnOn); err != nil {
		return fmt.Errorf("save judge mode: %w", err)
	}
	status := "Режим судьи выключен. Возвращаюсь к обычной работе."
	if turnOn {
		status = "Режим судьи включён. Проверяю новые сообщения по правилам беседы. Перед наказанием назову статью, предупрежу и объявлю приговор."
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
		if err := w.Telegram.SendMessage(ctx, msg, title+". Предупреждение: нарушение зафиксировано. Сейчас будет применено наказание."); err != nil {
			return fmt.Errorf("send judge warning: %w", err)
		}
		if err := w.JudgeStore.AdvanceJudgeCase(ctx, c.UpdateID, 1); err != nil {
			return fmt.Errorf("record judge warning: %w", err)
		}
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
