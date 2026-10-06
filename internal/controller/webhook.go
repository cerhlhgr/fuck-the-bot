package controller

import (
	"encoding/json"
	"io"
	"net/http"

	"fuck-the-bot/internal/model"
)

const (
	WebhookPath = "/telegram/webhook"
	maxBody     = 1 << 20
)

type Webhook struct {
	repo model.Repository
	wake chan struct{}
}

func NewWebhook(repo model.Repository) *Webhook {
	return &Webhook{repo: repo, wake: make(chan struct{}, 1)}
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
