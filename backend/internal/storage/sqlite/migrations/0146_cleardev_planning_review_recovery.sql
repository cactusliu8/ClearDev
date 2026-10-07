-- A failed compatibility summary may reopen only for a native-authorized
-- second attempt of the same interrupted planning review. Attempt1 is immutable.
-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_agent_step_update_valid;
CREATE TRIGGER cleardev_complex_agent_step_update_valid
BEFORE UPDATE ON cleardev_complex_agent_steps
WHEN OLD.role_binding_id IS NOT NEW.role_binding_id
 OR OLD.step_kind IS NOT NEW.step_kind OR OLD.request_id IS NOT NEW.request_id
 OR OLD.client_message_id IS NOT NEW.client_message_id OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
 OR OLD.requested_at IS NOT NEW.requested_at
 OR NOT (
  (OLD.send_status='PENDING' AND NEW.send_status IN ('SENT','FAILED'))
  OR (OLD.send_status='SENT' AND NEW.send_status IN ('SETTLED','FAILED'))
  OR (OLD.send_status='FAILED' AND OLD.reason_code='STEWARD_UNAVAILABLE' AND OLD.step_kind='COMPLEX_PLAN_REVIEW'
   AND NEW.send_status='PENDING' AND NEW.reason_code='' AND NEW.failed_at IS NULL AND NEW.sent_at IS NULL
   AND NEW.turn_id IS NULL AND NEW.final_message_id IS NULL AND NEW.final_message_text IS NULL
   AND NEW.message_sha256 IS NULL AND NEW.completed_at IS NULL
   AND EXISTS (
    SELECT 1 FROM cleardev_agent_step_attempts AS second
    JOIN cleardev_human_decision_requests AS grant ON grant.development_project_id=second.development_project_id
    WHERE second.logical_step_id=OLD.id AND second.attempt_number=2
     AND second.step_category='COMPLEX_PLANNING' AND second.step_kind=OLD.step_kind
     AND second.role_binding_id=OLD.role_binding_id AND second.prompt_sha256=OLD.prompt_sha256
     AND NOT EXISTS(SELECT 1 FROM cleardev_human_decision_effects WHERE request_id=grant.id)
     AND grant.decision_kind='AUTHORIZE_PLANNING_REVIEW_RECOVERY' AND grant.status='RESOLVED' AND grant.decision='APPROVE'
     AND json_extract(grant.binding_json,'$.logicalStepId')=OLD.id
     AND json_extract(grant.binding_json,'$.failureEventId')=second.trigger_failure_event_id
     AND json_extract(grant.binding_json,'$.aoSessionId')=second.ao_session_id
     AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE attempt_id=second.id)
   ))
 )
BEGIN SELECT RAISE(ABORT,'cleardev complex agent step is immutable or invalid'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_planning_recovery_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_planning_recovery_down_guard SELECT CASE WHEN EXISTS(
 SELECT 1 FROM cleardev_human_decision_requests WHERE decision_kind='AUTHORIZE_PLANNING_REVIEW_RECOVERY') THEN 0 ELSE 1 END;
DROP TABLE cleardev_planning_recovery_down_guard;
DROP TRIGGER cleardev_complex_agent_step_update_valid;
CREATE TRIGGER cleardev_complex_agent_step_update_valid
BEFORE UPDATE ON cleardev_complex_agent_steps
WHEN OLD.role_binding_id IS NOT NEW.role_binding_id
 OR OLD.step_kind IS NOT NEW.step_kind OR OLD.request_id IS NOT NEW.request_id
 OR OLD.client_message_id IS NOT NEW.client_message_id OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
 OR OLD.requested_at IS NOT NEW.requested_at
 OR OLD.send_status NOT IN ('PENDING','SENT')
 OR (OLD.send_status='PENDING' AND NEW.send_status NOT IN ('SENT','FAILED'))
 OR (OLD.send_status='SENT' AND NEW.send_status NOT IN ('SETTLED','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex agent step is immutable or invalid'); END;
-- +goose StatementEnd
