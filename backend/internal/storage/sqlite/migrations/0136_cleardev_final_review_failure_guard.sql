-- Permit a detected final-Reviewer binding failure to become durable without
-- trusting the now-invalid live session. Request and transport facts stay immutable.
-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_final_review_update_guard;

CREATE TRIGGER cleardev_final_review_update_guard
BEFORE UPDATE ON cleardev_requirement_final_reviews
WHEN OLD.id IS NOT NEW.id OR OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.development_project_id IS NOT NEW.development_project_id OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256 OR OLD.plan_id IS NOT NEW.plan_id OR OLD.plan_sha256 IS NOT NEW.plan_sha256
 OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
 OR OLD.source_workspace_path IS NOT NEW.source_workspace_path OR OLD.review_packet_json IS NOT NEW.review_packet_json
 OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256 OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
 OR OLD.check_run_ids_json IS NOT NEW.check_run_ids_json OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status IN ('SETTLED','FAILED')
 OR NOT ((OLD.status='REQUESTED' AND NEW.status IN ('PENDING','FAILED'))
      OR (OLD.status='PENDING' AND NEW.status IN ('SENT','FAILED'))
      OR (OLD.status='SENT' AND NEW.status IN ('SETTLED','FAILED')))
 OR (NEW.status='FAILED' AND (
      OLD.ao_session_id IS NOT NEW.ao_session_id
   OR OLD.workspace_path IS NOT NEW.workspace_path
   OR OLD.bound_at IS NOT NEW.bound_at
   OR OLD.sent_at IS NOT NEW.sent_at))
 OR (NEW.status<>'FAILED' AND OLD.status<>'REQUESTED' AND (
      OLD.ao_session_id IS NOT NEW.ao_session_id
   OR OLD.workspace_path IS NOT NEW.workspace_path
   OR OLD.bound_at IS NOT NEW.bound_at))
 OR (NEW.status<>'FAILED' AND OLD.status='SENT' AND OLD.sent_at IS NOT NEW.sent_at)
 OR (NEW.status<>'FAILED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_development_projects AS project
    WHERE project.id=NEW.development_project_id AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 ))
 OR (NEW.status<>'FAILED' AND NEW.ao_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions AS session
    JOIN cleardev_development_projects AS project ON project.ao_project_id=session.project_id
    WHERE project.id=NEW.development_project_id AND session.id=NEW.ao_session_id
      AND session.kind='worker' AND session.harness='codex'
      AND session.session_mode='chat' AND session.permission_mode='auto'
      AND session.creation_idempotency_key='cleardev-requirement-final-review:'||NEW.id
      AND session.workspace_path=NEW.workspace_path
 ))
 OR (NEW.status<>'FAILED' AND NEW.ao_session_id IS NOT NULL AND (
      EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_exception_ondemand_bindings WHERE ao_session_id=NEW.ao_session_id)))
 OR (NEW.status='SENT' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_agent_message_reservations AS message ON message.attempt_id=attempt.id AND message.client_message_id=attempt.client_message_id
    JOIN cleardev_agent_attempt_events AS event ON event.attempt_id=attempt.id AND event.client_message_id=message.client_message_id
    WHERE attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND message.ao_session_id=NEW.ao_session_id AND message.prompt_sha256=NEW.prompt_sha256
      AND message.source IN ('ORIGINAL','RECOVERY_ORIGINAL') AND event.status='SENT' AND length(event.turn_id)>0
 ))
 OR (NEW.status='SETTLED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_results AS result
    JOIN cleardev_agent_step_attempts AS attempt ON attempt.id=result.attempt_id
    JOIN cleardev_agent_step_result_parses AS parsed ON parsed.result_id=result.id
    WHERE result.id=NEW.result_id AND parsed.conclusion='VALID'
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND json_extract(result.raw_message_text,'$.kind')='REQUIREMENT_FINAL_REVIEW_RESULT'
      AND json_extract(result.raw_message_text,'$.verdict')=NEW.verdict
 ))
BEGIN SELECT RAISE(ABORT,'requirement final review is immutable, not independent, or has no bound provider result'); END;
-- +goose StatementEnd

-- +goose Down
-- A database that has frozen this contract cannot safely restore the old guard.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_final_review_0136_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_final_review_0136_down_guard
SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE json_extract(execution_package_json,'$.finalReviewPolicy') IS NOT NULL)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_final_review_0136_down_guard;

DROP TRIGGER cleardev_final_review_update_guard;

CREATE TRIGGER cleardev_final_review_update_guard
BEFORE UPDATE ON cleardev_requirement_final_reviews
WHEN OLD.id IS NOT NEW.id OR OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.development_project_id IS NOT NEW.development_project_id OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256 OR OLD.plan_id IS NOT NEW.plan_id OR OLD.plan_sha256 IS NOT NEW.plan_sha256
 OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
 OR OLD.source_workspace_path IS NOT NEW.source_workspace_path OR OLD.review_packet_json IS NOT NEW.review_packet_json
 OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256 OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
 OR OLD.check_run_ids_json IS NOT NEW.check_run_ids_json OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status IN ('SETTLED','FAILED')
 OR NOT ((OLD.status='REQUESTED' AND NEW.status IN ('PENDING','FAILED'))
      OR (OLD.status='PENDING' AND NEW.status IN ('SENT','FAILED'))
      OR (OLD.status='SENT' AND NEW.status IN ('SETTLED','FAILED')))
 OR (OLD.status<>'REQUESTED' AND (OLD.ao_session_id IS NOT NEW.ao_session_id OR OLD.workspace_path IS NOT NEW.workspace_path OR OLD.bound_at IS NOT NEW.bound_at))
 OR (OLD.status='SENT' AND OLD.sent_at IS NOT NEW.sent_at)
 OR (NEW.status<>'FAILED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_development_projects AS project
    WHERE project.id=NEW.development_project_id AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 ))
 OR (NEW.ao_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions AS session
    JOIN cleardev_development_projects AS project ON project.ao_project_id=session.project_id
    WHERE project.id=NEW.development_project_id AND session.id=NEW.ao_session_id
      AND session.kind='worker' AND session.harness='codex'
      AND session.session_mode='chat' AND session.permission_mode='auto'
      AND session.creation_idempotency_key='cleardev-requirement-final-review:'||NEW.id
      AND session.workspace_path=NEW.workspace_path
 ))
 OR (NEW.ao_session_id IS NOT NULL AND (
      EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_exception_ondemand_bindings WHERE ao_session_id=NEW.ao_session_id)))
 OR (NEW.status='SENT' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_agent_message_reservations AS message ON message.attempt_id=attempt.id AND message.client_message_id=attempt.client_message_id
    JOIN cleardev_agent_attempt_events AS event ON event.attempt_id=attempt.id AND event.client_message_id=message.client_message_id
    WHERE attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND message.ao_session_id=NEW.ao_session_id AND message.prompt_sha256=NEW.prompt_sha256
      AND message.source IN ('ORIGINAL','RECOVERY_ORIGINAL') AND event.status='SENT' AND length(event.turn_id)>0
 ))
 OR (NEW.status='SETTLED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_results AS result
    JOIN cleardev_agent_step_attempts AS attempt ON attempt.id=result.attempt_id
    JOIN cleardev_agent_step_result_parses AS parsed ON parsed.result_id=result.id
    WHERE result.id=NEW.result_id AND parsed.conclusion='VALID'
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND json_extract(result.raw_message_text,'$.kind')='REQUIREMENT_FINAL_REVIEW_RESULT'
      AND json_extract(result.raw_message_text,'$.verdict')=NEW.verdict
 ))
BEGIN SELECT RAISE(ABORT,'requirement final review is immutable, not independent, or has no bound provider result'); END;
-- +goose StatementEnd
