-- A failed discussion may use its existing unused second attempt. Preserve
-- the full old row atomically; no successful response, path-integrity failure,
-- third attempt, extra parse correction or new discussion is authorized.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE cleardev_planning_step_recoveries ADD COLUMN old_discussion_json TEXT
 CHECK(old_discussion_json IS NULL OR (json_valid(old_discussion_json) AND json_type(old_discussion_json)='object'));
DROP TRIGGER cleardev_planning_step_recovery_current;
CREATE TRIGGER cleardev_planning_step_recovery_current BEFORE INSERT ON cleardev_planning_step_recoveries
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_agent_steps step
 JOIN cleardev_complex_role_bindings role ON role.id=step.role_binding_id
 JOIN cleardev_development_projects requirement ON requirement.id=role.development_project_id
 JOIN cleardev_agent_step_attempts first ON first.id=NEW.first_attempt_id
 JOIN cleardev_agent_attempt_events failure ON failure.id=NEW.failure_event_id AND failure.attempt_id=first.id
 JOIN sessions session ON session.id=first.ao_session_id
 WHERE step.id=NEW.logical_step_id AND requirement.id=NEW.requirement_id
 AND requirement.cancelled_at IS NULL AND requirement.state<>'PAUSED'
 AND step.step_kind='REQUIREMENT_COMPILATION' AND step.send_status=NEW.original_status
 AND first.logical_step_id=step.id AND first.attempt_number=1 AND first.step_category='COMPLEX_PLANNING'
 AND first.step_kind=step.step_kind AND first.role_binding_id=role.id AND first.client_message_id=step.client_message_id
 AND first.prompt_sha256=step.prompt_sha256 AND role.status='BOUND' AND role.role='STEWARD' AND role.ao_session_id=session.id
 AND session.is_terminated=0 AND session.project_id=requirement.ao_project_id
 AND session.id IN (SELECT session_id FROM cleardev_project_tool_sessions)
 AND session.session_mode='chat'
 AND session.harness=json_extract(NEW.binding_json,'$.harness') AND session.model=json_extract(NEW.binding_json,'$.model')
 AND session.workspace_path=json_extract(NEW.binding_json,'$.workspacePath')
 AND session.creation_idempotency_key=role.session_creation_idempotency_key
 AND role.session_creation_idempotency_key=json_extract(NEW.binding_json,'$.sessionCreationKey')
 AND NOT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=session.id AND state NOT IN ('completed','failed','interrupted'))
 AND session.provider_conversation_id<>'' AND session.provider_conversation_id=json_extract(NEW.binding_json,'$.providerConversationId')
 AND json_extract(NEW.binding_json,'$.requirementId')=requirement.id
 AND json_extract(NEW.binding_json,'$.logicalStepId')=step.id
 AND json_extract(NEW.binding_json,'$.firstAttemptId')=first.id
 AND json_extract(NEW.binding_json,'$.failureEventId')=failure.id
 AND json_extract(NEW.binding_json,'$.aoSessionId')=session.id
 AND json_extract(NEW.binding_json,'$.roleBindingId')=role.id
 AND json_extract(NEW.binding_json,'$.promptSha256')=step.prompt_sha256
 AND json_extract(NEW.binding_json,'$.clientMessageId')=step.client_message_id
 AND NEW.second_attempt_id=step.id||':attempt:2'
 AND failure.status IN ('FAILED','INTERRUPTED') AND failure.turn_state IN ('failed','interrupted')
 AND failure.failure_category IN ('PROVIDER_UNAVAILABLE','PROVIDER_FAILURE','AUTHENTICATION_REQUIRED','QUOTA_EXHAUSTED','RATE_LIMITED','MODEL_UNAVAILABLE','RESULT_INVALID')
 AND failure.id=(SELECT id FROM cleardev_agent_attempt_events WHERE attempt_id=first.id ORDER BY rowid DESC LIMIT 1)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts WHERE logical_step_id=step.id AND attempt_number=2)
 AND NOT EXISTS(SELECT 1 FROM cleardev_contract_versions WHERE development_project_id=requirement.id AND state='APPROVED')
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE development_project_id=requirement.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE development_project_id=requirement.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE development_project_id=requirement.id)
 AND EXISTS (
  SELECT 1 FROM cleardev_product_goals product
  JOIN cleardev_development_projects parent ON parent.id=product.id
  JOIN cleardev_product_discussions discussion ON discussion.product_id=product.id
  WHERE product.id=json_extract(NEW.binding_json,'$.productId')
  AND discussion.id=json_extract(NEW.binding_json,'$.discussionId')
  AND discussion.ordinal=(SELECT max(ordinal) FROM cleardev_product_discussions WHERE product_id=product.id)
  AND parent.cancelled_at IS NULL AND parent.state<>'PAUSED'
  AND ((product.id=requirement.id AND discussion.id=step.request_id AND ((discussion.settled_at IS NULL AND discussion.failure_reason IS NULL AND discussion.result_json IS NULL AND NEW.old_discussion_json IS NULL) OR (discussion.settled_at IS NOT NULL AND discussion.failure_reason='PRODUCT_DISCOVERY_INVALID' AND discussion.result_json IS NULL AND step.send_status='FAILED' AND step.reason_code='PRODUCT_DISCOVERY_INVALID' AND failure.failure_category='RESULT_INVALID' AND NEW.old_discussion_json=json_object('id',discussion.id,'product_id',discussion.product_id,'ordinal',discussion.ordinal,'user_message',discussion.user_message,'agent_step_id',discussion.agent_step_id,'result_json',discussion.result_json,'result_sha256',discussion.result_sha256,'failure_reason',discussion.failure_reason,'created_at',discussion.created_at,'settled_at',discussion.settled_at))) AND json_extract(NEW.binding_json,'$.stageId')='')
   OR EXISTS(SELECT 1 FROM cleardev_product_stages stage WHERE stage.product_id=product.id AND stage.discussion_id=discussion.id
    AND stage.id=json_extract(NEW.binding_json,'$.stageId') AND stage.definition_sha256=json_extract(NEW.binding_json,'$.stageDefinitionSha256')
    AND stage.development_requirement_id=requirement.id
    AND NOT EXISTS(SELECT 1 FROM cleardev_complex_compilation_requests WHERE id=step.request_id)))
 )
) BEGIN SELECT RAISE(ABORT,'planning continuation requires an exact current stopped first attempt'); END;
DROP TRIGGER cleardev_product_discussion_settle_guard;
CREATE TRIGGER cleardev_product_discussion_settle_guard BEFORE UPDATE ON cleardev_product_discussions
WHEN (OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.ordinal IS NOT NEW.ordinal
 OR OLD.user_message IS NOT NEW.user_message OR OLD.created_at IS NOT NEW.created_at OR OLD.settled_at IS NOT NULL
 OR NEW.settled_at IS NULL
 OR (NEW.failure_reason IS NULL AND NOT EXISTS (
   SELECT 1 FROM cleardev_complex_agent_steps AS step
   JOIN cleardev_complex_role_bindings AS binding ON binding.id=step.role_binding_id
   WHERE step.id=NEW.agent_step_id AND step.request_id=NEW.id
     AND binding.development_project_id=NEW.product_id AND binding.role='STEWARD'
     AND step.step_kind='REQUIREMENT_COMPILATION' AND step.send_status='SETTLED'
     AND step.final_message_text=NEW.result_json
     AND json_extract(NEW.result_json,'$.kind')='PRODUCT_DISCOVERY'
 ))) AND NOT COALESCE((OLD.failure_reason='PRODUCT_DISCOVERY_INVALID' AND OLD.settled_at IS NOT NULL
 AND OLD.agent_step_id IS NULL AND OLD.result_json IS NULL AND OLD.result_sha256 IS NULL
 AND NEW.failure_reason IS NULL AND NEW.settled_at IS NULL AND NEW.agent_step_id IS NULL AND NEW.result_json IS NULL AND NEW.result_sha256 IS NULL
 AND NEW.id=OLD.id AND NEW.product_id=OLD.product_id AND NEW.ordinal=OLD.ordinal AND NEW.user_message=OLD.user_message AND NEW.created_at=OLD.created_at
 AND EXISTS(
 SELECT 1 FROM cleardev_planning_step_recoveries recovery
 JOIN cleardev_agent_step_attempts second ON second.id=recovery.second_attempt_id
 JOIN cleardev_complex_agent_steps step ON step.id=recovery.logical_step_id
 WHERE recovery.requirement_id=OLD.product_id AND json_extract(recovery.binding_json,'$.discussionId')=OLD.id
 AND json_extract(recovery.binding_json,'$.stageId')='' AND recovery.original_reason='PRODUCT_DISCOVERY_INVALID'
 AND recovery.old_discussion_json=json_object('id',OLD.id,'product_id',OLD.product_id,'ordinal',OLD.ordinal,'user_message',OLD.user_message,'agent_step_id',OLD.agent_step_id,'result_json',OLD.result_json,'result_sha256',OLD.result_sha256,'failure_reason',OLD.failure_reason,'created_at',OLD.created_at,'settled_at',OLD.settled_at)
 AND second.logical_step_id=step.id AND second.attempt_number=2 AND second.prompt_sha256=step.prompt_sha256
 AND second.trigger_failure_event_id=recovery.failure_event_id AND step.request_id=OLD.id AND step.send_status='PENDING'
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE attempt_id=second.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts other WHERE other.logical_step_id=step.id AND other.attempt_number>2)
 )),0)
BEGIN SELECT RAISE(ABORT,'product response requires its exact settled source or immutable invalid-discussion continuation'); END;
DROP TRIGGER cleardev_product_discussion_cdc_update;
CREATE TRIGGER cleardev_product_discussion_cdc_update AFTER UPDATE ON cleardev_product_discussions BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id,'discussionId',NEW.id),COALESCE(NEW.settled_at,(SELECT recovery.created_at FROM cleardev_planning_step_recoveries recovery WHERE recovery.requirement_id=NEW.product_id AND json_extract(recovery.binding_json,'$.discussionId')=NEW.id AND json_extract(recovery.binding_json,'$.stageId')='' AND recovery.old_discussion_json IS NOT NULL)) FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE invalid_discussion_down(value INTEGER CHECK(value=0));
-- Refuse before changing any part of the recovery schema. Goose commits each
-- migration separately: allowing 0215 to down first would partially roll back
-- an existing protected continuation before an older migration rejects it.
INSERT INTO invalid_discussion_down SELECT
 (SELECT count(*) FROM cleardev_planning_step_recoveries)
 + (SELECT count(*) FROM cleardev_workflow_failure_receipts)
 + (SELECT count(*) FROM cleardev_failure_coordination_sources)
 -- Existing execution contracts also carry protected checks and review history.
 -- Rollback after real execution must restore a stopped backup, not partially
 -- apply the per-version Down chain before an older history guard refuses it.
 + (SELECT count(*) FROM cleardev_complex_execution_runs);
DROP TABLE invalid_discussion_down;
DROP TRIGGER cleardev_planning_step_recovery_current;
CREATE TRIGGER cleardev_planning_step_recovery_current BEFORE INSERT ON cleardev_planning_step_recoveries
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_agent_steps step
 JOIN cleardev_complex_role_bindings role ON role.id=step.role_binding_id
 JOIN cleardev_development_projects requirement ON requirement.id=role.development_project_id
 JOIN cleardev_agent_step_attempts first ON first.id=NEW.first_attempt_id
 JOIN cleardev_agent_attempt_events failure ON failure.id=NEW.failure_event_id AND failure.attempt_id=first.id
 JOIN sessions session ON session.id=first.ao_session_id
 WHERE step.id=NEW.logical_step_id AND requirement.id=NEW.requirement_id
 AND requirement.cancelled_at IS NULL AND requirement.state<>'PAUSED'
 AND step.step_kind='REQUIREMENT_COMPILATION' AND step.send_status=NEW.original_status
 AND first.logical_step_id=step.id AND first.attempt_number=1 AND first.step_category='COMPLEX_PLANNING'
 AND first.step_kind=step.step_kind AND first.role_binding_id=role.id AND first.client_message_id=step.client_message_id
 AND first.prompt_sha256=step.prompt_sha256 AND role.status='BOUND' AND role.role='STEWARD' AND role.ao_session_id=session.id
 AND session.is_terminated=0 AND session.project_id=requirement.ao_project_id
 AND session.id IN (SELECT session_id FROM cleardev_project_tool_sessions)
 AND session.session_mode='chat'
 AND session.harness=json_extract(NEW.binding_json,'$.harness') AND session.model=json_extract(NEW.binding_json,'$.model')
 AND session.workspace_path=json_extract(NEW.binding_json,'$.workspacePath')
 AND session.creation_idempotency_key=role.session_creation_idempotency_key
 AND role.session_creation_idempotency_key=json_extract(NEW.binding_json,'$.sessionCreationKey')
 AND NOT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=session.id AND state NOT IN ('completed','failed','interrupted'))
 AND session.provider_conversation_id<>'' AND session.provider_conversation_id=json_extract(NEW.binding_json,'$.providerConversationId')
 AND json_extract(NEW.binding_json,'$.requirementId')=requirement.id
 AND json_extract(NEW.binding_json,'$.logicalStepId')=step.id
 AND json_extract(NEW.binding_json,'$.firstAttemptId')=first.id
 AND json_extract(NEW.binding_json,'$.failureEventId')=failure.id
 AND json_extract(NEW.binding_json,'$.aoSessionId')=session.id
 AND json_extract(NEW.binding_json,'$.roleBindingId')=role.id
 AND json_extract(NEW.binding_json,'$.promptSha256')=step.prompt_sha256
 AND json_extract(NEW.binding_json,'$.clientMessageId')=step.client_message_id
 AND NEW.second_attempt_id=step.id||':attempt:2'
 AND failure.status IN ('FAILED','INTERRUPTED') AND failure.turn_state IN ('failed','interrupted')
 AND failure.failure_category IN ('PROVIDER_UNAVAILABLE','PROVIDER_FAILURE','AUTHENTICATION_REQUIRED','QUOTA_EXHAUSTED','RATE_LIMITED','MODEL_UNAVAILABLE','RESULT_INVALID')
 AND failure.id=(SELECT id FROM cleardev_agent_attempt_events WHERE attempt_id=first.id ORDER BY rowid DESC LIMIT 1)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts WHERE logical_step_id=step.id AND attempt_number=2)
 AND NOT EXISTS(SELECT 1 FROM cleardev_contract_versions WHERE development_project_id=requirement.id AND state='APPROVED')
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE development_project_id=requirement.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE development_project_id=requirement.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE development_project_id=requirement.id)
 AND EXISTS (
  SELECT 1 FROM cleardev_product_goals product
  JOIN cleardev_development_projects parent ON parent.id=product.id
  JOIN cleardev_product_discussions discussion ON discussion.product_id=product.id
  WHERE product.id=json_extract(NEW.binding_json,'$.productId')
  AND discussion.id=json_extract(NEW.binding_json,'$.discussionId')
  AND discussion.ordinal=(SELECT max(ordinal) FROM cleardev_product_discussions WHERE product_id=product.id)
  AND parent.cancelled_at IS NULL AND parent.state<>'PAUSED'
  AND ((product.id=requirement.id AND discussion.id=step.request_id AND discussion.settled_at IS NULL AND discussion.failure_reason IS NULL AND discussion.result_json IS NULL AND json_extract(NEW.binding_json,'$.stageId')='')
   OR EXISTS(SELECT 1 FROM cleardev_product_stages stage WHERE stage.product_id=product.id AND stage.discussion_id=discussion.id
    AND stage.id=json_extract(NEW.binding_json,'$.stageId') AND stage.definition_sha256=json_extract(NEW.binding_json,'$.stageDefinitionSha256')
    AND stage.development_requirement_id=requirement.id
    AND NOT EXISTS(SELECT 1 FROM cleardev_complex_compilation_requests WHERE id=step.request_id)))
 )
) BEGIN SELECT RAISE(ABORT,'planning continuation requires an exact current stopped first attempt'); END;
DROP TRIGGER cleardev_product_discussion_settle_guard;
CREATE TRIGGER cleardev_product_discussion_settle_guard BEFORE UPDATE ON cleardev_product_discussions
WHEN OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.ordinal IS NOT NEW.ordinal
 OR OLD.user_message IS NOT NEW.user_message OR OLD.created_at IS NOT NEW.created_at OR OLD.settled_at IS NOT NULL
 OR NEW.settled_at IS NULL
 OR (NEW.failure_reason IS NULL AND NOT EXISTS (
   SELECT 1 FROM cleardev_complex_agent_steps AS step
   JOIN cleardev_complex_role_bindings AS binding ON binding.id=step.role_binding_id
   WHERE step.id=NEW.agent_step_id AND step.request_id=NEW.id
     AND binding.development_project_id=NEW.product_id AND binding.role='STEWARD'
     AND step.step_kind='REQUIREMENT_COMPILATION' AND step.send_status='SETTLED'
     AND step.final_message_text=NEW.result_json
     AND json_extract(NEW.result_json,'$.kind')='PRODUCT_DISCOVERY'
 ))
BEGIN SELECT RAISE(ABORT,'product response needs its exact settled Steward step'); END;
DROP TRIGGER cleardev_product_discussion_cdc_update;
CREATE TRIGGER cleardev_product_discussion_cdc_update AFTER UPDATE ON cleardev_product_discussions BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id,'discussionId',NEW.id),NEW.settled_at FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
ALTER TABLE cleardev_planning_step_recoveries DROP COLUMN old_discussion_json;
-- +goose StatementEnd
