-- Three-round task rework dispatches rounds two and three as real attempts.
-- The attempt insert trigger froze non-mail rounds at 0 and 1, so the widened
-- budget tables alone could never run the later rounds. The bound moves to
-- 0..6: three rework rounds plus the two human-authorized recovery turns,
-- always gated by the budget CAS and the dispatch selector. Mail runs keep
-- their slot-validated rounds.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'AND NEW.round NOT IN(0,1))', 'AND NEW.round NOT IN(0,1,2,3,4,5,6))')
WHERE type = 'trigger' AND name = 'cleardev_complex_execution_attempt_insert_valid';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_rework_round_guard;
CREATE TEMP TABLE cleardev_rework_round_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_rework_round_guard_valid BEFORE INSERT ON cleardev_rework_round_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'rework round constraint was not widened'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_rework_round_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND name='cleardev_complex_execution_attempt_insert_valid'
        AND sql LIKE '%NEW.round NOT IN(0,1,2,3,4,5,6))%')
THEN 1 ELSE 0 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_rework_round_guard;
-- +goose StatementEnd

-- +goose Down
-- Refuse while any stored attempt already relies on the wider rule.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_rework_round_down_guard;
CREATE TEMP TABLE cleardev_rework_round_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_rework_round_down_guard_valid BEFORE INSERT ON cleardev_rework_round_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses stored rework rounds beyond one'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_rework_round_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE round>1 AND status NOT IN ('BLOCKED','FAILED'))
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE round>1)
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE mode='STANDARD' AND json_extract(execution_package_json,'$.deliveryPolicy') IS NULL)
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
DROP TABLE cleardev_rework_round_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'AND NEW.round NOT IN(0,1,2,3,4,5,6))', 'AND NEW.round NOT IN(0,1))')
WHERE type = 'trigger' AND name = 'cleardev_complex_execution_attempt_insert_valid';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
