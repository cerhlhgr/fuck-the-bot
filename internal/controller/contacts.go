package controller

import (
	"strings"

	"fuck-the-bot/internal/model"
)

func wantsContactMention(messages []model.Message) bool {
	for _, msg := range messages {
		content := msg.Text + " " + msg.Caption
		if msg.ReplyToMessage != nil {
			content += " " + msg.ReplyToMessage.Text + " " + msg.ReplyToMessage.Caption
		}
		content = strings.ToLower(content)
		for _, word := range []string{"линкан", "линкуй", "линки на", "ссылку на", "позов", "позва", "отмет", "упомян", "тегни", "пингни", "контакт"} {
			if strings.Contains(content, word) {
				return true
			}
		}
	}
	return false
}
