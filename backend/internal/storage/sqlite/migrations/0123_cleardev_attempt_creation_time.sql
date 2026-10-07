-- +goose Up
ALTER TABLE cleardev_agent_step_attempts ADD COLUMN created_at DATETIME;
ALTER TABLE cleardev_agent_step_attempts ADD COLUMN requested_at_semantics TEXT NOT NULL DEFAULT 'LEGACY_STEP_REQUEST'
    CHECK (requested_at_semantics IN ('ACTUAL_CREATION', 'LEGACY_STEP_REQUEST', 'LEGACY_RETRY_BOUNDARY'));
DROP TRIGGER cleardev_agent_step_attempts_update_forbidden;
UPDATE cleardev_agent_step_attempts SET requested_at_semantics = 'LEGACY_RETRY_BOUNDARY' WHERE attempt_number = 2;
-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_step_attempts_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_step_attempts_creation_required
BEFORE INSERT ON cleardev_agent_step_attempts
WHEN NEW.requested_at_semantics != 'ACTUAL_CREATION' OR NEW.created_at IS NULL OR NEW.created_at != NEW.requested_at
BEGIN
    SELECT RAISE(ABORT, 'new cleardev attempts require actual creation time');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cleardev_agent_step_attempts_creation_required;
ALTER TABLE cleardev_agent_step_attempts DROP COLUMN requested_at_semantics;
ALTER TABLE cleardev_agent_step_attempts DROP COLUMN created_at;
