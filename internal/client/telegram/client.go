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

type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

func replyTo(messageID int64) *replyParameters {
	if messageID <= 0 {
		return nil
	}
	return &replyParameters{MessageID: messageID, AllowSendingWithoutReply: true}
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

func (c *Client) SendMessage(ctx context.Context, original model.Message, answer string) error {
	for i, chunk := range view.TelegramChunks(answer) {
		input := struct {
			ChatID          int64            `json:"chat_id"`
			Text            string           `json:"text"`
			MessageThreadID int64            `json:"message_thread_id,omitempty"`
			ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
		}{ChatID: original.Chat.ID, Text: chunk, MessageThreadID: original.MessageThreadID}
		if i == 0 {
			input.ReplyParameters = replyTo(original.MessageID)
		}
		var sent json.RawMessage
		if err := c.call(ctx, "sendMessage", input, &sent); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) SendPoll(ctx context.Context, original model.Message, poll model.Poll) error {
	type pollOption struct {
		Text string `json:"text"`
	}
	input := struct {
		ChatID          int64            `json:"chat_id"`
		MessageThreadID int64            `json:"message_thread_id,omitempty"`
		Question        string           `json:"question"`
		Options         []pollOption     `json:"options"`
		ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
	}{ChatID: original.Chat.ID, MessageThreadID: original.MessageThreadID, Question: poll.Question, ReplyParameters: replyTo(original.MessageID)}
	for _, option := range poll.Options {
		input.Options = append(input.Options, pollOption{Text: option})
	}
	var sent json.RawMessage
	return c.call(ctx, "sendPoll", input, &sent)
}

func (c *Client) SetReaction(ctx context.Context, target model.Message, emoji string) error {
	input := struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int64 `json:"message_id"`
		Reaction  []struct {
			Type  string `json:"type"`
			Emoji string `json:"emoji"`
		} `json:"reaction"`
	}{ChatID: target.Chat.ID, MessageID: target.MessageID}
	input.Reaction = append(input.Reaction, struct {
		Type  string `json:"type"`
		Emoji string `json:"emoji"`
	}{Type: "emoji", Emoji: emoji})
	var ok bool
	if err := c.call(ctx, "setMessageReaction", input, &ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Telegram did not confirm reaction")
	}
	return nil
}
