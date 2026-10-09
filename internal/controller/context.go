package controller

import "fuck-the-bot/internal/model"

// selectDecisionHistory passes the full hour-long dialogue to the model.
// Webhooks may store messages that arrive after this batch was assembled;
// those messages belong to the next decision and must not leak into this one.
func selectDecisionHistory(history []model.HistoryEntry, messages []model.Message) []model.HistoryEntry {
	if len(messages) == 0 {
		return nil
	}
	maxNewID := messages[0].MessageID
	for _, msg := range messages[1:] {
		if msg.MessageID > maxNewID {
			maxNewID = msg.MessageID
		}
	}
	selected := make([]model.HistoryEntry, 0, len(history))
	for _, entry := range history {
		if !entry.Bot && entry.MessageID > maxNewID {
			continue
		}
		selected = append(selected, entry)
	}
	return selected
}
