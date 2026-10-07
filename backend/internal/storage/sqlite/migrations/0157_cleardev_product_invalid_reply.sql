-- A rejected protocol reply is a terminal fact for its discussion round, not a
-- retryable send. The stored constraint only admitted
-- PRODUCT_STEWARD_WORKSPACE_CHANGED as a settled failure, so a non-conforming
-- Steward reply could never settle and its pending row then blocked the next
-- user message. Widen the failure-branch whitelist exactly like 0007 widened
-- the harness CHECK: SQLite cannot ALTER a CHECK, and rebuilding
-- cleardev_product_discussions would drag in the stages/contexts/execution
-- foreign keys and every dependent trigger. Successful-result rows keep their
-- original rule and all stored history bytes are unchanged. The replaced text
-- omits surrounding parentheses so the CHECK keeps its exact grouping.
-- These writable_schema steps run without a transaction like 0007; each guard
-- table is dropped and recreated so a failed guard leaves no reusable state.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'failure_reason=''PRODUCT_STEWARD_WORKSPACE_CHANGED''',
    'failure_reason IN (''PRODUCT_STEWARD_WORKSPACE_CHANGED'',''PRODUCT_DISCOVERY_INVALID'')'
)
WHERE type = 'table' AND name = 'cleardev_product_discussions';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_product_invalid_reply_guard;
CREATE TEMP TABLE cleardev_product_invalid_reply_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_product_invalid_reply_guard_valid BEFORE INSERT ON cleardev_product_invalid_reply_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'product discussion failure constraint was not widened'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_product_invalid_reply_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_product_discussions'
        AND sql LIKE '%''PRODUCT_DISCOVERY_INVALID''%')
THEN 1 ELSE 0 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_product_invalid_reply_guard;
-- +goose StatementEnd

-- +goose Down
-- The guard repeats 0156's history checks because goose commits each Down
-- separately: without them this constraint would already narrow before a later
-- Down guard rejects the downgrade, leaving a partially rolled-back schema.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_product_invalid_reply_guard;
CREATE TEMP TABLE cleardev_product_invalid_reply_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_product_invalid_reply_guard_valid BEFORE INSERT ON cleardev_product_invalid_reply_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses recorded invalid replies or downstream-guarded history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_product_invalid_reply_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
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
DROP TABLE cleardev_product_invalid_reply_guard;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(
    sql,
    'failure_reason IN (''PRODUCT_STEWARD_WORKSPACE_CHANGED'',''PRODUCT_DISCOVERY_INVALID'')',
    'failure_reason=''PRODUCT_STEWARD_WORKSPACE_CHANGED'''
)
WHERE type = 'table' AND name = 'cleardev_product_discussions';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_product_invalid_reply_guard;
CREATE TEMP TABLE cleardev_product_invalid_reply_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_product_invalid_reply_guard_valid BEFORE INSERT ON cleardev_product_invalid_reply_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'product discussion failure constraint was not restored'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_product_invalid_reply_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_product_discussions'
        AND sql LIKE '%''PRODUCT_DISCOVERY_INVALID''%')
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_product_invalid_reply_guard;
-- +goose StatementEnd
