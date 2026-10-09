package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"sort"
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
	SendContactMention(context.Context, model.Message, model.Contact, string) error
	SendVoice(context.Context, model.Message, []byte) error
	SendPoll(context.Context, model.Message, model.Poll) error
	SetReaction(context.Context, model.Message, string) error
}

type ImageSearcher interface {
	Search(context.Context, string) (string, error)
}

type WebSearcher interface {
	Search(context.Context, string, string) ([]model.SearchResult, error)
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
	Search      WebSearcher
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

func (w *Worker) describeCandidatePhoto(ctx context.Context, msg model.Message, updateID int64, now time.Time) model.Message {
	log.Printf("photo analysis started update_id=%d chat_id=%d message_id=%d", updateID, msg.Chat.ID, msg.MessageID)
	if w.Photos == nil || w.Vision == nil {
		log.Printf("photo analysis unavailable update_id=%d reason=not_configured", updateID)
		return msg
	}
	photo, err := w.Photos.DownloadPhoto(ctx, msg.Photo)
	if err != nil {
		log.Printf("photo download failed update_id=%d chat_id=%d message_id=%d error=%v", updateID, msg.Chat.ID, msg.MessageID, err)
		return msg
	}
	hash := sha256.Sum256(photo)
	if description, ok := w.cachedPhoto(hash, now); ok {
		msg.PhotoDescription = description
		log.Printf("photo analysis cache hit update_id=%d chat_id=%d message_id=%d", updateID, msg.Chat.ID, msg.MessageID)
		return msg
	}
	description, err := w.Vision.DescribePhoto(ctx, photo)
	if err != nil {
		log.Printf("photo analysis failed update_id=%d chat_id=%d message_id=%d error=%v", updateID, msg.Chat.ID, msg.MessageID, err)
		return msg
	}
	msg.PhotoDescription = description
	w.rememberPhoto(hash, description, now)
	log.Printf("photo analysis completed update_id=%d chat_id=%d message_id=%d", updateID, msg.Chat.ID, msg.MessageID)
	return msg
}

const batchPageSize = 500
const maxDecisionCandidates = 32

func (w *Worker) repliedToBot(msg model.Message) bool {
	return w.BotID != 0 && msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil && msg.ReplyToMessage.From.ID == w.BotID
}

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
	for i := range updates {
		if updates[i].Message != nil {
			updates[i].Message.NormalizeThread()
		}
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
		for start := 0; start < len(items); start += maxDecisionCandidates {
			end := start + maxDecisionCandidates
			if end > len(items) {
				end = len(items)
			}
			batch := items[start:end]
			processedIDs, err := w.processBatch(ctx, batch)
			if err != nil {
				log.Printf("process dialogue batch chat_id=%d thread_id=%d first_update_id=%d count=%d: %v", key.chatID, key.threadID, batch[0].UpdateID, len(batch), err)
				break
			}
			if err := w.Repo.MarkUpdatesProcessed(ctx, processedIDs); err != nil {
				return fmt.Errorf("mark dialogue batch processed: %w", err)
			}
			log.Printf("dialogue batch processed chat_id=%d thread_id=%d updates=%d", key.chatID, key.threadID, len(processedIDs))
		}
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
	for i := range items {
		if items[i].Message != nil {
			items[i].Message.NormalizeThread()
		}
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
			if err := w.Repo.MarkUpdatesConsidered(ctx, plan.ConsideredUpdateIDs); err != nil {
				return nil, fmt.Errorf("mark decision candidates considered: %w", err)
			}
			return plan.UpdateIDs, nil
		}
	}
	now := time.Now()
	var batchCandidates []model.Update
	var latest model.Update
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
		if len(batchCandidates) > 0 && (batchCandidates[0].Message.Chat.ID != msg.Chat.ID || batchCandidates[0].Message.MessageThreadID != msg.MessageThreadID) {
			return nil, errors.New("dialogue batch spans multiple chats or topics")
		}
		pending.Message = &msg
		batchCandidates = append(batchCandidates, pending)
		latest = pending
	}
	if latest.Message == nil {
		return updateIDs, nil
	}
	msg := *latest.Message
	if len(batchCandidates) > maxDecisionCandidates {
		return nil, fmt.Errorf("chat_id=%d decision batch exceeds %d messages", msg.Chat.ID, maxDecisionCandidates)
	}
	var olderCandidates []model.Update
	if remaining := maxDecisionCandidates - len(batchCandidates); remaining > 0 {
		var err error
		olderCandidates, err = w.Repo.UnconsideredUpdates(ctx, msg.Chat.ID, msg.MessageThreadID, msg.MessageID, now, remaining)
		if err != nil {
			return nil, fmt.Errorf("chat_id=%d load decision candidates: %w", msg.Chat.ID, err)
		}
	}
	byUpdateID := make(map[int64]model.Update, len(olderCandidates)+len(batchCandidates))
	for _, candidate := range olderCandidates {
		byUpdateID[candidate.UpdateID] = candidate
	}
	for _, candidate := range batchCandidates {
		byUpdateID[candidate.UpdateID] = candidate
	}
	candidates := make([]model.Update, 0, len(byUpdateID))
	for _, candidate := range byUpdateID {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Message.MessageID == candidates[j].Message.MessageID {
			return candidates[i].UpdateID < candidates[j].UpdateID
		}
		return candidates[i].Message.MessageID < candidates[j].Message.MessageID
	})
	messages := make([]model.Message, 0, len(candidates))
	consideredUpdateIDs := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		current := *candidate.Message
		if len(current.Photo) > 0 {
			current = w.describeCandidatePhoto(ctx, current, candidate.UpdateID, now)
			if current.PhotoDescription != "" {
				if err := w.Repo.AddIncoming(ctx, current, now); err != nil {
					return nil, fmt.Errorf("chat_id=%d message_id=%d save photo description: %w", current.Chat.ID, current.MessageID, err)
				}
			}
		}
		messages = append(messages, current)
		consideredUpdateIDs = append(consideredUpdateIDs, candidate.UpdateID)
	}
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
		SearchEnabled:    w.Search != nil,
	}
	if wantsContactMention(messages) {
		request.Contacts, err = w.Repo.RecentContacts(ctx, msg.Chat.ID, 100)
		if err != nil {
			return nil, fmt.Errorf("chat_id=%d load contacts: %w", msg.Chat.ID, err)
		}
	}
	for _, current := range messages {
		request.NewMessageIDs = append(request.NewMessageIDs, current.MessageID)
		if _, mentioned := model.MentionedText(current, w.Username); mentioned || w.repliedToBot(current) {
			request.TriggerMessageIDs = append(request.TriggerMessageIDs, current.MessageID)
		}
		if w.repliedToBot(current) {
			request.NewReplyToBotIDs = append(request.NewReplyToBotIDs, current.MessageID)
			botText := model.MessageHistoryText(*current.ReplyToMessage)
			if botText == "" {
				botText = "[Не текстовое сообщение бота]"
			}
			request.RepliedToBotMessages = append(request.RepliedToBotMessages, model.RepliedToBotMessage{
				UserMessageID: current.MessageID,
				BotMessageID:  current.ReplyToMessage.MessageID,
				BotText:       model.CompactText(botText, 1200),
			})
		}
	}
	if msg.ReplyToMessage != nil {
		request.CurrentReplyToMessageID = msg.ReplyToMessage.MessageID
		request.CurrentRepliedToBot = w.repliedToBot(msg)
	}
	log.Printf("AI decision started chat_id=%d thread_id=%d candidate_messages=%d context_messages=%d selected_messages=%d important_entries=%d", msg.Chat.ID, msg.MessageThreadID, len(messages), len(history), len(selectedHistory), len(important))
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
		for i := len(messages) - 1; i >= 0; i-- {
			current := messages[i]
			if w.repliedToBot(current) && action.ReplyToMessageID == current.ReplyToMessage.MessageID {
				log.Printf("AI action target corrected chat_id=%d bot_message_id=%d user_message_id=%d", msg.Chat.ID, action.ReplyToMessageID, current.MessageID)
				action.ReplyToMessageID = current.MessageID
				break
			}
		}
		if validActionTarget(action, messages) {
			validActions = append(validActions, action)
		} else {
			log.Printf("AI selected invalid action target update_id=%d chat_id=%d action=%s reply_to_message_id=%d", latest.UpdateID, msg.Chat.ID, action.Action, action.ReplyToMessageID)
		}
	}
	plan := model.ActionPlan{UpdateIDs: updateIDs, ConsideredUpdateIDs: consideredUpdateIDs, Actions: validActions}
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
	if err := w.Repo.MarkUpdatesConsidered(ctx, plan.ConsideredUpdateIDs); err != nil {
		return nil, fmt.Errorf("mark decision candidates considered: %w", err)
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

func validActionTarget(action model.Decision, messages []model.Message) bool {
	id := action.ReplyToMessageID
	if id < 0 {
		return false
	}
	if id == 0 {
		return action.Action == "message" || action.Action == "poll" || action.Action == "image"
	}
	for _, message := range messages {
		if message.MessageID == id {
			return true
		}
	}
	return false
}

func (w *Worker) executePlan(ctx context.Context, msg model.Message, firstUpdateID int64, plan model.ActionPlan) error {
	for index := plan.Completed; index < len(plan.Actions); index++ {
		if err := w.executeAction(ctx, msg, firstUpdateID, plan.Actions[index]); err != nil {
			if errors.Is(err, model.ErrChatMigrated) {
				log.Printf("action plan abandoned chat_id=%d thread_id=%d first_update_id=%d reason=chat_migrated remaining=%d", msg.Chat.ID, msg.MessageThreadID, firstUpdateID, len(plan.Actions)-index)
				if w.ActionPlans != nil {
					for skipped := index; skipped < len(plan.Actions); skipped++ {
						if markErr := w.ActionPlans.MarkActionCompleted(ctx, msg.Chat.ID, msg.MessageThreadID, firstUpdateID, skipped); markErr != nil {
							return fmt.Errorf("mark migrated chat action %d completed: %w", skipped, markErr)
						}
					}
				}
				return nil
			}
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
		if w.Images == nil && w.Search == nil {
			return errors.New("image searcher is not configured")
		}
		if w.Search != nil {
			results, err := w.Search.Search(ctx, "images", decision.ImageQuery)
			if err == nil && len(results) > 0 {
				storedText = formatSearchResults("images", decision.Caption, results)
			} else if err != nil {
				log.Printf("image web search failed update_id=%d chat_id=%d error=%v", item.UpdateID, msg.Chat.ID, err)
			}
		}
		if storedText != "" {
			if err := w.Telegram.SendMessage(ctx, target, storedText); err != nil {
				return fmt.Errorf("chat_id=%d message_id=%d send image search results: %w", msg.Chat.ID, msg.MessageID, err)
			}
			break
		}
		if w.Images == nil {
			storedText = "Картинку по запросу не нашёл."
			if err := w.Telegram.SendMessage(ctx, target, storedText); err != nil {
				return fmt.Errorf("chat_id=%d message_id=%d send image search result: %w", msg.Chat.ID, msg.MessageID, err)
			}
			break
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
	case "search":
		if w.Search == nil {
			if decision.SearchType == "images" && w.Images != nil {
				if imageURL, err := w.Images.Search(ctx, decision.SearchQuery); err == nil {
					storedText = formatSearchResults("images", decision.Caption, []model.SearchResult{{Title: "Изображение", URL: imageURL}})
				}
			}
			if storedText == "" {
				storedText = "Поиск в интернете пока не настроен."
			}
		} else {
			results, err := w.Search.Search(ctx, decision.SearchType, decision.SearchQuery)
			if decision.SearchType == "images" && (err != nil || len(results) == 0) && w.Images != nil {
				if imageURL, fallbackErr := w.Images.Search(ctx, decision.SearchQuery); fallbackErr == nil {
					results = []model.SearchResult{{Title: "Изображение", URL: imageURL}}
					err = nil
				}
			}
			if err != nil {
				log.Printf("web search failed update_id=%d chat_id=%d type=%s error=%v", item.UpdateID, msg.Chat.ID, decision.SearchType, err)
				storedText = "Поиск сейчас не сработал. Попробуй ещё раз позже."
			} else {
				storedText = formatSearchResults(decision.SearchType, decision.Caption, results)
			}
		}
		if err := w.Telegram.SendMessage(ctx, target, storedText); err != nil {
			return fmt.Errorf("chat_id=%d message_id=%d send search results: %w", msg.Chat.ID, msg.MessageID, err)
		}
		log.Printf("Telegram search results sent update_id=%d chat_id=%d type=%s reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.SearchType, decision.ReplyToMessageID)
	case "reaction":
		if err := w.Telegram.SetReaction(ctx, target, decision.Reaction); err != nil {
			log.Printf("Telegram reaction unavailable update_id=%d chat_id=%d reply_to_message_id=%d error=%v", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID, err)
			return nil
		}
		storedText = "Реакция: " + decision.Reaction
		log.Printf("Telegram reaction set update_id=%d chat_id=%d reply_to_message_id=%d", item.UpdateID, msg.Chat.ID, decision.ReplyToMessageID)
	case "mention":
		contacts, err := w.Repo.FindContacts(ctx, msg.Chat.ID, decision.ContactQuery)
		if err != nil {
			return fmt.Errorf("chat_id=%d find contact: %w", msg.Chat.ID, err)
		}
		contact, found, ambiguous := chooseContact(contacts, decision.ContactQuery)
		switch {
		case ambiguous:
			var names []string
			for _, candidate := range contacts {
				name := candidate.Name
				if candidate.Username != "" && !strings.EqualFold(name, "@"+candidate.Username) {
					name += " (@" + candidate.Username + ")"
				}
				names = append(names, name)
			}
			storedText = "Нашёл нескольких: " + strings.Join(names, ", ") + ". Уточни @username или полное имя."
		case !found:
			storedText = "Не нашёл такого человека среди тех, кто писал в этой беседе."
		default:
			if err := w.Telegram.SendContactMention(ctx, target, contact, decision.Reply); err != nil {
				return fmt.Errorf("chat_id=%d mention Telegram contact: %w", msg.Chat.ID, err)
			}
			storedText = strings.TrimSpace(decision.Reply + " " + contact.Name + " (" + contact.Link + ")")
			log.Printf("Telegram contact mentioned update_id=%d chat_id=%d user_id=%d", item.UpdateID, msg.Chat.ID, contact.UserID)
		}
		if !found {
			if err := w.Telegram.SendMessage(ctx, target, storedText); err != nil {
				return fmt.Errorf("chat_id=%d send contact lookup result: %w", msg.Chat.ID, err)
			}
		}
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

func chooseContact(contacts []model.Contact, query string) (model.Contact, bool, bool) {
	query = normalizeContactName(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	var exactUsername []model.Contact
	var exactName []model.Contact
	for _, contact := range contacts {
		if contact.Username != "" && normalizeContactName(contact.Username) == query {
			exactUsername = append(exactUsername, contact)
		} else if normalizeContactName(contact.Name) == query {
			exactName = append(exactName, contact)
		}
	}
	if len(exactUsername) == 1 {
		return exactUsername[0], true, false
	}
	if len(exactUsername) > 1 {
		return model.Contact{}, false, true
	}
	if len(exactName) == 1 {
		return exactName[0], true, false
	}
	if len(exactName) > 1 || len(contacts) > 1 {
		return model.Contact{}, false, true
	}
	if len(contacts) == 1 {
		return contacts[0], true, false
	}
	return model.Contact{}, false, false
}

func normalizeContactName(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "ё", "е")
}
