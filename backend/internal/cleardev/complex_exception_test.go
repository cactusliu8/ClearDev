package cleardev

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskRequiresSpecialistForPackageJSON(t *testing.T) {
	task := ComplexPlanTask{
		Key: "normalize-and-deduplicate", WritePaths: []string{"src/email.js"},
		SharedPathsRequireApproval: []string{"package.json"}, GeneratedPaths: []string{"package-lock.json"},
		ForbiddenPaths: []string{".git/**"},
	}
	if !TaskRequiresSpecialist(task) {
		t.Fatal("package.json task did not require Specialist")
	}
	migration := ComplexPlanTask{Key: "schema", WritePaths: []string{"migrations/001_init.sql"}, ForbiddenPaths: []string{".git/**"}}
	if !TaskRequiresSpecialist(migration) {
		t.Fatal("SQLite migration task did not require Specialist")
	}
	needed := FrozenSpecialistChecksForTask(migration)
	if len(needed) != 1 || needed[0].ID != "sqlite-migration-specialist" {
		t.Fatalf("migration specialist checks = %#v", needed)
	}
	if exceptionPathIsRepoControl("package.json") {
		t.Fatal("package.json was treated as repository control")
	}
	if !exceptionPathIsRepoControl(".git/config") {
		t.Fatal(".git/config was not treated as repository control")
	}
}

func TestFrozenGeneratedCommandForPackageLock(t *testing.T) {
	command, ok := FrozenGeneratedCommandForPaths([]string{"package-lock.json"})
	if !ok || command.ID != "npm-package-lock" || command.Image != StandardRequiredImage {
		t.Fatalf("frozen generator = %#v ok=%v", command, ok)
	}
	if _, ok := FrozenGeneratedCommandForPaths([]string{"src/generated.js"}); ok {
		t.Fatal("unfrozen generated path was accepted")
	}
}

func TestFreezeComplexExceptionBudgetsDoesNotTransferTurns(t *testing.T) {
	at := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	tasks := []ComplexExecutionTask{{ID: "task-1", TaskKey: "normalize-and-deduplicate"}}
	plan := []ComplexPlanTask{{
		Key: "normalize-and-deduplicate", WritePaths: []string{"src/email.js"},
		SharedPathsRequireApproval: []string{"package.json"}, GeneratedPaths: []string{"package-lock.json"},
	}}
	budgets := FreezeComplexExceptionBudgets("run-1", tasks, plan, ComplexStandardMaxReworkCount, at)
	byKind := map[string]ComplexExceptionBudget{}
	for _, budget := range budgets {
		byKind[budget.RoleKind+"|"+budget.ComplexExecutionTaskID] = budget
		if budget.UsedTurns != 0 || budget.ModelSelection != ComplexExceptionModelSelection {
			t.Fatalf("budget = %#v", budget)
		}
	}
	// One turn is one round: the historical single rework round keeps its
	// frozen three and two turns as the floor.
	if byKind["BUILDER|task-1"].MaxTurns != ComplexExceptionBuilderMaxTurns ||
		byKind["REVIEWER|task-1"].MaxTurns != ComplexExceptionReviewerMaxTurns ||
		byKind["STEWARD_EXCEPTION|task-1"].MaxTurns != ComplexExceptionStewardExceptionMaxTurns ||
		byKind["SPECIALIST|task-1"].MaxTurns != ComplexExceptionSpecialistMaxTurns ||
		byKind["RECOVERY|"].MaxTurns != ComplexExceptionRecoveryMaxTurns {
		t.Fatalf("budget maxima = %#v", byKind)
	}
}

func TestApprovedSharedPathsRejectsUnlistedAndForbidden(t *testing.T) {
	task := ComplexPlanTask{
		WritePaths: []string{"src/email.js"}, SharedPathsRequireApproval: []string{"package.json"},
		GeneratedPaths: []string{"package-lock.json"}, ForbiddenPaths: []string{".git/**"},
	}
	got, err := ApprovedSharedPaths(task, []string{"package.json"})
	if err != nil || strings.Join(got, ",") != "package.json" {
		t.Fatalf("approved = %v err=%v", got, err)
	}
	if _, err := ApprovedSharedPaths(task, []string{"src/secret.js"}); err == nil {
		t.Fatal("unlisted path was approved")
	}
	if _, err := ApprovedSharedPaths(task, []string{".git/config"}); err == nil {
		t.Fatal("forbidden path was approved")
	}
}

func TestPermissionRulesAfterScopeApprovalMovesSharedToWrite(t *testing.T) {
	current := PathRules{
		WritePaths: []string{"src/email.js"}, GeneratedPaths: []string{"package-lock.json"},
		SharedPathsRequireApproval: []string{"package.json"}, ForbiddenPaths: []string{".git/**"},
	}
	next := PermissionRulesAfterScopeApproval(current, []string{"package.json"})
	if strings.Join(next.WritePaths, ",") != "src/email.js,package.json" || len(next.SharedPathsRequireApproval) != 0 {
		t.Fatalf("permission after approval = %#v", next)
	}
}

func TestParseComplexScopeExpansionRequestRejectsUnknownAndEchoedBindings(t *testing.T) {
	payload := map[string]any{
		"schemaVersion": 1, "kind": ComplexScopeExpansionRequestKind,
		"requestedPaths": []string{"package.json"}, "summary": "需要写入 package.json。",
	}
	raw, _ := json.Marshal(payload)
	got, err := ParseComplexScopeExpansionRequest(raw, "d1", "t1", 0, "v2", strings.Repeat("a", 64), "p1", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("bound plan hash = %s", got.PlanSHA256)
	}
	payload["planSha256"] = strings.Repeat("c", 64)
	stale, _ := json.Marshal(payload)
	if _, err := ParseComplexScopeExpansionRequest(stale, "d1", "t1", 0, "v2", strings.Repeat("a", 64), "p1", strings.Repeat("b", 64)); err == nil {
		t.Fatal("echoed stale plan hash was accepted")
	}
	payload["planSha256"] = strings.Repeat("b", 64)
	payload["extra"] = true
	unknown, _ := json.Marshal(payload)
	if _, err := ParseComplexScopeExpansionRequest(unknown, "d1", "t1", 0, "v2", strings.Repeat("a", 64), "p1", strings.Repeat("b", 64)); err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func TestParseComplexSpecialistAndRecoveryResults(t *testing.T) {
	specialist, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "kind": ComplexSpecialistResultKind, "outcome": "PASS",
		"constraints": []string{"不新增依赖。"}, "reasonCode": "SPECIALIST_PASS", "summary": "可以改 package.json。",
	})
	if _, err := ParseComplexSpecialistResult(specialist, "run", "task", "v2", strings.Repeat("a", 64), "p1", strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	recovery, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "kind": ComplexRecoveryResultKind,
		"action": ComplexRecoveryActionRetryInfraCheck, "outcome": "PASS",
		"reasonCode": "RECOVERY_SAFE", "summary": "重试已结算的基础设施检查。",
	})
	if _, err := ParseComplexRecoveryResult(recovery, "run", "CHECKER_UNAVAILABLE", "check-1"); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveComplexExecutionMissingEvidenceIncludesGeneratedProof(t *testing.T) {
	dispatch := ComplexExecutionDispatch{
		ID: "d1", ComplexExecutionTaskID: "task-1", CandidateCommitID: "c1", Status: ComplexExecutionDispatchObserved,
	}
	snapshot := ComplexExecutionSnapshot{
		Run: ComplexExecutionRun{ID: "run", Decision: ComplexExecutionDecisionDispatch, BuilderAOSessionID: "builder", InitialBaseCommitSHA: strings.Repeat("a", 40)},
		Tasks: []ComplexExecutionTask{{
			ID: "task-1", Status: DevelopmentTaskStatusRunning, CurrentDispatchID: dispatch.ID, CurrentRound: 0,
		}},
		Dispatches: []ComplexExecutionDispatch{dispatch},
		CheckSpecs: []ComplexExecutionCheckSpecFact{{ID: "scope", ComplexExecutionTaskID: "task-1", Kind: CandidateCheckScope}},
		CheckRuns: []ComplexExecutionCheckRun{{
			CheckSpecFactID: "scope", DispatchID: dispatch.ID, CandidateCommitID: dispatch.CandidateCommitID,
			Status: ComplexExecutionCheckRunSettled, Result: EvidenceResultPass,
		}},
		Exception: &ComplexExceptionFacts{
			GeneratedCommands: []ComplexGeneratedCommandFact{{ID: "cmd", ComplexExecutionTaskID: "task-1"}},
		},
	}
	missing := DeriveComplexExecutionMissingEvidence(snapshot)
	found := false
	for _, item := range missing {
		if item == "GENERATED_PROOF" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing evidence = %v, want GENERATED_PROOF", missing)
	}
}

func TestComplexTaskDispatchPathsDisjointTreatsSharedAsExclusive(t *testing.T) {
	left := ComplexPlanTask{WritePaths: []string{"src/email.js"}, SharedPathsRequireApproval: []string{"package.json"}}
	right := ComplexPlanTask{WritePaths: []string{"src/deduplicate.js"}, SharedPathsRequireApproval: []string{"package.json"}}
	if ComplexTaskDispatchPathsDisjoint(left, right) {
		t.Fatal("shared package.json was treated as concurrent")
	}
	other := ComplexPlanTask{WritePaths: []string{"src/summary.js"}}
	if !ComplexTaskDispatchPathsDisjoint(left, other) {
		t.Fatal("disjoint write and one-sided shared path were treated as conflict")
	}
}
