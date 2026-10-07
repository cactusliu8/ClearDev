package cleardev

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func savedCLITrialFixture(t *testing.T) (ComplexExecutionRun, RequirementFinalReview) {
	t.Helper()
	run, review := finalReviewCoreFixture(t)
	var packet RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	packet.FunctionalTrial = true
	packet.TrialCLIExecutable = "/opt/ao/current ao"
	raw, _ := json.Marshal(packet)
	review.ReviewPacketJSON, review.ReviewPacketSHA256 = string(raw), sha256Hex(raw)
	return run, review
}

func TestStageTrialPromptPreservesSavedCLIPromptBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("frozen POSIX executable quoting fixture")
	}
	_, review := savedCLITrialFixture(t)
	// Captured from the exact 4443fd113 production template before this repair.
	const savedPromptSHA = "4f5d457a882c37831206666b68e7546cfd02bffe135dc032e29cec69183f87e8"
	if got := sha256Hex([]byte(RequirementFinalReviewPrompt(review))); got != savedPromptSHA {
		t.Fatalf("saved CLI trial prompt changed: %s", got)
	}
}

func TestStageTrialHostPromptScopesRequiredToolAccess(t *testing.T) {
	_, review := savedCLITrialFixture(t)
	review.ReviewPacketJSON = strings.TrimSuffix(review.ReviewPacketJSON, "}") + `,"trialHostExecution":true}`
	prompt := RequirementFinalReviewPrompt(review)
	for _, required := range []string{"sandbox_permissions", "require_escalated", "仅限本审核的精确 stage-trial/browser 命令", "不得执行 ao start", "不得读取或输出会话能力", "宿主执行不可用", "不需要额外试用报告", "不得修改候选源码"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("host trial contract missing %q", required)
		}
	}
}

func TestStageTrialHostExecutionRequiresFrozenTrialCLI(t *testing.T) {
	for _, missing := range []string{"trial", "executable"} {
		t.Run(missing, func(t *testing.T) {
			run, review := savedCLITrialFixture(t)
			var packet RequirementFinalReviewPacket
			_ = json.Unmarshal([]byte(review.ReviewPacketJSON), &packet)
			if missing == "trial" {
				packet.FunctionalTrial = false
			} else {
				packet.TrialCLIExecutable = ""
			}
			raw, _ := json.Marshal(packet)
			review.ReviewPacketJSON = strings.TrimSuffix(string(raw), "}") + `,"trialHostExecution":true}`
			review.ReviewPacketSHA256 = sha256Hex([]byte(review.ReviewPacketJSON))
			err := ValidateRequirementFinalReviewBinding(review, run, review.CandidateCommitSHA, review.CheckRunIDs)
			if err == nil || !strings.Contains(err.Error(), "host trial execution requires") {
				t.Fatalf("wrong host trial binding error: %v", err)
			}
		})
	}
}
