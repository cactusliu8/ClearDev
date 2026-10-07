-- A budget turn is one round of work, not one chat message. The reviewer's
-- round legitimately sends two messages (the verdict and the check-results
-- follow-up), and counting both consumed the next round's turn, which is how
-- a rework round ended up with no review left. Occupancies now also carry a
-- round key, so a round spends exactly one turn no matter how many messages
-- it sends: only the first message of a round runs the counted CAS update,
-- and later steps of the same round record their own occupancy row without
-- one. Stored rows keep their original step ids.

-- +goose Up
ALTER TABLE cleardev_complex_exception_budget_occupancies ADD COLUMN round_key TEXT;
CREATE INDEX cleardev_complex_exception_budget_occupancy_round
    ON cleardev_complex_exception_budget_occupancies(round_key)
    WHERE round_key IS NOT NULL;

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_budget_occupancy_round_down_guard;
CREATE TEMP TABLE cleardev_budget_occupancy_round_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_budget_occupancy_round_down_guard_valid BEFORE INSERT ON cleardev_budget_occupancy_round_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_budget_occupancy_round_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_exception_budget_occupancies WHERE round_key IS NOT NULL)
 OR EXISTS(SELECT 1 FROM (SELECT task_mapping_id FROM cleardev_complex_execution_verified_candidates GROUP BY task_mapping_id HAVING count(*)>1))
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_budget_occupancy_round_down_guard;
-- +goose StatementEnd
DROP INDEX IF EXISTS cleardev_complex_exception_budget_occupancy_round;
ALTER TABLE cleardev_complex_exception_budget_occupancies DROP COLUMN round_key;
