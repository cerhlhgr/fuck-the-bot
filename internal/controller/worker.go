package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"fuck-the-bot/internal/model"
)

type AI interface {
	Ask(context.Context, model.DecisionRequest) (model.Decision, error)
}

type Messenger interface {
	SendMessage(context.Context, model.Message, string) error
	SendPoll(context.Context, model.Message, model.Poll) error
	SetReaction(context.Context, model.Message, string) error
}

type ImageSearcher interface {
	Search(context.Context, string) (string, error)
}

type PhotoDownloader interface {
	DownloadPhoto(context.Context, []model.PhotoSize) ([]byte, error)
}

type PhotoAnalyzer interface {
	DescribePhoto(context.Context, []byte) (string, error)
}

type Worker struct {
	Repo       model.Repository
	AI         AI
	Telegram   Messenger
	Images     ImageSearcher
	Photos     PhotoDownloader
	Vision     PhotoAnalyzer
	BotID      int64
	Username   string
	photoMu    sync.Mutex
	photoCache map[[32]byte]photoCacheEntry
}

const photoCacheLifetime = 24 * time.Hour
const maxCachedPhotos = 1024

type photoCacheEntry struct {
	description string
	createdAt   time.Time
}

func (w *Worker) cachedPhoto(hash [32]byte, now time.Time) (string, bool) {
	w.photoMu.Lock()
	defer w.photoMu.Unlock()
	entry, ok := w.photoCache[hash]
	return entry.description, ok && now.Sub(entry.createdAt) < photoCacheLifetime
}

func (w *Worker) rememberPhoto(hash [32]byte, description string, now time.Time) {
	w.photoMu.Lock()
	defer w.photoMu.Unlock()
	if w.photoCache == nil {
		w.photoCache = make(map[[32]byte]photoCacheEntry)
	}
	if _, exists := w.photoCache[hash]; !exists && len(w.photoCache) >= maxCachedPhotos {
		var oldestHash [32]byte
		var oldestTime time.Time
		for key, entry := range w.photoCache {
			if oldestTime.IsZero() || entry.createdAt.Before(oldestTime) {
				oldestHash, oldestTime = key, entry.createdAt
			}
		}
		delete(w.photoCache, oldestHash)
	}
	w.photoCache[hash] = photoCacheEntry{description: description, createdAt: now}
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
	if len(msg.Photo) > 0 && (msg.From == nil || !msg.From.IsBot) && (msg.Date == 0 || !time.Unix(msg.Date, 0).Before(now.Add(-model.ContextLifetime))) {
		log.Printf("photo analysis started update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
		if w.Photos == nil || w.Vision == nil {
			log.Printf("photo analysis unavailable update_id=%d reason=not_configured", item.UpdateID)
		} else if photo, err := w.Photos.DownloadPhoto(ctx, msg.Photo); err != nil {
			log.Printf("photo download failed update_id=%d chat_id=%d message_id=%d error=%v", item.UpdateID, msg.Chat.ID, msg.MessageID, err)
		} else {
			hash := sha256.Sum256(photo)
			if description, ok := w.cachedPhoto(hash, now); ok {
				msg.PhotoDescription = description
				log.Printf("photo analysis cache hit update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
			} else if description, err := w.Vision.DescribePhoto(ctx, photo); err != nil {
				log.Printf("photo analysis failed update_id=%d chat_id=%d message_id=%d error=%v", item.UpdateID, msg.Chat.ID, msg.MessageID, err)
			} else {
				msg.PhotoDescription = description
				w.rememberPhoto(hash, description, now)
				log.Printf("photo analysis completed update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
			}
		}
	}
	if err := w.Repo.AddIncoming(ctx, msg, now); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d save incoming message: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("message stored update_id=%d chat_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageID)
	if msg.From != nil && msg.From.IsBot {
		log.Printf("AI decision skipped update_id=%d reason=other_bot_message", item.UpdateID)
		return nil
	}
	if msg.Date != 0 && time.Unix(msg.Date, 0).Before(now.Add(-model.ContextLifetime)) {
		log.Printf("AI decision skipped update_id=%d reason=message_older_than_one_hour", item.UpdateID)
		return nil
	}
	history, err := w.Repo.Conversation(ctx, msg.Chat.ID, msg.MessageThreadID, now)
	if err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d load conversation: %w", msg.Chat.ID, msg.MessageID, err)
	}
	important, err := w.Repo.ImportantContext(ctx, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d load important context: %w", msg.Chat.ID, msg.MessageID, err)
	}
	request := model.DecisionRequest{
		BotUsername:      w.Username,
		CurrentMessageID: msg.MessageID,
		History:          history,
		Important:        important,
	}
	if msg.ReplyToMessage != nil {
		request.CurrentReplyToMessageID = msg.ReplyToMessage.MessageID
		request.CurrentRepliedToBot = w.BotID != 0 && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID == w.BotID
	}
	log.Printf("AI decision started update_id=%d chat_id=%d context_messages=%d important_entries=%d reply_to_bot=%t", item.UpdateID, msg.Chat.ID, len(history), len(important), request.CurrentRepliedToBot)
	started := time.Now()
	decision, err := w.AI.Ask(ctx, request)
	if err != nil {
		log.Printf("AI decision failed update_id=%d chat_id=%d duration=%s error=%v", item.UpdateID, msg.Chat.ID, time.Since(started), err)
		return nil
	}
	if summary := strings.TrimSpace(decision.Important); summary != "" {
		kind := decision.ImportantKind
		if kind == "" {
			kind = "fact"
		}
		if err := w.Repo.AddImportant(ctx, msg, summary, kind, now); err != nil {
			return fmt.Errorf("chat_id=%d message_id=%d save important context: %w", msg.Chat.ID, msg.MessageID, err)
		}
		log.Printf("important context stored update_id=%d chat_id=%d thread_id=%d message_id=%d", item.UpdateID, msg.Chat.ID, msg.MessageThreadID, msg.MessageID)
	}
	action := decision.Action
	if action == "" { // Older callers can still construct a decision without an explicit action.
		switch {
		case decision.Poll != nil:
			action = "poll"
		case decision.Reply != "":
			action = "reply"
		default:
			action = "silence"
		}
	}
	if action == "silence" {
		log.Printf("AI chose silence update_id=%d chat_id=%d duration=%s", item.UpdateID, msg.Chat.ID, time.Since(started))
		return nil
	}
	if decision.ReplyToMessageID > 0 {
		validTarget := false
		for _, entry := range history {
			if !entry.Bot && entry.MessageID == decision.ReplyToMessageID {
				validTarget = true
				break
			}
		}
		if !validTarget {
			log.Printf("AI selected invalid reply target update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
			return nil
		}
	} else if action == "reply" || action == "reaction" || decision.ReplyToMessageID < 0 {
		log.Printf("AI selected invalid reply target update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
		return nil
	}
	target := msg
	target.MessageID = decision.ReplyToMessageID
	var storedText string
	log.Printf("AI chose action update_id=%d chat_id=%d action=%s reply_to_message_id=%d duration=%s", item.UpdateID, msg.Chat.ID, action, decision.ReplyToMessageID, time.Since(started))
	switch action {
	case "poll":
		if decision.Poll == nil {
			return fmt.Errorf("AI selected a poll without poll data")
		}
		log.Printf("AI chose poll update_id=%d chat_id=%d reply_to_message_id=%d duration=%s", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID, time.Since(started))
		if err := w.Telegram.SendPoll(ctx, target, *decision.Poll); err != nil {
			return fmt.Errorf("chat_id=%d message_id=%d send Telegram poll: %w", msg.Chat.ID, msg.MessageID, err)
		}
		storedText = "Опрос: " + decision.Poll.Question + "\nВарианты: " + strings.Join(decision.Poll.Options, "; ")
		log.Printf("Telegram poll sent update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
	case "reply", "message":
		if err := w.Telegram.SendMessage(ctx, target, decision.Reply); err != nil {
			return fmt.Errorf("chat_id=%d message_id=%d send Telegram message: %w", msg.Chat.ID, msg.MessageID, err)
		}
		storedText = decision.Reply
		log.Printf("Telegram message sent update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
	case "image":
		if w.Images == nil {
			return errors.New("image searcher is not configured")
		}
		imageURL, err := w.Images.Search(ctx, decision.ImageQuery)
		if err != nil {
			log.Printf("image search failed update_id=%d chat_id=%d error=%v", item.UpdateID, msg.Chat.ID, err)
			storedText = "Картинку по запросу не нашёл."
		} else {
			storedText = strings.TrimSpace(decision.Caption)
			if storedText != "" {
				storedText += "\n"
			}
			storedText += imageURL
		}
		if err := w.Telegram.SendMessage(ctx, target, storedText); err != nil {
			return fmt.Errorf("chat_id=%d message_id=%d send image link: %w", msg.Chat.ID, msg.MessageID, err)
		}
		log.Printf("Telegram image link sent update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
	case "reaction":
		if err := w.Telegram.SetReaction(ctx, target, decision.Reaction); err != nil {
			log.Printf("Telegram reaction unavailable update_id=%d chat_id=%d reply_to_message_id=%d error=%v", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID, err)
			return nil
		}
		storedText = "Реакция: " + decision.Reaction
		log.Printf("Telegram reaction set update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
	default:
		return fmt.Errorf("unknown AI action %q", action)
	}
	if err := w.Repo.AddBotReply(ctx, msg.Chat.ID, msg.MessageThreadID, decision.ReplyToMessageID, w.Username, storedText, time.Now()); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d save bot response: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("bot response stored update_id=%d chat_id=%d", item.UpdateID, msg.Chat.ID)
	return nil
}
