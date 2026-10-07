package sqlite

import (
	"context"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

// These are the exact migration bytes from the previously deployed branch, not
// invented ledger entries. In particular 0208 guards human-authorized changes
// and 0205 accepts exact Reviewer coordination reports that must remain valid.
func TestUnifiedFailureMigrationsUpgradeOccupiedVersionsAndPreserveInstalledGuards(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 202)
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("testdata/failure-routing-prior-212"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 212); err != nil {
		t.Fatal("apply known prior deployment", err)
	}
	if gooseVersion(t, db) != 212 {
		t.Fatal("fixture did not occupy the previously deployed migration numbers")
	}
	beforeSchema := workflowMigrationSchema(t, db)
	beforeLedger := preSendMigrationRows(t, db, "goose_db_version")
	if err := migrate(db); err != nil {
		t.Fatal("normal startup must apply fresh migration numbers", err)
	}
	if gooseVersion(t, db) != 215 {
		t.Fatal("unified failure migrations were silently skipped")
	}
	for _, name := range []string{"cleardev_workflow_failure_receipts", "cleardev_failure_coordination_sources", "cleardev_failure_prior_guards"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatal("missing physical table", name, count, err)
		}
	}
	var column int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('cleardev_planning_step_recoveries') WHERE name='old_discussion_json'`).Scan(&column); err != nil || column != 1 {
		t.Fatal("missing physical discussion snapshot column", column, err)
	}
	afterSchema := workflowMigrationSchema(t, db)
	rows, err := db.Query(`SELECT name,original_sql,bridge_predicate FROM cleardev_failure_prior_guards ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name, original, predicate string
		if err := rows.Scan(&name, &original, &predicate); err != nil {
			t.Fatal(err)
		}
		if original != beforeSchema["trigger:"+name] {
			t.Fatal("prior deployment guard was overwritten", name)
		}
		condition := strings.Index(original, "WHEN ")
		body := strings.Index(original, "BEGIN SELECT RAISE(ABORT,")
		if condition < 0 || body <= condition {
			t.Fatal("unexpected prior trigger shape", name)
		}
		expected := original[:condition] + "WHEN NOT (" + predicate + ") AND (" + original[condition+5:body] + ") " + original[body:]
		if afterSchema["trigger:"+name] != expected {
			t.Fatal("an existing predicate changed instead of being preserved", name)
		}
		if !strings.Contains(afterSchema["trigger:"+strings.Replace(name, "cleardev_", "cleardev_unified_", 1)], "WHEN ("+predicate+") AND (") {
			t.Fatal("new source branch is not independently guarded", name)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatal("not all prior guards were preserved", count)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	oldEvents := beforeSchema["trigger:cleardev_planner_runtime_event_insert"]
	oldAmendment := beforeSchema["trigger:cleardev_planner_runtime_amendment_insert"]
	if !strings.Contains(oldEvents, "cleardev_complex_execution_reviews review") || !strings.Contains(oldEvents, "cleardev_requirement_final_reviews review") || !strings.Contains(oldAmendment, "cleardev_engineering_revision_decisions approved") {
		t.Fatal("fixture did not exercise the deployed Reviewer and human-authority variants")
	}
	for _, fragment := range []string{"cleardev_engineering_revision_decisions", "package-lock.json"} {
		if strings.Contains(afterSchema["trigger:cleardev_unified_planner_runtime_amendment_insert"], fragment) {
			t.Fatal("automatic source inherited a historical path-authority exception", fragment)
		}
	}
	for _, statement := range []string{
		`UPDATE cleardev_failure_prior_guards SET original_sql='invalid'`,
		`DELETE FROM cleardev_failure_prior_guards`,
		`INSERT OR REPLACE INTO cleardev_failure_prior_guards SELECT * FROM cleardev_failure_prior_guards`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatal("stored prior guards were mutable", statement)
		}
	}
	ledger := preSendMigrationRows(t, db, "goose_db_version")
	if len(ledger) != len(beforeLedger)+3 || !reflect.DeepEqual(ledger[:len(beforeLedger)], beforeLedger) {
		t.Fatal("old migration ledger was rewritten")
	}
	// This is an empty synthetic database. Populated production histories must
	// use a consistent backup, not Down. Only the three new migrations run here.
	// Supply the original migration catalog for this empty rollback test;
	// the production tree deliberately does not reuse occupied 203–212.
	rollbackFiles := fstest.MapFS{}
	for _, source := range []fs.FS{os.DirFS("testdata/failure-routing-prior-212"), mustMigrationSubFS(t)} {
		entries, err := fs.ReadDir(source, ".")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			raw, err := fs.ReadFile(source, entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			rollbackFiles[entry.Name()] = &fstest.MapFile{Data: raw}
		}
	}
	rollback, err := goose.NewProvider(goose.DialectSQLite3, db, rollbackFiles)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rollback.DownTo(context.Background(), 212); err != nil {
		t.Fatal("empty fixture cannot restore its original deployed schema", err)
	}
	if gooseVersion(t, db) != 212 || !reflect.DeepEqual(beforeSchema, workflowMigrationSchema(t, db)) {
		t.Fatal("downgrade lost a historical guard or schema object")
	}
}

func mustMigrationSubFS(t *testing.T) fs.FS {
	t.Helper()
	files, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	return files
}
