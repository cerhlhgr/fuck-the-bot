package controller

import (
	"regexp"
	"strconv"
	"strings"

	"fuck-the-bot/internal/model"
)

var decimalReference = regexp.MustCompile(`\b[0-9]+\b`)

// Message IDs are useful to the API, but a bare number does not identify a
// message for a person reading the chat. Expand known references in verdicts.
func clarifyMessageReferences(answer string, history []model.HistoryEntry) string {
	if answer == "" || len(history) == 0 {
		return answer
	}
	lower := strings.ToLower(answer)
	if !strings.Contains(lower, "вердикт") && !strings.Contains(lower, "сообщени") && !strings.Contains(lower, "реплик") {
		return answer
	}
	byID := make(map[int64]model.HistoryEntry, len(history))
	for _, entry := range history {
		if entry.MessageID > 0 && !entry.Bot {
			byID[entry.MessageID] = entry
		}
	}
	if len(byID) == 0 {
		return answer
	}
	count := 0
	return decimalReference.ReplaceAllStringFunc(answer, func(raw string) string {
		if count >= 8 {
			return raw
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return raw
		}
		entry, found := byID[id]
		if !found {
			return raw
		}
		count++
		quote := model.CompactText(entry.Text, 100)
		if quote == "" || quote == "[сообщение без текста]" {
			return "сообщение " + entry.Author
		}
		if entry.Author == "" {
			return "«" + quote + "»"
		}
		return "«" + quote + "» (" + entry.Author + ")"
	})
}
