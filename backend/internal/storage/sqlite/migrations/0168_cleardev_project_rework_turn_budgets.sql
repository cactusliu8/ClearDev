-- Three-round task rework needs three rework turns per role on top of the
-- initial round: a generic Builder runs up to four rounds and its Reviewer
-- reviews up to four candidates, one turn each now that a round's follow-up
-- messages are free. The stored CHECK froze the generic shapes at three and
-- two turns, so the table text is widened the same way 0132 and 0162 widened
-- theirs. The historical mail shapes (five and six) stay valid; existing rows
-- keep their values.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'max_turns IN (3,5) AND max_rework_count IN (1,3)', 'max_turns IN (3,4,5) AND max_rework_count IN (1,3)')
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'max_turns IN (2,6))', 'max_turns IN (2,4,6))')
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
-- The mail-policy insert trigger froze the historical turn shapes; it is
-- recreated so a three-round rework policy may freeze four turns per role.
DROP TRIGGER IF EXISTS cleardev_mail_role_budget_insert;
CREATE TRIGGER cleardev_mail_role_budget_insert BEFORE INSERT ON cleardev_complex_exception_budgets
WHEN (NEW.role_kind='BUILDER' AND NEW.max_turns<>CASE WHEN EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) THEN 5 WHEN NEW.max_rework_count=3 THEN 4 ELSE 3 END)
 OR (NEW.role_kind='REVIEWER' AND NEW.max_turns<>CASE WHEN EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) THEN 6 WHEN NEW.max_turns=4 THEN 4 ELSE 2 END)
BEGIN SELECT RAISE(ABORT,'role budget does not match the frozen mail attempt policy'); END;
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_rework_turn_budget_guard;
CREATE TEMP TABLE cleardev_rework_turn_budget_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_rework_turn_budget_guard_valid BEFORE INSERT ON cleardev_rework_turn_budget_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'rework turn budget constraint was not widened'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_rework_turn_budget_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_complex_exception_budgets'
        AND sql LIKE '%max_turns IN (3,4,5) AND max_rework_count IN (1,3)%'
        AND sql LIKE '%max_turns IN (2,4,6))%')
THEN 1 ELSE 0 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_rework_turn_budget_guard;
-- +goose StatementEnd

-- +goose Down
-- Refuse while any stored budget already relies on the wider rule.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_rework_turn_budget_down_guard;
CREATE TEMP TABLE cleardev_rework_turn_budget_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_rework_turn_budget_down_guard_valid BEFORE INSERT ON cleardev_rework_turn_budget_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses stored four-turn rework budgets'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_rework_turn_budget_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE max_turns=4)
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
DROP TABLE cleardev_rework_turn_budget_down_guard;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS cleardev_mail_role_budget_insert;
CREATE TRIGGER cleardev_mail_role_budget_insert BEFORE INSERT ON cleardev_complex_exception_budgets
WHEN (NEW.role_kind='BUILDER' AND NEW.max_turns<>CASE WHEN EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) THEN 5 ELSE 3 END)
 OR (NEW.role_kind='REVIEWER' AND NEW.max_turns<>CASE WHEN EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) THEN 6 ELSE 2 END)
BEGIN SELECT RAISE(ABORT,'role budget does not match the frozen mail attempt policy'); END;
-- +goose StatementBegin
PRAGMA writable_schema = ON;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'max_turns IN (3,4,5) AND max_rework_count IN (1,3)', 'max_turns IN (3,5) AND max_rework_count IN (1,3)')
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE sqlite_master
SET sql = replace(sql, 'max_turns IN (2,4,6))', 'max_turns IN (2,6))')
WHERE type = 'table' AND name = 'cleardev_complex_exception_budgets';
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA writable_schema = RESET;
-- +goose StatementEnd
