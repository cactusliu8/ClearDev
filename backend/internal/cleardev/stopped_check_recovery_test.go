package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStoppedCheckRecoveryHasBoundedNativeAuthority(t *testing.T) {
	spec, found := ProductionDecisionRegistry().Lookup("AUTHORIZE_STOPPED_CHECK_RECOVERY")
	if !found || spec.BindingSchemaVersion != 1 || !spec.Allowed[HumanDecisionApprove] || !spec.Allowed[HumanDecisionReject] || !spec.Allowed[HumanDecisionLater] {
		t.Fatal("a settled Planner STOP and unavailable checker have no exact native same-candidate recovery")
	}
}

func stoppedCheckTestBinding() StoppedCheckRecoveryBinding {
	return StoppedCheckRecoveryBinding{DevelopmentRequirementID: "req", ExecutionRunID: "run", EventID: "event", StopSHA256: strings.Repeat("a", 64), ContextSHA256: strings.Repeat("b", 64), TaskID: "task", DispatchID: "dispatch", CheckRunID: "failed-check", RetryCheckRunID: "failed-check:stopped-check-retry", CandidateSHA: strings.Repeat("c", 40), TaskPacketSHA256: strings.Repeat("d", 64), ReworkCount: 9, BuilderRoleBindingID: "builder", AOSessionID: "session", ProviderConversationID: "native", WorkspacePath: "/worktree", SessionCreationKey: "key", CreationFingerprint: "fingerprint", Harness: "opencode", Model: "provider/model"}
}

func TestStoppedCheckRecoveryStrictIdentityAndStableIntent(t *testing.T) {
	b := stoppedCheckTestBinding()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseStoppedCheckRecoveryBinding(raw); err != nil || got != b {
		t.Fatal(got, err)
	}
	for _, change := range []func(*StoppedCheckRecoveryBinding){func(b *StoppedCheckRecoveryBinding) { b.RetryCheckRunID = "arbitrary-retry" }, func(b *StoppedCheckRecoveryBinding) { b.CheckRunID = "" }, func(b *StoppedCheckRecoveryBinding) { b.ContextSHA256 = "changed" }, func(b *StoppedCheckRecoveryBinding) { b.ReworkCount = -1 }, func(b *StoppedCheckRecoveryBinding) { b.CandidateSHA = "not-a-candidate" }, func(b *StoppedCheckRecoveryBinding) { b.TaskPacketSHA256 = "" }} {
		bad := b
		change(&bad)
		raw, _ := json.Marshal(bad)
		if _, err := ParseStoppedCheckRecoveryBinding(raw); err == nil {
			t.Fatal("loose authority accepted", bad)
		}
	}
	unknown := string(raw[:len(raw)-1]) + `,"approve":true}`
	if _, err := ParseStoppedCheckRecoveryBinding([]byte(unknown)); err == nil {
		t.Fatal("caller-supplied approval accepted")
	}
	target, err := StoppedCheckRecoveryTarget(b)
	if err != nil {
		t.Fatal(err)
	}
	changed := b
	changed.CandidateSHA = strings.Repeat("e", 40)
	other, err := StoppedCheckRecoveryTarget(changed)
	if err != nil || target == other {
		t.Fatal("intent did not bind candidate", err)
	}
}

func TestStoppedCheckRecoveryNeverRewritesStopOrAuthorizesAnotherCandidate(t *testing.T) {
	raw := `{"decision":"STOP"}`
	stop := PlannerCoordinationDecision{EventID: "event", ExecutionRunID: "run", Source: "PLANNER", Outcome: PlannerRuntimeStop, ReasonCode: ReasonPlannerRuntimeStopped, ResultJSON: raw, ResultSHA256: sha256Hex([]byte(raw))}
	task := ComplexExecutionTask{ID: "task", CurrentDispatchID: "dispatch", CurrentRound: 8, ReworkCount: 9, Status: DevelopmentTaskStatusRunning, ExecutionPackageSHA256: strings.Repeat("d", 64)}
	dispatch := ComplexExecutionDispatch{ID: "dispatch", ComplexExecutionTaskID: "task", CandidateCommitSHA: strings.Repeat("c", 40), Round: 8, Status: ComplexExecutionDispatchObserved}
	grant := StoppedCheckRecovery{EventID: "event", DecisionRequestID: "native-decision", StopSHA256: stop.ResultSHA256, TaskID: "task", DispatchID: "dispatch", CandidateSHA: dispatch.CandidateCommitSHA, TaskPacketSHA256: task.ExecutionPackageSHA256, CheckRunID: "failed", RetryCheckRunID: "retry", ReworkCount: 9}
	snapshot := ComplexExecutionSnapshot{Tasks: []ComplexExecutionTask{task}, Dispatches: []ComplexExecutionDispatch{dispatch}, PlannerRuntime: &PlannerRuntimeSnapshot{Decisions: []PlannerCoordinationDecision{stop}, CheckRecoveries: []StoppedCheckRecovery{grant}}}
	if !StoppedCheckRecoveryResolvesStop(snapshot, stop) || !StoppedCheckRecoveryRetainsReworkCount(snapshot, task) {
		t.Fatal("exact grant not recognized")
	}
	if got, found := PlannerRuntimeEffectiveDecision(snapshot.PlannerRuntime, "event"); !found || got.Outcome != "STOP" {
		t.Fatal("history rewritten as CONTINUE")
	}
	for _, mutate := range []func(*ComplexExecutionSnapshot){
		func(s *ComplexExecutionSnapshot) { s.Tasks[0].ReworkCount = 10 },
		func(s *ComplexExecutionSnapshot) { s.Tasks[0].CurrentDispatchID = "new-dispatch" },
		func(s *ComplexExecutionSnapshot) { s.Tasks[0].Status = DevelopmentTaskStatusBlocked },
		func(s *ComplexExecutionSnapshot) { s.Tasks[0].ExecutionPackageSHA256 = strings.Repeat("e", 64) },
		func(s *ComplexExecutionSnapshot) { s.Dispatches[0].CandidateCommitSHA = strings.Repeat("f", 40) },
		func(s *ComplexExecutionSnapshot) { s.Dispatches[0].Status = ComplexExecutionDispatchRework },
	} {
		changed := snapshot
		changed.Tasks = append([]ComplexExecutionTask(nil), snapshot.Tasks...)
		changed.Dispatches = append([]ComplexExecutionDispatch(nil), snapshot.Dispatches...)
		mutate(&changed)
		if StoppedCheckRecoveryResolvesStop(changed, stop) {
			t.Fatal("grant widened beyond original candidate/check stage")
		}
	}
	forged := stop
	forged.Source = "CONTROL_PLANE"
	if StoppedCheckRecoveryResolvesStop(snapshot, forged) {
		t.Fatal("source kind substituted")
	}
}

func TestStoppedCheckRecoveryFinalPromptExplainsAuthorityWithoutRewritingHistory(t *testing.T) {
	packet := RequirementFinalReviewPacket{PlannerRuntime: &PlannerRuntimeSnapshot{}}
	raw, _ := json.Marshal(packet)
	old := RequirementFinalReviewPrompt(RequirementFinalReview{ReviewPacketJSON: string(raw)})
	if strings.Contains(old, "Same-candidate Check Recovery") {
		t.Fatal("historical prompt changed")
	}
	packet.PlannerRuntime.CheckRecoveries = []StoppedCheckRecovery{{EventID: "kept-stop", DecisionRequestID: "native-grant", CheckRunID: "failed-check", RetryCheckRunID: "unique-retry"}}
	raw, _ = json.Marshal(packet)
	prompt := RequirementFinalReviewPrompt(RequirementFinalReview{ReviewPacketJSON: string(raw)})
	for _, needle := range []string{"Same-candidate Check Recovery", "不是 Planner CONTINUE", "仍是真实历史", "不得增加消息/任务预算", "unique-retry"} {
		if !strings.Contains(prompt, needle) {
			t.Fatal("missing safe recovery explanation", needle)
		}
	}
}
