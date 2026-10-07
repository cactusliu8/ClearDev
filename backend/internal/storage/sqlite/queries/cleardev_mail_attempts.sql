-- name: ListClearDevMailAttemptSlots :many
SELECT * FROM cleardev_mail_attempt_slots WHERE execution_run_id=? ORDER BY round;

-- name: GetClearDevMailAttemptSlot :one
SELECT * FROM cleardev_mail_attempt_slots WHERE dispatch_id=?;

-- name: InsertClearDevMailAttemptSlot :exec
INSERT INTO cleardev_mail_attempt_slots(dispatch_id,execution_run_id,task_id,round,attempt_kind,grant_request_id,created_at) VALUES(?,?,?,?,?,?,?);
