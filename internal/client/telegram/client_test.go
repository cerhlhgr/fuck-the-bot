package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestChatMigrationErrorCanBeRecognized(t *testing.T) {
	for _, body := range []string{
		`{"ok":false,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1003953382590}}`,
		`{"ok":false,"description":"Bad Request: group chat was upgraded to a supergroup chat"}`,
	} {
		client := New("test-token")
		client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		msg := model.Message{MessageID: 121}
		msg.Chat.ID = -5081451629
		if err := client.SendMessage(context.Background(), msg, "Привет"); !errors.Is(err, model.ErrChatMigrated) {
			t.Fatalf("migration error not recognized: %v", err)
		}
	}
}

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

func TestSendContactMentionEscapesNameAndTargetsThread(t *testing.T) {
	client := New("test-token")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var input struct {
			ChatID          int64  `json:"chat_id"`
			MessageThreadID int64  `json:"message_thread_id"`
			Text            string `json:"text"`
			ParseMode       string `json:"parse_mode"`
			ReplyParameters struct {
				MessageID int64 `json:"message_id"`
			} `json:"reply_parameters"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.ChatID != -42 || input.MessageThreadID != 29 || input.ReplyParameters.MessageID != 81 || input.ParseMode != "HTML" || input.Text != `Вот &lt;смотри&gt; <a href="tg://user?id=123">Сергей &lt;админ&gt; (@sergey)</a>` {
			t.Fatalf("wrong contact mention: %+v", input)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})
	msg := model.Message{MessageID: 81, MessageThreadID: 29}
	msg.Chat.ID = -42
	contact := model.Contact{UserID: 123, Username: "sergey", Name: "Сергей <админ>", Link: "tg://user?id=123"}
	if err := client.SendContactMention(context.Background(), msg, contact, "Вот <смотри>"); err != nil {
		t.Fatal(err)
	}
}

func TestSendContactMentionWithoutUsername(t *testing.T) {
	client := New("test-token")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var input struct {
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.ParseMode != "HTML" || input.Text != `<a href="tg://user?id=456">Серёжа</a>` {
			t.Fatalf("id-only mention = %+v", input)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})
	msg := model.Message{MessageID: 81}
	msg.Chat.ID = -42
	if err := client.SendContactMention(context.Background(), msg, model.Contact{UserID: 456, Name: "Серёжа", Link: "tg://user?id=456"}, ""); err != nil {
		t.Fatal(err)
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

func TestSendAudioToOriginalTopic(t *testing.T) {
	client := New("test-token")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendAudio") {
			t.Fatalf("wrong Telegram method: %s", req.URL.Path)
		}
		var input struct {
			ChatID          int64  `json:"chat_id"`
			MessageThreadID int64  `json:"message_thread_id"`
			Audio           string `json:"audio"`
			Title           string `json:"title"`
			ReplyParameters struct {
				MessageID int64 `json:"message_id"`
			} `json:"reply_parameters"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.ChatID != -42 || input.MessageThreadID != 29 || input.ReplyParameters.MessageID != 17 || input.Audio != "https://cdn.example/song.mp3" || input.Title != "Ночь" {
			t.Fatalf("wrong audio destination: %+v", input)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})
	msg := model.Message{MessageID: 17, MessageThreadID: 29}
	msg.Chat.ID = -42
	if err := client.SendAudio(context.Background(), msg, "https://cdn.example/song.mp3", "Ночь"); err != nil {
		t.Fatal(err)
	}
}

func TestSendVoiceUploadsMP3ToOriginalTopic(t *testing.T) {
	client := New("test-token")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendVoice") || !strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data;") {
			t.Fatalf("wrong voice request: %s %s", req.URL.Path, req.Header.Get("Content-Type"))
		}
		if err := req.ParseMultipartForm(12 << 20); err != nil {
			t.Fatal(err)
		}
		if req.FormValue("chat_id") != "-42" || req.FormValue("message_thread_id") != "29" {
			t.Fatalf("wrong voice destination: %+v", req.Form)
		}
		var reply struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.Unmarshal([]byte(req.FormValue("reply_parameters")), &reply); err != nil || reply.MessageID != 17 {
			t.Fatalf("wrong voice reply: %+v, %v", reply, err)
		}
		file, header, err := req.FormFile("voice")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil || string(data) != "ID3fake-mp3" || header.Filename != "voice.mp3" || header.Header.Get("Content-Type") != "audio/mpeg" {
			t.Fatalf("wrong uploaded voice: %q, %+v, %v", data, header, err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`))}, nil
	})
	msg := model.Message{MessageID: 17, MessageThreadID: 29}
	msg.Chat.ID = -42
	if err := client.SendVoice(context.Background(), msg, []byte("ID3fake-mp3")); err != nil {
		t.Fatal(err)
	}
	if err := client.SendVoice(context.Background(), msg, nil); err == nil {
		t.Fatal("accepted empty voice")
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
