-- +goose Up
-- A desktop grant permits one implementation only. Unused automatic repair
-- capacity cannot be used after HUMAN_EXTRA, even by a direct slot insertion.
-- +goose StatementBegin
CREATE TRIGGER cleardev_mail_attempt_after_extra_forbidden
BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN EXISTS (
    SELECT 1 FROM cleardev_mail_attempt_slots
    WHERE execution_run_id = NEW.execution_run_id AND attempt_kind = 'HUMAN_EXTRA'
)
BEGIN
    SELECT RAISE(ABORT, 'mail human extra attempt is terminal');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cleardev_mail_attempt_after_extra_forbidden;
