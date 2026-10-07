package cleardev

import (
	"strings"
	"testing"
	"time"
)

func TestDeriveTrustedProgressSameFactsSameHash(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	confirmed := RequirementVersion{ID: "rv2", DevelopmentRequirementID: "req-1", Version: 2, Status: RequirementVersionStatusConfirmed, TaskSetVersion: 1}
	facts := TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Demo"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks: []DevelopmentTask{
				{ID: "task-1", RequirementVersionID: confirmed.ID, Title: "Build", Status: DevelopmentTaskStatusRunning},
			},
			Events: []RequirementEvent{{Sequence: 4, Action: ActionStartDevelopmentTask, SubjectType: SubjectDevelopmentTask, SubjectID: "task-1", Outcome: EventAccepted}},
		},
		HasStewardSession: true,
		Now:               now,
	}
	first := DeriveTrustedProgress(facts)
	second := DeriveTrustedProgress(facts)
	if first.FactSummarySHA256 == "" || first.FactSummarySHA256 != second.FactSummarySHA256 {
		t.Fatalf("hash mismatch: %s vs %s", first.FactSummarySHA256, second.FactSummarySHA256)
	}
	if first.Phase != TrustedPhaseDeveloping || first.NextOwner.Role != TrustedOwnerBuilder || first.NextOwner.Action != TrustedActionImplementTask {
		t.Fatalf("developing summary = %+v", first)
	}
	if first.LatestFactSequence != 4 || len(first.CurrentWork) != 1 {
		t.Fatalf("current work/seq = %+v", first)
	}
}

func TestDeriveTrustedProgressPendingConfirmationNeedsHuman(t *testing.T) {
	facts := TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Wait"},
			RequirementVersions: []RequirementVersion{{ID: "rv1", Version: 1, Status: RequirementVersionStatusPendingConfirmation}},
			Events:              []RequirementEvent{{Sequence: 2, Outcome: EventAccepted, Action: ActionSubmitRequirementConfirmation, SubjectID: "rv1"}},
		},
		PendingDecisions: []HumanDecisionRequest{{ID: "hd-1", Status: HumanDecisionRequestPending, DecisionKind: HumanDecisionKindConfirmVersion}},
		Now:              time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
	}
	got := DeriveTrustedProgress(facts)
	if got.Phase != TrustedPhaseAwaitingConfirmation || got.Attention != OverallAttentionNeedsHuman {
		t.Fatalf("phase/attention = %s %s", got.Phase, got.Attention)
	}
	if got.NextOwner != (TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionConfirmRequirement}) {
		t.Fatalf("next owner = %+v", got.NextOwner)
	}
	if got.SortRank != TrustedSortNeedsHuman || len(got.PendingDecisions) == 0 {
		t.Fatalf("pending sort = %+v", got)
	}
	if got.CanRequestExplanation || got.ExplanationUnavailableReason != "STEWARD_UNAVAILABLE" {
		t.Fatalf("steward availability = %+v", got)
	}
}

func TestDeriveTrustedProgressHistoricalTasksDoNotCount(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	confirmed := RequirementVersion{ID: "rv2", Version: 2, Status: RequirementVersionStatusConfirmed, TaskSetVersion: 2}
	facts := TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Demo"},
			RequirementVersions: []RequirementVersion{{ID: "rv1", Version: 1, Status: RequirementVersionStatusSuperseded, TaskSetVersion: 9}, confirmed},
			DevelopmentTasks: []DevelopmentTask{
				{ID: "old", RequirementVersionID: "rv1", Title: "Old", Status: DevelopmentTaskStatusRunning},
				{ID: "done", RequirementVersionID: confirmed.ID, Title: "Now", Status: DevelopmentTaskStatusDone},
			},
		},
		Now: now,
	}
	got := DeriveTrustedProgress(facts)
	if got.Phase != TrustedPhaseIntegrating || got.TaskCounts.Done != 1 || got.TaskCounts.Running != 0 {
		t.Fatalf("progress = %+v", got)
	}
	var current, historical int
	for _, task := range got.Tasks {
		if task.Current {
			current++
		} else {
			historical++
		}
	}
	if current != 1 || historical != 1 {
		t.Fatalf("tasks current=%d historical=%d", current, historical)
	}
}

func TestDeriveTrustedProgressExpiredEvidenceDoesNotComplete(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	taskSet := int64(1)
	confirmed := RequirementVersion{ID: "rv2", Version: 2, Status: RequirementVersionStatusConfirmed, TaskSetVersion: taskSet}
	candidate := IntegrationCandidate{ID: "ic-1", RequirementVersionID: confirmed.ID, TaskSetVersion: &taskSet, Sequence: 1, CommitSHA: sha40("a")}
	facts := TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:           DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Demo"},
			RequirementVersions:   []RequirementVersion{confirmed},
			DevelopmentTasks:      []DevelopmentTask{{ID: "task-1", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusDone}},
			IntegrationCandidates: []IntegrationCandidate{candidate},
			Evidence:              []EvidenceRecord{{Sequence: 1, Kind: EvidenceKindIntegration, IntegrationCandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Result: EvidenceResultPass, ExpiresAt: &expired}},
		},
		Now: now,
	}
	got := DeriveTrustedProgress(facts)
	if got.Phase != TrustedPhaseIntegrating || len(got.MissingEvidence) == 0 {
		t.Fatalf("expired evidence completed: %+v", got)
	}
}

func TestDeriveTrustedProgressCancelledAndCompletedSort(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	cancelledAt := now
	cancelled := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{Requirement: DevelopmentRequirement{ID: "c", AOProjectID: "ao", Name: "C", CancelledAt: &cancelledAt}},
		Now:      now,
	})
	if cancelled.Phase != TrustedPhaseCancelled || cancelled.NextOwner.Role != TrustedOwnerNone || cancelled.SortRank != TrustedSortCompletedOrCancelled {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	taskSet := int64(1)
	confirmed := RequirementVersion{ID: "rv", Version: 1, Status: RequirementVersionStatusConfirmed, TaskSetVersion: taskSet}
	candidate := IntegrationCandidate{ID: "ic", RequirementVersionID: confirmed.ID, TaskSetVersion: &taskSet, Sequence: 1, CommitSHA: sha40("b")}
	completed := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:           DevelopmentRequirement{ID: "d", AOProjectID: "ao", Name: "D"},
			RequirementVersions:   []RequirementVersion{confirmed},
			DevelopmentTasks:      []DevelopmentTask{{ID: "t", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusDone}},
			IntegrationCandidates: []IntegrationCandidate{candidate},
			Evidence:              []EvidenceRecord{{Sequence: 1, Kind: EvidenceKindIntegration, IntegrationCandidateID: candidate.ID, CommitSHA: candidate.CommitSHA, Result: EvidenceResultPass}},
		},
		Now: now,
	})
	if completed.Phase != TrustedPhaseCompleted || completed.SortRank != TrustedSortCompletedOrCancelled {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestDeriveTrustedProgressBlockedReworkAndUnknown(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	confirmed := RequirementVersion{ID: "rv", Version: 1, Status: RequirementVersionStatusConfirmed, TaskSetVersion: 1}
	blocked := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "b", AOProjectID: "ao", Name: "B"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks:    []DevelopmentTask{{ID: "t", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusBlocked}},
		},
		Now: now,
	})
	if blocked.Phase != TrustedPhaseDeveloping || blocked.Attention != OverallAttentionBlocked || blocked.NextOwner.Action != TrustedActionUnblock {
		t.Fatalf("blocked developing = %+v next=%+v", blocked, blocked.NextOwner)
	}
	if blocked.SortRank != TrustedSortBlockedOrRework || len(blocked.Blockers) == 0 {
		t.Fatalf("blocked sort = %+v", blocked)
	}
	rework := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "r", AOProjectID: "ao", Name: "R"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks:    []DevelopmentTask{{ID: "t", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusRework}},
		},
		Now: now,
	})
	if rework.Attention != OverallAttentionRework || rework.NextOwner.Action != TrustedActionReworkTask {
		t.Fatalf("rework = %+v", rework)
	}
}

func TestDeriveTrustedProgressComplexExecutionScopeOverlay(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	confirmed := RequirementVersion{ID: "rv", Version: 2, Status: RequirementVersionStatusConfirmed, TaskSetVersion: 1}
	execution := ComplexExecutionSnapshot{
		Run:          ComplexExecutionRun{RequirementVersionID: confirmed.ID, TaskSetVersion: confirmed.TaskSetVersion, Decision: ComplexExecutionDecisionDispatch, Mode: WorkModeStandard, BuilderAOSessionID: "builder", InitialBaseCommitSHA: strings.Repeat("a", 40)},
		RoleBindings: []ComplexExecutionRoleBinding{{Role: StandardRoleSteward, Status: RoleBindingStatusBound}},
		Tasks:        []ComplexExecutionTask{{ID: "map-1", Status: DevelopmentTaskStatusRunning, CurrentDispatchID: "d1", CurrentRound: 1}},
		Dispatches:   []ComplexExecutionDispatch{{ID: "d1", ComplexExecutionTaskID: "map-1", Round: 1, Status: ComplexExecutionDispatchRunning}},
	}
	execution.Phase, execution.PhaseReason = DeriveComplexExecutionPhase(execution)
	// Without exception facts the phase is BUILDING. Overlay still must be deterministic.
	facts := TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req", AOProjectID: "ao", Name: "X"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks:    []DevelopmentTask{{ID: "t", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusRunning}},
		},
		ComplexExecution:  &execution,
		HasStewardSession: true,
		Now:               now,
	}
	got := DeriveTrustedProgress(facts)
	if got.Phase != TrustedPhaseDeveloping || got.ExecutionMode != "STANDARD" {
		t.Fatalf("execution overlay = %+v", got)
	}
	if got.FactSummarySHA256 != DeriveTrustedProgress(facts).FactSummarySHA256 {
		t.Fatal("execution overlay hash changed")
	}
}

func TestDeriveTrustedProgressInPolicyScopeWaitsForSteward(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	confirmed := RequirementVersion{ID: "rv", Version: 1, Status: RequirementVersionStatusConfirmed, TaskSetVersion: 1}
	execution := ComplexExecutionSnapshot{
		Run: ComplexExecutionRun{
			RequirementVersionID: confirmed.ID, TaskSetVersion: confirmed.TaskSetVersion, Decision: ComplexExecutionDecisionDispatch, Mode: WorkModeStandard,
			BuilderAOSessionID: "builder", InitialBaseCommitSHA: strings.Repeat("a", 40),
		},
		RoleBindings: []ComplexExecutionRoleBinding{{Role: StandardRoleSteward, Status: RoleBindingStatusBound}},
		Tasks:        []ComplexExecutionTask{{ID: "map-1", Status: DevelopmentTaskStatusRunning, CurrentDispatchID: "d1", CurrentRound: 0}},
		Dispatches:   []ComplexExecutionDispatch{{ID: "d1", ComplexExecutionTaskID: "map-1", Round: 0, Status: ComplexExecutionDispatchRunning}},
		Exception: &ComplexExceptionFacts{
			ScopeRequests: []ComplexScopeExpansionRequest{{ID: "scope-1", Status: "PENDING", DispatchID: "d1"}},
		},
	}
	got := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req", AOProjectID: "ao", Name: "Mail"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks:    []DevelopmentTask{{ID: "t", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusRunning}},
		},
		ComplexExecution:  &execution,
		HasStewardSession: true,
		Now:               now,
	})
	if got.Phase != TrustedPhaseAwaitingScope || got.Attention != OverallAttentionNone {
		t.Fatalf("phase/attention = %s %s", got.Phase, got.Attention)
	}
	if got.NextOwner != (TrustedOwner{Role: TrustedOwnerSteward, Action: TrustedActionDecideScope}) {
		t.Fatalf("next owner = %+v", got.NextOwner)
	}
	if len(got.PendingDecisions) != 0 {
		t.Fatalf("pending decisions = %+v", got.PendingDecisions)
	}
	foundScope := false
	for _, item := range got.CurrentWork {
		if item.Kind == "SCOPE_EXPANSION" {
			foundScope = true
			break
		}
	}
	if !foundScope {
		t.Fatalf("current work = %+v", got.CurrentWork)
	}
	if got.SortRank != TrustedSortInProgress {
		t.Fatalf("sort rank = %d", got.SortRank)
	}
}

func TestSortTrustedProgressSummariesStableOrder(t *testing.T) {
	items := []TrustedProgressSummary{
		{DevelopmentRequirementID: "c", Name: "C", SortRank: TrustedSortCompletedOrCancelled},
		{DevelopmentRequirementID: "a2", Name: "A", SortRank: TrustedSortNeedsHuman},
		{DevelopmentRequirementID: "a1", Name: "A", SortRank: TrustedSortNeedsHuman},
		{DevelopmentRequirementID: "b", Name: "B", SortRank: TrustedSortInProgress},
	}
	SortTrustedProgressSummaries(items)
	got := []string{items[0].DevelopmentRequirementID, items[1].DevelopmentRequirementID, items[2].DevelopmentRequirementID, items[3].DevelopmentRequirementID}
	want := []string{"a1", "a2", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestDeriveTrustedProgressReviewFailureIsABlocker(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	confirmed := RequirementVersion{ID: "rv", Version: 1, Status: RequirementVersionStatusConfirmed, TaskSetVersion: 1}
	got := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao", Name: "Review"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks:    []DevelopmentTask{{ID: "t1", RequirementVersionID: confirmed.ID, Title: "Build", Status: DevelopmentTaskStatusReview}},
			Candidates:          []CandidateCommit{{ID: "cand-2", DevelopmentTaskID: "t1", Sequence: 2, CommitSHA: sha40("b")}},
		},
		StandardFlow: &StandardFlowSnapshot{
			RequirementVersionID: confirmed.ID,
			Dispatches:           []Dispatch{{ID: "dispatch-1", RequirementVersionID: confirmed.ID, Status: DispatchStatusAccepted}},
			LocalReviews: []LocalReview{{
				ID: "review-1", CandidateCommitID: "cand-2", DispatchID: "dispatch-1",
				Verdict: LocalReviewRework, ReasonCode: ReasonCode("REVIEW_REWORK"),
			}},
		},
		Now: now,
	})
	if got.Phase != TrustedPhaseVerifying {
		t.Fatalf("phase = %s", got.Phase)
	}
	found := false
	for _, blocker := range got.Blockers {
		if blocker.Kind == "REVIEW_FAILED" && blocker.SubjectID == "review-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers = %+v, want REVIEW_FAILED", got.Blockers)
	}
}

func TestDeriveTrustedProgressHistoricalReviewFailureDoesNotBlockCompletedWork(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	taskSet := int64(1)
	confirmed := RequirementVersion{ID: "rv", Version: 1, Status: RequirementVersionStatusConfirmed, TaskSetVersion: taskSet}
	integration := IntegrationCandidate{ID: "ic", RequirementVersionID: confirmed.ID, TaskSetVersion: &taskSet, Sequence: 1, CommitSHA: sha40("c")}
	got := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao", Name: "Done"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks:    []DevelopmentTask{{ID: "t1", RequirementVersionID: confirmed.ID, Title: "Build", Status: DevelopmentTaskStatusDone}},
			Candidates: []CandidateCommit{
				{ID: "cand-old", DevelopmentTaskID: "t1", Sequence: 1, CommitSHA: sha40("a")},
				{ID: "cand-new", DevelopmentTaskID: "t1", Sequence: 2, CommitSHA: sha40("b")},
			},
			IntegrationCandidates: []IntegrationCandidate{integration},
			Evidence:              []EvidenceRecord{{Sequence: 1, Kind: EvidenceKindIntegration, IntegrationCandidateID: integration.ID, CommitSHA: integration.CommitSHA, Result: EvidenceResultPass}},
		},
		StandardFlow: &StandardFlowSnapshot{
			RequirementVersionID: confirmed.ID,
			Dispatches:           []Dispatch{{ID: "dispatch-1", RequirementVersionID: confirmed.ID, Status: DispatchStatusAccepted}},
			LocalReviews: []LocalReview{
				{ID: "review-old", CandidateCommitID: "cand-old", DispatchID: "dispatch-1", Verdict: LocalReviewRework, ReasonCode: ReasonCode("REVIEW_REWORK")},
				{ID: "review-new", CandidateCommitID: "cand-new", DispatchID: "dispatch-1", Verdict: LocalReviewPass},
			},
		},
		Now: now,
	})
	if got.Phase != TrustedPhaseCompleted || got.Attention != OverallAttentionNone {
		t.Fatalf("completed progress = phase=%s attention=%s blockers=%+v", got.Phase, got.Attention, got.Blockers)
	}
	if len(got.Blockers) != 0 {
		t.Fatalf("historical review remained a blocker: %+v", got.Blockers)
	}
	if got.NextOwner != (TrustedOwner{Role: TrustedOwnerNone, Action: TrustedActionNone}) {
		t.Fatalf("next owner = %+v", got.NextOwner)
	}
	if got.SortRank != TrustedSortCompletedOrCancelled {
		t.Fatalf("sort rank = %d", got.SortRank)
	}
}

func TestDeriveTrustedProgressOldStandardFlowReviewDoesNotBlockCurrentComplexExecution(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	taskSet := int64(1)
	confirmed := RequirementVersion{ID: "rv2", Version: 2, Status: RequirementVersionStatusConfirmed, TaskSetVersion: taskSet}
	integration := IntegrationCandidate{ID: "ic", RequirementVersionID: confirmed.ID, TaskSetVersion: &taskSet, Sequence: 1, CommitSHA: sha40("c")}
	completedAt := now
	execution := ComplexExecutionSnapshot{
		Run: ComplexExecutionRun{Decision: ComplexExecutionDecisionDispatch, Mode: WorkModeStandard, CompletedAt: &completedAt},
		Tasks: []ComplexExecutionTask{{
			ID: "map-1", DevelopmentTaskID: "t-new", Status: DevelopmentTaskStatusDone,
			CurrentDispatchID: "d-new", CurrentRound: 1,
		}},
		Dispatches: []ComplexExecutionDispatch{{
			ID: "d-new", ComplexExecutionTaskID: "map-1", Round: 1,
			CandidateCommitID: "cand-new", Status: ComplexExecutionDispatchVerified,
		}},
		Reviews: []ComplexExecutionReview{{
			ID: "review-current", ComplexExecutionTaskID: "map-1", DispatchID: "d-new",
			CandidateCommitID: "cand-new", Verdict: LocalReviewPass,
		}},
		Integration: &ComplexExecutionIntegration{ID: "int-1", IntegrationCandidateID: integration.ID, CandidateCommitSHA: integration.CommitSHA},
	}
	execution.Phase, execution.PhaseReason = DeriveComplexExecutionPhase(execution)
	got := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao", Name: "Moved on"},
			RequirementVersions: []RequirementVersion{{ID: "rv1", Version: 1, Status: RequirementVersionStatusSuperseded}, confirmed},
			DevelopmentTasks: []DevelopmentTask{
				{ID: "t-old", RequirementVersionID: "rv1", Title: "Old", Status: DevelopmentTaskStatusRework},
				{ID: "t-new", RequirementVersionID: confirmed.ID, Title: "Now", Status: DevelopmentTaskStatusDone},
			},
			Candidates: []CandidateCommit{
				{ID: "cand-old", DevelopmentTaskID: "t-old", Sequence: 1, CommitSHA: sha40("a")},
				{ID: "cand-new", DevelopmentTaskID: "t-new", Sequence: 1, CommitSHA: sha40("b")},
			},
			IntegrationCandidates: []IntegrationCandidate{integration},
			Evidence:              []EvidenceRecord{{Sequence: 1, Kind: EvidenceKindIntegration, IntegrationCandidateID: integration.ID, CommitSHA: integration.CommitSHA, Result: EvidenceResultPass}},
		},
		StandardFlow: &StandardFlowSnapshot{
			RequirementVersionID: "rv1",
			Dispatches:           []Dispatch{{ID: "dispatch-old", RequirementVersionID: "rv1", Status: DispatchStatusAccepted}},
			LocalReviews: []LocalReview{{
				ID: "review-old-flow", CandidateCommitID: "cand-old", DispatchID: "dispatch-old",
				Verdict: LocalReviewBlocked, ReasonCode: ReasonCode("REVIEW_BLOCKED"),
			}},
		},
		ComplexExecution: &execution,
		Now:              now,
	})
	if got.Phase != TrustedPhaseCompleted || got.Attention != OverallAttentionNone {
		t.Fatalf("current complex completion = phase=%s attention=%s blockers=%+v", got.Phase, got.Attention, got.Blockers)
	}
	if len(got.Blockers) != 0 {
		t.Fatalf("old standard-flow review blocked the current flow: %+v", got.Blockers)
	}
	if got.NextOwner != (TrustedOwner{Role: TrustedOwnerNone, Action: TrustedActionNone}) {
		t.Fatalf("next owner = %+v", got.NextOwner)
	}
}

func TestDeriveTrustedProgressPreviousComplexRoundReviewIsNotACurrentBlocker(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	taskSet := int64(1)
	confirmed := RequirementVersion{ID: "rv", Version: 1, Status: RequirementVersionStatusConfirmed, TaskSetVersion: taskSet}
	integration := IntegrationCandidate{ID: "ic", RequirementVersionID: confirmed.ID, TaskSetVersion: &taskSet, Sequence: 1, CommitSHA: sha40("c")}
	completedAt := now
	execution := ComplexExecutionSnapshot{
		Run: ComplexExecutionRun{Decision: ComplexExecutionDecisionDispatch, Mode: WorkModeStandard, CompletedAt: &completedAt},
		Tasks: []ComplexExecutionTask{{
			ID: "map-1", DevelopmentTaskID: "t1", Status: DevelopmentTaskStatusDone,
			CurrentDispatchID: "d2", CurrentRound: 1,
		}},
		Dispatches: []ComplexExecutionDispatch{
			{ID: "d1", ComplexExecutionTaskID: "map-1", Round: 0, CandidateCommitID: "cand-1", Status: ComplexExecutionDispatchRework},
			{ID: "d2", ComplexExecutionTaskID: "map-1", Round: 1, CandidateCommitID: "cand-2", Status: ComplexExecutionDispatchVerified},
		},
		Reviews: []ComplexExecutionReview{
			{ID: "review-old-round", ComplexExecutionTaskID: "map-1", DispatchID: "d1", CandidateCommitID: "cand-1", Verdict: LocalReviewRework, ReasonCode: ReasonCode("REVIEW_REWORK")},
			{ID: "review-current", ComplexExecutionTaskID: "map-1", DispatchID: "d2", CandidateCommitID: "cand-2", Verdict: LocalReviewPass},
		},
		Integration: &ComplexExecutionIntegration{ID: "int-1", IntegrationCandidateID: integration.ID, CandidateCommitSHA: integration.CommitSHA},
	}
	execution.Phase, execution.PhaseReason = DeriveComplexExecutionPhase(execution)
	got := DeriveTrustedProgress(TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement:         DevelopmentRequirement{ID: "req-1", AOProjectID: "ao", Name: "Reworked"},
			RequirementVersions: []RequirementVersion{confirmed},
			DevelopmentTasks:    []DevelopmentTask{{ID: "t1", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusDone}},
			Candidates: []CandidateCommit{
				{ID: "cand-1", DevelopmentTaskID: "t1", Sequence: 1, CommitSHA: sha40("a")},
				{ID: "cand-2", DevelopmentTaskID: "t1", Sequence: 2, CommitSHA: sha40("b")},
			},
			IntegrationCandidates: []IntegrationCandidate{integration},
			Evidence:              []EvidenceRecord{{Sequence: 1, Kind: EvidenceKindIntegration, IntegrationCandidateID: integration.ID, CommitSHA: integration.CommitSHA, Result: EvidenceResultPass}},
		},
		ComplexExecution: &execution,
		Now:              now,
	})
	if got.Phase != TrustedPhaseCompleted || len(got.Blockers) != 0 || got.NextOwner.Role != TrustedOwnerNone {
		t.Fatalf("previous round leaked into current progress: %+v next=%+v", got, got.NextOwner)
	}
}

func TestDeriveTrustedProgressCurrentPreflightBlocks(t *testing.T) {
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	facts := TrustedProgressFacts{
		Snapshot: RequirementSnapshot{
			Requirement: DevelopmentRequirement{ID: "req-1", AOProjectID: "ao-1", Name: "Blocked model"},
		},
		ComplexPlanning: &ComplexPlanningSnapshot{
			Requirement: ComplexRequirement{DevelopmentRequirementID: "req-1"},
			RoleBindings: []ComplexRoleBinding{{
				ID: "bind-steward", Role: StandardRoleSteward, Status: RoleBindingStatusRequested,
			}},
		},
		Controlled: []ControlledProgressFacts{{BudgetVersion: MessageBudgetV1, Target: ControlledWork{RoleBindingID: "bind-steward"}, Preflight: &ControlledPreflight{RoleBindingID: "bind-steward", Outcome: ControlledPreflightFailed, ReasonCode: ReasonModelNotAvailable}}},
		Now:        now,
	}
	got := DeriveTrustedProgress(facts)
	if got.Phase != TrustedPhaseBlocked {
		t.Fatalf("phase = %s, want BLOCKED", got.Phase)
	}
	if got.Attention != OverallAttentionBlocked || got.ReasonCode != ReasonModelNotAvailable {
		t.Fatalf("attention/reason = %s %s", got.Attention, got.ReasonCode)
	}
	if len(got.Blockers) != 1 || got.Blockers[0].Kind != "AWAITING_USER" || got.Blockers[0].ReasonCode != ReasonModelNotAvailable {
		t.Fatalf("blockers = %+v", got.Blockers)
	}
	if got.NextOwner.Action != TrustedActionUnblock {
		t.Fatalf("next owner = %+v", got.NextOwner)
	}
}
