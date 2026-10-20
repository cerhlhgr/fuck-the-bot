package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"fuck-the-bot/internal/model"
)

func TestJudgeTelegramMethods(t *testing.T) {
	client := New("test-token")
	var methods []string
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		methods = append(methods, method)
		var data map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		if string(data["chat_id"]) != "-10042" || (method != "deleteMessage" && method != "forwardMessage" && string(data["user_id"]) != "42") {
			t.Fatalf("wrong moderation target for %s: %v", method, data)
		}
		if method == "forwardMessage" && (string(data["from_chat_id"]) != "-10042" || string(data["message_id"]) != "7" || string(data["message_thread_id"]) != "29") {
			t.Fatalf("wrong forwarded evidence: %v", data)
		}
		if method == "restrictChatMember" {
			var permissions map[string]bool
			if err := json.Unmarshal(data["permissions"], &permissions); err != nil || permissions["can_send_messages"] {
				t.Fatalf("restriction still allows messages: %v, %v", permissions, err)
			}
			if string(data["until_date"]) == "0" {
				t.Fatal("restriction is permanent")
			}
		}
		result := `true`
		if method == "getChatMember" {
			result = `{"status":"administrator","can_restrict_members":true,"can_delete_messages":true}`
		} else if method == "forwardMessage" {
			result = `{"message_id":900}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
	})
	ctx := context.Background()
	member, err := client.GetChatMember(ctx, -10042, 42)
	if err != nil || !member.IsAdministrator() || !member.CanRestrictMembers {
		t.Fatalf("wrong member permissions: %+v, %v", member, err)
	}
	msg := model.Message{MessageID: 7, MessageThreadID: 29}
	msg.Chat.ID = -10042
	forwardedID, err := client.ForwardMessage(ctx, msg)
	if err != nil || forwardedID != 900 {
		t.Fatalf("wrong forwarded message: %d, %v", forwardedID, err)
	}
	if err := client.RestrictChatMember(ctx, -10042, 42, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := client.KickChatMember(ctx, -10042, 42); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteMessage(ctx, -10042, 7); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "getChatMember,forwardMessage,restrictChatMember,banChatMember,deleteMessage" {
		t.Fatalf("wrong Telegram API methods: %v", methods)
	}
}

func TestUnforwardableJudgeEvidence(t *testing.T) {
	client := New("test-token")
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"ok":false,"description":"Bad Request: message can't be forwarded"}`))}, nil
	})
	msg := model.Message{MessageID: 7}
	msg.Chat.ID = -10042
	_, err := client.ForwardMessage(context.Background(), msg)
	if !errors.Is(err, model.ErrMessageCannotForward) {
		t.Fatalf("wrong unforwardable error: %v", err)
	}
}
