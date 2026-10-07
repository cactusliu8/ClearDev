package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func unifiedInvalidDiscussionFixture(t *testing.T) (*projectPlanningFixture, string, core.AgentStep, string) {
	t.Helper()
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "EMPTY")
	f.s.stepTimeout = 10 * time.Second
	product := f.create(t)
	f.h.replies = append(f.h.replies, "invalid original discussion", "invalid discussion correction")
	if _, err := f.s.SubmitProductDiscussion(ctx, product.Goal.ID, projectChoice(product, "empty")); err != nil {
		t.Fatal(err)
	}
	p, _, err := f.store.GetClearDevComplexPlanning(ctx, product.Goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	var step core.AgentStep
	for _, item := range p.AgentSteps {
		if item.SendStatus == core.AgentStepSendStatusFailed && item.ReasonCode == "PRODUCT_DISCOVERY_INVALID" {
			step = item
		}
	}
	if step.ID == "" {
		t.Fatal("fixture did not reach invalid discussion")
	}
	for _, role := range p.RoleBindings {
		if role.ID == step.RoleBindingID {
			r, found, err := f.store.GetSession(ctx, domain.SessionID(role.AOSessionID))
			if err != nil || !found {
				t.Fatal(err)
			}
			r.Metadata.ProviderConversationID = "unified-original-steward-native"
			r.Activity.State = domain.ActivityIdle
			if err := f.store.UpdateSession(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, _, err := f.store.GetClearDevProduct(ctx, product.Goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	discussion := before.Discussions[len(before.Discussions)-1]
	if discussion.FailureReason != "PRODUCT_DISCOVERY_INVALID" {
		t.Fatal("not a terminal invalid discussion")
	}
	return f, product.Goal.ID, step, discussion.ID
}

func TestUnifiedFailureInvalidDiscussionReturnsToOriginalStewardWithoutNewRound(t *testing.T) {
	ctx := context.Background()
	f, id, step, discussionID := unifiedInvalidDiscussionFixture(t)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := gen.New(db)
	snapshot, err := q.GetClearDevFailedDiscussionSnapshot(ctx, discussionID)
	if err != nil {
		t.Fatal(err)
	}
	oldEvidence, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, step.ID)
	if err != nil || len(oldEvidence) != 1 {
		t.Fatal(err)
	}
	oldRaw, _ := json.Marshal(oldEvidence[0])
	before, _, err := f.store.GetClearDevProduct(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, genericProjectReply("empty"))
	f.s.automaticFailureRouting = true
	if err := f.s.runComplexFlow(ctx, id); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevProduct(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	last := after.Discussions[len(after.Discussions)-1]
	if len(after.Discussions) != len(before.Discussions) || last.ID != discussionID || last.FailureReason != "" || last.Result == nil {
		t.Fatalf("discussion not repaired in place: %+v", last)
	}
	state, err := f.store.ReadClearDevPlanningStepRecovery(ctx, id, time.Now().UTC())
	if err != nil || len(state.History) != 1 {
		t.Fatal("missing unique planning recovery", err)
	}
	var saved string
	if err := db.QueryRow(`SELECT old_discussion_json FROM cleardev_planning_step_recoveries WHERE logical_step_id=?`, step.ID).Scan(&saved); err != nil || saved != snapshot {
		t.Fatal("original failed discussion snapshot was not retained", err)
	}
	attempts, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, step.ID)
	if err != nil || len(attempts) != 2 {
		t.Fatal("wrong attempt count", len(attempts), err)
	}
	first, _ := json.Marshal(attempts[0])
	if string(first) != string(oldRaw) {
		t.Fatal("first-attempt reply/parse/delivery evidence changed")
	}
	if attempts[1].PromptSHA256 != attempts[0].PromptSHA256 || attempts[1].AOSessionID != attempts[0].AOSessionID {
		t.Fatal("original prompt or role changed")
	}
	var messages int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_agent_message_reservations WHERE logical_step_id=?`, step.ID).Scan(&messages); err != nil || messages != 3 {
		t.Fatal("unexpected original/correction/retry message budget", messages, err)
	}
	for _, statement := range []string{
		`UPDATE cleardev_product_discussions SET failure_reason=NULL,settled_at=NULL WHERE id=?`,
		`UPDATE cleardev_product_discussions SET user_message='changed' WHERE id=?`,
		`UPDATE cleardev_planning_step_recoveries SET old_discussion_json='{}' WHERE logical_step_id=?`,
	} {
		arg := discussionID
		if strings.Contains(statement, "recoveries") {
			arg = step.ID
		}
		if _, err := db.Exec(statement, arg); err == nil {
			t.Fatal("history or source protection failed", statement)
		}
	}
}

func TestUnifiedFailureInvalidDiscussionSecondFailureDoesNotCreateThirdAttempt(t *testing.T) {
	ctx := context.Background()
	f, id, step, _ := unifiedInvalidDiscussionFixture(t)
	f.h.replies = append(f.h.replies, "still-invalid-second-attempt")
	f.s.automaticFailureRouting = true
	_ = f.s.runComplexFlow(ctx, id)
	before, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, step.ID)
	if err != nil || len(before) != 2 {
		t.Fatal("second attempt never executed", len(before), err)
	}
	calls := len(f.h.relays)
	for range 3 {
		_ = f.s.runComplexFlow(ctx, id)
	}
	after, err := f.store.ListClearDevAgentStepAttemptStates(ctx, id, step.ID)
	if err != nil || !reflect.DeepEqual(before, after) || calls != len(f.h.relays) {
		t.Fatal("repeated failure gained a third attempt/correction", err)
	}
	view, err := f.s.GetWorkflowRecovery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, option := range view.Options {
		if option.Action == core.RecoveryRetryPlanningStep && option.UnavailableReason == "" {
			t.Fatal("exhausted failed discussion remained automatically recoverable")
		}
	}
}
