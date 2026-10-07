package controller

import (
	"context"
	"errors"
	"testing"

	"fuck-the-bot/internal/model"
)

type fakeVoiceSynthesizer struct {
	request model.VoiceRequest
	audio   []byte
	err     error
	calls   int
}

func (f *fakeVoiceSynthesizer) Synthesize(_ context.Context, request model.VoiceRequest) ([]byte, error) {
	f.calls++
	f.request = request
	return f.audio, f.err
}

func TestWorkerSendsRequestedVoiceInSameTopic(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Action: "voice", ReplyToMessageID: 17, Voice: &model.VoiceRequest{Text: "Ну что, собрались?", Speaker: "onyx", Instructions: "Ехидно"}}}
	telegram := &fakeTelegram{}
	synth := &fakeVoiceSynthesizer{audio: []byte("ID3fake-mp3")}
	worker := Worker{Repo: repo, AI: ai, Telegram: telegram, Voice: synth, Username: "mybot"}
	msg := model.Message{MessageID: 17, MessageThreadID: 29, Text: "Бот, запиши голосовое"}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 17, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if !ai.request.VoiceEnabled || synth.calls != 1 || synth.request.Text != "Ну что, собрались?" || len(telegram.voiceTargets) != 1 || len(telegram.messages) != 0 {
		t.Fatalf("voice action failed: enabled=%t calls=%d targets=%+v text=%+v", ai.request.VoiceEnabled, synth.calls, telegram.voiceTargets, telegram.messages)
	}
	target := telegram.voiceTargets[0]
	if target.Chat.ID != -42 || target.MessageThreadID != 29 || target.MessageID != 17 || len(repo.botReplies) != 1 || repo.botReplies[0] != "Голосовое: Ну что, собрались?" {
		t.Fatalf("voice sent to wrong place or not stored: target=%+v history=%+v", target, repo.botReplies)
	}
}

func TestWorkerReportsVoiceSynthesisFailure(t *testing.T) {
	repo := &fakeRepo{}
	ai := &fakeAI{decision: model.Decision{Action: "voice", ReplyToMessageID: 17, Voice: &model.VoiceRequest{Text: "Привет", Speaker: "onyx"}}}
	telegram := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: telegram, Voice: &fakeVoiceSynthesizer{err: errors.New("gateway down")}, Username: "mybot"}
	msg := model.Message{MessageID: 17, Text: "Запиши голосовое"}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 17, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(telegram.voices) != 0 || len(telegram.answers) != 1 || telegram.answers[0] != "Не получилось записать голосовое. Попробуй ещё раз позже." {
		t.Fatalf("failure was not reported: voices=%d answers=%+v", len(telegram.voices), telegram.answers)
	}
}

func TestWorkerIgnoresVoiceActionForOldMessage(t *testing.T) {
	repo := &fakeRepo{history: []model.HistoryEntry{{MessageID: 16, Author: "@user", Text: "Старое сообщение"}}}
	ai := &fakeAI{decision: model.Decision{Action: "voice", ReplyToMessageID: 16, Voice: &model.VoiceRequest{Text: "Привет", Speaker: "onyx"}}}
	telegram := &fakeTelegram{}
	synth := &fakeVoiceSynthesizer{audio: []byte("ID3fake-mp3")}
	worker := Worker{Repo: repo, AI: ai, Telegram: telegram, Voice: synth, Username: "mybot"}
	msg := model.Message{MessageID: 17, Text: "Новое сообщение"}
	msg.Chat.ID = -42
	if err := worker.Process(context.Background(), model.Update{UpdateID: 17, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(ai.request.History) < 2 || ai.request.History[0].MessageID != 16 {
		t.Fatalf("old message was not present in AI context: %+v", ai.request.History)
	}
	if synth.calls != 0 || len(telegram.voices) != 0 {
		t.Fatalf("old voice request was executed: calls=%d voices=%d", synth.calls, len(telegram.voices))
	}
}
