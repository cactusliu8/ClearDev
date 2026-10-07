package cleardev_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type controlledProposalPause struct {
	fixedClaimPause
	before, after, claimed, resulted func()
}

func (s *controlledProposalPause) RecordClearDevComplexExceptionRecovery(ctx context.Context, p core.ComplexRecoveryAction) error {
	if s.before != nil {
		s.before()
	}
	err := s.Store.RecordClearDevComplexExceptionRecovery(ctx, p)
	if err == nil && s.after != nil {
		s.after()
	}
	return err
}

func (s *controlledProposalPause) ClaimClearDevFixedRecoveryAction(ctx context.Context, c core.FixedRecoveryClaim) (bool, error) {
	ok, err := s.Store.ClaimClearDevFixedRecoveryAction(ctx, c)
	if ok && err == nil && s.claimed != nil {
		s.claimed()
	}
	return ok, err
}
func (s *controlledProposalPause) RecordClearDevFixedRecoveryResult(ctx context.Context, r core.FixedRecoveryResult) error {
	err := s.Store.RecordClearDevFixedRecoveryResult(ctx, r)
	if err == nil && s.resulted != nil {
		s.resulted()
	}
	return err
}

func TestClearDevControlledProgressLegalProposalOwnerAndHash(t *testing.T) {
	for _, tc := range []struct{ mode, action string }{{"STANDARD", core.ComplexRecoveryActionRestoreSession}, {"PARALLEL", core.ComplexRecoveryActionRebuildReviewer}} {
		t.Run(tc.mode, func(t *testing.T) {
			store, h, deps, id, data := fixedRecoveryFlowDeps(t, tc.mode, tc.action)
			pause := &controlledProposalPause{fixedClaimPause: fixedClaimPause{store}}
			deps.ComplexExecutionFacts = pause
			svc := cleardevsvc.New(deps)
			var before core.TrustedProgressSummary
			read := func() core.TrustedProgressSummary {
				t.Helper()
				db, err := sql.Open("sqlite", "file:"+filepath.Join(data, "ao.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				var prior, next int64
				if err = db.QueryRow("SELECT count(*) FROM change_log").Scan(&prior); err != nil {
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
					t.Fatal("project/detail mismatch")
				}
				controlledProgressHTTPCLI(t, svc, id, view.Requirement.AOProjectID, view.TrustedProgress.FactSummarySHA256)
				if err = db.QueryRow("SELECT count(*) FROM change_log").Scan(&next); err != nil {
					t.Fatal(err)
				}
				if prior != next || sends != h.sends || creates != h.actualCreates || restores != h.restores {
					t.Fatal("read performed write/action")
				}
				return view.TrustedProgress
			}
			var proposalHash, claimHash string
			var savedNote core.ProgressExplanationRequest
			pause.before = func() {
				before = read()
				// Seed an existing explanation through real storage, without sending a model message.
				ctx := context.Background()
				now := time.Now().UTC()
				note := core.ProgressExplanationRequest{ID: "proposal-note", DevelopmentRequirementID: id, FactSummarySHA256: before.FactSummarySHA256, SourceStewardSessionID: "stored-steward", SessionCreationIdempotencyKey: "proposal-note-session", ClientMessageID: "proposal-note-message", PromptText: "old prompt", PromptSHA256: before.FactSummarySHA256, CreatedAt: now}
				if _, _, err := store.OccupyClearDevProgressExplanation(ctx, core.OccupyProgressExplanationCommand{Request: note}); err != nil {
					t.Fatal(err)
				}
				if ok, err := store.BindClearDevProgressExplanationSession(ctx, note.ID, "stored-session", ""); err != nil || !ok {
					t.Fatalf("seed binding: %v", err)
				}
				if ok, err := store.MarkClearDevProgressExplanationSent(ctx, note.ID, now); err != nil || !ok {
					t.Fatalf("seed sent: %v", err)
				}
				if ok, err := store.SettleClearDevProgressExplanation(ctx, note.ID, `{"summary":"waiting for Recovery"}`, before.FactSummarySHA256, now); err != nil || !ok {
					t.Fatalf("seed result: %v", err)
				}
				var err error
				savedNote, _, err = store.GetClearDevProgressExplanation(ctx, note.ID)
				if err != nil {
					t.Fatal(err)
				}
				before = read()
				if before.Explanation == nil || before.Explanation.Stale {
					t.Fatal("old explanation should start current")
				}
				if before.NextOwner.Role != core.TrustedOwnerRecovery {
					t.Fatal("missing proposal must still need Recovery")
				}
			}
			checkState := func(state string) string {
				t.Helper()
				v := read()
				for _, w := range v.ControlledWork {
					if w.RecoveryRequestID != "" {
						if w.State != state || w.AttemptNumber == nil || *w.AttemptNumber != 1 {
							t.Fatalf("want %s with original attempt: %+v", state, w)
						}
						if v.Phase == core.TrustedPhaseCompleted {
							t.Fatal("recovery bypassed completion")
						}
						if read().FactSummarySHA256 != v.FactSummarySHA256 {
							t.Fatal("repeat read changed hash")
						}
						return v.FactSummarySHA256
					}
				}
				t.Fatal("missing recovery target")
				return ""
			}
			pause.claimed = func() {
				claimHash = checkState("RECOVERY_UNCONFIRMED")
				if claimHash == proposalHash {
					t.Fatal("claim boundary hash unchanged")
				}
			}
			pause.resulted = func() {
				hash := checkState("RETRY_ELIGIBLE")
				if hash == claimHash {
					t.Fatal("result boundary hash unchanged")
				}
			}

			checked := false
			pause.after = func() {
				checked = true
				e, ok, err := store.GetClearDevComplexExecution(context.Background(), id)
				if err != nil || !ok || len(e.FixedRecoveries) != 1 || len(e.Exception.RecoveryActions) != 1 {
					t.Fatalf("missing persisted facts: %v", err)
				}
				r, p := e.FixedRecoveries[0], e.Exception.RecoveryActions[0]
				if r.Claim != nil || r.Result != nil || p.Outcome != "PASS" || p.TriggerFactID != r.Request.FailureEventID {
					t.Fatal("invalid proposal checkpoint")
				}
				after := read()
				storedNote, _, err := store.GetClearDevProgressExplanation(context.Background(), savedNote.ID)
				if err != nil || !reflect.DeepEqual(savedNote, storedNote) || after.Explanation == nil || !after.Explanation.Stale {
					t.Fatal("proposal must stale the unchanged stored explanation")
				}

				proposalHash = checkState("RECOVERY_PENDING")
				t.Logf("persisted request=%s failure=%s proposal=%s trigger=%s outcome=%s claim=nil result=nil; actual creates=%d sends=%d restores=%d; global owner=%+v; hash changed=%v", r.Request.ID, r.Request.FailureEventID, p.ID, p.TriggerFactID, p.Outcome, h.actualCreates, h.sends, h.restores, after.NextOwner, before.FactSummarySHA256 != after.FactSummarySHA256)
				found := false
				for _, w := range after.ControlledWork {
					if w.RecoveryRequestID != r.Request.ID {
						continue
					}
					found = true
					t.Logf("selected work state=%s owner=%+v", w.State, w.NextOwner)
					if w.State != "RECOVERY_PENDING" || w.NextOwner.Role != core.TrustedOwnerControlPlane || w.NextOwner.Action != core.TrustedActionCompleteRecovery {
						t.Errorf("legal persisted proposal must hand next action to CONTROL_PLANE/COMPLETE_RECOVERY; got %s %+v", w.State, w.NextOwner)
					}
				}
				if !found {
					t.Error("fixed recovery target missing")
				}
				if before.FactSummarySHA256 == after.FactSummarySHA256 {
					t.Error("legal proposal changed actionable owner boundary but business hash is unchanged")
				}
			}
			if _, err := svc.StartComplexStandardExecution(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if !checked || claimHash == "" {
				t.Fatal("proposal checkpoint not reached")
			}
		})
	}
}
