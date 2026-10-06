package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

type fakeRepo struct {
	queued     []model.Update
	insertErr  error
	incoming   []model.Message
	botReplies []string
	history    []model.HistoryEntry
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
	return nil
}
func (f *fakeRepo) AddBotReply(_ context.Context, _, _ int64, _ string, answer string, _ time.Time) error {
	f.botReplies = append(f.botReplies, answer)
	return nil
}
func (f *fakeRepo) Conversation(context.Context, int64, int64, int64, time.Time) ([]model.HistoryEntry, error) {
	return f.history, nil
}

type fakeAI struct {
	text    string
	history []model.HistoryEntry
}

func (f *fakeAI) Ask(_ context.Context, text string, history []model.HistoryEntry) (string, error) {
	f.text, f.history = text, history
	return "Ну привет!", nil
}

type fakeTelegram struct {
	messages []model.Message
	answers  []string
}

func (f *fakeTelegram) SendMessage(_ context.Context, msg model.Message, answer string) error {
	f.messages = append(f.messages, msg)
	f.answers = append(f.answers, answer)
	return nil
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

func TestWorkerProcessesMentionWithPriorConversation(t *testing.T) {
	prior := []model.HistoryEntry{{Date: time.Now().Add(-time.Minute), Author: "@ivan", Text: "старое сообщение"}}
	repo := &fakeRepo{history: prior}
	ai := &fakeAI{}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Username: "MyBot"}
	msg := model.Message{MessageID: 7, Text: "@MyBot привет", Entities: []model.Entity{{Type: "mention", Offset: 0, Length: 6}}}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 12, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ai.text != "привет" || len(ai.history) != 1 || ai.history[0].Text != "старое сообщение" || len(repo.incoming) != 1 || len(repo.botReplies) != 1 || len(tg.messages) != 1 || tg.messages[0].Chat.ID != -42 || tg.answers[0] != "Ну привет!" {
		t.Fatalf("unexpected processing: ai=%+v incoming=%+v replies=%+v", ai, repo.incoming, tg.answers)
	}
}
