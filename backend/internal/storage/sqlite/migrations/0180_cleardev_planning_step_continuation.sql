-- A single explicit, budget-preserving second attempt for a product discussion
-- or pre-confirmation compilation. The original failed step is snapshotted;
-- attempts, messages, results and parse evidence remain append-only.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_planning_step_recoveries (
 id TEXT PRIMARY KEY NOT NULL,
 requirement_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
 logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
 first_attempt_id TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
 failure_event_id TEXT NOT NULL REFERENCES cleardev_agent_attempt_events(id),
 second_attempt_id TEXT NOT NULL UNIQUE REFERENCES cleardev_agent_step_attempts(id) DEFERRABLE INITIALLY DEFERRED,
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json) AND json_type(binding_json)='object'),
 old_step_json TEXT NOT NULL CHECK(json_valid(old_step_json) AND json_type(old_step_json)='object'),
 original_status TEXT NOT NULL CHECK(original_status IN ('SENT','FAILED')),
 original_reason TEXT NOT NULL,
 original_summary TEXT NOT NULL,
 original_stopped_at TIMESTAMP NOT NULL,
 supplement TEXT NOT NULL CHECK(length(supplement)<=16000),
 created_at TIMESTAMP NOT NULL
);
CREATE INDEX cleardev_planning_step_recovery_requirement ON cleardev_planning_step_recoveries(requirement_id,created_at);
CREATE TRIGGER cleardev_planning_step_recovery_immutable BEFORE UPDATE ON cleardev_planning_step_recoveries
BEGIN SELECT RAISE(ABORT,'planning step recovery is immutable'); END;
CREATE TRIGGER cleardev_planning_step_recovery_keep_history BEFORE DELETE ON cleardev_planning_step_recoveries
BEGIN SELECT RAISE(ABORT,'planning step recovery history is immutable'); END;
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
CREATE TRIGGER cleardev_planning_step_recovery_cdc AFTER INSERT ON cleardev_planning_step_recoveries BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',id,'recoveryId',NEW.id),NEW.created_at
 FROM cleardev_development_projects WHERE id=NEW.requirement_id;
END;
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
  OR (OLD.send_status IN ('SENT','FAILED') AND OLD.step_kind='REQUIREMENT_COMPILATION'
   AND NEW.send_status='PENDING' AND NEW.reason_code='' AND NEW.failed_at IS NULL AND NEW.sent_at IS NULL
   AND NEW.turn_id IS NULL AND NEW.final_message_id IS NULL AND NEW.final_message_text IS NULL
   AND NEW.message_sha256 IS NULL AND NEW.completed_at IS NULL
   AND EXISTS (
    SELECT 1 FROM cleardev_planning_step_recoveries recovery
    JOIN cleardev_agent_step_attempts second ON second.id=recovery.second_attempt_id
    WHERE recovery.logical_step_id=OLD.id AND recovery.original_status=OLD.send_status AND recovery.original_reason=OLD.reason_code
     AND second.logical_step_id=OLD.id AND second.attempt_number=2 AND second.step_category='COMPLEX_PLANNING'
     AND second.step_kind=OLD.step_kind AND second.role_binding_id=OLD.role_binding_id
     AND second.prompt_sha256=OLD.prompt_sha256 AND second.client_message_id=OLD.client_message_id||':attempt:2'
     AND second.trigger_failure_event_id=recovery.failure_event_id
     AND second.ao_session_id=json_extract(recovery.binding_json,'$.aoSessionId')
     AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE attempt_id=second.id)
   ))
 )
BEGIN SELECT RAISE(ABORT,'cleardev complex agent step is immutable or invalid'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_planning_step_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_planning_step_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_planning_step_recoveries)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config, '{}'), '$.cleardev')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_planning_step_down_guard;
DROP TABLE cleardev_planning_step_recoveries;
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
