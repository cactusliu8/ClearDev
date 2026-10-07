-- 0193: one explicit recovery within the original coordination message budget.
-- Original STOP/request/event rows never change. The effective decision view
-- keeps the barrier closed while the original Planner's second attempt runs.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_planner_runtime_recoveries (
 id TEXT PRIMARY KEY NOT NULL,
 event_id TEXT NOT NULL UNIQUE REFERENCES cleardev_planner_runtime_events(id),
 requirement_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
 first_attempt_id TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
 failure_event_id TEXT NOT NULL REFERENCES cleardev_agent_attempt_events(id),
 second_attempt_id TEXT NOT NULL UNIQUE REFERENCES cleardev_agent_step_attempts(id) DEFERRABLE INITIALLY DEFERRED,
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json) AND json_type(binding_json)='object'),
 old_step_json TEXT NOT NULL CHECK(json_valid(old_step_json) AND json_type(old_step_json)='object'),
 original_stopped_at TIMESTAMP NOT NULL,
 original_summary TEXT NOT NULL,
 supplement TEXT NOT NULL CHECK(length(trim(supplement)) BETWEEN 1 AND 16000),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TRIGGER cleardev_planner_recovery_current BEFORE INSERT ON cleardev_planner_runtime_recoveries
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries old WHERE old.id=NEW.id OR old.event_id=NEW.event_id OR old.logical_step_id=NEW.logical_step_id OR old.second_attempt_id=NEW.second_attempt_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_planner_runtime_events event
 JOIN cleardev_planner_runtime_requests request ON request.event_id=event.id
 JOIN cleardev_planner_runtime_decisions stop ON stop.event_id=event.id
 JOIN cleardev_complex_execution_runs run ON run.id=event.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 JOIN cleardev_complex_role_bindings role ON role.id=request.planner_role_binding_id
 JOIN sessions session ON session.id=request.ao_session_id
 JOIN cleardev_agent_step_attempts first ON first.id=NEW.first_attempt_id
 JOIN cleardev_agent_attempt_events failure ON failure.id=NEW.failure_event_id AND failure.attempt_id=first.id
 JOIN cleardev_agent_message_reservations message ON message.client_message_id=first.client_message_id
 JOIN cleardev_agent_message_confirmations confirmation ON confirmation.client_message_id=message.client_message_id
 JOIN conversation_turns turn ON turn.id=confirmation.turn_id
 JOIN conversation_messages native ON native.client_message_id=message.client_message_id AND native.turn_id=turn.id
 JOIN cleardev_message_budget_versions budget ON budget.development_project_id=project.id
 WHERE event.id=NEW.event_id AND run.id=NEW.execution_run_id AND project.id=NEW.requirement_id
 AND run.status='ACCEPTED' AND run.mode='STANDARD' AND run.settled_at IS NULL
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256 AND version.task_set_version=run.accepted_task_set_version
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED' AND project.paused_from_state IS NULL
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 AND stop.source='CONTROL_PLANE' AND stop.outcome='STOP' AND stop.reason_code='PLANNER_COORDINATION_UNAVAILABLE'
 AND stop.result_json IS NULL AND stop.result_sha256 IS NULL AND stop.created_at=NEW.original_stopped_at
 AND step.id=NEW.logical_step_id AND step.send_status='FAILED' AND step.reason_code=stop.reason_code
 AND step.step_kind='COMPLEX_ENGINEERING_PLAN' AND step.request_id=event.id AND step.role_binding_id=role.id
 AND role.role='ENGINEERING_PLANNER' AND role.status='BOUND' AND role.ao_session_id=session.id
 AND role.development_project_id=project.id AND session.project_id=project.ao_project_id
 AND session.kind='worker' AND session.session_mode='chat' AND session.permission_mode='auto' AND session.is_terminated=0
 AND session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)
 AND session.creation_idempotency_key=role.session_creation_idempotency_key
 AND session.creation_idempotency_key<>'' AND session.creation_request_fingerprint<>'' AND session.provider_conversation_id<>''
 AND session.activity_state IN('idle','exited')
 AND NOT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=session.id AND state NOT IN('completed','failed','interrupted'))
 AND first.logical_step_id=step.id AND first.attempt_number=1 AND first.step_category='COMPLEX_PLANNING'
 AND first.step_kind=step.step_kind AND first.role_binding_id=role.id AND first.ao_session_id=session.id
 AND first.client_message_id=step.client_message_id AND first.prompt_sha256=step.prompt_sha256
 AND NEW.second_attempt_id=step.id||':attempt:2'
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts WHERE logical_step_id=step.id AND attempt_number<>1)
 AND failure.id=(SELECT id FROM cleardev_agent_attempt_events WHERE attempt_id=first.id ORDER BY rowid DESC LIMIT 1)
 AND failure.status IN('FAILED','INTERRUPTED') AND failure.turn_state IN('failed','interrupted')
 AND failure.failure_category IN('PROVIDER_UNAVAILABLE','PROVIDER_FAILURE','AUTHENTICATION_REQUIRED','QUOTA_EXHAUSTED','RATE_LIMITED','MODEL_UNAVAILABLE')
 AND failure.client_message_id IN('',first.client_message_id) AND failure.turn_id IN('',turn.id)
 AND failure.prompt_sha256 IN('',first.prompt_sha256)
 AND message.attempt_id=first.id AND message.logical_step_id=step.id AND message.development_project_id=project.id
 AND message.ao_session_id=session.id AND message.prompt_sha256=step.prompt_sha256 AND message.source='ORIGINAL' AND message.budget_id IS NULL
 AND message.budget_version='MESSAGE_BUDGET_V1' AND budget.version=message.budget_version
 AND (SELECT count(*) FROM cleardev_agent_message_reservations WHERE logical_step_id=step.id)=1
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_results WHERE attempt_id=first.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_parse_corrections WHERE step_id=step.id)
 AND EXISTS(SELECT 1 FROM cleardev_agent_attempt_events sent WHERE sent.attempt_id=first.id AND sent.status='SENT'
  AND sent.client_message_id=message.client_message_id AND sent.prompt_sha256=message.prompt_sha256 AND sent.turn_id=turn.id)
 AND turn.handled_by_session_id=session.id AND turn.completed_at IS NOT NULL AND turn.rolled_back_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM conversation_messages answer WHERE answer.turn_id=turn.id AND answer.role='assistant')
 AND json_extract(NEW.binding_json,'$.nativeTurnId')=turn.id
 AND json_extract(NEW.binding_json,'$.nativeTurnState')=turn.state
 AND ((turn.state IN('failed','interrupted') AND json_extract(NEW.binding_json,'$.terminalEventId')=0)
 OR (turn.state='completed' AND NOT EXISTS(SELECT 1 FROM conversation_messages answer WHERE answer.turn_id=turn.id AND answer.role='assistant')
 AND EXISTS(SELECT 1 FROM conversation_provider_events terminal
 WHERE terminal.id=json_extract(NEW.binding_json,'$.terminalEventId')
 AND terminal.conversation_id=turn.conversation_id AND terminal.session_id=turn.handled_by_session_id
 AND terminal.method='turn.completed' AND terminal.provider_event_id NOT LIKE 'acp-history:%'
 AND json_valid(terminal.payload_json) AND json_extract(terminal.payload_json,'$.kind')='turn.completed'
 AND json_extract(terminal.payload_json,'$.providerTurnId')=turn.provider_turn_id
 AND json_extract(terminal.payload_json,'$.turnState')='completed')))
 AND native.role='user' AND native.origin='automation'
 AND json_extract(NEW.binding_json,'$.requirementId')=project.id
 AND json_extract(NEW.binding_json,'$.executionRunId')=run.id
 AND json_extract(NEW.binding_json,'$.eventId')=event.id
 AND json_extract(NEW.binding_json,'$.logicalStepId')=step.id
 AND json_extract(NEW.binding_json,'$.firstAttemptId')=first.id
 AND json_extract(NEW.binding_json,'$.failureEventId')=failure.id
 AND json_extract(NEW.binding_json,'$.roleBindingId')=role.id
 AND json_extract(NEW.binding_json,'$.aoSessionId')=session.id
 AND json_extract(NEW.binding_json,'$.providerConversationId')=session.provider_conversation_id
 AND json_extract(NEW.binding_json,'$.workspacePath')=session.workspace_path
 AND json_extract(NEW.binding_json,'$.sessionCreationKey')=session.creation_idempotency_key
 AND json_extract(NEW.binding_json,'$.creationFingerprint')=session.creation_request_fingerprint
 AND json_extract(NEW.binding_json,'$.harness')=session.harness
 AND json_extract(NEW.binding_json,'$.model')=session.model
 AND json_extract(NEW.binding_json,'$.promptSha256')=step.prompt_sha256
 AND json_extract(NEW.binding_json,'$.clientMessageId')=step.client_message_id
 AND json_extract(NEW.binding_json,'$.contextSha256')=request.context_sha256
)
BEGIN SELECT RAISE(ABORT,'coordination retry requires the exact settled first message and unchanged authority'); END;
CREATE TABLE cleardev_planner_runtime_recovery_decisions (
    event_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_planner_runtime_recoveries(event_id),
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    source TEXT NOT NULL CHECK(source IN('PLANNER','CONTROL_PLANE')),
    outcome TEXT NOT NULL CHECK(outcome IN('CONTINUE','AMEND_REMAINING','PRODUCT_CLARIFICATION_REQUIRED','STOP','STALE','LIMIT_REACHED')),
    reason_code TEXT NOT NULL,
    result_json TEXT CHECK(result_json IS NULL OR (json_valid(result_json) AND json_type(result_json)='object')),
    result_sha256 TEXT CHECK(result_sha256 IS NULL OR (length(result_sha256)=64 AND result_sha256 NOT GLOB '*[^0-9a-f]*')),
    summary TEXT NOT NULL CHECK(length(trim(summary))>0),
    created_at TIMESTAMP NOT NULL,
    CHECK((source='PLANNER' AND result_json IS NOT NULL AND result_sha256 IS NOT NULL)
       OR (source='CONTROL_PLANE' AND outcome IN('STOP','STALE','LIMIT_REACHED') AND reason_code<>'')),
    CHECK((outcome IN('CONTINUE','AMEND_REMAINING') AND reason_code='') OR
          (outcome NOT IN('CONTINUE','AMEND_REMAINING') AND reason_code<>''))
) WITHOUT ROWID;
CREATE TRIGGER cleardev_planner_runtime_recoveries_update BEFORE UPDATE ON cleardev_planner_runtime_recoveries
BEGIN SELECT RAISE(ABORT,'coordination recovery history is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_recoveries_delete BEFORE DELETE ON cleardev_planner_runtime_recoveries
BEGIN SELECT RAISE(ABORT,'coordination recovery history is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_recoveries_cdc AFTER INSERT ON cleardev_planner_runtime_recoveries BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
CREATE TRIGGER cleardev_planner_runtime_recovery_decisions_update BEFORE UPDATE ON cleardev_planner_runtime_recovery_decisions
BEGIN SELECT RAISE(ABORT,'coordination recovery history is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_recovery_decisions_delete BEFORE DELETE ON cleardev_planner_runtime_recovery_decisions
BEGIN SELECT RAISE(ABORT,'coordination recovery history is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_recovery_decisions_cdc AFTER INSERT ON cleardev_planner_runtime_recovery_decisions BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
CREATE VIEW cleardev_planner_runtime_current_decisions AS
 SELECT * FROM cleardev_planner_runtime_decisions original
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=original.event_id)
 UNION ALL SELECT * FROM cleardev_planner_runtime_recovery_decisions;
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
  OR (OLD.send_status IN ('SENT','FAILED') AND OLD.step_kind='REQUIREMENT_COMPILATION'
   AND NEW.send_status='PENDING' AND NEW.reason_code='' AND NEW.failed_at IS NULL AND NEW.sent_at IS NULL
   AND NEW.turn_id IS NULL AND NEW.final_message_id IS NULL AND NEW.final_message_text IS NULL
   AND NEW.message_sha256 IS NULL AND NEW.completed_at IS NULL
   AND EXISTS (
    SELECT 1 FROM cleardev_planning_extra_continuations recovery
    JOIN cleardev_planning_extra_requests intent ON intent.decision_request_id=recovery.decision_request_id
    JOIN cleardev_planning_extra_grants grant ON grant.decision_request_id=intent.decision_request_id
    JOIN cleardev_agent_step_attempts third ON third.id=recovery.third_attempt_id
    WHERE recovery.logical_step_id=OLD.id AND recovery.original_status=OLD.send_status AND recovery.original_reason=OLD.reason_code
     AND third.logical_step_id=OLD.id AND third.attempt_number=3 AND third.role_binding_id=OLD.role_binding_id
     AND third.prompt_sha256=OLD.prompt_sha256 AND third.client_message_id=OLD.client_message_id||':attempt:3'
     AND third.trigger_failure_event_id=json_extract(intent.binding_json,'$.failureEventId')
     AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE attempt_id=third.id)
   ))

  OR (OLD.send_status='FAILED' AND OLD.step_kind='COMPLEX_ENGINEERING_PLAN'
   AND OLD.reason_code='PLANNER_COORDINATION_UNAVAILABLE'
   AND NEW.send_status='PENDING' AND NEW.reason_code='' AND NEW.failed_at IS NULL AND NEW.sent_at IS NULL
   AND NEW.turn_id IS NULL AND NEW.final_message_id IS NULL AND NEW.final_message_text IS NULL
   AND NEW.message_sha256 IS NULL AND NEW.completed_at IS NULL
   AND EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery
    JOIN cleardev_agent_step_attempts second ON second.id=recovery.second_attempt_id
    WHERE recovery.logical_step_id=OLD.id AND recovery.event_id=OLD.request_id
     AND second.logical_step_id=OLD.id AND second.attempt_number=2 AND second.step_kind=OLD.step_kind
     AND second.step_category='COMPLEX_PLANNING' AND second.role_binding_id=OLD.role_binding_id
     AND second.prompt_sha256=OLD.prompt_sha256 AND second.client_message_id=OLD.client_message_id||':attempt:2'
     AND second.trigger_failure_event_id=recovery.failure_event_id
     AND second.ao_session_id=json_extract(recovery.binding_json,'$.aoSessionId')
     AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE attempt_id=second.id)))
 )
BEGIN SELECT RAISE(ABORT,'cleardev complex agent step is immutable or invalid'); END;
DROP VIEW cleardev_planner_runtime_barriers;
CREATE VIEW cleardev_planner_runtime_barriers AS
SELECT event.id AS event_id,event.execution_run_id,
       COALESCE(decision.reason_code,'PLANNER_COORDINATION_PENDING') AS reason_code
FROM cleardev_planner_runtime_events event
LEFT JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=event.id
WHERE (decision.event_id IS NULL OR decision.outcome NOT IN('CONTINUE','AMEND_REMAINING')
 OR (decision.outcome='AMEND_REMAINING' AND
     (SELECT count(*) FROM cleardev_planner_runtime_task_amendments amendment WHERE amendment.event_id=event.id)
       <>json_array_length(decision.result_json,'$.amendments')))
AND NOT (decision.source='CONTROL_PLANE' AND decision.outcome='STOP'
 AND EXISTS(SELECT 1 FROM cleardev_human_decision_requests grant
   WHERE grant.decision_kind='AUTHORIZE_COORDINATION_REPAIR' AND grant.status='RESOLVED' AND grant.decision='APPROVE'
     AND json_extract(grant.binding_json,'$.eventId')=event.id));
DROP TRIGGER cleardev_planner_runtime_decision_insert;
CREATE TRIGGER cleardev_planner_runtime_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_events WHERE id=NEW.event_id AND execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_requests request
    JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
    JOIN cleardev_planner_runtime_events event ON event.id=request.event_id
    WHERE request.event_id=NEW.event_id AND request.execution_run_id=NEW.execution_run_id
      AND step.role_binding_id=request.planner_role_binding_id AND step.request_id=event.id
      AND step.step_kind='COMPLEX_ENGINEERING_PLAN' AND step.send_status='SETTLED'
      AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='PLANNER_RUNTIME_COORDINATION'
      AND json_extract(NEW.result_json,'$.eventId')=event.id
      AND json_extract(NEW.result_json,'$.contextSha256')=request.context_sha256
      AND json_extract(NEW.result_json,'$.decision')=NEW.outcome
      AND json_extract(step.final_message_text,'$.decision')=NEW.outcome
      AND (json_extract(event.report_json,'$.category')<>'PRODUCT' OR NEW.outcome IN('PRODUCT_CLARIFICATION_REQUIRED','STOP'))
 ))
 OR (NEW.outcome IN('CONTINUE','AMEND_REMAINING') AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_runs run
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED' AND version.state='APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED' AND project.paused_from_state IS NULL
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome='AMEND_REMAINING' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
DROP TRIGGER cleardev_planner_runtime_amendment_insert;
CREATE TRIGGER cleardev_planner_runtime_amendment_insert
BEFORE INSERT ON cleardev_planner_runtime_task_amendments
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_task_amendments old WHERE old.id=NEW.id
             OR (old.event_id=NEW.event_id AND old.task_mapping_id=NEW.task_mapping_id)
             OR (old.task_mapping_id=NEW.task_mapping_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_task_amendments WHERE task_mapping_id=NEW.task_mapping_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_effective_tasks task
    JOIN cleardev_work_items item ON item.id=task.work_item_id
    JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=NEW.event_id
    JOIN json_each(decision.result_json,'$.amendments') amendment ON json_extract(amendment.value,'$.taskKey')=task.plan_task_key
    WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=NEW.execution_run_id
      AND decision.execution_run_id=NEW.execution_run_id AND decision.outcome='AMEND_REMAINING' AND decision.source='PLANNER'
      AND ((item.state='PLANNED' AND item.rework_count=0
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=task.id))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4 AND item.state IN('BLOCKED','REVIEW','REWORK')
       AND json_extract(NEW.task_packet_json,'$.runtimeRevision.firstRound')=item.rework_count+1
       AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE execution_run_id=task.execution_run_id AND status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))))
      AND task.task_packet_sha256=NEW.previous_package_sha256 AND NEW.task_packet_sha256<>NEW.previous_package_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.eventId')=NEW.event_id
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.decisionSha256')=decision.result_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.previousPackageSha256')=NEW.previous_package_sha256
      AND ((json_extract(task.task_packet_json,'$.schemaVersion')<>4 AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision'))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4
       AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')
       AND json_remove(NEW.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.writePaths')=json_extract(task.task_packet_json,'$.projectExecution.basis.writePaths')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.checks')=json_extract(task.task_packet_json,'$.projectExecution.basis.checks')
       AND (json_type(amendment.value,'$.executionBasis') IS NULL AND json_extract(NEW.task_packet_json,'$.projectExecution')=json_extract(task.task_packet_json,'$.projectExecution')
        OR json_extract(NEW.task_packet_json,'$.projectExecution.basis')=json_extract(amendment.value,'$.executionBasis'))))
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria') BETWEEN 1 AND (12 + CASE WHEN EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets grant WHERE grant.execution_run_id=NEW.execution_run_id AND grant.complex_execution_task_id=NEW.task_mapping_id AND grant.role_kind='BUILDER' AND grant.authorized_extra_turns>0) THEN 6 ELSE 0 END)
      AND json_array_length(amendment.value,'$.additionalReviewCriteria') BETWEEN 0 AND 6
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria')=json_array_length(task.task_packet_json,'$.reviewCriteria')+json_array_length(amendment.value,'$.additionalReviewCriteria')
      AND NOT EXISTS(SELECT 1 FROM json_each(task.task_packet_json,'$.reviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||criterion.key||']') IS NOT criterion.value)
      AND NOT EXISTS(SELECT 1 FROM json_each(amendment.value,'$.additionalReviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||(json_array_length(task.task_packet_json,'$.reviewCriteria')+criterion.key)||']') IS NOT criterion.value)
 )
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;
DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_insert_valid BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id
 JOIN cleardev_complex_execution_role_bindings b ON b.id=NEW.builder_role_binding_id JOIN cleardev_complex_execution_agent_steps s ON s.id=NEW.agent_step_id
 WHERE t.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.status='ACCEPTED' AND b.execution_run_id=r.id AND b.role='BUILDER' AND b.status='BOUND'
 AND s.role_binding_id=b.id AND s.step_kind='BUILDER_TASK' AND s.request_id=NEW.id
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.execution_run_id=NEW.execution_run_id AND s.task_id=NEW.task_mapping_id AND s.round=NEW.round))
 OR (NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NEW.round NOT IN(0,1,2,3,4,5,6) AND NOT (NEW.round BETWEEN 7 AND 12 AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets grant WHERE grant.execution_run_id=NEW.execution_run_id AND grant.complex_execution_task_id=NEW.task_mapping_id AND grant.role_kind='BUILDER' AND grant.authorized_extra_turns>0)))
 OR (NEW.batch_id IS NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND mode='STANDARD'))
 OR (NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b JOIN cleardev_complex_execution_runs r ON r.id=b.execution_run_id JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 WHERE b.id=NEW.batch_id AND r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND b.status='RUNNING' AND b.common_base_sha=NEW.base_commit_sha AND t.id=NEW.task_mapping_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key)))

 OR CASE WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING' ) THEN NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
 AND EXISTS(SELECT 1 FROM cleardev_work_items item WHERE item.complex_execution_task_id=effective.id
   AND item.rework_count=NEW.round AND ((NEW.round=0 AND item.state='PLANNED') OR
    (NEW.round>0 AND item.state='RUNNING' AND EXISTS(SELECT 1 FROM cleardev_project_events event
      WHERE event.subject_id=item.id AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED'
      AND event.reason_text='planner-engineering:'||revision.event_id))))
 AND (NEW.round=0 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts previous
   WHERE previous.task_mapping_id=NEW.task_mapping_id AND previous.round=NEW.round-1 AND previous.settled_at IS NOT NULL))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies dependency
   JOIN cleardev_planner_runtime_effective_tasks prerequisite ON prerequisite.id=dependency.depends_on_mapping_id
   WHERE dependency.task_mapping_id=NEW.task_mapping_id AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_verified_candidates verified
    JOIN cleardev_complex_execution_task_attempts checked ON checked.id=verified.task_attempt_id
    WHERE verified.task_mapping_id=prerequisite.id
     AND checked.round>=COALESCE(json_extract(prerequisite.task_packet_json,'$.runtimeRevision.firstRound'),0)
     AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts later
      WHERE later.task_mapping_id=prerequisite.id AND later.round>checked.round)))
 AND NEW.base_commit_sha=COALESCE((SELECT candidate.commit_sha
   FROM cleardev_complex_execution_task_attempts prior JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=prior.id
   WHERE prior.execution_run_id=NEW.execution_run_id AND prior.builder_role_binding_id=NEW.builder_role_binding_id
    AND prior.batch_id IS NULL AND prior.settled_at IS NOT NULL
   ORDER BY prior.dispatched_at DESC, candidate.created_at DESC LIMIT 1),
   (SELECT base_commit_sha FROM cleardev_complex_execution_role_bindings WHERE id=NEW.builder_role_binding_id))) ELSE ((NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_mapping_id AND a.round=NEW.round-1 AND a.base_commit_sha=NEW.base_commit_sha AND (NEW.batch_id IS NULL OR a.batch_id=NEW.batch_id)
 AND (a.status='REWORK'
  OR (a.status='BLOCKED' AND a.settled_at IS NOT NULL AND a.reason_code IN('CHECKER_UNAVAILABLE','CHECK_FAILED','CHECK_TIMEOUT')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm
    JOIN cleardev_work_items w ON w.id=tm.work_item_id
    JOIN cleardev_complex_execution_runs r ON r.id=tm.execution_run_id
    JOIN cleardev_complex_execution_agent_steps original ON original.id=a.agent_step_id
    JOIN cleardev_candidate_commits c ON c.complex_execution_task_attempt_id=a.id
    JOIN cleardev_complex_execution_check_runs failed ON failed.task_attempt_id=a.id AND failed.candidate_commit_id=c.id AND failed.candidate_commit_sha=c.commit_sha
    JOIN cleardev_complex_execution_check_specs spec ON spec.id=failed.check_spec_id
    JOIN cleardev_project_events event ON event.subject_id=w.id
    WHERE tm.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.mode='STANDARD' AND r.status='ACCEPTED' AND r.settled_at IS NULL
     AND json_extract(r.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
     AND w.state='RUNNING' AND w.rework_count=NEW.round
     AND original.send_status='SETTLED' AND original.turn_id IS NOT NULL
     AND spec.execution_run_id=r.id AND spec.check_kind='REQUIRED_CHECK'
     AND failed.settled_at IS NOT NULL AND failed.reason_code=a.reason_code AND (failed.status='FAILED' OR (failed.status='SETTLED' AND failed.result='FAIL'))
     AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='builder-first-check:'||failed.id))
  OR (a.status='BLOCKED' AND a.reason_code='BUILDER_BLOCKED' AND a.settled_at IS NOT NULL AND EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_task_amendments revision
    JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
    JOIN cleardev_work_items item ON item.complex_execution_task_id=revision.task_mapping_id
    JOIN cleardev_project_events event ON event.subject_id=item.id
    WHERE revision.task_mapping_id=NEW.task_mapping_id AND revision.execution_run_id=NEW.execution_run_id
    AND json_extract(revision.task_packet_json,'$.runtimeRevision.firstRound')=NEW.round
    AND json_extract(revision.task_packet_json,'$.schemaVersion')=4
    AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
    AND item.state='RUNNING' AND item.rework_count=NEW.round
    AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='planner-engineering:'||revision.event_id))
  OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=a.id AND recovery.action='CONTINUE_BUILDER' AND recovery.execution_run_id=NEW.execution_run_id AND a.status IN('BLOCKED','NEEDS_HUMAN'))
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
  OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN')
  OR (a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='DEVELOPMENT')
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_mapping_id AND p.execution_run_id=NEW.execution_run_id)))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))) END
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d LEFT JOIN cleardev_complex_execution_verified_candidates v ON v.task_mapping_id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND v.id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d JOIN cleardev_complex_execution_task_mappings t ON t.id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b WHERE b.execution_run_id=NEW.execution_run_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key) AND b.ordinal<(SELECT ordinal FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;
CREATE TRIGGER cleardev_planner_runtime_recovery_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_recovery_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_recovery_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r WHERE r.event_id=NEW.event_id AND r.execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r JOIN cleardev_agent_step_results result ON result.attempt_id=r.second_attempt_id JOIN cleardev_complex_agent_steps step ON step.id=r.logical_step_id WHERE r.event_id=NEW.event_id AND result.final_message_id=step.final_message_id AND result.turn_id=step.turn_id AND result.raw_message_sha256=step.message_sha256))
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_events WHERE id=NEW.event_id AND execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_requests request
    JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
    JOIN cleardev_planner_runtime_events event ON event.id=request.event_id
    WHERE request.event_id=NEW.event_id AND request.execution_run_id=NEW.execution_run_id
      AND step.role_binding_id=request.planner_role_binding_id AND step.request_id=event.id
      AND step.step_kind='COMPLEX_ENGINEERING_PLAN' AND step.send_status='SETTLED'
      AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='PLANNER_RUNTIME_COORDINATION'
      AND json_extract(NEW.result_json,'$.eventId')=event.id
      AND json_extract(NEW.result_json,'$.contextSha256')=request.context_sha256
      AND json_extract(NEW.result_json,'$.decision')=NEW.outcome
      AND json_extract(step.final_message_text,'$.decision')=NEW.outcome
      AND (json_extract(event.report_json,'$.category')<>'PRODUCT' OR NEW.outcome IN('PRODUCT_CLARIFICATION_REQUIRED','STOP'))
 ))
 OR (NEW.outcome IN('CONTINUE','AMEND_REMAINING') AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_runs run
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED' AND version.state='APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED' AND project.paused_from_state IS NULL
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome='AMEND_REMAINING' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
CREATE TRIGGER cleardev_coordination_second_attempt_exact BEFORE INSERT ON cleardev_agent_step_attempts
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r WHERE r.logical_step_id=NEW.logical_step_id OR r.second_attempt_id=NEW.id)
 AND (EXISTS(SELECT 1 FROM cleardev_agent_step_attempts old WHERE old.id=NEW.id OR (old.logical_step_id=NEW.logical_step_id AND old.attempt_number=NEW.attempt_number))
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r JOIN cleardev_agent_step_attempts first ON first.id=r.first_attempt_id
 WHERE NEW.id=r.second_attempt_id AND NEW.logical_step_id=r.logical_step_id AND NEW.attempt_number=2
 AND NEW.development_project_id=r.requirement_id AND NEW.step_category=first.step_category AND NEW.step_kind=first.step_kind
 AND NEW.role_binding_id=first.role_binding_id AND NEW.ao_session_id=first.ao_session_id
 AND NEW.client_message_id=first.client_message_id||':attempt:2' AND NEW.prompt_sha256=first.prompt_sha256
 AND NEW.trigger_failure_event_id=r.failure_event_id AND NEW.created_at=NEW.requested_at AND NEW.requested_at_semantics='ACTUAL_CREATION'))
BEGIN SELECT RAISE(ABORT,'coordination recovery second attempt must retain the original identity'); END;
CREATE TRIGGER cleardev_coordination_message_exact BEFORE INSERT ON cleardev_agent_message_reservations
WHEN NEW.source<>'PARSE_CORRECTION' AND EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r WHERE r.logical_step_id=NEW.logical_step_id)
 AND NOT EXISTS(
 SELECT 1 FROM cleardev_planner_runtime_recoveries r
 JOIN cleardev_agent_step_attempts second ON second.id=r.second_attempt_id
 JOIN cleardev_complex_agent_steps step ON step.id=r.logical_step_id
 JOIN sessions session ON session.id=second.ao_session_id
 JOIN cleardev_complex_execution_runs run ON run.id=r.execution_run_id
 JOIN cleardev_development_projects project ON project.id=r.requirement_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 WHERE NEW.logical_step_id=r.logical_step_id AND NEW.attempt_id=second.id
 AND NEW.client_message_id=second.client_message_id AND NEW.prompt_sha256=second.prompt_sha256 AND NEW.source='RECOVERY_ORIGINAL'
 AND NEW.ao_session_id=second.ao_session_id AND NEW.development_project_id=r.requirement_id AND NEW.budget_id IS NULL
 AND session.is_terminated=0 AND session.permission_mode='auto' AND session.session_mode='chat'
 AND session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)
 AND session.provider_conversation_id=json_extract(r.binding_json,'$.providerConversationId')
 AND session.workspace_path=json_extract(r.binding_json,'$.workspacePath')
 AND session.creation_idempotency_key=json_extract(r.binding_json,'$.sessionCreationKey')
 AND session.creation_request_fingerprint=json_extract(r.binding_json,'$.creationFingerprint')
 AND session.harness=json_extract(r.binding_json,'$.harness') AND session.model=json_extract(r.binding_json,'$.model')
 AND step.send_status='PENDING' AND run.status='ACCEPTED' AND project.cancelled_at IS NULL AND project.state<>'PAUSED' AND project.paused_from_state IS NULL
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256 AND version.task_set_version=run.accepted_task_set_version
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recovery_decisions WHERE event_id=r.event_id)
 AND EXISTS(SELECT 1 FROM cleardev_agent_attempt_events failure WHERE failure.id=r.failure_event_id
 AND failure.id=(SELECT id FROM cleardev_agent_attempt_events WHERE attempt_id=r.first_attempt_id ORDER BY rowid DESC LIMIT 1))
 AND EXISTS(SELECT 1 FROM conversation_turns original WHERE original.id=json_extract(r.binding_json,'$.nativeTurnId')
 AND original.handled_by_session_id=session.id AND original.state=json_extract(r.binding_json,'$.nativeTurnState')
 AND original.completed_at IS NOT NULL AND original.rolled_back_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM conversation_messages answer WHERE answer.turn_id=original.id AND answer.role='assistant')
 AND (original.state IN('failed','interrupted') OR (original.state='completed'
 AND NOT EXISTS(SELECT 1 FROM conversation_messages answer WHERE answer.turn_id=original.id AND answer.role='assistant')
 AND EXISTS(SELECT 1 FROM conversation_provider_events terminal WHERE terminal.id=json_extract(r.binding_json,'$.terminalEventId')
 AND terminal.session_id=session.id AND terminal.conversation_id=original.conversation_id
 AND terminal.method='turn.completed' AND terminal.provider_event_id NOT LIKE 'acp-history:%'
 AND json_valid(terminal.payload_json) AND json_extract(terminal.payload_json,'$.kind')='turn.completed'
 AND json_extract(terminal.payload_json,'$.providerTurnId')=original.provider_turn_id
 AND json_extract(terminal.payload_json,'$.turnState')='completed'))))
)
BEGIN SELECT RAISE(ABORT,'coordination send requires its exact current recovery identity'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_coordination_retry_down_guard(ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_coordination_retry_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config,'{}'),'$.cleardev')='object')
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_coordination_retry_down_guard;
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
  OR (OLD.send_status IN ('SENT','FAILED') AND OLD.step_kind='REQUIREMENT_COMPILATION'
   AND NEW.send_status='PENDING' AND NEW.reason_code='' AND NEW.failed_at IS NULL AND NEW.sent_at IS NULL
   AND NEW.turn_id IS NULL AND NEW.final_message_id IS NULL AND NEW.final_message_text IS NULL
   AND NEW.message_sha256 IS NULL AND NEW.completed_at IS NULL
   AND EXISTS (
    SELECT 1 FROM cleardev_planning_extra_continuations recovery
    JOIN cleardev_planning_extra_requests intent ON intent.decision_request_id=recovery.decision_request_id
    JOIN cleardev_planning_extra_grants grant ON grant.decision_request_id=intent.decision_request_id
    JOIN cleardev_agent_step_attempts third ON third.id=recovery.third_attempt_id
    WHERE recovery.logical_step_id=OLD.id AND recovery.original_status=OLD.send_status AND recovery.original_reason=OLD.reason_code
     AND third.logical_step_id=OLD.id AND third.attempt_number=3 AND third.role_binding_id=OLD.role_binding_id
     AND third.prompt_sha256=OLD.prompt_sha256 AND third.client_message_id=OLD.client_message_id||':attempt:3'
     AND third.trigger_failure_event_id=json_extract(intent.binding_json,'$.failureEventId')
     AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE attempt_id=third.id)
   ))

 )
BEGIN SELECT RAISE(ABORT,'cleardev complex agent step is immutable or invalid'); END;
DROP VIEW cleardev_planner_runtime_barriers;
CREATE VIEW cleardev_planner_runtime_barriers AS
SELECT event.id AS event_id,event.execution_run_id,
       COALESCE(decision.reason_code,'PLANNER_COORDINATION_PENDING') AS reason_code
FROM cleardev_planner_runtime_events event
LEFT JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=event.id
WHERE (decision.event_id IS NULL OR decision.outcome NOT IN('CONTINUE','AMEND_REMAINING')
 OR (decision.outcome='AMEND_REMAINING' AND
     (SELECT count(*) FROM cleardev_planner_runtime_task_amendments amendment WHERE amendment.event_id=event.id)
       <>json_array_length(decision.result_json,'$.amendments')))
AND NOT (decision.source='CONTROL_PLANE' AND decision.outcome='STOP'
 AND EXISTS(SELECT 1 FROM cleardev_human_decision_requests grant
   WHERE grant.decision_kind='AUTHORIZE_COORDINATION_REPAIR' AND grant.status='RESOLVED' AND grant.decision='APPROVE'
     AND json_extract(grant.binding_json,'$.eventId')=event.id));
DROP TRIGGER cleardev_planner_runtime_decision_insert;
CREATE TRIGGER cleardev_planner_runtime_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_events WHERE id=NEW.event_id AND execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_requests request
    JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
    JOIN cleardev_planner_runtime_events event ON event.id=request.event_id
    WHERE request.event_id=NEW.event_id AND request.execution_run_id=NEW.execution_run_id
      AND step.role_binding_id=request.planner_role_binding_id AND step.request_id=event.id
      AND step.step_kind='COMPLEX_ENGINEERING_PLAN' AND step.send_status='SETTLED'
      AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='PLANNER_RUNTIME_COORDINATION'
      AND json_extract(NEW.result_json,'$.eventId')=event.id
      AND json_extract(NEW.result_json,'$.contextSha256')=request.context_sha256
      AND json_extract(NEW.result_json,'$.decision')=NEW.outcome
      AND json_extract(step.final_message_text,'$.decision')=NEW.outcome
      AND (json_extract(event.report_json,'$.category')<>'PRODUCT' OR NEW.outcome IN('PRODUCT_CLARIFICATION_REQUIRED','STOP'))
 ))
 OR (NEW.outcome IN('CONTINUE','AMEND_REMAINING') AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_runs run
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED' AND version.state='APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome='AMEND_REMAINING' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
DROP TRIGGER cleardev_planner_runtime_amendment_insert;
CREATE TRIGGER cleardev_planner_runtime_amendment_insert
BEFORE INSERT ON cleardev_planner_runtime_task_amendments
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_task_amendments old WHERE old.id=NEW.id
             OR (old.event_id=NEW.event_id AND old.task_mapping_id=NEW.task_mapping_id)
             OR (old.task_mapping_id=NEW.task_mapping_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_task_amendments WHERE task_mapping_id=NEW.task_mapping_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_effective_tasks task
    JOIN cleardev_work_items item ON item.id=task.work_item_id
    JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=NEW.event_id
    JOIN json_each(decision.result_json,'$.amendments') amendment ON json_extract(amendment.value,'$.taskKey')=task.plan_task_key
    WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=NEW.execution_run_id
      AND decision.execution_run_id=NEW.execution_run_id AND decision.outcome='AMEND_REMAINING' AND decision.source='PLANNER'
      AND ((item.state='PLANNED' AND item.rework_count=0
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=task.id))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4 AND item.state IN('BLOCKED','REVIEW','REWORK')
       AND json_extract(NEW.task_packet_json,'$.runtimeRevision.firstRound')=item.rework_count+1
       AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE execution_run_id=task.execution_run_id AND status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))))
      AND task.task_packet_sha256=NEW.previous_package_sha256 AND NEW.task_packet_sha256<>NEW.previous_package_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.eventId')=NEW.event_id
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.decisionSha256')=decision.result_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.previousPackageSha256')=NEW.previous_package_sha256
      AND ((json_extract(task.task_packet_json,'$.schemaVersion')<>4 AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision'))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4
       AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')
       AND json_remove(NEW.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.writePaths')=json_extract(task.task_packet_json,'$.projectExecution.basis.writePaths')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.checks')=json_extract(task.task_packet_json,'$.projectExecution.basis.checks')
       AND (json_type(amendment.value,'$.executionBasis') IS NULL AND json_extract(NEW.task_packet_json,'$.projectExecution')=json_extract(task.task_packet_json,'$.projectExecution')
        OR json_extract(NEW.task_packet_json,'$.projectExecution.basis')=json_extract(amendment.value,'$.executionBasis'))))
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria') BETWEEN 1 AND (12 + CASE WHEN EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets grant WHERE grant.execution_run_id=NEW.execution_run_id AND grant.complex_execution_task_id=NEW.task_mapping_id AND grant.role_kind='BUILDER' AND grant.authorized_extra_turns>0) THEN 6 ELSE 0 END)
      AND json_array_length(amendment.value,'$.additionalReviewCriteria') BETWEEN 0 AND 6
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria')=json_array_length(task.task_packet_json,'$.reviewCriteria')+json_array_length(amendment.value,'$.additionalReviewCriteria')
      AND NOT EXISTS(SELECT 1 FROM json_each(task.task_packet_json,'$.reviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||criterion.key||']') IS NOT criterion.value)
      AND NOT EXISTS(SELECT 1 FROM json_each(amendment.value,'$.additionalReviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||(json_array_length(task.task_packet_json,'$.reviewCriteria')+criterion.key)||']') IS NOT criterion.value)
 )
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;
DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_insert_valid BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id
 JOIN cleardev_complex_execution_role_bindings b ON b.id=NEW.builder_role_binding_id JOIN cleardev_complex_execution_agent_steps s ON s.id=NEW.agent_step_id
 WHERE t.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.status='ACCEPTED' AND b.execution_run_id=r.id AND b.role='BUILDER' AND b.status='BOUND'
 AND s.role_binding_id=b.id AND s.step_kind='BUILDER_TASK' AND s.request_id=NEW.id
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.execution_run_id=NEW.execution_run_id AND s.task_id=NEW.task_mapping_id AND s.round=NEW.round))
 OR (NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NEW.round NOT IN(0,1,2,3,4,5,6) AND NOT (NEW.round BETWEEN 7 AND 12 AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets grant WHERE grant.execution_run_id=NEW.execution_run_id AND grant.complex_execution_task_id=NEW.task_mapping_id AND grant.role_kind='BUILDER' AND grant.authorized_extra_turns>0)))
 OR (NEW.batch_id IS NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND mode='STANDARD'))
 OR (NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b JOIN cleardev_complex_execution_runs r ON r.id=b.execution_run_id JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 WHERE b.id=NEW.batch_id AND r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND b.status='RUNNING' AND b.common_base_sha=NEW.base_commit_sha AND t.id=NEW.task_mapping_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key)))

 OR CASE WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING' ) THEN NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
 AND EXISTS(SELECT 1 FROM cleardev_work_items item WHERE item.complex_execution_task_id=effective.id
   AND item.rework_count=NEW.round AND ((NEW.round=0 AND item.state='PLANNED') OR
    (NEW.round>0 AND item.state='RUNNING' AND EXISTS(SELECT 1 FROM cleardev_project_events event
      WHERE event.subject_id=item.id AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED'
      AND event.reason_text='planner-engineering:'||revision.event_id))))
 AND (NEW.round=0 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts previous
   WHERE previous.task_mapping_id=NEW.task_mapping_id AND previous.round=NEW.round-1 AND previous.settled_at IS NOT NULL))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies dependency
   JOIN cleardev_planner_runtime_effective_tasks prerequisite ON prerequisite.id=dependency.depends_on_mapping_id
   WHERE dependency.task_mapping_id=NEW.task_mapping_id AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_verified_candidates verified
    JOIN cleardev_complex_execution_task_attempts checked ON checked.id=verified.task_attempt_id
    WHERE verified.task_mapping_id=prerequisite.id
     AND checked.round>=COALESCE(json_extract(prerequisite.task_packet_json,'$.runtimeRevision.firstRound'),0)
     AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts later
      WHERE later.task_mapping_id=prerequisite.id AND later.round>checked.round)))
 AND NEW.base_commit_sha=COALESCE((SELECT candidate.commit_sha
   FROM cleardev_complex_execution_task_attempts prior JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=prior.id
   WHERE prior.execution_run_id=NEW.execution_run_id AND prior.builder_role_binding_id=NEW.builder_role_binding_id
    AND prior.batch_id IS NULL AND prior.settled_at IS NOT NULL
   ORDER BY prior.dispatched_at DESC, candidate.created_at DESC LIMIT 1),
   (SELECT base_commit_sha FROM cleardev_complex_execution_role_bindings WHERE id=NEW.builder_role_binding_id))) ELSE ((NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_mapping_id AND a.round=NEW.round-1 AND a.base_commit_sha=NEW.base_commit_sha AND (NEW.batch_id IS NULL OR a.batch_id=NEW.batch_id)
 AND (a.status='REWORK'
  OR (a.status='BLOCKED' AND a.settled_at IS NOT NULL AND a.reason_code IN('CHECKER_UNAVAILABLE','CHECK_FAILED','CHECK_TIMEOUT')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm
    JOIN cleardev_work_items w ON w.id=tm.work_item_id
    JOIN cleardev_complex_execution_runs r ON r.id=tm.execution_run_id
    JOIN cleardev_complex_execution_agent_steps original ON original.id=a.agent_step_id
    JOIN cleardev_candidate_commits c ON c.complex_execution_task_attempt_id=a.id
    JOIN cleardev_complex_execution_check_runs failed ON failed.task_attempt_id=a.id AND failed.candidate_commit_id=c.id AND failed.candidate_commit_sha=c.commit_sha
    JOIN cleardev_complex_execution_check_specs spec ON spec.id=failed.check_spec_id
    JOIN cleardev_project_events event ON event.subject_id=w.id
    WHERE tm.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.mode='STANDARD' AND r.status='ACCEPTED' AND r.settled_at IS NULL
     AND json_extract(r.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
     AND w.state='RUNNING' AND w.rework_count=NEW.round
     AND original.send_status='SETTLED' AND original.turn_id IS NOT NULL
     AND spec.execution_run_id=r.id AND spec.check_kind='REQUIRED_CHECK'
     AND failed.settled_at IS NOT NULL AND failed.reason_code=a.reason_code AND (failed.status='FAILED' OR (failed.status='SETTLED' AND failed.result='FAIL'))
     AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='builder-first-check:'||failed.id))
  OR (a.status='BLOCKED' AND a.reason_code='BUILDER_BLOCKED' AND a.settled_at IS NOT NULL AND EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_task_amendments revision
    JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
    JOIN cleardev_work_items item ON item.complex_execution_task_id=revision.task_mapping_id
    JOIN cleardev_project_events event ON event.subject_id=item.id
    WHERE revision.task_mapping_id=NEW.task_mapping_id AND revision.execution_run_id=NEW.execution_run_id
    AND json_extract(revision.task_packet_json,'$.runtimeRevision.firstRound')=NEW.round
    AND json_extract(revision.task_packet_json,'$.schemaVersion')=4
    AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
    AND item.state='RUNNING' AND item.rework_count=NEW.round
    AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='planner-engineering:'||revision.event_id))
  OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=a.id AND recovery.action='CONTINUE_BUILDER' AND recovery.execution_run_id=NEW.execution_run_id AND a.status IN('BLOCKED','NEEDS_HUMAN'))
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
  OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN')
  OR (a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='DEVELOPMENT')
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_mapping_id AND p.execution_run_id=NEW.execution_run_id)))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))) END
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d LEFT JOIN cleardev_complex_execution_verified_candidates v ON v.task_mapping_id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND v.id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d JOIN cleardev_complex_execution_task_mappings t ON t.id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b WHERE b.execution_run_id=NEW.execution_run_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key) AND b.ordinal<(SELECT ordinal FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;
DROP TRIGGER cleardev_coordination_second_attempt_exact;
DROP TRIGGER cleardev_coordination_message_exact;
DROP VIEW cleardev_planner_runtime_current_decisions;
DROP TABLE cleardev_planner_runtime_recovery_decisions;
DROP TABLE cleardev_planner_runtime_recoveries;
-- +goose StatementEnd
