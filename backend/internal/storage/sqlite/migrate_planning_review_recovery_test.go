package sqlite

import "testing"

func init() { shippedMigrations[146] = "0146_cleardev_planning_review_recovery.sql" }

func TestMigration0146PlanningRecoveryUpgradeDownReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 145)
	upTo(t, db, 146)
	downTo(t, db, 145)
	upTo(t, db, 146)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='cleardev_complex_agent_step_update_valid' AND sql LIKE '%AUTHORIZE_PLANNING_REVIEW_RECOVERY%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recovery guard count=%d err=%v", count, err)
	}
}

func TestMigration0146CannotResetFailedStepWithoutGrant(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 143)
	seedProductIdentityHistory(t, db, false)
	upTo(t, db, 146)
	_, err := db.Exec(`INSERT INTO cleardev_complex_agent_steps(id,role_binding_id,step_kind,request_id,client_message_id,prompt_sha256,send_status,requested_at,failed_at,reason_code) SELECT 'failed-review',role_binding_id,'COMPLEX_PLAN_REVIEW','review-request','review-client',prompt_sha256,'FAILED',requested_at,requested_at,'STEWARD_UNAVAILABLE' FROM cleardev_complex_agent_steps WHERE id='identity-step'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_agent_steps SET send_status='PENDING',failed_at=NULL,reason_code='' WHERE id='failed-review'`); err == nil {
		t.Fatal("failed step reset without native grant")
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_agent_steps SET send_status='SETTLED' WHERE id='failed-review'`); err == nil {
		t.Fatal("failed step became completed")
	}
	var status, reason string
	if err := db.QueryRow(`SELECT send_status,reason_code FROM cleardev_complex_agent_steps WHERE id='failed-review'`).Scan(&status, &reason); err != nil || status != "FAILED" || reason != "STEWARD_UNAVAILABLE" {
		t.Fatalf("failed evidence changed: %s %s %v", status, reason, err)
	}
}
