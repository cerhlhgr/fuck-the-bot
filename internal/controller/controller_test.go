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
	queued       []model.Update
	insertErr    error
	incoming     []model.Message
	botReplies   []string
	replyTargets []int64
	history      []model.HistoryEntry
}

func (f *fakeRepo) EnqueueUpdate(_ context.Context, item model.Update, _ []byte) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.queued = append(f.queued, item)
	return nil
}
func (f *fakeRepo) NextUpdate(context.Context) (model.Update, error) {
	if len(f.queued) == 0 {
		return model.Update{}, model.ErrNoUpdates
	}
	item := f.queued[0]
	f.queued = f.queued[1:]
	return item, nil
}
func (f *fakeRepo) MarkUpdateProcessed(context.Context, int64) error { return nil }
func (f *fakeRepo) Prune(context.Context, time.Time) error           { return nil }
func (f *fakeRepo) AddIncoming(_ context.Context, msg model.Message, _ time.Time) error {
	f.incoming = append(f.incoming, msg)
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	f.history = append(f.history, model.HistoryEntry{MessageID: msg.MessageID, Text: text, Author: model.AuthorName(msg.From)})
	return nil
}
func (f *fakeRepo) AddBotReply(_ context.Context, _, _, replyToMessageID int64, _ string, answer string, _ time.Time) error {
	f.botReplies = append(f.botReplies, answer)
	f.replyTargets = append(f.replyTargets, replyToMessageID)
	return nil
}
func (f *fakeRepo) Conversation(context.Context, int64, int64, time.Time) ([]model.HistoryEntry, error) {
	return f.history, nil
}

type fakeAI struct {
	request  model.DecisionRequest
	decision model.Decision
	calls    int
}

func (f *fakeAI) Ask(_ context.Context, request model.DecisionRequest) (model.Decision, error) {
	f.request = request
	f.calls++
	return f.decision, nil
}

type fakeTelegram struct {
	messages        []model.Message
	answers         []string
	pollTargets     []model.Message
	polls           []model.Poll
	reactionTargets []model.Message
	reactions       []string
}

func (f *fakeTelegram) SendMessage(_ context.Context, msg model.Message, answer string) error {
	f.messages = append(f.messages, msg)
	f.answers = append(f.answers, answer)
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
	if got := request(http.MethodPost, `{"update_id":123}`); got != http.StatusOK {
		t.Fatalf("valid request status = %d", got)
	}
	if len(repo.queued) != 1 || repo.queued[0].UpdateID != 123 || len(handler.Wake()) != 1 {
		t.Fatalf("update not queued: %+v", repo.queued)
	}
}

func TestWorkerLetsAIChooseEarlierMessage(t *testing.T) {
	repo := &fakeRepo{history: []model.HistoryEntry{{MessageID: 5, Text: "старый вопрос", Author: "@ivan"}}}
	ai := &fakeAI{decision: model.Decision{Reply: "Ну привет!", ReplyToMessageID: 5}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 7, Text: "что думаешь?"}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 12, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ai.calls != 1 || ai.request.CurrentMessageID != 7 || len(ai.request.History) != 2 || ai.request.History[1].Text != "что думаешь?" || len(tg.messages) != 1 || tg.messages[0].MessageID != 5 || tg.messages[0].Chat.ID != -42 || len(repo.replyTargets) != 1 || repo.replyTargets[0] != 5 {
		t.Fatalf("unexpected decision handling: ai=%+v sent=%+v targets=%+v", ai, tg.messages, repo.replyTargets)
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
	if !ai.request.CurrentRepliedToBot || ai.request.CurrentReplyToMessageID != 5 || len(tg.messages) != 1 || tg.messages[0].MessageID != 8 {
		t.Fatalf("reply context was lost: ai=%+v sent=%+v", ai, tg.messages)
	}
}

func TestWorkerSendsAndStoresPoll(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Poll: &model.Poll{Question: "Куда идём?", Options: []string{"В кино", "Домой"}}, ReplyToMessageID: 8}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 8, MessageThreadID: 29, Text: "Сделай опрос: куда идём?"}
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
	msg := model.Message{MessageID: 8, MessageThreadID: 29, Text: "скинь кота"}
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

func TestWorkerHonorsSilenceAndRejectsUnknownTarget(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, BotID: 99, Username: "MyBot"}
	msg := model.Message{MessageID: 8, Text: "обычная реплика"}
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
