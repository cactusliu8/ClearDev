-- Discussions created before 0157 recorded a rejected protocol reply only on
-- its Agent step, leaving the discussion row pending forever: no result, no
-- settled_at, and no way to append the next user message because every
-- previous round must be settled. Reconcile exactly those rows to the same
-- terminal fact 0157 now stores, keeping the step's own failed_at as the
-- settlement time. Timeout and unavailability steps stay pending because they
-- keep their recovery semantics. Nothing else is touched and the step bytes
-- remain the original evidence.

-- +goose Up
UPDATE cleardev_product_discussions AS d
SET settled_at = COALESCE(
        (SELECT s.failed_at FROM cleardev_complex_agent_steps AS s
         WHERE s.request_id = d.id AND s.send_status = 'FAILED'
           AND s.reason_code = 'PRODUCT_DISCOVERY_INVALID'),
        CURRENT_TIMESTAMP),
    failure_reason = 'PRODUCT_DISCOVERY_INVALID'
WHERE d.settled_at IS NULL AND d.failure_reason IS NULL
  AND EXISTS (SELECT 1 FROM cleardev_complex_agent_steps AS s
              WHERE s.request_id = d.id AND s.send_status = 'FAILED'
                AND s.reason_code = 'PRODUCT_DISCOVERY_INVALID');

-- +goose Down
-- The reconciliation is a repair, not a rewrite: once settled the failed round
-- is history, exactly like any failed reply recorded after 0157. Reopening it
-- would erase a real failure, so there is nothing to undo. The guard repeats
-- the downstream history checks because goose commits each Down separately:
-- without it this file would still lower the recorded version before a later
-- guard refuses the downgrade, leaving a partially rolled-back schema.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_product_reconcile_down_guard;
CREATE TEMP TABLE cleardev_product_reconcile_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_product_reconcile_down_guard_valid BEFORE INSERT ON cleardev_product_reconcile_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_product_reconcile_down_guard SELECT CASE WHEN
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
DROP TABLE cleardev_product_reconcile_down_guard;
-- +goose StatementEnd
