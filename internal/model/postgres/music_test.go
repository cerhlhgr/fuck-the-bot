package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"fuck-the-bot/internal/migrations"
	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/model/postgres"
)

func TestMusicTaskCallbackAndDelivery(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to test PostgreSQL")
	}
	ctx := context.Background()
	pool, err := postgres.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := migrations.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store := postgres.New(pool)
	msg := model.Message{MessageID: 37, MessageThreadID: 5}
	msg.Chat.ID = -time.Now().UnixNano()
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM bot_music_tasks WHERE chat_id = $1`, msg.Chat.ID) }()
	request := model.MusicRequest{Mode: "simple", Prompt: "панк-рок про дорогу"}
	created, err := store.CreateMusicTask(ctx, "test-token-a-"+time.Now().Format("150405.000000000"), msg, request)
	if err != nil || !created {
		t.Fatalf("create task: %t, %v", created, err)
	}
	var hash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM bot_music_tasks WHERE chat_id = $1`, msg.Chat.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	created, err = store.CreateMusicTask(ctx, "other-token", msg, request)
	if err != nil || created {
		t.Fatalf("duplicate task: %t, %v", created, err)
	}
	if err := store.ApplyMusicCallback(ctx, "missing", model.MusicCallback{TaskID: "task-1", Stage: "complete", Code: 200}); !errors.Is(err, model.ErrMusicTaskNotFound) {
		t.Fatalf("unknown token: %v", err)
	}
	if err := store.ApplyMusicCallback(ctx, hash, model.MusicCallback{TaskID: "task-1", Stage: "text", Code: 200}); err != nil {
		t.Fatal(err)
	}
	if err := store.BindMusicTask(ctx, hash, "task-1"); err != nil {
		t.Fatalf("callback before task binding: %v", err)
	}
	if err := store.ApplyMusicCallback(ctx, hash, model.MusicCallback{TaskID: "wrong", Stage: "complete", Code: 200}); !errors.Is(err, model.ErrMusicTaskMismatch) {
		t.Fatalf("wrong task ID: %v", err)
	}
	callback := model.MusicCallback{TaskID: "task-1", Stage: "complete", Code: 200, Tracks: []model.MusicTrack{
		{AudioID: "audio-1", AudioURL: "https://cdn.example/one.mp3", Title: "Первый"},
		{AudioID: "audio-2", AudioURL: "https://cdn.example/two.mp3", Title: "Второй"},
	}}
	for i := 0; i < 2; i++ {
		if err := store.ApplyMusicCallback(ctx, hash, callback); err != nil {
			t.Fatal(err)
		}
	}
	tracks, err := store.PendingMusicTracks(ctx, 10)
	if err != nil || len(tracks) != 2 {
		t.Fatalf("pending tracks: %+v, %v", tracks, err)
	}
	for _, track := range tracks {
		if track.ChatID != msg.Chat.ID || track.ThreadID != msg.MessageThreadID || track.ReplyToMessageID != msg.MessageID {
			t.Fatalf("track lost destination: %+v", track)
		}
	}
	if err := store.MarkMusicTrackDelivered(ctx, tracks[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryMusicTrack(ctx, tracks[1].ID, "Telegram temporarily unavailable"); err != nil {
		t.Fatal(err)
	}
	tracks, err = store.PendingMusicTracks(ctx, 10)
	if err != nil || len(tracks) != 0 {
		t.Fatalf("delivered or deferred tracks returned: %+v, %v", tracks, err)
	}
	unlock, acquired, err := store.TryMusicDeliveryLock(ctx)
	if err != nil || !acquired {
		t.Fatalf("delivery lock: %t, %v", acquired, err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
}
