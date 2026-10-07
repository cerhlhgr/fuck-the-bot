package tts

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

func TestSynthesizeSendsTimewebAudioRequest(t *testing.T) {
	client := New("test-key")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != endpoint || req.Method != http.MethodPost || req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("wrong TTS request: %s %s", req.Method, req.URL)
		}
		var input struct {
			Model          string `json:"model"`
			Voice          string `json:"voice"`
			Input          string `json:"input"`
			Instructions   string `json:"instructions"`
			ResponseFormat string `json:"response_format"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input.Model != "openai/gpt-4o-mini-tts" || input.Voice != "onyx" || input.Input != "Ну что, собрались?" || input.Instructions != "Говори с лёгкой ехидцей" || input.ResponseFormat != "mp3" {
			t.Fatalf("wrong TTS parameters: %+v", input)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: io.NopCloser(strings.NewReader("ID3fake-mp3"))}, nil
	})
	audio, err := client.Synthesize(context.Background(), model.VoiceRequest{Text: "Ну что, собрались?", Speaker: "onyx", Instructions: "Говори с лёгкой ехидцей"})
	if err != nil || string(audio) != "ID3fake-mp3" {
		t.Fatalf("audio = %q, %v", audio, err)
	}
}

func TestSynthesizeRejectsInvalidAndEmptyAudio(t *testing.T) {
	client := New("test-key")
	if _, err := client.Synthesize(context.Background(), model.VoiceRequest{Text: "hello", Speaker: "unknown"}); err == nil {
		t.Fatal("accepted unknown speaker")
	}
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := client.Synthesize(context.Background(), model.VoiceRequest{Text: "hello"}); err == nil {
		t.Fatal("accepted empty audio")
	}
}
