package cleardev

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStageTrialPromptPreservesHistoricalReviewContract(t *testing.T) {
	run, review := finalReviewCoreFixture(t)
	legacy := RequirementFinalReviewPrompt(review)
	if !strings.Contains(legacy, "启动服务或执行新的检查命令") || strings.Contains(legacy, "stage-trial") {
		t.Fatal("historical prompt silently changed")
	}
	var packet RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	packet.FunctionalTrial = true
	raw, _ := json.Marshal(packet)
	review.ReviewPacketJSON, review.ReviewPacketSHA256 = string(raw), sha256Hex(raw)
	if err := ValidateRequirementFinalReviewBinding(review, run, review.CandidateCommitSHA, review.CheckRunIDs); err == nil {
		t.Fatal("mail/historical runtime gained project trial authority")
	}
	prompt := RequirementFinalReviewPrompt(review)
	for _, required := range []string{"亲自操作一遍", "ao cleardev stage-trial start", "ao browser snapshot --interactive", "代码/日志审核", "不需要额外试用报告", "无法运行或无法操作返回 BLOCKED/NEEDS_HUMAN"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("trial contract missing %q", required)
		}
	}
	if strings.Contains(prompt, "启动服务或执行新的检查命令") {
		t.Fatal("new trial still forbidden")
	}
}

func TestStageTrialPromptUsesFrozenCLIForEveryAOOperation(t *testing.T) {
	_, review := finalReviewCoreFixture(t)
	var packet RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	packet.FunctionalTrial = true
	packet.TrialCLIExecutable = filepath.Join(t.TempDir(), "current ao")
	raw, _ := json.Marshal(packet)
	review.ReviewPacketJSON = string(raw)
	prompt := RequirementFinalReviewPrompt(review)
	executable := quoteTrialExecutable(packet.TrialCLIExecutable)
	for _, operation := range []string{"cleardev stage-trial start", "cleardev stage-trial stop", "browser open", "browser snapshot --interactive"} {
		if !strings.Contains(prompt, executable+" "+operation) {
			t.Fatalf("operation %q lost the frozen executable", operation)
		}
	}
}

func TestStageTrialPromptPreservesSavedTrialBytes(t *testing.T) {
	_, review := finalReviewCoreFixture(t)
	var packet RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		t.Fatal(err)
	}
	packet.FunctionalTrial = true
	raw, _ := json.Marshal(packet)
	review.ReviewPacketJSON = string(raw)
	// Verified against the exact 527fb31 template before the CLI pin was added.
	const savedPromptSHA = "b7baba293aa776a99df9dfaf716f061f6628512fb098dd483fff7c5046c853d0"
	if got := sha256Hex([]byte(RequirementFinalReviewPrompt(review))); got != savedPromptSHA {
		t.Fatalf("saved trial prompt changed: %s", got)
	}
}

func TestStageTrialExecutableQuotesLiteralShellCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting boundary")
	}
	// The executable name contains characters interpreted by an unquoted shell.
	path := filepath.Join(t.TempDir(), "ao ' $(exit 29) `exit 30`")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("sh", "-c", quoteTrialExecutable(path)+" cleardev stage-trial start requirement").CombinedOutput()
	if err != nil || string(output) != "cleardev\nstage-trial\nstart\nrequirement\n" {
		t.Fatalf("literal command = %q, err %v", output, err)
	}
}
