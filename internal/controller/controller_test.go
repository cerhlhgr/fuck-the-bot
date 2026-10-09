package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

type fakeRepo struct {
	queued               []model.Update
	allUpdates           []model.Update
	consideredUpdates    map[int64]bool
	insertErr            error
	incoming             []model.Message
	botReplies           []string
	replyTargets         []int64
	history              []model.HistoryEntry
	important            []model.ImportantEntry
	contacts             map[int64][]model.Contact
	recentContactsCalls  int
	importantSourceText  string
	importantErr         error
	conversationThreadID int64
}

func mentionTestMessage(msg *model.Message, username string) {
	mention := "@" + username
	if msg.Text != "" {
		prefix := msg.Text + " "
		msg.Text = prefix + mention
		msg.Entities = append(msg.Entities, model.Entity{Type: "mention", Offset: utf16TestLength(prefix), Length: utf16TestLength(mention)})
		return
	}
	prefix := msg.Caption
	if prefix != "" {
		prefix += " "
	}
	msg.Caption = prefix + mention
	msg.CaptionEntities = append(msg.CaptionEntities, model.Entity{Type: "mention", Offset: utf16TestLength(prefix), Length: utf16TestLength(mention)})
}

func utf16TestLength(value string) int {
	length := 0
	for _, r := range value {
		length += model.UTF16RuneLength(r)
	}
	return length
}

func (f *fakeRepo) EnqueueUpdate(_ context.Context, item model.Update, _ []byte) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.queued = append(f.queued, item)
	f.allUpdates = append(f.allUpdates, item)
	if item.Message != nil {
		_ = f.AddIncoming(context.Background(), *item.Message, time.Now())
	}
	return nil
}
func (f *fakeRepo) UnconsideredUpdates(_ context.Context, chatID, threadID, beforeMessageID int64, now time.Time, limit int) ([]model.Update, error) {
	var candidates []model.Update
	for _, update := range f.allUpdates {
		msg := update.Message
		if msg == nil || msg.Chat.ID != chatID || msg.MessageThreadID != threadID || msg.MessageID > beforeMessageID || f.consideredUpdates[update.UpdateID] {
			continue
		}
		if msg.Date != 0 && time.Unix(msg.Date, 0).Before(now.Add(-model.ContextLifetime)) {
			continue
		}
		candidates = append(candidates, update)
		if len(candidates) == limit {
			break
		}
	}
	return candidates, nil
}
func (f *fakeRepo) MarkUpdatesConsidered(_ context.Context, ids []int64) error {
	if f.consideredUpdates == nil {
		f.consideredUpdates = make(map[int64]bool)
	}
	for _, id := range ids {
		f.consideredUpdates[id] = true
	}
	return nil
}
func (f *fakeRepo) TryDecisionLock(context.Context) (func() error, bool, error) {
	return func() error { return nil }, true, nil
}
func (f *fakeRepo) PendingUpdates(_ context.Context, afterID int64, limit int) ([]model.Update, error) {
	var result []model.Update
	for _, item := range f.queued {
		if item.UpdateID > afterID {
			result = append(result, item)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
func (f *fakeRepo) MarkUpdatesProcessed(_ context.Context, ids []int64) error {
	processed := make(map[int64]bool, len(ids))
	for _, id := range ids {
		processed[id] = true
	}
	kept := f.queued[:0]
	for _, item := range f.queued {
		if !processed[item.UpdateID] {
			kept = append(kept, item)
		}
	}
	f.queued = kept
	return nil
}
func (f *fakeRepo) Prune(context.Context, time.Time) error { return nil }
func (f *fakeRepo) AddIncoming(_ context.Context, msg model.Message, _ time.Time) error {
	text := model.MessageHistoryText(msg)
	for i, entry := range f.history {
		if !entry.Bot && entry.MessageID == msg.MessageID {
			if msg.PhotoDescription != "" {
				f.history[i].Text = text
				for j := range f.incoming {
					if f.incoming[j].MessageID == msg.MessageID {
						f.incoming[j] = msg
					}
				}
			}
			return nil
		}
	}
	f.incoming = append(f.incoming, msg)
	f.history = append(f.history, model.HistoryEntry{MessageID: msg.MessageID, Text: text, Author: model.AuthorName(msg.From)})
	return nil
}
func (f *fakeRepo) AddBotReply(_ context.Context, _, _, replyToMessageID int64, _ string, answer string, _ time.Time) error {
	f.botReplies = append(f.botReplies, answer)
	f.replyTargets = append(f.replyTargets, replyToMessageID)
	return nil
}
func (f *fakeRepo) Conversation(_ context.Context, _, threadID int64, _ time.Time) ([]model.HistoryEntry, error) {
	f.conversationThreadID = threadID
	return f.history, nil
}
func (f *fakeRepo) ImportantContext(context.Context, int64, int64) ([]model.ImportantEntry, error) {
	return f.important, nil
}
func (f *fakeRepo) FindContacts(_ context.Context, chatID int64, query string) ([]model.Contact, error) {
	query = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(query)), "@")
	var matches []model.Contact
	for _, contact := range f.contacts[chatID] {
		if strings.Contains(strings.ToLower(contact.Name), query) || strings.Contains(strings.ToLower(contact.Username), query) {
			matches = append(matches, contact)
		}
	}
	return matches, nil
}
func (f *fakeRepo) RecentContacts(_ context.Context, chatID int64, limit int) ([]model.Contact, error) {
	f.recentContactsCalls++
	contacts := f.contacts[chatID]
	if len(contacts) > limit {
		contacts = contacts[:limit]
	}
	return contacts, nil
}
func (f *fakeRepo) ApplyImportant(_ context.Context, msg model.Message, summary, kind string, forgetIDs []int64, now time.Time) error {
	if f.importantErr != nil {
		return f.importantErr
	}
	forget := make(map[int64]bool, len(forgetIDs))
	for _, id := range forgetIDs {
		forget[id] = true
	}
	kept := f.important[:0]
	for _, entry := range f.important {
		if forget[entry.SourceMessageID] {
			continue
		}
		kept = append(kept, entry)
	}
	f.important = kept
	if summary == "" {
		return nil
	}
	for _, entry := range f.important {
		if entry.SourceMessageID == msg.MessageID {
			return nil
		}
	}
	f.importantSourceText = model.MessageHistoryText(msg)
	f.important = append(f.important, model.ImportantEntry{SourceMessageID: msg.MessageID, SourceDate: now, Author: model.AuthorName(msg.From), Summary: summary, Kind: kind})
	return nil
}
func (f *fakeRepo) ApplyImportantBatch(ctx context.Context, messages []model.Message, updates []model.ImportantUpdate, forgetIDs []int64, now time.Time) error {
	if f.importantErr != nil {
		return f.importantErr
	}
	if len(messages) > 0 && len(forgetIDs) > 0 {
		if err := f.ApplyImportant(ctx, messages[0], "", "", forgetIDs, now); err != nil {
			return err
		}
	}
	byID := make(map[int64]model.Message, len(messages))
	for _, msg := range messages {
		byID[msg.MessageID] = msg
	}
	for _, update := range updates {
		if err := f.ApplyImportant(ctx, byID[update.SourceMessageID], update.Summary, update.Kind, nil, now); err != nil {
			return err
		}
	}
	return nil
}

type fakeAI struct {
	request  model.DecisionRequest
	requests []model.DecisionRequest
	decision model.Decision
	err      error
	calls    int
}

func (f *fakeAI) Ask(_ context.Context, request model.DecisionRequest) (model.Decision, error) {
	f.request = request
	f.requests = append(f.requests, request)
	f.calls++
	return f.decision, f.err
}

type fakeTelegram struct {
	messages           []model.Message
	answers            []string
	mentionedContacts  []model.Contact
	sendMessageCalls   int
	sendMessageErrorAt int
	sendMessageErr     error
	voiceTargets       []model.Message
	voices             [][]byte
	pollTargets        []model.Message
	polls              []model.Poll
	reactionTargets    []model.Message
	reactions          []string
}

func (f *fakeTelegram) SendVoice(_ context.Context, msg model.Message, audio []byte) error {
	f.voiceTargets = append(f.voiceTargets, msg)
	f.voices = append(f.voices, append([]byte(nil), audio...))
	return nil
}

func (f *fakeTelegram) SendMessage(_ context.Context, msg model.Message, answer string) error {
	f.sendMessageCalls++
	if f.sendMessageErr != nil {
		return f.sendMessageErr
	}
	if f.sendMessageCalls == f.sendMessageErrorAt {
		return errors.New("Telegram temporarily unavailable")
	}
	f.messages = append(f.messages, msg)
	f.answers = append(f.answers, answer)
	return nil
}

func (f *fakeTelegram) SendContactMention(_ context.Context, msg model.Message, contact model.Contact, _ string) error {
	f.messages = append(f.messages, msg)
	f.mentionedContacts = append(f.mentionedContacts, contact)
	return nil
}

func (f *fakeTelegram) SendPoll(_ context.Context, msg model.Message, poll model.Poll) error {
	f.pollTargets = append(f.pollTargets, msg)
	f.polls = append(f.polls, poll)
	return nil
}

func (f *fakeTelegram) SetReaction(_ context.Context, msg model.Message, emoji string) error {
	f.reactionTargets = append(f.reactionTargets, msg)
	f.reactions = append(f.reactions, emoji)
	return nil
}

type fakeImageSearcher struct {
	query string
	url   string
}

type fakePhotoDownloader struct {
	sizes []model.PhotoSize
	data  []byte
	err   error
}

func (f *fakePhotoDownloader) DownloadPhoto(_ context.Context, sizes []model.PhotoSize) ([]byte, error) {
	f.sizes = sizes
	return f.data, f.err
}

type fakePhotoAnalyzer struct {
	data        []byte
	description string
	calls       int
}

func (f *fakePhotoAnalyzer) DescribePhoto(_ context.Context, data []byte) (string, error) {
	f.data = data
	f.calls++
	return f.description, nil
}

func (f *fakeImageSearcher) Search(_ context.Context, query string) (string, error) {
	f.query = query
	return f.url, nil
}

func TestWebhookHandler(t *testing.T) {
	repo := &fakeRepo{}
	handler := NewWebhook(repo)
	request := func(method, body string) int {
		t.Helper()
		req := httptest.NewRequest(method, WebhookPath, strings.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	if got := request(http.MethodGet, ""); got != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d", got)
	}
	if got := request(http.MethodPost, "bad json"); got != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d", got)
	}
	if got := request(http.MethodPost, strings.Repeat("x", maxBody+1)); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request status = %d", got)
	}
	repo.insertErr = errors.New("database down")
	if got := request(http.MethodPost, `{"update_id":123}`); got != http.StatusServiceUnavailable {
		t.Fatalf("storage failure status = %d", got)
	}
	repo.insertErr = nil
	if got := request(http.MethodPost, `{"update_id":123,"message":{"message_id":7,"chat":{"id":-42},"text":"привет"}}`); got != http.StatusOK {
		t.Fatalf("valid request status = %d", got)
	}
	if len(repo.queued) != 1 || repo.queued[0].UpdateID != 123 || len(repo.history) != 1 || repo.history[0].Text != "привет" {
		t.Fatalf("update was not stored immediately: queued=%+v history=%+v", repo.queued, repo.history)
	}
}

func TestScheduledRunGroupsPendingMessagesByDialogue(t *testing.T) {
	repo := &fakeRepo{}
	for _, item := range []struct {
		updateID, chatID, threadID, messageID int64
	}{
		{1, -42, 0, 10},
		{2, -42, 0, 11},
		{3, -43, 0, 20},
		{4, -42, 7, 30},
	} {
		msg := model.Message{MessageID: item.messageID, MessageThreadID: item.threadID, IsTopicMessage: item.threadID != 0, Text: "сообщение"}
		mentionTestMessage(&msg, "MyBot")
		msg.Chat.ID = item.chatID
		if err := repo.EnqueueUpdate(context.Background(), model.Update{UpdateID: item.updateID, Message: &msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.incoming) != 4 {
		t.Fatal("webhook did not store messages before the scheduled run")
	}
	ai := &fakeAI{decision: model.Decision{Action: "silence"}}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Username: "MyBot"}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 3 || len(ai.requests[0].NewMessageIDs) != 2 || ai.requests[0].NewMessageIDs[0] != 10 || ai.requests[0].NewMessageIDs[1] != 11 || len(ai.requests[1].NewMessageIDs) != 1 || ai.requests[1].NewMessageIDs[0] != 20 || len(ai.requests[2].NewMessageIDs) != 1 || ai.requests[2].NewMessageIDs[0] != 30 || len(repo.queued) != 0 {
		t.Fatalf("scheduled grouping failed: calls=%d requests=%+v pending=%+v", ai.calls, ai.requests, repo.queued)
	}
}

func TestWorkerMentionsContactFromCurrentChat(t *testing.T) {
	repo := &fakeRepo{contacts: map[int64][]model.Contact{
		-42: {{UserID: 123, Username: "sergey", Name: "Сергей", Link: "tg://user?id=123"}},
		-43: {{UserID: 456, Username: "sergey", Name: "Сергей", Link: "tg://user?id=456"}},
	}}
	msg := model.Message{MessageID: 81, Text: "Позови Сергея"}
	mentionTestMessage(&msg, "mybot")
	msg.Chat.ID = -42
	ai := &fakeAI{decision: model.Decision{Actions: []model.Decision{{Action: "mention", ContactQuery: "Сергей", Reply: "Вот:", ReplyToMessageID: 81}}}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Username: "mybot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 1, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(tg.mentionedContacts) != 1 || tg.mentionedContacts[0].UserID != 123 || len(tg.messages) != 1 || tg.messages[0].MessageID != 81 {
		t.Fatalf("wrong contact mentioned: contacts=%+v messages=%+v", tg.mentionedContacts, tg.messages)
	}
}

func TestWorkerAsksToClarifyAmbiguousContact(t *testing.T) {
	repo := &fakeRepo{contacts: map[int64][]model.Contact{
		-42: {{UserID: 123, Name: "Сергей Иванов", Link: "tg://user?id=123"}, {UserID: 456, Name: "Сергей Петров", Link: "tg://user?id=456"}},
	}}
	msg := model.Message{MessageID: 81, Text: "Позови Сергея"}
	mentionTestMessage(&msg, "mybot")
	msg.Chat.ID = -42
	ai := &fakeAI{decision: model.Decision{Actions: []model.Decision{{Action: "mention", ContactQuery: "Сергей", ReplyToMessageID: 81}}}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Username: "mybot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 1, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(tg.mentionedContacts) != 0 || len(tg.answers) != 1 || !strings.Contains(tg.answers[0], "Уточни") {
		t.Fatalf("ambiguous contact silently chosen: mentions=%+v answers=%+v", tg.mentionedContacts, tg.answers)
	}
}

func TestScheduledRunWaitsForMentionOrReply(t *testing.T) {
	repo := &fakeRepo{}
	first := model.Message{MessageID: 10, Text: "Встречаемся в пятницу"}
	mentioned := model.Message{MessageID: 11, Text: "Во сколько?"}
	mentionTestMessage(&mentioned, "MyBot")
	reply := model.Message{MessageID: 12, Text: "А ты что думаешь?", ReplyToMessage: &model.Message{MessageID: 5, From: &model.User{ID: 99, IsBot: true}}}
	last := model.Message{MessageID: 13, Text: "Уже решили"}
	for i, msg := range []*model.Message{&first, &mentioned, &reply, &last} {
		msg.Chat.ID = -42
		if err := repo.EnqueueUpdate(context.Background(), model.Update{UpdateID: int64(i + 1), Message: msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	ai := &fakeAI{decision: model.Decision{Action: "silence"}}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, BotID: 99, Username: "MyBot"}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(ai.request.NewMessageIDs) != 3 || ai.request.NewMessageIDs[0] != 10 || ai.request.NewMessageIDs[1] != 11 || ai.request.NewMessageIDs[2] != 12 || len(ai.request.TriggerMessageIDs) != 2 || ai.request.TriggerMessageIDs[0] != 11 || ai.request.TriggerMessageIDs[1] != 12 || len(ai.request.NewReplyToBotIDs) != 1 || ai.request.NewReplyToBotIDs[0] != 12 || len(repo.incoming) != 4 || len(repo.queued) != 0 || repo.consideredUpdates[4] {
		t.Fatalf("candidate selection failed: calls=%d new=%v triggers=%v stored=%d pending=%d", ai.calls, ai.request.NewMessageIDs, ai.request.TriggerMessageIDs, len(repo.incoming), len(repo.queued))
	}
	if len(ai.request.History) != 3 || ai.request.History[0].MessageID != 10 || ai.request.History[1].MessageID != 11 || ai.request.History[2].MessageID != 12 {
		t.Fatalf("unexpected history around mention: %+v", ai.request.History)
	}
}

func TestUnmentionedPhotoWaitsForTriggerBeforeAnalysis(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	vision := &fakePhotoAnalyzer{description: "кот"}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Photos: &fakePhotoDownloader{data: []byte("photo")}, Vision: vision, Username: "MyBot"}
	msg := model.Message{MessageID: 20, Photo: []model.PhotoSize{{FileID: "photo-1"}}}
	msg.Chat.ID = -42
	if err := repo.EnqueueUpdate(context.Background(), model.Update{UpdateID: 1, Message: &msg}, nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 0 || vision.calls != 0 || len(repo.incoming) != 1 || len(repo.queued) != 0 {
		t.Fatalf("unmentioned photo triggered AI: decision=%d vision=%d stored=%d pending=%d", ai.calls, vision.calls, len(repo.incoming), len(repo.queued))
	}
	mention := model.Message{MessageID: 21, Text: "Что на фото?"}
	mentionTestMessage(&mention, "MyBot")
	mention.Chat.ID = -42
	if err := repo.EnqueueUpdate(context.Background(), model.Update{UpdateID: 2, Message: &mention}, nil); err != nil {
		t.Fatal(err)
	}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || vision.calls != 1 || len(ai.request.History) != 2 || !strings.Contains(ai.request.History[0].Text, "На фото: кот") {
		t.Fatalf("photo missing when mentioned later: decision=%d vision=%d history=%+v", ai.calls, vision.calls, ai.request.History)
	}
}

func TestScheduledRunRetriesFailedDecision(t *testing.T) {
	repo := &fakeRepo{}
	msg := model.Message{MessageID: 10, Text: "Привет"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := repo.EnqueueUpdate(context.Background(), model.Update{UpdateID: 1, Message: &msg}, nil); err != nil {
		t.Fatal(err)
	}
	ai := &fakeAI{err: errors.New("AI temporarily unavailable")}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Username: "MyBot"}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.queued) != 1 || ai.calls != 1 {
		t.Fatalf("failed decision was lost: calls=%d pending=%+v", ai.calls, repo.queued)
	}
	ai.err = nil
	ai.decision = model.Decision{Action: "silence"}
	if err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repo.queued) != 0 || ai.calls != 2 {
		t.Fatalf("failed decision was not retried: calls=%d pending=%+v", ai.calls, repo.queued)
	}
}

func TestBatchStoresImportantFactsFromDifferentMessages(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Action: "silence", ImportantUpdates: []model.ImportantUpdate{
		{SourceMessageID: 10, Summary: "Встреча в пятницу", Kind: "fact"},
		{SourceMessageID: 11, Summary: "Не писать до утра", Kind: "instruction"},
	}}}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Username: "MyBot"}
	first := model.Message{MessageID: 10, Text: "Встречаемся в пятницу"}
	mentionTestMessage(&first, "MyBot")
	first.Chat.ID = -42
	second := model.Message{MessageID: 11, Text: "Бот, не пиши до утра"}
	mentionTestMessage(&second, "MyBot")
	second.Chat.ID = -42
	if err := worker.ProcessBatch(context.Background(), []model.Update{{UpdateID: 1, Message: &first}, {UpdateID: 2, Message: &second}}); err != nil {
		t.Fatal(err)
	}
	if len(repo.important) != 2 || repo.important[0].SourceMessageID != 10 || repo.important[1].SourceMessageID != 11 || repo.important[1].Kind != "instruction" {
		t.Fatalf("batch lost important facts: %+v", repo.important)
	}
}

func TestWorkerDoesNotAnswerEarlierContextMessage(t *testing.T) {
	repo := &fakeRepo{history: []model.HistoryEntry{{MessageID: 5, Text: "старый вопрос", Author: "@ivan"}}}
	ai := &fakeAI{decision: model.Decision{Reply: "Ну привет!", ReplyToMessageID: 5}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 7, Text: "что думаешь?"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 12, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || ai.request.CurrentMessageID != 7 || len(ai.request.History) != 2 || ai.request.History[1].Text != msg.Text || len(tg.messages) != 0 || len(repo.replyTargets) != 0 {
		t.Fatalf("unexpected decision handling: ai=%+v sent=%+v targets=%+v", ai, tg.messages, repo.replyTargets)
	}
}

func TestWorkerStoresImportantContextEvenWhenSilent(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Action: "silence", Important: "Встреча в пятницу в 19:00 у входа."}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Username: "MyBot"}
	msg := model.Message{MessageID: 41, Text: "Встречаемся в пятницу в 19 у входа", From: &model.User{Username: "ivan"}}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 41, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(repo.important) != 1 || repo.importantSourceText != msg.Text || repo.important[0].Summary != ai.decision.Important || len(tg.messages) != 0 {
		t.Fatalf("important silence was not stored correctly: memory=%+v sent=%+v", repo.important, tg.messages)
	}
	msg.MessageID = 42
	msg.Text = "Во сколько встреча?"
	msg.Entities = nil
	mentionTestMessage(&msg, "MyBot")
	ai.decision = model.Decision{Action: "silence"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 42, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(ai.request.Important) != 1 || ai.request.Important[0].Summary != "Встреча в пятницу в 19:00 у входа." {
		t.Fatalf("important context was not passed to AI: %+v", ai.request.Important)
	}
}

func TestWorkerStoresInstructionInPermanentContext(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Action: "silence", Important: "Не писать в чат до нового указания", ImportantKind: "instruction"}}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Username: "MyBot"}
	msg := model.Message{MessageID: 43, Text: "Бот, не пиши"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 43, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(repo.important) != 1 || repo.important[0].Kind != "instruction" || repo.important[0].Summary != ai.decision.Important {
		t.Fatalf("bot instruction was not stored: %+v", repo.important)
	}
}

func TestWorkerForgetsCancelledContextAndReplacesInstruction(t *testing.T) {
	repo := &fakeRepo{important: []model.ImportantEntry{
		{SourceMessageID: 10, Summary: "Встреча в пятницу", Kind: "fact"},
		{SourceMessageID: 11, Summary: "Не писать в чат", Kind: "instruction"},
	}}
	ai := &fakeAI{decision: model.Decision{Action: "silence", ForgetImportantIDs: []int64{11}, Important: "Можно писать в чат", ImportantKind: "instruction"}}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Username: "MyBot"}
	msg := model.Message{MessageID: 12, Text: "Бот, снова пиши"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 12, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(repo.important) != 2 || repo.important[0].SourceMessageID != 10 || repo.important[1].SourceMessageID != 12 || repo.important[1].Kind != "instruction" {
		t.Fatalf("wrong context after replacement: %+v", repo.important)
	}
	msg.MessageID = 13
	msg.Text = "Встречу отменили"
	msg.Entities = nil
	mentionTestMessage(&msg, "MyBot")
	ai.decision = model.Decision{Action: "silence", ForgetImportantIDs: []int64{10}}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 13, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(repo.important) != 1 || repo.important[0].SourceMessageID != 12 {
		t.Fatalf("cancelled fact remained in context: %+v", repo.important)
	}
}

func TestWorkerRejectsUnknownImportantContextID(t *testing.T) {
	repo := &fakeRepo{important: []model.ImportantEntry{{SourceMessageID: 10, Summary: "Встреча", Kind: "fact"}}}
	ai := &fakeAI{decision: model.Decision{Action: "silence", ForgetImportantIDs: []int64{99}}}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Username: "MyBot"}
	msg := model.Message{MessageID: 12, Text: "Встречу отменили"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 12, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(repo.important) != 1 || repo.important[0].SourceMessageID != 10 {
		t.Fatalf("unknown ID changed context: %+v", repo.important)
	}
}

func TestWorkerDoesNotSendActionWhenImportantContextSaveFails(t *testing.T) {
	repo := &fakeRepo{importantErr: errors.New("database down")}
	ai := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Принято", ReplyToMessageID: 41, Important: "Правило беседы"}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Username: "MyBot"}
	msg := model.Message{MessageID: 41, Text: "Новое правило беседы"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 41, Message: &msg}); err == nil || len(tg.messages) != 0 {
		t.Fatalf("save failure should stop action before sending: err=%v sent=%+v", err, tg.messages)
	}
}

func TestWorkerPassesReplyToBotToAI(t *testing.T) {
	var item model.Update
	const payload = `{"update_id":13,"message":{"message_id":8,"chat":{"id":-42},"from":{"id":7},"text":"а почему?","reply_to_message":{"message_id":5,"from":{"id":99,"is_bot":true},"text":"Ну привет!"}}}`
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Reply: "Потому что", ReplyToMessageID: 8}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	if err := worker.Process(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || !ai.request.CurrentRepliedToBot || ai.request.CurrentReplyToMessageID != 5 || len(ai.request.NewReplyToBotIDs) != 1 || ai.request.NewReplyToBotIDs[0] != 8 || len(tg.messages) != 1 || tg.messages[0].MessageID != 8 {
		t.Fatalf("reply context was lost: ai=%+v sent=%+v", ai, tg.messages)
	}
}

func TestReplyToBotInOrdinarySupergroupKeepsDialogueAndCorrectsTarget(t *testing.T) {
	repo := &fakeRepo{history: []model.HistoryEntry{{MessageID: 5506, Text: "Начальный вопрос"}, {MessageID: 0, Bot: true, Text: "Ответ бота"}}}
	ai := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Продолжаю разговор", ReplyToMessageID: 5507}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 5508, MessageThreadID: 5506, Text: "А почему?", ReplyToMessage: &model.Message{MessageID: 5507, Text: "Ответ бота", From: &model.User{ID: 99, IsBot: true}}}
	msg.Chat.ID = -1003953382590
	if err := worker.Process(context.Background(), model.Update{UpdateID: 478934975, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || repo.conversationThreadID != 0 || len(ai.request.RepliedToBotMessages) != 1 || ai.request.RepliedToBotMessages[0].BotText != "Ответ бота" || len(tg.messages) != 1 || tg.messages[0].MessageID != 5508 || tg.messages[0].MessageThreadID != 0 || len(repo.replyTargets) != 1 || repo.replyTargets[0] != 5508 {
		t.Fatalf("reply to bot was lost: AI=%+v thread=%d sent=%+v targets=%+v", ai.request, repo.conversationThreadID, tg.messages, repo.replyTargets)
	}
}

func TestWorkerDoesNotTriggerOnReplyToAnotherUser(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 9, Text: "А почему?", ReplyToMessage: &model.Message{MessageID: 5, From: &model.User{ID: 7}}}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 14, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 0 || len(repo.incoming) != 1 {
		t.Fatalf("reply to another user triggered AI: calls=%d stored=%d", ai.calls, len(repo.incoming))
	}
}

func TestWorkerSendsAndStoresPoll(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Poll: &model.Poll{Question: "Куда идём?", Options: []string{"В кино", "Домой"}}, ReplyToMessageID: 8}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 8, MessageThreadID: 29, IsTopicMessage: true, Text: "Сделай опрос: куда идём?"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 18, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 0 || len(tg.polls) != 1 || tg.polls[0].Question != "Куда идём?" || len(tg.pollTargets) != 1 || tg.pollTargets[0].MessageID != 8 || tg.pollTargets[0].MessageThreadID != 29 {
		t.Fatalf("wrong Telegram action: %+v", tg)
	}
	if len(repo.botReplies) != 1 || !strings.Contains(repo.botReplies[0], "Куда идём?") || !strings.Contains(repo.botReplies[0], "В кино") || repo.replyTargets[0] != 8 {
		t.Fatalf("poll was not stored in history: %+v", repo)
	}
}

func TestWorkerHandlesImageReactionAndStandaloneMessage(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	tg := &fakeTelegram{}
	images := &fakeImageSearcher{url: "https://upload.wikimedia.org/cat.jpg"}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Images: images, Username: "MyBot"}
	msg := model.Message{MessageID: 8, MessageThreadID: 29, IsTopicMessage: true, Text: "скинь кота"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	item := model.Update{UpdateID: 20, Message: &msg}

	ai.decision = model.Decision{Action: "image", ImageQuery: "cat", Caption: "Держи, блин", ReplyToMessageID: 8}
	if err := worker.Process(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if images.query != "cat" || len(tg.answers) != 1 || tg.answers[0] != "Держи, блин\nhttps://upload.wikimedia.org/cat.jpg" || tg.messages[0].MessageID != 8 || repo.botReplies[0] != tg.answers[0] {
		t.Fatalf("image action was handled incorrectly: images=%+v tg=%+v repo=%+v", images, tg, repo)
	}

	ai.decision = model.Decision{Action: "reaction", Reaction: "🤡", ReplyToMessageID: 8}
	if err := worker.Process(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if len(tg.reactions) != 1 || tg.reactions[0] != "🤡" || tg.reactionTargets[0].MessageID != 8 || repo.botReplies[1] != "Реакция: 🤡" {
		t.Fatalf("reaction action was handled incorrectly: tg=%+v repo=%+v", tg, repo)
	}

	ai.decision = model.Decision{Action: "message", Reply: "Всем привет"}
	if err := worker.Process(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	if len(tg.answers) != 2 || tg.answers[1] != "Всем привет" || tg.messages[1].MessageID != 0 || repo.replyTargets[2] != 0 {
		t.Fatalf("standalone message was handled incorrectly: tg=%+v repo=%+v", tg, repo)
	}
}

func TestWorkerAnalyzesIncomingPhotoBeforeDecision(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	photos := &fakePhotoDownloader{data: []byte("photo-data")}
	vision := &fakePhotoAnalyzer{description: "кот сидит на диване"}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Photos: photos, Vision: vision, Username: "MyBot"}
	msg := model.Message{MessageID: 31, Caption: "Что на фото?", Photo: []model.PhotoSize{{FileID: "photo-1", Width: 500, Height: 500}}}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 21, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(photos.sizes) != 1 || photos.sizes[0].FileID != "photo-1" || string(vision.data) != "photo-data" || len(repo.incoming) != 1 || repo.incoming[0].PhotoDescription != "кот сидит на диване" || len(ai.request.History) != 1 || ai.request.History[0].Text != msg.Caption+"\n[На фото: кот сидит на диване]" {
		t.Fatalf("photo was not added to decision context: photos=%+v vision=%+v incoming=%+v history=%+v", photos, vision, repo.incoming, ai.request.History)
	}
}

func TestWorkerAnalyzesPhotoReplyToBotWithoutMention(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	vision := &fakePhotoAnalyzer{description: "кот сидит на диване"}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Photos: &fakePhotoDownloader{data: []byte("photo-data")}, Vision: vision, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 33, Caption: "Что тут?", Photo: []model.PhotoSize{{FileID: "photo-1"}}, ReplyToMessage: &model.Message{MessageID: 5, From: &model.User{ID: 99, IsBot: true}}}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 23, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || vision.calls != 1 || len(ai.request.NewReplyToBotIDs) != 1 || ai.request.NewReplyToBotIDs[0] != 33 || len(ai.request.History) != 1 || ai.request.History[0].Text != "Что тут?\n[На фото: кот сидит на диване]" {
		t.Fatalf("photo reply was not analyzed: ai=%+v vision_calls=%d", ai.request, vision.calls)
	}
}

func TestWorkerReusesDescriptionForIdenticalPhoto(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	photos := &fakePhotoDownloader{data: []byte("same-photo-bytes")}
	vision := &fakePhotoAnalyzer{description: "кот сидит на диване"}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Photos: photos, Vision: vision, Username: "MyBot"}
	for _, id := range []int64{51, 52} {
		msg := model.Message{MessageID: id, Photo: []model.PhotoSize{{FileID: "photo-1"}}}
		mentionTestMessage(&msg, "MyBot")
		msg.Chat.ID = -42
		if err := worker.Process(context.Background(), model.Update{UpdateID: id, Message: &msg}); err != nil {
			t.Fatal(err)
		}
	}
	if vision.calls != 1 || len(repo.history) != 2 || repo.history[0].Text != "@MyBot\n[На фото: кот сидит на диване]" || repo.history[1].Text != repo.history[0].Text {
		t.Fatalf("identical photo was analyzed again or lost from history: calls=%d history=%+v", vision.calls, repo.history)
	}
}

func TestWorkerKeepsPhotoMessageWhenDownloadFails(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, Photos: &fakePhotoDownloader{err: errors.New("download failed")}, Vision: &fakePhotoAnalyzer{}, Username: "MyBot"}
	msg := model.Message{MessageID: 32, Photo: []model.PhotoSize{{FileID: "photo-2"}}}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 22, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(ai.request.History) != 1 || ai.request.History[0].Text != "@MyBot\n[Фото: содержимое недоступно для анализа]" {
		t.Fatalf("photo fallback was not passed to AI: %+v", ai.request.History)
	}
}

func TestWorkerHonorsSilenceAndRejectsUnknownTarget(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 8, Text: "обычная реплика"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 14, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || len(tg.messages) != 0 || len(repo.incoming) != 1 {
		t.Fatalf("silent decision sent a reply: calls=%d sent=%d incoming=%d", ai.calls, len(tg.messages), len(repo.incoming))
	}
	ai.decision = model.Decision{Reply: "ответ", ReplyToMessageID: 999}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 15, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(tg.messages) != 0 {
		t.Fatal("AI-selected unknown target was sent to Telegram")
	}
	ai.decision = model.Decision{Poll: &model.Poll{Question: "Куда?", Options: []string{"Туда", "Сюда"}}, ReplyToMessageID: 999}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 19, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(tg.polls) != 0 {
		t.Fatal("AI-selected poll with unknown target was sent to Telegram")
	}
}

func TestWorkerPassesCompleteHourAndRejectsActionOnContextMessage(t *testing.T) {
	repo := &fakeRepo{}
	for id := int64(1); id <= 70; id++ {
		repo.history = append(repo.history, model.HistoryEntry{MessageID: id, Text: strings.Repeat("длинная реплика ", 100)})
	}
	msg := model.Message{MessageID: 71, Text: "Что решили?"}
	mentionTestMessage(&msg, "MyBot")
	msg.Chat.ID = -42
	ai := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Запоздалый ответ", ReplyToMessageID: 1}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Username: "MyBot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 71, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(ai.request.History) != 71 || ai.request.History[0].Text != repo.history[0].Text || ai.request.History[69].Text != repo.history[69].Text || len(tg.messages) != 0 {
		t.Fatalf("full context or action target guard failed: history=%d answers=%d", len(ai.request.History), len(tg.messages))
	}
}

func TestWorkerStoresOtherBotMessagesWithoutReplying(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	worker := Worker{Repo: repo, AI: ai, Telegram: &fakeTelegram{}, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 8, Text: "сообщение другого бота", From: &model.User{ID: 100, IsBot: true}}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 16, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(repo.incoming) != 1 || ai.calls != 0 {
		t.Fatalf("other bot message: saved=%d AI calls=%d", len(repo.incoming), ai.calls)
	}
	msg.From.ID = 99
	if err := worker.Process(context.Background(), model.Update{UpdateID: 17, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(repo.incoming) != 1 {
		t.Fatal("own bot message was stored twice")
	}
}
