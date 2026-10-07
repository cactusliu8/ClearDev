package cleardev

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// Provider and Git are explicit doubles; all workflow, message and recovery
// facts go through the real SQLite service and transactions.
type planningStepFailureAgent struct {
	*productTestAgent
	fail bool
}

func (h *planningStepFailureAgent) RelayChatTurnWithID(ctx context.Context, sid domain.SessionID, prompt, client string) (string, error) {
	turn, err := h.productTestAgent.RelayChatTurnWithID(ctx, sid, prompt, client)
	if err != nil || !h.fail {
		return turn, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	snapshot := h.snapshots[sid]
	for i := range snapshot.Turns {
		if snapshot.Turns[i].ID == turn {
			snapshot.Turns[i].State = domain.TurnStateFailed
			snapshot.Turns[i].Failure = &domain.ConversationFailure{Category: domain.AgentFailureProviderUnavailable, ErrorSummary: "explicit terminal provider failure", Retryable: false}
		}
	}
	messages := snapshot.Messages[:0]
	for _, message := range snapshot.Messages {
		if message.TurnID != turn || message.Role != domain.MessageRoleAssistant {
			messages = append(messages, message)
		}
	}
	snapshot.Messages = messages
	h.snapshots[sid] = snapshot
	return turn, nil
}

type planningContinuationFixture struct {
	s       *Service
	h       *planningStepFailureAgent
	store   *sqlitestore.Store
	product string
	id      string
	step    core.AgentStep
	target  string
	reply   string
	dir     string
}

func planningContinuationService(store *sqlitestore.Store, h *planningStepFailureAgent, newID func() string, clock func() time.Time) *Service {
	return New(Deps{
		Facts: store, StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		ParseCorrections: store, AgentAttempts: store, ControlledPreflights: store, ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ProgressExplanations: store, Workspace: gitWorkspaceObserver{},
		RecoverAgentSession: func(context.Context, domain.SessionID) error {
			return errors.New("unexpected planning fixture session recovery")
		},
		AO: store, Sessions: h, Chat: h, Inspector: h, Checks: h, Human: allowStandardHuman{},
		StepTimeout: 10 * time.Second, PollInterval: time.Millisecond, NewID: newID, Clock: clock, BackgroundContext: context.Background(),
		RunBackground: func(run func()) { run() },
	})
}

func newPlanningContinuationFixture(t *testing.T, compilation bool) planningContinuationFixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "s04-project", Path: "/tmp/s04-project", Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	h := &productTestAgent{standardAgentHarness: newStandardAgentHarness(store, false), replies: []string{productReadyReply()}, base: forty("a")}
	agent := &planningStepFailureAgent{productTestAgent: h, fail: !compilation}
	ids := &standardTestIDs{}
	s := planningContinuationService(store, agent, ids.New, time.Now)
	product := createTestProduct(t, s)
	id, reply := product.Goal.ID, productReadyReply()
	if compilation {
		stage := product.Stages[0].Stage
		h.replies = append(h.replies, "invalid compilation", "invalid correction")
		prepared, err := s.PrepareProductStage(ctx, product.Goal.ID, stage.ID, PrepareProductStageInput{DefinitionSHA256: stage.DefinitionSHA256})
		if err != nil {
			t.Fatal(err)
		}
		id, reply = prepared.Stages[0].Stage.DevelopmentRequirementID, stageReadyReply(stage.Definition)
	}
	planning, found, err := store.GetClearDevComplexPlanning(ctx, id)
	if err != nil || !found {
		t.Fatalf("missing planning facts: %v", err)
	}
	var step core.AgentStep
	for _, item := range planning.AgentSteps {
		if item.Kind == core.ComplexAgentStepCompilation && item.SendStatus == core.AgentStepSendStatusFailed {
			step = item
		}
	}
	if step.ID == "" {
		t.Fatalf("fixture did not reach a failed original step: %+v", planning.AgentSteps)
	}
	for _, role := range planning.RoleBindings {
		if role.ID != step.RoleBindingID {
			continue
		}
		record, found, err := store.GetSession(ctx, domain.SessionID(role.AOSessionID))
		if err != nil || !found {
			t.Fatalf("missing bound session: %v", err)
		}
		record.Metadata.ProviderConversationID = "planning-continuation-original-native"
		record.Activity.State = domain.ActivityIdle
		if err := store.UpdateSession(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.ReadClearDevPlanningStepRecovery(ctx, id, s.now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return planningContinuationFixture{s: s, h: agent, store: store, product: product.Goal.ID, id: id, step: step, target: state.Option.TargetID, reply: reply, dir: dir}
}

func (f planningContinuationFixture) input() WorkflowRecoveryInput {
	return WorkflowRecoveryInput{RequestID: "planning-step-retry", Action: "RETRY_PLANNING_STEP", TargetID: f.target}
}

func TestPlanningStepContinuationRetainsOriginalDiscussionAndStage(t *testing.T) {
	for _, compilation := range []bool{false, true} {
		name := "discussion-provider-failure"
		if compilation {
			name = "stage-compilation-invalid-result"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newPlanningContinuationFixture(t, compilation)
			before, _, err := f.store.GetClearDevProduct(ctx, f.product)
			if err != nil {
				t.Fatal(err)
			}
			attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
			if err != nil || len(attempts) != 1 {
				t.Fatalf("expected one original attempt: %v", err)
			}
			old := attempts[0]
			calls := len(f.h.relays)
			originalPrompt := ""
			for _, relay := range f.h.relays {
				if relay.clientMessageID == f.step.ClientMessageID {
					originalPrompt = relay.prompt
				}
			}
			options, err := f.s.GetWorkflowRecovery(ctx, f.id)
			if err != nil || len(options.Options) != 1 || options.Options[0].UnavailableReason != "" || options.Options[0].Action != "RETRY_PLANNING_STEP" {
				t.Fatalf("original planning stop has no usable continuation: %+v %v", options, err)
			}
			if len(f.h.relays) != calls {
				t.Fatal("reading recovery sent a message")
			}
			f.h.fail = false
			f.h.replies = append(f.h.replies, f.reply)
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
				t.Fatal(err)
			}
			if err := f.s.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.h.relays) != calls+1 || !strings.HasSuffix(f.h.relays[calls].clientMessageID, ":attempt:2") || f.h.relays[calls].prompt != originalPrompt {
				t.Fatalf("continuation did not send exactly the original request once: calls=%d -> %d", calls, len(f.h.relays))
			}
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil || len(f.h.relays) != calls+1 {
				t.Fatalf("request replay duplicated the successor: %v", err)
			}
			after, _, err := f.store.GetClearDevProduct(ctx, f.product)
			if err != nil || len(before.Discussions) != len(after.Discussions) || before.Discussions[0].ID != after.Discussions[0].ID || before.Discussions[0].UserMessage != after.Discussions[0].UserMessage {
				t.Fatalf("continuation replaced original discussion: %v", err)
			}
			all, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
			if err != nil || len(all) != 2 || !reflect.DeepEqual(old, all[0]) {
				t.Fatalf("original attempt/evidence changed or budget reset: %v", err)
			}
			if _, found, err := f.store.GetClearDevComplexExecution(ctx, f.id); err != nil || found {
				t.Fatalf("planning continuation started execution: %v", err)
			}
			if compilation {
				child, err := f.s.GetRequirement(ctx, f.id)
				if err != nil || len(child.ComplexPlanning.Compilations) != 1 || child.ComplexPlanning.Compilations[0].Outcome != "READY" {
					t.Fatalf("original compilation did not complete: %+v %v", child.ComplexPlanning, err)
				}
				if after.Stages[0].DevelopmentRequirementID != f.id {
					t.Fatal("stage acquired a replacement requirement")
				}
			} else if after.Discussions[0].Result == nil || after.Discussions[0].Result.Outcome != "READY" {
				t.Fatal("original discussion did not settle")
			}
		})
	}
}
