-- The independent review proved the authorization still could not be spent:
-- 0161 widened the update guard, but the table's own column CHECK
-- (used_turns<=max_turns) kept refusing the extra occupancy. SQLite cannot
-- ALTER a CHECK, so the stored CREATE TABLE text is widened the same way 0007
-- widened the harness CHECK. Every stored row and every other constraint
-- stays byte-identical.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK(used_turns>=0 AND used_turns<=max_turns)',
    'CHECK(used_turns>=0 AND used_turns<=max_turns+authorized_extra_turns)'
)
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_review_budget_check_guard;
CREATE TEMP TABLE cleardev_review_budget_check_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_review_budget_check_guard_valid BEFORE INSERT ON cleardev_review_budget_check_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'review budget occupancy check was not widened'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_review_budget_check_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_complex_exception_budgets'
        AND sql LIKE '%used_turns<=max_turns+authorized_extra_turns%')
THEN 1 ELSE 0 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_review_budget_check_guard;
-- +goose StatementEnd

-- +goose Down
-- The guard repeats the downstream history checks because goose commits each
-- Down separately; without them this file would narrow the CHECK before a
-- later guard refuses the downgrade.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_review_budget_check_down_guard;
CREATE TEMP TABLE cleardev_review_budget_check_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_review_budget_check_down_guard_valid BEFORE INSERT ON cleardev_review_budget_check_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_review_budget_check_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_review_budget_check_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'CHECK(used_turns>=0 AND used_turns<=max_turns+authorized_extra_turns)',
    'CHECK(used_turns>=0 AND used_turns<=max_turns)'
)
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
