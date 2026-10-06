package model

import (
	"encoding/json"
	"testing"
)

func TestMentionedText(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		entities []Entity
		want     string
		found    bool
	}{
		{"mention after emoji", "😈 @MyBot привет", []Entity{{Type: "mention", Offset: 3, Length: 6}}, "😈  привет", true},
		{"other bot", "@OtherBot привет", []Entity{{Type: "mention", Offset: 0, Length: 9}}, "", false},
		{"plain text without entity", "@MyBot привет", nil, "", false},
		{"invalid UTF-16 offset", "😈 @MyBot", []Entity{{Type: "mention", Offset: 1, Length: 6}}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := MentionedText(Message{Text: tc.text, Entities: tc.entities}, "MyBot")
			if got != tc.want || found != tc.found {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, found, tc.want, tc.found)
			}
		})
	}
}

func TestCaptionAndCompactText(t *testing.T) {
	msg := Message{Caption: "@MyBot что на фото?", CaptionEntities: []Entity{{Type: "mention", Offset: 0, Length: 6}}}
	got, ok := MentionedText(msg, "mybot")
	if !ok || got != "что на фото?" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
	if got := CompactText("Привет,\n как   дела?", 20); got != "Привет, как дела?" {
		t.Fatalf("unexpected compact text: %q", got)
	}
}

func TestMessageHistoryTextIncludesPhotoDescription(t *testing.T) {
	var update Update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":2,"chat":{"id":-42},"photo":[{"file_id":"photo-1","width":500,"height":500,"file_size":1200}]}}`), &update); err != nil {
		t.Fatal(err)
	}
	if update.Message == nil || len(update.Message.Photo) != 1 || update.Message.Photo[0].FileID != "photo-1" {
		t.Fatalf("photo missing from Telegram update: %+v", update)
	}
	msg := Message{Caption: "Что скажешь?", Photo: []PhotoSize{{FileID: "photo-1"}}, PhotoDescription: "кот сидит на диване"}
	if got := MessageHistoryText(msg); got != "Что скажешь?\n[На фото: кот сидит на диване]" {
		t.Fatalf("history text = %q", got)
	}
	msg.Caption, msg.PhotoDescription = "", ""
	if got := MessageHistoryText(msg); got != "[Фото: содержимое недоступно для анализа]" {
		t.Fatalf("photo fallback = %q", got)
	}
}
