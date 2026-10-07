-- name: GetClearDevProjectCheckContinuation :one
SELECT * FROM cleardev_project_check_continuations WHERE retry_check_run_id = ?;

-- name: InsertClearDevProjectCheckContinuation :exec
INSERT INTO cleardev_project_check_continuations
(retry_check_run_id, blocked_attempt_settled_at, created_at) VALUES (?, ?, ?);
