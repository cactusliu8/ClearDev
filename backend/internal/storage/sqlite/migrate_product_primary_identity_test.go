package sqlite

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func init() { shippedMigrations[145] = "0145_cleardev_product_primary_identity.sql" }

// Seed valid legacy data through the public schema, including explicit negative
// rowids or the NULL identities accepted before 0144. No guards are disabled.
func seedProductIdentityHistory(t *testing.T, db *sql.DB, nullStages bool) {
	t.Helper()
	at := "2026-09-22 00:00:00"
	result := `{"kind":"PRODUCT_DISCOVERY","outcome":"READY","stages":[{"key":"one"},{"key":"two"}]}`
	statements := []string{
		`INSERT INTO projects(id,path,registered_at) VALUES('identity-ao','/tmp/identity-ao','` + at + `')`,
		`INSERT INTO cleardev_development_projects(id,ao_project_id,name,state,created_at,updated_at) VALUES('identity-product','identity-ao','Mail','INTAKE','` + at + `','` + at + `'),('identity-child','identity-ao','Child','INTAKE','` + at + `','` + at + `')`,
		`INSERT INTO cleardev_complex_requirements(development_project_id,original_prd_text,original_prd_sha256,target_requirement_version_id,created_at) VALUES('identity-product','mail','` + strings.Repeat("a", 64) + `','identity-target','` + at + `')`,
		`INSERT INTO cleardev_product_goals(rowid,id,request_id,created_at) VALUES(-1,'identity-product','identity-request','` + at + `')`,
		`INSERT INTO cleardev_product_discussions(rowid,id,product_id,ordinal,user_message,created_at) VALUES(-1,'identity-discussion','identity-product',0,'mail','` + at + `')`,
		`INSERT INTO cleardev_complex_role_bindings(id,development_project_id,role,session_creation_idempotency_key,status,requested_at) VALUES('identity-steward','identity-product','STEWARD','identity-key','REQUESTED','` + at + `')`,
		`INSERT INTO cleardev_complex_agent_steps(id,role_binding_id,step_kind,request_id,client_message_id,prompt_sha256,send_status,turn_id,final_message_id,final_message_text,message_sha256,requested_at,sent_at,completed_at) VALUES('identity-step','identity-steward','REQUIREMENT_COMPILATION','identity-discussion','identity-client','` + strings.Repeat("a", 64) + `','SETTLED','turn','message','` + result + `','` + strings.Repeat("a", 64) + `','` + at + `','` + at + `','` + at + `')`,
		`UPDATE cleardev_product_discussions SET agent_step_id='identity-step',result_json='` + result + `',result_sha256='` + strings.Repeat("a", 64) + `',settled_at='` + at + `' WHERE id='identity-discussion'`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	stageID := "'identity-stage'"
	if nullStages {
		stageID = "NULL"
	}
	if _, err := db.Exec(`INSERT INTO cleardev_product_stages(rowid,id,product_id,discussion_id,ordinal,definition_json,definition_sha256,created_at) VALUES(-1,` + stageID + `,'identity-product','identity-discussion',0,'{"key":"one","feasibility":"SUPPORTED"}','` + strings.Repeat("a", 64) + `','` + at + `')`); err != nil {
		t.Fatal(err)
	}
	if nullStages {
		if _, err := db.Exec(`INSERT INTO cleardev_product_stages(rowid,id,product_id,discussion_id,ordinal,definition_json,definition_sha256,created_at) VALUES(2,NULL,'identity-product','identity-discussion',1,'{"key":"two","feasibility":"SUPPORTED"}','` + strings.Repeat("b", 64) + `','` + at + `')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE cleardev_product_stages SET development_requirement_id='identity-child',base_commit_sha='` + strings.Repeat("a", 40) + `' WHERE rowid=-1`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigration0145PreservesHistoryAndNormalInserts(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 143)
	seedProductIdentityHistory(t, db, false)
	upTo(t, db, 144)
	normalInserts := []string{
		`INSERT INTO cleardev_product_goals(id,request_id,created_at) VALUES('identity-child','second-request','2026-09-22')`,
		`INSERT INTO cleardev_product_discussions(id,product_id,ordinal,user_message,created_at) VALUES('second-discussion','identity-product',1,'next','2026-09-22')`,
		`INSERT INTO cleardev_product_stages(id,product_id,discussion_id,ordinal,definition_json,definition_sha256,created_at) VALUES('second-stage','identity-product','identity-discussion',1,'{"key":"two","feasibility":"SUPPORTED"}','` + strings.Repeat("b", 64) + `','2026-09-22')`,
	}
	// Reproduce the predecessor's false conflict independently for all three tables.
	for _, statement := range normalInserts {
		if _, err := db.Exec(statement); err == nil || !strings.Contains(err.Error(), "identity is null or occupied") {
			t.Fatalf("expected old automatic-rowid defect: %v", err)
		}
	}
	var cdcBefore, cdcAfter int
	if err := db.QueryRow(`SELECT count(*) FROM change_log`).Scan(&cdcBefore); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 145)
	if err := db.QueryRow(`SELECT count(*) FROM change_log`).Scan(&cdcAfter); err != nil || cdcAfter != cdcBefore {
		t.Fatalf("migration fabricated events: %d -> %d, %v", cdcBefore, cdcAfter, err)
	}
	for _, table := range []string{"cleardev_product_goals", "cleardev_product_discussions", "cleardev_product_stages"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("lost %s: count=%d err=%v", table, count, err)
		}
		if _, err := db.Exec(`SELECT rowid FROM ` + table); err == nil {
			t.Fatalf("%s retained a hidden conflict identity", table)
		}
	}
	var message, step, definition string
	if err := db.QueryRow(`SELECT user_message,agent_step_id FROM cleardev_product_discussions`).Scan(&message, &step); err != nil || message != "mail" || step != "identity-step" {
		t.Fatalf("discussion changed: %q %q %v", message, step, err)
	}
	if err := db.QueryRow(`SELECT definition_json FROM cleardev_product_stages`).Scan(&definition); err != nil || definition != `{"key":"one","feasibility":"SUPPORTED"}` {
		t.Fatalf("definition changed: %q %v", definition, err)
	}
	for _, statement := range normalInserts {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("normal insert rejected: %v", err)
		}
	}

	var brokenKeys int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&brokenKeys); err != nil || brokenKeys != 0 {
		t.Fatalf("migration left broken foreign keys: count=%d err=%v", brokenKeys, err)
	}
	if err := migrateDown(144, db); err == nil {
		t.Fatal("downgrade removed protections from recorded history")
	}
	if gooseVersion(t, db) != 145 {
		t.Fatal("failed downgrade changed version")
	}
}

func TestMigration0145RejectsLegacyNullStagesWithoutLosingHistory(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 143)
	seedProductIdentityHistory(t, db, true)
	upTo(t, db, 144)
	// The fourth review's legacy NULL-link replacement really succeeds at 0144.
	// Roll the probe back so the upgrade must preserve both original records.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE OR REPLACE cleardev_product_stages SET development_requirement_id='identity-child',base_commit_sha='` + strings.Repeat("a", 40) + `' WHERE rowid=2`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM cleardev_product_stages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy defect not reproduced: count=%d err=%v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	gooseMu.Lock()
	goose.SetBaseFS(migrationsFS)
	err = goose.UpTo(db, "migrations", 145)
	gooseMu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "NOT NULL") {
		t.Fatalf("malformed history must safely refuse upgrade: %v", err)
	}
	if gooseVersion(t, db) != 144 {
		t.Fatal("failed upgrade changed version")
	}
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_product_stages WHERE id IS NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("lost legacy rows: count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM cleardev_product_stages WHERE rowid=-1 AND development_requirement_id='identity-child'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost original link: count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE '%_v145'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration left partial schema: count=%d err=%v", count, err)
	}
}

func TestMigration0145EmptyDownAndReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 145)
	downTo(t, db, 144)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cleardev_product_goal_identity_guard'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("down failed to restore 0144: count=%d err=%v", count, err)
	}
	upTo(t, db, 145)
	downTo(t, db, 140)
	upTo(t, db, 145)
}

func TestMigration0145PreservesPreparedStage(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 143)
	seedProductIdentityHistory(t, db, false)
	if _, err := db.Exec(`UPDATE cleardev_product_stages SET development_requirement_id='identity-child',base_commit_sha='` + strings.Repeat("a", 40) + `' WHERE id='identity-stage'`); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 145)
	var child, base string
	if err := db.QueryRow(`SELECT development_requirement_id,base_commit_sha FROM cleardev_product_stages WHERE id='identity-stage'`).Scan(&child, &base); err != nil || child != "identity-child" || base != strings.Repeat("a", 40) {
		t.Fatalf("migration changed delivery binding: %q %q %v", child, base, err)
	}
	if _, err := db.Exec(`INSERT INTO cleardev_product_discussions(id,product_id,ordinal,user_message,created_at) VALUES('reopen','identity-product',1,'reopen','2026-09-22')`); err == nil {
		t.Fatal("migration unfroze prepared proposal")
	}
}
