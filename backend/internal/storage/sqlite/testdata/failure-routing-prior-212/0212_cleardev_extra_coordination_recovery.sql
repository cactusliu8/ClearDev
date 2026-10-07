-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_planner_recovery_current;
CREATE TRIGGER cleardev_planner_recovery_current BEFORE INSERT ON cleardev_planner_runtime_recoveries
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries old WHERE old.id=NEW.id OR old.event_id=NEW.event_id OR old.logical_step_id=NEW.logical_step_id OR old.second_attempt_id=NEW.second_attempt_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_planner_runtime_events event
 JOIN cleardev_planner_runtime_requests request ON request.event_id=event.id
 JOIN cleardev_planner_runtime_current_decisions stop ON stop.event_id=event.id
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
DROP TRIGGER cleardev_extra_coordination_message_exact;
CREATE TRIGGER cleardev_extra_coordination_message_exact BEFORE INSERT ON cleardev_agent_message_reservations
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_requests offer WHERE NEW.logical_step_id=offer.event_id||':planner')
 AND NOT EXISTS(
 SELECT 1 FROM cleardev_extra_coordination_requests offer
 JOIN cleardev_extra_coordination_grants grant ON grant.event_id=offer.event_id
 JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
 JOIN cleardev_planner_runtime_requests request ON request.event_id=offer.event_id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 JOIN cleardev_agent_step_attempts attempt ON attempt.id=NEW.attempt_id
 JOIN sessions session ON session.id=request.ao_session_id
 JOIN cleardev_complex_role_bindings planner ON planner.id=request.planner_role_binding_id
 JOIN cleardev_complex_execution_runs run ON run.id=request.execution_run_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 WHERE NEW.logical_step_id=step.id AND NEW.ao_session_id=session.id AND NEW.development_project_id=project.id
 AND NEW.budget_id IS NULL AND NEW.budget_version='MESSAGE_BUDGET_V1'
 AND request.ordinal=3 AND request.context_json=offer.context_json AND request.context_sha256=json_extract(offer.binding_json,'$.contextSha256')
 AND request.prompt=offer.prompt AND step.prompt_sha256=offer.prompt_sha256
 AND human.status='RESOLVED' AND human.decision='APPROVE' AND human.decision_kind='AUTHORIZE_EXTRA_PLANNER_COORDINATION'
 AND human.binding_json=offer.binding_json AND human.development_project_id=project.id AND grant.execution_run_id=run.id
 AND planner.status='BOUND' AND planner.role='ENGINEERING_PLANNER' AND planner.ao_session_id=session.id
 AND planner.id=json_extract(offer.binding_json,'$.plannerRoleBindingId') AND session.id=json_extract(offer.binding_json,'$.aoSessionId')
 AND session.kind='worker' AND session.is_terminated=0 AND session.session_mode='chat' AND session.permission_mode='auto'
 AND session.project_id=project.ao_project_id AND session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)
 AND session.provider_conversation_id=json_extract(offer.binding_json,'$.providerConversationId')
 AND session.workspace_path=json_extract(offer.binding_json,'$.workspacePath')
 AND session.creation_idempotency_key=json_extract(offer.binding_json,'$.sessionCreationKey')
 AND session.creation_request_fingerprint=json_extract(offer.binding_json,'$.creationFingerprint')
 AND session.harness=json_extract(offer.binding_json,'$.harness') AND session.model=json_extract(offer.binding_json,'$.model')
 AND attempt.logical_step_id=step.id AND attempt.role_binding_id=planner.id AND attempt.ao_session_id=session.id
 AND attempt.development_project_id=project.id AND attempt.step_category='COMPLEX_PLANNING'
 AND attempt.step_kind='COMPLEX_ENGINEERING_PLAN' AND attempt.attempt_number IN(1,2) AND attempt.prompt_sha256=offer.prompt_sha256
 AND ((NEW.source='ORIGINAL' AND attempt.attempt_number=1 AND NEW.client_message_id=step.client_message_id AND NEW.prompt_sha256=attempt.prompt_sha256)
   OR (NEW.source='RECOVERY_ORIGINAL' AND attempt.attempt_number=2 AND NEW.client_message_id=step.client_message_id||':attempt:2' AND NEW.prompt_sha256=attempt.prompt_sha256)
   OR (NEW.source='PARSE_CORRECTION' AND EXISTS(SELECT 1 FROM cleardev_parse_corrections correction
      WHERE correction.step_id=step.id AND correction.client_message_id=NEW.client_message_id
      AND correction.prompt_sha256=NEW.prompt_sha256 AND correction.attempt_number=attempt.attempt_number)))
 AND run.status='ACCEPTED' AND project.cancelled_at IS NULL AND project.state<>'PAUSED' AND project.paused_from_state IS NULL
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256 AND version.task_set_version=run.accepted_task_set_version
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 AND (NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_decisions WHERE event_id=offer.event_id)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=offer.event_id AND recovery.second_attempt_id=attempt.id))
 AND NOT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=session.id AND state NOT IN('completed','failed','interrupted'))
)
BEGIN SELECT RAISE(ABORT,'extra coordination message requires its exact current native grant and bounded attempt'); END;
DROP VIEW cleardev_planner_runtime_prior_decisions;
CREATE VIEW cleardev_planner_runtime_prior_decisions AS
 SELECT * FROM cleardev_planner_runtime_decisions original
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=original.event_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=original.event_id)
 UNION ALL SELECT * FROM cleardev_planner_runtime_recovery_decisions
 UNION ALL SELECT * FROM cleardev_extra_coordination_decisions extra
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=extra.event_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE extra_retry_downgrade_guard(ok INTEGER CHECK(ok=1));
INSERT INTO extra_retry_downgrade_guard SELECT NOT (EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions)
 OR EXISTS(SELECT 1 FROM cleardev_product_goals)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs));
DROP TABLE extra_retry_downgrade_guard;
DROP TRIGGER cleardev_planner_recovery_current;
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
DROP TRIGGER cleardev_extra_coordination_message_exact;
CREATE TRIGGER cleardev_extra_coordination_message_exact BEFORE INSERT ON cleardev_agent_message_reservations
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_requests offer WHERE NEW.logical_step_id=offer.event_id||':planner')
 AND NOT EXISTS(
 SELECT 1 FROM cleardev_extra_coordination_requests offer
 JOIN cleardev_extra_coordination_grants grant ON grant.event_id=offer.event_id
 JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
 JOIN cleardev_planner_runtime_requests request ON request.event_id=offer.event_id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 JOIN cleardev_agent_step_attempts attempt ON attempt.id=NEW.attempt_id
 JOIN sessions session ON session.id=request.ao_session_id
 JOIN cleardev_complex_role_bindings planner ON planner.id=request.planner_role_binding_id
 JOIN cleardev_complex_execution_runs run ON run.id=request.execution_run_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 WHERE NEW.logical_step_id=step.id AND NEW.ao_session_id=session.id AND NEW.development_project_id=project.id
 AND NEW.budget_id IS NULL AND NEW.budget_version='MESSAGE_BUDGET_V1'
 AND request.ordinal=3 AND request.context_json=offer.context_json AND request.context_sha256=json_extract(offer.binding_json,'$.contextSha256')
 AND request.prompt=offer.prompt AND step.prompt_sha256=offer.prompt_sha256
 AND human.status='RESOLVED' AND human.decision='APPROVE' AND human.decision_kind='AUTHORIZE_EXTRA_PLANNER_COORDINATION'
 AND human.binding_json=offer.binding_json AND human.development_project_id=project.id AND grant.execution_run_id=run.id
 AND planner.status='BOUND' AND planner.role='ENGINEERING_PLANNER' AND planner.ao_session_id=session.id
 AND planner.id=json_extract(offer.binding_json,'$.plannerRoleBindingId') AND session.id=json_extract(offer.binding_json,'$.aoSessionId')
 AND session.kind='worker' AND session.is_terminated=0 AND session.session_mode='chat' AND session.permission_mode='auto'
 AND session.project_id=project.ao_project_id AND session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)
 AND session.provider_conversation_id=json_extract(offer.binding_json,'$.providerConversationId')
 AND session.workspace_path=json_extract(offer.binding_json,'$.workspacePath')
 AND session.creation_idempotency_key=json_extract(offer.binding_json,'$.sessionCreationKey')
 AND session.creation_request_fingerprint=json_extract(offer.binding_json,'$.creationFingerprint')
 AND session.harness=json_extract(offer.binding_json,'$.harness') AND session.model=json_extract(offer.binding_json,'$.model')
 AND attempt.logical_step_id=step.id AND attempt.role_binding_id=planner.id AND attempt.ao_session_id=session.id
 AND attempt.development_project_id=project.id AND attempt.step_category='COMPLEX_PLANNING'
 AND attempt.step_kind='COMPLEX_ENGINEERING_PLAN' AND attempt.attempt_number IN(1,2) AND attempt.prompt_sha256=offer.prompt_sha256
 AND ((NEW.source='ORIGINAL' AND attempt.attempt_number=1 AND NEW.client_message_id=step.client_message_id AND NEW.prompt_sha256=attempt.prompt_sha256)
   OR (NEW.source='RECOVERY_ORIGINAL' AND attempt.attempt_number=2 AND NEW.client_message_id=step.client_message_id||':attempt:2' AND NEW.prompt_sha256=attempt.prompt_sha256)
   OR (NEW.source='PARSE_CORRECTION' AND EXISTS(SELECT 1 FROM cleardev_parse_corrections correction
      WHERE correction.step_id=step.id AND correction.client_message_id=NEW.client_message_id
      AND correction.prompt_sha256=NEW.prompt_sha256 AND correction.attempt_number=attempt.attempt_number)))
 AND run.status='ACCEPTED' AND project.cancelled_at IS NULL AND project.state<>'PAUSED' AND project.paused_from_state IS NULL
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256 AND version.task_set_version=run.accepted_task_set_version
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_decisions WHERE event_id=offer.event_id)
 AND NOT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=session.id AND state NOT IN('completed','failed','interrupted'))
)
BEGIN SELECT RAISE(ABORT,'extra coordination message requires its exact current native grant and bounded attempt'); END;
DROP VIEW cleardev_planner_runtime_prior_decisions;
CREATE VIEW cleardev_planner_runtime_prior_decisions AS
 SELECT * FROM cleardev_planner_runtime_decisions original
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=original.event_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=original.event_id)
 UNION ALL SELECT * FROM cleardev_planner_runtime_recovery_decisions
 UNION ALL SELECT * FROM cleardev_extra_coordination_decisions;
-- +goose StatementEnd
