package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

type fakeJudgeStore struct {
	enabled bool
	cases   map[int64]model.JudgeCase
}

type judgeContextRepo struct {
	fakeRepo
	conversationAt time.Time
}

func (r *judgeContextRepo) Conversation(ctx context.Context, chatID, threadID int64, now time.Time) ([]model.HistoryEntry, error) {
	r.conversationAt = now
	return r.fakeRepo.Conversation(ctx, chatID, threadID, now)
}

func (f *fakeJudgeStore) JudgeMode(context.Context, int64) (bool, error) { return f.enabled, nil }
func (f *fakeJudgeStore) SetJudgeMode(_ context.Context, _ int64, enabled bool) error {
	f.enabled = enabled
	return nil
}
func (f *fakeJudgeStore) JudgeCase(_ context.Context, id int64) (model.JudgeCase, bool, error) {
	c, ok := f.cases[id]
	return c, ok, nil
}
func (f *fakeJudgeStore) SaveJudgeCase(_ context.Context, c model.JudgeCase) (model.JudgeCase, error) {
	if f.cases == nil {
		f.cases = map[int64]model.JudgeCase{}
	}
	if previous, ok := f.cases[c.UpdateID]; ok {
		return previous, nil
	}
	f.cases[c.UpdateID] = c
	return c, nil
}
func (f *fakeJudgeStore) AdvanceJudgeCase(_ context.Context, id int64, phase int) error {
	c := f.cases[id]
	if phase > c.Phase {
		c.Phase = phase
	}
	f.cases[id] = c
	return nil
}
func (f *fakeJudgeStore) RecordJudgeForward(_ context.Context, id, forwardedID int64) error {
	c := f.cases[id]
	c.ForwardedMessageID = forwardedID
	f.cases[id] = c
	return nil
}

type fakeJudgeAI struct {
	article    int
	articles   []int
	unbanQuery string
	calls      int
	request    model.JudgeRequest
}

func (f *fakeJudgeAI) Judge(_ context.Context, r model.JudgeRequest) (model.JudgeDecision, error) {
	article := f.article
	if f.calls < len(f.articles) {
		article = f.articles[f.calls]
	}
	f.calls++
	f.request = r
	return model.JudgeDecision{Article: article, EvidenceMessageID: r.MessageID, Reason: "явное нарушение", UnbanQuery: f.unbanQuery}, nil
}

type fakeJudgeTG struct {
	events        []string
	forwardErr    error
	restrictErr   error
	unrestrictErr error
	unbanErr      error
	restrictions  []time.Time
	members       map[int64]model.ChatMember
	pollResult    model.UnbanPollResult
}

func (f *fakeJudgeTG) ForwardMessage(_ context.Context, msg model.Message) (int64, error) {
	f.events = append(f.events, "forward")
	if f.forwardErr != nil {
		return 0, f.forwardErr
	}
	return msg.MessageID + 1000, nil
}

func (f *fakeJudgeTG) GetChatMember(_ context.Context, _, userID int64) (model.ChatMember, error) {
	if member, ok := f.members[userID]; ok {
		return member, nil
	}
	if userID == 1 || userID == 99 {
		return model.ChatMember{Status: "administrator", CanRestrictMembers: true, CanDeleteMessages: true}, nil
	}
	return model.ChatMember{Status: "member"}, nil
}
func (f *fakeJudgeTG) SendUnbanPoll(_ context.Context, _ model.Message, _ string) (int64, error) {
	f.events = append(f.events, "send_unban_poll")
	return 12345, nil
}
func (f *fakeJudgeTG) StopUnbanPoll(_ context.Context, _, _ int64) (model.UnbanPollResult, error) {
	f.events = append(f.events, "stop_unban_poll")
	return f.pollResult, nil
}
func (f *fakeJudgeTG) UnrestrictChatMember(_ context.Context, _, userID int64) error {
	f.events = append(f.events, "unrestrict")
	if f.unrestrictErr != nil {
		return f.unrestrictErr
	}
	if f.members == nil {
		f.members = make(map[int64]model.ChatMember)
	}
	f.members[userID] = model.ChatMember{Status: "member", CanSendMessages: true}
	return nil
}
func (f *fakeJudgeTG) UnbanChatMember(_ context.Context, _, userID int64) error {
	f.events = append(f.events, "unban")
	if f.unbanErr != nil {
		return f.unbanErr
	}
	if f.members == nil {
		f.members = make(map[int64]model.ChatMember)
	}
	f.members[userID] = model.ChatMember{Status: "left"}
	return nil
}
func (f *fakeJudgeTG) RestrictChatMember(_ context.Context, _, _ int64, until time.Time) error {
	f.events = append(f.events, "restrict")
	f.restrictions = append(f.restrictions, until)
	return f.restrictErr
}
func (f *fakeJudgeTG) KickChatMember(context.Context, int64, int64) error {
	f.events = append(f.events, "kick")
	return nil
}
func (f *fakeJudgeTG) DeleteMessage(context.Context, int64, int64) error {
	f.events = append(f.events, "delete")
	return nil
}

type judgeMessenger struct {
	fakeTelegram
	events *[]string
}

func (m *judgeMessenger) SendMessage(ctx context.Context, msg model.Message, text string) error {
	*m.events = append(*m.events, "notice:"+text)
	return m.fakeTelegram.SendMessage(ctx, msg, text)
}

func judgeMessage(id, userID int64, text string) model.Message {
	msg := model.Message{MessageID: id, Date: time.Now().Unix(), Text: text, From: &model.User{ID: userID, FirstName: "Участник"}}
	msg.Chat.ID = -10042
	msg.Chat.Type = "supergroup"
	return msg
}

func TestJudgeModeWarnsThenSentencesThenRestricts(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	store := &fakeJudgeStore{}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{article: 2}
	ordinary := &fakeAI{}
	w := Worker{Repo: repo, JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: messenger, AI: ordinary, BotID: 99, Username: "mybot"}
	command := judgeMessage(1, 1, "Режим судьи")
	if err := w.Process(ctx, model.Update{UpdateID: 1, Message: &command}); err != nil {
		t.Fatal(err)
	}
	if !store.enabled || len(messenger.answers) != 1 {
		t.Fatal("judge mode did not activate")
	}
	tg.events = nil
	msg := judgeMessage(2, 2, "оскорбительное сообщение")
	if err := w.Process(ctx, model.Update{UpdateID: 2, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if ordinary.calls != 0 || judge.calls != 1 {
		t.Fatalf("wrong AI path: ordinary=%d judge=%d", ordinary.calls, judge.calls)
	}
	if len(tg.events) != 5 || tg.events[0] != "forward" || !strings.Contains(tg.events[1], "Статья 2") || !strings.Contains(tg.events[1], "Предупреждение") || !strings.Contains(tg.events[2], "Статья 2") || !strings.Contains(tg.events[2], "Приговор") || tg.events[3] != "restrict" || tg.events[4] != "delete" {
		t.Fatalf("wrong warning/sentence/penalty order: %v", tg.events)
	}
	if store.cases[2].ForwardedMessageID != 1002 {
		t.Fatal("forwarded evidence was not recorded")
	}
	if until := tg.restrictions[0]; time.Until(until) < 59*time.Minute || time.Until(until) > time.Hour+time.Minute {
		t.Fatalf("wrong restriction duration: %s", until)
	}
	if store.cases[2].Phase != 3 {
		t.Fatal("sanction was not recorded")
	}
	if !repo.consideredUpdates[2] {
		t.Fatal("judge message can be reconsidered by the ordinary worker")
	}
	off := judgeMessage(3, 1, "Режим судьи отключить")
	if err := w.Process(ctx, model.Update{UpdateID: 3, Message: &off}); err != nil || store.enabled {
		t.Fatalf("mode did not turn off: %v", err)
	}
}

func TestJudgeWarnsAdministratorWithoutSanction(t *testing.T) {
	ctx := context.Background()
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{article: 2}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: messenger, BotID: 99}
	msg := judgeMessage(4, 1, "Оскорбление от администратора")
	update := model.Update{UpdateID: 4, Message: &msg}
	if err := w.Process(ctx, update); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 || len(tg.events) != 2 || tg.events[0] != "forward" || !strings.Contains(tg.events[1], "Предупреждение администратору") || !strings.Contains(tg.events[1], "Статья 2") {
		t.Fatalf("administrator did not receive evidence and warning: %v", tg.events)
	}
	if len(tg.restrictions) != 0 || store.cases[4].Phase != 3 || !store.cases[4].WarningOnly {
		t.Fatalf("administrator was sanctioned or warning was not saved: %+v events=%v", store.cases[4], tg.events)
	}
	if err := w.Process(ctx, update); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 || len(tg.events) != 2 {
		t.Fatalf("retry repeated administrator warning: calls=%d events=%v", judge.calls, tg.events)
	}
}

func TestJudgeAdminPrivacyWarningDoesNotForwardEvidence(t *testing.T) {
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: &fakeJudgeAI{article: 3}, Telegram: messenger, BotID: 99}
	msg := judgeMessage(6, 1, "Нарушение конфиденциальности от администратора")
	if err := w.Process(context.Background(), model.Update{UpdateID: 6, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(tg.events) != 1 || !strings.Contains(tg.events[0], "Статья 3") || store.cases[6].ForwardedMessageID != -1 || store.cases[6].Phase != 3 {
		t.Fatalf("administrator privacy warning repeated evidence or failed: events=%v case=%+v", tg.events, store.cases[6])
	}
}

func TestJudgeResumesPenaltyWithoutRepeatingNotices(t *testing.T) {
	ctx := context.Background()
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{restrictErr: errors.New("temporary Telegram failure")}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{article: 1}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: messenger, BotID: 99}
	msg := judgeMessage(5, 2, "угроза")
	update := model.Update{UpdateID: 5, Message: &msg}
	if err := w.Process(ctx, update); err == nil {
		t.Fatal("expected Telegram failure")
	}
	if store.cases[5].Phase != 2 || judge.calls != 1 || len(messenger.answers) != 2 {
		t.Fatalf("wrong state after failure: case=%+v judge_calls=%d notices=%v", store.cases[5], judge.calls, messenger.answers)
	}
	tg.restrictErr = nil
	if err := w.Process(ctx, update); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 || len(messenger.answers) != 2 || store.cases[5].Phase != 3 {
		t.Fatal("retry repeated AI decision or notices")
	}
	forwards := 0
	for _, event := range tg.events {
		if event == "forward" {
			forwards++
		}
	}
	if forwards != 1 {
		t.Fatalf("retry forwarded evidence %d times", forwards)
	}
}

func TestNonAdminCannotSwitchJudgeMode(t *testing.T) {
	store := &fakeJudgeStore{}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: &fakeJudgeAI{}, Telegram: messenger, BotID: 99}
	msg := judgeMessage(10, 2, "Режим судьи")
	if err := w.Process(context.Background(), model.Update{UpdateID: 10, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if store.enabled || len(messenger.answers) != 1 || !strings.Contains(messenger.answers[0], "только администратор") {
		t.Fatal("non-admin switched mode")
	}
}

func TestJudgeLoadsMostRecentHour(t *testing.T) {
	repo := &judgeContextRepo{}
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	judge := &fakeJudgeAI{}
	w := Worker{Repo: repo, JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: &fakeTelegram{}, BotID: 99}
	msg := judgeMessage(11, 2, "Проверяем сообщение")
	if err := w.Process(context.Background(), model.Update{UpdateID: 11, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if delta := time.Since(repo.conversationAt); delta < 0 || delta > time.Minute {
		t.Fatalf("judge requested stale context at %s (now %s)", repo.conversationAt, time.Now())
	}
	if judge.calls != 1 {
		t.Fatalf("judge was not called: %d", judge.calls)
	}
}

func TestJudgeDoesNotForwardPrivateEvidence(t *testing.T) {
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: &fakeJudgeAI{article: 3}, Telegram: messenger, BotID: 99}
	msg := judgeMessage(20, 2, "чужие личные данные")
	if err := w.Process(context.Background(), model.Update{UpdateID: 20, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if store.cases[20].ForwardedMessageID != -1 || len(tg.events) != 4 || tg.events[2] != "kick" || tg.events[3] != "delete" {
		t.Fatalf("private evidence was repeated or sentence missing: case=%+v events=%v", store.cases[20], tg.events)
	}
}

func TestJudgeWarnsWhenMessageCannotBeForwarded(t *testing.T) {
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{forwardErr: model.ErrMessageCannotForward}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: &fakeJudgeAI{article: 1}, Telegram: messenger, BotID: 99}
	msg := judgeMessage(21, 2, "угроза")
	if err := w.Process(context.Background(), model.Update{UpdateID: 21, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if store.cases[21].ForwardedMessageID != -1 || len(tg.events) != 5 || tg.events[0] != "forward" || tg.events[3] != "restrict" {
		t.Fatalf("unforwardable evidence blocked sentence: case=%+v events=%v", store.cases[21], tg.events)
	}
}

func TestJudgeWorkerReviewsEveryQueuedMessageWithoutMention(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{}
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{articles: []int{0, 2}}
	ordinary := &fakeAI{}
	w := Worker{Repo: repo, JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: messenger, AI: ordinary, BotID: 99, Username: "mybot"}
	first := judgeMessage(30, 2, "Обычное сообщение без упоминания бота")
	second := judgeMessage(31, 2, "Сообщение с нарушением, тоже без упоминания")
	for i, msg := range []model.Message{first, second} {
		if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: int64(30 + i), Message: &msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 || ordinary.calls != 0 || len(repo.queued) != 0 {
		t.Fatalf("wrong worker route: judge=%d ordinary=%d pending=%d", judge.calls, ordinary.calls, len(repo.queued))
	}
	if len(messenger.answers) != 2 || len(tg.restrictions) != 1 || len(tg.events) != 5 {
		t.Fatalf("unexpected chat activity: notices=%v events=%v restrictions=%d", messenger.answers, tg.events, len(tg.restrictions))
	}
	if judge.request.MessageID != second.MessageID || len(judge.request.History) < 2 {
		t.Fatalf("judge lost conversation context: %+v", judge.request)
	}
}
