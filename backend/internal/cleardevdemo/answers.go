package cleardevdemo

import (
	"fmt"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

// FrozenSample is the import used to prove page, API, and SQLite agreement.
var FrozenSample = []string{
	"user@example.com",
	" user@example.com ",
	"USER@example.com",
	"not-an-email",
	"foo@example.com",
}

// FrozenSampleCounts is the expected result of FrozenSample.
var FrozenSampleCounts = map[string]int{
	"accepted":   2,
	"rejected":   1,
	"duplicates": 2,
}

func clarificationAnswers(view cleardevsvc.RequirementView) (map[string]any, error) {
	if view.ComplexPlanning == nil || len(view.ComplexPlanning.CompilationRequests) == 0 {
		return nil, fmt.Errorf("steward did not persist a clarification request")
	}
	request := view.ComplexPlanning.CompilationRequests[len(view.ComplexPlanning.CompilationRequests)-1]
	answers := make([]map[string]string, 0, len(view.ComplexPlanning.Questions))
	for _, question := range view.ComplexPlanning.Questions {
		if question.CompilationRequestID != request.ID {
			continue
		}
		text, ok := frozenClarificationText(question)
		if !ok {
			return nil, fmt.Errorf("no frozen answer for clarification %q (%s)", question.QuestionKey, question.Text)
		}
		answers = append(answers, map[string]string{"questionKey": question.QuestionKey, "text": text})
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("clarification request %s has no questions", request.ID)
	}
	return map[string]any{
		"compilationRequestId": request.ID,
		"clarificationRound":   request.ClarificationRound,
		"answers":              answers,
	}, nil
}

func frozenClarificationText(question core.ComplexClarificationQuestion) (string, bool) {
	blob := strings.ToLower(question.QuestionKey + " " + question.Text)
	switch {
	case strings.Contains(blob, "duplicate") || strings.Contains(question.Text, "重复"):
		return "重复地址忽略不计，只保留第一次出现的记录。", true
	case strings.Contains(blob, "invalid") || strings.Contains(question.Text, "无效") || strings.Contains(question.Text, "拒绝"):
		return "计入拒绝数量。", true
	default:
		return "", false
	}
}
