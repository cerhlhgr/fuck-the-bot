package view

import (
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestJudgePromptAndDecision(t *testing.T) {
	prompt := JudgePrompt(model.JudgeRequest{MessageID: 42, Author: "@user", Text: "текст"})
	for _, part := range []string{"Статья 1", "Статья 2", "Статья 3", "ориентации", "Одного явно адресованного оскорбления достаточно", "unban_query", `"message_id":42`} {
		if !strings.Contains(prompt, part) {
			t.Fatalf("missing %q in judge prompt", part)
		}
	}
	decision, err := ParseJudgeDecision(`{"article":2,"evidence_message_id":42,"reason":"травля"}`, 42)
	if err != nil || decision.Article != 2 {
		t.Fatalf("unexpected judge decision: %+v, %v", decision, err)
	}
	vote, err := ParseJudgeDecision(`{"article":0,"evidence_message_id":0,"reason":"","unban_query":"@vasya"}`, 42)
	if err != nil || vote.UnbanQuery != "@vasya" || vote.Article != 0 {
		t.Fatalf("unexpected unban request: %+v, %v", vote, err)
	}
	for _, answer := range []string{
		`{"article":2,"evidence_message_id":41,"reason":"травля"}`,
		`{"article":4,"evidence_message_id":42,"reason":"травля"}`,
		`{"article":2,"evidence_message_id":42,"reason":""}`,
		`{"article":2,"evidence_message_id":42,"reason":"травля"} extra`,
		`{"article":2,"evidence_message_id":42,"reason":"травля","unban_query":"@vasya"}`,
	} {
		if _, err := ParseJudgeDecision(answer, 42); err == nil {
			t.Fatalf("accepted invalid judge answer: %s", answer)
		}
	}
}
