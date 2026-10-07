package cleardev

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// Positive facts are produced through the ordinary service/store flow in a
// temporary database. SQL below only attempts forbidden history rewrites; no
// trigger or foreign key is disabled to create a passing fixture.
func TestPlannerTaskContractSQLFactsRejectReplacement(t *testing.T) {
	f := newAutoExecutionFixture(t)
	enableContractFixture(f, "dependency")
	f.confirm(t)
	before, found, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil || !found || before.Integration == nil || before.FinalReview == nil || before.FinalReview.Verdict != "PASS" {
		t.Fatalf("fixture did not complete through protected final review: %v", err)
	}
	planningBefore, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || len(planningBefore.Validations) != 1 {
		t.Fatalf("fixture has no real admission: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	var beforeEvents int
	if err := db.QueryRow("SELECT count(*) FROM change_log").Scan(&beforeEvents); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE cleardev_complex_plan_validations SET plan_sha256=printf('%064d',0)`,
		`UPDATE cleardev_complex_plan_validations SET check_catalog_sha256=printf('%064d',0)`,
		`DELETE FROM cleardev_complex_plan_validations`,
		`INSERT OR REPLACE INTO cleardev_complex_plan_validations SELECT * FROM cleardev_complex_plan_validations`,
		`UPDATE cleardev_complex_engineering_plans SET plan_json='{}'`,
		`DELETE FROM cleardev_complex_engineering_plans`,
		`INSERT OR REPLACE INTO cleardev_complex_engineering_plans SELECT * FROM cleardev_complex_engineering_plans`,
		`UPDATE cleardev_complex_execution_task_mappings SET task_packet_json='{}'`,
		`DELETE FROM cleardev_complex_execution_task_mappings`,
		`INSERT OR REPLACE INTO cleardev_complex_execution_task_mappings SELECT * FROM cleardev_complex_execution_task_mappings`,
		`UPDATE cleardev_complex_execution_runs SET id='reused-run'`,
		`UPDATE cleardev_complex_execution_runs SET rowid=rowid+10000`,
		`UPDATE cleardev_complex_execution_runs SET plan_review_id='fake-Steward-approval'`,
		`UPDATE cleardev_complex_execution_runs SET execution_package_json='{}'`,
		`INSERT OR REPLACE INTO cleardev_complex_execution_runs SELECT * FROM cleardev_complex_execution_runs`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("contract/candidate/review history was rewritable: %s", statement)
		}
	}
	// SQLite REPLACE also conflicts on an explicit rowid even when every named
	// unique identity is changed. Require the contract guard itself to reject it.
	for _, attack := range []struct {
		table        string
		guard        string
		replacements map[string]string
	}{
		{table: "cleardev_complex_engineering_plans", guard: "engineering plan identity cannot replace history", replacements: map[string]string{
			"id": "'replacement-plan'", "planning_request_id": "'replacement-request'", "agent_step_id": "'replacement-step'", "version": "999",
		}},
		{table: "cleardev_complex_execution_task_mappings", guard: "task contract identity cannot replace candidate or review history", replacements: map[string]string{
			"id": "'replacement-task'", "work_item_id": "'replacement-item'", "plan_task_key": "'replacement-key'", "ordinal": "999",
		}},
	} {
		statement := contractRowIDReplacement(t, db, attack.table, attack.replacements)
		if _, err := db.Exec(statement); err == nil || !strings.Contains(err.Error(), attack.guard) {
			t.Fatalf("rowid conflict was not blocked by %s: %v", attack.table, err)
		}
	}
	after, _, err := f.store.GetClearDevComplexExecution(context.Background(), f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected rewrites changed execution or final F: %v", err)
	}
	planningAfter, _, err := f.store.GetClearDevComplexPlanning(context.Background(), f.view.Requirement.ID)
	if err != nil || !reflect.DeepEqual(planningBefore, planningAfter) {
		t.Fatalf("rejected rewrites changed plan/contract admission: %v", err)
	}
	var afterEvents int
	if err := db.QueryRow("SELECT count(*) FROM change_log").Scan(&afterEvents); err != nil || beforeEvents != afterEvents {
		t.Fatalf("rejected history rewrite emitted CDC: before=%d after=%d err=%v", beforeEvents, afterEvents, err)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() || rows.Err() != nil {
		t.Fatal("contract fixture lost foreign-key integrity")
	}
	if phase, _ := core.DeriveComplexExecutionPhase(after); phase != core.ComplexExecutionCompleted {
		t.Fatal("rejected writes changed protected completion")
	}
}

func contractRowIDReplacement(t *testing.T, db *sql.DB, table string, replacements map[string]string) string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Error(err)
		}
	}()
	columns, expressions := []string{"rowid"}, []string{"rowid"}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		quoted := `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
		columns = append(columns, quoted)
		if replacement, ok := replacements[column]; ok {
			expressions = append(expressions, replacement)
		} else {
			expressions = append(expressions, quoted)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// table and expressions are compile-time test data, never application input.
	return fmt.Sprintf("INSERT OR REPLACE INTO %s (%s) SELECT %s FROM %s LIMIT 1", table, strings.Join(columns, ","), strings.Join(expressions, ","), table)
}
