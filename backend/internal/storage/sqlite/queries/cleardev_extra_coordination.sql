-- name: GetClearDevExtraCoordinationRequest :one
SELECT * FROM cleardev_extra_coordination_requests WHERE id=?;

-- name: GetClearDevExtraCoordinationRequestForEvent :one
SELECT * FROM cleardev_extra_coordination_requests WHERE event_id=?;

-- name: GetClearDevExtraCoordinationRequestForRun :one
SELECT * FROM cleardev_extra_coordination_requests WHERE execution_run_id=?;

-- name: GetClearDevExtraCoordinationRequestForStep :one
SELECT offer.* FROM cleardev_extra_coordination_requests offer
JOIN cleardev_planner_runtime_requests request ON request.event_id=offer.event_id
WHERE request.agent_step_id=?;

-- name: InsertClearDevExtraCoordinationRequest :exec
INSERT INTO cleardev_extra_coordination_requests(id,execution_run_id,event_id,decision_request_id,binding_json,context_json,prompt,prompt_sha256,supplement,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevExtraCoordinationGrant :one
SELECT * FROM cleardev_extra_coordination_grants WHERE event_id=?;

-- name: ListClearDevExtraCoordinationGrants :many
SELECT * FROM cleardev_extra_coordination_grants WHERE execution_run_id=? ORDER BY created_at,event_id;

-- name: InsertClearDevExtraCoordinationGrant :exec
INSERT INTO cleardev_extra_coordination_grants(event_id,execution_run_id,decision_request_id,created_at) VALUES(?,?,?,?);

-- name: GetClearDevExtraCoordinationDecision :one
SELECT * FROM cleardev_extra_coordination_decisions WHERE event_id=?;

-- name: ListClearDevExtraCoordinationDecisions :many
SELECT * FROM cleardev_extra_coordination_decisions WHERE execution_run_id=? ORDER BY created_at,event_id;

-- name: InsertClearDevExtraCoordinationDecision :exec
INSERT INTO cleardev_extra_coordination_decisions(event_id,execution_run_id,source,outcome,reason_code,result_json,result_sha256,summary,created_at)
VALUES(?,?,?,?,?,?,?,?,?);

-- name: ListClearDevPlannerBarrierEvents :many
SELECT event_id FROM cleardev_planner_runtime_barriers WHERE execution_run_id=? ORDER BY event_id;
