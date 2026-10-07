package suno

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestGenerateUsesTaratorkaContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/generate" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("wrong Suno request: %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Prompt       string `json:"prompt"`
			CustomMode   bool   `json:"customMode"`
			Instrumental bool   `json:"instrumental"`
			Model        string `json:"model"`
			CallBackURL  string `json:"callBackUrl"`
			Title        string `json:"title"`
			Style        string `json:"style"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Prompt != "[Verse] Ночь" || !payload.CustomMode || payload.Instrumental || payload.Model != "V6" || payload.Title != "Ночь" || payload.Style != "rock" || payload.CallBackURL != "https://bot.example/suno/callback/token" {
			t.Errorf("wrong music parameters: %+v", payload)
		}
		_, _ = w.Write([]byte(`{"code":200,"msg":"success","data":{"taskId":"task-1"}}`))
	}))
	defer server.Close()
	client, err := New(server.URL+"/api/v1", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := client.Generate(context.Background(), model.MusicRequest{Mode: "custom", Prompt: "[Verse] Ночь", Title: "Ночь", Style: "rock"}, "https://bot.example/suno/callback/token")
	if err != nil || taskID != "task-1" {
		t.Fatalf("generation = %q, %v", taskID, err)
	}
}
