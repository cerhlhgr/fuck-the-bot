package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"fuck-the-bot/internal/model"
)

const endpoint = "https://api.search.brave.com/res/v1"

type Client struct {
	http     *http.Client
	endpoint string
	key      string
}

func New(key string) *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}, endpoint: endpoint, key: key}
}

func (c *Client) Search(ctx context.Context, kind, query string) ([]model.SearchResult, error) {
	if c.key == "" {
		return nil, errors.New("Brave Search API key is missing")
	}
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 400 || len(strings.Fields(query)) > 50 {
		return nil, errors.New("invalid search query")
	}
	switch kind {
	case "web", "images", "videos", "news":
	default:
		return nil, fmt.Errorf("unsupported search type %q", kind)
	}
	params := url.Values{"q": {query}, "count": {"5"}}
	for _, r := range query {
		if unicode.Is(unicode.Cyrillic, r) {
			params.Set("search_lang", "ru")
			break
		}
	}
	if kind == "images" {
		params.Set("safesearch", "strict")
	} else {
		params.Set("safesearch", "moderate")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/"+kind+"/search?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", c.key)
	req.Header.Set("User-Agent", "ShalunishkaBot/1.0 (Telegram search)")
	resp, err := c.do(ctx, req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return nil, fmt.Errorf("Brave %s search: %w", kind, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Brave %s search HTTP %d", kind, resp.StatusCode)
	}
	type item struct {
		Title       string `json:"title"`
		URL         string `json:"url"`
		Description string `json:"description"`
		Properties  struct {
			URL string `json:"url"`
		} `json:"properties"`
		Thumbnail struct {
			Src string `json:"src"`
		} `json:"thumbnail"`
	}
	var payload struct {
		Web struct {
			Results []item `json:"results"`
		} `json:"web"`
		Results []item `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Brave %s search: %w", kind, err)
	}
	items := payload.Results
	if kind == "web" {
		items = payload.Web.Results
	}
	seen := make(map[string]bool, len(items))
	var results []model.SearchResult
	for _, item := range items {
		result := model.SearchResult{Title: item.Title, URL: item.URL, Description: item.Description}
		if kind == "images" {
			result.URL = item.Properties.URL
			if !validResultURL(result.URL) {
				result.URL = item.Thumbnail.Src
			}
			if validResultURL(item.URL) {
				result.SourceURL = item.URL
			}
		}
		if !validResultURL(result.URL) || seen[result.URL] {
			continue
		}
		seen[result.URL] = true
		results = append(results, result)
		if len(results) == 3 {
			break
		}
	}
	return results, nil
}

func (c *Client) do(ctx context.Context, req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt > 0 {
			return resp, nil
		}
		_ = resp.Body.Close()
		wait := time.Second
		if seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && seconds > 0 && seconds <= 3 {
			wait = time.Duration(seconds) * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func validResultURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 1500 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
