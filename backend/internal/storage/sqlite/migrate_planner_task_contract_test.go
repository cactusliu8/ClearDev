package sqlite

import (
	"strings"
	"testing"
)

func init() { shippedMigrations[148] = "0148_cleardev_planner_task_contract.sql" }

func TestMigration0148PlannerContractUpgradeDownReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 147)
	oldInsert := readRunTrigger(t, db)
	var oldFinalizer, oldReviewer string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name='cleardev_complex_execution_completion_finalize'").Scan(&oldFinalizer); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name='cleardev_complex_execution_review_insert_valid'").Scan(&oldReviewer); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 148)
	for _, name := range []string{"cleardev_complex_plan_validations", "cleardev_complex_plan_validation_insert_guard", "cleardev_complex_plan_identity_insert_guard", "cleardev_complex_contract_no_steward_review"} {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("missing %s: %d %v", name, n, err)
		}
	}
	var notNull int
	if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('cleardev_complex_execution_runs') WHERE name='plan_review_id'`).Scan(&notNull); err != nil || notNull != 0 {
		t.Fatalf("new run admission must not require a fake Steward review: %d %v", notNull, err)
	}
	for _, fragment := range []string{"PLANNER_TASK_CONTRACT_V1", "NEW.plan_review_id IS NULL", "BETWEEN 1 AND 3", "BETWEEN 1 AND 2"} {
		if !strings.Contains(readRunTrigger(t, db), fragment) {
			t.Fatalf("new admission guard lacks %q", fragment)
		}
	}
	for name, expected := range map[string]string{"cleardev_complex_execution_completion_finalize": oldFinalizer, "cleardev_complex_execution_review_insert_valid": oldReviewer} {
		var actual string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", name).Scan(&actual); err != nil || actual != expected {
			t.Fatalf("contract migration changed the existing %s protection: %v", name, err)
		}
	}
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity: %s %v", check, err)
	}
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("migration disabled foreign keys: %d %v", foreignKeys, err)
	}
	downTo(t, db, 147)
	if readRunTrigger(t, db) != oldInsert {
		t.Fatal("empty-history downgrade did not restore the original admission guard")
	}
	if err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('cleardev_complex_execution_runs') WHERE name='plan_review_id'`).Scan(&notNull); err != nil || notNull != 1 {
		t.Fatalf("downgrade did not restore the required legacy review: %d %v", notNull, err)
	}
	upTo(t, db, 148)
}

func TestMigration0148RejectsUnboundAdmission(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 148)
	_, err := db.Exec(`INSERT INTO cleardev_complex_plan_validations(plan_id,plan_sha256,policy,check_catalog_sha256,created_at)
VALUES('missing-plan',?,'PLANNER_TASK_CONTRACT_V1',?,'2026-09-22T12:00:00Z')`, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err == nil {
		t.Fatal("a validation without a confirmed settled Planner result was admitted")
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM cleardev_complex_plan_validations").Scan(&n); err != nil || n != 0 {
		t.Fatalf("rejected admission left durable state: %d %v", n, err)
	}
}
