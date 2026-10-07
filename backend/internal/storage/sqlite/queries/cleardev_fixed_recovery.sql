-- name: InsertClearDevFixedRecoveryRequest :exec
INSERT INTO cleardev_fixed_recovery_requests(id,execution_run_id,logical_step_id,check_run_id,failure_event_id,request_json,created_at) VALUES(?,?,?,?,?,?,?);
-- name: GetClearDevFixedRecoveryRequest :one
SELECT * FROM cleardev_fixed_recovery_requests WHERE id=?;
-- name: GetClearDevFixedRecoveryForStep :one
SELECT * FROM cleardev_fixed_recovery_requests WHERE logical_step_id=?;
-- name: ListClearDevFixedRecoveryRequests :many
SELECT * FROM cleardev_fixed_recovery_requests WHERE execution_run_id=? ORDER BY created_at,id;
-- name: InsertClearDevFixedRecoveryClaim :exec
INSERT INTO cleardev_fixed_recovery_claims(request_id,proposal_id,action,operation_id,claimed_at) VALUES(?,?,?,?,?);
-- name: GetClearDevFixedRecoveryClaim :one
SELECT * FROM cleardev_fixed_recovery_claims WHERE request_id=?;
-- name: InsertClearDevFixedRecoveryResult :exec
INSERT INTO cleardev_fixed_recovery_results(request_id,outcome,result_json,recorded_at) VALUES(?,?,?,?);
-- name: GetClearDevFixedRecoveryResult :one
SELECT * FROM cleardev_fixed_recovery_results WHERE request_id=?;
-- name: InsertClearDevReplacementReviewResult :exec
INSERT INTO cleardev_replacement_review_results(recovery_request_id,original_review_id,attempt_id,result_id,verdict,result_json,recorded_at) VALUES(?,?,?,?,?,?,?);
-- name: GetClearDevReplacementReviewResult :one
SELECT * FROM cleardev_replacement_review_results WHERE recovery_request_id=?;

-- name: GetClearDevFixedRecoveryRawResult :one
SELECT * FROM cleardev_agent_step_results WHERE id=?;

-- name: GetClearDevFixedRecoveryRefusal :one
SELECT * FROM cleardev_fixed_recovery_refusals WHERE request_id=?;
-- name: InsertClearDevFixedRecoveryRefusal :exec
INSERT INTO cleardev_fixed_recovery_refusals(request_id,reason_code,recorded_at) VALUES(?,?,?);
