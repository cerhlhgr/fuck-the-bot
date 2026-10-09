package controller

import (
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestSelectDecisionHistoryKeepsEntireConversation(t *testing.T) {
	var history []model.HistoryEntry
	for id := int64(1); id <= 100; id++ {
		history = append(history, model.HistoryEntry{MessageID: id, Text: strings.Repeat("важная реплика ", 120)})
	}
	history = append(history, model.HistoryEntry{Bot: true, Text: "Ответ бота"})
	selected := selectDecisionHistory(history, []model.Message{{MessageID: 100, Text: "Что решили?"}})
	if len(selected) != len(history) || selected[0].Text != history[0].Text || selected[99].Text != history[99].Text || !selected[100].Bot {
		t.Fatalf("hour-long conversation was shortened: got=%d want=%d", len(selected), len(history))
	}
}

func TestSelectDecisionHistoryExcludesMessagesAfterCurrentBatch(t *testing.T) {
	history := []model.HistoryEntry{
		{MessageID: 8, Text: "обсуждение"},
		{MessageID: 9, Text: "обращение к боту"},
		{MessageID: 10, Text: "сообщение следующего запуска"},
	}
	selected := selectDecisionHistory(history, []model.Message{{MessageID: 9}})
	if len(selected) != 2 || selected[0].MessageID != 8 || selected[1].MessageID != 9 {
		t.Fatalf("future message leaked into decision: %+v", selected)
	}
	if got := selectDecisionHistory(history, nil); got != nil {
		t.Fatalf("history without a batch: %+v", got)
	}
}
