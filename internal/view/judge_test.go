package view

import (
	"strings"
	"testing"

	"fuck-the-bot/internal/model"
)

func TestJudgePromptAndDecision(t *testing.T) {
	prompt := JudgePrompt(model.JudgeRequest{MessageID: 42, Author: "@user", Text: "текст"})
	for _, part := range []string{"Статья 1", "Статья 2", "Статья 3", "ориентации", "прямую агрессию", "взаимный подкол", "direct_aggression", "aggression_quote", "unban_query", `"message_id":42`} {
		if !strings.Contains(prompt, part) {
			t.Fatalf("missing %q in judge prompt", part)
		}
	}
	decision, err := ParseJudgeDecision(`{"article":2,"evidence_message_id":42,"reason":"прямая агрессия","target":"@vasya","bullying_basis":"direct_aggression","aggression_quote":"заткнись, ты тупой"}`, 42, "@vasya, заткнись, ты тупой, всем надоел")
	if err != nil || decision.Article != 2 || decision.Target != "@vasya" || decision.BullyingBasis != "direct_aggression" || decision.AggressionQuote != "заткнись, ты тупой" {
		t.Fatalf("unexpected judge decision: %+v, %v", decision, err)
	}
	for _, answer := range []string{
		`{"article":2,"evidence_message_id":42,"reason":"мат"}`,
		`{"article":2,"evidence_message_id":42,"reason":"оскорбление","target":"@vasya"}`,
		`{"article":2,"evidence_message_id":42,"reason":"оскорбление","target":"@vasya","bullying_basis":"joke"}`,
		`{"article":2,"evidence_message_id":42,"reason":"подколы","target":"@vasya","bullying_basis":"repeated","aggression_quote":"Вася, опять проспал"}`,
		`{"article":2,"evidence_message_id":42,"reason":"подколы","target":"@vasya","bullying_basis":"direct_aggression","aggression_quote":"ты тупой"}`,
	} {
		decision, err := ParseJudgeDecision(answer, 42, "Вася, опять проспал, красавец 😂")
		if err != nil || decision.Article != 0 {
			t.Fatalf("unsubstantiated bullying must not trigger moderation: %+v, %v", decision, err)
		}
	}
	vote, err := ParseJudgeDecision(`{"article":0,"evidence_message_id":0,"reason":"","unban_query":"@vasya"}`, 42, "Размутьте Васю")
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
		if _, err := ParseJudgeDecision(answer, 42, "текст"); err == nil {
			t.Fatalf("accepted invalid judge answer: %s", answer)
		}
	}
}
