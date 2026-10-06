package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/view"
)

const endpoint = "https://api.timeweb.ai/v1/chat/completions"

type Client struct {
	http        *http.Client
	key         string
	model       string
	visionModel string
}

func New(key, modelName, visionModel string) *Client {
	return &Client{http: &http.Client{Timeout: 60 * time.Second}, key: key, model: modelName, visionModel: visionModel}
}

func (c *Client) Ask(ctx context.Context, decisionRequest model.DecisionRequest) (model.Decision, error) {
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
		}{"system", view.SystemPrompt(decisionRequest)},
		struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"user", "Выбери уместное действие для нового сообщения по переписке и верни только JSON."},
	)
	content, err := c.completion(ctx, input)
	if err != nil {
		return model.Decision{}, err
	}
	return view.ParseAIDecision(content)
}

func (c *Client) DescribePhoto(ctx context.Context, photo []byte) (string, error) {
	contentType := http.DetectContentType(photo)
	switch contentType {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
	default:
		return "", fmt.Errorf("unsupported photo content type %q", contentType)
	}
	dataURL := "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(photo)
	input := struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Messages  []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}{Model: c.visionModel, MaxTokens: 180}
	input.Messages = append(input.Messages,
		struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}{"system", "Кратко и точно опиши содержимое фотографии на русском языке в 1–2 предложениях. Укажи заметный текст на изображении, если он важен. Не выдумывай невидимые детали и не делай предположений о личности людей. Верни только описание без шуток и Markdown."},
		struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}{"user", []any{
			map[string]any{"type": "text", "text": "Что изображено на фото?"},
			map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}},
		}},
	)
	answer, err := c.completion(ctx, input)
	if err != nil {
		return "", err
	}
	answer = model.CompactText(answer, 500)
	if answer == "" {
		return "", errors.New("vision model returned an empty description")
	}
	return answer, nil
}

func (c *Client) completion(ctx context.Context, input any) (string, error) {
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
	return strings.TrimSpace(result.Choices[0].Message.Content), nil
}
