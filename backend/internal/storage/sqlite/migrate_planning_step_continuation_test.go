package sqlite

import "testing"

func init() { shippedMigrations[180] = "0180_cleardev_planning_step_continuation.sql" }

func TestPlanningStepContinuationMigrationRestoresExactGuard(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 179)
	var before string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_complex_agent_step_update_valid'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 180)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('cleardev_planning_step_recoveries','cleardev_planning_step_recovery_immutable','cleardev_planning_step_recovery_keep_history','cleardev_planning_step_recovery_cdc','cleardev_planning_step_recovery_current')`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("planning continuation schema incomplete: %d %v", count, err)
	}
	downTo(t, db, 179)
	var restored string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='cleardev_complex_agent_step_update_valid'`).Scan(&restored); err != nil || restored != before {
		t.Fatalf("empty downgrade changed the original guard bytes: %v", err)
	}
	upTo(t, db, 180)
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("foreign key failures: %d %v", count, err)
	}
}

func TestPlanningStepContinuationMigrationRejectsUnboundReset(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 143)
	seedProductIdentityHistory(t, db, false)
	upTo(t, db, 180)
	_, err := db.Exec(`INSERT INTO cleardev_complex_agent_steps(id,role_binding_id,step_kind,request_id,client_message_id,prompt_sha256,send_status,requested_at,failed_at,reason_code)
 SELECT 'failed-compilation',role_binding_id,'REQUIREMENT_COMPILATION','compilation-request','compilation-client',prompt_sha256,'FAILED',requested_at,requested_at,'STEWARD_RESULT_INVALID'
 FROM cleardev_complex_agent_steps WHERE id='identity-step'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_agent_steps SET send_status='PENDING',failed_at=NULL,reason_code='' WHERE id='failed-compilation'`); err == nil {
		t.Fatal("unbound compilation reset was allowed")
	}
	if _, err := db.Exec(`UPDATE cleardev_complex_agent_steps SET send_status='SETTLED' WHERE id='failed-compilation'`); err == nil {
		t.Fatal("failed compilation could forge completion")
	}
	var status, reason string
	if err := db.QueryRow(`SELECT send_status,reason_code FROM cleardev_complex_agent_steps WHERE id='failed-compilation'`).Scan(&status, &reason); err != nil || status != "FAILED" || reason != "STEWARD_RESULT_INVALID" {
		t.Fatalf("old failure was not preserved: %s %s %v", status, reason, err)
	}
}
