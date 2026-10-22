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
	enabled  bool
	cases    map[int64]model.JudgeCase
	warnings map[[3]int64]time.Time
	now      time.Time
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
	if !c.AdminWarning {
		if f.warnings == nil {
			f.warnings = map[[3]int64]time.Time{}
		}
		now := f.now
		if now.IsZero() {
			now = time.Now()
		}
		key := [3]int64{c.ChatID, c.UserID, int64(c.Article)}
		warnedAt, exists := f.warnings[key]
		c.WarningOnly = !exists || !now.Before(warnedAt.Add(time.Hour))
		if c.WarningOnly {
			f.warnings[key] = now
		}
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
	target     string
	basis      string
	quote      string
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
	decision := model.JudgeDecision{Article: article, EvidenceMessageID: r.MessageID, Reason: "явное нарушение", UnbanQuery: f.unbanQuery}
	if article == 2 {
		decision.Target = f.target
		if decision.Target == "" {
			decision.Target = "@victim"
		}
		decision.BullyingBasis = f.basis
		if decision.BullyingBasis == "" {
			decision.BullyingBasis = "direct_aggression"
		}
		decision.AggressionQuote = f.quote
		if decision.AggressionQuote == "" {
			decision.AggressionQuote = r.Text
		}
	}
	return decision, nil
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

func judgeRepo() *fakeRepo {
	contact := model.Contact{UserID: 4, Username: "victim", Name: "Пострадавший"}
	return &fakeRepo{contacts: map[int64][]model.Contact{
		-10042: {contact},
		-10043: {contact},
	}}
}

func TestJudgeModeWarnsFirstThenRestrictsOnRepeat(t *testing.T) {
	ctx := context.Background()
	repo := judgeRepo()
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
	if len(tg.events) != 2 || tg.events[0] != "forward" || !strings.Contains(tg.events[1], "Предупреждение") || !strings.Contains(tg.events[1], "При повторном") || len(tg.restrictions) != 0 || !store.cases[2].WarningOnly || store.cases[2].Phase != 3 {
		t.Fatalf("first violation must only warn: events=%v case=%+v", tg.events, store.cases[2])
	}
	tg.events = nil
	repeat := judgeMessage(3, 2, "повторное оскорбительное сообщение")
	if err := w.Process(ctx, model.Update{UpdateID: 3, Message: &repeat}); err != nil {
		t.Fatal(err)
	}
	if len(tg.events) != 5 || tg.events[0] != "forward" || !strings.Contains(tg.events[1], "Статья 2") || !strings.Contains(tg.events[1], "Предупреждение") || !strings.Contains(tg.events[2], "Статья 2") || !strings.Contains(tg.events[2], "Приговор") || tg.events[3] != "restrict" || tg.events[4] != "delete" {
		t.Fatalf("wrong warning/sentence/penalty order: %v", tg.events)
	}
	if store.cases[3].ForwardedMessageID != 1003 {
		t.Fatal("forwarded evidence was not recorded")
	}
	if until := tg.restrictions[0]; time.Until(until) < 59*time.Minute || time.Until(until) > time.Hour+time.Minute {
		t.Fatalf("wrong restriction duration: %s", until)
	}
	if store.cases[3].Phase != 3 || store.cases[3].WarningOnly {
		t.Fatal("sanction was not recorded")
	}
	if !repo.consideredUpdates[2] || !repo.consideredUpdates[3] {
		t.Fatal("judge message can be reconsidered by the ordinary worker")
	}
	off := judgeMessage(4, 1, "Режим судьи отключить")
	if err := w.Process(ctx, model.Update{UpdateID: 4, Message: &off}); err != nil || store.enabled {
		t.Fatalf("mode did not turn off: %v", err)
	}
}

func TestJudgeWarningExpiresAfterOneHour(t *testing.T) {
	ctx := context.Background()
	start := time.Now()
	store := &fakeJudgeStore{enabled: true, now: start}
	tg := &fakeJudgeTG{}
	w := Worker{
		Repo: judgeRepo(), JudgeStore: store, JudgeTG: tg,
		JudgeAI: &fakeJudgeAI{article: 2}, Telegram: &judgeMessenger{events: &tg.events}, BotID: 99,
	}
	for _, step := range []struct {
		id           int64
		elapsed      time.Duration
		warning      bool
		restrictions int
	}{
		{10, 0, true, 0},
		{11, 59 * time.Minute, false, 1},
		{12, time.Hour, true, 1},
		{13, 2*time.Hour - time.Second, false, 2},
		{14, 2 * time.Hour, true, 2},
	} {
		store.now = start.Add(step.elapsed)
		msg := judgeMessage(step.id, 2, "нарушение статьи 2")
		if err := w.Process(ctx, model.Update{UpdateID: step.id, Message: &msg}); err != nil {
			t.Fatal(err)
		}
		if c := store.cases[step.id]; c.WarningOnly != step.warning || c.Phase != 3 || len(tg.restrictions) != step.restrictions {
			t.Fatalf("after %s: case=%+v, restrictions=%d", step.elapsed, c, len(tg.restrictions))
		}
	}
}

func TestJudgeWarnsAdministratorWithoutSanction(t *testing.T) {
	ctx := context.Background()
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{article: 2}
	w := Worker{Repo: judgeRepo(), JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: messenger, BotID: 99}
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

func TestJudgeWarningsArePerChatUserAndArticle(t *testing.T) {
	ctx := context.Background()
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{articles: []int{2, 1, 2, 2, 2}}
	w := Worker{Repo: judgeRepo(), JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: messenger, BotID: 99}
	for _, entry := range []struct {
		updateID int64
		userID   int64
		chatID   int64
	}{
		{1, 2, -10042},
		{2, 2, -10042},
		{3, 3, -10042},
		{4, 2, -10043},
	} {
		msg := judgeMessage(entry.updateID, entry.userID, "нарушение")
		msg.Chat.ID = entry.chatID
		if err := w.Process(ctx, model.Update{UpdateID: entry.updateID, Message: &msg}); err != nil {
			t.Fatal(err)
		}
		if !store.cases[entry.updateID].WarningOnly {
			t.Fatalf("first violation should warn independently: %+v", store.cases[entry.updateID])
		}
	}
	if len(tg.restrictions) != 0 {
		t.Fatal("warnings caused a restriction")
	}
	repeat := judgeMessage(5, 2, "повторное нарушение статьи 2")
	if err := w.Process(ctx, model.Update{UpdateID: 5, Message: &repeat}); err != nil {
		t.Fatal(err)
	}
	if store.cases[5].WarningOnly || len(tg.restrictions) != 1 {
		t.Fatalf("repeat of the same article was not sanctioned: %+v restrictions=%v", store.cases[5], tg.restrictions)
	}
}

func TestJudgeIgnoresBullyingWithoutKnownParticipant(t *testing.T) {
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: &fakeJudgeAI{article: 2, target: "@stranger"}, Telegram: messenger, BotID: 99}
	msg := judgeMessage(7, 2, "Грубая шутка без адресата в беседе")
	if err := w.Process(context.Background(), model.Update{UpdateID: 7, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(store.cases) != 0 || len(tg.events) != 0 {
		t.Fatalf("unverified bullying target caused moderation: cases=%v events=%v", store.cases, tg.events)
	}
}

func TestJudgeIgnoresBullyingWithoutDirectAggressionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		basis string
		quote string
	}{
		{name: "repeated teasing is insufficient", basis: "repeated"},
		{name: "invented aggressive quote", quote: "заткнись, ты тупой"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeJudgeStore{enabled: true}
			tg := &fakeJudgeTG{}
			messenger := &judgeMessenger{events: &tg.events}
			judge := &fakeJudgeAI{article: 2, basis: tc.basis, quote: tc.quote}
			w := Worker{Repo: judgeRepo(), JudgeStore: store, JudgeTG: tg, JudgeAI: judge, Telegram: messenger, BotID: 99}
			msg := judgeMessage(7, 2, "@victim, ну ты гений, опять проспал 😂")
			if err := w.Process(context.Background(), model.Update{UpdateID: 7, Message: &msg}); err != nil {
				t.Fatal(err)
			}
			if len(store.cases) != 0 || len(tg.events) != 0 {
				t.Fatalf("teasing triggered moderation: cases=%v events=%v", store.cases, tg.events)
			}
		})
	}
}

func TestJudgeIgnoresBullyingAgainstFormerMember(t *testing.T) {
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{members: map[int64]model.ChatMember{4: {Status: "left"}}}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: judgeRepo(), JudgeStore: store, JudgeTG: tg, JudgeAI: &fakeJudgeAI{article: 2}, Telegram: messenger, BotID: 99}
	msg := judgeMessage(9, 2, "Нападка на бывшего участника")
	if err := w.Process(context.Background(), model.Update{UpdateID: 9, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(store.cases) != 0 || len(tg.events) != 0 {
		t.Fatalf("former member caused moderation: cases=%v events=%v", store.cases, tg.events)
	}
}

func TestJudgeCanIdentifyParticipantFromReply(t *testing.T) {
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: store, JudgeTG: tg, JudgeAI: &fakeJudgeAI{article: 2, target: "@victim"}, Telegram: messenger, BotID: 99}
	msg := judgeMessage(8, 2, "Нападка ответом на участника")
	msg.ReplyToMessage = &model.Message{MessageID: 6, From: &model.User{ID: 4, Username: "victim"}}
	if err := w.Process(context.Background(), model.Update{UpdateID: 8, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if !store.cases[8].WarningOnly || len(tg.events) != 2 {
		t.Fatalf("replied-to participant was not recognized: case=%+v events=%v", store.cases[8], tg.events)
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
	first := judgeMessage(4, 2, "первая угроза")
	if err := w.Process(ctx, model.Update{UpdateID: 4, Message: &first}); err != nil {
		t.Fatal(err)
	}
	tg.events = nil
	messenger.answers = nil
	msg := judgeMessage(5, 2, "угроза")
	update := model.Update{UpdateID: 5, Message: &msg}
	if err := w.Process(ctx, update); err == nil {
		t.Fatal("expected Telegram failure")
	}
	if store.cases[5].Phase != 2 || judge.calls != 2 || len(messenger.answers) != 2 {
		t.Fatalf("wrong state after failure: case=%+v judge_calls=%d notices=%v", store.cases[5], judge.calls, messenger.answers)
	}
	tg.restrictErr = nil
	if err := w.Process(ctx, update); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 || len(messenger.answers) != 2 || store.cases[5].Phase != 3 {
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
	first := judgeMessage(19, 2, "чужие личные данные")
	if err := w.Process(context.Background(), model.Update{UpdateID: 19, Message: &first}); err != nil {
		t.Fatal(err)
	}
	if len(tg.events) != 1 || !store.cases[19].WarningOnly {
		t.Fatalf("first privacy violation must only warn: events=%v case=%+v", tg.events, store.cases[19])
	}
	tg.events = nil
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
	first := judgeMessage(20, 2, "угроза")
	if err := w.Process(context.Background(), model.Update{UpdateID: 20, Message: &first}); err != nil {
		t.Fatal(err)
	}
	tg.events = nil
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
	repo := judgeRepo()
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
	if len(messenger.answers) != 1 || len(tg.restrictions) != 0 || len(tg.events) != 2 {
		t.Fatalf("unexpected chat activity: notices=%v events=%v restrictions=%d", messenger.answers, tg.events, len(tg.restrictions))
	}
	if judge.request.MessageID != second.MessageID || len(judge.request.History) < 2 {
		t.Fatalf("judge lost conversation context: %+v", judge.request)
	}
}

func TestJudgeModeKeepsOrdinaryConversationContext(t *testing.T) {
	ctx := context.Background()
	repo := judgeRepo()
	judge := &fakeJudgeAI{}
	ordinary := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Отвечаю", ReplyToMessageID: 51}}
	messenger := &fakeTelegram{}
	w := Worker{Repo: repo, JudgeStore: &fakeJudgeStore{enabled: true}, JudgeTG: &fakeJudgeTG{}, JudgeAI: judge, AI: ordinary, Telegram: messenger, BotID: 99, Username: "mybot"}
	first := judgeMessage(50, 2, "Во сколько встреча?")
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 50, Message: &first}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 || ordinary.calls != 0 || repo.consideredUpdates[50] || len(repo.queued) != 0 {
		t.Fatalf("ordinary message was lost or answered without trigger: judge=%d ordinary=%d considered=%v queued=%d", judge.calls, ordinary.calls, repo.consideredUpdates, len(repo.queued))
	}
	mention := judgeMessage(51, 2, "Так во сколько?")
	mentionTestMessage(&mention, "mybot")
	if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: 51, Message: &mention}, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 || ordinary.calls != 1 || len(ordinary.request.NewMessageIDs) != 2 || ordinary.request.NewMessageIDs[0] != 50 || ordinary.request.NewMessageIDs[1] != 51 || len(messenger.answers) != 1 || messenger.answers[0] != "Отвечаю" || !repo.consideredUpdates[50] || !repo.consideredUpdates[51] {
		t.Fatalf("judge mode did not preserve ordinary reply context: judge=%d ordinary=%d request=%+v answers=%v", judge.calls, ordinary.calls, ordinary.request, messenger.answers)
	}
}

func TestJudgeModeModeratesViolationAndAnswersOtherMention(t *testing.T) {
	ctx := context.Background()
	repo := judgeRepo()
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{articles: []int{2, 0}}
	ordinary := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Нормальный ответ", ReplyToMessageID: 61}}
	w := Worker{Repo: repo, JudgeStore: store, JudgeTG: tg, JudgeAI: judge, AI: ordinary, Telegram: messenger, BotID: 99, Username: "mybot"}
	for _, input := range []struct {
		id   int64
		text string
	}{{60, "Травля участника"}, {61, "А на мой вопрос ответишь?"}} {
		msg := judgeMessage(input.id, 2, input.text)
		mentionTestMessage(&msg, "mybot")
		if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: input.id, Message: &msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 2 || ordinary.calls != 1 || len(ordinary.request.NewMessageIDs) != 1 || ordinary.request.NewMessageIDs[0] != 61 || len(messenger.answers) != 2 || messenger.answers[1] != "Нормальный ответ" || !store.cases[60].WarningOnly || len(repo.queued) != 0 {
		t.Fatalf("merged mode lost moderation or ordinary answer: judge=%d ordinary=%d request=%+v answers=%v cases=%v", judge.calls, ordinary.calls, ordinary.request, messenger.answers, store.cases)
	}
}

func TestJudgeModeAnswersReplyToBot(t *testing.T) {
	ctx := context.Background()
	repo := judgeRepo()
	judge := &fakeJudgeAI{}
	ordinary := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Продолжаю", ReplyToMessageID: 71}}
	messenger := &fakeTelegram{}
	w := Worker{Repo: repo, JudgeStore: &fakeJudgeStore{enabled: true}, JudgeTG: &fakeJudgeTG{}, JudgeAI: judge, AI: ordinary, Telegram: messenger, BotID: 99, Username: "mybot"}
	msg := judgeMessage(71, 2, "А дальше?")
	msg.ReplyToMessage = &model.Message{MessageID: 70, Text: "Начало ответа", From: &model.User{ID: 99, IsBot: true}}
	if err := w.Process(ctx, model.Update{UpdateID: 71, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if judge.calls != 1 || ordinary.calls != 1 || len(ordinary.request.NewReplyToBotIDs) != 1 || ordinary.request.NewReplyToBotIDs[0] != 71 || len(messenger.answers) != 1 || messenger.answers[0] != "Продолжаю" {
		t.Fatalf("judge mode did not answer reply to bot: judge=%d ordinary=%d request=%+v answers=%v", judge.calls, ordinary.calls, ordinary.request, messenger.answers)
	}
}

func TestJudgePenaltyIsNotRepeatedWhenOrdinaryReplyFails(t *testing.T) {
	ctx := context.Background()
	repo := judgeRepo()
	store := &fakeJudgeStore{enabled: true}
	tg := &fakeJudgeTG{}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{articles: []int{2, 0, 0}}
	ordinary := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Ответ", ReplyToMessageID: 81}, err: errors.New("AI temporarily unavailable")}
	w := Worker{Repo: repo, JudgeStore: store, JudgeTG: tg, JudgeAI: judge, AI: ordinary, Telegram: messenger, BotID: 99, Username: "mybot"}
	for _, input := range []struct {
		id   int64
		text string
	}{{80, "Травля"}, {81, "Ответишь?"}} {
		msg := judgeMessage(input.id, 2, input.text)
		mentionTestMessage(&msg, "mybot")
		if err := repo.EnqueueUpdate(ctx, model.Update{UpdateID: input.id, Message: &msg}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repo.queued) != 1 || repo.queued[0].UpdateID != 81 || !repo.consideredUpdates[80] || len(tg.events) != 2 || ordinary.calls != 1 {
		t.Fatalf("moderation was not committed before ordinary failure: queued=%v considered=%v events=%v", repo.queued, repo.consideredUpdates, tg.events)
	}
	ordinary.err = nil
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(repo.queued) != 0 || judge.calls != 3 || ordinary.calls != 2 || len(tg.events) != 3 || len(messenger.answers) != 2 || messenger.answers[1] != "Ответ" {
		t.Fatalf("retry repeated penalty or missed reply: queued=%v judge=%d ordinary=%d events=%v", repo.queued, judge.calls, ordinary.calls, tg.events)
	}
}
