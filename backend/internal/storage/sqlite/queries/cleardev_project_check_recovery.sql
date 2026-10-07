-- name: GetClearDevProjectCheckRecovery :one
SELECT * FROM cleardev_project_check_recoveries WHERE original_check_run_id = ?;

-- name: InsertClearDevProjectCheckRecovery :exec
INSERT INTO cleardev_project_check_recoveries
(original_check_run_id, retry_check_run_id, original_attempt_settled_at, created_at)
VALUES (?, ?, ?, ?);
