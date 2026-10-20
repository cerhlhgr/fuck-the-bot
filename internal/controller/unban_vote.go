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

const unbanVoteDuration = 10 * time.Minute

func likelyUnbanRequest(text string) bool {
	text = strings.ToLower(text)
	for _, part := range []string{"разблок", "разбан", "размут", "снять бан", "снимите бан", "снять мут", "снимите мут", "голосован"} {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

func replyToUserID(msg model.Message) int64 {
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		return msg.ReplyToMessage.From.ID
	}
	return 0
}

func replyToAuthor(msg model.Message) string {
	if msg.ReplyToMessage != nil {
		return model.AuthorName(msg.ReplyToMessage.From)
	}
	return ""
}

func (w *Worker) startUnbanVote(ctx context.Context, updateID int64, msg model.Message, query string) error {
	if w.UnbanVotes == nil {
		return fmt.Errorf("unban vote storage is not configured")
	}
	if query == "clarify" {
		return w.Telegram.SendMessage(ctx, msg, "Кого именно разблокировать? Укажи @username или ответь на сообщение этого человека.")
	}
	if previous, found, err := w.UnbanVotes.UnbanVote(ctx, updateID); err != nil {
		return fmt.Errorf("load unban vote: %w", err)
	} else if found {
		return w.sendUnbanVotePoll(ctx, updateID, msg, previous)
	}
	var target model.Contact
	if query == "reply" {
		if msg.ReplyToMessage == nil || msg.ReplyToMessage.From == nil || msg.ReplyToMessage.From.ID <= 0 || msg.ReplyToMessage.From.IsBot {
			return w.Telegram.SendMessage(ctx, msg, "Ответь на сообщение нужного человека или укажи его @username, чтобы запустить голосование.")
		}
		user := msg.ReplyToMessage.From
		target = model.Contact{UserID: user.ID, Username: user.Username, Name: model.ContactName(user)}
	} else {
		contacts, err := w.Repo.FindContacts(ctx, msg.Chat.ID, query)
		if err != nil {
			return fmt.Errorf("find unban vote target: %w", err)
		}
		if len(contacts) == 0 {
			return w.Telegram.SendMessage(ctx, msg, "Не нашёл этого участника среди известных контактов беседы. Укажи точный @username или ответь на его сообщение.")
		}
		target, err = exactUnbanContact(contacts, query)
		if err != nil {
			return w.Telegram.SendMessage(ctx, msg, "Нашёл несколько участников с таким именем. Укажи точный @username, чтобы не разблокировать не того человека.")
		}
	}
	if target.UserID == msg.From.ID || target.UserID == w.BotID {
		return w.Telegram.SendMessage(ctx, msg, "Для этого человека голосование не запускаю.")
	}
	member, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, target.UserID)
	if err != nil {
		return fmt.Errorf("check unban vote target: %w", err)
	}
	if member.IsAdministrator() || (member.Status != "kicked" && (member.Status != "restricted" || member.CanSendMessages)) {
		return w.Telegram.SendMessage(ctx, msg, "Этот участник сейчас не заблокирован для отправки сообщений.")
	}
	bot, err := w.JudgeTG.GetChatMember(ctx, msg.Chat.ID, w.BotID)
	if err != nil {
		return fmt.Errorf("check unban vote bot permissions: %w", err)
	}
	if !bot.IsAdministrator() || (bot.Status != "creator" && !bot.CanRestrictMembers) {
		return w.Telegram.SendMessage(ctx, msg, "Не могу провести разблокировку: боту нужны права ограничивать участников.")
	}
	name := strings.TrimSpace(target.Name)
	if target.Username != "" {
		name = "@" + target.Username
	}
	if name == "" {
		name = fmt.Sprintf("участника %d", target.UserID)
	}
	vote, err := w.UnbanVotes.CreateUnbanVote(ctx, model.UnbanVote{
		RequestUpdateID: updateID, ChatID: msg.Chat.ID, ThreadID: msg.MessageThreadID,
		RequestMessageID: msg.MessageID, TargetUserID: target.UserID,
		TargetName: name, TargetStatus: member.Status,
	})
	if errors.Is(err, model.ErrUnbanVoteActive) {
		return w.Telegram.SendMessage(ctx, msg, "По этому участнику уже идёт голосование о разблокировке.")
	}
	if err != nil {
		return fmt.Errorf("create unban vote: %w", err)
	}
	return w.sendUnbanVotePoll(ctx, updateID, msg, vote)
}

func (w *Worker) sendUnbanVotePoll(ctx context.Context, updateID int64, msg model.Message, vote model.UnbanVote) error {
	if vote.PollMessageID != 0 {
		return nil
	}
	pollID, err := w.JudgeTG.SendUnbanPoll(ctx, msg, vote.TargetName)
	if err != nil {
		return fmt.Errorf("send unban poll: %w", err)
	}
	if err := w.UnbanVotes.SetUnbanVotePoll(ctx, updateID, pollID, time.Now().Add(unbanVoteDuration)); err != nil {
		return fmt.Errorf("save unban poll message: %w", err)
	}
	log.Printf("unban vote started update_id=%d chat_id=%d target_user_id=%d poll_message_id=%d", updateID, msg.Chat.ID, vote.TargetUserID, pollID)
	return nil
}

func exactUnbanContact(contacts []model.Contact, query string) (model.Contact, error) {
	query = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(query)), "@")
	var exact []model.Contact
	for _, contact := range contacts {
		if strings.EqualFold(contact.Username, query) || strings.EqualFold(contact.Name, query) {
			exact = append(exact, contact)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 || len(contacts) > 1 {
		return model.Contact{}, errors.New("ambiguous unban target")
	}
	return contacts[0], nil
}

func (w *Worker) processDueUnbanVotes(ctx context.Context, now time.Time) error {
	votes, err := w.UnbanVotes.DueUnbanVotes(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	for _, vote := range votes {
		if err := w.processUnbanVote(ctx, vote); err != nil {
			errs = append(errs, fmt.Errorf("vote %d: %w", vote.RequestUpdateID, err))
		}
	}
	return errors.Join(errs...)
}

func (w *Worker) processUnbanVote(ctx context.Context, vote model.UnbanVote) error {
	if vote.State == "open" {
		result, err := w.JudgeTG.StopUnbanPoll(ctx, vote.ChatID, vote.PollMessageID)
		if err != nil {
			return fmt.Errorf("stop Telegram poll: %w", err)
		}
		if err := w.UnbanVotes.CloseUnbanVote(ctx, vote.RequestUpdateID, result.Yes, result.No); err != nil {
			return fmt.Errorf("save unban poll result: %w", err)
		}
		vote.YesVotes, vote.NoVotes, vote.State = result.Yes, result.No, "closed"
		log.Printf("unban vote closed update_id=%d chat_id=%d target_user_id=%d yes=%d no=%d", vote.RequestUpdateID, vote.ChatID, vote.TargetUserID, result.Yes, result.No)
	}
	if vote.State == "closed" {
		if vote.YesVotes > vote.NoVotes {
			member, err := w.JudgeTG.GetChatMember(ctx, vote.ChatID, vote.TargetUserID)
			if err != nil {
				return fmt.Errorf("recheck vote target: %w", err)
			}
			switch member.Status {
			case "kicked":
				if err := w.JudgeTG.UnbanChatMember(ctx, vote.ChatID, vote.TargetUserID); err != nil {
					return fmt.Errorf("unban member: %w", err)
				}
			case "restricted":
				if !member.CanSendMessages {
					if err := w.JudgeTG.UnrestrictChatMember(ctx, vote.ChatID, vote.TargetUserID); err != nil {
						return fmt.Errorf("unrestrict member: %w", err)
					}
				}
			}
		}
		if err := w.UnbanVotes.MarkUnbanVoteApplied(ctx, vote.RequestUpdateID); err != nil {
			return fmt.Errorf("record unban vote action: %w", err)
		}
		vote.State = "applied"
	}
	if vote.State == "applied" {
		poll := model.Message{MessageID: vote.PollMessageID, MessageThreadID: vote.ThreadID, IsTopicMessage: vote.ThreadID != 0}
		poll.Chat.ID = vote.ChatID
		text := fmt.Sprintf("Голосование за разблокировку %s завершено: за — %d, против — %d. Большинства нет, ограничение остаётся.", vote.TargetName, vote.YesVotes, vote.NoVotes)
		if vote.YesVotes > vote.NoVotes {
			text = fmt.Sprintf("Голосование за разблокировку %s завершено: за — %d, против — %d. Большинство за; ограничение снято.", vote.TargetName, vote.YesVotes, vote.NoVotes)
			if vote.TargetStatus == "kicked" {
				text += " Участник может вернуться по ссылке-приглашению."
			}
		}
		if err := w.Telegram.SendMessage(ctx, poll, text); err != nil {
			return fmt.Errorf("send unban vote result: %w", err)
		}
		if err := w.UnbanVotes.CompleteUnbanVote(ctx, vote.RequestUpdateID); err != nil {
			return fmt.Errorf("record unban vote notification: %w", err)
		}
		log.Printf("unban vote completed update_id=%d chat_id=%d target_user_id=%d approved=%t", vote.RequestUpdateID, vote.ChatID, vote.TargetUserID, vote.YesVotes > vote.NoVotes)
	}
	return nil
}
