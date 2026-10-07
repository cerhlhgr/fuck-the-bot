package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const endpoint = "https://commons.wikimedia.org/w/api.php"

var ErrNotFound = errors.New("no image found")

type Client struct {
	http     *http.Client
	endpoint string
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 10 * time.Second}, endpoint: endpoint}
}

func (c *Client) Search(ctx context.Context, query string) (string, error) {
	params := url.Values{
		"action":        {"query"},
		"generator":     {"search"},
		"gsrsearch":     {query},
		"gsrnamespace":  {"6"},
		"gsrlimit":      {"5"},
		"prop":          {"imageinfo"},
		"iiprop":        {"url|mime"},
		"format":        {"json"},
		"formatversion": {"2"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ShalunishkaBot/1.0 (Telegram bot image lookup)")
	resp, err := c.http.Do(req)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return "", fmt.Errorf("search Wikimedia Commons: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Wikimedia Commons HTTP %d", resp.StatusCode)
	}
	var result struct {
		Query struct {
			Pages []struct {
				Index     int `json:"index"`
				ImageInfo []struct {
					URL  string `json:"url"`
					MIME string `json:"mime"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return "", fmt.Errorf("decode Wikimedia Commons response: %w", err)
	}
	bestIndex, bestURL := int(^uint(0)>>1), ""
	for _, page := range result.Query.Pages {
		if len(page.ImageInfo) == 0 || !strings.HasPrefix(page.ImageInfo[0].MIME, "image/") {
			continue
		}
		candidate := page.ImageInfo[0].URL
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "upload.wikimedia.org" || parsed.User != nil {
			continue
		}
		if page.Index < bestIndex {
			bestIndex, bestURL = page.Index, candidate
		}
	}
	if bestURL == "" {
		return "", ErrNotFound
	}
	return bestURL, nil
}
