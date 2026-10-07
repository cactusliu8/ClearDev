package cleardev_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestClearDevControlledProgressRecoveryMainPaths(t *testing.T) {
	for _, c := range []struct{ mode, action string }{{"STANDARD", core.ComplexRecoveryActionRestoreSession}, {"PARALLEL", core.ComplexRecoveryActionRebuildReviewer}} {
		t.Run(c.mode, func(t *testing.T) {
			store, h, deps, id, data := fixedRecoveryFlowDeps(t, c.mode, c.action)
			resultReads := &controlledFixedResultRead{Store: store}
			deps.ComplexExecutionFacts = resultReads
			svc := cleardevsvc.New(deps)
			checkpoints := 0
			read := func(expected string) string {
				t.Helper()
				db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "ao.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				var before, after int64
				if err = db.QueryRow("SELECT count(*) FROM change_log").Scan(&before); err != nil {
					t.Fatal(err)
				}
				sends, creates, restores := h.sends, h.actualCreates, h.restores
				view, err := svc.GetRequirement(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				project, err := svc.ListProjectProgress(context.Background(), view.Requirement.AOProjectID)
				if err != nil {
					t.Fatal(err)
				}
				if len(project.Requirements) != 1 || project.Requirements[0].FactSummarySHA256 != view.TrustedProgress.FactSummarySHA256 {
					t.Fatal("project/detail disagree")
				}
				if sends != h.sends || creates != h.actualCreates || restores != h.restores {
					t.Fatal("read caused external action")
				}
				if expected != "" {
					found := false
					for _, w := range view.TrustedProgress.ControlledWork {
						if w.State == expected {
							found = true
						}
					}
					if !found {
						t.Fatalf("want %s, summary=%+v", expected, view.TrustedProgress)
					}
				}
				controlledProgressHTTPCLI(t, svc, id, view.Requirement.AOProjectID, view.TrustedProgress.FactSummarySHA256)
				if err = db.QueryRow("SELECT count(*) FROM change_log").Scan(&after); err != nil {
					t.Fatal(err)
				}
				if before != after {
					t.Fatal("read wrote database facts")
				}
				t.Logf("checkpoint=%s phase=%s work=%+v sends=%d creates=%d restores=%d", expected, view.TrustedProgress.Phase, view.TrustedProgress.ControlledWork, sends, creates, restores)
				return view.TrustedProgress.FactSummarySHA256
			}
			resultReads.after = func() { read("RETRY_ELIGIBLE") }
			reviewCheckpoints := 0
			resultReads.afterReview = func(r core.ReplacementReviewResult) {
				reviewCheckpoints++
				v, err := svc.GetRequirement(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				for _, missing := range v.ComplexExecution.MissingEvidence {
					if missing == "REVIEWER_PASS" {
						t.Fatal("actual replacement PASS still missing in current read projection")
					}
				}
				for _, review := range v.ComplexExecution.Reviews {
					if review.ID == r.OriginalReviewID && review.Verdict != "" {
						t.Fatal("original review was overwritten")
					}
				}
				if v.TrustedProgress.Phase == core.TrustedPhaseCompleted {
					t.Fatal("replacement result bypassed candidate verification/integration")
				}
				read("")
			}

			h.beforeAction = func() { checkpoints++; read("RECOVERY_PENDING"); read("DELIVERY_UNCONFIRMED") }
			if c.action == core.ComplexRecoveryActionRebuildReviewer {
				h.afterReplacementSpawn = func() { read("RECOVERY_UNCONFIRMED") }
			}
			for wake := 0; wake < 6; wake++ {
				if _, err := svc.StartComplexStandardExecution(context.Background(), id); err != nil {
					t.Fatal(err)
				}
				e, _, err := store.GetClearDevComplexExecution(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				if e.Run.CompletedAt != nil {
					break
				}
			}
			if c.action == core.ComplexRecoveryActionRebuildReviewer && reviewCheckpoints != 1 {
				t.Fatal("replacement result checkpoint not executed")
			}
			if checkpoints == 0 {
				t.Fatal("no recovery checkpoint")
			}
			view, err := svc.GetRequirement(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if view.TrustedProgress.Phase != core.TrustedPhaseCompleted || len(view.TrustedProgress.Blockers) > 0 || len(view.TrustedProgress.ControlledWork) > 0 {
				t.Fatalf("history blocks completion: %+v", view.TrustedProgress)
			}
			hash := read("")
			sends, creates, restores := h.sends, h.actualCreates, h.restores
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.Open(data)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			rebindFixedRecoveryDeps(&deps, reopened, h)
			svc = cleardevsvc.New(deps)
			if err := svc.ResumeComplexStandardExecutions(context.Background()); err != nil {
				t.Fatal(err)
			}
			if read("") != hash {
				t.Fatal("reopen changed business hash")
			}
			if sends != h.sends || creates != h.actualCreates || restores != h.restores {
				t.Fatal("replay repeated external action")
			}
		})
	}
}

func TestClearDevControlledProgressUnconfirmedActionAndFinalFailure(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt-missing", true: "second-failed"}[final], func(t *testing.T) {
			store, h, deps, id, _ := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
			if final {
				h.secondFailure = true
			} else {
				deps.ComplexExecutionFacts = fixedResultSaveFailure{store}
			}
			svc := cleardevsvc.New(deps)
			_, _ = svc.StartComplexStandardExecution(context.Background(), id)
			view, err := svc.GetRequirement(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			want := "RECOVERY_UNCONFIRMED"
			if final {
				want = "FAILED"
			}
			found := false
			for _, w := range view.TrustedProgress.ControlledWork {
				if w.State == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s, got %+v", want, view.TrustedProgress)
			}
			if view.TrustedProgress.Phase == core.TrustedPhaseCompleted {
				t.Fatal("unknown/failure completed")
			}
			if !final && view.TrustedProgress.Phase != core.TrustedPhaseRecovering {
				t.Fatalf("unconfirmed phase=%s blockers=%+v", view.TrustedProgress.Phase, view.TrustedProgress.Blockers)
			}
		})
	}
}

type controlledFixedResultRead struct {
	*sqlite.Store
	after       func()
	afterReview func(core.ReplacementReviewResult)
}

func (s *controlledFixedResultRead) RecordClearDevFixedRecoveryResult(ctx context.Context, r core.FixedRecoveryResult) error {
	err := s.Store.RecordClearDevFixedRecoveryResult(ctx, r)
	if err == nil && r.Outcome == "PASS" && s.after != nil {
		s.after()
	}
	return err
}

func TestClearDevControlledProgressFaultReopen(t *testing.T) {
	store, h, deps, id, data := fixedRecoveryFlowDeps(t, "STANDARD", core.ComplexRecoveryActionRestoreSession)
	deps.ComplexExecutionFacts = fixedResultSaveFailure{store}
	svc := cleardevsvc.New(deps)
	_, _ = svc.StartComplexStandardExecution(context.Background(), id)
	before, err := svc.GetRequirement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	sends, creates, restores := h.sends, h.actualCreates, h.restores
	if restores != 1 {
		t.Fatalf("actual restore calls=%d", restores)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	rebindFixedRecoveryDeps(&deps, reopened, h)
	svc = cleardevsvc.New(deps)
	after, err := svc.GetRequirement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if before.TrustedProgress.FactSummarySHA256 != after.TrustedProgress.FactSummarySHA256 {
		t.Fatal("reopening unchanged unknown action changed summary")
	}
	if err = svc.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err = svc.GetRequirement(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range after.TrustedProgress.ControlledWork {
		if w.State == "RECOVERY_UNCONFIRMED" && w.RecoveryOperationID != "" {
			found = true
		}
	}
	if !found || after.TrustedProgress.Phase == core.TrustedPhaseCompleted {
		t.Fatalf("unknown native receipt became a successful action: %+v", after.TrustedProgress)
	}
	if sends != h.sends || creates != h.actualCreates || restores != h.restores {
		t.Fatal("fault reopen repeated actual action or send")
	}
	controlledProgressHTTPCLI(t, svc, id, after.Requirement.AOProjectID, after.TrustedProgress.FactSummarySHA256)
	t.Logf("missing native receipt after reopen remains unknown; sends=%d creates=%d restores=%d", sends, creates, restores)
}

func (s *controlledFixedResultRead) RecordClearDevReplacementReviewResult(ctx context.Context, r core.ReplacementReviewResult) error {
	err := s.Store.RecordClearDevReplacementReviewResult(ctx, r)
	if err == nil && s.afterReview != nil {
		s.afterReview(r)
	}
	return err
}
