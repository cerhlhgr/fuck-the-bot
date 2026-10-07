package controller

import (
	"net/url"
	"strings"

	"fuck-the-bot/internal/model"
)

func formatSearchResults(kind, caption string, results []model.SearchResult) string {
	if len(results) == 0 {
		return "По запросу ничего не нашёл."
	}
	var lines []string
	if caption = model.CompactText(caption, 300); caption != "" {
		lines = append(lines, caption)
	}
	limit := 3
	if kind == "images" {
		limit = 2
	}
	added := 0
	for _, result := range results {
		if added >= limit {
			break
		}
		if !validSearchURL(result.URL) {
			continue
		}
		title := model.CompactText(result.Title, 100)
		if title == "" {
			title = "Результат"
		}
		entry := title + "\n" + result.URL
		if kind == "images" {
			if validSearchURL(result.SourceURL) && result.SourceURL != result.URL {
				entry += "\nИсточник: " + result.SourceURL
			}
		} else if description := model.CompactText(result.Description, 180); description != "" {
			entry = title + " — " + description + "\n" + result.URL
		}
		if len(strings.Join(append(lines, entry), "\n\n")) > 3800 {
			continue
		}
		lines = append(lines, entry)
		added++
	}
	if added == 0 {
		return "По запросу ничего не нашёл."
	}
	return strings.Join(lines, "\n\n")
}

func validSearchURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 1500 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
