package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"fuck-the-bot/internal/model"
)

type AI interface {
	Ask(context.Context, model.DecisionRequest) (model.Decision, error)
}

type Messenger interface {
	SendMessage(context.Context, model.Message, string) error
}

type Worker struct {
	Repo     model.Repository
	AI       AI
	Telegram Messenger
	BotID    int64
	Username string
}

func (w *Worker) Run(ctx context.Context, wake <-chan struct{}) {
	log.Print("update worker started")
	defer log.Print("update worker stopped")
	poll := time.NewTicker(time.Second)
	defer poll.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-poll.C:
		case <-cleanup.C:
			if err := w.Repo.Prune(ctx, time.Now()); err != nil {
				log.Printf("prune old records: %v", err)
			}
		}
		for ctx.Err() == nil {
			item, err := w.Repo.NextUpdate(ctx)
			if errors.Is(err, model.ErrNoUpdates) {
				break
			}
			if err != nil {
				log.Printf("load queued update: %v", err)
				break
			}
			log.Printf("update dequeued update_id=%d", item.UpdateID)
			if err := w.Process(ctx, item); err != nil {
				log.Printf("process update %d: %v", item.UpdateID, err)
				break
			}
			if err := w.Repo.MarkUpdateProcessed(ctx, item.UpdateID); err != nil {
				log.Printf("mark update %d processed: %v", item.UpdateID, err)
				break
			}
			log.Printf("update processed update_id=%d", item.UpdateID)
		}
	}
}

func (w *Worker) Process(ctx context.Context, item model.Update) error {
	if item.Message == nil {
		log.Printf("update skipped update_id=%d reason=no_message", item.UpdateID)
		return nil
	}
	if item.Message.From != nil && item.Message.From.IsBot && item.Message.From.ID == w.BotID {
		log.Printf("update skipped update_id=%d reason=own_bot_message", item.UpdateID)
		return nil
	}
	msg := *item.Message
	now := time.Now()
	log.Printf("message received update_id=%d chat_id=%d thread_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageThreadID, msg.MessageID)
	if err := w.Repo.AddIncoming(ctx, msg, now); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d save incoming message: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("message stored update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
	if msg.From != nil && msg.From.IsBot {
		log.Printf("AI decision skipped update_id=%d reason=other_bot_message", item.UpdateID)
		return nil
	}
	if msg.Date != 0 && time.Unix(msg.Date, 0).Before(now.Add(-model.ContextLifetime)) {
		log.Printf("AI decision skipped update_id=%d reason=message_older_than_two_hours", item.UpdateID)
		return nil
	}
	history, err := w.Repo.Conversation(ctx, msg.Chat.ID, msg.MessageThreadID, now)
	if err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d load conversation: %w", msg.Chat.ID, msg.MessageID, err)
	}
	request := model.DecisionRequest{
		BotUsername:      w.Username,
		CurrentMessageID: msg.MessageID,
		History:          history,
	}
	if msg.ReplyToMessage != nil {
		request.CurrentReplyToMessageID = msg.ReplyToMessage.MessageID
		request.CurrentRepliedToBot = w.BotID != 0 && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID == w.BotID
	}
	log.Printf("AI decision started update_id=%d chat_id=%d context_messages=%d reply_to_bot=%t", item.UpdateID, msg.Chat.ID, len(history), request.CurrentRepliedToBot)
	started := time.Now()
	decision, err := w.AI.Ask(ctx, request)
	if err != nil {
		log.Printf("AI decision failed update_id=%d chat_id=%d duration=%s error=%v", item.UpdateID, msg.Chat.ID, time.Since(started), err)
		return nil
	}
	if decision.Reply == "" {
		log.Printf("AI chose silence update_id=%d chat_id=%d duration=%s", item.UpdateID, msg.Chat.ID, time.Since(started))
		return nil
	}
	validTarget := false
	for _, entry := range history {
		if !entry.Bot && entry.MessageID == decision.ReplyToMessageID && entry.MessageID > 0 {
			validTarget = true
			break
		}
	}
	if !validTarget {
		log.Printf("AI selected invalid reply target update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
		return nil
	}
	log.Printf("AI chose reply update_id=%d chat_id=%d reply_to_message_id=%d duration=%s", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID, time.Since(started))
	target := msg
	target.MessageID = decision.ReplyToMessageID
	if err := w.Telegram.SendMessage(ctx, target, decision.Reply); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d send Telegram reply: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("Telegram reply sent update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
	if err := w.Repo.AddBotReply(ctx, msg.Chat.ID, msg.MessageThreadID, decision.ReplyToMessageID, w.Username, decision.Reply, time.Now()); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d save bot reply: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("bot reply stored update_id=%d chat_id=%d", item.UpdateID, msg.Chat.ID)
	return nil
}
