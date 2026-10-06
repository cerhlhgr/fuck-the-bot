package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"fuck-the-bot/internal/model"
)

const MaxContextRunes = 12000

func Conversation(entries []model.HistoryEntry) string {
	var lines []string
	used := 0
	for _, entry := range entries { // newest first
		line := entry.Date.Local().Format("02.01 15:04") + " " + entry.Author + ": " + entry.Text
		size := len([]rune(line))
		if len(lines) > 0 {
			size++
		}
		if used+size > MaxContextRunes {
			break
		}
		lines = append(lines, line)
		used += size
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func SystemPrompt(text string, entries []model.HistoryEntry) string {
	prior := Conversation(entries)
	if prior == "" {
		prior = "(за последние 24 часа сообщений не было)"
	}
	return "Общайся грубо и с матом, можешь очень саркастично шутить или просто посылать, нам весело. " +
		"\nранее был диалог в беседе:\n" + prior +
		"\n\nТекущее сообщение: " + text +
		"\nВерни ответ строго в формате JSON: {\"reply\":\"твой ответ\"}. Никакого Markdown или текста вне JSON."
}

func ParseAIReply(content string) (string, error) {
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

func TelegramChunks(answer string) []string {
	runes := []rune(answer)
	var chunks []string
	for start := 0; start < len(runes); {
		end, units := start, 0
		for end < len(runes) && units+model.UTF16RuneLength(runes[end]) <= 4000 {
			units += model.UTF16RuneLength(runes[end])
			end++
		}
		chunks = append(chunks, string(runes[start:end]))
		start = end
	}
	return chunks
}
