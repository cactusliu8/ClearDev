package sqlite

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[194] = "0194_cleardev_extra_coordination.sql" }

func TestExtraCoordinationMigrationRoundTripAndAtomicFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "round trip"
		if fail {
			name = "late failure rolls back"
		}
		t.Run(name, func(t *testing.T) {
			db := openMigrationTestDB(t)
			upTo(t, db, 193)
			names := []string{"cleardev_planner_runtime_requests", "cleardev_planner_runtime_request_insert", "cleardev_planner_runtime_current_decisions", "cleardev_planner_runtime_barriers", "cleardev_planner_runtime_amendment_insert"}
			before := map[string]string{}
			for _, name := range names {
				var s string
				if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", name).Scan(&s); err != nil {
					t.Fatal(err)
				}
				before[name] = s
			}
			if fail {
				if _, err := db.Exec("CREATE TRIGGER cleardev_extra_coordination_step_exact BEFORE INSERT ON cleardev_complex_agent_steps BEGIN SELECT 1; END"); err != nil {
					t.Fatal(err)
				}
				if err := goose.UpTo(db, "migrations", 194); err == nil {
					t.Fatal("expected late migration failure")
				}
				if gooseVersion(t, db) != 193 {
					t.Fatal("failed migration changed version")
				}
				var n int
				if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name LIKE 'cleardev_extra_coordination_%'").Scan(&n); err != nil || n != 0 {
					t.Fatal("partial authority tables", n, err)
				}
			} else {
				upTo(t, db, 194)
				downTo(t, db, 193)
			}
			for name, want := range before {
				var got string
				if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", name).Scan(&got); err != nil || got != want {
					t.Fatal("schema changed", name, err)
				}
			}
			if !fail {
				upTo(t, db, 194)
			}
		})
	}
}

func TestExtraCoordinationMigrationPreservesHistoricalRowsAndGuardsPendingAuthority(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 193)
	tables := []string{"sessions", "cleardev_development_projects", "cleardev_contract_versions", "cleardev_agent_attempt_events", "cleardev_human_decision_requests", "change_log"}
	before := map[string][][]string{}
	for _, table := range tables {
		before[table] = preSendMigrationRows(t, db, table)
	}
	upTo(t, db, 194)
	for _, table := range tables {
		if !reflect.DeepEqual(before[table], preSendMigrationRows(t, db, table)) {
			t.Fatal("migration changed historical rows", table)
		}
	}
	var project string
	if err := db.QueryRow("SELECT id FROM cleardev_development_projects LIMIT 1").Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_human_decision_requests(id,development_project_id,decision_kind,binding_schema_version,binding_json,display_json,content_sha256,status,created_at)
VALUES('extra-history',?,'AUTHORIZE_EXTRA_PLANNER_COORDINATION',1,'{}','{}',?,'PENDING','2026-10-03T00:00:00Z')`, project, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(193, db); err == nil {
		t.Fatal("pending native authority lost on downgrade")
	}
	if gooseVersion(t, db) != 194 {
		t.Fatal("refused downgrade changed migration version")
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal(integrity, err)
	}
}
