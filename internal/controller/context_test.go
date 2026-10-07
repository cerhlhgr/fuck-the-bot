package controller

import (
	"strings"
	"testing"
	"unicode/utf8"

	"fuck-the-bot/internal/model"
)

func TestSelectDecisionHistoryKeepsNewRepliesAndRelevantOlderRows(t *testing.T) {
	var history []model.HistoryEntry
	for id := int64(1); id <= 30; id++ {
		history = append(history, model.HistoryEntry{MessageID: id, Text: "другая болтовня " + strings.Repeat("я", 700)})
	}
	history[2].Text = "Встреча команды завтра у входа"
	history[4].Text = "Родительское сообщение с нужным пояснением"
	newText := "Что со встречей? " + strings.Repeat("важно ", 1500)
	history = append(history,
		model.HistoryEntry{MessageID: 31, Text: newText},
		model.HistoryEntry{MessageID: 32, Text: "сообщение следующего запуска"},
	)
	msg := model.Message{MessageID: 31, Text: newText, ReplyToMessage: &model.Message{MessageID: 5}}
	selected := selectDecisionHistory(history, []model.Message{msg})
	seen := make(map[int64]model.HistoryEntry)
	oldCharacters := 0
	for i, entry := range selected {
		if i > 0 && entry.MessageID <= selected[i-1].MessageID {
			t.Fatalf("context order changed: %+v", selected)
		}
		seen[entry.MessageID] = entry
		if entry.MessageID != 31 {
			oldCharacters += utf8.RuneCountInString(entry.Text)
		}
	}
	if seen[3].MessageID != 3 || seen[5].MessageID != 5 || seen[31].Text != newText || seen[32].MessageID != 0 || seen[8].MessageID != 0 {
		t.Fatalf("context selection lost required rows or included unrelated ones: %+v", seen)
	}
	if oldCharacters > oldContextCharacters {
		t.Fatalf("older context exceeds budget: %d > %d", oldCharacters, oldContextCharacters)
	}
}

func TestSelectDecisionHistoryKeepsRecentRowsDuringLargeNewBatch(t *testing.T) {
	history := []model.HistoryEntry{
		{MessageID: 1, Text: "старый контекст"},
		{MessageID: 2, Text: "самая недавняя реплика до новой пачки"},
	}
	var messages []model.Message
	for id := int64(3); id <= 30; id++ {
		history = append(history, model.HistoryEntry{MessageID: id, Text: "новое"})
		messages = append(messages, model.Message{MessageID: id, Text: "новое"})
	}
	selected := selectDecisionHistory(history, messages)
	if len(selected) != len(history) || selected[0].MessageID != 1 || selected[1].MessageID != 2 || selected[len(selected)-1].MessageID != 30 {
		t.Fatalf("large batch displaced previous context: %+v", selected)
	}
}
