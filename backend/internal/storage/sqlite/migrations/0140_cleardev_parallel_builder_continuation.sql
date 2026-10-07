-- +goose Up
-- A PARALLEL Builder slot may be continued once after a pre-dispatch
-- infrastructure failure. Historical terminal bindings keep their original
-- slot; uniqueness applies only to the currently usable REQUESTED/BOUND slot.
DROP INDEX idx_cleardev_complex_execution_builder_slots;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_builder_slots
    ON cleardev_complex_execution_role_bindings (execution_run_id, builder_slot)
    WHERE role = 'BUILDER'
      AND builder_slot IS NOT NULL
      AND status IN ('REQUESTED', 'BOUND');

-- +goose Down
DROP INDEX idx_cleardev_complex_execution_builder_slots;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_builder_slots
    ON cleardev_complex_execution_role_bindings (execution_run_id, builder_slot)
    WHERE role = 'BUILDER' AND builder_slot IS NOT NULL;
