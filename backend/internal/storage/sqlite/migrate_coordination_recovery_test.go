package sqlite

import (
	"reflect"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[193] = "0193_cleardev_planner_coordination_recovery.sql" }

func TestCoordinationRecoveryMigrationRoundTripPreservesPredecessor(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 192)
	names := []string{"cleardev_complex_agent_step_update_valid", "cleardev_planner_runtime_barriers", "cleardev_planner_runtime_decision_insert", "cleardev_planner_runtime_amendment_insert", "cleardev_complex_execution_attempt_insert_valid"}
	before := map[string]string{}
	for _, n := range names {
		var sql string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", n).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		before[n] = sql
	}
	upTo(t, db, 193)
	for _, n := range []string{"cleardev_planner_runtime_recoveries", "cleardev_planner_runtime_recovery_decisions", "cleardev_planner_runtime_current_decisions", "cleardev_coordination_second_attempt_exact", "cleardev_coordination_message_exact"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", n).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing recovery schema", n, count, err)
		}
	}
	downTo(t, db, 192)
	for n, want := range before {
		var got string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", n).Scan(&got); err != nil || want != got {
			t.Fatal("downgrade changed original definition", n, err)
		}
	}
	upTo(t, db, 193)
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
}

func TestCoordinationRecoveryMigrationFailureRollsBack(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 192)
	var old string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name='cleardev_complex_agent_step_update_valid'").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER cleardev_coordination_message_exact BEFORE INSERT ON cleardev_agent_message_reservations BEGIN SELECT 1; END"); err != nil {
		t.Fatal(err)
	}
	gooseMu.Lock()
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	err := goose.UpTo(db, "migrations", 193)
	gooseMu.Unlock()
	if err == nil {
		t.Fatal("expected late migration failure")
	}
	var current string
	var tables int
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name='cleardev_complex_agent_step_update_valid'").Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN('cleardev_planner_runtime_recoveries','cleardev_planner_runtime_recovery_decisions')").Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if old != current || tables != 0 || gooseVersion(t, db) != 192 {
		t.Fatal("failed migration left partial authority", tables)
	}
}

func TestCoordinationRecoveryMigrationPreservesHistoricalFacts(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 192)
	tables := []string{"cleardev_development_projects", "cleardev_contract_versions", "sessions", "cleardev_agent_step_attempts", "cleardev_agent_attempt_events", "cleardev_human_decision_requests", "change_log"}
	before := map[string][][]string{}
	for _, table := range tables {
		before[table] = preSendMigrationRows(t, db, table)
	}
	upTo(t, db, 193)
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], preSendMigrationRows(t, db, table)) {
			t.Fatal("upgrade rewrote historical rows", table)
		}
	}
}
