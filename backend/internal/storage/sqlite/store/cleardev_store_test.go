package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func requirementDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func initialRequirement(id, aoProjectID string, now time.Time) core.InitialRequirement {
	text := "requirement v1"
	return core.InitialRequirement{
		Requirement: core.DevelopmentRequirement{
			ID: id, AOProjectID: aoProjectID, Name: "S01", CreatedAt: now, UpdatedAt: now,
		},
		Version: core.RequirementVersion{
			ID: id + "-v1", DevelopmentRequirementID: id, Version: 1,
			RequirementText: text, SHA256: requirementDigest(text),
			Status: core.RequirementVersionStatusDraft, CreatedAt: now,
		},
	}
}

func initialTask(requirementID, id string, now time.Time) core.InitialDevelopmentTask {
	return core.InitialDevelopmentTask{
		Task: core.DevelopmentTask{
			ID: id, DevelopmentRequirementID: requirementID, Title: "implement",
			Mode: core.WorkModeQuick, Status: core.DevelopmentTaskStatusPlanned,
			MaxReworkCount: 1, CreatedAt: now, UpdatedAt: now,
		},
		Permission: core.PermissionVersion{
			ID: id + "-permission-1", DevelopmentTaskID: id, Version: 1,
			Rules: core.PathRules{WritePaths: []string{"backend/**"}}, CreatedAt: now,
		},
		Checks: []core.RequiredCheck{{
			ID: id + "-check-1", DevelopmentTaskID: id, Name: "build", Kind: "command", CreatedAt: now,
		}},
	}
}

func seedClearDevAO(t *testing.T, store *sqlite.Store, projectID string) domain.SessionRecord {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: projectID, Path: "/tmp/" + projectID,
		Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, sampleRecord(projectID))
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func applyClearDev(t *testing.T, store *sqlite.Store, action core.Action, id string, at time.Time, trusted bool, reason string) error {
	t.Helper()
	return store.ApplyClearDevAction(context.Background(), core.ActionRequest{
		Action: action, SubjectID: id, TrustedHumanDecision: trusted, ReasonText: reason, At: at,
	})
}

func confirmVersion(t *testing.T, store *sqlite.Store, id string, now time.Time) {
	t.Helper()
	if err := applyClearDev(t, store, core.ActionSubmitRequirementConfirmation, id, now, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionConfirmRequirementVersion, id, now.Add(time.Second), true, ""); err != nil {
		t.Fatal(err)
	}
}

func TestClearDevRequirementVersionAndTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	session := seedClearDevAO(t, store, "ao-v3")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-1", "ao-v3", now)); err != nil {
		t.Fatal(err)
	}
	snapshot, ok, err := store.GetClearDevRequirement(ctx, "req-1")
	if err != nil || !ok {
		t.Fatalf("initial snapshot: ok=%v err=%v", ok, err)
	}
	if len(snapshot.RequirementVersions) != 1 || len(snapshot.DevelopmentTasks) != 0 || snapshot.RequirementVersions[0].Status != core.RequirementVersionStatusDraft {
		t.Fatalf("initial facts = %#v", snapshot)
	}
	confirmVersion(t, store, "req-1-v1", now.Add(time.Minute))
	if err := store.CreateClearDevTask(ctx, initialTask("req-1", "task-1", now.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err = store.GetClearDevRequirement(ctx, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DevelopmentTasks[0].RequirementVersionID != "req-1-v1" || snapshot.RequirementVersions[0].TaskSetVersion != 1 {
		t.Fatalf("task binding/task set = %#v / %#v", snapshot.DevelopmentTasks, snapshot.RequirementVersions)
	}
	if err := applyClearDev(t, store, core.ActionStartDevelopmentTask, "task-1", now.Add(3*time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.AppendClearDevCandidate(ctx, core.CandidateObservation{
		ID: "candidate-1", DevelopmentTaskID: "task-1", AOSessionID: string(session.ID),
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", ObservedAt: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, evidence := range []struct {
		kind core.EvidenceKind
		key  string
	}{{core.EvidenceKindScope, ""}, {core.EvidenceKindRequiredCheck, "build"}, {core.EvidenceKindIntegration, ""}} {
		if err := store.AppendClearDevEvidence(ctx, core.EvidenceRecord{
			ID: "task-evidence-" + string(rune('1'+index)), DevelopmentRequirementID: "req-1",
			SubjectType: core.SubjectDevelopmentTask, SubjectID: "task-1", Kind: evidence.kind, Key: evidence.key,
			Result: core.EvidenceResultPass, CandidateCommitID: candidate.ID, CommitSHA: candidate.CommitSHA,
			Source: core.EvidenceSourceControlPlaneChecker, CreatedAt: now.Add(time.Duration(5+index) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyClearDev(t, store, core.ActionSubmitDevelopmentTaskReview, "task-1", now.Add(9*time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionCompleteDevelopmentTask, "task-1", now.Add(10*time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}
	snapshot, _, _ = store.GetClearDevRequirement(ctx, "req-1")
	if snapshot.DevelopmentTasks[0].Status != core.DevelopmentTaskStatusDone {
		t.Fatalf("task status = %s", snapshot.DevelopmentTasks[0].Status)
	}
	integration, err := store.AppendClearDevIntegrationCandidate(ctx, core.IntegrationCandidateObservation{
		ID: "integration-1", DevelopmentRequirementID: "req-1", AOSessionID: string(session.ID),
		CommitSHA: "abcdef0123456789abcdef0123456789abcdef01", ObservedAt: now.Add(11 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if integration.RequirementVersionID != "req-1-v1" || integration.TaskSetVersion == nil || *integration.TaskSetVersion != 1 {
		t.Fatalf("integration binding = %#v", integration)
	}
	if err := store.AppendClearDevEvidence(ctx, core.EvidenceRecord{
		ID: "integration-evidence-1", DevelopmentRequirementID: "req-1",
		SubjectType: core.SubjectDevelopmentRequirement, SubjectID: "req-1", Kind: core.EvidenceKindIntegration,
		Result: core.EvidenceResultPass, IntegrationCandidateID: integration.ID, CommitSHA: integration.CommitSHA,
		Source: core.EvidenceSourceControlPlaneChecker, CreatedAt: now.Add(12 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, _, _ = store.GetClearDevRequirement(ctx, "req-1")
	if progress := core.DeriveOverallProgress(snapshot, now.Add(13*time.Minute)); progress.Phase != core.OverallPhaseCompleted {
		t.Fatalf("progress before task-set change = %#v", progress)
	}
	if err := store.CreateClearDevTask(ctx, initialTask("req-1", "task-2", now.Add(14*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendClearDevEvidence(ctx, core.EvidenceRecord{
		ID: "stale-integration-evidence", DevelopmentRequirementID: "req-1",
		SubjectType: core.SubjectDevelopmentRequirement, SubjectID: "req-1", Kind: core.EvidenceKindIntegration,
		Result: core.EvidenceResultPass, IntegrationCandidateID: integration.ID, CommitSHA: integration.CommitSHA,
		Source: core.EvidenceSourceControlPlaneChecker, CreatedAt: now.Add(15 * time.Minute),
	}); err == nil {
		t.Fatal("stale integration candidate accepted after task-set change")
	}
	snapshot, _, _ = store.GetClearDevRequirement(ctx, "req-1")
	if snapshot.RequirementVersions[0].TaskSetVersion != 2 {
		t.Fatalf("task set after new task = %d", snapshot.RequirementVersions[0].TaskSetVersion)
	}
}

func TestClearDevPauseAfterReworkLimitRecordsStableReason(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	session := seedClearDevAO(t, store, "ao-pause-limit")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-pause-limit", "ao-pause-limit", now)); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "req-pause-limit-v1", now.Add(time.Minute))
	initial := initialTask("req-pause-limit", "task-pause-limit", now.Add(2*time.Minute))
	initial.Task.MaxReworkCount = 0
	if err := store.CreateClearDevTask(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionStartDevelopmentTask, initial.Task.ID, now.Add(3*time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}
	candidate, err := store.AppendClearDevCandidate(ctx, core.CandidateObservation{
		ID: "candidate-pause-limit", DevelopmentTaskID: initial.Task.ID, AOSessionID: string(session.ID),
		CommitSHA: "1234567890abcdef1234567890abcdef12345678", ObservedAt: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendClearDevEvidence(ctx, core.EvidenceRecord{
		ID: "scope-pause-limit", DevelopmentRequirementID: initial.Task.DevelopmentRequirementID,
		SubjectType: core.SubjectDevelopmentTask, SubjectID: initial.Task.ID, Kind: core.EvidenceKindScope,
		Result: core.EvidenceResultFail, CandidateCommitID: candidate.ID, CommitSHA: candidate.CommitSHA,
		Source: core.EvidenceSourceControlPlaneChecker, CreatedAt: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionSubmitDevelopmentTaskReview, initial.Task.ID, now.Add(6*time.Minute), false, ""); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionPauseDevelopmentTaskNeedsHuman, initial.Task.ID, now.Add(7*time.Minute), false, "operator review"); err != nil {
		t.Fatal(err)
	}
	snapshot, ok, err := store.GetClearDevRequirement(ctx, initial.Task.DevelopmentRequirementID)
	if err != nil || !ok {
		t.Fatalf("get paused requirement: ok=%v err=%v", ok, err)
	}
	if got := snapshot.DevelopmentTasks[0].Status; got != core.DevelopmentTaskStatusNeedsHuman {
		t.Fatalf("paused task status = %s", got)
	}
	last := snapshot.Events[len(snapshot.Events)-1]
	if last.Action != core.ActionPauseDevelopmentTaskNeedsHuman || last.Outcome != core.EventAccepted || last.Reason != core.ReasonReworkLimitReached {
		t.Fatalf("pause event = %#v", last)
	}
}

func TestClearDevVersionReplacementAndHistoricalTaskCancellationIsolation(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seedClearDevAO(t, store, "ao-versions")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-v", "ao-versions", now)); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "req-v-v1", now.Add(time.Minute))
	if err := store.CreateClearDevTask(ctx, initialTask("req-v", "old-task", now.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	text := "requirement v2"
	if err := store.CreateClearDevRequirementVersion(ctx, core.RequirementVersion{
		ID: "req-v-v2", DevelopmentRequirementID: "req-v", Version: 0,
		RequirementText: text, SHA256: requirementDigest(text), Status: core.RequirementVersionStatusDraft,
		CreatedAt: now.Add(3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, store, "req-v-v2", now.Add(4*time.Minute))
	if err := store.CreateClearDevTask(ctx, initialTask("req-v", "current-task", now.Add(6*time.Minute))); err != nil {
		t.Fatal(err)
	}
	before, _, _ := store.GetClearDevRequirement(ctx, "req-v")
	if err := applyClearDev(t, store, core.ActionCancelDevelopmentTask, "old-task", now.Add(7*time.Minute), false, "obsolete"); err != nil {
		t.Fatal(err)
	}
	after, _, _ := store.GetClearDevRequirement(ctx, "req-v")
	var beforeCurrent, afterCurrent, oldTaskSet int64
	for _, version := range before.RequirementVersions {
		if version.ID == "req-v-v2" {
			beforeCurrent = version.TaskSetVersion
		}
	}
	for _, version := range after.RequirementVersions {
		if version.ID == "req-v-v2" {
			afterCurrent = version.TaskSetVersion
		}
		if version.ID == "req-v-v1" {
			oldTaskSet = version.TaskSetVersion
		}
	}
	if beforeCurrent != 1 || afterCurrent != 1 || oldTaskSet != 2 {
		t.Fatalf("task-set isolation: before=%d after=%d old=%d", beforeCurrent, afterCurrent, oldTaskSet)
	}
	if after.RequirementVersions[0].Status != core.RequirementVersionStatusSuperseded || after.RequirementVersions[1].Status != core.RequirementVersionStatusConfirmed {
		t.Fatalf("replacement statuses = %#v", after.RequirementVersions)
	}
}

func TestClearDevCancellationRejectsWritesAndRecordsStableReasons(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seedClearDevAO(t, store, "ao-cancel")
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.CreateClearDevRequirement(ctx, initialRequirement("req-cancel", "ao-cancel", now)); err != nil {
		t.Fatal(err)
	}
	if err := applyClearDev(t, store, core.ActionCancelRequirement, "req-cancel", now.Add(time.Minute), false, "stop"); err == nil {
		t.Fatal("untrusted cancellation was accepted")
	} else {
		var rule *core.RuleError
		if !errors.As(err, &rule) || rule.Code != core.ReasonHumanDecisionRequired {
			t.Fatalf("untrusted cancellation error = %v", err)
		}
	}
	if err := applyClearDev(t, store, core.ActionCancelRequirement, "req-cancel", now.Add(2*time.Minute), true, "stop"); err != nil {
		t.Fatal(err)
	}
	text := "requirement v2"
	err := store.CreateClearDevRequirementVersion(ctx, core.RequirementVersion{
		ID: "cancel-v2", DevelopmentRequirementID: "req-cancel", Version: 0,
		RequirementText: text, SHA256: requirementDigest(text), Status: core.RequirementVersionStatusDraft,
		CreatedAt: now.Add(3 * time.Minute),
	})
	var rule *core.RuleError
	if !errors.As(err, &rule) || rule.Code != core.ReasonRequirementCancelled {
		t.Fatalf("post-cancel write error = %v", err)
	}
	snapshot, _, err := store.GetClearDevRequirement(ctx, "req-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Requirement.CancelReason != core.ReasonUserCancelled {
		t.Fatalf("cancel reason = %q", snapshot.Requirement.CancelReason)
	}
	last := snapshot.Events[len(snapshot.Events)-1]
	if last.Outcome != core.EventRejected || last.Reason != core.ReasonRequirementCancelled {
		t.Fatalf("last event = %#v", last)
	}
}
