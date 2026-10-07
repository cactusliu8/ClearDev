-- 0191: the granted repair horizon reaches the attempts table CHECK.
--
-- 0188 taught the attempt-insert trigger to accept rounds 7-12 of a task
-- whose builder budget carries a human coordination repair grant, but the
-- attempts table kept the CHECK bound that 0171 only widened to 0..6.
-- SQLite evaluates a table CHECK before any trigger, so every granted round
-- 7-12 dispatch died as "CHECK constraint failed: round BETWEEN 0 AND 6"
-- and the approved repair never reached its Builder round. The CHECK now
-- matches the guarded trigger horizon; the 0188 trigger stays the gate that
-- demands the recorded grant for rounds 7-12. Mail attempt slots keep their
-- own 0..4 bound.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'round BETWEEN 0 AND 6', 'round BETWEEN 0 AND 12')
WHERE type = 'table' AND name = 'cleardev_complex_execution_task_attempts';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_repair_round_guard;
CREATE TEMP TABLE cleardev_repair_round_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_repair_round_guard_valid BEFORE INSERT ON cleardev_repair_round_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'repair round bound was not widened'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_repair_round_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_complex_execution_task_attempts' AND sql LIKE '%round BETWEEN 0 AND 12%')
 AND EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND name='cleardev_complex_execution_attempt_insert_valid'
        AND sql LIKE '%NEW.round BETWEEN 7 AND 12%')
THEN 1 ELSE 0 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_repair_round_guard;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_repair_round_down_guard;
CREATE TEMP TABLE cleardev_repair_round_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_repair_round_down_guard_valid BEFORE INSERT ON cleardev_repair_round_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'granted repair rounds would be lost'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_repair_round_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE round>6)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_dispatches)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_reopens)
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config,'{}'),'$.cleardev')='object')
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_repair_round_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'round BETWEEN 0 AND 12', 'round BETWEEN 0 AND 6')
WHERE type = 'table' AND name = 'cleardev_complex_execution_task_attempts';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
