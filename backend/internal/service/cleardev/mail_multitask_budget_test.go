package cleardev_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

func failMailCandidates(h *parallelAgentHarness, path string, limit int) map[string]bool {
	failed := map[string]bool{}
	h.checkFailureFor = func(request ports.ClearDevCheckRequest) bool {
		workspace := h.workspaces[request.WorkspacePath]
		if workspace == nil || len(workspace.paths) == 0 || workspace.paths[0].Path != path {
			return false
		}
		if failed[request.CandidateSHA] {
			return true
		}
		if len(failed) < limit {
			failed[request.CandidateSHA] = true
			return true
		}
		return false
	}
	return failed
}

func TestMailV2SerialTaskRetryKeepsTaskLocalReviewer(t *testing.T) {
	for _, mode := range []string{"check-failure", "review-rework"} {
		t.Run(mode, func(t *testing.T) {
			f := newMailParallelBoundaryFixtureForPaths(t, true)
			if mode == "check-failure" {
				failMailCandidates(f.harness, "frontend/app.js", 1)
			} else {
				reworked := false
				f.harness.responseOverride = func(_ domain.SessionID, kind, response string) string {
					if kind != "LOCAL_REVIEW" || reworked {
						return response
					}
					reworked = true
					response = strings.Replace(response, `"verdict":"PASS"`, `"verdict":"REWORK"`, 1)
					response = strings.Replace(response, `"reasonCode":"REVIEW_PASSED"`, `"reasonCode":"REVIEW_CHANGES_REQUIRED"`, 1)
					return strings.Replace(response, `"findings":[]`, `"findings":[{"severity":"BLOCKING","path":"frontend/app.js","message":"Repair this exact task candidate."}]`, 1)
				}
			}
			execution := f.advance(t, true, nil)
			phase, reason := core.DeriveComplexExecutionPhase(execution)
			if phase != core.ComplexExecutionCompleted || execution.Run.Mode != core.WorkModeStandard || len(execution.Dispatches) != 4 || len(execution.Verifications) != 3 {
				t.Fatalf("serial retry failed: phase=%s reason=%s dispatches=%+v reviews=%+v", phase, reason, execution.Dispatches, execution.Reviews)
			}
			owners := map[string]string{}
			for _, review := range execution.Reviews {
				for _, binding := range execution.RoleBindings {
					if binding.ID != review.ReviewerRoleBindingID {
						continue
					}
					if prior := owners[binding.AOSessionID]; prior != "" && prior != review.ComplexExecutionTaskID {
						t.Fatal("reviewer continuation crossed task ownership")
					}
					owners[binding.AOSessionID] = review.ComplexExecutionTaskID
				}
			}
			if len(owners) != 3 {
				t.Fatalf("expected one independent reviewer per task, got %d", len(owners))
			}
		})
	}
}

// This is a test-only desktop authority double. It uses the normal issued offer
// and ApplyHumanDecisionResult transactions, never writes SQLite to grant success.
func approveMailExtraForFixture(t *testing.T, f *mailParallelBoundaryFixture) {
	t.Helper()
	ctx := context.Background()
	pending, err := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var request core.HumanDecisionRequest
	for _, item := range pending {
		if item.DevelopmentRequirementID == f.prep.RequirementID && item.DecisionKind == core.HumanDecisionKindExtraMailAttempt {
			request = item
		}
	}
	if request.ID == "" {
		t.Fatal("expected an exact pending human extra attempt offer")
	}
	nonce, err := humanauthority.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	offer, err := f.store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{
		RequestID: request.ID, DesktopRunID: "test-desktop-mail-multitask", Nonce: nonce,
		IssuedAt: f.clock(), ExpiresAt: f.clock().Add(core.HumanDecisionOfferTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := parallelTestServiceWithOptions(f.store, f.harness, func(deps *cleardevsvc.Deps) {
		deps.NewID, deps.Clock, deps.Chat = f.ids.New, f.clock, f.chat
		deps.AutoAdvanceComplexPlans = true
	})
	if err := service.ApplyHumanDecisionResult(ctx, core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...),
		ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMailV2HumanExtraDoesNotConsumeOtherTasksNormalAttempts(t *testing.T) {
	f := newMailParallelBoundaryFixture(t)
	failed := failMailCandidates(f.harness, "frontend/app.js", 3)
	execution := f.advance(t, true, nil)
	phase, _ := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionNeedsHuman || len(failed) != 3 || execution.Integration != nil {
		t.Fatalf("initial automatic task budget did not stop correctly: phase=%s", phase)
	}
	f.reopen(t)
	approveMailExtraForFixture(t, f)
	execution = f.advance(t, false, nil)
	phase, reason := core.DeriveComplexExecutionPhase(execution)
	if phase != core.ComplexExecutionCompleted || len(execution.Dispatches) != 6 || len(execution.Verifications) != 3 || execution.FinalReview == nil || execution.FinalReview.Verdict != "PASS" {
		t.Fatalf("successful extra attempt blocked later normal task: phase=%s reason=%s tasks=%+v dispatches=%+v", phase, reason, execution.Tasks, execution.Dispatches)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), execution.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	byTask := map[string]int{}
	extra := 0
	for _, slot := range slots {
		byTask[slot.TaskID]++
		if slot.Kind == core.MailAttemptHumanExtra {
			extra++
		}
	}
	if extra != 1 {
		t.Fatalf("execution consumed %d human-extra attempts, want exactly one", extra)
	}
	for _, task := range execution.Tasks {
		want := 1
		if task.TaskKey == "frontend-state" {
			want = 4
		}
		if byTask[task.ID] != want {
			t.Fatalf("task %s budget=%d want=%d", task.TaskKey, byTask[task.ID], want)
		}
	}
}
