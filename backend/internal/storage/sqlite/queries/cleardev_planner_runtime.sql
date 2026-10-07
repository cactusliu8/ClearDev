-- name: InsertClearDevPlannerRuntimeEvent :exec
INSERT INTO cleardev_planner_runtime_events
(id,execution_run_id,dispatch_id,source_step_id,source_message_sha256,report_json,created_at)
VALUES(?,?,?,?,?,?,?);

-- name: GetClearDevPlannerRuntimeEvent :one
SELECT * FROM cleardev_planner_runtime_events WHERE id=?;

-- name: ListClearDevPlannerRuntimeEvents :many
SELECT * FROM cleardev_planner_runtime_events WHERE execution_run_id=? ORDER BY created_at,id;

-- name: InsertClearDevPlannerRuntimeRequest :exec
INSERT INTO cleardev_planner_runtime_requests
(event_id,execution_run_id,ordinal,agent_step_id,planner_role_binding_id,ao_session_id,context_json,context_sha256,prompt,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevPlannerRuntimeRequest :one
SELECT * FROM cleardev_planner_runtime_requests WHERE event_id=?;

-- name: ListClearDevPlannerRuntimeRequests :many
SELECT * FROM cleardev_planner_runtime_requests WHERE execution_run_id=? ORDER BY ordinal;

-- name: InsertClearDevPlannerRuntimeDecision :exec
INSERT INTO cleardev_planner_runtime_decisions
(event_id,execution_run_id,source,outcome,reason_code,result_json,result_sha256,summary,created_at)
VALUES(?,?,?,?,?,?,?,?,?);

-- name: GetClearDevPlannerRuntimeDecision :one
SELECT * FROM cleardev_planner_runtime_decisions WHERE event_id=?;

-- name: ListClearDevPlannerRuntimeDecisions :many
SELECT * FROM cleardev_planner_runtime_decisions WHERE execution_run_id=? ORDER BY created_at,event_id;

-- name: InsertClearDevPlannerRuntimeTaskAmendment :exec
INSERT INTO cleardev_planner_runtime_task_amendments
(id,event_id,execution_run_id,task_mapping_id,ordinal,previous_package_sha256,task_packet_json,task_packet_sha256,created_at)
VALUES(?,?,?,?,?,?,?,?,?);

-- name: ListClearDevPlannerRuntimeTaskAmendments :many
SELECT * FROM cleardev_planner_runtime_task_amendments WHERE execution_run_id=? ORDER BY task_mapping_id,ordinal;

-- name: GetClearDevPlannerRuntimeEffectiveTask :one
SELECT id,execution_run_id,plan_task_key,work_item_id,ordinal,
       CAST(task_packet_json AS TEXT) AS task_packet_json,
       CAST(task_packet_sha256 AS TEXT) AS task_packet_sha256
FROM cleardev_planner_runtime_effective_tasks WHERE id=?;

-- name: GetClearDevPlannerRuntimeSourceAttempt :one
SELECT attempt.* FROM cleardev_complex_execution_task_attempts attempt WHERE attempt.agent_step_id=?;

-- name: GetClearDevPlannerRuntimePlan :one
SELECT * FROM cleardev_complex_engineering_plans WHERE id=?;

-- name: CountClearDevPlannerRuntimeActiveAttempts :one
SELECT count(*) FROM cleardev_complex_execution_task_attempts
WHERE execution_run_id=? AND status IN('PENDING','RUNNING','OBSERVED','REVIEWING');

-- name: CountClearDevPlannerRuntimeUnresolvedStops :one
SELECT count(*) FROM cleardev_complex_execution_task_attempts attempt
JOIN cleardev_work_items item ON item.complex_execution_task_id=attempt.task_mapping_id
WHERE attempt.execution_run_id=? AND attempt.status IN('BLOCKED','NEEDS_HUMAN','FAILED')
AND item.state IN('BLOCKED','NEEDS_HUMAN');

-- name: CountClearDevPlannerRuntimeTaskAttempts :one
SELECT count(*) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=?;

-- name: CountClearDevPlannerRuntimeTaskReservations :one
SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=?;

-- name: CountClearDevPlannerRuntimeBarriers :one
SELECT count(*) FROM cleardev_planner_runtime_barriers WHERE execution_run_id=?;

-- name: CountClearDevPlannerRuntimeDirectionIntents :one
SELECT count(*) FROM cleardev_direction_intents WHERE requirement_version_id=?;

-- name: ListClearDevPlannerRuntimeStoppedEvents :many
SELECT event.* FROM cleardev_planner_runtime_events event
JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=event.id
WHERE decision.source='CONTROL_PLANE' AND decision.outcome='STOP'
ORDER BY event.created_at, event.id;
