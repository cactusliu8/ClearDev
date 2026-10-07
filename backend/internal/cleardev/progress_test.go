package cleardev

import (
	"testing"
	"time"
)

func TestDeriveOverallProgress(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	taskSet := int64(2)
	confirmed := RequirementVersion{ID: "rv2", Version: 2, Status: RequirementVersionStatusConfirmed, TaskSetVersion: taskSet}
	done := DevelopmentTask{ID: "task-1", RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusDone}
	matchingCandidate := IntegrationCandidate{ID: "ic-1", RequirementVersionID: confirmed.ID, TaskSetVersion: &taskSet, Sequence: 2, CommitSHA: sha40("a")}
	tests := []struct {
		name          string
		snapshot      RequirementSnapshot
		wantPhase     OverallPhase
		wantAttention OverallAttention
		wantCounts    TaskCounts
		wantMissing   string
	}{
		{
			name:      "cancelled wins over all other facts",
			snapshot:  RequirementSnapshot{Requirement: DevelopmentRequirement{CancelledAt: &now}, RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{done}},
			wantPhase: OverallPhaseCancelled, wantAttention: OverallAttentionNone,
		},
		{
			name:      "latest pending confirmation without current version",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{{ID: "rv1", Version: 1, Status: RequirementVersionStatusPendingConfirmation}}},
			wantPhase: OverallPhaseAwaitingConfirmation, wantAttention: OverallAttentionNeedsHuman,
		},
		{
			name:      "draft version needs definition",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{{ID: "rv1", Version: 1, Status: RequirementVersionStatusDraft}}},
			wantPhase: OverallPhaseDefiningRequirement, wantAttention: OverallAttentionNone,
		},
		{
			name:      "confirmed version with no active task plans tasks",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{{RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusCancelled}}},
			wantPhase: OverallPhasePlanningTasks, wantAttention: OverallAttentionNone,
		},
		{
			name: "development has priority over verification and attention reports rework",
			snapshot: RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{
				{RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusRunning},
				{RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusReview},
				{RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusRework},
			}},
			wantPhase: OverallPhaseDeveloping, wantAttention: OverallAttentionRework,
			wantCounts: TaskCounts{Running: 1, Review: 1, Rework: 1},
		},
		{
			name:      "reviewing when no developing task remains",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{{RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusReview}}},
			wantPhase: OverallPhaseVerifying, wantAttention: OverallAttentionNone,
			wantCounts: TaskCounts{Review: 1},
		},
		{
			name:      "all done without candidate integrates",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{done}},
			wantPhase: OverallPhaseIntegrating, wantAttention: OverallAttentionNone, wantCounts: TaskCounts{Done: 1}, wantMissing: MissingEvidenceIntegrationCandidate,
		},
		{
			name:      "matching candidate without current result integrates",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{done}, IntegrationCandidates: []IntegrationCandidate{matchingCandidate}},
			wantPhase: OverallPhaseIntegrating, wantAttention: OverallAttentionNone, wantCounts: TaskCounts{Done: 1}, wantMissing: MissingEvidenceIntegrationResult,
		},
		{
			name:      "latest matching integration pass completes",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{done}, IntegrationCandidates: []IntegrationCandidate{matchingCandidate}, Evidence: []EvidenceRecord{{Sequence: 1, Kind: EvidenceKindIntegration, IntegrationCandidateID: matchingCandidate.ID, CommitSHA: matchingCandidate.CommitSHA, Result: EvidenceResultPass}}},
			wantPhase: OverallPhaseCompleted, wantAttention: OverallAttentionNone, wantCounts: TaskCounts{Done: 1},
		},
		{
			name:      "latest matching integration failure is visible",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed}, DevelopmentTasks: []DevelopmentTask{done}, IntegrationCandidates: []IntegrationCandidate{matchingCandidate}, Evidence: []EvidenceRecord{{Sequence: 1, Kind: EvidenceKindIntegration, IntegrationCandidateID: matchingCandidate.ID, CommitSHA: matchingCandidate.CommitSHA, Result: EvidenceResultPass}, {Sequence: 2, Kind: EvidenceKindIntegration, IntegrationCandidateID: matchingCandidate.ID, CommitSHA: matchingCandidate.CommitSHA, Result: EvidenceResultFail}}},
			wantPhase: OverallPhaseIntegrating, wantAttention: OverallAttentionIntegrationFailed, wantCounts: TaskCounts{Done: 1},
		},
		{
			name:      "current version excludes history and old candidate binding",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{{ID: "rv1", Version: 1, Status: RequirementVersionStatusSuperseded, TaskSetVersion: 9}, confirmed}, DevelopmentTasks: []DevelopmentTask{{RequirementVersionID: "rv1", Status: DevelopmentTaskStatusRunning}, done}, IntegrationCandidates: []IntegrationCandidate{{ID: "legacy", RequirementVersionID: "rv1", Sequence: 99, CommitSHA: sha40("b")}}},
			wantPhase: OverallPhaseIntegrating, wantAttention: OverallAttentionNone, wantCounts: TaskCounts{Done: 1}, wantMissing: MissingEvidenceIntegrationCandidate,
		},
		{
			name:      "pending version attention dominates blocked task",
			snapshot:  RequirementSnapshot{RequirementVersions: []RequirementVersion{confirmed, {ID: "rv3", Version: 3, Status: RequirementVersionStatusPendingConfirmation}}, DevelopmentTasks: []DevelopmentTask{{RequirementVersionID: confirmed.ID, Status: DevelopmentTaskStatusBlocked}}},
			wantPhase: OverallPhaseDeveloping, wantAttention: OverallAttentionNeedsHuman, wantCounts: TaskCounts{Blocked: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveOverallProgress(tt.snapshot, now)
			if got.Phase != tt.wantPhase || got.Attention != tt.wantAttention || got.TaskCounts != tt.wantCounts {
				t.Fatalf("DeriveOverallProgress() = phase=%s attention=%s counts=%+v, want phase=%s attention=%s counts=%+v", got.Phase, got.Attention, got.TaskCounts, tt.wantPhase, tt.wantAttention, tt.wantCounts)
			}
			if tt.wantMissing == "" {
				if len(got.MissingEvidence) != 0 {
					t.Fatalf("unexpected missing evidence: %+v", got.MissingEvidence)
				}
			} else if len(got.MissingEvidence) != 1 || got.MissingEvidence[0].Kind != tt.wantMissing {
				t.Fatalf("missing evidence = %+v, want %q", got.MissingEvidence, tt.wantMissing)
			}
		})
	}
}

func sha40(char string) string {
	result := ""
	for range 40 {
		result += char
	}
	return result
}
