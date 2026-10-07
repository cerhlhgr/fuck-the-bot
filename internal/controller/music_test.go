package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

type testMusicStore struct {
	tokenHash string
	taskID    string
	msg       model.Message
	tracks    []model.MusicTrack
	delivered map[int64]bool
}

func (s *testMusicStore) CreateMusicTask(_ context.Context, hash string, msg model.Message, _ model.MusicRequest) (bool, error) {
	if s.tokenHash != "" {
		return false, nil
	}
	s.tokenHash, s.msg = hash, msg
	return true, nil
}
func (s *testMusicStore) BindMusicTask(_ context.Context, hash, taskID string) error {
	if hash != s.tokenHash {
		return model.ErrMusicTaskNotFound
	}
	s.taskID = taskID
	return nil
}
func (s *testMusicStore) FailMusicTask(context.Context, string, string) error { return nil }
func (s *testMusicStore) ApplyMusicCallback(_ context.Context, hash string, callback model.MusicCallback) error {
	if hash != s.tokenHash {
		return model.ErrMusicTaskNotFound
	}
	if callback.TaskID != s.taskID {
		return model.ErrMusicTaskMismatch
	}
	if callback.Code == 200 && callback.Stage == "complete" {
		for _, track := range callback.Tracks {
			found := false
			for _, existing := range s.tracks {
				found = found || existing.AudioID == track.AudioID
			}
			if !found {
				track.ID = int64(len(s.tracks) + 1)
				track.ChatID = s.msg.Chat.ID
				track.ThreadID = s.msg.MessageThreadID
				track.ReplyToMessageID = s.msg.MessageID
				s.tracks = append(s.tracks, track)
			}
		}
	}
	return nil
}
func (s *testMusicStore) PendingMusicTracks(context.Context, int) ([]model.MusicTrack, error) {
	var pending []model.MusicTrack
	for _, track := range s.tracks {
		if !s.delivered[track.ID] {
			pending = append(pending, track)
		}
	}
	return pending, nil
}
func (s *testMusicStore) MarkMusicTrackDelivered(_ context.Context, id int64) error {
	s.delivered[id] = true
	return nil
}
func (s *testMusicStore) RetryMusicTrack(context.Context, int64, string) error { return nil }
func (s *testMusicStore) TryMusicDeliveryLock(context.Context) (func() error, bool, error) {
	return func() error { return nil }, true, nil
}

type testMusicGenerator struct {
	callbackURL string
	calls       int
}

func (g *testMusicGenerator) Generate(_ context.Context, _ model.MusicRequest, callbackURL string) (string, error) {
	g.calls++
	g.callbackURL = callbackURL
	return "task-1", nil
}

type testMusicMessenger struct {
	messages []model.Message
	audios   []string
}

func (m *testMusicMessenger) SendAudio(_ context.Context, msg model.Message, audioURL, _ string) error {
	m.messages = append(m.messages, msg)
	m.audios = append(m.audios, audioURL)
	return nil
}
func (m *testMusicMessenger) SendMessage(context.Context, model.Message, string) error {
	return errors.New("unexpected link fallback")
}

func TestMusicCallbackDeliversToOriginalTopic(t *testing.T) {
	store := &testMusicStore{delivered: map[int64]bool{}}
	generator := &testMusicGenerator{}
	messenger := &testMusicMessenger{}
	history := &fakeRepo{}
	coordinator, err := NewMusicCoordinator(store, generator, messenger, history, "mybot", "https://bot.example.com")
	if err != nil {
		t.Fatal(err)
	}
	msg := model.Message{MessageID: 123, MessageThreadID: 45}
	msg.Chat.ID = -100876
	created, err := coordinator.Start(context.Background(), msg, model.MusicRequest{Mode: "simple", Prompt: "punk rock"})
	if err != nil || !created || generator.calls != 1 {
		t.Fatalf("start: %t, %v; calls=%d", created, err, generator.calls)
	}
	created, err = coordinator.Start(context.Background(), msg, model.MusicRequest{Mode: "simple", Prompt: "punk rock"})
	if err != nil || created || generator.calls != 1 {
		t.Fatalf("duplicate generation: %t, %v; calls=%d", created, err, generator.calls)
	}
	callbackURL, err := url.Parse(generator.callbackURL)
	if err != nil || !strings.HasPrefix(callbackURL.Path, MusicCallbackPath) {
		t.Fatalf("callback URL: %q, %v", generator.callbackURL, err)
	}
	post := func(body string, path string) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		response := httptest.NewRecorder()
		coordinator.ServeHTTP(response, request)
		return response.Code
	}
	textPayload := `{"code":200,"msg":"success","data":{"callbackType":"text","task_id":"task-1","data":[]}}`
	if code := post(textPayload, callbackURL.Path); code != http.StatusOK || len(store.tracks) != 0 {
		t.Fatalf("interim callback: code=%d tracks=%d", code, len(store.tracks))
	}
	complete := map[string]any{"code": 200, "msg": "success", "data": map[string]any{
		"callbackType": "complete", "task_id": "task-1", "data": []map[string]string{
			{"id": "audio-1", "audio_url": "https://cdn.example.com/one.mp3", "title": "Первый"},
			{"id": "audio-2", "audio_url": "https://cdn.example.com/two.mp3", "title": "Второй"},
		},
	}}
	body, _ := json.Marshal(complete)
	if code := post(string(body), callbackURL.Path); code != http.StatusOK {
		t.Fatalf("complete callback: %d", code)
	}
	if code := post(string(body), callbackURL.Path); code != http.StatusOK || len(store.tracks) != 2 {
		t.Fatalf("duplicate callback: code=%d tracks=%d", code, len(store.tracks))
	}
	if err := coordinator.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(messenger.messages) != 2 || len(history.botReplies) != 2 {
		t.Fatalf("delivery count: audio=%d history=%d", len(messenger.messages), len(history.botReplies))
	}
	for _, delivered := range messenger.messages {
		if delivered.Chat.ID != msg.Chat.ID || delivered.MessageThreadID != msg.MessageThreadID || delivered.MessageID != msg.MessageID {
			t.Fatalf("delivered to wrong destination: %+v", delivered)
		}
	}
	if code := post(string(body), MusicCallbackPath+strings.Repeat("0", 64)); code != http.StatusNotFound {
		t.Fatalf("unknown callback token returned %d", code)
	}
}

func TestWorkerStartsMusicFromAIDecision(t *testing.T) {
	store := &testMusicStore{delivered: map[int64]bool{}}
	generator := &testMusicGenerator{}
	history := &fakeRepo{}
	coordinator, err := NewMusicCoordinator(store, generator, &testMusicMessenger{}, history, "mybot", "https://bot.example.com")
	if err != nil {
		t.Fatal(err)
	}
	ai := &fakeAI{decision: model.Decision{Action: "music", ReplyToMessageID: 123, Music: &model.MusicRequest{Mode: "simple", Prompt: "панк-рок"}}}
	telegram := &fakeTelegram{}
	worker := Worker{Repo: history, AI: ai, Telegram: telegram, Music: coordinator, Username: "mybot"}
	msg := model.Message{MessageID: 123, MessageThreadID: 45, Text: "Бот, сделай панк-рок трек"}
	msg.Chat.ID = -100876
	if err := worker.Process(context.Background(), model.Update{UpdateID: 123, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if !ai.request.MusicEnabled || generator.calls != 1 || len(telegram.messages) != 1 || telegram.messages[0].Chat.ID != msg.Chat.ID || telegram.messages[0].MessageThreadID != msg.MessageThreadID || telegram.messages[0].MessageID != msg.MessageID {
		t.Fatalf("music action was not started in original dialogue: enabled=%t calls=%d sent=%+v", ai.request.MusicEnabled, generator.calls, telegram.messages)
	}
}
