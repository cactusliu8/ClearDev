package sqlite

import (
	"database/sql"
	"strings"
	"testing"
)

func init() { shippedMigrations[177] = "0177_cleardev_native_tool_guards.sql" }

func readControlledGuardSchema(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE type IN ('trigger','view') AND name LIKE 'cleardev_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		result[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNativeToolGuardsPreserveEveryOtherPredicateAndRoundTrip(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 176)
	before := readControlledGuardSchema(t, db)
	upTo(t, db, 177)
	after := readControlledGuardSchema(t, db)
	changed := 0
	for name, old := range before {
		want := strings.ReplaceAll(old, "session.harness = 'codex'", "session.id IN (SELECT session_id FROM cleardev_project_tool_sessions)")
		want = strings.ReplaceAll(want, "session.harness='codex'", "session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)")
		if after[name] != want {
			t.Fatalf("%s: migration changed another authority/evidence/budget predicate or missed a tool guard", name)
		}
		if old != want {
			changed++
			t.Logf("only the fixed tool predicate changed: %s", name)
		}
	}
	if changed != 8 || len(after) != len(before)+1 {
		t.Fatalf("unexpected schema scope: changed=%d before=%d after=%d", changed, len(before), len(after))
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("rewritten schema integrity: %s %v", integrity, err)
	}
	if err := migrateDown(176, db); err != nil {
		t.Fatal(err)
	}
	restored := readControlledGuardSchema(t, db)
	if len(restored) != len(before) {
		t.Fatal("downgrade left an extra schema object")
	}
	for name, original := range before {
		if restored[name] != original {
			t.Fatalf("downgrade did not restore the exact original guard: %s", name)
		}
	}
	upTo(t, db, 177)
	if _, err := db.Exec(`INSERT INTO projects (id,path,repo_origin_url,display_name,registered_at,config) VALUES ('selected','/selected','','Selected',CURRENT_TIMESTAMP,'{"cleardev":{"agent":"opencode","model":"local/model"}}')`); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(176, db); err == nil {
		t.Fatal("downgrade allowed an explicit project to lose its tool/model guards")
	}
	guarded := readControlledGuardSchema(t, db)
	for name, original := range after {
		if guarded[name] != original {
			t.Fatalf("rejected downgrade partially rewrote schema: %s", name)
		}
	}
}
