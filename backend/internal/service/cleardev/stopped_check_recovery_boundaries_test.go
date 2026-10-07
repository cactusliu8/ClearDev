package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func stoppedCheckRawDB(t *testing.T, f *projectPlanningFixture) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestStoppedCheckRecoveryRejectLaterAndUnapprovedSQL(t *testing.T) {
	ctx := context.Background()
	f, h, before := stoppedCheckFixture(t)
	input := stoppedCheckInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	req := stoppedCheckRequest(t, f)
	db := stoppedCheckRawDB(t, f)
	for _, query := range []string{
		`INSERT INTO cleardev_stopped_check_grants SELECT event_id,execution_run_id,decision_request_id,created_at FROM cleardev_stopped_check_requests`,
		`UPDATE cleardev_stopped_check_requests SET supplement='different'`,
		`DELETE FROM cleardev_stopped_check_requests`,
		`INSERT OR REPLACE INTO cleardev_stopped_check_requests SELECT * FROM cleardev_stopped_check_requests`,
		`INSERT INTO cleardev_complex_execution_check_runs(id,check_spec_id,task_attempt_id,candidate_commit_id,candidate_commit_sha,status,retry_ordinal,created_at) SELECT offer.retry_check_id,original.check_spec_id,original.task_attempt_id,original.candidate_commit_id,original.candidate_commit_sha,'PENDING',1,offer.created_at FROM cleardev_stopped_check_requests offer JOIN cleardev_complex_execution_check_runs original ON original.id=offer.original_check_id`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("unapproved or mutable authority accepted", query)
		}
	}
	later := coordinationRepairResult(t, f, req, core.HumanDecisionLater)
	if err := f.s.ApplyHumanDecisionResult(ctx, later); err != nil {
		t.Fatal(err)
	}
	pending, _, err := f.store.GetClearDevHumanDecisionRequest(ctx, req.ID)
	if err != nil || pending.Status != "PENDING" {
		t.Fatal("later became approval", pending, err)
	}
	now := f.s.now().UTC()
	offer, err := f.store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: req.ID, DesktopRunID: "stopped-check-reject-window", Nonce: mustComplexNonce(t), IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	reject := core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind, BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding, ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: core.HumanDecisionReject}
	if err := f.s.ApplyHumanDecisionResult(ctx, reject); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("later/reject changed original workflow", err)
	}
	input.RequestID = "cannot-replace-rejection"
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err == nil {
		t.Fatal("rejection opened another authority request")
	}
}

func TestStoppedCheckRecoverySourceChangesUnknownReceiptAndExpiryRefuse(t *testing.T) {
	ctx := context.Background()
	f, h, before := stoppedCheckFixture(t)
	input := stoppedCheckInput(t, f, h)
	if _, err := f.s.RequestWorkflowRecovery(ctx, h.requirementID, input); err != nil {
		t.Fatal(err)
	}
	req := stoppedCheckRequest(t, f)
	approve := coordinationRepairResult(t, f, req, core.HumanDecisionApprove)
	checkRefusal := func(label string) {
		t.Helper()
		if err := f.s.ApplyHumanDecisionResult(ctx, approve); err == nil {
			t.Fatal("unsafe source approved", label)
		}
	}
	h.receiptSettled = false
	checkRefusal("executor not known ended")
	h.receiptSettled = true
	originalCandidate := h.currentCandidate
	h.currentCandidate = forty("f")
	checkRefusal("workspace candidate changed")
	h.currentCandidate = originalCandidate
	state, err := f.store.ReadClearDevStoppedCheckRecovery(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := f.store.GetSession(ctx, domain.SessionID(state.Binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Metadata.ProviderConversationID = "other-native"
	if err := f.store.UpdateSession(ctx, changed); err != nil {
		t.Fatal(err)
	}
	checkRefusal("different native Builder")
	if err := f.store.UpdateSession(ctx, original); err != nil {
		t.Fatal(err)
	}
	changed = original
	changed.Activity.State = domain.ActivityActive
	if err := f.store.UpdateSession(ctx, changed); err != nil {
		t.Fatal(err)
	}
	checkRefusal("active Builder")
	if err := f.store.UpdateSession(ctx, original); err != nil {
		t.Fatal(err)
	}
	db := stoppedCheckRawDB(t, f)
	var priorState string
	if err := db.QueryRow(`SELECT state FROM cleardev_development_projects WHERE id=?`, h.requirementID).Scan(&priorState); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cleardev_development_projects SET paused_from_state=state,state='NEEDS_HUMAN' WHERE id=?`, h.requirementID); err != nil {
		t.Fatal(err)
	}
	checkRefusal("paused requirement")
	if _, err := db.Exec(`UPDATE cleardev_development_projects SET paused_from_state=NULL,state=? WHERE id=?`, priorState, h.requirementID); err != nil {
		t.Fatal(err)
	}
	forged := approve
	var b core.StoppedCheckRecoveryBinding
	if err := json.Unmarshal(approve.Binding, &b); err != nil {
		t.Fatal(err)
	}
	b.ReworkCount--
	forged.Binding, _ = json.Marshal(b)
	if err := f.s.ApplyHumanDecisionResult(ctx, forged); err == nil {
		t.Fatal("counter-refund binding was accepted")
	}
	if err := f.store.SettleClearDevHumanDecision(ctx, approve, f.s.now().Add(core.HumanDecisionOfferTTL+time.Second)); err == nil {
		t.Fatal("expired native offer accepted")
	}
	after, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil || len(after.PlannerRuntime.CheckRecoveries) != 0 || !reflect.DeepEqual(before.Exception.Budgets, after.Exception.Budgets) || !reflect.DeepEqual(before.CheckRuns, after.CheckRuns) || before.Tasks[0].ReworkCount != after.Tasks[0].ReworkCount {
		t.Fatal("refused source changed check or budgets", err)
	}
}

func TestStoppedCheckRecoveryConcurrentGrantRestartAndFailureDoNotRepeat(t *testing.T) {
	ctx := context.Background()
	f, h, before := stoppedCheckFixture(t)
	state, err := f.store.ReadClearDevStoppedCheckRecovery(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := sqlite.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	at := f.s.now().UTC()
	var wg sync.WaitGroup
	var successes atomic.Int32
	for _, st := range []*sqlite.Store{f.store, other} {
		wg.Add(1)
		go func(st *sqlite.Store) {
			defer wg.Done()
			if st.RequestClearDevStoppedCheckRecovery(ctx, "concurrent-stopped-check", "Request the same candidate check, no extra messages", state.Binding, at) == nil {
				successes.Add(1)
			}
		}(st)
	}
	wg.Wait()
	if successes.Load() == 0 {
		t.Fatal("no request registered")
	}
	req := stoppedCheckRequest(t, f)
	result := coordinationRepairResult(t, f, req, core.HumanDecisionApprove)
	successes.Store(0)
	settleAt := f.s.now()
	for _, st := range []*sqlite.Store{f.store, other} {
		wg.Add(1)
		go func(st *sqlite.Store) {
			defer wg.Done()
			if st.SettleClearDevHumanDecision(ctx, result, settleAt) == nil {
				successes.Add(1)
			}
		}(st)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("native approval must settle once", successes.Load())
	}
	granted, _, err := other.GetClearDevComplexExecution(ctx, h.requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if len(granted.PlannerRuntime.CheckRecoveries) != 1 || len(granted.WorkflowRecoveries) != 1 {
		t.Fatal("duplicate recovery")
	}
	g := granted.PlannerRuntime.CheckRecoveries[0]
	if _, registered, err := other.ValidateClearDevStoppedCheckBeforeRun(ctx, h.requirementID, g.RetryCheckRunID); err != nil || !registered {
		t.Fatal("reopened database lost exact pending recovery", registered, err)
	}
	db := stoppedCheckRawDB(t, f)
	for _, query := range []string{
		`UPDATE cleardev_stopped_check_grants SET created_at=created_at`,
		`DELETE FROM cleardev_stopped_check_grants`,
		`INSERT OR REPLACE INTO cleardev_stopped_check_grants SELECT * FROM cleardev_stopped_check_grants`,
		`UPDATE cleardev_work_items SET rework_count=rework_count-1 WHERE complex_execution_task_id IN (SELECT json_extract(binding_json,'$.taskId') FROM cleardev_stopped_check_requests)`,
		`INSERT INTO cleardev_complex_execution_task_attempts(id,execution_run_id,task_mapping_id,builder_role_binding_id,agent_step_id,round,base_commit_sha,status,reason_code) SELECT 'forbidden-new-dispatch',execution_run_id,task_mapping_id,builder_role_binding_id,agent_step_id,round+1,base_commit_sha,'PENDING','' FROM cleardev_complex_execution_task_attempts WHERE id=(SELECT json_extract(binding_json,'$.dispatchId') FROM cleardev_stopped_check_requests)`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("grant permitted mutation/refund/new dispatch", query)
		}
	}
	h.failures = 1
	for range 15 {
		x, _, err := f.store.GetClearDevComplexExecution(ctx, h.requirementID)
		if err != nil {
			t.Fatal(err)
		}
		if blocked, _ := core.PlannerRuntimeBarrier(x); blocked && x.Tasks[0].Status != core.DevelopmentTaskStatusRunning {
			if x.Tasks[0].ReworkCount != before.Tasks[0].ReworkCount || len(x.Dispatches) != len(before.Dispatches) || !reflect.DeepEqual(before.Exception.Budgets, x.Exception.Budgets) {
				t.Fatal("retry failure spent/refunded another round or sent more work")
			}
			v, err := f.s.GetWorkflowRecovery(ctx, h.requirementID)
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range v.Options {
				if o.Action == core.RecoveryRequestStoppedCheck {
					t.Fatal("technical retry required another native request", o)
				}
			}
			for range 3 {
				_, _, _ = f.s.advanceComplexStandardExecution(ctx, h.requirementID)
			}
			count := 0
			for _, request := range h.checkRequests {
				if request.RunID == g.RetryCheckRunID {
					count++
				}
			}
			if count != 1 {
				t.Fatal("failure repeated external check", count)
			}
			return
		}
		if _, _, err := f.s.advanceComplexStandardExecution(ctx, h.requirementID); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("failed authorized check did not remain stopped")
}
