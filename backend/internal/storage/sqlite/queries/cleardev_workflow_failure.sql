-- name: InsertClearDevWorkflowFailureReceipt :exec
INSERT INTO cleardev_workflow_failure_receipts(id,requirement_id,execution_run_id,source_kind,source_id,receipt_json,receipt_sha256,created_at)
VALUES(?,?,?,?,?,?,?,?);

-- name: GetClearDevWorkflowFailureReceipt :one
SELECT * FROM cleardev_workflow_failure_receipts WHERE id=?;

-- name: ListClearDevWorkflowFailureReceipts :many
SELECT * FROM cleardev_workflow_failure_receipts WHERE requirement_id=? ORDER BY created_at,id;

-- name: InsertClearDevFailureCoordinationSource :exec
INSERT INTO cleardev_failure_coordination_sources(event_id,execution_run_id,dispatch_id,source_step_id,source_message_sha256,working_tree_sha256,failure_receipt_id,bridge_kind,created_at)
VALUES(?,?,?,?,?,?,?,?,?);

-- name: GetClearDevFailureCoordinationSource :one
SELECT * FROM cleardev_failure_coordination_sources WHERE event_id=?;

-- name: GetClearDevUnpublishedFailureCoordinationForTask :one
SELECT bridge.* FROM cleardev_failure_coordination_sources bridge
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=bridge.dispatch_id
JOIN cleardev_work_items item ON item.complex_execution_task_id=attempt.task_mapping_id
WHERE attempt.execution_run_id=? AND attempt.task_mapping_id=?
 AND item.state IN('BLOCKED','NEEDS_HUMAN') AND item.rework_count=attempt.round
 AND attempt.round=(SELECT max(round) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=attempt.task_mapping_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_candidate_commits WHERE complex_execution_task_attempt_id=attempt.id);
