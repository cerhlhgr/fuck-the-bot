package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSendReply(t *testing.T) {
	client := New("test-token")
	var sent bool
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/sendMessage"):
			var input struct {
				ChatID          int64 `json:"chat_id"`
				MessageThreadID int64 `json:"message_thread_id"`
				ReplyParameters struct {
					MessageID int64 `json:"message_id"`
				} `json:"reply_parameters"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.ChatID != -42 || input.MessageThreadID != 29 || input.ReplyParameters.MessageID != 17 {
				t.Fatalf("wrong reply target: %+v", input)
			}
			sent = true
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
		default:
			t.Fatalf("unexpected Telegram method: %s", req.URL.Path)
			return nil, nil
		}
	})
	msg := model.Message{MessageID: 17, MessageThreadID: 29}
	msg.Chat.ID = -42
	if err := client.SendMessage(context.Background(), msg, "Привет"); err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatal("reply was not sent")
	}
}
