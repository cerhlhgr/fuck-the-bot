package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	telegramURL = "https://api.telegram.org/bot"
	aiURL       = "https://api.timeweb.ai/v1/chat/completions"
	maxBody     = 1 << 20
)

type config struct {
	telegramToken string
	aiKey         string
	model         string
	databaseURL   string
}

type telegramClient struct {
	http  *http.Client
	token string
}

type telegramResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

type user struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	IsBot     bool   `json:"is_bot"`
}

type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type message struct {
	MessageID       int64 `json:"message_id"`
	MessageThreadID int64 `json:"message_thread_id"`
	Date            int64 `json:"date"`
	Chat            struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From            *user    `json:"from"`
	Text            string   `json:"text"`
	Entities        []entity `json:"entities"`
	Caption         string   `json:"caption"`
	CaptionEntities []entity `json:"caption_entities"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

func main() {
	cfg := config{
		telegramToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		aiKey:         os.Getenv("TIMEWEB_AI_API_KEY"),
		model:         os.Getenv("AI_MODEL"),
		databaseURL:   os.Getenv("DATABASE_URL"),
	}
	if cfg.telegramToken == "" || cfg.aiKey == "" || cfg.databaseURL == "" {
		log.Fatal("set TELEGRAM_BOT_TOKEN, TIMEWEB_AI_API_KEY and DATABASE_URL")
	}
	if cfg.model == "" {
		cfg.model = "deepseek/deepseek-v4-pro"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	history, err := openHistoryStore(ctx, cfg.databaseURL)
	if err != nil {
		log.Fatalf("open history: %v", err)
	}
	defer history.close()
	tg := telegramClient{http: &http.Client{Timeout: 45 * time.Second}, token: cfg.telegramToken}
	aiHTTP := &http.Client{Timeout: 60 * time.Second}

	var me user
	if err := tg.call(ctx, "getMe", struct{}{}, &me); err != nil {
		log.Fatalf("getMe: %v", err)
	}
	if me.Username == "" {
		log.Fatal("bot has no username")
	}
	log.Printf("listening for @%s", me.Username)

	var offset int64
	for ctx.Err() == nil {
		if err := history.prune(ctx, time.Now()); err != nil {
			log.Printf("prune history: %v", err)
		}
		var updates []update
		err := tg.call(ctx, "getUpdates", struct {
			Offset         int64    `json:"offset"`
			Timeout        int      `json:"timeout"`
			AllowedUpdates []string `json:"allowed_updates"`
		}{offset, 30, []string{"message"}}, &updates)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("getUpdates: %v", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, item := range updates {
			if item.Message != nil && (item.Message.From == nil || !item.Message.From.IsBot) {
				msg := *item.Message
				prompt, mentioned := mentionedText(msg, me.Username)
				var priorConversation string
				if mentioned {
					priorConversation, err = history.conversation(ctx, msg.Chat.ID, msg.MessageThreadID, time.Now())
					if err != nil {
						log.Printf("load conversation for update %d: %v", item.UpdateID, err)
					}
				}
				if err := history.addIncoming(ctx, msg, time.Now()); err != nil {
					log.Printf("save incoming message %d: %v", msg.MessageID, err)
				}
				if mentioned {
					answer, err := askAI(ctx, aiHTTP, cfg.aiKey, cfg.model, prompt, priorConversation)
					if err != nil {
						log.Printf("AI request for update %d: %v", item.UpdateID, err)
						answer = "Не смог сейчас ответить: ИИ недоступен. Попробуй позже."
					}
					if err := tg.sendMessage(ctx, msg, answer); err != nil {
						log.Printf("sendMessage for update %d: %v", item.UpdateID, err)
					} else if err := history.addBotReply(ctx, msg.Chat.ID, msg.MessageThreadID, me.Username, answer, time.Now()); err != nil {
						log.Printf("save bot reply for update %d: %v", item.UpdateID, err)
					}
				}
			}
			offset = item.UpdateID + 1
		}
	}
}

func (tg telegramClient) call(ctx context.Context, method string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramURL+tg.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := tg.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %s", strings.ReplaceAll(err.Error(), tg.token, "[redacted]"))
	}
	defer resp.Body.Close()
	var result telegramResponse[json.RawMessage]
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, result.Description)
	}
	return json.Unmarshal(result.Result, output)
}

func (tg telegramClient) sendMessage(ctx context.Context, original message, answer string) error {
	// Telegram accepts at most 4096 characters per message. Keep the first
	// chunk attached to the mention and subsequent chunks in the same topic.
	runes := []rune(answer)
	for start := 0; start < len(runes); {
		end, units := start, 0
		for end < len(runes) && units+utf16RuneLength(runes[end]) <= 4000 {
			units += utf16RuneLength(runes[end])
			end++
		}
		input := struct {
			ChatID          int64  `json:"chat_id"`
			Text            string `json:"text"`
			MessageThreadID int64  `json:"message_thread_id,omitempty"`
			ReplyParameters *struct {
				MessageID                int64 `json:"message_id"`
				AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
			} `json:"reply_parameters,omitempty"`
		}{ChatID: original.Chat.ID, Text: string(runes[start:end]), MessageThreadID: original.MessageThreadID}
		if start == 0 {
			input.ReplyParameters = &struct {
				MessageID                int64 `json:"message_id"`
				AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
			}{original.MessageID, true}
		}
		var sent json.RawMessage
		if err := tg.call(ctx, "sendMessage", input, &sent); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func mentionedText(msg message, username string) (string, bool) {
	content, entities := msg.Text, msg.Entities
	if content == "" {
		content, entities = msg.Caption, msg.CaptionEntities
	}
	for _, e := range entities {
		if e.Type != "mention" {
			continue
		}
		start, end, ok := utf16ByteRange(content, e.Offset, e.Length)
		if !ok || !strings.EqualFold(content[start:end], "@"+username) {
			continue
		}
		text := strings.TrimSpace(content[:start] + content[end:])
		if text == "" {
			text = "Пользователь просто позвал тебя по имени. Ответь коротко."
		}
		return text, true
	}
	return "", false
}

func utf16ByteRange(s string, offset, length int) (int, int, bool) {
	if offset < 0 || length <= 0 || offset > int(^uint(0)>>1)-length || !utf8.ValidString(s) {
		return 0, 0, false
	}
	start, end, units := -1, -1, 0
	for i, r := range s {
		if units == offset {
			start = i
		}
		if units == offset+length {
			end = i
		}
		units += utf16RuneLength(r)
	}
	if units == offset {
		start = len(s)
	}
	if units == offset+length {
		end = len(s)
	}
	return start, end, start >= 0 && end >= start
}

func utf16RuneLength(r rune) int {
	if r > 0xffff {
		return 2
	}
	return 1
}

func askAI(ctx context.Context, client *http.Client, key, model, text, priorConversation string) (string, error) {
	const systemPrefix = "Общайся грубо и с матом, можешь очень саркастично шутить или просто посылать, нам весело. "
	if priorConversation == "" {
		priorConversation = "(за последние 24 часа сообщений не было)"
	}
	systemPrompt := systemPrefix + "\nранее был диалог в беседе:\n" + priorConversation + "\n\nТекущее сообщение: " + text + "\nВерни ответ строго в формате JSON: {\"reply\":\"твой ответ\"}. Никакого Markdown или текста вне JSON."
	input := struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{Model: model}
	input.Messages = append(input.Messages,
		struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"system", systemPrompt},
		struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{"user", "Ответь на текст сообщения выше."},
	)
	body, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, aiURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", errors.New("AI returned no choices")
	}
	return parseAIReply(result.Choices[0].Message.Content)
}

func parseAIReply(content string) (string, error) {
	var value struct {
		Reply string `json:"reply"`
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(content)))
	if err := decoder.Decode(&value); err != nil {
		return "", fmt.Errorf("AI returned invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", errors.New("AI returned extra text after JSON")
	}
	if strings.TrimSpace(value.Reply) == "" {
		return "", errors.New("AI returned an empty reply")
	}
	return value.Reply, nil
}
