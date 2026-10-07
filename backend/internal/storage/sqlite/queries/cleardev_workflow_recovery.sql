-- name: ListClearDevWorkflowRecoveries :many
SELECT * FROM cleardev_workflow_recoveries WHERE execution_run_id=? ORDER BY created_at,id;

-- name: GetClearDevWorkflowRecovery :one
SELECT * FROM cleardev_workflow_recoveries WHERE id=?;

-- name: InsertClearDevWorkflowRecovery :exec
INSERT INTO cleardev_workflow_recoveries(id,execution_run_id,action,target_id,dispatch_id,task_id,step_id,binding_id,successor_id,candidate_sha,provider_conversation_id,working_tree_sha256,original_stopped_at,original_status,original_reason,original_summary,supplement,created_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);
