package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAskSendsSystemPromptWithHistory(t *testing.T) {
	client := New("test-key", "deepseek/deepseek-v4-pro")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != endpoint || req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("wrong endpoint or authorization")
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
		if input.Model != "deepseek/deepseek-v4-pro" || len(input.Messages) != 2 || input.Messages[0].Role != "system" || !strings.Contains(input.Messages[0].Content, `"text":"старое сообщение"`) || !strings.Contains(input.Messages[0].Content, "message_id=17") {
			t.Fatalf("wrong AI request: %+v", input)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"reply\":\"Ну привет!\",\"reply_to_message_id\":17}"}}]}`))}, nil
	})
	answer, err := client.Ask(context.Background(), model.DecisionRequest{BotUsername: "MyBot", CurrentMessageID: 17, History: []model.HistoryEntry{{Date: time.Now(), MessageID: 17, Author: "@ivan", Text: "старое сообщение"}}})
	if err != nil || answer.Reply != "Ну привет!" || answer.ReplyToMessageID != 17 {
		t.Fatalf("answer = %q, %v", answer, err)
	}
}
