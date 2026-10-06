package controller

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"fuck-the-bot/internal/model"
)

const (
	WebhookPath = "/telegram/webhook"
	maxBody     = 1 << 20
)

type Webhook struct {
	repo   model.Repository
	secret string
	wake   chan struct{}
}

func NewWebhook(repo model.Repository, secret string) *Webhook {
	return &Webhook{repo: repo, secret: secret, wake: make(chan struct{}, 1)}
}

func (h *Webhook) Wake() <-chan struct{} { return h.wake }

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
		return
	}
	if r.URL.Path != WebhookPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	provided := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
	if subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "cannot read request", http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var item model.Update
	if err := json.Unmarshal(body, &item); err != nil || item.UpdateID <= 0 {
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}
	if err := h.repo.EnqueueUpdate(r.Context(), item, body); err != nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	select {
	case h.wake <- struct{}{}:
	default:
	}
	w.WriteHeader(http.StatusOK)
}

func ValidateWebhookConfig(rawURL, secret string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Path != WebhookPath || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("WEBHOOK_URL must be an HTTPS URL ending in %s", WebhookPath)
	}
	if port := parsed.Port(); port != "" && port != "443" && port != "80" && port != "88" && port != "8443" {
		return errors.New("WEBHOOK_URL uses a port Telegram does not support")
	}
	if len(secret) < 1 || len(secret) > 256 {
		return errors.New("WEBHOOK_SECRET must contain 1-256 characters")
	}
	for _, r := range secret {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-", r)) {
			return errors.New("WEBHOOK_SECRET may contain only letters, digits, _ and -")
		}
	}
	return nil
}
