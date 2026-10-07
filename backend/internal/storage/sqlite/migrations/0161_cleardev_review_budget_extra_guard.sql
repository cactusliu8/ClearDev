-- The budget update guard from 0132 only admitted used_turns growing within
-- max_turns, so both the extra-turn authorization itself and any occupancy
-- under that authorization were refused. The guard now admits exactly two
-- shapes: one occupancy step that stays within max_turns plus the authorized
-- extra turns, or one authorization step that changes nothing else. Every
-- other column stays immutable and every other update stays refused.

-- +goose Up
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_exception_budget_update_valid;
CREATE TRIGGER cleardev_complex_exception_budget_update_valid BEFORE UPDATE ON cleardev_complex_exception_budgets
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.complex_execution_task_id IS NOT NEW.complex_execution_task_id OR OLD.role_kind IS NOT NEW.role_kind OR OLD.allowed_agent_types_json IS NOT NEW.allowed_agent_types_json OR OLD.model_selection IS NOT NEW.model_selection OR OLD.max_turns IS NOT NEW.max_turns OR OLD.max_rework_count IS NOT NEW.max_rework_count OR OLD.created_at IS NOT NEW.created_at
 OR NOT (
   (NEW.used_turns=OLD.used_turns+1 AND NEW.used_turns<=NEW.max_turns+NEW.authorized_extra_turns AND NEW.authorized_extra_turns=OLD.authorized_extra_turns)
   OR (NEW.authorized_extra_turns=OLD.authorized_extra_turns+1 AND NEW.used_turns=OLD.used_turns AND NEW.authorized_extra_turns<=2)
 )
BEGIN SELECT RAISE(ABORT,'complex exception budget is immutable or exhausted'); END;
-- +goose StatementEnd

-- +goose Down
-- Restoring the strict guard only affects future updates; recorded
-- authorizations and occupancies stay their own immutable history. Guard the
-- downgrade like the other project migrations because goose commits each Down
-- separately.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_review_budget_guard_down_guard;
CREATE TEMP TABLE cleardev_review_budget_guard_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_review_budget_guard_down_guard_valid BEFORE INSERT ON cleardev_review_budget_guard_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_review_budget_guard_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_review_budget_guard_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_exception_budget_update_valid;
CREATE TRIGGER cleardev_complex_exception_budget_update_valid BEFORE UPDATE ON cleardev_complex_exception_budgets
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.complex_execution_task_id IS NOT NEW.complex_execution_task_id OR OLD.role_kind IS NOT NEW.role_kind OR OLD.allowed_agent_types_json IS NOT NEW.allowed_agent_types_json OR OLD.model_selection IS NOT NEW.model_selection OR OLD.max_turns IS NOT NEW.max_turns OR OLD.max_rework_count IS NOT NEW.max_rework_count OR OLD.created_at IS NOT NEW.created_at OR NEW.used_turns<>OLD.used_turns+1 OR NEW.used_turns>NEW.max_turns
BEGIN SELECT RAISE(ABORT,'complex exception budget is immutable or exhausted'); END;
-- +goose StatementEnd
