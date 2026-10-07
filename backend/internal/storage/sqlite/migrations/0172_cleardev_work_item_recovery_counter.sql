-- The authorized recovery of one builder round advances the work item's
-- rework counter past its frozen maximum, and the work item CHECK
-- (rework_count <= max_rework_count) rolled every recovery approval back.
-- The counter may now reach the frozen maximum plus the budget guard's hard
-- authorization ceiling of two: the per-approval authorization still lives in
-- the builder budget CAS, the dispatch selector still caps rounds by that
-- same budget, and every other flow keeps its own transition guards.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'rework_count >= 0 AND rework_count <= max_rework_count', 'rework_count >= 0 AND rework_count <= max_rework_count + 2')
WHERE type = 'table' AND name = 'cleardev_work_items';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_recovery_counter_guard;
CREATE TEMP TABLE cleardev_recovery_counter_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_recovery_counter_guard_valid BEFORE INSERT ON cleardev_recovery_counter_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'work item recovery counter bound was not widened'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_recovery_counter_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_work_items' AND sql LIKE '%rework_count <= max_rework_count + 2%')
THEN 1 ELSE 0 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_recovery_counter_guard;
-- +goose StatementEnd

-- +goose Down
-- Refuse while any stored work item already relies on the wider bound.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_recovery_counter_down_guard;
CREATE TEMP TABLE cleardev_recovery_counter_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_recovery_counter_down_guard_valid BEFORE INSERT ON cleardev_recovery_counter_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses stored recovery counters'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_recovery_counter_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_work_items WHERE rework_count > max_rework_count)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE status='REWORK' AND reason_code='BUILDER_BUDGET_EXHAUSTED')
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
DROP TABLE cleardev_recovery_counter_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'rework_count <= max_rework_count + 2', 'rework_count <= max_rework_count')
WHERE type = 'table' AND name = 'cleardev_work_items';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
