package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestComplexExecutionRequestProtocolIsStrictAndBound(t *testing.T) {
	valid := complexExecutionRequestJSON(t, nil)
	result, err := ParseComplexExecutionRequestResult(valid, "request-1", "v2", "plan-1", strings.Repeat("a", 64))
	if err != nil || result.Decision != "DISPATCH" {
		t.Fatalf("valid dispatch = %#v, %v", result, err)
	}
	needsHuman := complexExecutionRequestJSON(t, map[string]any{
		"decision": "NEEDS_HUMAN", "reasonCode": "HUMAN_DECISION_REQUIRED",
	})
	if _, err := ParseComplexExecutionRequestResult(needsHuman, "request-1", "v2", "plan-1", strings.Repeat("a", 64)); err != nil {
		t.Fatalf("valid NEEDS_HUMAN rejected: %v", err)
	}
	for name, raw := range map[string][]byte{
		"unknown":      complexExecutionRequestJSON(t, map[string]any{"extra": true}),
		"null":         []byte(`{"schemaVersion":1,"kind":"COMPLEX_EXECUTION_REQUEST","decision":"DISPATCH","reasonCode":"PLAN_READY","summary":null}`),
		"duplicate":    []byte(`{"schemaVersion":1,"schemaVersion":1,"kind":"COMPLEX_EXECUTION_REQUEST","decision":"DISPATCH","reasonCode":"PLAN_READY","summary":"ready"}`),
		"wrong-plan":   complexExecutionRequestJSON(t, map[string]any{"planId": "other"}),
		"wrong-reason": complexExecutionRequestJSON(t, map[string]any{"reasonCode": "HUMAN_DECISION_REQUIRED"}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseComplexExecutionRequestResult(raw, "request-1", "v2", "plan-1", strings.Repeat("a", 64)); err == nil {
				t.Fatal("invalid execution request was accepted")
			}
		})
	}
}

func TestComplexExecutionRunPackageIsBoundBeforeMaterialization(t *testing.T) {
	raw, digest, err := BuildComplexExecutionRunPackage(WorkModeStandard, "run-1", "v2", strings.Repeat("a", 64), "plan-1", strings.Repeat("b", 64))
	if err != nil || digest != sha256Hex(raw) || !strings.Contains(string(raw), `"executionRunId":"run-1"`) {
		t.Fatalf("run package = %s digest=%q err=%v", raw, digest, err)
	}
	if _, _, err := BuildComplexExecutionRunPackage(WorkModeStandard, "", "v2", strings.Repeat("a", 64), "plan-1", strings.Repeat("b", 64)); err == nil {
		t.Fatal("invalid run package was accepted")
	}
}

func TestComplexStandardExecutionPackageBindsFixedFacts(t *testing.T) {
	task := ComplexPlanTask{
		Key: "normalize", Title: "规范化", Objective: "实现邮箱规范化。",
		RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"},
		WritePaths: []string{"src/email.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{},
		ForbiddenPaths: []string{".git/**", "package.json"}, RequiredCheckIDs: []string{"email-unit"}, DependencyKeys: []string{},
	}
	pkg, raw, digest, err := BuildComplexStandardExecutionPackage(WorkModeStandard, ComplexStandardExecutionPackageInput{
		ExecutionRunID: "run-1", RequirementVersionID: "v2", RequirementSHA256: strings.Repeat("b", 64),
		RequirementText: "实现邮件名单。", PlanID: "plan-1", PlanSHA256: strings.Repeat("c", 64),
		TaskSetVersion: 1, TaskID: "task-1", Task: task,
	})
	if err != nil || digest == "" || len(raw) == 0 || pkg.RequiredChecks[0].ID != "email-unit" {
		t.Fatalf("build package = %#v raw=%s digest=%q err=%v", pkg, raw, digest, err)
	}
	parsed, err := ParseComplexStandardExecutionPackage(raw)
	if err != nil || parsed.TaskID != "task-1" {
		t.Fatalf("parse built package = %#v, %v", parsed, err)
	}
	pkg.RequiredChecks[0].Argv = []string{"sh", "-c", "echo owned"}
	changed, marshalErr := json.Marshal(pkg)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := ParseComplexStandardExecutionPackage(changed); err == nil {
		t.Fatal("agent-provided check argv was accepted")
	}
	shared, _, _, err := BuildComplexStandardExecutionPackage(WorkModeStandard, ComplexStandardExecutionPackageInput{
		ExecutionRunID: "run-1", RequirementVersionID: "v2", RequirementSHA256: strings.Repeat("b", 64),
		RequirementText: "实现邮件名单。", PlanID: "plan-1", PlanSHA256: strings.Repeat("c", 64),
		TaskSetVersion: 1, TaskID: "task-1", Task: ComplexPlanTask{
			Key: "shared", Title: "共享", Objective: "修改共享清单。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"},
			WritePaths: []string{"src/email.js"}, GeneratedPaths: []string{"package-lock.json"}, SharedPathsRequireApproval: []string{"package.json"},
			ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"email-unit", "all-tests"}, DependencyKeys: []string{},
		},
	})
	if err != nil || shared.TaskKey != "shared" || len(shared.SharedPathsRequireApproval) != 1 || len(shared.GeneratedPaths) != 1 {
		t.Fatalf("listed shared and frozen generated package = %#v err=%v", shared, err)
	}
	if _, _, _, err := BuildComplexStandardExecutionPackage(WorkModeStandard, ComplexStandardExecutionPackageInput{
		ExecutionRunID: "run-1", RequirementVersionID: "v2", RequirementSHA256: strings.Repeat("b", 64),
		RequirementText: "实现邮件名单。", PlanID: "plan-1", PlanSHA256: strings.Repeat("c", 64),
		TaskSetVersion: 1, TaskID: "task-1", Task: ComplexPlanTask{
			Key: "generated", Title: "生成", Objective: "不应派发。", RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"},
			WritePaths: []string{"src/email.js"}, GeneratedPaths: []string{"src/generated.js"}, SharedPathsRequireApproval: []string{},
			ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"email-unit"}, DependencyKeys: []string{},
		},
	}); err == nil {
		t.Fatal("unfrozen generated-path package was accepted")
	}
}

func TestComplexStandardEligibleTaskRequiresVerifiedDependenciesAndOneActiveTask(t *testing.T) {
	run := ComplexExecutionRun{ID: "run-1", Decision: ComplexExecutionDecisionDispatch, BuilderAOSessionID: "builder", InitialBaseCommitSHA: strings.Repeat("d", 40)}
	first := ComplexExecutionTask{ID: "one", ExecutionRunID: run.ID, TaskKey: "one", DevelopmentTaskID: "task-1", Ordinal: 0, Status: DevelopmentTaskStatusReview, CurrentRound: 0, CurrentDispatchID: "dispatch-1"}
	second := ComplexExecutionTask{ID: "two", ExecutionRunID: run.ID, TaskKey: "two", DevelopmentTaskID: "task-2", Ordinal: 1, DependencyTaskKeys: []string{"one"}, Status: DevelopmentTaskStatusPlanned}
	snapshot := ComplexExecutionSnapshot{Run: run, Tasks: []ComplexExecutionTask{first, second}}
	if _, ok := NextEligibleComplexStandardTask(snapshot); ok {
		t.Fatal("dependent task dispatched without verified predecessor")
	}
	snapshot.Verifications = []ComplexExecutionVerification{{
		ID: "verify-1", ExecutionRunID: run.ID, ComplexExecutionTaskID: first.ID, DispatchID: "dispatch-1", CandidateCommitID: "candidate-1",
		CandidateCommitSHA: strings.Repeat("e", 40), Round: 0, ScopeEvidenceID: "scope-1", RequiredCheckRunIDs: []string{"check-1"}, LocalReviewID: "review-1",
	}}
	next, ok := NextEligibleComplexStandardTask(snapshot)
	if !ok || next.TaskKey != "two" {
		t.Fatalf("next task = %#v ok=%v", next, ok)
	}
	snapshot.Tasks[1].Status = DevelopmentTaskStatusRunning
	if _, ok := NextEligibleComplexStandardTask(snapshot); ok {
		t.Fatal("a second task dispatched while one is running")
	}
}

func TestDeriveComplexExecutionPhaseDoesNotMarkPredecessorDone(t *testing.T) {
	run := ComplexExecutionRun{ID: "run-1", Decision: ComplexExecutionDecisionDispatch, BuilderAOSessionID: "builder", InitialBaseCommitSHA: strings.Repeat("a", 40)}
	first := ComplexExecutionTask{ID: "one", ExecutionRunID: run.ID, TaskKey: "one", DevelopmentTaskID: "task-1", Ordinal: 0, Status: DevelopmentTaskStatusReview, CurrentRound: 0, CurrentDispatchID: "dispatch-1"}
	second := ComplexExecutionTask{ID: "two", ExecutionRunID: run.ID, TaskKey: "two", DevelopmentTaskID: "task-2", Ordinal: 1, DependencyTaskKeys: []string{"one"}, Status: DevelopmentTaskStatusPlanned}
	snapshot := ComplexExecutionSnapshot{Run: run, Tasks: []ComplexExecutionTask{first, second}, Verifications: []ComplexExecutionVerification{{
		ExecutionRunID: run.ID, ComplexExecutionTaskID: first.ID, DispatchID: "dispatch-1", CandidateCommitID: "candidate-1", CandidateCommitSHA: strings.Repeat("b", 40),
		Round: 0, ScopeEvidenceID: "scope-1", RequiredCheckRunIDs: []string{"required-1"}, LocalReviewID: "review-1",
	}}}
	phase, reason := DeriveComplexExecutionPhase(snapshot)
	if phase != ComplexExecutionReadyToDispatch || reason != ReasonNone {
		t.Fatalf("verified predecessor phase = %s reason=%s", phase, reason)
	}
	snapshot.Tasks[1].Status = DevelopmentTaskStatusReview
	snapshot.Tasks[1].CurrentRound, snapshot.Tasks[1].CurrentDispatchID = 0, "dispatch-2"
	snapshot.Verifications = append(snapshot.Verifications, ComplexExecutionVerification{
		ExecutionRunID: run.ID, ComplexExecutionTaskID: second.ID, DispatchID: "dispatch-2", CandidateCommitID: "candidate-2", CandidateCommitSHA: strings.Repeat("c", 40),
		Round: 0, ScopeEvidenceID: "scope-2", RequiredCheckRunIDs: []string{"required-2"}, LocalReviewID: "review-2",
	})
	phase, _ = DeriveComplexExecutionPhase(snapshot)
	if phase != ComplexExecutionIntegrating {
		t.Fatalf("all verified phase = %s", phase)
	}
	now := time.Now().UTC()
	snapshot.Integration = &ComplexExecutionIntegration{ID: "integration-1", ExecutionRunID: run.ID, CompletedAt: now}
	snapshot.Run.CompletedAt = &now
	phase, _ = DeriveComplexExecutionPhase(snapshot)
	if phase != ComplexExecutionCompleted {
		t.Fatalf("final atomic completion phase = %s", phase)
	}
}

func TestComplexExecutionMissingEvidenceIsDerivedFromCurrentFacts(t *testing.T) {
	run := ComplexExecutionRun{ID: "run-1"}
	snapshot := ComplexExecutionSnapshot{Run: run}
	if got := DeriveComplexExecutionMissingEvidence(snapshot); len(got) != 1 || got[0] != "STEWARD_DECISION" {
		t.Fatalf("awaiting Steward missing evidence = %#v", got)
	}
	run.Decision = ComplexExecutionDecisionDispatch
	run.BuilderAOSessionID = "builder"
	run.InitialBaseCommitSHA = strings.Repeat("a", 40)
	task := ComplexExecutionTask{ID: "task", ExecutionRunID: run.ID, TaskKey: "one", Status: DevelopmentTaskStatusRunning, CurrentDispatchID: "dispatch"}
	dispatch := ComplexExecutionDispatch{ID: "dispatch", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CandidateCommitID: "candidate", CandidateCommitSHA: strings.Repeat("b", 40)}
	snapshot = ComplexExecutionSnapshot{Run: run, Tasks: []ComplexExecutionTask{task}, Dispatches: []ComplexExecutionDispatch{dispatch}}
	if got := DeriveComplexExecutionMissingEvidence(snapshot); strings.Join(got, ",") != "SCOPE_CHECK,REQUIRED_CHECKS,REVIEWER_PASS" {
		t.Fatalf("candidate missing evidence = %#v", got)
	}
	now := time.Now().UTC()
	snapshot.Integration = &ComplexExecutionIntegration{ID: "integration", ExecutionRunID: run.ID, CompletedAt: now}
	snapshot.Run.CompletedAt = &now
	if got := DeriveComplexExecutionMissingEvidence(snapshot); got == nil || len(got) != 0 {
		t.Fatalf("completed missing evidence = %#v, want non-nil empty", got)
	}
}

func TestComplexExecutionFailureFactsDeriveBlocked(t *testing.T) {
	run := ComplexExecutionRun{ID: "run-1"}
	snapshot := ComplexExecutionSnapshot{Run: run, RoleBindings: []ComplexExecutionRoleBinding{{ID: "builder", Role: StandardRoleBuilder, Status: RoleBindingStatusFailed, ReasonCode: ReasonBuilderSpawnFailed}}}
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBlocked || reason != ReasonBuilderSpawnFailed {
		t.Fatalf("failed Builder phase = %s/%s", phase, reason)
	}
	snapshot.RoleBindings = nil
	snapshot.AgentSteps = []AgentStep{{ID: "steward-step", Kind: AgentStepDispatchRequest, SendStatus: AgentStepSendStatusFailed, ReasonCode: ReasonCode("STEWARD_RESULT_INVALID")}}
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBlocked || reason != ReasonCode("STEWARD_RESULT_INVALID") {
		t.Fatalf("failed Steward step phase = %s/%s", phase, reason)
	}
	task := ComplexExecutionTask{ID: "task", ExecutionRunID: run.ID, Status: DevelopmentTaskStatusBlocked, CurrentRound: 0, CurrentDispatchID: "dispatch"}
	dispatch := ComplexExecutionDispatch{ID: "dispatch", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, Round: 0, ReasonCode: ReasonCode("REVIEWER_UNAVAILABLE")}
	run.Decision, run.BuilderAOSessionID, run.InitialBaseCommitSHA = ComplexExecutionDecisionDispatch, "builder", strings.Repeat("a", 40)
	snapshot = ComplexExecutionSnapshot{Run: run, Tasks: []ComplexExecutionTask{task}, Dispatches: []ComplexExecutionDispatch{dispatch}}
	if phase, reason := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionBlocked || reason != ReasonCode("REVIEWER_UNAVAILABLE") {
		t.Fatalf("failed dispatch phase = %s/%s", phase, reason)
	}
}

func TestComplexExecutionMissingEvidenceOmitsPassedIntegrationChecks(t *testing.T) {
	run := ComplexExecutionRun{ID: "run-1", Decision: ComplexExecutionDecisionDispatch, BuilderAOSessionID: "builder", InitialBaseCommitSHA: strings.Repeat("a", 40)}
	task := ComplexExecutionTask{ID: "task", ExecutionRunID: run.ID, TaskKey: "task", Ordinal: 0, Status: DevelopmentTaskStatusReview, CurrentRound: 0, CurrentDispatchID: "dispatch"}
	dispatch := ComplexExecutionDispatch{ID: "dispatch", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, CandidateCommitID: "candidate", CandidateCommitSHA: strings.Repeat("b", 40)}
	snapshot := ComplexExecutionSnapshot{
		Run: run, Tasks: []ComplexExecutionTask{task}, Dispatches: []ComplexExecutionDispatch{dispatch},
		Verifications: []ComplexExecutionVerification{{ID: "verification", ExecutionRunID: run.ID, ComplexExecutionTaskID: task.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, CandidateCommitSHA: dispatch.CandidateCommitSHA, Round: 0, ScopeEvidenceID: "scope", RequiredCheckRunIDs: []string{"required"}, LocalReviewID: "review"}},
		CheckSpecs:    []ComplexExecutionCheckSpecFact{{ID: "integration-spec", ExecutionRunID: run.ID, Kind: CandidateCheckIntegration, CheckID: "all-tests"}},
		CheckRuns:     []ComplexExecutionCheckRun{{ID: "integration-run", ExecutionRunID: run.ID, DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID, CheckSpecFactID: "integration-spec", Kind: CandidateCheckIntegration, Status: ComplexExecutionCheckRunSettled, Result: EvidenceResultPass}},
	}
	if phase, _ := DeriveComplexExecutionPhase(snapshot); phase != ComplexExecutionIntegrating {
		t.Fatalf("integration phase = %s", phase)
	}
	if got := DeriveComplexExecutionMissingEvidence(snapshot); strings.Join(got, ",") != "INTEGRATION_RESULT" {
		t.Fatalf("passed integration checks missing evidence = %#v", got)
	}
}

func complexExecutionRequestJSON(t *testing.T, overlay map[string]any) []byte {
	t.Helper()
	value := map[string]any{
		"schemaVersion": 1, "kind": "COMPLEX_EXECUTION_REQUEST", "decision": "DISPATCH", "reasonCode": "PLAN_READY", "summary": "计划已审核，可派发。",
	}
	for key, item := range overlay {
		value[key] = item
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
