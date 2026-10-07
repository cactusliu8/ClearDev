package sqlite

import (
	"encoding/json"
	"strings"
	"testing"
)

// Exercise the shipped SQL trigger against SQLite, including direct writes
// that bypass Go validation. Other tables are the minimal persisted facts the
// trigger consumes; provider/Git behavior is outside this test.
func TestProjectLockfileAmendmentSQLGuards(t *testing.T) {
	raw, err := migrationsFS.ReadFile("migrations/0199_cleardev_project_lockfile_repair.sql")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Split(string(raw), "-- +goose Down")[0]
	script = strings.Replace(script, "DROP TRIGGER cleardev_planner_runtime_amendment_insert;", "", 1)
	for _, kind := range []string{"manifest", "consumer", "other-path", "other-check", "no-dependencies", "extra-permission", "changed-objective", "missing-coverage", "null-coverage"} {
		t.Run(kind, func(t *testing.T) {
			db := openAgentSwitchMigrationTestDB(t)
			for _, stmt := range []string{
				`CREATE TABLE cleardev_planner_runtime_task_amendments(id,event_id,task_mapping_id,ordinal,execution_run_id,previous_package_sha256,task_packet_sha256,task_packet_json)`,
				`CREATE TABLE cleardev_planner_runtime_effective_tasks(id,work_item_id,execution_run_id,plan_task_key,task_packet_sha256,task_packet_json)`,
				`CREATE TABLE cleardev_work_items(id,state,rework_count)`,
				`CREATE TABLE cleardev_complex_execution_task_mappings(id,task_packet_json)`,
				`CREATE TABLE cleardev_planner_runtime_current_decisions(event_id,execution_run_id,outcome,source,result_sha256,result_json)`,
				`CREATE TABLE cleardev_complex_execution_task_attempts(task_mapping_id,execution_run_id,status)`,
				`CREATE TABLE cleardev_mail_attempt_slots(task_id)`,
				`CREATE TABLE cleardev_complex_exception_budgets(execution_run_id,complex_execution_task_id,role_kind,authorized_extra_turns)`,
				script,
			} {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			old := map[string]any{"schemaVersion": 4, "writePaths": []string{"package.json", "src/**"}, "objective": "unchanged", "reviewCriteria": []string{"original acceptance"}, "projectExecution": map[string]any{"runtime": map[string]any{"environment": "NODE_NPM_V1"}, "basis": map[string]any{"writePaths": []string{"package.json", "src/**"}, "dependencyNeeds": []string{"vite"}, "checks": []any{map[string]any{"id": "build", "argv": []string{"npm", "run", "build"}, "timeoutSeconds": 60, "mainPaths": []string{"package.json", "src/**"}}}}}}
			if kind == "consumer" {
				old["writePaths"] = []string{"src/**"}
			}
			if kind == "no-dependencies" {
				old["projectExecution"].(map[string]any)["basis"].(map[string]any)["dependencyNeeds"] = []string{}
			}
			oldRaw, _ := json.Marshal(old)
			var next map[string]any
			if err := json.Unmarshal(oldRaw, &next); err != nil {
				t.Fatal(err)
			}
			basis := next["projectExecution"].(map[string]any)["basis"].(map[string]any)
			basis["writePaths"] = append(basis["writePaths"].([]any), "package-lock.json")
			check := basis["checks"].([]any)[0].(map[string]any)
			check["mainPaths"] = append(check["mainPaths"].([]any), "package-lock.json")
			if kind != "consumer" {
				next["writePaths"] = append(next["writePaths"].([]any), "package-lock.json")
			}
			next["runtimeRevision"] = map[string]any{"eventId": "event", "decisionSha256": "decision", "previousPackageSha256": "old", "firstRound": 1}
			switch kind {
			case "missing-coverage":
				delete(check, "mainPaths")
			case "null-coverage":
				check["mainPaths"] = nil
			case "other-path":
				basis["writePaths"] = append(basis["writePaths"].([]any), "secrets/**")
			case "other-check":
				check["argv"] = []string{"npm", "run", "skip"}
			case "extra-permission":
				next["writePaths"] = append(next["writePaths"].([]any), "secrets/**")
			case "changed-objective":
				next["objective"] = "different product"
			}
			decision, _ := json.Marshal(map[string]any{"amendments": []any{map[string]any{"taskKey": "task", "additionalReviewCriteria": []string{}, "executionBasis": basis}}})
			nextRaw, _ := json.Marshal(next)
			for _, stmt := range []string{`INSERT INTO cleardev_work_items VALUES('work','BLOCKED',0)`} {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO cleardev_planner_runtime_effective_tasks VALUES('task','work','run','task','old',?)`, string(oldRaw)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO cleardev_complex_execution_task_mappings VALUES('task',?)`, string(oldRaw)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO cleardev_planner_runtime_current_decisions VALUES('event','run','AMEND_REMAINING','PLANNER','decision',?)`, string(decision)); err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`INSERT INTO cleardev_planner_runtime_task_amendments VALUES('revision','event','task',1,'run','old','new',?)`, string(nextRaw))
			wantOK := kind == "manifest" || kind == "consumer"
			if (err == nil) != wantOK {
				t.Fatalf("insert success=%v want=%v: %v", err == nil, wantOK, err)
			}
			_, err = db.Exec(strings.Split(string(raw), "-- +goose Down")[1])
			if wantOK && err == nil {
				t.Fatal("downgrade discarded the repair's validation rule")
			}
			if !wantOK && err != nil {
				t.Fatalf("empty repair history could not downgrade: %v", err)
			}

		})
	}
}
