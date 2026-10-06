package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/view"
)

const endpoint = "https://api.timeweb.ai/v1/chat/completions"

type Client struct {
	http  *http.Client
	key   string
	model string
}

func New(key, modelName string) *Client {
	return &Client{http: &http.Client{Timeout: 60 * time.Second}, key: key, model: modelName}
}

func (c *Client) Ask(ctx context.Context, text string, history []model.HistoryEntry) (string, error) {
	input := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{Model: c.model}
	input.Messages = append(input.Messages,
		struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"system", view.SystemPrompt(text, history)},
		struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"user", "Ответь на текст сообщения выше."},
	)
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("AI HTTP %d", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New("AI returned no choices")
	}
	return view.ParseAIReply(result.Choices[0].Message.Content)
}
