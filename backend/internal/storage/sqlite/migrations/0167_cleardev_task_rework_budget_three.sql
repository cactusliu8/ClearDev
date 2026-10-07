-- The task-level rework budget grows from one round to three: a reviewed task
-- may be sent back up to three times before it stops for a person. The stored
-- CHECK physically froze the value at one, so the table text is widened the
-- same way 0007 and 0162 widened theirs. Existing rows keep their value of
-- one, which stays valid under the new rule.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'max_rework_count=1)', 'max_rework_count IN (1,3))')
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_task_rework_budget_guard;
CREATE TEMP TABLE cleardev_task_rework_budget_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_task_rework_budget_guard_valid BEFORE INSERT ON cleardev_task_rework_budget_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'task rework budget constraint was not widened'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_task_rework_budget_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_complex_exception_budgets'
        AND sql LIKE '%max_rework_count IN (1,3))%')
THEN 1 ELSE 0 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_task_rework_budget_guard;
-- +goose StatementEnd

-- +goose Down
-- Refuse while any stored budget already relies on the wider rule.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_task_rework_budget_down_guard;
CREATE TEMP TABLE cleardev_task_rework_budget_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_task_rework_budget_down_guard_valid BEFORE INSERT ON cleardev_task_rework_budget_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses stored three-round rework budgets'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_task_rework_budget_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE max_rework_count>1)
 OR EXISTS(SELECT 1 FROM (SELECT task_mapping_id FROM cleardev_complex_execution_verified_candidates GROUP BY task_mapping_id HAVING count(*)>1))
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budget_occupancies WHERE round_key IS NOT NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_task_rework_budget_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'max_rework_count IN (1,3))', 'max_rework_count=1)')
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
