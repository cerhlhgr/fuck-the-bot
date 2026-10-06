package images

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

func TestSearchReturnsFirstImageURL(t *testing.T) {
	client := New()
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "commons.wikimedia.org" || req.URL.Query().Get("gsrsearch") != "cat" || req.URL.Query().Get("gsrnamespace") != "6" || req.Header.Get("User-Agent") == "" {
			t.Fatalf("wrong search request: %s", req.URL)
		}
		body := `{"query":{"pages":[{"index":2,"imageinfo":[{"url":"https://upload.wikimedia.org/second.jpg","mime":"image/jpeg"}]},{"index":1,"imageinfo":[{"url":"https://upload.wikimedia.org/first.jpg","mime":"image/jpeg"}]}]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	got, err := client.Search(context.Background(), "cat")
	if err != nil || got != "https://upload.wikimedia.org/first.jpg" {
		t.Fatalf("search result = %q, %v", got, err)
	}
}

func TestSearchRejectsMissingOrUntrustedImage(t *testing.T) {
	client := New()
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"query":{"pages":[{"index":1,"imageinfo":[{"url":"https://example.com/first.jpg","mime":"image/jpeg"}]}]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if _, err := client.Search(context.Background(), "cat"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
}
