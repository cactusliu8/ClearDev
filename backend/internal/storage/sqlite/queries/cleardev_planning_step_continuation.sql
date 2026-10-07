-- name: GetClearDevPlanningContinuationStep :one
SELECT step.* FROM cleardev_complex_agent_steps step
JOIN cleardev_complex_role_bindings role ON role.id=step.role_binding_id
WHERE role.development_project_id=? AND step.step_kind='REQUIREMENT_COMPILATION'
ORDER BY step.rowid DESC LIMIT 1;

-- name: CountClearDevPlanningContinuationConflicts :one
SELECT
 (SELECT count(*) FROM cleardev_contract_versions v WHERE v.development_project_id=sqlc.arg(requirement_id) AND v.state='APPROVED')
 +(SELECT count(*) FROM cleardev_complex_engineering_plans p WHERE p.development_project_id=sqlc.arg(requirement_id))
 +(SELECT count(*) FROM cleardev_complex_execution_runs r WHERE r.development_project_id=sqlc.arg(requirement_id))
 +(SELECT count(*) FROM cleardev_direction_intents d WHERE d.development_project_id=sqlc.arg(requirement_id));

-- name: IsClearDevPlanningContinuationSession :one
SELECT EXISTS(SELECT 1 FROM cleardev_project_tool_sessions WHERE session_id=?);

-- name: GetClearDevPlanningStepRecovery :one
SELECT * FROM cleardev_planning_step_recoveries WHERE id=?;

-- name: GetClearDevPlanningStepRecoveryForStep :one
SELECT * FROM cleardev_planning_step_recoveries WHERE logical_step_id=?;

-- name: ListClearDevPlanningStepRecoveries :many
SELECT * FROM cleardev_planning_step_recoveries WHERE requirement_id=? ORDER BY rowid;

-- name: InsertClearDevPlanningStepRecovery :exec
INSERT INTO cleardev_planning_step_recoveries
(id,requirement_id,logical_step_id,first_attempt_id,failure_event_id,second_attempt_id,binding_json,old_step_json,old_discussion_json,original_status,original_reason,original_summary,original_stopped_at,supplement,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);

-- name: ReopenClearDevStoppedProductStep :execrows
UPDATE cleardev_complex_agent_steps
SET send_status='PENDING',sent_at=NULL,turn_id=NULL,final_message_id=NULL,final_message_text=NULL,message_sha256=NULL,completed_at=NULL,failed_at=NULL,reason_code=''
WHERE id=sqlc.arg(id) AND send_status=sqlc.arg(original_status) AND reason_code=sqlc.arg(original_reason)
 AND send_status IN ('SENT','FAILED') AND step_kind='REQUIREMENT_COMPILATION';

-- name: GetClearDevFailedDiscussionSnapshot :one
SELECT CAST(json_object('id',discussion.id,'product_id',discussion.product_id,'ordinal',discussion.ordinal,'user_message',discussion.user_message,'agent_step_id',discussion.agent_step_id,'result_json',discussion.result_json,'result_sha256',discussion.result_sha256,'failure_reason',discussion.failure_reason,'created_at',discussion.created_at,'settled_at',discussion.settled_at) AS TEXT) AS snapshot_json FROM cleardev_product_discussions discussion
WHERE id=? AND failure_reason='PRODUCT_DISCOVERY_INVALID' AND settled_at IS NOT NULL AND result_json IS NULL;

-- name: ReopenClearDevFailedDiscussion :execrows
UPDATE cleardev_product_discussions SET settled_at=NULL,failure_reason=NULL
WHERE id=? AND failure_reason='PRODUCT_DISCOVERY_INVALID' AND settled_at IS NOT NULL AND result_json IS NULL;
