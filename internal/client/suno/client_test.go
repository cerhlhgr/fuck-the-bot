package suno

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestGenerateSimpleUsesPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Prompt     string `json:"prompt"`
			Style      string `json:"style"`
			CustomMode bool   `json:"customMode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Prompt != "панк-рок про дорогу" || payload.Style != "" || payload.CustomMode {
			t.Errorf("invalid simple request: %+v", payload)
		}
		_, _ = w.Write([]byte(`{"code":200,"msg":"success","data":{"taskId":"task-1"}}`))
	}))
	defer server.Close()
	client, err := New(server.URL+"/api/v1", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Generate(context.Background(), model.MusicRequest{Mode: "simple", Prompt: "панк-рок про дорогу"}, "https://bot.example/suno/callback/token"); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateReportsProviderAuthenticationFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected authorization header: %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("unexpected content type: %q", got)
		}
		_, _ = w.Write([]byte(`{"code":401,"msg":"Unauthorized: invalid key"}`))
	}))
	defer server.Close()
	client, err := New(server.URL+"/api/v1", " test-key ")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), model.MusicRequest{Mode: "simple", Prompt: "рок"}, "https://bot.example/suno/callback/token")
	if err == nil || !strings.Contains(err.Error(), "HTTP 200 provider_code=401") || !strings.Contains(err.Error(), "check SUNO_API_URL and SUNO_API_SECRET_KEY") || !strings.Contains(err.Error(), "api_host=") {
		t.Fatalf("missing authentication diagnosis: %v", err)
	}
	if strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "/suno/callback/token") {
		t.Fatalf("diagnostic leaked a secret: %v", err)
	}
}

func TestGenerateReportsNonJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>upstream unavailable</html>"))
	}))
	defer server.Close()
	client, err := New(server.URL+"/api/v1", "test-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), model.MusicRequest{Mode: "simple", Prompt: "рок"}, "https://bot.example/suno/callback/token")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "text/html") {
		t.Fatalf("missing response diagnosis: %v", err)
	}
}

func TestNewRejectsBearerPrefix(t *testing.T) {
	if _, err := New("https://apibox.erweima.ai/api/v1", "Bearer secret"); err == nil || !strings.Contains(err.Error(), "without Bearer prefix") {
		t.Fatalf("invalid key accepted: %v", err)
	}
}
