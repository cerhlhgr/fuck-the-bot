package controller

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"fuck-the-bot/internal/model"
)

const (
	WebhookPath = "/telegram/webhook"
	maxBody     = 1 << 20
)

type Webhook struct {
	repo model.Repository
}

func NewWebhook(repo model.Repository) *Webhook {
	return &Webhook{repo: repo}
}

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
	started := time.Now()
	log.Printf("webhook request received method=%s content_length=%d", r.Method, r.ContentLength)
	if r.Method != http.MethodPost {
		log.Printf("webhook request rejected reason=method_not_allowed method=%s", r.Method)
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		log.Printf("webhook request rejected reason=read_body error=%v", err)
		http.Error(w, "cannot read request", http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		log.Printf("webhook request rejected reason=body_too_large bytes=%d", len(body))
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var item model.Update
	if err := json.Unmarshal(body, &item); err != nil {
		log.Printf("webhook request rejected reason=invalid_json error=%v", err)
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}
	if item.UpdateID <= 0 {
		log.Printf("webhook request rejected reason=missing_update_id")
		http.Error(w, "invalid update", http.StatusBadRequest)
		return
	}
	chatID, messageID := int64(0), int64(0)
	if item.Message != nil {
		chatID, messageID = item.Message.Chat.ID, item.Message.MessageID
	}
	if err := h.repo.EnqueueUpdate(r.Context(), item, body); err != nil {
		log.Printf("webhook queue failed update_id=%d chat_id=%d message_id=%d error=%v", item.UpdateID, chatID, messageID, err)
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	log.Printf("webhook accepted update_id=%d chat_id=%d message_id=%d duration=%s", item.UpdateID, chatID, messageID, time.Since(started))
	w.WriteHeader(http.StatusOK)
}
