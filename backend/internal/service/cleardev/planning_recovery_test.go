package cleardev

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sqlite "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Only the model is fake: native envelopes, attempts, plans and stage facts use SQLite.
type interruptedProductReviewAgent struct {
	*productTestAgent
	interrupt bool
	lastError error
}

func (h *interruptedProductReviewAgent) RelayChatTurnWithID(ctx context.Context, sid domain.SessionID, prompt, client string) (string, error) {
	if strings.Contains(prompt, "kind=COMPLEX_ENGINEERING_PLAN") || strings.Contains(prompt, "kind=COMPLEX_PLAN_REVIEW") {
		turn, err := h.standardAgentHarness.RelayChatTurnWithID(ctx, sid, prompt, client)
		h.lastError = err
		if err == nil && h.interrupt && strings.Contains(prompt, "kind=COMPLEX_PLAN_REVIEW") {
			h.mu.Lock()
			snap := h.snapshots[sid]
			for i := range snap.Turns {
				if snap.Turns[i].ID == turn {
					snap.Turns[i].State = domain.TurnStateFailed
					snap.Turns[i].Failure = &domain.ConversationFailure{Category: domain.AgentFailureTurnInterrupted, ErrorSummary: "explicit test interruption", Retryable: false}
				}
			}
			msgs := snap.Messages[:0]
			for _, m := range snap.Messages {
				if m.TurnID != turn || m.Role != domain.MessageRoleAssistant {
					msgs = append(msgs, m)
				}
			}
			snap.Messages = msgs
			h.snapshots[sid] = snap
			h.mu.Unlock()
		}
		return turn, err
	}
	turn, err := h.productTestAgent.RelayChatTurnWithID(ctx, sid, prompt, client)
	h.lastError = err
	return turn, err
}

func interruptedProductReviewFixture(t *testing.T) (*Service, *interruptedProductReviewAgent, *sqlite.Store, string, string) {
	t.Helper()
	s, h, store := newProductTestService(t, productReadyReply())
	product := createTestProduct(t, s)
	stage := product.Stages[0].Stage
	h.replies = append(h.replies, stageReadyReply(stage.Definition))
	prepared, err := s.PrepareProductStage(context.Background(), product.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256})
	if err != nil {
		t.Fatal(err)
	}
	child := prepared.Stages[0].Stage.DevelopmentRequirementID
	agent := &interruptedProductReviewAgent{productTestAgent: h, interrupt: true}
	s.chat = agent
	h.complexPlanMutator = func(p *core.ComplexEngineeringPlanResult) {
		task := p.Tasks[0]
		task.DependencyKeys = []string{}
		task.WritePaths = []string{"backend/src/**", "frontend/**", "test/**"}
		task.RequiredCheckIDs = []string{"demo-integration"}
		p.Tasks = []core.ComplexPlanTask{task}
		p.IntegrationCheckIDs = []string{"demo-integration"}
		p.ParallelSuggestion.RecommendedBuilderCount = 1
	}
	applyFakeDesktopDecision(t, store, s, s.now, child, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	if err := s.runComplexFlow(context.Background(), child); err != nil {
		t.Fatal(err)
	}
	view := mustGetComplex(t, s, child)
	if len(view.ComplexPlanning.Plans) != 1 || len(view.ComplexPlanning.Reviews) != 0 {
		t.Fatalf("expected interrupted review: model error=%v relays=%d", agent.lastError, len(h.relays))
	}
	stepID := ""
	for _, step := range view.ComplexPlanning.AgentSteps {
		if step.Kind == core.ComplexAgentStepPlanReview && step.SendStatus == core.AgentStepSendStatusFailed {
			stepID = step.ID
		}
	}
	if stepID == "" {
		t.Fatalf("missing interrupted step: %+v", view.ComplexPlanning)
	}
	return s, agent, store, child, stepID
}

func TestPlanningRecoveryRequiresNativeGrantAndPreservesFirstAttempt(t *testing.T) {
	s, h, store, child, step := interruptedProductReviewFixture(t)
	ctx := context.Background()
	before, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 1))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.ResumeComplexFlows(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.BackfillHumanDecisionRequests(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2)); err == nil {
		t.Fatal("resume created attempt without native grant")
	}
	h.interrupt = false
	// Keep the test at the plan boundary; actual Builder execution has separate tests.
	var queued []func()
	s.runBackground = func(run func()) { queued = append(queued, run) }
	applyFakeDesktopDecision(t, store, s, s.now, child, core.HumanDecisionKindPlanningRecovery, core.HumanDecisionApprove)
	second, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2))
	if err != nil {
		t.Fatal(err)
	}
	if second.AOSessionID != before.AOSessionID || second.PromptSHA256 != before.PromptSHA256 || second.AttemptNumber != 2 {
		t.Fatalf("retry lost identity: %+v", second)
	}
	after, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 1))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("original failure changed: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("scheduled %d recovery workers", len(queued))
	}
	queued[0]()
	view := mustGetComplex(t, s, child)
	if len(view.ComplexPlanning.Reviews) != 1 {
		t.Fatalf("authorized review phase=%s steps=%+v error=%v", view.ComplexPlanning.Phase, view.ComplexPlanning.AgentSteps, h.lastError)
	}
	calls := len(h.relays)
	if err := s.ResumeComplexFlows(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.relays) != calls {
		t.Fatal("restart duplicated review")
	}
}

func planningRecoveryOffer(t *testing.T, s *Service, store *sqlite.Store, child string) core.HumanDecisionResult {
	t.Helper()
	ctx := context.Background()
	if err := s.BackfillHumanDecisionRequests(ctx); err != nil {
		t.Fatal(err)
	}
	pending := pendingHumanRequestByKind(t, store, child, core.HumanDecisionKindPlanningRecovery)
	at := s.now()
	offer, err := store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: pending.ID, DesktopRunID: "recovery-test-desktop", Nonce: mustComplexNonce(t), IssuedAt: at, ExpiresAt: at.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	return core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding, ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionApprove}
}

func TestPlanningRecoveryRejectsInvalidAuthorityAndStopsAfterSecondInterruption(t *testing.T) {
	for _, mode := range []string{"nonce", "desktop", "digest", "binding", "reject", "later", "expired", "terminated", "second-interruption"} {
		t.Run(mode, func(t *testing.T) {
			s, h, store, child, step := interruptedProductReviewFixture(t)
			ctx := context.Background()
			result := planningRecoveryOffer(t, s, store, child)
			calls := len(h.relays)
			switch mode {
			case "nonce":
				result.Nonce = mustComplexNonce(t)
			case "desktop":
				result.DesktopRunID = "other-desktop"
			case "digest":
				result.ContentSHA256 = strings.Repeat("f", 64)
			case "binding":
				result.Binding = []byte(strings.Replace(string(result.Binding), step, "other-step", 1))
			case "expired":
				originalClock := s.now
				s.now = func() time.Time { return originalClock().Add(2 * core.HumanDecisionOfferTTL) }
			case "terminated":
				var b core.PlanningRecoveryBinding
				_ = json.Unmarshal(result.Binding, &b)
				rec, _, err := store.GetSession(ctx, domain.SessionID(b.AOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				rec.IsTerminated = true
				if err := store.UpdateSession(ctx, rec); err != nil {
					t.Fatal(err)
				}
			case "reject":
				result.Decision = core.HumanDecisionReject
			case "later":
				result.Decision = core.HumanDecisionLater
			}
			err := s.ApplyHumanDecisionResult(ctx, result)
			invalid := mode == "nonce" || mode == "desktop" || mode == "digest" || mode == "binding" || mode == "expired" || mode == "terminated"
			if invalid && err == nil {
				t.Fatal("invalid authority accepted")
			}
			if !invalid && err != nil {
				t.Fatal(err)
			}
			if mode != "second-interruption" {
				if _, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2)); err == nil {
					t.Fatal("unapproved attempt created")
				}
				if len(h.relays) != calls {
					t.Fatal("unapproved model call")
				}
			} else {
				second, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2))
				if err != nil || second.SendStatus != core.AgentAttemptInterrupted {
					t.Fatalf("second failure not retained: %+v %v", second, err)
				}
				if len(h.relays) != calls+1 {
					t.Fatal("recovery did not send exactly once")
				}
				if err := s.ApplyHumanDecisionResult(ctx, result); err == nil {
					t.Fatal("native result replay accepted")
				}
				calls = len(h.relays)
				for range 3 {
					if err := s.ResumeComplexFlows(ctx); err != nil {
						t.Fatal(err)
					}
					if err := s.BackfillHumanDecisionRequests(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if len(h.relays) != calls {
					t.Fatal("exhausted recovery resent")
				}
				if _, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 3)); err == nil {
					t.Fatal("third attempt created")
				}
				pending, err := store.ListPendingClearDevHumanDecisionRequests(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range pending {
					if p.DecisionKind == core.HumanDecisionKindPlanningRecovery {
						t.Fatal("second recovery offered")
					}
				}
			}
		})
	}
}

func TestPlanningRecoveryConcurrentNativeGrantIsConsumedOnce(t *testing.T) {
	s, _, store, child, step := interruptedProductReviewFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.BackfillHumanDecisionRequests(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	result := planningRecoveryOffer(t, s, store, child)
	var success, scheduled atomic.Int32
	s.runBackground = func(func()) { scheduled.Add(1) }
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.ApplyHumanDecisionResult(ctx, result); err == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 || scheduled.Load() != 1 {
		t.Fatalf("grants=%d workers=%d", success.Load(), scheduled.Load())
	}
	if _, err := store.GetClearDevAgentAttemptState(ctx, child, agentAttemptID(step, 2)); err != nil {
		t.Fatal(err)
	}
}
