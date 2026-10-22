package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/model/postgres"
)

func TestJudgeModeAndCasePersistence(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to test PostgreSQL")
	}
	ctx := context.Background()
	pool, err := postgres.OpenPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	chatID := -time.Now().UnixNano()
	updateID := -chatID
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_cases WHERE update_id = $1`, updateID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_cases WHERE update_id = $1`, updateID+2)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_cases WHERE update_id = $1`, updateID+3)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_cases WHERE update_id = $1`, updateID+4)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_cases WHERE update_id = $1`, updateID+5)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_warnings WHERE chat_id = $1`, chatID)
		_, _ = pool.Exec(ctx, `DELETE FROM bot_judge_modes WHERE chat_id = $1`, chatID)
	}()
	store := postgres.New(pool)
	active, err := store.JudgeMode(ctx, chatID)
	if err != nil || active {
		t.Fatalf("unexpected initial mode: %t, %v", active, err)
	}
	if err := store.SetJudgeMode(ctx, chatID, true); err != nil {
		t.Fatal(err)
	}
	active, err = postgres.New(pool).JudgeMode(ctx, chatID)
	if err != nil || !active {
		t.Fatalf("mode was not persisted: %t, %v", active, err)
	}
	caseRecord := model.JudgeCase{UpdateID: updateID, ChatID: chatID, MessageID: 7, UserID: 42, Article: 2}
	stored, err := store.SaveJudgeCase(ctx, caseRecord)
	if err != nil || stored.Article != 2 || stored.Phase != 0 || !stored.WarningOnly || stored.AdminWarning {
		t.Fatalf("wrong saved case: %+v, %v", stored, err)
	}
	repeatCase := model.JudgeCase{UpdateID: updateID + 2, ChatID: chatID, MessageID: 9, UserID: 42, Article: 2}
	repeated, err := postgres.New(pool).SaveJudgeCase(ctx, repeatCase)
	if err != nil || repeated.WarningOnly || repeated.AdminWarning {
		t.Fatalf("repeat violation should be sanctioned: %+v, %v", repeated, err)
	}
	firstAgain, err := postgres.New(pool).SaveJudgeCase(ctx, caseRecord)
	if err != nil || !firstAgain.WarningOnly || firstAgain.AdminWarning {
		t.Fatalf("saving the same update changed its outcome: %+v, %v", firstAgain, err)
	}
	_, err = pool.Exec(ctx, `
		UPDATE bot_judge_warnings SET created_at = $4
		WHERE chat_id = $1 AND user_id = $2 AND article = $3`,
		chatID, 42, 2, time.Now().Add(-time.Hour-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	renewedCase := model.JudgeCase{UpdateID: updateID + 4, ChatID: chatID, MessageID: 11, UserID: 42, Article: 2}
	renewed, err := postgres.New(pool).SaveJudgeCase(ctx, renewedCase)
	if err != nil || !renewed.WarningOnly {
		t.Fatalf("expired warning should start a new hour: %+v, %v", renewed, err)
	}
	var warningUpdateID int64
	if err := pool.QueryRow(ctx, `SELECT first_update_id FROM bot_judge_warnings WHERE chat_id = $1 AND user_id = $2 AND article = $3`, chatID, 42, 2).Scan(&warningUpdateID); err != nil || warningUpdateID != renewedCase.UpdateID {
		t.Fatalf("warning timestamp was not renewed: update_id=%d err=%v", warningUpdateID, err)
	}
	secondRepeat := model.JudgeCase{UpdateID: updateID + 5, ChatID: chatID, MessageID: 12, UserID: 42, Article: 2}
	sanctioned, err := postgres.New(pool).SaveJudgeCase(ctx, secondRepeat)
	if err != nil || sanctioned.WarningOnly {
		t.Fatalf("repeat within the renewed hour should be sanctioned: %+v, %v", sanctioned, err)
	}
	if err := store.AdvanceJudgeCase(ctx, updateID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordJudgeForward(ctx, updateID, 99); err != nil {
		t.Fatal(err)
	}
	stored, found, err := postgres.New(pool).JudgeCase(ctx, updateID)
	if err != nil || !found || stored.Phase != 2 || stored.ForwardedMessageID != 99 {
		t.Fatalf("case phase was not persisted: %+v, %t, %v", stored, found, err)
	}
	if err := store.SetJudgeMode(ctx, chatID, false); err != nil {
		t.Fatal(err)
	}
	active, err = store.JudgeMode(ctx, chatID)
	if err != nil || active {
		t.Fatalf("mode was not disabled: %t, %v", active, err)
	}
	adminCase := model.JudgeCase{UpdateID: updateID + 1, ChatID: chatID, MessageID: 8, UserID: 43, Article: 2, WarningOnly: true, AdminWarning: true}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM bot_judge_cases WHERE update_id = $1`, adminCase.UpdateID) }()
	if _, err := store.SaveJudgeCase(ctx, adminCase); err != nil {
		t.Fatal(err)
	}
	stored, found, err = postgres.New(pool).JudgeCase(ctx, adminCase.UpdateID)
	if err != nil || !found || !stored.WarningOnly || !stored.AdminWarning {
		t.Fatalf("admin warning mode was not persisted: %+v, %t, %v", stored, found, err)
	}
	formerAdminCase := model.JudgeCase{UpdateID: updateID + 3, ChatID: chatID, MessageID: 10, UserID: 43, Article: 2}
	formerAdmin, err := postgres.New(pool).SaveJudgeCase(ctx, formerAdminCase)
	if err != nil || !formerAdmin.WarningOnly || formerAdmin.AdminWarning {
		t.Fatalf("admin warning must not count toward member sanctions: %+v, %v", formerAdmin, err)
	}
}
