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
	"unicode/utf8"

	"fuck-the-bot/internal/model"
)

type Client struct {
	http   *http.Client
	apiURL string
	key    string
}

func New(apiURL, key string) (*Client, error) {
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("invalid SUNO_API_URL")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("SUNO_API_SECRET_KEY is empty")
	}
	if strings.HasPrefix(strings.ToLower(key), "bearer ") {
		return nil, errors.New("SUNO_API_SECRET_KEY must contain only the key, without Bearer prefix")
	}
	if strings.HasPrefix(key, "\"") || strings.HasSuffix(key, "\"") || strings.HasPrefix(key, "'") || strings.HasSuffix(key, "'") {
		return nil, errors.New("SUNO_API_SECRET_KEY must not contain quote characters")
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
		return "", fmt.Errorf("Suno generate transport error: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return "", fmt.Errorf("Suno generate response read failed: HTTP %d: %w", resp.StatusCode, err)
	}
	if len(body) > 1<<20 {
		return "", fmt.Errorf("Suno generate response too large: HTTP %d content_type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var result struct {
		Code    int             `json:"code"`
		Msg     string          `json:"msg"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
		Data    struct {
			TaskID string `json:"taskId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("Suno generate invalid JSON: HTTP %d content_type=%q response_bytes=%d: %w", resp.StatusCode, resp.Header.Get("Content-Type"), len(body), err)
	}
	providerMessage := result.Msg
	if providerMessage == "" {
		providerMessage = result.Message
	}
	if providerMessage == "" && len(result.Error) > 0 {
		if err := json.Unmarshal(result.Error, &providerMessage); err != nil {
			var detail struct {
				Message string `json:"message"`
				Msg     string `json:"msg"`
			}
			if json.Unmarshal(result.Error, &detail) == nil {
				providerMessage = detail.Message
				if providerMessage == "" {
					providerMessage = detail.Msg
				}
			}
		}
	}
	providerMessage = safeProviderMessage(providerMessage, c.key, callbackURL, music.Prompt, music.Style, music.Title, music.NegativeTags)
	if resp.StatusCode != http.StatusOK || result.Code != 200 {
		hint := ""
		if resp.StatusCode == http.StatusUnauthorized || result.Code == http.StatusUnauthorized {
			hint = " hint=check SUNO_API_URL and SUNO_API_SECRET_KEY belong to the same provider; do not include Bearer in the key"
		}
		return "", fmt.Errorf("Suno generate rejected: api_host=%q HTTP %d provider_code=%d provider_message=%q%s", req.URL.Host, resp.StatusCode, result.Code, providerMessage, hint)
	}
	if result.Data.TaskID == "" {
		return "", fmt.Errorf("Suno generate returned no taskId: HTTP %d provider_code=%d provider_message=%q", resp.StatusCode, result.Code, providerMessage)
	}
	return result.Data.TaskID, nil
}

func safeProviderMessage(message string, sensitive ...string) string {
	for _, value := range sensitive {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	message = strings.TrimSpace(message)
	if utf8.RuneCountInString(message) > 500 {
		message = string([]rune(message)[:500]) + "…"
	}
	return message
}
