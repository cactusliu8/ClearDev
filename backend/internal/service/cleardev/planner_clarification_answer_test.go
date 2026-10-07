package cleardev

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	sqlitestore "github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

const plannerAnswerQuestion = `{"schemaVersion":2,"kind":"PRODUCT_CLARIFICATION_REQUIRED","summary":"Clarify the expected note ordering within the confirmed Stage.","questions":["Should notes appear newest first?"]}`

func plannerAnswerFixture(t *testing.T) (*projectPlanningFixture, RequirementView) {
	t.Helper()
	f := newProjectPlanningFixture(t, "EMPTY")
	product := f.choose(t, f.create(t), "empty")
	_, child := f.prepare(t, product)
	f.h.replies = append(f.h.replies, plannerAnswerQuestion)
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	persistPlannerAnswerNativeSnapshot(t, f, child.Requirement.ID)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if child.ComplexPlanning.Phase != core.ComplexPlanningNeedsHuman || child.ComplexPlanning.PlannerClarification == nil || !child.ComplexPlanning.PlannerClarification.CanAnswer {
		t.Fatalf("missing answerable question: %+v", child.ComplexPlanning.PlannerClarification)
	}
	return f, child
}

// The provider is a double. Persist its native conversation through normal AO
// store APIs, so admission actually checks turn/branch/messages in SQLite.
func persistPlannerAnswerNativeSnapshot(t *testing.T, f *projectPlanningFixture, id string) {
	t.Helper()
	ctx := context.Background()
	planning, _, err := f.store.GetClearDevComplexPlanning(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := core.ComplexRoleBindingByRole(planning, core.StandardRoleEngineeringPlanner)
	record, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Metadata.ProviderConversationID = "explicit-planner-answer-native"
	record.Activity.State = domain.ActivityIdle
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	conversation, err := f.store.CreateConversation(ctx, "planner-answer-conversation", domain.ConversationScopeSession, record.ProjectID, record.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.h.Snapshot(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range snapshot.Turns {
		var user, final domain.ConversationMessage
		for _, message := range snapshot.Messages {
			if message.TurnID == turn.ID {
				if message.Role == domain.MessageRoleUser {
					user = message
				}
				if message.Role == domain.MessageRoleAssistant {
					final = message
				}
			}
		}
		created, err := f.store.AppendUserMessage(ctx, conversation.ID, record.ID, "explicit-test-generation", user, turn.ID, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if !created {
			continue
		}
		if err := f.store.BindTurnToProvider(ctx, turn.ID, turn.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := f.store.SettleAssistantMessage(ctx, conversation.ID, final.ID, turn.ID, final.Text, final.ID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := f.store.SettleTurnByID(ctx, turn.ID, domain.TurnStateCompleted, "", time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
}

func plannerAnswerInput(view RequirementView) SubmitPlannerClarificationsInput {
	target := view.ComplexPlanning.PlannerClarification
	return SubmitPlannerClarificationsInput{RequestID: "original-planner-answer", PlanID: target.PlanID, PlanSHA256: target.PlanSHA256, Answers: []string{"Yes, show the newest saved note first; retain the confirmed persistence and scope."}}
}

func TestPlannerAnswersContinueOriginalPlanAcrossRestart(t *testing.T) {
	ctx := context.Background()
	f, before := plannerAnswerFixture(t)
	input := plannerAnswerInput(before)
	calls := len(f.h.relays)
	f.s.runBackground = func(func()) {}
	registered, err := f.s.SubmitPlannerClarifications(ctx, before.Requirement.ID, input)
	if err != nil || len(registered.ComplexPlanning.PlannerAnswerHistory) != 1 || len(f.h.relays) != calls {
		t.Fatalf("registration changed dispatch: %v %+v", err, registered.ComplexPlanning)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	loaded := mustGetComplex(t, f.s, before.Requirement.ID)
	if loaded.ComplexPlanning.PlannerClarification.Answer.RequestID != input.RequestID || !loaded.ComplexPlanning.PlannerClarification.CanAnswer || len(f.h.relays) != calls {
		t.Fatal("restart lost original request or sent during GET")
	}
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, before.RequirementVersions[0]))
	after, err := f.s.SubmitPlannerClarifications(ctx, before.Requirement.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.h.relays) != calls+1 || after.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned || len(after.ComplexPlanning.Plans) != 2 || after.ComplexExecution != nil {
		t.Fatalf("answers did not continue planning only: %v %+v sends=%d", err, after.ComplexPlanning, len(f.h.relays)-calls)
	}
	if !reflect.DeepEqual(before.RequirementVersions, after.RequirementVersions) || !reflect.DeepEqual(before.ComplexPlanning.Plans[0], after.ComplexPlanning.Plans[0]) || !reflect.DeepEqual(before.ComplexPlanning.Compilations, after.ComplexPlanning.Compilations) {
		t.Fatal("answers rewrote original confirmed facts")
	}
	if f.h.relays[calls].sessionID != f.h.relays[calls-1].sessionID || !strings.Contains(f.h.relays[calls].prompt, input.Answers[0]) || !strings.Contains(f.h.relays[calls].prompt, before.RequirementVersions[0].RequirementText) {
		t.Fatal("continuation changed original Planner or omitted immutable context")
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	if err := f.s.ResumeComplexFlows(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SubmitPlannerClarifications(ctx, before.Requirement.ID, input); err != nil || len(f.h.relays) != calls+1 {
		t.Fatalf("lost response replay sent twice: %v", err)
	}
}

func TestPlannerAnswersConcurrentRequestsKeepOneRegistration(t *testing.T) {
	f, view := plannerAnswerFixture(t)
	f.s.runBackground = func(func()) {}
	inputs := []SubmitPlannerClarificationsInput{plannerAnswerInput(view), plannerAnswerInput(view)}
	inputs[1].RequestID = "competing-planner-answer"
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, input := range inputs {
		wg.Add(1)
		go func(input SubmitPlannerClarificationsInput) {
			defer wg.Done()
			_, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input)
			errs <- err
		}(input)
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	planning, _, err := f.store.GetClearDevComplexPlanning(context.Background(), view.Requirement.ID)
	if err != nil || successes != 1 || len(planning.PlannerAnswers) != 1 {
		t.Fatalf("competing answers admitted: successes=%d answers=%d err=%v", successes, len(planning.PlannerAnswers), err)
	}
}

func TestPlannerAnswersRejectInvalidStaleAndConflictingRequests(t *testing.T) {
	for _, kind := range []string{"blank", "missing", "wrong hash", "changed source", "native conversation changed", "cancelled", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			f, view := plannerAnswerFixture(t)
			f.s.runBackground = func(func()) {}
			input := plannerAnswerInput(view)
			calls := len(f.h.relays)
			switch kind {
			case "blank":
				input.Answers = []string{" "}
			case "missing":
				input.Answers = nil
			case "wrong hash":
				input.PlanSHA256 = strings.Repeat("f", 64)
			case "changed source":
				f.h.source.BaseCommitSHA = forty("b")
			case "native conversation changed":
				binding, _ := core.ComplexRoleBindingByRole(core.ComplexPlanningSnapshot{RoleBindings: view.ComplexPlanning.RoleBindings}, core.StandardRoleEngineeringPlanner)
				record, _, _ := f.store.GetSession(context.Background(), domain.SessionID(binding.AOSessionID))
				record.Metadata.ProviderConversationID = "another-conversation"
				if err := f.store.UpdateSession(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				if err := f.s.CancelRequirement(context.Background(), view.Requirement.ID, "Stop"); err != nil {
					t.Fatal(err)
				}
			case "conflict":
				if _, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input); err != nil {
					t.Fatal(err)
				}
				input.Answers = []string{"different answer"}
			}
			if _, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input); err == nil {
				t.Fatal("unsafe answer was accepted")
			}
			if len(f.h.relays) != calls {
				t.Fatal("rejected answer sent a message")
			}
		})
	}
}

func TestPlannerAnswersLimitAndSourceChangeAfterRegistration(t *testing.T) {
	t.Run("limit", func(t *testing.T) {
		f, view := plannerAnswerFixture(t)
		for round := 0; round < core.MaxPlannerAnswerContinuations; round++ {
			input := plannerAnswerInput(view)
			input.RequestID += strings.Repeat("-next", round)
			f.h.replies = append(f.h.replies, plannerAnswerQuestion)
			if _, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input); err != nil {
				t.Fatal(err)
			}
			persistPlannerAnswerNativeSnapshot(t, f, view.Requirement.ID)
			view = mustGetComplex(t, f.s, view.Requirement.ID)
		}
		if view.ComplexPlanning.PlannerClarification.CanAnswer || view.ComplexPlanning.PlannerClarification.ReasonCode != "PLANNER_ANSWER_LIMIT_REACHED" || len(view.ComplexPlanning.PlannerAnswerHistory) != 2 {
			t.Fatal("answer limit was reset")
		}
		input := plannerAnswerInput(view)
		input.RequestID = "forbidden-third-answer"
		if _, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input); err == nil {
			t.Fatal("third continuation accepted")
		}
	})
	t.Run("registered source changed", func(t *testing.T) {
		f, view := plannerAnswerFixture(t)
		f.s.runBackground = func(func()) {}
		if _, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, plannerAnswerInput(view)); err != nil {
			t.Fatal(err)
		}
		calls := len(f.h.relays)
		f.h.source.BaseCommitSHA = forty("b")
		f.s.runBackground = func(run func()) { run() }
		if err := f.s.ResumeComplexFlows(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(f.h.relays) != calls {
			t.Fatal("stale source sent registered answer")
		}
		planning, _, _ := f.store.GetClearDevComplexPlanning(context.Background(), view.Requirement.ID)
		if len(planning.PlannerAnswers) != 1 {
			t.Fatal("source failure discarded answers")
		}
	})
}

func TestPlannerAnswersPreflightFailureKeepsOriginalRequest(t *testing.T) {
	f, view := plannerAnswerFixture(t)
	calls := len(f.h.relays)
	input := plannerAnswerInput(view)
	f.s.preflightChecker = recheckPreflightError{err: ports.ErrChatAuthRequired}
	stopped, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input)
	if err != nil || len(f.h.relays) != calls || stopped.ComplexPlanning.PlannerClarification.Answer == nil {
		t.Fatalf("preflight failure discarded registration or sent: %v", err)
	}
	if stopped.ComplexPlanning.PlannerClarification.ReasonCode != "LOGIN_REQUIRED" {
		t.Fatalf("preflight cause missing: %+v", stopped.ComplexPlanning.PlannerClarification)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	f.service()
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, view.RequirementVersions[0]))
	after, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input)
	if err != nil || len(f.h.relays) != calls+1 || after.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned || len(after.ComplexPlanning.PlannerAnswerHistory) != 1 {
		t.Fatalf("fixed preflight did not continue original registered request: %v %+v", err, after.ComplexPlanning)
	}
}

func TestPlannerAnswersHistoryAndBudgetCannotBeErased(t *testing.T) {
	f, view := plannerAnswerFixture(t)
	f.s.runBackground = func(func()) {}
	input := plannerAnswerInput(view)
	if _, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, query := range []string{`UPDATE cleardev_planner_answers SET answers_json='["changed"]' WHERE request_id=?`, `DELETE FROM cleardev_planner_answers WHERE request_id=?`, `INSERT OR REPLACE INTO cleardev_planner_answers SELECT request_id,development_project_id,plan_id,plan_sha256,'["changed"]',answers_sha256,next_planning_request_id,created_at FROM cleardev_planner_answers WHERE request_id=?`} {
		if _, err := db.Exec(query, input.RequestID); err == nil {
			t.Fatal("Planner answer history could be erased")
		}
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var originalVersion int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&originalVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(context.Background(), 181); err == nil {
		t.Fatal("downgrade erased Planner answers")
	}
	var version int
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != originalVersion {
		t.Fatal("refused downgrade changed version")
	}
}

type plannerAnswerReservationRace struct {
	AgentAttemptStore
	mutate func()
}

func (r *plannerAnswerReservationRace) ReserveClearDevAgentMessage(ctx context.Context, command core.ReserveAgentMessageCommand) (bool, error) {
	if r.mutate != nil {
		mutate := r.mutate
		r.mutate = nil
		mutate()
	}
	return r.AgentAttemptStore.ReserveClearDevAgentMessage(ctx, command)
}

func TestPlannerAnswersReservationRechecksOriginalNativeIdentity(t *testing.T) {
	f, view := plannerAnswerFixture(t)
	calls := len(f.h.relays)
	input := plannerAnswerInput(view)
	binding, _ := core.ComplexRoleBindingByRole(core.ComplexPlanningSnapshot{RoleBindings: view.ComplexPlanning.RoleBindings}, core.StandardRoleEngineeringPlanner)
	original, _, err := f.store.GetSession(context.Background(), domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	f.s.attempts = &plannerAnswerReservationRace{AgentAttemptStore: f.store, mutate: func() {
		changed := original
		changed.Metadata.ProviderConversationID = "different-after-preflight"
		if err := f.store.UpdateSession(context.Background(), changed); err != nil {
			t.Fatal(err)
		}
	}}
	stopped, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input)
	if err != nil || len(f.h.relays) != calls || len(stopped.ComplexPlanning.PlannerAnswerHistory) != 1 {
		t.Fatalf("reservation accepted replaced conversation or erased registration: %v", err)
	}
	if err := f.store.UpdateSession(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, view.RequirementVersions[0]))
	after, err := f.s.SubmitPlannerClarifications(context.Background(), view.Requirement.ID, input)
	if err != nil || len(f.h.relays) != calls+1 || after.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned {
		t.Fatalf("restored original identity did not continue original unsent attempt: %v", err)
	}
}

// Simulate process loss after the real message reservation and confirmed send,
// with or without the local step's sent marker committed before the loss.
type plannerAnswerSentMarkerCrash struct {
	*sqlitestore.Store
	persistMarker bool
	fired         bool
}

func (c *plannerAnswerSentMarkerCrash) MarkClearDevComplexAgentStepSent(ctx context.Context, id string, at time.Time) (bool, error) {
	if !c.fired {
		c.fired = true
		if c.persistMarker {
			if _, err := c.Store.MarkClearDevComplexAgentStepSent(ctx, id, at); err != nil {
				return false, err
			}
		}
		return false, errors.New("explicit process loss after confirmed Planner send")
	}
	return c.Store.MarkClearDevComplexAgentStepSent(ctx, id, at)
}

type plannerAnswerRunningSnapshot struct {
	*projectPlanningAgent
	turnID string
	calls  int
}

func (c *plannerAnswerRunningSnapshot) Snapshot(ctx context.Context, id domain.SessionID) (chatsvc.Snapshot, error) {
	snapshot, err := c.projectPlanningAgent.Snapshot(ctx, id)
	c.calls++
	if c.calls == 1 {
		for i := range snapshot.Turns {
			if snapshot.Turns[i].ID == c.turnID {
				snapshot.Turns[i].State = domain.TurnStateRunning
				snapshot.Turns[i].CompletedAt = nil
			}
		}
		messages := make([]domain.ConversationMessage, 0, len(snapshot.Messages))
		for _, message := range snapshot.Messages {
			if message.TurnID != c.turnID || message.Role != domain.MessageRoleAssistant {
				messages = append(messages, message)
			}
		}
		snapshot.Messages = messages
	}
	return snapshot, err
}

func TestPlannerAnswersResumeSentTurnWithoutResending(t *testing.T) {
	for _, test := range []struct {
		name          string
		persistMarker bool
		running       bool
	}{
		{"lost marker, running turn", false, true},
		{"saved marker, running turn", true, true},
		{"lost marker, completed turn", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			f, before := plannerAnswerFixture(t)
			calls := len(f.h.relays)
			crash := &plannerAnswerSentMarkerCrash{Store: f.store, persistMarker: test.persistMarker}
			f.s.complex = crash
			f.h.replies = append(f.h.replies, genericEngineeringReply(t, before.RequirementVersions[0]))
			if _, err := f.s.SubmitPlannerClarifications(ctx, before.Requirement.ID, plannerAnswerInput(before)); err != nil {
				t.Fatal(err)
			}
			if !crash.fired || len(f.h.relays) != calls+1 {
				t.Fatal("fixture did not interrupt after the real confirmed send")
			}
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			f.store, err = sqlite.Open(f.dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.store.Close() })
			f.service()
			var pending *plannerAnswerRunningSnapshot
			if test.running {
				last := f.h.relays[len(f.h.relays)-1]
				pending = &plannerAnswerRunningSnapshot{projectPlanningAgent: f.h, turnID: f.h.turnByClientMessageID[last.clientMessageID]}
				f.s.chat = pending
			}
			if err := f.s.ResumeComplexFlows(ctx); err != nil {
				t.Fatal(err)
			}
			after := mustGetComplex(t, f.s, before.Requirement.ID)
			if len(after.ComplexPlanning.Plans) != 2 || after.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned || after.ComplexExecution != nil {
				t.Fatalf("restart did not observe the original sent turn: plans=%d phase=%s sends=%d", len(after.ComplexPlanning.Plans), after.ComplexPlanning.Phase, len(f.h.relays)-calls)
			}
			if len(f.h.relays) != calls+1 || len(after.ComplexPlanning.PlannerAnswerHistory) != 1 || !reflect.DeepEqual(before.ComplexPlanning.Plans[0], after.ComplexPlanning.Plans[0]) || !reflect.DeepEqual(before.RequirementVersions, after.RequirementVersions) {
				t.Fatal("restart resent answers or changed original facts")
			}
			if pending != nil && pending.calls < 2 {
				t.Fatal("restart did not wait for the running successor to finish")
			}
		})
	}
}

func TestPlannerAnswersNewMessageStillRequiresIdleSession(t *testing.T) {
	ctx := context.Background()
	f, before := plannerAnswerFixture(t)
	calls := len(f.h.relays)
	input := plannerAnswerInput(before)
	f.s.runBackground = func(func()) {}
	if _, err := f.s.SubmitPlannerClarifications(ctx, before.Requirement.ID, input); err != nil {
		t.Fatal(err)
	}
	// Dropping the fixture's scheduled callback simulates a stopped process;
	// recreate its service so no in-memory running latch survives that loss.
	f.service()
	binding, _ := core.ComplexRoleBindingByRole(core.ComplexPlanningSnapshot{RoleBindings: before.ComplexPlanning.RoleBindings}, core.StandardRoleEngineeringPlanner)
	record, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Activity.State = domain.ActivityActive
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ResumeComplexFlows(ctx); err != nil {
		t.Fatal(err)
	}
	stopped := mustGetComplex(t, f.s, before.Requirement.ID)
	if len(f.h.relays) != calls || len(stopped.ComplexPlanning.PlannerAnswerHistory) != 1 {
		t.Fatal("busy session received a new message or lost registered answers")
	}
	record.Activity.State = domain.ActivityIdle
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, before.RequirementVersions[0]))
	after, err := f.s.SubmitPlannerClarifications(ctx, before.Requirement.ID, input)
	if err != nil || len(f.h.relays) != calls+1 || after.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned {
		t.Fatalf("idle original session did not continue the original request: %v", err)
	}
}
