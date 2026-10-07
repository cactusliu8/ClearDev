-- name: GetClearDevStoppedCheckRequest :one
SELECT * FROM cleardev_stopped_check_requests WHERE id=?;

-- name: GetClearDevStoppedCheckRequestForRun :one
SELECT * FROM cleardev_stopped_check_requests WHERE execution_run_id=?;

-- name: GetClearDevStoppedCheckRequestForEvent :one
SELECT * FROM cleardev_stopped_check_requests WHERE event_id=?;

-- name: GetClearDevStoppedCheckRequestForRetry :one
SELECT * FROM cleardev_stopped_check_requests WHERE retry_check_id=?;

-- name: InsertClearDevStoppedCheckRequest :exec
INSERT INTO cleardev_stopped_check_requests(id,execution_run_id,event_id,original_check_id,retry_check_id,decision_request_id,binding_json,context_json,supplement,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevStoppedCheckGrant :one
SELECT * FROM cleardev_stopped_check_grants WHERE event_id=?;

-- name: InsertClearDevStoppedCheckGrant :exec
INSERT INTO cleardev_stopped_check_grants(event_id,execution_run_id,decision_request_id,created_at) VALUES(?,?,?,?);

-- name: ListClearDevStoppedCheckGrants :many
SELECT offer.binding_json,grant.decision_request_id FROM cleardev_stopped_check_grants grant
JOIN cleardev_stopped_check_requests offer ON offer.event_id=grant.event_id
WHERE grant.execution_run_id=? ORDER BY grant.created_at,grant.event_id;

-- name: ListClearDevStoppedCheckRetryChain :many
SELECT * FROM cleardev_stopped_check_retry_chain WHERE execution_run_id=? ORDER BY retry_ordinal;

-- name: GetClearDevStoppedCheckRequestForChain :one
SELECT offer.* FROM cleardev_stopped_check_requests offer
JOIN cleardev_stopped_check_retry_chain chain ON chain.event_id=offer.event_id
WHERE chain.check_id=?;
