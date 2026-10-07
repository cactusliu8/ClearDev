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
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func extraPlanningRawDB(t *testing.T, f planningContinuationFixture) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func extraPlanningFactCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, table := range []string{"change_log", "cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_agent_message_reservations", "cleardev_agent_message_confirmations", "cleardev_planning_extra_requests", "cleardev_planning_extra_grants", "cleardev_planning_extra_continuations", "cleardev_human_decision_requests", "cleardev_human_decision_dispatches", "cleardev_human_decision_effects", "cleardev_controlled_preflights"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

type extraPlanningRowReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func extraPlanningSQLHistory(t *testing.T, reader extraPlanningRowReader) map[string][][]any {
	t.Helper()
	out := map[string][][]any{}
	for _, table := range []string{"cleardev_planning_extra_requests", "cleardev_planning_extra_grants", "cleardev_planning_extra_continuations", "change_log"} {
		rows, err := reader.QueryContext(context.Background(), "SELECT rowid,* FROM "+table+" ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range pointers {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			for i, value := range values {
				if raw, ok := value.([]byte); ok {
					values[i] = append([]byte(nil), raw...)
				}
			}
			out[table] = append(out[table], values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func pauseExtraPlanningRequirement(t *testing.T, f planningContinuationFixture) {
	t.Helper()
	// There is no requirement-pause API. Exercise the existing legal persisted
	// pause representation in this isolated fixture, retaining its source state.
	db := extraPlanningRawDB(t, f)
	var original string
	if err := db.QueryRow(`SELECT state FROM cleardev_development_projects WHERE id=?`, f.id).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cleardev_development_projects SET paused_from_state=state,state='NEEDS_HUMAN' WHERE id=?`, f.id); err != nil {
		t.Fatal(err)
	}
	var state, from string
	if err := db.QueryRow(`SELECT state,paused_from_state FROM cleardev_development_projects WHERE id=?`, f.id).Scan(&state, &from); err != nil || state != "NEEDS_HUMAN" || from != original || from == "" {
		t.Fatalf("fixture did not persist the real pause marker: state=%s from=%s original=%s err=%v", state, from, original, err)
	}
}

func TestExtraPlanningAttemptGetIsPureReadAtEveryAuthorityBoundary(t *testing.T) {
	for _, compilation := range []bool{false, true} {
		name := "discussion"
		if compilation {
			name = "compilation"
		}
		t.Run(name, func(t *testing.T) {
			f := exhaustedExtraPlanningFixture(t, compilation)
			db := extraPlanningRawDB(t, f)
			check := func(phase string) {
				t.Helper()
				before := extraPlanningFactCounts(t, db)
				budget := extraPlanningBudget(t, f)
				attempts := extraPlanningAttempts(t, f)
				checker := &scriptedControlledPreflight{}
				f.s.preflightChecker = checker
				restores, scheduled, calls := 0, 0, len(f.h.relays)
				oldRestore, oldBackground := f.s.recoverAgentSession, f.s.runBackground
				f.s.recoverAgentSession = func(context.Context, domain.SessionID) error { restores++; return nil }
				f.s.runBackground = func(func()) { scheduled++ }
				for range 3 {
					if _, err := f.s.GetWorkflowRecovery(context.Background(), f.id); err != nil {
						t.Fatalf("%s read failed: %v", phase, err)
					}
				}
				f.s.recoverAgentSession, f.s.runBackground = oldRestore, oldBackground
				f.s.preflightChecker = alwaysPassControlledPreflight{}
				if checker.ncalls() != 0 || restores != 0 || scheduled != 0 || len(f.h.relays) != calls || !reflect.DeepEqual(before, extraPlanningFactCounts(t, db)) || !reflect.DeepEqual(budget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(attempts, extraPlanningAttempts(t, f)) {
					t.Fatalf("%s GET prepared external work or changed persistent authority/message facts", phase)
				}
			}
			check("exhausted")
			input := requestExtraPlanningAttempt(t, f)
			check("requested")
			approveExtraPlanningAttempt(t, f)
			check("approved")
			f.s.runBackground = func(func()) {}
			if _, err := f.s.RequestWorkflowRecovery(context.Background(), f.id, extraPlanningInput("pure-read-registered", extraPlanningContinueAction, input.TargetID)); err != nil {
				t.Fatal(err)
			}
			check("registered-unsent")
		})
	}
}

func TestExtraPlanningAttemptRealPauseStopsRequestApprovalAndRegisteredSend(t *testing.T) {
	for _, compilation := range []bool{false, true} {
		for _, boundary := range []string{"request", "approval", "registered-send"} {
			name := "discussion/" + boundary
			if compilation {
				name = "compilation/" + boundary
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				f := exhaustedExtraPlanningFixture(t, compilation)
				input := extraPlanningInput("paused-request", extraPlanningRequestAction, extraPlanningOption(t, f, extraPlanningRequestAction).TargetID)
				var result core.HumanDecisionResult
				var pending func()
				if boundary != "request" {
					input = requestExtraPlanningAttempt(t, f)
					_, result = extraPlanningNativeOffer(t, f)
				}
				if boundary == "registered-send" {
					if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
						t.Fatal(err)
					}
					f.s.runBackground = func(run func()) { pending = run }
					if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput("paused-registered", extraPlanningContinueAction, input.TargetID)); err != nil || pending == nil {
						t.Fatalf("register before pause: %v", err)
					}
				}
				pauseExtraPlanningRequirement(t, f)
				budget, attempts, calls := extraPlanningBudget(t, f), extraPlanningAttempts(t, f), len(f.h.relays)
				switch boundary {
				case "request":
					if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, input); err == nil {
						t.Fatal("paused original requirement obtained a decision request")
					}
					if len(extraPlanningHistory(t, f, extraPlanningRequestAction)) != 0 {
						t.Fatal("paused request persisted authority intent")
					}
				case "approval":
					if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
						t.Fatal("native approval granted a paused original requirement")
					}
				case "registered-send":
					pending()
				}
				if !reflect.DeepEqual(budget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(attempts, extraPlanningAttempts(t, f)) || len(f.h.relays) != calls {
					t.Fatal("pause was bypassed or spent another message")
				}
			})
		}
	}
}

func TestExtraPlanningAttemptFactsRejectChangesAndDeletion(t *testing.T) {
	ctx := context.Background()
	f := exhaustedExtraPlanningFixture(t, true)
	input := requestExtraPlanningAttempt(t, f)
	approveExtraPlanningAttempt(t, f)
	f.s.runBackground = func(func()) {}
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput("immutable-extra-registered", extraPlanningContinueAction, input.TargetID)); err != nil {
		t.Fatal(err)
	}
	db := extraPlanningRawDB(t, f)
	var recursive int
	if err := db.QueryRow(`PRAGMA recursive_triggers`).Scan(&recursive); err != nil || recursive != 0 {
		t.Fatalf("fixture must exercise the ordinary recursive_triggers=0 connection: %d %v", recursive, err)
	}
	before := extraPlanningFactCounts(t, db)
	beforeRows := extraPlanningSQLHistory(t, db)
	budget, attempts := extraPlanningBudget(t, f), extraPlanningAttempts(t, f)
	history, err := f.s.GetWorkflowRecovery(ctx, f.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"cleardev_planning_extra_requests", "cleardev_planning_extra_grants", "cleardev_planning_extra_continuations"} {
		if before[table] != 1 {
			t.Fatalf("missing real saved fact for %s: %+v", table, before)
		}
		for _, statement := range []string{"UPDATE " + table + " SET created_at=created_at WHERE logical_step_id=?", "DELETE FROM " + table + " WHERE logical_step_id=?"} {
			if _, err := db.Exec(statement, f.step.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
				t.Fatalf("existing history was not protected by its immutable trigger: %s %v", statement, err)
			}
		}
		for _, operation := range []string{"INSERT OR REPLACE", "INSERT OR IGNORE"} {
			t.Run(table+"/"+operation, func(t *testing.T) {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				if _, err := tx.Exec(operation+" INTO "+table+" SELECT * FROM "+table+" WHERE logical_step_id=?", f.step.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
					t.Errorf("duplicate insert bypassed immutable history: %s %s err=%v", table, operation, err)
				}
				// Check before rollback: the rejected statement itself must retain
				// row identities, every field and the complete CDC history.
				if !reflect.DeepEqual(beforeRows, extraPlanningSQLHistory(t, tx)) {
					t.Error("duplicate insert changed rowid, immutable content or CDC")
				}
			})
		}
	}
	after, err := f.s.GetWorkflowRecovery(ctx, f.id)
	if err != nil || !reflect.DeepEqual(history.History, after.History) || !reflect.DeepEqual(before, extraPlanningFactCounts(t, db)) || !reflect.DeepEqual(beforeRows, extraPlanningSQLHistory(t, db)) || !reflect.DeepEqual(budget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(attempts, extraPlanningAttempts(t, f)) {
		t.Fatalf("failed history rewrite changed authority, original facts or budget: %v", err)
	}
	t.Run("every-unique-identity", func(t *testing.T) {
		extraPlanningCheckConflictIdentities(t, f, db)
	})
}

// A second genuine pending discussion supplies distinct current source facts,
// sessions and confirmed terminal failures in the same isolated database.
func extraPlanningSecondSource(t *testing.T, first planningContinuationFixture) planningContinuationFixture {
	t.Helper()
	ctx := context.Background()
	s := first.s
	s.runBackground = func(run func()) { run() }
	first.h.fail = true
	first.h.replies = append(first.h.replies, productReadyReply())
	command := s.productContainer("s04-project", "第二个独立产品", "保留第二个真实讨论来源，用于数据库历史保护测试。")
	goal := core.ProductGoal{ID: command.Requirement.ID, RequestID: "immutable-second-product", AOProjectID: command.Requirement.AOProjectID, Name: command.Requirement.Name, GoalText: command.OriginalPRDText, CreatedAt: command.Requirement.CreatedAt}
	if _, _, err := first.store.CreateClearDevProductGoal(ctx, core.CreateProductGoalCommand{Goal: goal, Container: command,
		Discussion: core.ProductDiscussion{ID: "product-message:" + goal.RequestID, ProductID: goal.ID, UserMessage: goal.GoalText, CreatedAt: goal.CreatedAt}}); err != nil {
		t.Fatal(err)
	}
	s.scheduleComplexFlow(goal.ID)
	planning, found, err := first.store.GetClearDevComplexPlanning(ctx, goal.ID)
	if err != nil || !found {
		t.Fatalf("second source has no real planning facts: %v", err)
	}
	var step core.AgentStep
	for _, item := range planning.AgentSteps {
		if item.Kind == core.ComplexAgentStepCompilation && item.SendStatus == core.AgentStepSendStatusFailed {
			step = item
		}
	}
	if step.ID == "" {
		t.Fatal("second source did not produce a terminal first failure")
	}
	for _, role := range planning.RoleBindings {
		if role.ID != step.RoleBindingID {
			continue
		}
		record, found, err := first.store.GetSession(ctx, domain.SessionID(role.AOSessionID))
		if err != nil || !found {
			t.Fatalf("second source has no bound original session: %v", err)
		}
		record.Metadata.ProviderConversationID = "immutable-second-original-native"
		record.Activity.State = domain.ActivityIdle
		if err := first.store.UpdateSession(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	state, err := first.store.ReadClearDevPlanningStepRecovery(ctx, goal.ID, s.now().UTC())
	if err != nil || state.Option.UnavailableReason != "" {
		t.Fatalf("second original source is not recoverable: %+v %v", state.Option, err)
	}
	first.h.replies = append(first.h.replies, productReadyReply())
	if _, err := s.RequestWorkflowRecovery(ctx, goal.ID, extraPlanningInput("immutable-second-original-retry", core.RecoveryRetryPlanningStep, state.Option.TargetID)); err != nil {
		t.Fatal(err)
	}
	s.runBackground = func(func()) {}
	return planningContinuationFixture{s: s, h: first.h, store: first.store, product: goal.ID, id: goal.ID, step: step, dir: first.dir}
}

type extraPlanningSQLCandidate struct {
	table   string
	columns []string
	values  []any
}

func (c extraPlanningSQLCandidate) insert(ctx context.Context, tx *sql.Tx, operation string, rowid *int64) error {
	columns := append([]string(nil), c.columns...)
	values := append([]any(nil), c.values...)
	if rowid != nil {
		columns, values = append([]string{"rowid"}, columns...), append([]any{*rowid}, values...)
	}
	_, err := tx.ExecContext(ctx, operation+" INTO "+c.table+" ("+strings.Join(columns, ",")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")+")", values...)
	return err
}

func extraPlanningCheckConflictIdentities(t *testing.T, first planningContinuationFixture, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	second := extraPlanningSecondSource(t, first)
	state, err := second.store.ReadClearDevExtraPlanningAttempt(ctx, second.id, second.s.now().UTC())
	if err != nil || state.Option.UnavailableReason != "" {
		t.Fatalf("second source lacks measured eligible original failures: %+v %v", state.Option, err)
	}
	binding, err := json.Marshal(state.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.ParseExtraPlanningAttemptBinding(binding); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	q := gen.New(tx)
	step, err := q.GetClearDevComplexAgentStep(ctx, second.step.ID)
	if err != nil {
		t.Fatal(err)
	}
	failure, err := q.GetLatestClearDevAgentAttemptEventForAttempt(ctx, state.Binding.SecondAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	old, err := json.Marshal(step)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	// This transaction declares synthetic pending/approved source facts only
	// for SQLite identity probes. It is rolled back and does not represent a
	// native approval path. The main fixture used the real service offer/apply.
	display := core.HumanDecisionDisplay{Title: "Second source identity fixture", Summary: "Synthetic SQL fixture", FullContent: "Keep the genuine second discussion source unchanged.", ChangeSummary: "No execution or real human approval"}
	displayRaw, err := json.Marshal(display)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := core.HumanDecisionContentSHA256(extraPlanningDecisionKind, binding, display)
	if err != nil {
		t.Fatal(err)
	}
	const decision = "immutable-sql-second-decision"
	if err := q.InsertClearDevHumanDecisionRequest(ctx, gen.InsertClearDevHumanDecisionRequestParams{ID: decision, DevelopmentProjectID: second.id, DecisionKind: extraPlanningDecisionKind,
		BindingSchemaVersion: 1, BindingJson: string(binding), DisplayJson: string(displayRaw), ContentSha256: digest, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	request := extraPlanningSQLCandidate{table: "cleardev_planning_extra_requests", columns: []string{"id", "requirement_id", "logical_step_id", "decision_request_id", "binding_json", "old_step_json", "original_status", "original_reason", "original_summary", "original_stopped_at", "supplement", "created_at"},
		values: []any{"immutable-sql-second-request", second.id, second.step.ID, decision, string(binding), string(old), step.SendStatus, step.ReasonCode, failure.ErrorSummary, failure.RecordedAt, "", at}}
	grant := extraPlanningSQLCandidate{table: "cleardev_planning_extra_grants", columns: []string{"decision_request_id", "logical_step_id", "created_at"}, values: []any{decision, second.step.ID, at}}
	third := second.step.ID + ":attempt:3"
	continuation := extraPlanningSQLCandidate{table: "cleardev_planning_extra_continuations", columns: []string{"id", "requirement_id", "logical_step_id", "decision_request_id", "third_attempt_id", "old_step_json", "original_status", "original_reason", "original_summary", "original_stopped_at", "supplement", "created_at"},
		values: []any{"immutable-sql-second-continuation", second.id, second.step.ID, decision, third, string(old), step.SendStatus, step.ReasonCode, failure.ErrorSummary, failure.RecordedAt, "", at}}
	positive := func(candidate extraPlanningSQLCandidate, complete func()) {
		t.Helper()
		before := extraPlanningSQLHistory(t, tx)
		if _, err := tx.ExecContext(ctx, "SAVEPOINT extra_identity_positive"); err != nil {
			t.Fatal(err)
		}
		if err := candidate.insert(ctx, tx, "INSERT", nil); err != nil {
			t.Fatalf("nonconflicting %s candidate is not valid: %v", candidate.table, err)
		}
		if complete != nil {
			complete()
		}
		var violations int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil || violations != 0 {
			t.Fatalf("positive candidate violated unchanged foreign-key/source guards: %d %v", violations, err)
		}
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO extra_identity_positive"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "RELEASE extra_identity_positive"); err != nil || !reflect.DeepEqual(before, extraPlanningSQLHistory(t, tx)) {
			t.Fatalf("positive probe did not preserve complete history after rollback: %v", err)
		}
	}
	conflicts := func(candidate extraPlanningSQLCandidate, keys []string) {
		t.Helper()
		for _, key := range keys {
			for _, operation := range []string{"INSERT OR REPLACE", "INSERT OR IGNORE"} {
				t.Run(candidate.table+"/"+key+"/"+operation, func(t *testing.T) {
					var existing any
					if err := tx.QueryRowContext(ctx, "SELECT "+key+" FROM "+candidate.table+" WHERE logical_step_id=?", first.step.ID).Scan(&existing); err != nil {
						t.Fatal(err)
					}
					changed := candidate
					changed.values = append([]any(nil), candidate.values...)
					var rowid *int64
					if key == "rowid" {
						value, ok := existing.(int64)
						if !ok {
							t.Fatalf("unexpected explicit rowid type: %T", existing)
						}
						rowid = &value
					} else {
						for index, column := range changed.columns {
							if column == key {
								changed.values[index] = existing
							}
						}
						// Grant decision/step are one-to-one in the original source
						// guard. A single-key mismatch may be rejected there first.
					}
					before := extraPlanningSQLHistory(t, tx)
					if _, err := tx.ExecContext(ctx, "SAVEPOINT extra_identity_conflict"); err != nil {
						t.Fatal(err)
					}
					err := changed.insert(ctx, tx, operation, rowid)
					sourceRejected := candidate.table == "cleardev_planning_extra_grants" && key != "rowid" && err != nil && strings.Contains(err.Error(), "exact resolved native approval")
					if err == nil || !strings.Contains(err.Error(), "immutable") && !sourceRejected {
						t.Errorf("conflicting %s did not reject history replacement: %v", key, err)
					}
					if !reflect.DeepEqual(before, extraPlanningSQLHistory(t, tx)) {
						t.Error("rejected identity conflict changed rowid, complete values or CDC")
					}
					if _, err := tx.ExecContext(ctx, "ROLLBACK TO extra_identity_conflict"); err != nil {
						t.Fatal(err)
					}
					if _, err := tx.ExecContext(ctx, "RELEASE extra_identity_conflict"); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
	positive(request, nil)
	conflicts(request, []string{"id", "logical_step_id", "decision_request_id", "rowid"})
	if err := request.insert(ctx, tx, "INSERT", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cleardev_human_decision_requests SET status='RESOLVED',decision='APPROVE',resolved_at=? WHERE id=?`, at, decision); err != nil {
		t.Fatal(err)
	}
	positive(grant, nil)
	conflicts(grant, []string{"decision_request_id", "logical_step_id", "rowid"})
	if err := grant.insert(ctx, tx, "INSERT", nil); err != nil {
		t.Fatal(err)
	}
	positive(continuation, func() {
		rows, err := q.InsertClearDevAgentStepAttempt(ctx, gen.InsertClearDevAgentStepAttemptParams{ID: third, DevelopmentProjectID: second.id, LogicalStepID: second.step.ID,
			StepCategory: string(core.AgentStepCategoryComplexPlanning), StepKind: string(core.ComplexAgentStepCompilation), AttemptNumber: 3,
			RoleBindingID: state.Binding.Source.RoleBindingID, AoSessionID: state.Binding.Source.AOSessionID, ClientMessageID: second.step.ClientMessageID + ":attempt:3", PromptSha256: second.step.PromptSHA256,
			TriggerFailureEventID: sql.NullString{String: failure.ID, Valid: true}, RequestedAt: at, CreatedAt: sql.NullTime{Time: at, Valid: true}, RequestedAtSemantics: string(core.AttemptTimeActualCreation)})
		if err != nil || rows != 1 {
			t.Fatalf("normal third source companion failed: %d %v", rows, err)
		}
	})
	conflicts(continuation, []string{"id", "logical_step_id", "decision_request_id", "third_attempt_id", "rowid"})
}

func TestExtraPlanningAttemptCannotReuseOriginalRecoveryRequestID(t *testing.T) {
	for _, action := range []string{extraPlanningRequestAction, extraPlanningContinueAction} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			f := exhaustedExtraPlanningFixture(t, true)
			target := extraPlanningOption(t, f, extraPlanningRequestAction).TargetID
			if action == extraPlanningContinueAction {
				requestExtraPlanningAttempt(t, f)
				approveExtraPlanningAttempt(t, f)
			}
			f.s.runBackground = func(func()) {}
			db := extraPlanningRawDB(t, f)
			before := extraPlanningFactCounts(t, db)
			// A rejected explicit CONTINUE can retain its normal preflight
			// observation. It cannot create authority, messages or attempts.
			delete(before, "change_log")
			delete(before, "cleardev_controlled_preflights")
			budget, attempts := extraPlanningBudget(t, f), extraPlanningAttempts(t, f)
			history, err := f.s.GetWorkflowRecovery(ctx, f.id)
			if err != nil {
				t.Fatal(err)
			}
			// The same HTTP request id already persisted the original RETRY body.
			// Changing its action and target cannot create a second history item.
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput(f.input().RequestID, action, target)); err == nil {
				t.Fatal("original RETRY request id accepted a different extra-attempt HTTP intent")
			}
			after, err := f.s.GetWorkflowRecovery(ctx, f.id)
			afterCounts := extraPlanningFactCounts(t, db)
			delete(afterCounts, "change_log")
			delete(afterCounts, "cleardev_controlled_preflights")
			if err != nil || !reflect.DeepEqual(history.History, after.History) || !reflect.DeepEqual(before, afterCounts) || !reflect.DeepEqual(budget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(attempts, extraPlanningAttempts(t, f)) {
				t.Fatalf("conflicting public request id changed authority, history or accounting: before=%+v after=%+v err=%v", before, afterCounts, err)
			}
		})
	}
}

func TestExtraPlanningAttemptRejectsOlderUnknownDelivery(t *testing.T) {
	for _, status := range []core.AgentAttemptSendStatus{core.AgentAttemptDeliveryUnknown, core.AgentAttemptObservationTimedOut} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			f := exhaustedExtraPlanningFixture(t, true)
			option := extraPlanningOption(t, f, extraPlanningRequestAction)
			attempts := extraPlanningAttempts(t, f)
			category := domain.AgentFailureDeliveryUnknown
			if status == core.AgentAttemptObservationTimedOut {
				category = domain.AgentFailureObservationTimeout
			}
			// A terminal second result cannot conceal that an older original
			// message now lacks a settled delivery/observation identity.
			if err := f.store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: "older-original-unknown", AttemptID: attempts[0].ID, Status: status, ClientMessageID: f.step.ClientMessageID, PromptSHA256: f.step.PromptSHA256, FailureCategory: category, RecordedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			oldBudget := extraPlanningBudget(t, f)
			calls := len(f.h.relays)
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, extraPlanningInput("unknown-old-message", extraPlanningRequestAction, option.TargetID)); err == nil {
				t.Fatal("newer terminal failure hid an older unknown message")
			}
			if len(extraPlanningHistory(t, f, extraPlanningRequestAction)) != 0 || len(extraPlanningAttempts(t, f)) != 2 || len(f.h.relays) != calls || !reflect.DeepEqual(oldBudget, extraPlanningBudget(t, f)) {
				t.Fatal("uncertain old delivery created authority or changed the ledger")
			}
		})
	}
}

func TestExtraPlanningAttemptRechecksRegisteredSuccessorBeforeAnyReservation(t *testing.T) {
	for _, mode := range []string{"source", "native", "preflight", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := exhaustedExtraPlanningFixture(t, true)
			input := requestExtraPlanningAttempt(t, f)
			approveExtraPlanningAttempt(t, f)
			var pending func()
			f.s.runBackground = func(run func()) { pending = run }
			continuation := extraPlanningInput("registered-source-recheck", extraPlanningContinueAction, input.TargetID)
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil || pending == nil {
				t.Fatalf("did not register a successor without delivery: %v", err)
			}
			oldBudget := extraPlanningBudget(t, f)
			oldHistory := extraPlanningHistory(t, f, extraPlanningContinueAction)
			attempts := extraPlanningAttempts(t, f)
			record, _, err := f.store.GetSession(ctx, domain.SessionID(attempts[2].AOSessionID))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source":
				f.h.dirty = true
			case "native":
				changed := record
				changed.Metadata.ProviderConversationID = "different-native-conversation"
				if err := f.store.UpdateSession(ctx, changed); err != nil {
					t.Fatal(err)
				}
			case "preflight":
				f.s.preflightChecker = &scriptedControlledPreflight{}
			case "cancelled":
				if err := f.s.CancelRequirement(ctx, f.id, "explicit fixture cancellation after extra registration"); err != nil {
					t.Fatal(err)
				}
			}
			calls := len(f.h.relays)
			pending()
			after := extraPlanningAttempts(t, f)
			if len(f.h.relays) != calls || len(after) != 3 || after[2].SendStatus != core.AgentAttemptPending || after[2].LastEventID != "" || !reflect.DeepEqual(oldBudget, extraPlanningBudget(t, f)) || !reflect.DeepEqual(oldHistory, extraPlanningHistory(t, f, extraPlanningContinueAction)) {
				t.Fatalf("changed prerequisites acquired a message reservation, terminal event or replacement continuation: %+v", after)
			}
			if mode == "cancelled" {
				return
			}
			f.h.dirty = false
			if err := f.store.UpdateSession(ctx, record); err != nil {
				t.Fatal(err)
			}
			f.s.preflightChecker = alwaysPassControlledPreflight{}
			f.h.fail = false
			f.h.replies = append(f.h.replies, f.reply)
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil {
				t.Fatal("retry exact existing continuation after fixing its prerequisites", err)
			}
			pending()
			if len(f.h.relays) != calls+1 || len(extraPlanningAttempts(t, f)) != 3 || !reflect.DeepEqual(oldHistory, extraPlanningHistory(t, f, extraPlanningContinueAction)) {
				t.Fatal("fixed prerequisites did not resume the same registered successor exactly once")
			}
		})
	}
}

func TestExtraPlanningAttemptLostConfirmationReopenOnlyReconcilesOriginalMessage(t *testing.T) {
	ctx := context.Background()
	f := exhaustedExtraPlanningFixture(t, true)
	input := requestExtraPlanningAttempt(t, f)
	approveExtraPlanningAttempt(t, f)
	oldBudget := extraPlanningBudget(t, f)
	f.h.fail = false
	f.h.replies = append(f.h.replies, f.reply)
	f.s.attempts = messageBudgetFailConfirmation{f.store}
	continuation := extraPlanningInput("lost-extra-confirmation", extraPlanningContinueAction, input.TargetID)
	calls := len(f.h.relays)
	if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil {
		t.Fatal(err)
	}
	unknown := extraPlanningAttempts(t, f)
	if len(f.h.relays) != calls+1 || len(unknown) != 3 || unknown[2].SendStatus != core.AgentAttemptDeliveryUnknown {
		t.Fatalf("fixture lacks an actual send with lost confirmation: %+v calls=%d", unknown, len(f.h.relays)-calls)
	}
	before := extraPlanningBudget(t, f)
	if *before.ReservedMessages != *oldBudget.ReservedMessages+1 || *before.ConfirmedSentMessages != *oldBudget.ConfirmedSentMessages {
		t.Fatalf("lost acknowledgement refunded or confirmed an unknown send: %+v", before)
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
	f.h.store = f.store
	f.s = planningContinuationService(f.store, f.h, f.s.newID, f.s.now)
	f.s.desktopRunID = extraPlanningTestDesktop
	for range 2 {
		if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, continuation); err != nil {
			t.Fatal(err)
		}
		if err := f.s.ResumeComplexFlows(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after := extraPlanningBudget(t, f)
	if len(f.h.relays) != calls+1 || len(extraPlanningAttempts(t, f)) != 3 || *after.ReservedMessages != *before.ReservedMessages || *after.ConfirmedSentMessages != *oldBudget.ConfirmedSentMessages+1 {
		t.Fatalf("reopen resent or refunded the existing third message: before=%+v after=%+v sends=%d", before, after, len(f.h.relays)-calls)
	}
}
