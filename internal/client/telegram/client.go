package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fuck-the-bot/internal/model"
	"fuck-the-bot/internal/view"
)

const endpoint = "https://api.telegram.org/bot"

type Client struct {
	http  *http.Client
	token string
}

func New(token string) *Client {
	return &Client{http: &http.Client{Timeout: 45 * time.Second}, token: token}
}

func (c *Client) call(ctx context.Context, method string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %s", strings.ReplaceAll(err.Error(), c.token, "[redacted]"))
	}
	defer resp.Body.Close()
	var result struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return fmt.Errorf("Telegram HTTP %d: %s", resp.StatusCode, result.Description)
	}
	return json.Unmarshal(result.Result, output)
}

func (c *Client) GetMe(ctx context.Context) (model.User, error) {
	var me model.User
	err := c.call(ctx, "getMe", struct{}{}, &me)
	return me, err
}

func (c *Client) RegisterWebhook(ctx context.Context, webhookURL, secret string) error {
	var registered bool
	err := c.call(ctx, "setWebhook", struct {
		URL            string   `json:"url"`
		SecretToken    string   `json:"secret_token"`
		AllowedUpdates []string `json:"allowed_updates"`
		MaxConnections int      `json:"max_connections"`
	}{webhookURL, secret, []string{"message"}, 1}, &registered)
	if err != nil {
		return err
	}
	if !registered {
		return fmt.Errorf("Telegram did not accept the webhook")
	}
	return nil
}

func (c *Client) SendMessage(ctx context.Context, original model.Message, answer string) error {
	for i, chunk := range view.TelegramChunks(answer) {
		input := struct {
			ChatID          int64  `json:"chat_id"`
			Text            string `json:"text"`
			MessageThreadID int64  `json:"message_thread_id,omitempty"`
			ReplyParameters *struct {
				MessageID                int64 `json:"message_id"`
				AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
			} `json:"reply_parameters,omitempty"`
		}{ChatID: original.Chat.ID, Text: chunk, MessageThreadID: original.MessageThreadID}
		if i == 0 {
			input.ReplyParameters = &struct {
				MessageID                int64 `json:"message_id"`
				AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
			}{original.MessageID, true}
		}
		var sent json.RawMessage
		if err := c.call(ctx, "sendMessage", input, &sent); err != nil {
			return err
		}
	}
	return nil
}
