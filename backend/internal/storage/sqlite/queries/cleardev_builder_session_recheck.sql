-- name: InsertClearDevBuilderSessionCheck :execrows
INSERT INTO cleardev_builder_session_checks(recovery_id,checkpoint,stage,outcome,reason_code,preflight_id,binding_sha256,checked_at)
VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(recovery_id,checkpoint) DO NOTHING;

-- name: GetClearDevBuilderSessionCheck :one
SELECT * FROM cleardev_builder_session_checks WHERE recovery_id=? AND checkpoint=?;

-- name: ListClearDevBuilderSessionChecks :many
SELECT checks.* FROM cleardev_builder_session_checks checks
JOIN cleardev_workflow_recoveries recovery ON recovery.id=checks.recovery_id
WHERE recovery.execution_run_id=?
ORDER BY checks.rowid;

-- name: CountClearDevBuilderSessionOpenTurns :one
SELECT count(*) FROM conversation_turns
WHERE handled_by_session_id=? AND state NOT IN ('completed','failed','interrupted');

-- name: GetLatestClearDevBuilderSessionRecoveryForStep :one
SELECT * FROM cleardev_workflow_recoveries
WHERE step_id=? AND action='RETRY_BUILDER_SESSION' ORDER BY rowid DESC LIMIT 1;

-- name: GetClearDevBuilderSessionCheckForStep :many
SELECT checks.* FROM cleardev_builder_session_checks checks
JOIN cleardev_workflow_recoveries recovery ON recovery.id=checks.recovery_id
WHERE recovery.step_id=?
AND recovery.id=(SELECT id FROM cleardev_workflow_recoveries latest
 WHERE latest.step_id=recovery.step_id AND latest.action='RETRY_BUILDER_SESSION' ORDER BY latest.rowid DESC LIMIT 1)
ORDER BY checks.rowid;
