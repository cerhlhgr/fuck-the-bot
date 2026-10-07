package search

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSearchParsesAllResultTypes(t *testing.T) {
	cases := []struct {
		kind, body, wantURL, wantSource string
	}{
		{"web", `{"web":{"results":[{"title":"Example","url":"https://example.org/page","description":"A page"}]}}`, "https://example.org/page", ""},
		{"news", `{"results":[{"title":"News","url":"https://news.example.org/story","description":"A story"}]}`, "https://news.example.org/story", ""},
		{"videos", `{"results":[{"title":"Video","url":"https://video.example.org/watch/1","description":"A clip"}]}`, "https://video.example.org/watch/1", ""},
		{"images", `{"results":[{"title":"Photo","url":"https://example.org/photo","properties":{"url":"https://cdn.example.org/photo.jpg"}}]}`, "https://cdn.example.org/photo.jpg", "https://example.org/photo"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			client := New("test-key")
			client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/res/v1/"+tc.kind+"/search" || req.URL.Query().Get("q") != "кот видео" || req.URL.Query().Get("count") != "5" || req.URL.Query().Get("search_lang") != "ru" || req.Header.Get("X-Subscription-Token") != "test-key" {
					t.Fatalf("wrong search request: %s headers=%v", req.URL, req.Header)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			results, err := client.Search(context.Background(), tc.kind, "кот видео")
			if err != nil || len(results) != 1 || results[0].URL != tc.wantURL || results[0].SourceURL != tc.wantSource {
				t.Fatalf("results = %+v, %v", results, err)
			}
		})
	}
}

func TestSearchRejectsUnsafeAndDuplicateLinks(t *testing.T) {
	client := New("test-key")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"web":{"results":[{"title":"bad","url":"javascript:alert(1)"},{"title":"good","url":"https://example.org"},{"title":"duplicate","url":"https://example.org"},{"title":"bad userinfo","url":"https://evil@example.org"}]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	results, err := client.Search(context.Background(), "web", "test")
	if err != nil || len(results) != 1 || results[0].URL != "https://example.org" {
		t.Fatalf("untrusted results passed through: %+v, %v", results, err)
	}
	if _, err := client.Search(context.Background(), "other", "test"); err == nil {
		t.Fatal("unknown search type accepted")
	}
}

func TestSearchDoesNotExposeKeyOnHTTPError(t *testing.T) {
	client := New("private-test-key")
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"message":"private-test-key"}`))}, nil
	})
	_, err := client.Search(context.Background(), "web", "test")
	if err == nil || strings.Contains(err.Error(), "private-test-key") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSearchRequiresKey(t *testing.T) {
	if _, err := New("").Search(context.Background(), "web", "test"); err == nil {
		t.Fatal("search without API key was accepted")
	}
}

func TestSearchNetworkErrorDoesNotLogQuery(t *testing.T) {
	client := New("test-key")
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})
	_, err := client.Search(context.Background(), "web", "частный запрос")
	if err == nil || strings.Contains(err.Error(), "частный запрос") || !strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("query leaked in error: %v", err)
	}
}

func TestSearchRetriesOneRateLimitResponse(t *testing.T) {
	client := New("test-key")
	attempts := 0
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"1"}}, Body: io.NopCloser(strings.NewReader(`{"error":"rate limited"}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"web":{"results":[{"title":"OK","url":"https://example.org"}]}}`))}, nil
	})
	results, err := client.Search(context.Background(), "web", "test")
	if err != nil || attempts != 2 || len(results) != 1 {
		t.Fatalf("rate limit retry: attempts=%d results=%+v err=%v", attempts, results, err)
	}
}
