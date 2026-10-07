-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
 AND NOT(OLD.status IN('REWORK','BLOCKED') AND OLD.reason_code='CHECKER_UNAVAILABLE' AND OLD.settled_at IS NOT NULL
 AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
 SELECT 1 FROM cleardev_stopped_check_requests offer JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id
 JOIN cleardev_workflow_recoveries recovery ON recovery.target_id=offer.original_check_id
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=offer.retry_check_id
 WHERE json_extract(offer.binding_json,'$.dispatchId')=OLD.id AND offer.execution_run_id=OLD.execution_run_id
 AND recovery.action='RETRY_CHECK' AND recovery.dispatch_id=OLD.id AND recovery.successor_id=retry.id
 AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at
 AND retry.status='PENDING' AND retry.task_attempt_id=OLD.id AND retry.retry_ordinal=1))
     AND NOT(OLD.status IN('BLOCKED','NEEDS_HUMAN') AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action IN('RETRY_REVIEW','RETRY_CHECK') AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at)) AND NOT((OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED'
 OR (OLD.status='NEEDS_HUMAN' AND OLD.reason_code='BUILDER_WORKTREE_DIRTY'
  AND EXISTS(SELECT 1 FROM cleardev_complex_execution_agent_steps step WHERE step.id=OLD.agent_step_id AND step.send_status='PENDING' AND step.sent_at IS NULL AND step.turn_id IS NULL)
  AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.step_id=OLD.agent_step_id AND recovery.action='RETRY_BUILDER_SESSION' AND recovery.original_reason=OLD.reason_code AND recovery.original_status=OLD.status AND recovery.original_stopped_at=OLD.settled_at AND recovery.working_tree_sha256<>'' AND recovery.provider_conversation_id<>'')))
 AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action='RETRY_BUILDER_SESSION' AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code IN('BUILDER_BUDGET_EXHAUSTED','BUILDER_BLOCKED') AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at))
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id WHERE json_extract(r.binding_json,'$.dispatchId')=OLD.id AND h.logical_step_id=OLD.agent_step_id AND r.original_stopped_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
CREATE TABLE cleardev_unsent_workspace_down_guard(remaining INTEGER CHECK(remaining=0));
INSERT INTO cleardev_unsent_workspace_down_guard SELECT count(*) FROM cleardev_workflow_recoveries WHERE action='RETRY_BUILDER_SESSION' AND original_reason='BUILDER_WORKTREE_DIRTY';
DROP TABLE cleardev_unsent_workspace_down_guard;
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
 AND NOT(OLD.status IN('REWORK','BLOCKED') AND OLD.reason_code='CHECKER_UNAVAILABLE' AND OLD.settled_at IS NOT NULL
 AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
 SELECT 1 FROM cleardev_stopped_check_requests offer JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id
 JOIN cleardev_workflow_recoveries recovery ON recovery.target_id=offer.original_check_id
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=offer.retry_check_id
 WHERE json_extract(offer.binding_json,'$.dispatchId')=OLD.id AND offer.execution_run_id=OLD.execution_run_id
 AND recovery.action='RETRY_CHECK' AND recovery.dispatch_id=OLD.id AND recovery.successor_id=retry.id
 AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at
 AND retry.status='PENDING' AND retry.task_attempt_id=OLD.id AND retry.retry_ordinal=1))
     AND NOT(OLD.status IN('BLOCKED','NEEDS_HUMAN') AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action IN('RETRY_REVIEW','RETRY_CHECK') AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action='RETRY_BUILDER_SESSION' AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code IN('BUILDER_BUDGET_EXHAUSTED','BUILDER_BLOCKED') AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at))
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id WHERE json_extract(r.binding_json,'$.dispatchId')=OLD.id AND h.logical_step_id=OLD.agent_step_id AND r.original_stopped_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
-- +goose StatementEnd
