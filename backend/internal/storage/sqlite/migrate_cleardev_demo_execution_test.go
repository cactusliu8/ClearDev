package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigration0117ExpandsChecksAndAcceptsCurrentConfirmedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ao.db")
	db, err := sql.Open("sqlite", "file:"+path+pragmas)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	})
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 116)
	seedDemoExecutionPlan(t, db, "dev-v2", "req-v2", 2)
	if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_check_specs (
id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
check_spec_sha256, argv_json, timeout_seconds, created_at
) VALUES (
'old-all-tests', 'run-v2', NULL, NULL, 'INTEGRATION', 'all-tests',
'82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586',
'["node","--test"]', 60, '2026-08-28T12:00:00Z')`); err != nil {
		t.Fatalf("insert frozen all-tests spec before 0117: %v", err)
	}

	unlisted := []string{
		"cleardev_complex_execution_required_check_binding_valid",
		"cleardev_complex_execution_check_run_insert_valid",
		"cleardev_complex_execution_verified_candidate_insert_valid",
		"cleardev_complex_execution_result_check_insert_valid",
	}
	beforeUnlisted := map[string]string{}
	for _, name := range unlisted {
		var sqlText string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&sqlText); err != nil {
			t.Fatalf("read %s before 0117: %v", name, err)
		}
		beforeUnlisted[name] = sqlText
	}

	upTo(t, db, 117)

	for _, name := range unlisted {
		var sqlText string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&sqlText); err != nil {
			t.Fatalf("read %s after 0117: %v", name, err)
		}
		if sqlText != beforeUnlisted[name] {
			t.Fatalf("0117 rebuilt unlisted trigger %s", name)
		}
	}

	var kept int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_complex_execution_check_specs WHERE id = 'old-all-tests'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 1 {
		t.Fatal("0117 dropped the existing all-tests check spec")
	}
	raw0117, err := migrationsFS.ReadFile("migrations/0117_cleardev_demo_execution.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"DROP TRIGGER IF EXISTS cleardev_complex_execution_required_check_binding_valid",
		"DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_insert_valid",
		"DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_insert_valid",
		"DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_insert_valid",
	} {
		if strings.Contains(string(raw0117), name) {
			t.Fatalf("0117 still drops unlisted trigger via %s", name)
		}
	}

	var tableSQL, runTrigger, resultInsert, resultUpdate string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'cleardev_complex_execution_check_specs'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo-backend", "demo-database", "demo-api", "demo-frontend", "demo-integration", "email-unit"} {
		if !strings.Contains(tableSQL, want) {
			t.Fatalf("0117 check-spec catalog missing %q", want)
		}
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_run_insert_valid'`).Scan(&runTrigger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_result_insert_valid'`).Scan(&resultInsert); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'cleardev_complex_execution_result_update_valid'`).Scan(&resultUpdate); err != nil {
		t.Fatal(err)
	}
	for name, sqlText := range map[string]string{"run": runTrigger, "result insert": resultInsert, "result update": resultUpdate} {
		if strings.Contains(sqlText, "version.version = 2") {
			t.Fatalf("%s trigger still requires version number 2", name)
		}
		if strings.Contains(sqlText, "run.accepted_task_set_version = 1") {
			t.Fatalf("%s trigger still repeats accepted_task_set_version = 1", name)
		}
		if !strings.Contains(sqlText, "version.superseded_by_id IS NULL") || !strings.Contains(sqlText, "version.task_set_version =") {
			t.Fatalf("%s trigger does not bind the current confirmed version", name)
		}
	}

	if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_check_specs (
id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
check_spec_sha256, argv_json, timeout_seconds, created_at
) VALUES (
'demo-integration-spec', 'run-v2', NULL, NULL, 'INTEGRATION', 'demo-integration',
'527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4',
'["npm","test"]', 60, '2026-08-28T12:01:00Z')`); err != nil {
		t.Fatalf("insert demo-integration spec: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_check_specs (
id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
check_spec_sha256, argv_json, timeout_seconds, created_at
) VALUES (
'bad-id', 'run-v2', NULL, NULL, 'INTEGRATION', 'made-up',
'527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4',
'["npm","test"]', 60, '2026-08-28T12:02:00Z')`); err == nil {
		t.Fatal("unknown check id was accepted")
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_check_specs (
id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
check_spec_sha256, argv_json, timeout_seconds, created_at
) VALUES (
'bad-argv', 'run-v2', NULL, NULL, 'INTEGRATION', 'demo-backend',
'002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22',
'["npm","run","test"]', 60, '2026-08-28T12:03:00Z')`); err == nil {
		t.Fatal("rewritten demo-backend argv was accepted")
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_exception_specialist_checks (
id, execution_run_id, complex_execution_task_id, specialist_result_id, check_id,
argv_json, status, created_at
) VALUES (
'mig-check', 'run-v2', 'task-missing', 'result-missing', 'sqlite-migration-specialist',
'["npm","run","check:migrations"]', 'PENDING', '2026-08-28T12:04:00Z')`); err != nil {
		t.Fatalf("insert sqlite-migration-specialist: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_exception_specialist_checks (
id, execution_run_id, complex_execution_task_id, specialist_result_id, check_id,
argv_json, status, created_at
) VALUES (
'bad-specialist', 'run-v2', 'task-missing', 'result-missing', 'made-up-specialist',
'["npm","run","check:migrations"]', 'PENDING', '2026-08-28T12:05:00Z')`); err == nil {
		t.Fatal("unknown specialist check id was accepted")
	}

	seedDemoExecutionPlan(t, db, "dev-v1", "req-v1", 1)
	var v1Runs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cleardev_complex_execution_runs WHERE id = 'run-v1'`).Scan(&v1Runs); err != nil {
		t.Fatal(err)
	}
	if v1Runs != 1 {
		t.Fatal("current confirmed v1 could not start after 0117")
	}
}

func TestDemoExecutionShippedMigrationHashesRemainFrozenThrough0116(t *testing.T) {
	for file, want := range map[string]string{
		"migrations/0106_cleardev_control_foundation.sql":           "44fb135471e67de24370f7a19f8254db180e0681181fc871912c31438834b8ca",
		"migrations/0107_cleardev_requirement_lifecycle.sql":        "a8bec65b948548f12ce27e098c80bda1916bea13dd0a2357daea90d3834adb7d",
		"migrations/0108_cleardev_standard_single_task.sql":         "ad29d76c72df0b98455a8ec21628ab981dd7bb2dd9899ae31552cd7952ca014e",
		"migrations/0109_cleardev_desktop_human_authority.sql":      "3e9ae8f524cb4ad521030e35cd2d367385b80f3b52e4ae0bcf8d06ec184d21b9",
		"migrations/0110_cleardev_complex_requirement_planning.sql": "62da5c58ab7a09bb386a867b0f1c812c7a6b1d618bdd29ace57c1a449da375fb",
		"migrations/0111_cleardev_direction_change.sql":             "7a5f237dcec9afa9f059c421f45329d4b62d467cf88552b976d417d2001c189c",
		"migrations/0112_cleardev_complex_standard_execution.sql":   "416b5fbfc482b4831a57dc42c98b61400fa5ebe11a55d3822d30b20a102563ef",
		"migrations/0113_cleardev_parallel_execution.sql":           "79611ed495f120180ec0368e7bf6e3306580849d6e0a092e6e632c49383e79c3",
		"migrations/0114_cleardev_quick_execution.sql":              "7cac1611547c65cf0faaac82ae4ef4eea2376dc5db06814b48d9708060b8e6d9",
		"migrations/0115_cleardev_controlled_exceptions.sql":        "d62135dc2a2c750b3d637c1eb25f267b83c67704a2506f584afea514860a6891",
		"migrations/0116_cleardev_trusted_progress.sql":             "4fea86409922e39ef78fa98acedd0ae4f7e9c42295a18370b6f20093da80b6d3",
	} {
		content, err := migrationsFS.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(content)); got != want {
			t.Errorf("%s SHA-256 = %s, want %s", file, got, want)
		}
	}
}

func seedDemoExecutionPlan(t *testing.T, db *sql.DB, projectID, versionID string, version int) {
	t.Helper()
	const at = "2026-08-28T12:00:00Z"
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	aoID := "ao-" + projectID
	sessionID := "session-" + projectID
	stewardID := "steward-" + projectID
	plannerID := "planner-" + projectID
	planID := "plan-" + projectID
	reviewID := "review-" + projectID
	runID := "run-v2"
	if version == 1 {
		runID = "run-v1"
	}
	if _, err := db.Exec(`INSERT INTO projects (id, path, registered_at, config, kind)
VALUES (?, '/repo-`+projectID+`', ?, '{}', 'single_repo')`, aoID, at); err != nil {
		t.Fatalf("seed AO project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
id, project_id, num, harness, session_mode, permission_mode, creation_idempotency_key,
creation_request_fingerprint, activity_last_at, created_at, updated_at
) VALUES (?, ?, 1, 'codex', 'chat', 'auto', ?, ?, ?, ?, ?)`, sessionID, aoID, "key-"+projectID, "key-"+projectID, at, at, at); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_development_projects (
id, ao_project_id, name, state, paused_from_state, created_at, updated_at
) VALUES (?, ?, ?, 'READY', NULL, ?, ?)`, projectID, aoID, projectID, at, at); err != nil {
		t.Fatalf("seed development project: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_contract_versions (
id, development_project_id, version, contract_text, sha256, state,
superseded_by_id, created_at, approved_at, task_set_version
) VALUES (?, ?, ?, 'text', ?, 'APPROVED', NULL, ?, ?, 0)`, versionID, projectID, version, sha, at, at); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_requirements (
development_project_id, original_prd_text, original_prd_sha256, target_requirement_version_id, created_at
) VALUES (?, 'prd', ?, ?, ?)`, projectID, sha, versionID, at); err != nil {
		t.Fatalf("seed complex requirement: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_role_bindings (
id, development_project_id, role, session_creation_idempotency_key, ao_session_id, status, reason_code, requested_at
) VALUES (?, ?, 'STEWARD', ?, NULL, 'REQUESTED', '', ?)`, stewardID, projectID, "steward-key-"+projectID, at); err != nil {
		t.Fatalf("seed steward requested: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_role_bindings
SET status = 'BOUND', ao_session_id = ?, bound_at = ? WHERE id = ?`, sessionID, at, stewardID); err != nil {
		t.Fatalf("bind steward: %v", err)
	}
	plannerSession := sessionID + "-planner"
	if _, err := db.Exec(`INSERT INTO sessions (
id, project_id, num, harness, session_mode, permission_mode, creation_idempotency_key,
creation_request_fingerprint, activity_last_at, created_at, updated_at
) VALUES (?, ?, 2, 'codex', 'chat', 'auto', ?, ?, ?, ?, ?)`, plannerSession, aoID, "planner-key-"+projectID, "planner-key-"+projectID, at, at, at); err != nil {
		t.Fatalf("seed planner session: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_role_bindings (
id, development_project_id, role, session_creation_idempotency_key, ao_session_id, status, reason_code, requested_at
) VALUES (?, ?, 'ENGINEERING_PLANNER', ?, NULL, 'REQUESTED', '', ?)`, plannerID, projectID, "planner-key-"+projectID, at); err != nil {
		t.Fatalf("seed planner requested: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_role_bindings
SET status = 'BOUND', ao_session_id = ?, bound_at = ? WHERE id = ?`, plannerSession, at, plannerID); err != nil {
		t.Fatalf("bind planner: %v", err)
	}

	planStep := "plan-step-" + projectID
	reviewStep := "review-step-" + projectID
	seedSettledComplexStep(t, db, plannerID, planStep, "COMPLEX_ENGINEERING_PLAN", "plan-req-"+projectID, at)
	seedSettledComplexStep(t, db, stewardID, reviewStep, "COMPLEX_PLAN_REVIEW", "review-req-"+projectID, at)

	planJSON := `{"parallelSuggestion":{"recommendedBuilderCount":1},"tasks":[{"key":"a"},{"key":"b"}]}`
	if _, err := db.Exec(`INSERT INTO cleardev_complex_engineering_plans (
id, planning_request_id, development_project_id, requirement_version_id, requirement_sha256,
compilation_sha256, version, planner_role_binding_id, agent_step_id, turn_id, final_message_id,
plan_json, plan_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)`,
		planID, "plan-req-"+projectID, projectID, versionID, sha, sha, plannerID, planStep, planStep+"-turn", planStep+"-message", planJSON, sha, at); err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_complex_plan_reviews (
id, plan_id, steward_role_binding_id, agent_step_id, review_request_id, verdict, reason_code,
summary, findings_json, plan_sha256, turn_id, final_message_id, created_at
) VALUES (?, ?, ?, ?, ?, 'APPROVED', 'PLAN_ACCEPTABLE', 'ok', '[]', ?, ?, ?, ?)`,
		reviewID, planID, stewardID, reviewStep, "review-req-"+projectID, sha, reviewStep+"-turn", reviewStep+"-message", at); err != nil {
		t.Fatalf("seed review: %v", err)
	}

	pkg := `{"executionRunId":"` + runID + `"}`
	if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_runs (
id, development_project_id, requirement_version_id, requirement_sha256, plan_id, plan_review_id,
plan_sha256, steward_role_binding_id, mode, selection_reason_code, fixed_builder_count,
expected_task_set_version, accepted_task_set_version, execution_package_json, execution_package_sha256,
status, requested_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'STANDARD', 'ONE_BUILDER_REQUIRED', 1, 0, 1, ?, ?, 'PENDING', ?)`,
		runID, projectID, versionID, sha, planID, reviewID, sha, stewardID, pkg, sha, at); err != nil {
		t.Fatalf("seed execution run for version %d: %v", version, err)
	}
}

func seedSettledComplexStep(t *testing.T, db *sql.DB, bindingID, stepID, kind, requestID, at string) {
	t.Helper()
	sha := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := db.Exec(`INSERT INTO cleardev_complex_agent_steps (
id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256, send_status, requested_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?, '')`, stepID, bindingID, kind, requestID, stepID+"-client", sha, at); err != nil {
		t.Fatalf("insert pending step: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_agent_steps SET send_status = 'SENT', sent_at = ? WHERE id = ?`, at, stepID); err != nil {
		t.Fatalf("mark step sent: %v", err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_agent_steps
SET send_status = 'SETTLED', turn_id = ?, final_message_id = ?, final_message_text = 'ok',
message_sha256 = ?, completed_at = ? WHERE id = ?`, stepID+"-turn", stepID+"-message", sha, at, stepID); err != nil {
		t.Fatalf("settle step: %v", err)
	}
}
