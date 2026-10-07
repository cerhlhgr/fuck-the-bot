package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAskSendsSystemPromptWithHistory(t *testing.T) {
	client := New("test-key", "openai/gpt-5.4-nano", "openai/gpt-4.1-mini")
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
		if input.Model != "openai/gpt-5.4-nano" || len(input.Messages) != 2 || input.Messages[0].Role != "system" || !strings.Contains(input.Messages[0].Content, `"старое сообщение"`) || !strings.Contains(input.Messages[0].Content, "message_id=17") {
			t.Fatalf("wrong AI request: %+v", input)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"reply\":\"Ну привет!\",\"reply_to_message_id\":17}"}}]}`))}, nil
	})
	answer, err := client.Ask(context.Background(), model.DecisionRequest{BotUsername: "MyBot", CurrentMessageID: 17, History: []model.HistoryEntry{{Date: time.Now(), MessageID: 17, Author: "@ivan", Text: "старое сообщение"}}})
	if err != nil || answer.Reply != "Ну привет!" || answer.ReplyToMessageID != 17 {
		t.Fatalf("answer = %+v, %v", answer, err)
	}
}

func TestDescribePhotoSendsImageToVisionModel(t *testing.T) {
	client := New("test-key", "openai/gpt-5.4-nano", "openai/gpt-4.1-mini")
	photo := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 16}
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var input struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Model != "openai/gpt-4.1-mini" || len(input.Messages) != 2 || input.Messages[1].Role != "user" {
			t.Fatalf("wrong vision request: %+v", input)
		}
		var parts []struct {
			Type     string `json:"type"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(input.Messages[1].Content, &parts); err != nil {
			t.Fatal(err)
		}
		if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,") {
			t.Fatalf("photo missing from vision request: %+v", parts)
		}
		encoded := strings.TrimPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || string(decoded) != string(photo) {
			t.Fatalf("wrong photo bytes: %v", err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"На фото кот."}}]}`))}, nil
	})
	got, err := client.DescribePhoto(context.Background(), photo)
	if err != nil || got != "На фото кот." {
		t.Fatalf("description = %q, %v", got, err)
	}
}

func TestAskLogsTokenUsageWithoutPromptContent(t *testing.T) {
	client := New("test-key", "openai/gpt-5.4-nano", "openai/gpt-4.1-mini")
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"action\":\"silence\"}"}}],"usage":{"prompt_tokens":120,"completion_tokens":108,"total_tokens":228,"prompt_tokens_details":{"cached_tokens":60},"completion_tokens_details":{"reasoning_tokens":100}}}`))}, nil
	})
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	_, err := client.Ask(context.Background(), model.DecisionRequest{BotUsername: "MyBot", History: []model.HistoryEntry{{Text: "private-message"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	if !strings.Contains(got, "purpose=decision") || !strings.Contains(got, "prompt_tokens=120") || !strings.Contains(got, "cache_hit_tokens=60") || !strings.Contains(got, "reasoning_tokens=100") || strings.Contains(got, "private-message") {
		t.Fatalf("unexpected usage log: %s", got)
	}
}

func TestAskLogsMissingReasoningUsage(t *testing.T) {
	client := New("test-key", "openai/gpt-5.4-nano", "openai/gpt-4.1-mini")
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"action\":\"silence\"}"}}],"usage":{"prompt_tokens":120,"completion_tokens":8,"total_tokens":128}}`))}, nil
	})
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	if _, err := client.Ask(context.Background(), model.DecisionRequest{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "reasoning_tokens=unavailable") {
		t.Fatalf("missing reasoning usage not logged: %s", logs.String())
	}
}
