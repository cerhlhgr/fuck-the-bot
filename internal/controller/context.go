package controller

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"fuck-the-bot/internal/model"
)

const (
	oldContextCharacters = 4000
	recentContextRows    = 8
	relevantContextRows  = 3
	oldRowCharacters     = 700
	replyRowCharacters   = 1200
)

// selectDecisionHistory keeps every new message intact. Older rows are selected
// by reply relation, term overlap and recency, then bounded before going to AI.
func selectDecisionHistory(history []model.HistoryEntry, messages []model.Message) []model.HistoryEntry {
	newIDs := make(map[int64]bool, len(messages))
	replyIDs := make(map[int64]bool, len(messages))
	queryTerms := make(map[string]bool)
	var maxNewID int64
	for _, msg := range messages {
		newIDs[msg.MessageID] = true
		if msg.MessageID > maxNewID {
			maxNewID = msg.MessageID
		}
		if msg.ReplyToMessage != nil {
			replyIDs[msg.ReplyToMessage.MessageID] = true
		}
		for _, term := range contextTerms(model.MessageHistoryText(msg)) {
			queryTerms[term] = true
		}
	}

	// A webhook can persist a newer message while this worker is preparing the
	// current batch. It belongs to the next decision, not this one.
	eligible := make([]model.HistoryEntry, 0, len(history))
	for _, entry := range history {
		if !entry.Bot && entry.MessageID > maxNewID && !newIDs[entry.MessageID] {
			continue
		}
		eligible = append(eligible, entry)
	}
	selected := make(map[int]model.HistoryEntry)
	remaining := oldContextCharacters
	addOld := func(index, maxCharacters int) {
		if _, exists := selected[index]; exists || remaining < 80 {
			return
		}
		entry := eligible[index]
		text := []rune(entry.Text)
		limit := maxCharacters
		if limit > remaining {
			limit = remaining
		}
		if len(text) > limit {
			entry.Text = string(text[:limit-1]) + "…"
			remaining -= limit
		} else {
			remaining -= len(text)
		}
		selected[index] = entry
	}
	for i, entry := range eligible {
		if newIDs[entry.MessageID] && !entry.Bot {
			selected[i] = entry
		}
	}
	for i, entry := range eligible {
		if replyIDs[entry.MessageID] && !entry.Bot {
			addOld(i, replyRowCharacters)
		}
	}

	var olderIndices []int
	for i, entry := range eligible {
		if !entry.Bot && newIDs[entry.MessageID] {
			continue
		}
		olderIndices = append(olderIndices, i)
	}
	recentStart := len(olderIndices) - recentContextRows
	if recentStart < 0 {
		recentStart = 0
	}
	type match struct{ index, score int }
	var matches []match
	for _, i := range olderIndices[:recentStart] {
		if _, exists := selected[i]; exists {
			continue
		}
		score := 0
		for _, term := range contextTerms(eligible[i].Text + " " + eligible[i].Author) {
			if queryTerms[term] {
				score += utf8.RuneCountInString(term)
			}
		}
		if score > 0 {
			matches = append(matches, match{i, score})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].index > matches[j].index
	})
	for i := 0; i < len(matches) && i < relevantContextRows; i++ {
		addOld(matches[i].index, oldRowCharacters)
	}
	for i := len(olderIndices) - 1; i >= recentStart; i-- {
		index := olderIndices[i]
		if _, exists := selected[index]; !exists {
			addOld(index, oldRowCharacters)
		}
	}

	result := make([]model.HistoryEntry, 0, len(selected))
	for i := range eligible {
		if chosen, ok := selected[i]; ok {
			result = append(result, chosen)
		}
	}
	return result
}

func contextTerms(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool, len(fields))
	terms := make([]string, 0, len(fields))
	for _, term := range fields {
		runes := []rune(term)
		if len(runes) < 4 {
			continue
		}
		if len(runes) >= 6 {
			term = string(runes[:5])
		}
		if seen[term] {
			continue
		}
		seen[term] = true
		terms = append(terms, term)
	}
	return terms
}
