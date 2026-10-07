-- An explicit second attempt preserves the original technical STOP.
-- name: GetClearDevPlannerCoordinationRecovery :one
SELECT * FROM cleardev_planner_runtime_recoveries WHERE id=?;

-- name: GetClearDevPlannerCoordinationRecoveryForEvent :one
SELECT * FROM cleardev_planner_runtime_recoveries WHERE event_id=?;

-- name: GetClearDevPlannerCoordinationRecoveryForStep :one
SELECT * FROM cleardev_planner_runtime_recoveries WHERE logical_step_id=?;

-- name: ListClearDevPlannerCoordinationRecoveries :many
SELECT * FROM cleardev_planner_runtime_recoveries WHERE execution_run_id=? ORDER BY created_at,id;

-- name: InsertClearDevPlannerCoordinationRecovery :exec
INSERT INTO cleardev_planner_runtime_recoveries
(id,event_id,requirement_id,execution_run_id,logical_step_id,first_attempt_id,failure_event_id,second_attempt_id,binding_json,old_step_json,original_stopped_at,original_summary,supplement,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevPlannerCoordinationRecoveryDecision :one
SELECT * FROM cleardev_planner_runtime_recovery_decisions WHERE event_id=?;

-- name: ListClearDevPlannerCoordinationRecoveryDecisions :many
SELECT * FROM cleardev_planner_runtime_recovery_decisions WHERE execution_run_id=? ORDER BY created_at,event_id;

-- name: InsertClearDevPlannerCoordinationRecoveryDecision :exec
INSERT INTO cleardev_planner_runtime_recovery_decisions
(event_id,execution_run_id,source,outcome,reason_code,result_json,result_sha256,summary,created_at)
VALUES(?,?,?,?,?,?,?,?,?);

-- name: GetClearDevPlannerRecoveryNativeMessage :one
SELECT message.text AS text, turn.id AS turn_id, turn.state AS native_state,
 terminal.id AS terminal_event_id, terminal.received_at AS terminal_received_at, turn.requested_at
FROM conversation_messages message
JOIN conversation_turns turn ON turn.id=message.turn_id AND turn.conversation_id=message.conversation_id
LEFT JOIN conversation_provider_events terminal ON turn.state='completed'
 AND terminal.conversation_id=turn.conversation_id AND terminal.session_id=turn.handled_by_session_id
 AND terminal.method='turn.completed' AND terminal.provider_event_id NOT LIKE 'acp-history:%'
 AND json_valid(terminal.payload_json)
 AND json_extract(terminal.payload_json,'$.kind')='turn.completed'
 AND json_extract(terminal.payload_json,'$.providerTurnId')=turn.provider_turn_id
 AND json_extract(terminal.payload_json,'$.turnState')='completed'
WHERE message.client_message_id=? AND message.role='user' AND message.origin='automation'
AND turn.id=? AND turn.handled_by_session_id=?
AND NOT EXISTS(SELECT 1 FROM conversation_messages answer WHERE answer.turn_id=turn.id AND answer.role='assistant')
AND (turn.state IN('failed','interrupted') OR (turn.state='completed' AND terminal.id IS NOT NULL))
AND turn.completed_at IS NOT NULL AND turn.rolled_back_at IS NULL
ORDER BY terminal.id LIMIT 1;

-- name: ListClearDevEffectivePlannerStoppedEvents :many
SELECT event.* FROM cleardev_planner_runtime_events event
JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=event.id
WHERE decision.source='CONTROL_PLANE' AND decision.outcome='STOP'
ORDER BY event.created_at,event.id;

-- name: ReopenClearDevPlannerCoordinationStep :execrows
UPDATE cleardev_complex_agent_steps
SET send_status='PENDING',sent_at=NULL,turn_id=NULL,final_message_id=NULL,final_message_text=NULL,message_sha256=NULL,completed_at=NULL,failed_at=NULL,reason_code=''
WHERE cleardev_complex_agent_steps.id=sqlc.arg(id) AND send_status=sqlc.arg(original_status) AND reason_code=sqlc.arg(original_reason)
 AND send_status='FAILED' AND step_kind='COMPLEX_ENGINEERING_PLAN'
 AND EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r WHERE r.logical_step_id=cleardev_complex_agent_steps.id);
