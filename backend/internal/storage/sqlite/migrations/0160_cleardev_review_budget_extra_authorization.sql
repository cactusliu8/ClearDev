-- A requirement final review REWORK sends the task back to its Builder, and
-- the rework round's review is a second reviewer turn. MESSAGE_BUDGET_V1 keeps
-- one reviewer budget per task, so that second review needs the same kind of
-- explicit human authorization the mail flow grants as HUMAN_EXTRA. The
-- authorization is additive and narrow: it grants extra reviewer turns (one
-- per approval, at most two) without touching the frozen max_turns values or
-- any recorded occupancy.

-- +goose Up
ALTER TABLE cleardev_complex_exception_budgets
    ADD COLUMN authorized_extra_turns INTEGER NOT NULL DEFAULT 0
    CHECK(authorized_extra_turns BETWEEN 0 AND 2);

-- +goose Down
-- Removing the column only rejects future extra authorizations; turns already
-- taken under an approval remain their own recorded history. Guard the
-- downgrade the same way the other project migrations do, because goose
-- commits each Down separately.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_review_budget_extra_down_guard;
CREATE TEMP TABLE cleardev_review_budget_extra_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_review_budget_extra_down_guard_valid BEFORE INSERT ON cleardev_review_budget_extra_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_review_budget_extra_down_guard SELECT CASE WHEN
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
DROP TABLE cleardev_review_budget_extra_down_guard;
-- +goose StatementEnd
ALTER TABLE cleardev_complex_exception_budgets DROP COLUMN authorized_extra_turns;
