package cleardev

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxPlannerAnswerContinuations bounds explicit answer successors per confirmed specification.
const MaxPlannerAnswerContinuations = 2

// PlannerClarificationAnswer is immutable context for one successor planning
// request. It does not revise the confirmed requirement or grant authority.
type PlannerClarificationAnswer struct {
	RequestID                string    `json:"requestId"`
	DevelopmentRequirementID string    `json:"developmentRequirementId"`
	PlanID                   string    `json:"planId"`
	PlanSHA256               string    `json:"planSha256"`
	Questions                []string  `json:"questions"`
	Answers                  []string  `json:"answers"`
	AnswersSHA256            string    `json:"answersSha256"`
	NextPlanningRequestID    string    `json:"nextPlanningRequestId"`
	CreatedAt                time.Time `json:"createdAt"`
}

// PlannerClarificationView exposes the exact question and current submission eligibility.
type PlannerClarificationView struct {
	PlanID     string                      `json:"planId"`
	PlanSHA256 string                      `json:"planSha256"`
	Questions  []string                    `json:"questions"`
	CanAnswer  bool                        `json:"canAnswer"`
	ReasonCode ReasonCode                  `json:"reasonCode,omitempty"`
	Answer     *PlannerClarificationAnswer `json:"answer,omitempty"`
}

// NormalizePlannerAnswers validates and hashes the complete ordered answer set.
func NormalizePlannerAnswers(questions, answers []string) ([]string, string, error) {
	if len(questions) == 0 || len(answers) != len(questions) {
		return nil, "", errors.New("answer every current Planner question")
	}
	normalized := make([]string, len(answers))
	total := 0
	for i, answer := range answers {
		normalized[i] = strings.TrimSpace(answer)
		total += utf8.RuneCountInString(normalized[i])
		if normalized[i] == "" || total > MaxComplexAnswerRunes {
			return nil, "", errors.New("planner answers must be nonempty and at most 10000 characters in total")
		}
	}
	raw, err := CanonicalJSONBytes(normalized)
	if err != nil {
		return nil, "", err
	}
	return normalized, sha256Hex(raw), nil
}

// PlannerAnswerForPlan finds a persisted answer bound to this immutable question.
func PlannerAnswerForPlan(snapshot ComplexPlanningSnapshot, plan ComplexEngineeringPlan) (PlannerClarificationAnswer, bool) {
	clarification, ok := PlannerClarificationForPlan(plan)
	if !ok {
		return PlannerClarificationAnswer{}, false
	}
	for _, answer := range snapshot.PlannerAnswers {
		if answer.PlanID != plan.ID || answer.PlanSHA256 != plan.PlanSHA256 || answer.DevelopmentRequirementID != plan.DevelopmentRequirementID || answer.RequestID == "" || answer.NextPlanningRequestID != fmt.Sprintf("cleardev-complex-plan-%s-%d", plan.RequirementVersionID, plannerNextRound(snapshot, plan)) {
			continue
		}
		_, digest, err := NormalizePlannerAnswers(clarification.Questions, answer.Answers)
		if err == nil && digest == answer.AnswersSHA256 {
			return answer, true
		}
	}
	return PlannerClarificationAnswer{}, false
}

func plannerNextRound(snapshot ComplexPlanningSnapshot, plan ComplexEngineeringPlan) int {
	round := 1
	for _, item := range snapshot.Plans {
		if item.RequirementVersionID == plan.RequirementVersionID && item.Version <= plan.Version {
			round++
		}
	}
	return round
}

// PlannerAnswerCount counts valid answer continuations for one confirmed version.
func PlannerAnswerCount(snapshot ComplexPlanningSnapshot, versionID string) int {
	count := 0
	for _, plan := range snapshot.Plans {
		if plan.RequirementVersionID == versionID {
			if _, ok := PlannerAnswerForPlan(snapshot, plan); ok {
				count++
			}
		}
	}
	return count
}
