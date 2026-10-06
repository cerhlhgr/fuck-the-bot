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

func TestDownloadPhotoChoosesLargestAllowedSize(t *testing.T) {
	client := New("test-token")
	photo := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16}
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/getFile"):
			var input struct {
				FileID string `json:"file_id"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.FileID != "medium" {
				t.Fatalf("selected wrong size: %s", input.FileID)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"file_path":"photos/test.jpg","file_size":6}}`))}, nil
		case strings.HasSuffix(req.URL.Path, "/file/bottest-token/photos/test.jpg"):
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(photo)))}, nil
		default:
			t.Fatalf("unexpected Telegram method: %s", req.URL.Path)
			return nil, nil
		}
	})
	got, err := client.DownloadPhoto(context.Background(), []model.PhotoSize{
		{FileID: "small", Width: 100, Height: 100, FileSize: 100},
		{FileID: "medium", Width: 800, Height: 800, FileSize: 1000},
		{FileID: "too-large", Width: 2000, Height: 2000, FileSize: maxPhotoBytes + 1},
	})
	if err != nil || string(got) != string(photo) {
		t.Fatalf("photo = %v, %v", got, err)
	}
}

func TestSendPoll(t *testing.T) {
	client := New("test-token")
	var sent bool
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendPoll") {
			t.Fatalf("unexpected Telegram method: %s", req.URL.Path)
		}
		var input struct {
			ChatID          int64  `json:"chat_id"`
			MessageThreadID int64  `json:"message_thread_id"`
			Question        string `json:"question"`
			Options         []struct {
				Text string `json:"text"`
			} `json:"options"`
			ReplyParameters struct {
				MessageID int64 `json:"message_id"`
			} `json:"reply_parameters"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.ChatID != -42 || input.MessageThreadID != 29 || input.ReplyParameters.MessageID != 17 || input.Question != "Куда идём?" || len(input.Options) != 2 || input.Options[0].Text != "В кино" || input.Options[1].Text != "Домой" {
			t.Fatalf("wrong poll payload: %+v", input)
		}
		sent = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})
	msg := model.Message{MessageID: 17, MessageThreadID: 29}
	msg.Chat.ID = -42
	if err := client.SendPoll(context.Background(), msg, model.Poll{Question: "Куда идём?", Options: []string{"В кино", "Домой"}}); err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatal("poll was not sent")
	}
}

func TestStandaloneMessageHasNoReplyParameters(t *testing.T) {
	client := New("test-token")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var input map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if _, ok := input["reply_parameters"]; ok {
			t.Fatal("standalone message has reply parameters")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})
	msg := model.Message{}
	msg.Chat.ID = -42
	if err := client.SendMessage(context.Background(), msg, "Всем привет"); err != nil {
		t.Fatal(err)
	}
}

func TestSetReaction(t *testing.T) {
	client := New("test-token")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/setMessageReaction") {
			t.Fatalf("unexpected method: %s", req.URL.Path)
		}
		var input struct {
			ChatID    int64 `json:"chat_id"`
			MessageID int64 `json:"message_id"`
			Reaction  []struct {
				Type  string `json:"type"`
				Emoji string `json:"emoji"`
			} `json:"reaction"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.ChatID != -42 || input.MessageID != 17 || len(input.Reaction) != 1 || input.Reaction[0].Type != "emoji" || input.Reaction[0].Emoji != "🤡" {
			t.Fatalf("wrong reaction payload: %+v", input)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":true` + `}`))}, nil
	})
	msg := model.Message{MessageID: 17}
	msg.Chat.ID = -42
	if err := client.SetReaction(context.Background(), msg, "🤡"); err != nil {
		t.Fatal(err)
	}
}
