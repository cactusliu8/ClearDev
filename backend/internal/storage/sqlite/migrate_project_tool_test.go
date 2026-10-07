package sqlite

import "testing"

func init() { shippedMigrations[176] = "0176_cleardev_project_tool.sql" }

func TestProjectToolMigrationPreservesLegacyConfigAndGuardsDowngrade(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 175)
	legacy := `{"worker":{"agent":"opencode"},"agentConfig":{"model":"legacy-codex-model"}}`
	if _, err := db.Exec(`INSERT INTO projects (id,path,repo_origin_url,display_name,registered_at,config) VALUES ('legacy','/legacy','','Legacy',CURRENT_TIMESTAMP,?)`, legacy); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 176)
	var config string
	if err := db.QueryRow(`SELECT config FROM projects WHERE id='legacy'`).Scan(&config); err != nil || config != legacy {
		t.Fatalf("legacy configuration was rewritten: %q, %v", config, err)
	}
	selected := `{"cleardev":{"agent":"opencode","model":"local/model"}}`
	if _, err := db.Exec(`UPDATE projects SET config=? WHERE id='legacy'`, selected); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(175, db); err == nil {
		t.Fatal("downgrade would silently turn an explicit OpenCode project into legacy Codex")
	}
	if err := db.QueryRow(`SELECT config FROM projects WHERE id='legacy'`).Scan(&config); err != nil || config != selected {
		t.Fatalf("blocked downgrade changed project choice: %q, %v", config, err)
	}
	if _, err := db.Exec(`UPDATE projects SET config=? WHERE id='legacy'`, legacy); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(175, db); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 176)
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity: %s %v", integrity, err)
	}
}
