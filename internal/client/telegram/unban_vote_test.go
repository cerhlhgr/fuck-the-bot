package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestUnbanPollAndMemberReleaseMethods(t *testing.T) {
	client := New("test-token")
	var methods []string
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		method := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		methods = append(methods, method)
		var input map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if string(input["chat_id"]) != "-10042" {
			t.Fatalf("wrong chat for %s: %v", method, input)
		}
		result := `true`
		switch method {
		case "sendPoll":
			if string(input["message_thread_id"]) != "29" || !strings.Contains(string(input["question"]), "@vasya") || !strings.Contains(string(input["question"]), "3 минуты") || string(input["is_anonymous"]) != "true" {
				t.Fatalf("wrong vote poll payload: %v", input)
			}
			var options []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input["options"], &options); err != nil || len(options) != 2 || options[0].Text != "За" || options[1].Text != "Против" {
				t.Fatalf("wrong vote options: %+v, %v", options, err)
			}
			result = `{"message_id":555}`
		case "stopPoll":
			if string(input["message_id"]) != "555" {
				t.Fatalf("wrong stopped poll: %v", input)
			}
			result = `{"options":[{"text":"За","voter_count":1},{"text":"Против","voter_count":0}]}`
		case "restrictChatMember":
			var permissions map[string]bool
			if err := json.Unmarshal(input["permissions"], &permissions); err != nil || !permissions["can_send_messages"] || permissions["can_pin_messages"] {
				t.Fatalf("unsafe unrestrict permissions: %v, %v", permissions, err)
			}
		case "unbanChatMember":
			if string(input["only_if_banned"]) != "true" {
				t.Fatalf("unban could remove an unbanned member: %v", input)
			}
		default:
			t.Fatalf("unexpected method: %s", method)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
	})
	msg := model.Message{MessageID: 77, MessageThreadID: 29}
	msg.Chat.ID = -10042
	ctx := context.Background()
	pollID, err := client.SendUnbanPoll(ctx, msg, "@vasya")
	if err != nil || pollID != 555 {
		t.Fatalf("vote poll was not sent: %d, %v", pollID, err)
	}
	result, err := client.StopUnbanPoll(ctx, -10042, pollID)
	if err != nil || result.Yes != 1 || result.No != 0 {
		t.Fatalf("wrong poll result: %+v, %v", result, err)
	}
	if err := client.UnrestrictChatMember(ctx, -10042, 3); err != nil {
		t.Fatal(err)
	}
	if err := client.UnbanChatMember(ctx, -10042, 3); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "sendPoll,stopPoll,restrictChatMember,unbanChatMember" {
		t.Fatalf("wrong calls: %v", methods)
	}
}
