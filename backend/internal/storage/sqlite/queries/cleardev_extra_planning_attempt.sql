-- name: GetClearDevPlanningExtraRequest :one
SELECT * FROM cleardev_planning_extra_requests WHERE id=?;

-- name: GetClearDevPlanningExtraRequestForStep :one
SELECT * FROM cleardev_planning_extra_requests WHERE logical_step_id=?;

-- name: GetClearDevPlanningExtraRequestForDecision :one
SELECT * FROM cleardev_planning_extra_requests WHERE decision_request_id=?;

-- name: ListClearDevPlanningExtraRequests :many
SELECT * FROM cleardev_planning_extra_requests WHERE requirement_id=? ORDER BY rowid;

-- name: InsertClearDevPlanningExtraRequest :exec
INSERT INTO cleardev_planning_extra_requests (id,requirement_id,logical_step_id,decision_request_id,binding_json,old_step_json,original_status,original_reason,original_summary,original_stopped_at,supplement,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevPlanningExtraGrant :one
SELECT * FROM cleardev_planning_extra_grants WHERE decision_request_id=?;

-- name: GetClearDevPlanningExtraGrantForStep :one
SELECT * FROM cleardev_planning_extra_grants WHERE logical_step_id=?;

-- name: InsertClearDevPlanningExtraGrant :exec
INSERT INTO cleardev_planning_extra_grants (decision_request_id,logical_step_id,created_at) VALUES (?,?,?);

-- name: GetClearDevPlanningExtraContinuation :one
SELECT * FROM cleardev_planning_extra_continuations WHERE id=?;

-- name: GetClearDevPlanningExtraContinuationForStep :one
SELECT * FROM cleardev_planning_extra_continuations WHERE logical_step_id=?;

-- name: GetClearDevPlanningExtraContinuationForDecision :one
SELECT * FROM cleardev_planning_extra_continuations WHERE decision_request_id=?;

-- name: ListClearDevPlanningExtraContinuations :many
SELECT * FROM cleardev_planning_extra_continuations WHERE requirement_id=? ORDER BY rowid;

-- name: InsertClearDevPlanningExtraContinuation :exec
INSERT INTO cleardev_planning_extra_continuations (id,requirement_id,logical_step_id,decision_request_id,third_attempt_id,old_step_json,original_status,original_reason,original_summary,original_stopped_at,supplement,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?);

-- name: ListClearDevPlanningExtraGrants :many
SELECT grant.* FROM cleardev_planning_extra_grants grant JOIN cleardev_planning_extra_requests intent ON intent.decision_request_id=grant.decision_request_id WHERE intent.requirement_id=?;
