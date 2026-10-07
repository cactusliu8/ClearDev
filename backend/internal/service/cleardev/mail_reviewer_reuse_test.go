package cleardev

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Explicit fake Git worktree movement for the existing mail flow fixture.
// Production PrepareBaseWorkspace is exercised separately with real Git.
func (h *mailFlowChecks) PrepareBaseWorkspace(_ context.Context, workspace, oldSHA, newSHA string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reviewerWorkspaces[workspace] != oldSHA {
		return ports.ErrClearDevCandidateInvalid
	}
	h.reviewerWorkspaces[workspace] = newSHA
	return nil
}

func mailReworkCompleted(t *testing.T, f *autoExecutionFixture) core.ComplexExecutionSnapshot {
	t.Helper()
	execution, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if err != nil || !found || phase != core.ComplexExecutionCompleted || len(execution.Tasks) != 1 || len(execution.Dispatches) != 2 || len(execution.Reviews) != 2 || execution.Integration == nil {
		t.Fatalf("rework completion: phase=%s reason=%s found=%v err=%v", phase, reason, found, err)
	}
	view, err := f.service.GetRequirement(context.Background(), f.view.Requirement.ID)
	if err != nil || view.TrustedProgress.Phase != core.TrustedPhaseCompleted {
		t.Fatalf("trusted phase=%s err=%v", view.TrustedProgress.Phase, err)
	}
	f.noQuick(t)
	return execution
}

func TestMailReworkReusesReviewerButCreatesFreshReview(t *testing.T) {
	f, h := newMailFlowFixture(t)
	h.reworkOnce = true
	f.confirm(t)
	execution := mailReworkCompleted(t, f)
	if len(execution.Reviews) != 2 || len(execution.Dispatches) != 2 {
		t.Fatalf("reviews=%d dispatches=%d", len(execution.Reviews), len(execution.Dispatches))
	}
	old, fresh := execution.Reviews[0], execution.Reviews[1]
	oldRole, _ := complexExecutionBindingByID(execution, old.ReviewerRoleBindingID)
	newRole, _ := complexExecutionBindingByID(execution, fresh.ReviewerRoleBindingID)
	if oldRole.AOSessionID == "" || oldRole.AOSessionID != newRole.AOSessionID || oldRole.WorkspacePath != newRole.WorkspacePath || newRole.ContinuationOfRoleBindingID != oldRole.ID {
		t.Fatalf("not the same reviewer: old=%+v new=%+v", oldRole, newRole)
	}
	oldStep, _ := complexExecutionStepByID(execution, old.AgentStepID)
	newStep, _ := complexExecutionStepByID(execution, fresh.AgentStepID)
	if old.ID == fresh.ID || old.AgentStepID == fresh.AgentStepID || old.CandidateCommitID == fresh.CandidateCommitID || old.CandidateCommitSHA == fresh.CandidateCommitSHA || old.ReviewPacketSHA256 == fresh.ReviewPacketSHA256 || oldStep.TurnID == newStep.TurnID || oldStep.ClientMessageID == newStep.ClientMessageID || old.Verdict != core.LocalReviewRework || fresh.Verdict != core.LocalReviewPass {
		t.Fatalf("not fresh review facts: old=%+v new=%+v", old, fresh)
	}
	if len(h.spawnConfigs) != 4 || h.reviewerRuns != 2 || execution.Integration.CandidateCommitSHA != fresh.CandidateCommitSHA {
		t.Fatalf("spawns=%d reviews=%d final=%s", len(h.spawnConfigs), h.reviewerRuns, execution.Integration.CandidateCommitSHA)
	}
	if changed, err := f.store.BindClearDevComplexExecutionRoleBinding(context.Background(), newRole.ID, newRole.AOSessionID, newRole.WorkspacePath, newRole.BaseCommitSHA, f.clock()); err != nil || changed {
		t.Fatalf("exact binding replay failed: %v", err)
	}
	if _, err := f.store.BindClearDevComplexExecutionRoleBinding(context.Background(), newRole.ID, newRole.AOSessionID, newRole.WorkspacePath, oldRole.BaseCommitSHA, f.clock()); err == nil {
		t.Fatal("binding replay accepted stale candidate SHA")
	}
	counts := f.counts()
	f.reopen(t)
	f.service.checks, f.service.inspector, f.service.chat = h, h, h
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ResumeComplexFlows(context.Background()); err != nil {
		t.Fatal(err)
	}
	again := mailReworkCompleted(t, f)
	if f.counts() != counts || !reflect.DeepEqual(execution.Reviews, again.Reviews) {
		t.Fatal("completed restart changed reviews or repeated work")
	}
}

func TestMailReviewerReuseDowngradePreservesHistory(t *testing.T) {
	f, h := newMailFlowFixture(t)
	h.reworkOnce = true
	f.confirm(t)
	before := mailReworkCompleted(t, f)
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeVersion int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(context.Background(), 129); err == nil {
		t.Fatal("downgrade erased recorded Reviewer reuse")
	}
	var version int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != beforeVersion {
		t.Fatalf("downgrade changed version: %d %v", version, err)
	}
	if after := mailReworkCompleted(t, f); !reflect.DeepEqual(before, after) {
		t.Fatal("refused downgrade changed immutable review history")
	}
}

func TestMailReviewerSecondReworkStopsWithoutAnotherSession(t *testing.T) {
	f, h := newMailFlowFixture(t)
	h.reviewVerdicts = []string{"REWORK", "REWORK"}
	f.confirm(t)
	assertMailNotCompleted(t, f)
	if len(h.spawnConfigs) != 4 || h.reviewerRuns != 2 {
		t.Fatal("second REWORK reset the retry limit or recreated Reviewer")
	}
}
