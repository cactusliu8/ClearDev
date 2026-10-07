package sqlite

import "testing"

func init() { shippedMigrations[149] = "0149_cleardev_planner_runtime.sql" }

func TestMigration0149PlannerRuntimeUpgradeDownReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 148)
	protected := map[string]string{}
	for _, name := range []string{"cleardev_complex_execution_completion_finalize", "cleardev_complex_execution_review_insert_valid"} {
		var source string
		if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", name).Scan(&source); err != nil {
			t.Fatal(err)
		}
		protected[name] = source
	}
	for pass := 0; pass < 2; pass++ {
		upTo(t, db, 149)
		for _, name := range []string{"cleardev_planner_runtime_events", "cleardev_planner_runtime_requests", "cleardev_planner_runtime_decisions",
			"cleardev_planner_runtime_task_amendments", "cleardev_planner_runtime_effective_tasks", "cleardev_planner_runtime_barriers",
			"cleardev_planner_runtime_dispatch_guard", "cleardev_planner_runtime_final_review_guard", "cleardev_planner_runtime_completion_guard"} {
			var count int
			if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name=?", name).Scan(&count); err != nil || count != 1 {
				t.Fatalf("migration missing %s: count=%d err=%v", name, count, err)
			}
		}
		for name, want := range protected {
			var actual string
			if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE name=?", name).Scan(&actual); err != nil || actual != want {
				t.Fatalf("runtime migration changed original protection %s: %v", name, err)
			}
		}
		var integrity string
		if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("migration integrity: %s %v", integrity, err)
		}
		downTo(t, db, 148)
		var count int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'cleardev_planner_runtime_%'").Scan(&count); err != nil || count != 0 {
			t.Fatalf("empty-history downgrade left runtime schema: %d %v", count, err)
		}
	}
}
