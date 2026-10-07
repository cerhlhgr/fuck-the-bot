package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

type fakeWebSearcher struct {
	kind, query string
	results     []model.SearchResult
	err         error
}

func (f *fakeWebSearcher) Search(_ context.Context, kind, query string) ([]model.SearchResult, error) {
	f.kind, f.query = kind, query
	return f.results, f.err
}

func TestWorkerSearchesVideosAndSendsSources(t *testing.T) {
	repo := &fakeRepo{}
	msg := model.Message{MessageID: 81, Text: "Найди видео про ремонт велосипеда"}
	mentionTestMessage(&msg, "mybot")
	msg.Chat.ID = -42
	search := &fakeWebSearcher{results: []model.SearchResult{{Title: "Ремонт", URL: "https://video.example.org/watch", Description: "Как починить колесо"}}}
	ai := &fakeAI{decision: model.Decision{Actions: []model.Decision{{Action: "search", SearchType: "videos", SearchQuery: "ремонт велосипеда", Caption: "Вот видео:", ReplyToMessageID: 81}}}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Search: search, Username: "mybot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 1, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if !ai.request.SearchEnabled || search.kind != "videos" || search.query != "ремонт велосипеда" || len(tg.answers) != 1 || !strings.Contains(tg.answers[0], "https://video.example.org/watch") || tg.messages[0].MessageID != 81 || len(repo.botReplies) != 1 {
		t.Fatalf("web search action failed: search=%+v answers=%+v targets=%+v", search, tg.answers, tg.messages)
	}
}

func TestImageSearchFallsBackToCommons(t *testing.T) {
	repo := &fakeRepo{}
	msg := model.Message{MessageID: 82, Text: "Найди картинку кота"}
	mentionTestMessage(&msg, "mybot")
	msg.Chat.ID = -42
	search := &fakeWebSearcher{err: errors.New("gateway down")}
	images := &fakeImageSearcher{url: "https://upload.wikimedia.org/cat.jpg"}
	ai := &fakeAI{decision: model.Decision{Actions: []model.Decision{{Action: "search", SearchType: "images", SearchQuery: "cat", ReplyToMessageID: 82}}}}
	tg := &fakeTelegram{}
	worker := Worker{Repo: repo, AI: ai, Telegram: tg, Search: search, Images: images, Username: "mybot"}
	if err := worker.Process(context.Background(), model.Update{UpdateID: 2, Message: &msg}); err != nil {
		t.Fatal(err)
	}
	if search.kind != "images" || images.query != "cat" || len(tg.answers) != 1 || !strings.Contains(tg.answers[0], "https://upload.wikimedia.org/cat.jpg") {
		t.Fatalf("image fallback failed: search=%+v images=%+v answers=%+v", search, images, tg.answers)
	}
}

func TestFormatSearchResultsRejectsUnsafeLinks(t *testing.T) {
	got := formatSearchResults("web", "Вот:", []model.SearchResult{{Title: "Wrong", URL: "javascript:alert(1)"}, {Title: "Safe", URL: "https://example.org", Description: "A page"}})
	if strings.Contains(got, "javascript:") || !strings.Contains(got, "https://example.org") || !strings.Contains(got, "A page") {
		t.Fatalf("search formatting = %q", got)
	}
}

func TestFormatSearchResultsEmpty(t *testing.T) {
	if got := formatSearchResults("news", "Лови", nil); got != "По запросу ничего не нашёл." {
		t.Fatalf("empty results = %q", got)
	}
}

func TestFormatSearchResultsFitsTelegramMessage(t *testing.T) {
	longURL := "https://example.org/" + strings.Repeat("a", 1480)
	results := []model.SearchResult{
		{Title: "Первый", URL: longURL},
		{Title: "Второй", URL: longURL + "b"},
		{Title: "Третий", URL: longURL + "c"},
	}
	got := formatSearchResults("web", "Результаты:", results)
	if len(got) > 3800 || !strings.Contains(got, "Первый") || strings.Contains(got, "Третий") {
		t.Fatalf("search result size = %d", len(got))
	}
}
