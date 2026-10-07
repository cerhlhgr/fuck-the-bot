package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fuck-the-bot/internal/model"
)

const endpoint = "https://api.timeweb.ai/v1/audio/speech"
const maxVoiceBytes = 10 << 20

type Client struct {
	http *http.Client
	key  string
}

func New(key string) *Client {
	return &Client{http: &http.Client{Timeout: 90 * time.Second}, key: key}
}

func (c *Client) Synthesize(ctx context.Context, voice model.VoiceRequest) ([]byte, error) {
	voice, err := model.ValidateVoiceRequest(voice)
	if err != nil {
		return nil, err
	}
	input := struct {
		Model          string `json:"model"`
		Voice          string `json:"voice"`
		Input          string `json:"input"`
		Instructions   string `json:"instructions,omitempty"`
		ResponseFormat string `json:"response_format"`
	}{
		Model: "openai/gpt-4o-mini-tts", Voice: voice.Speaker, Input: voice.Text,
		Instructions: voice.Instructions, ResponseFormat: "mp3",
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Timeweb TTS request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Timeweb TTS HTTP %d", resp.StatusCode)
	}
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		return nil, errors.New("Timeweb TTS returned JSON instead of audio")
	}
	if resp.ContentLength > maxVoiceBytes {
		return nil, fmt.Errorf("Timeweb TTS audio exceeds %d bytes", maxVoiceBytes)
	}
	audio, err := io.ReadAll(io.LimitReader(resp.Body, maxVoiceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Timeweb TTS audio: %w", err)
	}
	if len(audio) == 0 || len(audio) > maxVoiceBytes {
		return nil, fmt.Errorf("Timeweb TTS audio is empty or exceeds %d bytes", maxVoiceBytes)
	}
	return audio, nil
}
