package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
}

func TestMentionedText(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		entities []entity
		want     string
		found    bool
	}{
		{"mention after emoji", "😈 @MyBot привет", []entity{{Type: "mention", Offset: 3, Length: 6}}, "😈  привет", true},
		{"other bot", "@OtherBot привет", []entity{{Type: "mention", Offset: 0, Length: 9}}, "", false},
		{"plain text is not an entity", "@MyBot привет", nil, "", false},
		{"invalid UTF-16 offset", "😈 @MyBot", []entity{{Type: "mention", Offset: 1, Length: 6}}, "", false},
		{"mention only", "@MyBot", []entity{{Type: "mention", Offset: 0, Length: 6}}, "Пользователь просто позвал тебя по имени. Ответь коротко.", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := mentionedText(message{Text: tc.text, Entities: tc.entities}, "MyBot")
			if got != tc.want || found != tc.found {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, found, tc.want, tc.found)
			}
		})
	}
}

func TestParseAIReply(t *testing.T) {
	got, err := parseAIReply(` {"reply":"Привет, чёрт!"} `)
	if err != nil || got != "Привет, чёрт!" {
		t.Fatalf("got %q, %v", got, err)
	}
	for _, input := range []string{`{"reply":""}`, "```json\n{\"reply\":\"hi\"}\n```", `{"reply":"hi"} trailing`, `[]`} {
		if _, err := parseAIReply(input); err == nil {
			t.Errorf("accepted invalid AI output %q", input)
		}
	}
	if _, err := parseAIReply(strings.Repeat("x", 100)); err == nil {
		t.Fatal("accepted non-JSON response")
	}
}

func TestMentionedCaption(t *testing.T) {
	msg := message{Caption: "@MyBot что на фото?", CaptionEntities: []entity{{Type: "mention", Offset: 0, Length: 6}}}
	got, ok := mentionedText(msg, "mybot")
	if !ok || got != "что на фото?" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
}

func TestAskAIRequestAndResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != aiURL || req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected AI URL or authorization")
		}
		var input struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Model != "deepseek/deepseek-v4-pro" || len(input.Messages) != 2 || input.Messages[0].Role != "system" || !strings.Contains(input.Messages[0].Content, "ранее был диалог в беседе:\n12:00 @ivan: старое сообщение") || !strings.Contains(input.Messages[0].Content, "Текущее сообщение: привет") || !strings.Contains(input.Messages[0].Content, `{"reply":"твой ответ"}`) {
			t.Fatalf("unexpected AI request: %+v", input)
		}
		return jsonResponse(`{"choices":[{"message":{"content":"{\"reply\":\"Ну привет!\"}"}}]}`), nil
	})}
	got, err := askAI(context.Background(), client, "test-key", "deepseek/deepseek-v4-pro", "привет", "12:00 @ivan: старое сообщение")
	if err != nil || got != "Ну привет!" {
		t.Fatalf("got (%q, %v)", got, err)
	}
}

func TestSendMessageKeepsChatAndTopic(t *testing.T) {
	var sent []struct {
		ChatID          int64  `json:"chat_id"`
		Text            string `json:"text"`
		MessageThreadID int64  `json:"message_thread_id"`
		ReplyParameters *struct {
			MessageID int64 `json:"message_id"`
		} `json:"reply_parameters"`
	}
	tg := telegramClient{token: "test-token", http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/sendMessage") {
			t.Fatalf("unexpected Telegram method: %s", req.URL.Path)
		}
		var item struct {
			ChatID          int64  `json:"chat_id"`
			Text            string `json:"text"`
			MessageThreadID int64  `json:"message_thread_id"`
			ReplyParameters *struct {
				MessageID int64 `json:"message_id"`
			} `json:"reply_parameters"`
		}
		if err := json.NewDecoder(req.Body).Decode(&item); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, item)
		return jsonResponse(`{"ok":true,"result":{}}`), nil
	})}}
	msg := message{MessageID: 17, MessageThreadID: 29}
	msg.Chat.ID = -42
	if err := tg.sendMessage(context.Background(), msg, strings.Repeat("😈", 2500)); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0].ChatID != -42 || sent[0].MessageThreadID != 29 || sent[0].ReplyParameters == nil || sent[0].ReplyParameters.MessageID != 17 || sent[1].ReplyParameters != nil {
		t.Fatalf("unexpected Telegram messages: %+v", sent)
	}
	for _, item := range sent {
		if len([]rune(item.Text)) > 2000 {
			t.Fatalf("message exceeds UTF-16 chunk size: %d emoji", len([]rune(item.Text)))
		}
	}
}
