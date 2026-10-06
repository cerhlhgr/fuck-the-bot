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
	Ask(context.Context, string, []model.HistoryEntry) (string, error)
}

type Messenger interface {
	SendMessage(context.Context, model.Message, string) error
}

type Worker struct {
	Repo     model.Repository
	AI       AI
	Telegram Messenger
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
	if item.Message.From != nil && item.Message.From.IsBot {
		log.Printf("update skipped update_id=%d reason=bot_message", item.UpdateID)
		return nil
	}
	msg := *item.Message
	prompt, mentioned := model.MentionedText(msg, w.Username)
	log.Printf("message received update_id=%d chat_id=%d thread_id=%d message_id=%d mentioned=%t", item.UpdateID, msg.Chat.ID, msg.MessageThreadID, msg.MessageID, mentioned)
	var prior []model.HistoryEntry
	if mentioned {
		var err error
		prior, err = w.Repo.Conversation(ctx, msg.Chat.ID, msg.MessageThreadID, msg.MessageID, time.Now())
		if err != nil {
			return fmt.Errorf("chat_id=%d message_id=%d load conversation: %w", msg.Chat.ID, msg.MessageID, err)
		}
	}
	if err := w.Repo.AddIncoming(ctx, msg, time.Now()); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d save incoming message: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("message stored update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
	if !mentioned {
		return nil
	}
	log.Printf("AI request started update_id=%d chat_id=%d context_messages=%d", item.UpdateID, msg.Chat.ID, len(prior))
	started := time.Now()
	answer, err := w.AI.Ask(ctx, prompt, prior)
	if err != nil {
		log.Printf("AI request failed update_id=%d chat_id=%d duration=%s error=%v", item.UpdateID, msg.Chat.ID, time.Since(started), err)
		answer = "Не смог сейчас ответить: ИИ недоступен. Попробуй позже."
	} else {
		log.Printf("AI request completed update_id=%d chat_id=%d duration=%s", item.UpdateID, msg.Chat.ID, time.Since(started))
	}
	if err := w.Telegram.SendMessage(ctx, msg, answer); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d send Telegram reply: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("Telegram reply sent update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
	if err := w.Repo.AddBotReply(ctx, msg.Chat.ID, msg.MessageThreadID, w.Username, answer, time.Now()); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d save bot reply: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("bot reply stored update_id=%d chat_id=%d", item.UpdateID, msg.Chat.ID)
	return nil
}
