-- +goose Up
-- Keep the execution-wide one-HUMAN_EXTRA authorization in the existing slot
-- insert guard. Only terminality becomes task-local for the frozen V2 policy.
-- Historical V1 keeps its original execution-wide terminal interpretation.
-- +goose StatementBegin
DROP TRIGGER cleardev_mail_attempt_after_extra_forbidden;
CREATE TRIGGER cleardev_mail_attempt_after_extra_forbidden BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN EXISTS (
 SELECT 1 FROM cleardev_mail_attempt_slots prior
 WHERE prior.execution_run_id=NEW.execution_run_id AND prior.attempt_kind='HUMAN_EXTRA'
 AND (prior.task_id=NEW.task_id OR NOT EXISTS (
  SELECT 1 FROM cleardev_bounded_mail_runs run
  WHERE run.id=NEW.execution_run_id
    AND json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
 ))
)
BEGIN SELECT RAISE(ABORT,'mail human extra attempt is terminal'); END;
-- +goose StatementEnd

-- +goose Down
-- A V2 run that has used this authority cannot be reinterpreted by the older
-- global terminal gate, even if no sibling has been dispatched yet.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_mail_v2_extra_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_mail_v2_extra_down_guard
SELECT CASE WHEN EXISTS (
 SELECT 1 FROM cleardev_mail_attempt_slots slot
 JOIN cleardev_complex_execution_runs run ON run.id=slot.execution_run_id
 WHERE slot.attempt_kind='HUMAN_EXTRA'
   AND json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
) THEN 0 ELSE 1 END;
DROP TABLE cleardev_mail_v2_extra_down_guard;

DROP TRIGGER cleardev_mail_attempt_after_extra_forbidden;
CREATE TRIGGER cleardev_mail_attempt_after_extra_forbidden BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
BEGIN SELECT RAISE(ABORT,'mail human extra attempt is terminal'); END;
-- +goose StatementEnd
