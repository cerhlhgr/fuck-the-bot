package suno

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fuck-the-bot/internal/model"
)

type Client struct {
	http   *http.Client
	apiURL string
	key    string
}

func New(apiURL, key string) (*Client, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || strings.TrimSpace(key) == "" {
		return nil, errors.New("invalid Suno API URL or key")
	}
	return &Client{http: &http.Client{Timeout: 25 * time.Second}, apiURL: strings.TrimRight(apiURL, "/"), key: key}, nil
}

// Generate uses the /generate contract also used by taratorka_bot.
func (c *Client) Generate(ctx context.Context, music model.MusicRequest, callbackURL string) (string, error) {
	payload := struct {
		Prompt       string `json:"prompt,omitempty"`
		CustomMode   bool   `json:"customMode"`
		Instrumental bool   `json:"instrumental"`
		Model        string `json:"model"`
		CallBackURL  string `json:"callBackUrl"`
		Style        string `json:"style,omitempty"`
		Title        string `json:"title,omitempty"`
		NegativeTags string `json:"negativeTags,omitempty"`
		VocalGender  string `json:"vocalGender,omitempty"`
	}{
		Prompt: music.Prompt, CustomMode: music.Mode == "custom", Instrumental: music.Instrumental,
		Model: "V6", CallBackURL: callbackURL, Style: music.Style, Title: music.Title,
		NegativeTags: music.NegativeTags, VocalGender: music.VocalGender,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/generate", bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Suno generate request: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			TaskID string `json:"taskId"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("decode Suno generate response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || result.Code != 200 || result.Data.TaskID == "" {
		return "", fmt.Errorf("Suno generate rejected request: HTTP %d code %d message %q", resp.StatusCode, result.Code, result.Msg)
	}
	return result.Data.TaskID, nil
}
