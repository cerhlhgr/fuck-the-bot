package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

type fakeUnbanVoteStore struct{ votes map[int64]model.UnbanVote }

func (f *fakeUnbanVoteStore) UnbanVote(_ context.Context, id int64) (model.UnbanVote, bool, error) {
	vote, ok := f.votes[id]
	return vote, ok, nil
}

func (f *fakeUnbanVoteStore) CreateUnbanVote(_ context.Context, vote model.UnbanVote) (model.UnbanVote, error) {
	if f.votes == nil {
		f.votes = make(map[int64]model.UnbanVote)
	}
	if old, ok := f.votes[vote.RequestUpdateID]; ok {
		return old, nil
	}
	for _, old := range f.votes {
		if old.ChatID == vote.ChatID && old.TargetUserID == vote.TargetUserID && old.State != "complete" {
			return model.UnbanVote{}, model.ErrUnbanVoteActive
		}
	}
	vote.State = "created"
	f.votes[vote.RequestUpdateID] = vote
	return vote, nil
}
func (f *fakeUnbanVoteStore) SetUnbanVotePoll(_ context.Context, id, pollID int64, closesAt time.Time) error {
	vote := f.votes[id]
	vote.PollMessageID, vote.ClosesAt, vote.State = pollID, closesAt, "open"
	f.votes[id] = vote
	return nil
}
func (f *fakeUnbanVoteStore) DueUnbanVotes(_ context.Context, now time.Time) ([]model.UnbanVote, error) {
	var due []model.UnbanVote
	for _, vote := range f.votes {
		if (vote.State == "open" && !vote.ClosesAt.After(now)) || vote.State == "closed" || vote.State == "applied" {
			due = append(due, vote)
		}
	}
	return due, nil
}
func (f *fakeUnbanVoteStore) CloseUnbanVote(_ context.Context, id int64, yes, no int) error {
	vote := f.votes[id]
	vote.YesVotes, vote.NoVotes, vote.State = yes, no, "closed"
	f.votes[id] = vote
	return nil
}
func (f *fakeUnbanVoteStore) MarkUnbanVoteApplied(_ context.Context, id int64) error {
	vote := f.votes[id]
	vote.State = "applied"
	f.votes[id] = vote
	return nil
}
func (f *fakeUnbanVoteStore) CompleteUnbanVote(_ context.Context, id int64) error {
	vote := f.votes[id]
	vote.State = "complete"
	f.votes[id] = vote
	return nil
}

func TestJudgeStartsUnbanVoteAndOneYesVoteUnrestricts(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{contacts: map[int64][]model.Contact{-10042: {{UserID: 3, Username: "vasya", Name: "Вася"}}}}
	store := &fakeUnbanVoteStore{}
	tg := &fakeJudgeTG{members: map[int64]model.ChatMember{3: {Status: "restricted", CanSendMessages: false}}, pollResult: model.UnbanPollResult{Yes: 1, No: 0}}
	messenger := &judgeMessenger{events: &tg.events}
	judge := &fakeJudgeAI{unbanQuery: "@vasya"}
	ordinary := &fakeAI{decision: model.Decision{Action: "reply", Reply: "Лишний ответ", ReplyToMessageID: 40}}
	w := Worker{Repo: repo, JudgeStore: &fakeJudgeStore{enabled: true}, JudgeTG: tg, JudgeAI: judge, AI: ordinary, Telegram: messenger, UnbanVotes: store, BotID: 99, Username: "mybot"}
	msg := judgeMessage(40, 2, "Давайте разблокируем @vasya")
	mentionTestMessage(&msg, "mybot")
	if err := w.Process(ctx, model.Update{UpdateID: 40, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if store.votes[40].State != "open" || store.votes[40].TargetUserID != 3 || len(tg.events) != 1 || tg.events[0] != "send_unban_poll" || len(judge.request.Contacts) != 1 || ordinary.calls != 0 {
		t.Fatalf("vote was not started: vote=%+v events=%v", store.votes[40], tg.events)
	}
	if delay := time.Until(store.votes[40].ClosesAt); delay < 2*time.Minute || delay > 4*time.Minute {
		t.Fatalf("wrong vote duration: %s", delay)
	}
	vote := store.votes[40]
	vote.ClosesAt = time.Now().Add(-time.Second)
	store.votes[40] = vote
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.votes[40].State != "complete" || tg.members[3].Status != "member" || strings.Join(tg.events, ",") != "send_unban_poll,stop_unban_poll,unrestrict,notice:Голосование за разблокировку @vasya завершено: за — 1, против — 0. Большинство за; ограничение снято." {
		t.Fatalf("positive vote was not applied: vote=%+v events=%v", store.votes[40], tg.events)
	}
}

func TestJudgeUnbanVoteCanTargetRepliedToMember(t *testing.T) {
	store := &fakeUnbanVoteStore{}
	tg := &fakeJudgeTG{members: map[int64]model.ChatMember{3: {Status: "kicked"}}}
	judge := &fakeJudgeAI{unbanQuery: "reply"}
	w := Worker{Repo: &fakeRepo{}, JudgeStore: &fakeJudgeStore{enabled: true}, JudgeTG: tg, JudgeAI: judge, Telegram: &fakeTelegram{}, UnbanVotes: store, BotID: 99}
	msg := judgeMessage(41, 2, "Давайте вернём его")
	msg.ReplyToMessage = &model.Message{MessageID: 12, From: &model.User{ID: 3, Username: "vasya", FirstName: "Вася"}}
	if err := w.Process(context.Background(), model.Update{UpdateID: 41, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if store.votes[41].TargetUserID != 3 || store.votes[41].TargetStatus != "kicked" || store.votes[41].State != "open" || judge.request.ReplyToUserID != 3 {
		t.Fatalf("reply target was not used: vote=%+v request=%+v", store.votes[41], judge.request)
	}
}

func TestJudgeUnbanVoteDoesNotGuessAmbiguousContact(t *testing.T) {
	repo := &fakeRepo{contacts: map[int64][]model.Contact{-10042: {
		{UserID: 3, Username: "vasya_1", Name: "Вася"},
		{UserID: 4, Username: "vasya_2", Name: "Вася"},
	}}}
	store := &fakeUnbanVoteStore{}
	tg := &fakeJudgeTG{}
	messenger := &fakeTelegram{}
	w := Worker{Repo: repo, JudgeStore: &fakeJudgeStore{enabled: true}, JudgeTG: tg, JudgeAI: &fakeJudgeAI{unbanQuery: "Вася"}, Telegram: messenger, UnbanVotes: store, BotID: 99}
	msg := judgeMessage(42, 2, "Разблокируйте Васю")
	if err := w.Process(context.Background(), model.Update{UpdateID: 42, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if len(store.votes) != 0 || len(messenger.answers) != 1 || !strings.Contains(messenger.answers[0], "несколько участников") {
		t.Fatalf("ambiguous vote was started: votes=%v answers=%v", store.votes, messenger.answers)
	}
}

func TestJudgeVoteTieKeepsRestriction(t *testing.T) {
	ctx := context.Background()
	store := &fakeUnbanVoteStore{votes: map[int64]model.UnbanVote{7: {
		RequestUpdateID: 7, ChatID: -10042, TargetUserID: 3, TargetName: "@vasya", TargetStatus: "restricted", PollMessageID: 55,
		ClosesAt: time.Now().Add(-time.Second), State: "open",
	}}}
	tg := &fakeJudgeTG{members: map[int64]model.ChatMember{3: {Status: "restricted"}}, pollResult: model.UnbanPollResult{Yes: 1, No: 1}}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeTG: tg, Telegram: messenger, UnbanVotes: store}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.votes[7].State != "complete" || tg.members[3].Status != "restricted" || len(tg.events) != 2 || tg.events[0] != "stop_unban_poll" || !strings.Contains(tg.events[1], "Большинства нет") {
		t.Fatalf("tie should preserve restriction: vote=%+v events=%v", store.votes[7], tg.events)
	}
}

func TestJudgeVoteUnbansKickedMember(t *testing.T) {
	ctx := context.Background()
	store := &fakeUnbanVoteStore{votes: map[int64]model.UnbanVote{8: {
		RequestUpdateID: 8, ChatID: -10042, TargetUserID: 3, TargetName: "@vasya", TargetStatus: "kicked", PollMessageID: 56,
		ClosesAt: time.Now().Add(-time.Second), State: "open",
	}}}
	tg := &fakeJudgeTG{members: map[int64]model.ChatMember{3: {Status: "kicked"}}, pollResult: model.UnbanPollResult{Yes: 2, No: 1}}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeTG: tg, Telegram: messenger, UnbanVotes: store}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if store.votes[8].State != "complete" || tg.members[3].Status != "left" || strings.Join(tg.events[:2], ",") != "stop_unban_poll,unban" || !strings.Contains(tg.events[2], "ссылке-приглашению") {
		t.Fatalf("positive vote did not unban: vote=%+v events=%v", store.votes[8], tg.events)
	}
}

func TestJudgeVoteRetriesReleaseWithoutStoppingPollAgain(t *testing.T) {
	ctx := context.Background()
	store := &fakeUnbanVoteStore{votes: map[int64]model.UnbanVote{9: {
		RequestUpdateID: 9, ChatID: -10042, TargetUserID: 3, TargetName: "@vasya", TargetStatus: "restricted", PollMessageID: 57,
		ClosesAt: time.Now().Add(-time.Second), State: "open",
	}}}
	tg := &fakeJudgeTG{members: map[int64]model.ChatMember{3: {Status: "restricted"}}, pollResult: model.UnbanPollResult{Yes: 1}, unrestrictErr: errors.New("temporary error")}
	messenger := &judgeMessenger{events: &tg.events}
	w := Worker{Repo: &fakeRepo{}, JudgeTG: tg, Telegram: messenger, UnbanVotes: store}
	if err := w.processDueUnbanVotes(ctx, time.Now()); err == nil {
		t.Fatal("expected unrestrict failure")
	}
	if store.votes[9].State != "closed" || strings.Join(tg.events, ",") != "stop_unban_poll,unrestrict" {
		t.Fatalf("poll progress lost: %+v events=%v", store.votes[9], tg.events)
	}
	tg.unrestrictErr = nil
	if err := w.processDueUnbanVotes(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if store.votes[9].State != "complete" || tg.members[3].Status != "member" || strings.Join(tg.events[:3], ",") != "stop_unban_poll,unrestrict,unrestrict" {
		t.Fatalf("retry failed: %+v events=%v", store.votes[9], tg.events)
	}
}
