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
			if err := w.Process(ctx, item); err != nil {
				log.Printf("process update %d: %v", item.UpdateID, err)
				break
			}
			if err := w.Repo.MarkUpdateProcessed(ctx, item.UpdateID); err != nil {
				log.Printf("mark update %d processed: %v", item.UpdateID, err)
				break
			}
		}
	}
}

func (w *Worker) Process(ctx context.Context, item model.Update) error {
	if item.Message == nil || item.Message.From != nil && item.Message.From.IsBot {
		return nil
	}
	msg := *item.Message
	prompt, mentioned := model.MentionedText(msg, w.Username)
	var prior []model.HistoryEntry
	if mentioned {
		var err error
		prior, err = w.Repo.Conversation(ctx, msg.Chat.ID, msg.MessageThreadID, time.Now())
		if err != nil {
			return fmt.Errorf("load conversation: %w", err)
		}
	}
	if err := w.Repo.AddIncoming(ctx, msg, time.Now()); err != nil {
		return fmt.Errorf("save incoming message: %w", err)
	}
	if !mentioned {
		return nil
	}
	answer, err := w.AI.Ask(ctx, prompt, prior)
	if err != nil {
		log.Printf("AI request for update %d: %v", item.UpdateID, err)
		answer = "Не смог сейчас ответить: ИИ недоступен. Попробуй позже."
	}
	if err := w.Telegram.SendMessage(ctx, msg, answer); err != nil {
		return fmt.Errorf("send Telegram reply: %w", err)
	}
	if err := w.Repo.AddBotReply(ctx, msg.Chat.ID, msg.MessageThreadID, w.Username, answer, time.Now()); err != nil {
		return fmt.Errorf("save bot reply: %w", err)
	}
	return nil
}
