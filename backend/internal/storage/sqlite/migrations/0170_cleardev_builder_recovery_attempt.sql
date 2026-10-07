-- The controlled recovery of one budget-exhausted builder round reopens that
-- blocked attempt as its own rework successor. The attempt update trigger
-- refused every transition out of BLOCKED, so the settlement could grant the
-- extra turn without ever being able to run the round again. The widened
-- trigger admits exactly one edge: BLOCKED -> REWORK for the recorded
-- BUILDER_BUDGET_EXHAUSTED round of a task whose builder budget carries a
-- human-authorized extra turn. Every other frozen field stays immutable.

-- +goose NO TRANSACTION
-- +goose Up
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='BLOCKED' AND NOT(NEW.status='REWORK' AND OLD.reason_code='BUILDER_BUDGET_EXHAUSTED' AND OLD.settled_at IS NOT NULL
      AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;

-- +goose Down
-- Refuse while any stored attempt already uses the recovery edge.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_builder_recovery_down_guard;
CREATE TEMP TABLE cleardev_builder_recovery_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_builder_recovery_down_guard_valid BEFORE INSERT ON cleardev_builder_recovery_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses stored builder recovery reopens'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_builder_recovery_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE status='REWORK' AND reason_code='BUILDER_BUDGET_EXHAUSTED')
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budget_occupancies WHERE round_key IS NOT NULL)
 OR EXISTS(SELECT 1 FROM (SELECT task_mapping_id FROM cleardev_complex_execution_verified_candidates GROUP BY task_mapping_id HAVING count(*)>1))
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_builder_recovery_down_guard;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
