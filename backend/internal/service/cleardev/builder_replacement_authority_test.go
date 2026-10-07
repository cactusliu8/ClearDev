package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// A failed stop observation is unknown, even when the stored session is exited.
// All other session behavior still comes from the existing explicit fixture.
type builderReplacementAuthorityUnknownStop struct{ *builderReplacementHarness }

func (h builderReplacementAuthorityUnknownStop) ObserveBuilderHandoffSession(context.Context, domain.SessionID) (ports.BuilderHandoffSessionObservation, error) {
	h.lossCalls++
	return ports.BuilderHandoffSessionObservation{}, errors.New("explicit fixture: stopped observation unavailable")
}

type builderReplacementAuthorityPreflightCounter struct {
	ControlledPreflightChecker
	calls int
}

func (c *builderReplacementAuthorityPreflightCounter) CheckControlledPreflight(ctx context.Context, harness domain.AgentHarness, model string) (ports.ChatControlledPreflight, error) {
	c.calls++
	return c.ControlledPreflightChecker.CheckControlledPreflight(ctx, harness, model)
}

type builderReplacementAuthoritySnapshotCounter struct {
	*builderReplacementHarness
	verifyCalls int
}

func (c *builderReplacementAuthoritySnapshotCounter) VerifyBuilderHandoff(ctx context.Context, snapshot ports.ClearDevBuilderHandoffSnapshot) error {
	c.verifyCalls++
	return c.builderReplacementHarness.VerifyBuilderHandoff(ctx, snapshot)
}

// These facts exclude the decision/grant rows that a legitimate human result
// settles. They include actual role/session/attempt/message/budget evidence.
func builderReplacementAuthorityEffects(t *testing.T, f *projectPlanningFixture, h *builderReplacementHarness, db *sql.DB, id string) map[string]any {
	t.Helper()
	ctx := context.Background()
	execution, found, err := f.store.GetClearDevComplexExecution(ctx, id)
	if err != nil || !found {
		t.Fatal("execution unavailable", err)
	}
	attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := f.store.GetClearDevMessageBudget(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	code, err := os.ReadFile(filepath.Join(h.oldWorkspace, "src", "storage.ts"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"roleBindings": execution.RoleBindings, "dispatches": execution.Dispatches, "agentSteps": execution.AgentSteps,
		"attempts": attempts, "budget": budget, "sessionCount": sessions, "spawnCalls": h.spawnCalls,
		"replacementSpawns": h.replacementSpawns, "providerSends": len(h.relays), "originalCode": string(code),
		"originalHead": replacementGit(t, h.oldWorkspace, "rev-parse", "HEAD"), "originalStatus": replacementGit(t, h.oldWorkspace, "status", "--porcelain"),
	}
}

func builderReplacementAuthorityOffer(t *testing.T, f *projectPlanningFixture, decisionID string) core.HumanDecisionOffer {
	t.Helper()
	offer, found, err := f.s.IssueHumanDecisionOffer(context.Background(), f.s.desktopRunID)
	if err != nil || !found || offer.DecisionKind != core.HumanDecisionKindBuilderReplacement || offer.RequestID != decisionID {
		t.Fatalf("exact private offer unavailable: found=%t kind=%s err=%v", found, offer.DecisionKind, err)
	}
	return offer
}

// Test-only substitution; this is never a real human approval or native UI.
func builderReplacementAuthorityResult(offer core.HumanDecisionOffer, decision core.HumanDecisionChoice) core.HumanDecisionResult {
	return core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID,
		RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion,
		Binding: append(json.RawMessage(nil), offer.Binding...), ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: decision,
	}
}

func TestBuilderReplacementAuthorityRejectsChangedSourceAtPrivateBoundaries(t *testing.T) {
	changes := []struct {
		name   string
		change func(*testing.T, *projectPlanningFixture, *builderReplacementHarness)
	}{
		{"original-code-changed", func(t *testing.T, _ *projectPlanningFixture, h *builderReplacementHarness) {
			if err := os.WriteFile(filepath.Join(h.oldWorkspace, "src", "storage.ts"), []byte("export const changed = 'after request';\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}},
		{"original-identity-changed", func(t *testing.T, f *projectPlanningFixture, h *builderReplacementHarness) {
			r, found, err := f.store.GetSession(context.Background(), domain.SessionID(h.oldSessionID))
			if err != nil || !found {
				t.Fatal("original session unavailable", err)
			}
			r.Metadata.ProviderConversationID += "-changed-after-request"
			if err := f.store.UpdateSession(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		}},
		{"project-launch-config-changed", func(t *testing.T, f *projectPlanningFixture, _ *builderReplacementHarness) {
			p, found, err := f.store.GetProject(context.Background(), "notes-project")
			if err != nil || !found {
				t.Fatal("original project unavailable", err)
			}
			p.Config.AgentConfig.Model = "explicit-different-model-after-request"
			if err := f.store.UpsertProject(context.Background(), p); err != nil {
				t.Fatal(err)
			}
		}},
		{"stop-observation-unknown", func(_ *testing.T, f *projectPlanningFixture, h *builderReplacementHarness) {
			f.s.sessions = builderReplacementAuthorityUnknownStop{h}
		}},
	}
	for _, boundary := range []string{"initial-offer", "registered-reopen", "approval-settlement"} {
		for _, change := range changes {
			t.Run(boundary+"/"+change.name, func(t *testing.T) {
				f, h, id := newBuilderReplacementFixture(t)
				ctx := context.Background()
				db := builderReplacementStorageDB(t, f)
				initial := builderReplacementAuthorityEffects(t, f, h, db, id)
				input := builderReplacementInput(t, f, id, "authority-request", core.RecoveryRequestBuilderReplacement)
				if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(initial, builderReplacementAuthorityEffects(t, f, h, db, id)) {
					t.Fatal("request created a worker, attempt, message or budget change")
				}
				state, err := f.s.builderReplacementState(ctx, id)
				if err != nil || state.DecisionRequestID == "" {
					t.Fatal("original replacement decision missing", err)
				}
				var offer core.HumanDecisionOffer
				var reopen ReopenHumanDecisionInput
				if boundary != "initial-offer" {
					offer = builderReplacementAuthorityOffer(t, f, state.DecisionRequestID)
				}
				if boundary == "registered-reopen" {
					if err := f.s.ApplyHumanDecisionResult(ctx, builderReplacementAuthorityResult(offer, core.HumanDecisionLater)); err != nil {
						t.Fatal("synthetic Later was refused", err)
					}
					history, err := f.store.ListClearDevHumanDecisionDispatchHistory(ctx, offer.RequestID)
					if err != nil || len(history) != 1 || history[0].Outcome != core.HumanDecisionDispatchLater {
						t.Fatal("exact Later receipt missing", err)
					}
					reopen = ReopenHumanDecisionInput{RequestID: "authority-reopen", DecisionRequestID: offer.RequestID, ContentSHA256: offer.ContentSHA256, PreviousDispatchID: history[0].ID}
					if _, err := f.s.ReopenHumanDecision(ctx, id, reopen); err != nil {
						t.Fatal("current reopen registration failed", err)
					}
				}
				change.change(t, f, h)
				before := builderReplacementStorageRows(t, db)
				beforeEffects := builderReplacementAuthorityEffects(t, f, h, db, id)
				if boundary == "approval-settlement" {
					if err := f.s.ApplyHumanDecisionResult(ctx, builderReplacementAuthorityResult(offer, core.HumanDecisionApprove)); err == nil {
						t.Fatal("accurate synthetic APPROVE accepted a changed original source")
					}
				} else {
					_, found, err := f.s.IssueHumanDecisionOffer(ctx, f.s.desktopRunID)
					if found {
						t.Fatalf("changed source received a private offer: err=%v", err)
					}
				}
				if boundary == "registered-reopen" {
					// A durable historical receipt is replayable. It cannot cause a
					// new private offer or supersede the changed-source check above.
					if _, err := f.s.ReopenHumanDecision(ctx, id, reopen); err != nil {
						t.Fatal("original display intent receipt was lost", err)
					}
				}
				if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) || !reflect.DeepEqual(beforeEffects, builderReplacementAuthorityEffects(t, f, h, db, id)) {
					t.Fatal("refused authority changed persisted facts, CDC, code, role, attempt, message or budget")
				}
				req, found, err := f.s.humanDecisions.GetClearDevHumanDecisionRequest(ctx, state.DecisionRequestID)
				if err != nil || !found || req.Status != core.HumanDecisionRequestPending || req.Decision != "" {
					t.Fatal("refused authority resolved the original pending decision", err)
				}
				if _, found, err := f.store.GetClearDevHumanDecisionEffect(ctx, state.DecisionRequestID); err != nil || found {
					t.Fatal("refused authority committed a human effect", err)
				}
			})
		}
	}
}

func TestBuilderReplacementAuthorityApproveOnlyRegistersGrant(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	db := builderReplacementStorageDB(t, f)
	input := builderReplacementInput(t, f, id, "authority-approve-request", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	before := builderReplacementAuthorityEffects(t, f, h, db, id)
	approveBuilderReplacementStorage(t, f)
	if !reflect.DeepEqual(before, builderReplacementAuthorityEffects(t, f, h, db, id)) {
		t.Fatal("synthetic approval created a worker, role, attempt, message or budget change")
	}
	rows := builderReplacementStorageRows(t, db)
	if len(rows["cleardev_builder_replacement_grants"]) != 1 || len(rows["cleardev_builder_replacement_handoffs"]) != 0 || len(rows["cleardev_builder_replacement_aliases"]) != 0 || len(rows["cleardev_builder_session_fences"]) != 0 {
		t.Fatal("approval did not create exactly one grant without a handoff, alias or fence")
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.BuilderReplacements) != 1 || view.BuilderReplacements[0].State != "APPROVED_AWAITING_CONTINUE" || view.BuilderReplacements[0].NewAOSessionID != "" || view.BuilderReplacements[0].NewWorkspacePath != "" {
		t.Fatal("approval was presented as an already established worker", err)
	}
}

func TestBuilderReplacementAuthorityRejectCannotContinue(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	db := builderReplacementStorageDB(t, f)
	input := builderReplacementInput(t, f, id, "authority-reject-request", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.builderReplacementState(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	offer := builderReplacementAuthorityOffer(t, f, state.DecisionRequestID)
	beforeEffects := builderReplacementAuthorityEffects(t, f, h, db, id)
	if err := f.s.ApplyHumanDecisionResult(ctx, builderReplacementAuthorityResult(offer, core.HumanDecisionReject)); err != nil {
		t.Fatal("synthetic rejection was refused", err)
	}
	if !reflect.DeepEqual(beforeEffects, builderReplacementAuthorityEffects(t, f, h, db, id)) {
		t.Fatal("rejection created a worker, attempt, message or budget change")
	}
	before := builderReplacementStorageRows(t, db)
	continuation := WorkflowRecoveryInput{RequestID: "authority-rejected-continue", ExecutionRunID: input.ExecutionRunID, Action: core.RecoveryContinueBuilderReplacement, TargetID: input.TargetID, Supplement: ""}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, continuation); err == nil {
		t.Fatal("rejected handoff was continued")
	}
	if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) || !reflect.DeepEqual(beforeEffects, builderReplacementAuthorityEffects(t, f, h, db, id)) || len(before["cleardev_builder_replacement_grants"]) != 0 {
		t.Fatal("rejected continuation changed facts, CDC, roles, attempts, messages or budgets")
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil || len(view.BuilderReplacements) != 1 || view.BuilderReplacements[0].State != "REJECTED" {
		t.Fatal("rejected decision lost its public receipt", err)
	}
}

func TestBuilderReplacementAuthorityPendingGETIsReadOnly(t *testing.T) {
	f, h, id := newBuilderReplacementFixture(t)
	ctx := context.Background()
	input := builderReplacementInput(t, f, id, "authority-read-only-request", core.RecoveryRequestBuilderReplacement)
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, input); err != nil {
		t.Fatal(err)
	}
	db := builderReplacementStorageDB(t, f)
	before := builderReplacementStorageRows(t, db)
	beforeEffects := builderReplacementAuthorityEffects(t, f, h, db, id)
	lossCalls, captureCalls := h.lossCalls, h.captureCalls
	preflight := &builderReplacementAuthorityPreflightCounter{ControlledPreflightChecker: f.s.preflightChecker}
	f.s.preflightChecker = preflight
	snapshot := &builderReplacementAuthoritySnapshotCounter{builderReplacementHarness: h}
	f.s.inspector = snapshot
	for i := 0; i < 3; i++ {
		if _, err := f.s.GetWorkflowRecovery(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.GetHumanDecisionDisplays(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if h.lossCalls != lossCalls || h.captureCalls != captureCalls || preflight.calls != 0 || snapshot.verifyCalls != 0 {
		t.Fatalf("read-only GET performed external admission: lossCalls=%d captures=%d preflights=%d snapshotChecks=%d", h.lossCalls-lossCalls, h.captureCalls-captureCalls, preflight.calls, snapshot.verifyCalls)
	}
	if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) || !reflect.DeepEqual(beforeEffects, builderReplacementAuthorityEffects(t, f, h, db, id)) {
		t.Fatal("pending GET changed persisted rows, CDC, role, attempt, message or budget")
	}
}
