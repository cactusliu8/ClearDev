package cleardev

import (
	"strings"
	"testing"
)

func TestPlannerAnswersRequireCompleteBoundedContext(t *testing.T) {
	for _, answers := range [][]string{nil, {""}, {"one", "extra"}, {strings.Repeat("界", MaxComplexAnswerRunes+1)}} {
		if _, _, err := NormalizePlannerAnswers([]string{"question"}, answers); err == nil {
			t.Fatal("invalid Planner answer accepted")
		}
	}
	normalized, digest, err := NormalizePlannerAnswers([]string{"question"}, []string{"  newest first  "})
	if err != nil || normalized[0] != "newest first" || len(digest) != 64 {
		t.Fatal("normalized answer lost binding")
	}
	_, same, err := NormalizePlannerAnswers([]string{"question"}, normalized)
	if err != nil || same != digest {
		t.Fatal("normalized replay changed digest")
	}
}
