-- 0194: one native-human-authorized third coordination, never a refund.
-- Existing requests, STOP/LIMIT decisions and role/message budgets remain unchanged.
-- +goose Up

-- +goose StatementBegin
CREATE TABLE cleardev_extra_coordination_requests (
 id TEXT PRIMARY KEY NOT NULL,
 execution_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_runs(id),
 event_id TEXT NOT NULL UNIQUE REFERENCES cleardev_planner_runtime_events(id),
 decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id),
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json)),
 context_json TEXT NOT NULL CHECK(json_valid(context_json)),
 prompt TEXT NOT NULL CHECK(length(trim(prompt))>0),
 prompt_sha256 TEXT NOT NULL CHECK(length(prompt_sha256)=64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
 supplement TEXT NOT NULL CHECK(length(trim(supplement))>0 AND length(supplement)<=16000),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TABLE cleardev_extra_coordination_grants (
 event_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_extra_coordination_requests(event_id),
 execution_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_runs(id),
 decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cleardev_extra_coordination_decisions (
    event_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_extra_coordination_grants(event_id),
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_extra_coordination_request_exact BEFORE INSERT ON cleardev_extra_coordination_requests
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_requests old WHERE old.id=NEW.id OR old.event_id=NEW.event_id OR old.execution_run_id=NEW.execution_run_id OR old.decision_request_id=NEW.decision_request_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_human_decision_requests human
 JOIN cleardev_planner_runtime_events event ON event.id=NEW.event_id
 JOIN cleardev_planner_runtime_decisions stop ON stop.event_id=event.id
 JOIN cleardev_complex_execution_runs run ON run.id=event.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_complex_engineering_plans plan ON plan.id=run.plan_id
 JOIN cleardev_complex_role_bindings planner ON planner.id=plan.planner_role_binding_id
 JOIN sessions session ON session.id=planner.ao_session_id
 WHERE human.id=NEW.decision_request_id AND human.decision_kind='AUTHORIZE_EXTRA_PLANNER_COORDINATION'
 AND human.status='PENDING' AND human.binding_schema_version=1 AND human.binding_json=NEW.binding_json
 AND human.development_project_id=project.id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED' AND run.mode='STANDARD'
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND stop.source='CONTROL_PLANE' AND stop.outcome='LIMIT_REACHED' AND stop.reason_code='PLANNER_COORDINATION_LIMIT_REACHED'
 AND stop.result_json IS NULL AND stop.result_sha256 IS NULL AND json_extract(event.report_json,'$.category')='ENGINEERING'
 AND json_extract(NEW.binding_json,'$.eventId')=event.id AND json_extract(NEW.binding_json,'$.executionRunId')=run.id
 AND json_extract(NEW.binding_json,'$.developmentRequirementId')=project.id AND json_extract(NEW.binding_json,'$.ordinal')=3
 AND json_extract(NEW.binding_json,'$.plannerRoleBindingId')=planner.id AND json_extract(NEW.binding_json,'$.aoSessionId')=session.id
 AND json_extract(NEW.binding_json,'$.providerConversationId')=session.provider_conversation_id
 AND json_extract(NEW.binding_json,'$.workspacePath')=session.workspace_path
 AND json_extract(NEW.binding_json,'$.sessionCreationKey')=session.creation_idempotency_key
 AND json_extract(NEW.binding_json,'$.creationFingerprint')=session.creation_request_fingerprint
 AND json_extract(NEW.binding_json,'$.harness')=session.harness AND json_extract(NEW.binding_json,'$.model')=session.model
 AND session.kind='worker' AND session.session_mode='chat' AND session.permission_mode='auto' AND session.is_terminated=0
 AND session.provider_conversation_id<>'' AND session.workspace_path<>'' AND session.creation_request_fingerprint<>''
 AND planner.role='ENGINEERING_PLANNER' AND planner.status='BOUND' AND planner.development_project_id=project.id
 AND session.creation_idempotency_key=planner.session_creation_idempotency_key AND session.project_id=project.ao_project_id
 AND session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
 AND version.task_set_version=run.accepted_task_set_version AND project.cancelled_at IS NULL AND project.paused_from_state IS NULL AND project.state<>'PAUSED'
 AND (SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=run.id)=2
 AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests WHERE event_id=event.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_barriers WHERE execution_run_id=run.id AND event_id<>event.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE execution_run_id=run.id AND status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
 AND NOT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=session.id AND state NOT IN('completed','failed','interrupted'))
 )
BEGIN SELECT RAISE(ABORT,'extra coordination requires the exact current limit, original Planner and untouched budgets'); END;
CREATE TRIGGER cleardev_extra_coordination_grant_exact BEFORE INSERT ON cleardev_extra_coordination_grants
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants old WHERE old.event_id=NEW.event_id OR old.execution_run_id=NEW.execution_run_id OR old.decision_request_id=NEW.decision_request_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_extra_coordination_requests offer
 JOIN cleardev_human_decision_requests human ON human.id=offer.decision_request_id
 WHERE offer.event_id=NEW.event_id AND offer.execution_run_id=NEW.execution_run_id AND human.id=NEW.decision_request_id
 AND human.decision_kind='AUTHORIZE_EXTRA_PLANNER_COORDINATION' AND human.status='RESOLVED' AND human.decision='APPROVE'
 AND human.binding_json=offer.binding_json AND human.resolved_at=NEW.created_at
 AND (SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=NEW.execution_run_id)=2
 AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests WHERE event_id=NEW.event_id)
 )
BEGIN SELECT RAISE(ABORT,'third coordination requires its single native human grant'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_extra_coordination_requests_update BEFORE UPDATE ON cleardev_extra_coordination_requests BEGIN SELECT RAISE(ABORT,'extra coordination history is immutable'); END;
CREATE TRIGGER cleardev_extra_coordination_requests_delete BEFORE DELETE ON cleardev_extra_coordination_requests BEGIN SELECT RAISE(ABORT,'extra coordination history is immutable'); END;
CREATE TRIGGER cleardev_extra_coordination_requests_cdc AFTER INSERT ON cleardev_extra_coordination_requests BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_extra_coordination_grants_update BEFORE UPDATE ON cleardev_extra_coordination_grants BEGIN SELECT RAISE(ABORT,'extra coordination history is immutable'); END;
CREATE TRIGGER cleardev_extra_coordination_grants_delete BEFORE DELETE ON cleardev_extra_coordination_grants BEGIN SELECT RAISE(ABORT,'extra coordination history is immutable'); END;
CREATE TRIGGER cleardev_extra_coordination_grants_cdc AFTER INSERT ON cleardev_extra_coordination_grants BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_extra_coordination_decisions_update BEFORE UPDATE ON cleardev_extra_coordination_decisions BEGIN SELECT RAISE(ABORT,'extra coordination history is immutable'); END;
CREATE TRIGGER cleardev_extra_coordination_decisions_delete BEFORE DELETE ON cleardev_extra_coordination_decisions BEGIN SELECT RAISE(ABORT,'extra coordination history is immutable'); END;
CREATE TRIGGER cleardev_extra_coordination_decisions_cdc AFTER INSERT ON cleardev_extra_coordination_decisions BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
DROP VIEW cleardev_planner_runtime_current_decisions;
CREATE VIEW cleardev_planner_runtime_current_decisions AS
 SELECT * FROM cleardev_planner_runtime_decisions original
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=original.event_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=original.event_id)
 UNION ALL SELECT * FROM cleardev_planner_runtime_recovery_decisions
 UNION ALL SELECT * FROM cleardev_extra_coordination_decisions;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TEMP TABLE cleardev_extra_coordination_schema_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_extra_coordination_schema_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM sqlite_master WHERE name='cleardev_planner_runtime_requests' AND instr(sql,'ordinal BETWEEN 1 AND 2')>0) THEN 1 ELSE 0 END;
PRAGMA writable_schema=ON;
UPDATE sqlite_master SET sql=replace(sql,'ordinal BETWEEN 1 AND 2','ordinal BETWEEN 1 AND 3') WHERE type='table' AND name='cleardev_planner_runtime_requests';
PRAGMA writable_schema=RESET;
INSERT INTO cleardev_extra_coordination_schema_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM sqlite_master WHERE name='cleardev_planner_runtime_requests' AND instr(sql,'ordinal BETWEEN 1 AND 3')>0) THEN 1 ELSE 0 END;
DROP TABLE cleardev_extra_coordination_schema_guard;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER cleardev_planner_runtime_request_insert;
CREATE TRIGGER cleardev_planner_runtime_request_insert
BEFORE INSERT ON cleardev_planner_runtime_requests
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests old WHERE old.event_id=NEW.event_id
             OR old.agent_step_id=NEW.agent_step_id OR (old.execution_run_id=NEW.execution_run_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=NEW.execution_run_id)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests prior
           LEFT JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=prior.event_id
           WHERE prior.execution_run_id=NEW.execution_run_id AND decision.event_id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_current_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_events event
    JOIN cleardev_complex_execution_runs run ON run.id=event.execution_run_id
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    JOIN cleardev_complex_engineering_plans plan ON plan.id=run.plan_id
    JOIN cleardev_complex_role_bindings planner ON planner.id=plan.planner_role_binding_id
    WHERE event.id=NEW.event_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND (json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1' OR json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1')
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND version.task_set_version=run.accepted_task_set_version AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND planner.id=NEW.planner_role_binding_id AND planner.role='ENGINEERING_PLANNER'
      AND planner.status='BOUND' AND planner.ao_session_id=NEW.ao_session_id
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 )
 OR (NEW.ordinal=3 AND NOT EXISTS(
 SELECT 1 FROM cleardev_extra_coordination_grants grant
 JOIN cleardev_extra_coordination_requests offer ON offer.event_id=grant.event_id
 JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
 JOIN sessions session ON session.id=NEW.ao_session_id
 WHERE grant.event_id=NEW.event_id AND grant.execution_run_id=NEW.execution_run_id
 AND human.status='RESOLVED' AND human.decision='APPROVE' AND human.decision_kind='AUTHORIZE_EXTRA_PLANNER_COORDINATION'
 AND human.binding_json=offer.binding_json AND NEW.context_json=offer.context_json AND NEW.prompt=offer.prompt
 AND NEW.context_sha256=json_extract(offer.binding_json,'$.contextSha256')
 AND NEW.planner_role_binding_id=json_extract(offer.binding_json,'$.plannerRoleBindingId')
 AND NEW.ao_session_id=json_extract(offer.binding_json,'$.aoSessionId')
 AND session.provider_conversation_id=json_extract(offer.binding_json,'$.providerConversationId')
 AND session.creation_request_fingerprint=json_extract(offer.binding_json,'$.creationFingerprint')
 AND session.workspace_path=json_extract(offer.binding_json,'$.workspacePath')
 AND session.harness=json_extract(offer.binding_json,'$.harness') AND session.model=json_extract(offer.binding_json,'$.model')
 AND session.permission_mode='auto' AND session.session_mode='chat' AND session.is_terminated=0
 ))
BEGIN SELECT RAISE(ABORT,'runtime request requires quiescent current facts, the original Planner and remaining durable quota'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_extra_coordination_decision_insert
BEFORE INSERT ON cleardev_extra_coordination_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_decisions WHERE event_id=NEW.event_id)
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
 OR NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=NEW.event_id AND grant.execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(
 SELECT 1 FROM cleardev_planner_runtime_requests request
 JOIN cleardev_agent_step_attempts attempt ON attempt.logical_step_id=request.agent_step_id
 JOIN cleardev_agent_step_results result ON result.attempt_id=attempt.id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 WHERE request.event_id=NEW.event_id AND request.ordinal=3 AND attempt.ao_session_id=request.ao_session_id
 AND attempt.role_binding_id=request.planner_role_binding_id AND attempt.prompt_sha256=step.prompt_sha256
 AND result.turn_id=step.turn_id AND result.final_message_id=step.final_message_id AND result.raw_message_sha256=step.message_sha256))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_extra_coordination_step_exact BEFORE INSERT ON cleardev_complex_agent_steps
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE NEW.id=grant.event_id||':planner' OR NEW.request_id=grant.event_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests request JOIN cleardev_extra_coordination_requests offer ON offer.event_id=request.event_id
 WHERE request.event_id=NEW.request_id AND request.agent_step_id=NEW.id AND request.ordinal=3
 AND NEW.role_binding_id=request.planner_role_binding_id AND NEW.step_kind='COMPLEX_ENGINEERING_PLAN'
 AND NEW.client_message_id='cleardev-complex-step-'||NEW.id AND NEW.prompt_sha256=offer.prompt_sha256 AND NEW.send_status='PENDING')
BEGIN SELECT RAISE(ABORT,'extra coordination step must be the exact human-authorized request'); END;
-- +goose StatementEnd


-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE TEMP TABLE cleardev_extra_coordination_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_extra_coordination_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_extra_coordination_requests)
 OR EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants)
 OR EXISTS(SELECT 1 FROM cleardev_extra_coordination_decisions)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests WHERE ordinal>2)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_extra_coordination_down_guard;
DROP TRIGGER cleardev_extra_coordination_message_exact;
DROP TRIGGER cleardev_extra_coordination_step_exact;
DROP TRIGGER cleardev_planner_runtime_request_insert;
DROP VIEW cleardev_planner_runtime_current_decisions;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE VIEW cleardev_planner_runtime_current_decisions AS
 SELECT * FROM cleardev_planner_runtime_decisions original
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=original.event_id)
 UNION ALL SELECT * FROM cleardev_planner_runtime_recovery_decisions;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_planner_runtime_request_insert
BEFORE INSERT ON cleardev_planner_runtime_requests
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests old WHERE old.event_id=NEW.event_id
             OR old.agent_step_id=NEW.agent_step_id OR (old.execution_run_id=NEW.execution_run_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=NEW.execution_run_id)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests prior
           LEFT JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=prior.event_id
           WHERE prior.execution_run_id=NEW.execution_run_id AND decision.event_id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_events event
    JOIN cleardev_complex_execution_runs run ON run.id=event.execution_run_id
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    JOIN cleardev_complex_engineering_plans plan ON plan.id=run.plan_id
    JOIN cleardev_complex_role_bindings planner ON planner.id=plan.planner_role_binding_id
    WHERE event.id=NEW.event_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND (json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1' OR json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1')
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND version.task_set_version=run.accepted_task_set_version AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND planner.id=NEW.planner_role_binding_id AND planner.role='ENGINEERING_PLANNER'
      AND planner.status='BOUND' AND planner.ao_session_id=NEW.ao_session_id
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 )
BEGIN SELECT RAISE(ABORT,'runtime request requires quiescent current facts, the original Planner and remaining durable quota'); END;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE cleardev_extra_coordination_decisions;
DROP TABLE cleardev_extra_coordination_grants;
DROP TABLE cleardev_extra_coordination_requests;
PRAGMA writable_schema=ON;
UPDATE sqlite_master SET sql=replace(sql,'ordinal BETWEEN 1 AND 3','ordinal BETWEEN 1 AND 2') WHERE type='table' AND name='cleardev_planner_runtime_requests';
PRAGMA writable_schema=RESET;
-- +goose StatementEnd
