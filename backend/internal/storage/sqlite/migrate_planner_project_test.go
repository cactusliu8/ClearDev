package sqlite

import (
	"reflect"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[187] = "0187_cleardev_planner_project_coordination.sql" }

func TestProjectPlannerMigrationPreservesHistoricalFacts(t *testing.T) {
	db := openMigrationTestDB(t)
	seedClearDev0106LifecycleFixture(t, db)
	upTo(t, db, 186)
	read := func() map[string][][]string {
		out := map[string][][]string{}
		for _, table := range []string{"cleardev_development_projects", "cleardev_contract_versions", "cleardev_work_items", "cleardev_candidate_commits", "cleardev_evidence", "cleardev_project_events"} {
			rows, err := db.Query("SELECT rowid,* FROM " + table + " ORDER BY rowid")
			if err != nil {
				t.Fatal(err)
			}
			out[table] = migrationEvidenceRows(t, rows)
		}
		return out
	}
	before := read()
	upTo(t, db, 187)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("upgrade changed historical row bytes or identities")
	}
	var invalid int
	var integrity string
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatal("upgrade changed foreign-key integrity", invalid, err)
	}
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("upgrade changed SQLite integrity", integrity, err)
	}
	// This generic pre-project fixture does not rely on the new edge. Its
	// history must remain exact across both directions of this migration.
	if err := goose.DownTo(db, "migrations", 186); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("downgrade changed old history")
	}
	version, err := goose.GetDBVersion(db)
	if err != nil || version != 186 {
		t.Fatal("unexpected migration version", version, err)
	}
	upTo(t, db, 187)
	if !reflect.DeepEqual(before, read()) {
		t.Fatal("re-upgrade changed old history")
	}
}

func TestProjectPlannerMigrationEmptyDatabaseCanDowngrade(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 187)
	if err := goose.DownTo(db, "migrations", 186); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 187)
}
