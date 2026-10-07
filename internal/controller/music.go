package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fuck-the-bot/internal/model"
)

const MusicCallbackPath = "/suno/callback/"
const maxMusicCallbackBody = 1 << 20

var ErrMusicUpstream = errors.New("Suno generation failed")

type MusicGenerator interface {
	Generate(context.Context, model.MusicRequest, string) (string, error)
}

type MusicMessenger interface {
	SendAudio(context.Context, model.Message, string, string) error
	SendMessage(context.Context, model.Message, string) error
}

type MusicCoordinator struct {
	Store       model.MusicStore
	Generator   MusicGenerator
	Telegram    MusicMessenger
	History     model.Repository
	BotUsername string
	callbackURL *url.URL
	wake        chan struct{}
}

func NewMusicCoordinator(store model.MusicStore, generator MusicGenerator, telegram MusicMessenger, history model.Repository, username, callbackBaseURL string) (*MusicCoordinator, error) {
	parsed, err := url.Parse(callbackBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("SUNO_API_CALLBACK_URL must be a public HTTPS URL")
	}
	return &MusicCoordinator{Store: store, Generator: generator, Telegram: telegram, History: history, BotUsername: username, callbackURL: parsed, wake: make(chan struct{}, 1)}, nil
}

func musicTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (m *MusicCoordinator) Start(ctx context.Context, msg model.Message, request model.MusicRequest) (bool, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return false, fmt.Errorf("create music callback token: %w", err)
	}
	token := hex.EncodeToString(bytes[:])
	callbackURL := *m.callbackURL
	callbackURL.Path = MusicCallbackPath + token
	callbackURL.RawQuery = ""
	callbackURL.Fragment = ""
	hash := musicTokenHash(token)
	created, err := m.Store.CreateMusicTask(ctx, hash, msg, request)
	if err != nil || !created {
		return created, err
	}
	taskID, err := m.Generator.Generate(ctx, request, callbackURL.String())
	if err != nil {
		if saveErr := m.Store.FailMusicTask(ctx, hash, err.Error()); saveErr != nil {
			return true, fmt.Errorf("Suno request: %v; save failure: %w", err, saveErr)
		}
		return true, fmt.Errorf("%w: %v", ErrMusicUpstream, err)
	}
	if err := m.Store.BindMusicTask(ctx, hash, taskID); err != nil {
		return true, fmt.Errorf("save Suno task ID: %w", err)
	}
	log.Printf("Suno generation started chat_id=%d thread_id=%d source_message_id=%d task_id=%s", msg.Chat.ID, msg.MessageThreadID, msg.MessageID, taskID)
	return true, nil
}

func (m *MusicCoordinator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, MusicCallbackPath) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, MusicCallbackPath)
	if len(token) != 64 || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	if _, err := hex.DecodeString(token); err != nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMusicCallbackBody+1))
	if err != nil || len(body) > maxMusicCallbackBody {
		http.Error(w, "invalid callback body", http.StatusBadRequest)
		return
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Stage  string `json:"callbackType"`
			TaskID string `json:"task_id"`
			Tracks []struct {
				ID       string `json:"id"`
				AudioURL string `json:"audio_url"`
				Title    string `json:"title"`
			} `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data.TaskID == "" || (payload.Code == 200 && payload.Data.Stage != "text" && payload.Data.Stage != "first" && payload.Data.Stage != "complete" && payload.Data.Stage != "error") {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	callback := model.MusicCallback{TaskID: payload.Data.TaskID, Stage: payload.Data.Stage, Code: payload.Code, Error: payload.Msg}
	if payload.Code == 200 && payload.Data.Stage == "complete" {
		for _, item := range payload.Data.Tracks {
			if item.ID == "" || !validMusicURL(item.AudioURL) {
				continue
			}
			callback.Tracks = append(callback.Tracks, model.MusicTrack{AudioID: item.ID, AudioURL: item.AudioURL, Title: model.CompactText(item.Title, 100)})
		}
	}
	if err := m.Store.ApplyMusicCallback(r.Context(), musicTokenHash(token), callback); err != nil {
		switch {
		case errors.Is(err, model.ErrMusicTaskNotFound), errors.Is(err, model.ErrMusicTaskMismatch):
			http.NotFound(w, r)
		default:
			log.Printf("Suno callback storage failed task_id=%s error=%v", callback.TaskID, err)
			http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	log.Printf("Suno callback accepted task_id=%s stage=%s tracks=%d code=%d", callback.TaskID, callback.Stage, len(callback.Tracks), callback.Code)
	if callback.Stage == "complete" && callback.Code == 200 {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
	w.WriteHeader(http.StatusOK)
}

func validMusicURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func (m *MusicCoordinator) RunDelivery(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := m.DeliverOnce(ctx); err != nil {
			log.Printf("deliver Suno tracks: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

func (m *MusicCoordinator) DeliverOnce(ctx context.Context) error {
	unlock, acquired, err := m.Store.TryMusicDeliveryLock(ctx)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() {
		if err := unlock(); err != nil {
			log.Printf("release music delivery lock: %v", err)
		}
	}()
	tracks, err := m.Store.PendingMusicTracks(ctx, 20)
	if err != nil {
		return err
	}
	for _, track := range tracks {
		msg := model.Message{MessageID: track.ReplyToMessageID, MessageThreadID: track.ThreadID}
		msg.Chat.ID = track.ChatID
		if err := m.Telegram.SendAudio(ctx, msg, track.AudioURL, track.Title); err != nil {
			log.Printf("Telegram Suno audio upload failed chat_id=%d track_id=%d error=%v; trying link", track.ChatID, track.ID, err)
			if linkErr := m.Telegram.SendMessage(ctx, msg, "Трек готов: "+track.AudioURL); linkErr != nil {
				if retryErr := m.Store.RetryMusicTrack(ctx, track.ID, linkErr.Error()); retryErr != nil {
					return retryErr
				}
				continue
			}
		}
		if err := m.Store.MarkMusicTrackDelivered(ctx, track.ID); err != nil {
			return err
		}
		if err := m.History.AddBotReply(ctx, track.ChatID, track.ThreadID, track.ReplyToMessageID, m.BotUsername, "Аудио: "+track.Title+" "+track.AudioURL, time.Now()); err != nil {
			log.Printf("store Suno audio history failed chat_id=%d track_id=%d error=%v", track.ChatID, track.ID, err)
		}
		log.Printf("Telegram Suno audio sent chat_id=%d thread_id=%d track_id=%d", track.ChatID, track.ThreadID, track.ID)
	}
	return nil
}
