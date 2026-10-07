package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func init() {
	shippedMigrations[151] = "0151_cleardev_project_execution.sql"
	shippedMigrations[152] = "0152_cleardev_project_check_catalog.sql"
	shippedMigrations[153] = "0153_cleardev_project_delivery_evidence.sql"
	shippedMigrations[154] = "0154_cleardev_project_delivery_continuation.sql"
	shippedMigrations[155] = "0155_cleardev_project_reviewer_checks.sql"
	shippedMigrations[156] = "0156_cleardev_project_no_dependency_checks.sql"
	shippedMigrations[157] = "0157_cleardev_product_invalid_reply.sql"
	shippedMigrations[158] = "0158_cleardev_product_invalid_reply_reconcile.sql"
	shippedMigrations[159] = "0159_cleardev_project_review_rework_attempt.sql"
	shippedMigrations[160] = "0160_cleardev_review_budget_extra_authorization.sql"
	shippedMigrations[161] = "0161_cleardev_review_budget_extra_guard.sql"
	shippedMigrations[162] = "0162_cleardev_review_budget_extra_check.sql"
	shippedMigrations[163] = "0163_cleardev_review_rework_verified_candidate.sql"
	shippedMigrations[164] = "0164_cleardev_final_review_per_candidate.sql"
	shippedMigrations[165] = "0165_cleardev_integration_candidate_rework_round.sql"
	shippedMigrations[166] = "0166_cleardev_budget_occupancy_round.sql"
	shippedMigrations[167] = "0167_cleardev_task_rework_budget_three.sql"
}

func TestProjectNoDependencyReviewerCheckMigrationRestoresOldGuard(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 155)
	var oldGuard string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_review_check_result_valid'`).Scan(&oldGuard); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 156)
	var newGuard string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_review_check_result_valid'`).Scan(&newGuard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(newGuard, "json_type(NEW.result_json,'$.projectReceipt.packageLockSha256') IS NULL") || newGuard == oldGuard {
		t.Fatal("no-dependency npm receipt is not admitted by the new guard")
	}
	if err := migrateDown(155, db); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_review_check_result_valid'`).Scan(&restored); err != nil || restored != oldGuard {
		t.Fatalf("original reviewer result guard was not restored: %v", err)
	}
}

func TestProjectExecutionMigrationsUpgradeAndEmptyDowngrade(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 150)
	var mailViewBefore string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='view' AND name='cleardev_mail_replacement_final_sources'`).Scan(&mailViewBefore); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 155)
	for _, name := range []string{"cleardev_project_execution_admissions", "cleardev_completed_project_deliveries", "cleardev_review_check_results"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing %s: count=%d err=%v", name, count, err)
		}
	}
	var deliveryGuard string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='cleardev_project_delivery_context_guard'`).Scan(&deliveryGuard); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"$.selection.option.key", "$.selection.option.title", "$.selection.option.origin", "$.selection.option.repositoryUrl", "$.selection.option.description", "$.selection.option.tradeoffs"} {
		if !strings.Contains(deliveryGuard, path) {
			t.Fatalf("delivery provenance trigger does not bind the complete saved option: missing %s", path)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("migration introduced foreign-key violations: %d %v", violations, err)
	}
	if err := migrateDown(150, db); err != nil {
		t.Fatal(err)
	}
	if version := gooseVersion(t, db); version != 150 {
		t.Fatalf("downgrade version=%d", version)
	}
	var mailViewAfter string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='view' AND name='cleardev_mail_replacement_final_sources'`).Scan(&mailViewAfter); err != nil || mailViewAfter != mailViewBefore {
		t.Fatalf("downgrade did not restore the original mail replacement view: err=%v", err)
	}
	rows, err := db.Query(`SELECT * FROM cleardev_mail_replacement_final_sources LIMIT 0`)
	if err != nil {
		t.Fatalf("downgraded mail replacement view cannot be queried: %v", err)
	}
	defer rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("downgraded mail replacement view rows failed: %v", err)
	}
	for _, name := range []string{"cleardev_project_replacement_authorities", "cleardev_project_replacement_replies", "cleardev_project_replacement_review_check_sources", "cleardev_project_replacement_final_sources", "cleardev_project_replacement_result_exact"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("downgrade retained %s: count=%d err=%v", name, count, err)
		}
	}
	upTo(t, db, 155)
}

type projectMigrationReceiptRow struct {
	ReviewID, CheckID, RunID, ResultJSON, Digest, RecordedAt string
}

func readProjectMigrationReceipts(t *testing.T, db *sql.DB) []projectMigrationReceiptRow {
	t.Helper()
	rows, err := db.Query(`SELECT review_id,check_id,run_id,result_json,result_sha256,CAST(created_at AS TEXT) FROM cleardev_review_check_results ORDER BY review_id,check_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result []projectMigrationReceiptRow
	for rows.Next() {
		var row projectMigrationReceiptRow
		if err := rows.Scan(&row.ReviewID, &row.CheckID, &row.RunID, &row.ResultJSON, &row.Digest, &row.RecordedAt); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

// This is deliberately a row-preservation fixture, not a completed product or
// proof that these invented reviews passed the old service. Missing upstream
// rows are recorded as pre-existing FK violations and must not increase. Full
// valid graphs and completion guards are covered by the Service/SQLite tests.
func TestProjectReviewerMigrationPreservesLegacySuccessAndFailureBytes(t *testing.T) {
	db := openMigrationTestDB(t)
	db.SetMaxOpenConns(1)
	upTo(t, db, 154)
	var requestGuard, resultGuard string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_review_check_request_valid'`).Scan(&requestGuard); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_review_check_result_valid'`).Scan(&resultGuard); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER cleardev_review_check_request_valid; DROP TRIGGER cleardev_review_check_result_valid`); err != nil {
		t.Fatal(err)
	}
	for index, outcome := range []string{"PASS", "FAIL", "TIMED_OUT", "INFRA_ERROR"} {
		id := fmt.Sprintf("legacy-row-fixture-%d", index)
		request := fmt.Sprintf(`{"reviewId":%q,"candidateId":"legacy-candidate","candidateSha":%q,"packetSha256":%q,"requestStepId":"original-step","checkIds":["demo-api"],"requestedAt":"2026-09-23T01:00:00Z"}`, id, strings.Repeat("a", 40), strings.Repeat("b", 64))
		result, err := json.Marshal(map[string]any{
			"reviewId": id, "checkId": "demo-api", "outcome": outcome, "output": "historical output retained verbatim: " + outcome,
			"proof":      map[string]any{"runId": id + ":requested-check:demo-api", "candidateSha": strings.Repeat("a", 40), "passed": outcome == "PASS"},
			"recordedAt": "2026-09-23T01:01:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO cleardev_review_check_requests VALUES(?,?,?,'2026-09-23T01:00:00Z')`, id, request, strings.Repeat("c", 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO cleardev_review_check_results VALUES(?,'demo-api',?,?,?,'2026-09-23T01:01:00Z')`, id, id+":requested-check:demo-api", string(result), strings.Repeat("d", 64)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(requestGuard + ";" + resultGuard + ";PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	before := readProjectMigrationReceipts(t, db)
	var cdcBefore, fkBefore int
	if err := db.QueryRow(`SELECT count(*) FROM change_log`).Scan(&cdcBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fkBefore); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		upTo(t, db, 155)
		after := readProjectMigrationReceipts(t, db)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("upgrade rewrote historical success/failure receipt bytes or timestamps")
		}
		var cdcAfter, fkAfter int
		if err := db.QueryRow(`SELECT count(*) FROM change_log`).Scan(&cdcAfter); err != nil || cdcAfter != cdcBefore {
			t.Fatalf("migration re-announced old receipt events: %d -> %d %v", cdcBefore, cdcAfter, err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&fkAfter); err != nil || fkAfter != fkBefore {
			t.Fatalf("migration changed existing FK violations: %d -> %d %v", fkBefore, fkAfter, err)
		}
		// Dependent views and the task-review completion trigger still resolve
		// the same table, rather than the deleted temporary backup name.
		if _, err := db.Exec(`UPDATE cleardev_complex_execution_reviews SET summary=summary WHERE 0`); err != nil {
			t.Fatalf("dependent completion trigger was broken by table rebuild: %v", err)
		}
		for _, statement := range []string{
			`UPDATE cleardev_review_check_results SET result_json=result_json WHERE review_id='legacy-row-fixture-0'`,
			`DELETE FROM cleardev_review_check_results WHERE review_id='legacy-row-fixture-0'`,
			`INSERT OR REPLACE INTO cleardev_review_check_results SELECT * FROM cleardev_review_check_results WHERE review_id='legacy-row-fixture-0'`,
		} {
			if _, err := db.Exec(statement); err == nil {
				t.Fatal("upgrade lost an immutable history guard")
			}
		}
		if err := migrateDown(154, db); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, readProjectMigrationReceipts(t, db)) {
			t.Fatal("downgrade rewrote historical Reviewer receipts")
		}
		var schema string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_review_check_results'`).Scan(&schema); err != nil || !strings.Contains(schema, "'demo-backend','demo-api','demo-frontend','demo-integration'") {
			t.Fatalf("old result catalog not restored: %v", err)
		}
	}
}

// An existing planning-only context is also history. None of the new Down
// steps may partially roll the schema back before an older guard rejects it.
func TestProjectExecutionMigrationDownPreservesPlanningHistoryAtEveryVersion(t *testing.T) {
	for _, version := range []int64{151, 152, 153, 154, 155, 156, 157} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			db := openMigrationTestDB(t)
			db.SetMaxOpenConns(1)
			upTo(t, db, 150)
			var guard string
			if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_product_context_insert_guard'`).Scan(&guard); err != nil {
				t.Fatal(err)
			}
			// Minimal row-preservation fixture, not a real product discussion.
			// All admission and source semantics are tested through the Service.
			if _, err := db.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER cleardev_product_context_insert_guard;
INSERT INTO cleardev_product_discussion_contexts VALUES('historical-context-fixture',2,NULL,NULL)`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(guard + ";PRAGMA foreign_keys=ON"); err != nil {
				t.Fatal(err)
			}
			upTo(t, db, version)
			schema := func() string {
				var value string
				if err := db.QueryRow(`SELECT group_concat(type||':'||name||':'||coalesce(sql,''),char(10)) FROM (SELECT type,name,sql FROM sqlite_schema ORDER BY type,name)`).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := schema()
			if err := migrateDown(149, db); err == nil {
				t.Fatal("downgrade erased the saved planning-only context")
			}
			if got := gooseVersion(t, db); got != version || schema() != before {
				t.Fatalf("refused downgrade partially changed schema at %d, now %d", version, got)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM cleardev_product_discussion_contexts WHERE discussion_id='historical-context-fixture' AND protocol_version=2 AND selection_json IS NULL AND selection_sha256 IS NULL`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("historical context changed: %d %v", count, err)
			}
		})
	}
}

func TestProjectExecutionMigrationDownRefusesAdmittedHistory(t *testing.T) {
	for _, version := range []int64{151, 152, 153, 154, 155, 156, 157} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			db := openMigrationTestDB(t)
			db.SetMaxOpenConns(1)
			upTo(t, db, version)
			// Like the existing migration down-guard fixtures, seed only the
			// historical row the guard must preserve; this is not admission.
			if _, err := db.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER cleardev_project_admission_insert_guard;
INSERT INTO cleardev_project_execution_admissions VALUES('fixture-run','fixture-stage','fixture-plan','{}',?,'2026-09-23T01:00:00Z')`, strings.Repeat("a", 64)); err != nil {
				t.Fatal(err)
			}
			if err := migrateDown(version-1, db); err == nil {
				t.Fatal("downgrade discarded or reinterpreted admitted project history")
			}
			if got := gooseVersion(t, db); got != version {
				t.Fatalf("refused downgrade moved version %d -> %d", version, got)
			}
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM cleardev_project_execution_admissions WHERE execution_run_id='fixture-run'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("refused downgrade lost admission history: %d %v", count, err)
			}
		})
	}
}

// A settled rejected reply is history its constraint must keep recognising.
// The 0157 Down guard must refuse before the failure whitelist narrows again.
func TestProjectInvalidReplyMigrationDownKeepsRejectedReplyHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	db.SetMaxOpenConns(1)
	upTo(t, db, 157)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER cleardev_product_discussion_insert_guard;
INSERT INTO cleardev_product_goals VALUES('fixture-goal','fixture-request',1,'2026-09-24T01:00:00Z');
INSERT INTO cleardev_product_discussions VALUES('fixture-invalid','fixture-goal',0,'rejected reply',NULL,NULL,NULL,'PRODUCT_DISCOVERY_INVALID','2026-09-24T01:00:00Z','2026-09-24T01:00:01Z')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(156, db); err == nil {
		t.Fatal("downgrade discarded rejected-reply history")
	}
	if got := gooseVersion(t, db); got != 157 {
		t.Fatalf("refused downgrade moved version 157 -> %d", got)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_product_discussions WHERE id='fixture-invalid' AND settled_at IS NOT NULL AND failure_reason='PRODUCT_DISCOVERY_INVALID'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("refused downgrade lost the rejected reply: %d %v", count, err)
	}
}

// Discussions left pending by a rejected reply before 0157 must settle to the
// same terminal fact; timeout and unavailability steps keep their recovery.
func TestProjectInvalidReplyReconcileSettlesOnlyRejectedReplies(t *testing.T) {
	db := openMigrationTestDB(t)
	db.SetMaxOpenConns(1)
	upTo(t, db, 157)
	// Minimal historical rows only; this is not a product or approval fixture.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF; DROP TRIGGER cleardev_complex_agent_step_insert_valid;
INSERT INTO cleardev_product_discussions VALUES('pending-invalid','fixture-goal-a',0,'rejected reply',NULL,NULL,NULL,NULL,'2026-09-24T01:00:00Z',NULL);
INSERT INTO cleardev_product_discussions VALUES('pending-timeout','fixture-goal-b',0,'timed out reply',NULL,NULL,NULL,NULL,'2026-09-24T02:00:00Z',NULL);
INSERT INTO cleardev_complex_agent_steps(id,role_binding_id,step_kind,request_id,client_message_id,prompt_sha256,send_status,requested_at,failed_at,reason_code)
VALUES('step-invalid','fixture-binding','REQUIREMENT_COMPILATION','pending-invalid','client-invalid','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','FAILED','2026-09-24T01:00:01Z','2026-09-24T01:00:02Z','PRODUCT_DISCOVERY_INVALID');
INSERT INTO cleardev_complex_agent_steps(id,role_binding_id,step_kind,request_id,client_message_id,prompt_sha256,send_status,requested_at,failed_at,reason_code)
VALUES('step-timeout','fixture-binding','REQUIREMENT_COMPILATION','pending-timeout','client-timeout','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','FAILED','2026-09-24T02:00:01Z','2026-09-24T02:00:02Z','PRODUCT_STEWARD_TIMEOUT')`); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 158)
	var settled sql.NullString
	var reason string
	if err := db.QueryRow(`SELECT settled_at, failure_reason FROM cleardev_product_discussions WHERE id='pending-invalid'`).Scan(&settled, &reason); err != nil || !settled.Valid || reason != "PRODUCT_DISCOVERY_INVALID" {
		t.Fatalf("rejected reply was not reconciled to its failed step: settled=%v reason=%q err=%v", settled, reason, err)
	}
	var pending int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_product_discussions WHERE id='pending-timeout' AND settled_at IS NULL AND failure_reason IS NULL`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("timeout reply must stay pending for recovery: %d %v", pending, err)
	}
	if err := migrateDown(157, db); err == nil {
		t.Fatal("downgrade must refuse while the reconciled failure is history")
	}
	var kept int
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_product_discussions WHERE id='pending-invalid' AND failure_reason='PRODUCT_DISCOVERY_INVALID'`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("refused downgrade lost the reconciled failure: %d %v", kept, err)
	}
	if got := gooseVersion(t, db); got != 158 {
		t.Fatalf("refused downgrade moved version 158 -> %d", got)
	}
}

// The independent review proved the authorized extra turn still could not be
// spent because the table CHECK kept refusing used_turns beyond max_turns.
func TestReviewBudgetExtraTurnIsConsumableAfterAuthorization(t *testing.T) {
	db := openMigrationTestDB(t)
	db.SetMaxOpenConns(1)
	upTo(t, db, 162)
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF;
INSERT INTO cleardev_complex_exception_budgets (id, execution_run_id, complex_execution_task_id, role_kind, allowed_agent_types_json, model_selection, max_turns, used_turns, max_rework_count, created_at, authorized_extra_turns)
VALUES ('fixture-budget','fixture-run','fixture-task','REVIEWER','["codex-chat"]','codex-chat',2,2,0,'2026-09-25T01:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_exception_budgets SET used_turns=used_turns+1 WHERE id='fixture-budget'`); err != nil {
		t.Fatalf("authorized extra turn was not consumable: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_exception_budgets SET used_turns=used_turns+1 WHERE id='fixture-budget'`); err == nil {
		t.Fatal("occupancy beyond max_turns plus authorized turns was accepted")
	}
}

// A rework round verifies a second candidate for the same task mapping; the
// rebuilt table must allow exactly that while keeping every other uniqueness.
func TestRepeatedVerifiedCandidateForReworkedTaskIsRecorded(t *testing.T) {
	db := openMigrationTestDB(t)
	db.SetMaxOpenConns(1)
	upTo(t, db, 163)
	// Only the table's own constraints are under test here; the association
	// trigger is verified through the service tests.
	insert := `PRAGMA foreign_keys=OFF; DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_insert_valid;
INSERT INTO cleardev_complex_execution_verified_candidates (id, task_mapping_id, task_attempt_id, candidate_commit_id, scope_check_run_id, required_check_runs_json, review_id, verified_at)
VALUES (?, 'fixture-task', ?, ?, 'fixture-scope-'||?, '[]', ?, '2026-09-25T01:00:00Z')`
	if _, err := db.Exec(insert, "verified-1", "attempt-1", "candidate-1", "1", "review-1"); err != nil {
		t.Fatalf("first round verification was refused: %v", err)
	}
	if _, err := db.Exec(insert, "verified-2", "attempt-2", "candidate-2", "2", "review-2"); err != nil {
		t.Fatalf("rework round verification was refused: %v", err)
	}
	if _, err := db.Exec(insert, "verified-3", "attempt-2", "candidate-3", "3", "review-3"); err == nil {
		t.Fatal("reusing an attempt id was accepted")
	}
}
