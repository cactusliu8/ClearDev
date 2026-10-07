package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SQL fault injection is restricted to this fresh temporary test database.
// Production entry points continue to use Service/Store transactions only.
func TestProjectReviewerSQLRejectsForgedAndReplaceableReceipts(t *testing.T) {
	t.Run("historical", func(t *testing.T) { testProjectReviewerSQLReceipts(t, false) })
	t.Run("fresh", func(t *testing.T) { testProjectReviewerSQLReceipts(t, true) })
}

func testProjectReviewerSQLReceipts(t *testing.T, fresh bool) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := attachProjectRequestingReviewer(f, preparer)
	h.freshInstall = fresh
	ctx := context.Background()
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	var execution core.ComplexExecutionSnapshot
	var request core.ReviewCheckRequest
	var review core.ComplexExecutionReview
	found := false
	for transition := 0; transition < 100 && !found; transition++ {
		progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
		if err != nil || stopped || !progressed {
			t.Fatalf("could not reach the pending supplemental request: %v", err)
		}
		execution, _, err = f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(execution.Reviews) == 1 {
			review = execution.Reviews[0]
			request, _, found, err = f.store.GetClearDevReviewerChecks(ctx, review.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found || request.ProjectExecution == nil {
		t.Fatal("the original requesting Reviewer did not persist its exact contract")
	}
	check, _ := core.ReviewCheckSpec(request, request.CheckIDs[0])
	runID := core.ReviewCheckRunID(review.ID, check.ID)
	result, err := f.s.runExecutionCandidateCheck(ctx, execution.Run, ports.ClearDevCheckRequest{
		RunID: runID, WorkspacePath: review.CandidateWorkspacePath, CandidateSHA: request.CandidateSHA,
		Image: core.StandardCandidateCheckImage, Argv: check.Argv, Timeout: time.Duration(check.TimeoutSeconds) * time.Second,
		MemoryBytes: standardCheckMemoryBytes, PidsLimit: standardCheckPidsLimit, OutputLimit: standardCheckOutputLimit,
	}, check.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence := reviewCheckEvidence(request, check.ID, check.Argv, result, nil, time.Now().UTC())
	if err := core.ValidateReviewCheckEvidence(request, evidence); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	insert := "INSERT INTO cleardev_review_check_results(review_id,check_id,run_id,result_json,result_sha256,created_at) VALUES(?,?,?,?,?,?)"
	for name, mutate := range map[string]func(*core.ReviewCheckEvidence){
		"mixed-policy": func(e *core.ReviewCheckEvidence) {
			if fresh {
				e.ProjectReceipt.Policy = core.ProjectCheckPolicyV1
			} else {
				e.ProjectReceipt.Policy = core.ProjectCheckPolicyV2
			}
		},
		"mixed-cache": func(e *core.ReviewCheckEvidence) {
			if fresh {
				e.ProjectReceipt.DependencyCacheKey = strings.Repeat("6", 64)
			} else {
				e.ProjectReceipt.DependencyCacheKey = ""
			}
		},
		"missing-receipt": func(e *core.ReviewCheckEvidence) { e.ProjectReceipt = nil },
		"wrong-contract":  func(e *core.ReviewCheckEvidence) { e.ProjectReceipt.ContractSHA256 = strings.Repeat("f", 64) },
		"wrong-candidate": func(e *core.ReviewCheckEvidence) {
			e.ProjectReceipt.CandidateSHA, e.Proof.CandidateSHA = forty("e"), forty("e")
		},
		"unapproved-command": func(e *core.ReviewCheckEvidence) {
			e.ProjectReceipt.Argv, e.Proof.Argv = []string{"node", "skip-tests.js"}, []string{"node", "skip-tests.js"}
		},
		"wrong-timeout": func(e *core.ReviewCheckEvidence) { e.ProjectReceipt.TimeoutSeconds = 1 },
		"missing-dependency-lock": func(e *core.ReviewCheckEvidence) {
			if fresh {
				e.ProjectReceipt.PackageLockSHA256 = "invalid"
			} else {
				e.ProjectReceipt.PackageLockSHA256 = ""
			}
		},
		"missing-environment": func(e *core.ReviewCheckEvidence) { e.ProjectReceipt.CheckEnvironmentID, e.Proof.EnvironmentID = "", "" },
		"false-exit-pass":     func(e *core.ReviewCheckEvidence) { e.ProjectReceipt.ExitCode, e.Proof.ExitCode = 3, 3 },
		"timeout-pass":        func(e *core.ReviewCheckEvidence) { e.ProjectReceipt.TimedOut, e.Proof.TimedOut = true, true },
		"truncated-pass":      func(e *core.ReviewCheckEvidence) { e.ProjectReceipt.OutputTruncated, e.Proof.Truncated = true, true },
		"fake-output":         func(e *core.ReviewCheckEvidence) { e.Output = "all checks succeeded without execution" },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(evidence)
			var changed core.ReviewCheckEvidence
			if json.Unmarshal(encoded, &changed) != nil {
				t.Fatal("fixture encoding failed")
			}
			mutate(&changed)
			encoded, _ = json.Marshal(changed)
			if _, err := db.ExecContext(ctx, insert, review.ID, check.ID, runID, string(encoded), coreDigest(encoded), evidence.RecordedAt); err == nil {
				t.Fatal("SQL accepted a forged project check that bypassed service validation")
			}
		})
	}
	if err := f.store.RecordClearDevReviewerCheck(ctx, evidence); err != nil {
		t.Fatalf("valid trusted receipt rejected after fault injection: %v", err)
	}
	encoded, _ := json.Marshal(evidence)
	if _, err := db.ExecContext(ctx, strings.Replace(insert, "INSERT INTO", "INSERT OR REPLACE INTO", 1), review.ID, check.ID, runID, string(encoded), coreDigest(encoded), evidence.RecordedAt); err == nil {
		t.Fatal("INSERT OR REPLACE bypassed the append-only evidence ledger")
	}
	if _, err := db.ExecContext(ctx, "UPDATE cleardev_review_check_results SET result_json=result_json WHERE review_id=?", review.ID); err == nil {
		t.Fatal("evidence UPDATE was accepted")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM cleardev_review_check_results WHERE review_id=?", review.ID); err == nil {
		t.Fatal("evidence DELETE was accepted")
	}
	completed := driveProjectFlow(t, f, child.Requirement.ID, false)
	if completed.Run.CompletedAt == nil || len(h.requests) != 1 {
		t.Fatal("resuming from the stored receipt re-executed a supplemental check or failed to finalize")
	}
}
