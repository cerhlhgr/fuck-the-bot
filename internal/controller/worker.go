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
	SendVoice(context.Context, model.Message, []byte) error
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

type MusicStarter interface {
	Start(context.Context, model.Message, model.MusicRequest) (bool, error)
}

type VoiceSynthesizer interface {
	Synthesize(context.Context, model.VoiceRequest) ([]byte, error)
}

type Worker struct {
	Repo        model.Repository
	ActionPlans model.ActionPlanStore
	AI          AI
	Telegram    Messenger
	Images      ImageSearcher
	Photos      PhotoDownloader
	Vision      PhotoAnalyzer
	Music       MusicStarter
	Voice       VoiceSynthesizer
	BotID       int64
	Username    string
	photoMu     sync.Mutex
	photoCache  map[[32]byte]photoCacheEntry
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

const batchPageSize = 500

func (w *Worker) Run(ctx context.Context, interval time.Duration) {
	log.Printf("scheduled decision worker started interval=%s", interval)
	defer log.Print("scheduled decision worker stopped")
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	next := time.Now().Truncate(interval).Add(interval)
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-cleanup.C:
			if err := w.Repo.Prune(ctx, time.Now()); err != nil {
				log.Printf("prune old records: %v", err)
			}
		case <-timer.C:
			if err := w.RunOnce(ctx); err != nil {
				log.Printf("scheduled decision run failed: %v", err)
			}
			next = time.Now().Truncate(interval).Add(interval)
			timer.Reset(time.Until(next))
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) error {
	unlock, acquired, err := w.Repo.TryDecisionLock(ctx)
	if err != nil {
		return fmt.Errorf("acquire decision lock: %w", err)
	}
	if !acquired {
		log.Print("scheduled decision run skipped reason=another_worker_active")
		return nil
	}
	defer func() {
		if err := unlock(); err != nil {
			log.Printf("release decision lock: %v", err)
		}
	}()
	updates, err := w.Repo.PendingUpdates(ctx, 0, batchPageSize)
	if err != nil {
		return fmt.Errorf("load pending updates: %w", err)
	}
	if len(updates) == 0 {
		return nil
	}
	type dialogue struct{ chatID, threadID int64 }
	groups := make(map[dialogue][]model.Update)
	var order []dialogue
	for _, item := range updates {
		key := dialogue{}
		if item.Message != nil {
			key = dialogue{item.Message.Chat.ID, item.Message.MessageThreadID}
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], item)
	}
	for _, key := range order {
		items := groups[key]
		processedIDs, err := w.processBatch(ctx, items)
		if err != nil {
			log.Printf("process dialogue batch chat_id=%d thread_id=%d first_update_id=%d count=%d: %v", key.chatID, key.threadID, items[0].UpdateID, len(items), err)
			continue
		}
		if err := w.Repo.MarkUpdatesProcessed(ctx, processedIDs); err != nil {
			return fmt.Errorf("mark dialogue batch processed: %w", err)
		}
		log.Printf("dialogue batch processed chat_id=%d thread_id=%d updates=%d", key.chatID, key.threadID, len(processedIDs))
	}
	return nil
}

// Process keeps the single-update entry point useful for focused tests and callers.
func (w *Worker) Process(ctx context.Context, item model.Update) error {
	return w.ProcessBatch(ctx, []model.Update{item})
}

func (w *Worker) ProcessBatch(ctx context.Context, items []model.Update) error {
	_, err := w.processBatch(ctx, items)
	return err
}

func (w *Worker) processBatch(ctx context.Context, items []model.Update) ([]int64, error) {
	if len(items) == 0 {
		return nil, nil
	}
	updateIDs := make([]int64, 0, len(items))
	for _, pending := range items {
		updateIDs = append(updateIDs, pending.UpdateID)
	}
	firstUpdateID := items[0].UpdateID
	if w.ActionPlans != nil && items[0].Message != nil {
		first := items[0].Message
		plan, found, err := w.ActionPlans.LoadActionPlan(ctx, first.Chat.ID, first.MessageThreadID, firstUpdateID)
		if err != nil {
			return nil, fmt.Errorf("load action plan: %w", err)
		}
		if found {
			log.Printf("resuming action plan chat_id=%d thread_id=%d first_update_id=%d completed=%d total=%d", first.Chat.ID, first.MessageThreadID, firstUpdateID, plan.Completed, len(plan.Actions))
			if err := w.executePlan(ctx, *first, firstUpdateID, plan); err != nil {
				return nil, err
			}
			return plan.UpdateIDs, nil
		}
	}
	now := time.Now()
	var messages []model.Message
	var item model.Update
	for _, pending := range items {
		if pending.Message == nil {
			log.Printf("update skipped update_id=%d reason=no_message", pending.UpdateID)
			continue
		}
		if pending.Message.From != nil && pending.Message.From.IsBot && pending.Message.From.ID == w.BotID {
			log.Printf("update skipped update_id=%d reason=own_bot_message", pending.UpdateID)
			continue
		}
		msg := *pending.Message
		log.Printf("message loaded update_id=%d chat_id=%d thread_id=%d message_id=%d", pending.UpdateID, msg.Chat.ID, msg.MessageThreadID, msg.MessageID)
		_, mentioned := model.MentionedText(msg, w.Username)
		if mentioned && len(msg.Photo) > 0 && (msg.From == nil || !msg.From.IsBot) && (msg.Date == 0 || !time.Unix(msg.Date, 0).Before(now.Add(-model.ContextLifetime))) {
			log.Printf("photo analysis started update_id=%d chat_id=%d message_id=%d", pending.UpdateID, msg.Chat.ID, msg.MessageID)
			if w.Photos == nil || w.Vision == nil {
				log.Printf("photo analysis unavailable update_id=%d reason=not_configured", pending.UpdateID)
			} else if photo, err := w.Photos.DownloadPhoto(ctx, msg.Photo); err != nil {
				log.Printf("photo download failed update_id=%d chat_id=%d message_id=%d error=%v", pending.UpdateID, msg.Chat.ID, msg.MessageID, err)
			} else {
				hash := sha256.Sum256(photo)
				if description, ok := w.cachedPhoto(hash, now); ok {
					msg.PhotoDescription = description
					log.Printf("photo analysis cache hit update_id=%d chat_id=%d message_id=%d", pending.UpdateID, msg.Chat.ID, msg.MessageID)
				} else if description, err := w.Vision.DescribePhoto(ctx, photo); err != nil {
					log.Printf("photo analysis failed update_id=%d chat_id=%d message_id=%d error=%v", pending.UpdateID, msg.Chat.ID, msg.MessageID, err)
				} else {
					msg.PhotoDescription = description
					w.rememberPhoto(hash, description, now)
					log.Printf("photo analysis completed update_id=%d chat_id=%d message_id=%d", pending.UpdateID, msg.Chat.ID, msg.MessageID)
				}
			}
		}
		if err := w.Repo.AddIncoming(ctx, msg, now); err != nil {
			return nil, fmt.Errorf("chat_id=%d message_id=%d save incoming message: %w", msg.Chat.ID, msg.MessageID, err)
		}
		if msg.From != nil && msg.From.IsBot {
			log.Printf("AI decision skipped update_id=%d reason=other_bot_message", pending.UpdateID)
			continue
		}
		if msg.Date != 0 && time.Unix(msg.Date, 0).Before(now.Add(-model.ContextLifetime)) {
			log.Printf("AI decision skipped update_id=%d reason=message_older_than_one_hour", pending.UpdateID)
			continue
		}
		if !mentioned {
			log.Printf("AI decision skipped update_id=%d reason=bot_not_mentioned", pending.UpdateID)
			continue
		}
		if len(messages) > 0 && (messages[0].Chat.ID != msg.Chat.ID || messages[0].MessageThreadID != msg.MessageThreadID) {
			return nil, errors.New("dialogue batch spans multiple chats or topics")
		}
		messages = append(messages, msg)
		item = pending
	}
	if len(messages) == 0 {
		return updateIDs, nil
	}
	msg := messages[len(messages)-1]
	history, err := w.Repo.Conversation(ctx, msg.Chat.ID, msg.MessageThreadID, now)
	if err != nil {
		return nil, fmt.Errorf("chat_id=%d load conversation: %w", msg.Chat.ID, err)
	}
	selectedHistory := selectDecisionHistory(history, messages)
	important, err := w.Repo.ImportantContext(ctx, msg.Chat.ID, msg.MessageThreadID)
	if err != nil {
		return nil, fmt.Errorf("chat_id=%d load important context: %w", msg.Chat.ID, err)
	}
	request := model.DecisionRequest{
		BotUsername:      w.Username,
		CurrentMessageID: msg.MessageID,
		History:          selectedHistory,
		Important:        important,
		MusicEnabled:     w.Music != nil,
		VoiceEnabled:     w.Voice != nil,
	}
	for _, current := range messages {
		request.NewMessageIDs = append(request.NewMessageIDs, current.MessageID)
		if current.ReplyToMessage != nil && w.BotID != 0 && current.ReplyToMessage.From != nil && current.ReplyToMessage.From.ID == w.BotID {
			request.NewReplyToBotIDs = append(request.NewReplyToBotIDs, current.MessageID)
		}
	}
	if msg.ReplyToMessage != nil {
		request.CurrentReplyToMessageID = msg.ReplyToMessage.MessageID
		request.CurrentRepliedToBot = w.BotID != 0 && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID == w.BotID
	}
	log.Printf("AI decision started chat_id=%d thread_id=%d new_messages=%d context_messages=%d selected_messages=%d important_entries=%d", msg.Chat.ID, msg.MessageThreadID, len(messages), len(history), len(selectedHistory), len(important))
	started := time.Now()
	decision, err := w.AI.Ask(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("chat_id=%d AI decision failed after %s: %w", msg.Chat.ID, time.Since(started), err)
	}
	updates := append([]model.ImportantUpdate(nil), decision.ImportantUpdates...)
	if summary := strings.TrimSpace(decision.Important); summary != "" {
		kind := decision.ImportantKind
		if kind == "" {
			kind = "fact"
		}
		updates = append(updates, model.ImportantUpdate{SourceMessageID: msg.MessageID, Summary: summary, Kind: kind})
	}
	if len(updates) > 0 || len(decision.ForgetImportantIDs) > 0 {
		validIDs := make(map[int64]bool, len(important))
		for _, entry := range important {
			validIDs[entry.SourceMessageID] = true
		}
		for _, id := range decision.ForgetImportantIDs {
			if !validIDs[id] {
				log.Printf("AI selected invalid important context ID chat_id=%d thread_id=%d source_message_id=%d", msg.Chat.ID, msg.MessageThreadID, id)
				return updateIDs, nil
			}
		}
		newIDs := make(map[int64]bool, len(messages))
		for _, current := range messages {
			newIDs[current.MessageID] = true
		}
		seen := make(map[int64]bool, len(updates))
		for _, update := range updates {
			if !newIDs[update.SourceMessageID] || seen[update.SourceMessageID] {
				log.Printf("AI selected invalid important update chat_id=%d source_message_id=%d", msg.Chat.ID, update.SourceMessageID)
				return updateIDs, nil
			}
			seen[update.SourceMessageID] = true
		}
		if err := w.Repo.ApplyImportantBatch(ctx, messages, updates, decision.ForgetImportantIDs, now); err != nil {
			return nil, fmt.Errorf("chat_id=%d update important context: %w", msg.Chat.ID, err)
		}
		log.Printf("important context updated chat_id=%d thread_id=%d forgotten=%d stored=%d", msg.Chat.ID, msg.MessageThreadID, len(decision.ForgetImportantIDs), len(updates))
	}
	actions := decision.Actions
	if len(actions) == 0 && decisionAction(decision) != "silence" {
		actions = []model.Decision{decision}
	}
	validActions := make([]model.Decision, 0, len(actions))
	for _, action := range actions {
		action.Action = decisionAction(action)
		if validActionTarget(action, selectedHistory, messages) {
			validActions = append(validActions, action)
		} else {
			log.Printf("AI selected invalid action target update_id=%d chat_id=%d action=%s reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, action.Action, action.ReplyToMessageID)
		}
	}
	plan := model.ActionPlan{UpdateIDs: updateIDs, Actions: validActions}
	if w.ActionPlans != nil {
		plan, err = w.ActionPlans.SaveActionPlan(ctx, msg.Chat.ID, msg.MessageThreadID, firstUpdateID, plan)
		if err != nil {
			return nil, fmt.Errorf("save action plan: %w", err)
		}
	}
	log.Printf("AI chose actions chat_id=%d thread_id=%d count=%d duration=%s", msg.Chat.ID, msg.MessageThreadID, len(plan.Actions), time.Since(started))
	if err := w.executePlan(ctx, msg, firstUpdateID, plan); err != nil {
		return nil, err
	}
	return plan.UpdateIDs, nil
}

func decisionAction(decision model.Decision) string {
	if decision.Action != "" {
		return decision.Action
	}
	if decision.Poll != nil {
		return "poll"
	}
	if decision.Reply != "" {
		return "reply"
	}
	return "silence"
}

func validActionTarget(action model.Decision, history []model.HistoryEntry, messages []model.Message) bool {
	id := action.ReplyToMessageID
	if id < 0 {
		return false
	}
	if id == 0 {
		return action.Action == "message" || action.Action == "poll" || action.Action == "image"
	}
	found := false
	for _, entry := range history {
		if !entry.Bot && entry.MessageID == id {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if action.Action == "voice" || action.Action == "music" {
		for _, message := range messages {
			if message.MessageID == id {
				return true
			}
		}
		return false
	}
	return true
}

func (w *Worker) executePlan(ctx context.Context, msg model.Message, firstUpdateID int64, plan model.ActionPlan) error {
	for index := plan.Completed; index < len(plan.Actions); index++ {
		if err := w.executeAction(ctx, msg, firstUpdateID, plan.Actions[index]); err != nil {
			return fmt.Errorf("action %d of %d: %w", index+1, len(plan.Actions), err)
		}
		if w.ActionPlans != nil {
			if err := w.ActionPlans.MarkActionCompleted(ctx, msg.Chat.ID, msg.MessageThreadID, firstUpdateID, index); err != nil {
				return fmt.Errorf("mark action %d completed: %w", index, err)
			}
		}
	}
	return nil
}

func (w *Worker) executeAction(ctx context.Context, msg model.Message, updateID int64, decision model.Decision) error {
	item := model.Update{UpdateID: updateID}
	started := time.Now()
	action := decisionAction(decision)
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
	case "music":
		if decision.Music == nil {
			return errors.New("AI selected music without parameters")
		}
		if w.Music == nil {
			storedText = "Сейчас не могу создать трек: генерация музыки не настроена."
		} else {
			created, err := w.Music.Start(ctx, target, *decision.Music)
			if errors.Is(err, ErrMusicUpstream) {
				log.Printf("Suno generation failed chat_id=%d message_id=%d error=%v", msg.Chat.ID, target.MessageID, err)
				storedText = "Не удалось запустить генерацию трека. Попробуй ещё раз позже."
			} else if err != nil {
				return fmt.Errorf("chat_id=%d start music: %w", msg.Chat.ID, err)
			} else if !created {
				log.Printf("Suno generation already exists chat_id=%d source_message_id=%d", msg.Chat.ID, target.MessageID)
				return nil
			} else {
				storedText = "Запустил генерацию трека. Пришлю сюда, когда будет готов."
			}
		}
		if err := w.Telegram.SendMessage(ctx, target, storedText); err != nil {
			return fmt.Errorf("chat_id=%d send music status: %w", msg.Chat.ID, err)
		}
	case "voice":
		if decision.Voice == nil {
			return errors.New("AI selected voice without parameters")
		}
		sentVoice := false
		if w.Voice == nil {
			storedText = "Сейчас не могу отправить голосовое."
		} else if audio, err := w.Voice.Synthesize(ctx, *decision.Voice); err != nil {
			log.Printf("Timeweb TTS failed chat_id=%d message_id=%d error=%v", msg.Chat.ID, target.MessageID, err)
			storedText = "Не получилось записать голосовое. Попробуй ещё раз позже."
		} else {
			if err := w.Telegram.SendVoice(ctx, target, audio); err != nil {
				return fmt.Errorf("chat_id=%d message_id=%d send Telegram voice: %w", msg.Chat.ID, target.MessageID, err)
			}
			storedText = "Голосовое: " + decision.Voice.Text
			sentVoice = true
			log.Printf("Telegram voice sent update_id=%d chat_id=%d reply_to_message_id=%d bytes=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID, len(audio))
		}
		if !sentVoice {
			if err := w.Telegram.SendMessage(ctx, target, storedText); err != nil {
				return fmt.Errorf("chat_id=%d message_id=%d send voice error: %w", msg.Chat.ID, target.MessageID, err)
			}
		}
	default:
		return fmt.Errorf("unknown AI action %q", action)
	}
	if err := w.Repo.AddBotReply(ctx, msg.Chat.ID, msg.MessageThreadID, decision.ReplyToMessageID, w.Username, storedText, time.Now()); err != nil {
		return fmt.Errorf("chat_id=%d message_id=%d save bot response: %w", msg.Chat.ID, msg.MessageID, err)
	}
	log.Printf("bot response stored update_id=%d chat_id=%d", item.UpdateID, msg.Chat.ID)
	return nil
}
