-- Precise human-authorized handoff of a wholly unsent project Builder step.
-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
-- +goose StatementBegin
BEGIN IMMEDIATE;
ALTER TABLE cleardev_agent_attempt_events RENAME TO cleardev_agent_attempt_events_before_handoff;
DROP INDEX idx_cleardev_agent_attempt_events_attempt;
CREATE TABLE cleardev_agent_attempt_events (
    id                  TEXT PRIMARY KEY,
    attempt_id          TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
    status              TEXT NOT NULL CHECK (status IN (
        'SENT', 'CORRECTION_SENT', 'COMPLETED', 'FAILED', 'INTERRUPTED',
        'OBSERVATION_TIMEOUT', 'DELIVERY_UNKNOWN', 'RETIRED_BEFORE_SEND'
    )),
    client_message_id   TEXT NOT NULL DEFAULT '',
    prompt_sha256       TEXT NOT NULL DEFAULT '' CHECK (
        prompt_sha256 = '' OR (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*')
    ),
    turn_id             TEXT NOT NULL DEFAULT '',
    turn_state          TEXT NOT NULL DEFAULT '' CHECK (turn_state IN ('', 'queued', 'running', 'completed', 'interrupted', 'failed')),
    failure_category    TEXT NOT NULL DEFAULT '' CHECK (failure_category IN (
        '', 'MODEL_UNAVAILABLE', 'AUTHENTICATION_REQUIRED', 'QUOTA_EXHAUSTED',
        'RATE_LIMITED', 'PROVIDER_UNAVAILABLE', 'DRIVER_INCOMPATIBLE',
        'SESSION_LOST', 'TURN_INTERRUPTED', 'OBSERVATION_TIMEOUT',
        'DELIVERY_UNKNOWN', 'PROVIDER_FAILURE', 'RESULT_INVALID'
    )),
    retryable           INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1)),
    retry_at            TIMESTAMP,
    provider_error_code TEXT NOT NULL DEFAULT '',
    error_summary       TEXT NOT NULL DEFAULT '',
    recorded_at         TIMESTAMP NOT NULL,
    CHECK (
        (status IN ('SENT', 'CORRECTION_SENT', 'COMPLETED') AND failure_category = '')
        OR (status NOT IN ('SENT', 'CORRECTION_SENT', 'COMPLETED', 'RETIRED_BEFORE_SEND') AND failure_category <> '')
        OR (status='RETIRED_BEFORE_SEND' AND turn_id='' AND turn_state='' AND failure_category='' AND retryable=0 AND retry_at IS NULL AND provider_error_code='')
    )
);
CREATE INDEX idx_cleardev_agent_attempt_events_attempt
    ON cleardev_agent_attempt_events (attempt_id, recorded_at, id);


INSERT INTO cleardev_agent_attempt_events(rowid,id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at)
SELECT rowid,id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at FROM cleardev_agent_attempt_events_before_handoff;
DROP TABLE cleardev_agent_attempt_events_before_handoff;
CREATE TRIGGER cleardev_agent_attempt_events_update_forbidden BEFORE UPDATE ON cleardev_agent_attempt_events BEGIN SELECT RAISE(ABORT,'cleardev agent attempt events are immutable'); END;
CREATE TRIGGER cleardev_agent_attempt_events_delete_forbidden BEFORE DELETE ON cleardev_agent_attempt_events BEGIN SELECT RAISE(ABORT,'cleardev agent attempt events are append-only'); END;
CREATE TRIGGER cleardev_agent_attempt_events_no_replace
BEFORE INSERT ON cleardev_agent_attempt_events
WHEN EXISTS(SELECT 1 FROM cleardev_agent_attempt_events old WHERE old.id=NEW.id OR old.rowid=NEW.rowid)
BEGIN SELECT RAISE(ABORT,'cleardev agent attempt event identity already exists'); END;
CREATE TRIGGER cleardev_agent_attempt_events_cdc_insert
AFTER INSERT ON cleardev_agent_attempt_events BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', attempt.development_project_id,
                       'logicalStepId', attempt.logical_step_id,
                       'agentAttemptId', attempt.id,
                       'attemptStatus', NEW.status),
           NEW.recorded_at
    FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_development_projects AS project ON project.id = attempt.development_project_id
    WHERE attempt.id = NEW.attempt_id;
END;
CREATE TABLE cleardev_builder_replacement_requests (id TEXT PRIMARY KEY NOT NULL, requirement_id TEXT NOT NULL REFERENCES cleardev_development_projects(id), execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id), logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id), decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id) DEFERRABLE INITIALLY DEFERRED, target_id TEXT NOT NULL UNIQUE, binding_json TEXT NOT NULL CHECK(json_valid(binding_json)), source_json TEXT NOT NULL CHECK(json_valid(source_json)), old_dispatch_json TEXT NOT NULL CHECK(json_valid(old_dispatch_json)), old_step_json TEXT NOT NULL CHECK(json_valid(old_step_json)), original_stopped_at TIMESTAMP NOT NULL, original_status TEXT NOT NULL, original_reason TEXT NOT NULL, original_summary TEXT NOT NULL, supplement TEXT NOT NULL CHECK(length(supplement)<=16000), created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_replacement_requests_update_forbidden BEFORE UPDATE ON cleardev_builder_replacement_requests BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_requests_delete_forbidden BEFORE DELETE ON cleardev_builder_replacement_requests BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_requests_no_replace BEFORE INSERT ON cleardev_builder_replacement_requests WHEN EXISTS(SELECT 1 FROM cleardev_builder_replacement_requests old WHERE old.rowid=NEW.rowid OR (old.id=NEW.id) OR (old.logical_step_id=NEW.logical_step_id) OR (old.decision_request_id=NEW.decision_request_id) OR (old.target_id=NEW.target_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_replacement_grants (decision_request_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_human_decision_requests(id), request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_builder_replacement_requests(id), logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id), created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_replacement_grants_update_forbidden BEFORE UPDATE ON cleardev_builder_replacement_grants BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_grants_delete_forbidden BEFORE DELETE ON cleardev_builder_replacement_grants BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_grants_no_replace BEFORE INSERT ON cleardev_builder_replacement_grants WHEN EXISTS(SELECT 1 FROM cleardev_builder_replacement_grants old WHERE old.rowid=NEW.rowid OR (old.decision_request_id=NEW.decision_request_id) OR (old.request_id=NEW.request_id) OR (old.logical_step_id=NEW.logical_step_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_replacement_handoffs (id TEXT PRIMARY KEY NOT NULL, request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_builder_replacement_requests(id), decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_builder_replacement_grants(decision_request_id), logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id), new_role_binding_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_role_bindings(id) DEFERRABLE INITIALLY DEFERRED, session_creation_key TEXT NOT NULL UNIQUE, retirement_event_id TEXT NOT NULL UNIQUE REFERENCES cleardev_agent_attempt_events(id) DEFERRABLE INITIALLY DEFERRED, second_attempt_id TEXT NOT NULL UNIQUE, supplement TEXT NOT NULL CHECK(length(supplement)<=16000), created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_replacement_handoffs_update_forbidden BEFORE UPDATE ON cleardev_builder_replacement_handoffs BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_handoffs_delete_forbidden BEFORE DELETE ON cleardev_builder_replacement_handoffs BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_handoffs_no_replace BEFORE INSERT ON cleardev_builder_replacement_handoffs WHEN EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs old WHERE old.rowid=NEW.rowid OR (old.id=NEW.id) OR (old.request_id=NEW.request_id) OR (old.decision_request_id=NEW.decision_request_id) OR (old.logical_step_id=NEW.logical_step_id) OR (old.new_role_binding_id=NEW.new_role_binding_id) OR (old.session_creation_key=NEW.session_creation_key) OR (old.retirement_event_id=NEW.retirement_event_id) OR (old.second_attempt_id=NEW.second_attempt_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_replacement_aliases (handoff_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_builder_replacement_handoffs(id), logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id), new_role_binding_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_role_bindings(id), new_ao_session_id TEXT NOT NULL UNIQUE REFERENCES sessions(id), workspace_path TEXT NOT NULL, launch_sha256 TEXT NOT NULL CHECK(length(launch_sha256)=64), snapshot_sha256 TEXT NOT NULL CHECK(length(snapshot_sha256)=64), second_attempt_id TEXT NOT NULL UNIQUE REFERENCES cleardev_agent_step_attempts(id) DEFERRABLE INITIALLY DEFERRED, created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_replacement_aliases_update_forbidden BEFORE UPDATE ON cleardev_builder_replacement_aliases BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_aliases_delete_forbidden BEFORE DELETE ON cleardev_builder_replacement_aliases BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_aliases_no_replace BEFORE INSERT ON cleardev_builder_replacement_aliases WHEN EXISTS(SELECT 1 FROM cleardev_builder_replacement_aliases old WHERE old.rowid=NEW.rowid OR (old.handoff_id=NEW.handoff_id) OR (old.logical_step_id=NEW.logical_step_id) OR (old.new_role_binding_id=NEW.new_role_binding_id) OR (old.new_ao_session_id=NEW.new_ao_session_id) OR (old.second_attempt_id=NEW.second_attempt_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_replacement_observations (id TEXT PRIMARY KEY NOT NULL, handoff_id TEXT NOT NULL REFERENCES cleardev_builder_replacement_handoffs(id), stage TEXT NOT NULL CHECK(stage IN('CREATE','COPY','BIND','SEND')), operation_key TEXT NOT NULL, outcome TEXT NOT NULL CHECK(outcome IN('STARTED','CONFIRMED','FAILED','UNKNOWN')), reason_code TEXT NOT NULL, ao_session_id TEXT NOT NULL, workspace_path TEXT NOT NULL, launch_sha256 TEXT NOT NULL, snapshot_sha256 TEXT NOT NULL, observed_at TIMESTAMP NOT NULL, UNIQUE(handoff_id,stage,operation_key,outcome));
CREATE UNIQUE INDEX idx_cleardev_builder_replacement_one_stage_claim ON cleardev_builder_replacement_observations(handoff_id,stage) WHERE outcome='STARTED';
CREATE TRIGGER cleardev_builder_replacement_observations_update_forbidden BEFORE UPDATE ON cleardev_builder_replacement_observations BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_observations_delete_forbidden BEFORE DELETE ON cleardev_builder_replacement_observations BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_observations_no_replace BEFORE INSERT ON cleardev_builder_replacement_observations WHEN EXISTS(SELECT 1 FROM cleardev_builder_replacement_observations old WHERE old.rowid=NEW.rowid OR (old.id=NEW.id) OR (old.handoff_id=NEW.handoff_id AND old.stage=NEW.stage AND old.operation_key=NEW.operation_key AND old.outcome=NEW.outcome) OR (old.handoff_id=NEW.handoff_id AND old.stage=NEW.stage AND old.outcome='STARTED' AND NEW.outcome='STARTED')) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_session_fences (ao_session_id TEXT PRIMARY KEY NOT NULL REFERENCES sessions(id), old_role_binding_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_role_bindings(id), handoff_id TEXT NOT NULL UNIQUE REFERENCES cleardev_builder_replacement_handoffs(id), created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_session_fences_update_forbidden BEFORE UPDATE ON cleardev_builder_session_fences BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_session_fences_delete_forbidden BEFORE DELETE ON cleardev_builder_session_fences BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_session_fences_no_replace BEFORE INSERT ON cleardev_builder_session_fences WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_fences old WHERE old.rowid=NEW.rowid OR (old.ao_session_id=NEW.ao_session_id) OR (old.old_role_binding_id=NEW.old_role_binding_id) OR (old.handoff_id=NEW.handoff_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_session_operations (id TEXT PRIMARY KEY NOT NULL, ao_session_id TEXT NOT NULL REFERENCES sessions(id), kind TEXT NOT NULL CHECK(length(trim(kind))>0), created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_session_operations_update_forbidden BEFORE UPDATE ON cleardev_builder_session_operations BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_session_operations_delete_forbidden BEFORE DELETE ON cleardev_builder_session_operations BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_session_operations_no_replace BEFORE INSERT ON cleardev_builder_session_operations WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_operations old WHERE old.rowid=NEW.rowid OR (old.id=NEW.id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_session_operation_ends (operation_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_builder_session_operations(id), outcome TEXT NOT NULL CHECK(outcome IN('COMPLETED','FAILED_BEFORE_ACTION')), created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_session_operation_ends_update_forbidden BEFORE UPDATE ON cleardev_builder_session_operation_ends BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_session_operation_ends_delete_forbidden BEFORE DELETE ON cleardev_builder_session_operation_ends BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_session_operation_ends_no_replace BEFORE INSERT ON cleardev_builder_session_operation_ends WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_operation_ends old WHERE old.rowid=NEW.rowid OR (old.operation_id=NEW.operation_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TABLE cleardev_builder_handoff_contexts (ao_session_id TEXT PRIMARY KEY NOT NULL REFERENCES sessions(id), context_json TEXT NOT NULL CHECK(json_valid(context_json)), reference_path TEXT NOT NULL, context_sha256 TEXT NOT NULL CHECK(length(context_sha256)=64), snapshot_sha256 TEXT NOT NULL CHECK(length(snapshot_sha256)=64), system_prompt TEXT NOT NULL, launch_fingerprint TEXT NOT NULL CHECK(length(trim(launch_fingerprint))>0), created_at TIMESTAMP NOT NULL);
CREATE TRIGGER cleardev_builder_handoff_contexts_update_forbidden BEFORE UPDATE ON cleardev_builder_handoff_contexts BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_handoff_contexts_delete_forbidden BEFORE DELETE ON cleardev_builder_handoff_contexts BEGIN SELECT RAISE(ABORT,'Builder handoff facts are immutable'); END;
CREATE TRIGGER cleardev_builder_handoff_contexts_no_replace BEFORE INSERT ON cleardev_builder_handoff_contexts WHEN EXISTS(SELECT 1 FROM cleardev_builder_handoff_contexts old WHERE old.rowid=NEW.rowid OR (old.ao_session_id=NEW.ao_session_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff fact identity already exists'); END;
CREATE TRIGGER cleardev_builder_replacement_requests_cdc AFTER INSERT ON cleardev_builder_replacement_requests BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.id),NEW.created_at FROM (SELECT p.ao_project_id AS project_id FROM cleardev_development_projects p WHERE p.id=NEW.requirement_id); END;
CREATE TRIGGER cleardev_builder_replacement_grants_cdc AFTER INSERT ON cleardev_builder_replacement_grants BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.decision_request_id),NEW.created_at FROM (SELECT p.ao_project_id AS project_id FROM cleardev_development_projects p JOIN cleardev_builder_replacement_requests r ON r.requirement_id=p.id WHERE r.id=NEW.request_id); END;
CREATE TRIGGER cleardev_builder_replacement_handoffs_cdc AFTER INSERT ON cleardev_builder_replacement_handoffs BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.id),NEW.created_at FROM (SELECT p.ao_project_id AS project_id FROM cleardev_development_projects p JOIN cleardev_builder_replacement_requests r ON r.requirement_id=p.id WHERE r.id=NEW.request_id); END;
CREATE TRIGGER cleardev_builder_replacement_aliases_cdc AFTER INSERT ON cleardev_builder_replacement_aliases BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.handoff_id),NEW.created_at FROM (SELECT p.ao_project_id AS project_id FROM cleardev_development_projects p JOIN cleardev_builder_replacement_requests r ON r.requirement_id=p.id JOIN cleardev_builder_replacement_handoffs h ON h.request_id=r.id WHERE h.id=NEW.handoff_id); END;
CREATE TRIGGER cleardev_builder_replacement_observations_cdc AFTER INSERT ON cleardev_builder_replacement_observations BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.handoff_id),NEW.observed_at FROM (SELECT p.ao_project_id AS project_id FROM cleardev_development_projects p JOIN cleardev_builder_replacement_requests r ON r.requirement_id=p.id JOIN cleardev_builder_replacement_handoffs h ON h.request_id=r.id WHERE h.id=NEW.handoff_id); END;
CREATE TRIGGER cleardev_builder_session_fences_cdc AFTER INSERT ON cleardev_builder_session_fences BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.ao_session_id),NEW.created_at FROM (SELECT s.project_id AS project_id FROM sessions s WHERE s.id=NEW.ao_session_id); END;
CREATE TRIGGER cleardev_builder_session_operations_cdc AFTER INSERT ON cleardev_builder_session_operations BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.ao_session_id),NEW.created_at FROM (SELECT s.project_id AS project_id FROM sessions s WHERE s.id=NEW.ao_session_id); END;
CREATE TRIGGER cleardev_builder_session_operation_ends_cdc AFTER INSERT ON cleardev_builder_session_operation_ends BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.operation_id),NEW.created_at FROM (SELECT s.project_id AS project_id FROM sessions s JOIN cleardev_builder_session_operations op ON op.ao_session_id=s.id WHERE op.id=NEW.operation_id); END;
CREATE TRIGGER cleardev_builder_handoff_contexts_cdc AFTER INSERT ON cleardev_builder_handoff_contexts BEGIN INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT project_id,NULL,'cleardev_project_updated',json_object('builderHandoffFact',NEW.ao_session_id),NEW.created_at FROM (SELECT s.project_id AS project_id FROM sessions s WHERE s.id=NEW.ao_session_id); END;
CREATE TRIGGER cleardev_builder_replacement_request_current BEFORE INSERT ON cleardev_builder_replacement_requests WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_runs run JOIN cleardev_contract_versions v ON v.id=run.requirement_version_id JOIN cleardev_development_projects p ON p.id=run.development_project_id JOIN cleardev_complex_execution_agent_steps step ON step.id=NEW.logical_step_id JOIN cleardev_complex_execution_role_bindings b ON b.id=step.role_binding_id JOIN cleardev_complex_execution_task_attempts a ON a.agent_step_id=step.id JOIN cleardev_message_budget_versions mb ON mb.development_project_id=p.id
 WHERE p.id=NEW.requirement_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED' AND run.settled_at IS NULL AND run.mode='STANDARD' AND json_extract(run.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1' AND v.state='APPROVED' AND v.superseded_by_id IS NULL AND p.cancelled_at IS NULL AND p.paused_from_state IS NULL AND p.state<>'PAUSED' AND mb.version='MESSAGE_BUDGET_V1' AND b.status='BOUND' AND b.role='BUILDER' AND a.status='BLOCKED' AND a.reason_code='BUILDER_SPAWN_FAILED' AND a.settled_at=NEW.original_stopped_at AND step.send_status='PENDING' AND step.sent_at IS NULL AND step.turn_id IS NULL
 AND step.id=json_extract(NEW.binding_json,'$.logicalStepId') AND step.role_binding_id=json_extract(NEW.binding_json,'$.oldRoleBindingId') AND step.client_message_id=json_extract(NEW.binding_json,'$.clientMessageId') AND step.prompt_sha256=json_extract(NEW.binding_json,'$.promptSha256') AND a.id=json_extract(NEW.binding_json,'$.dispatchId') AND a.task_mapping_id=json_extract(NEW.binding_json,'$.taskId') AND a.round=json_extract(NEW.binding_json,'$.round') AND a.base_commit_sha=json_extract(NEW.binding_json,'$.baseCommitSha') AND b.ao_session_id=json_extract(NEW.binding_json,'$.oldAOSessionId') AND b.workspace_path=json_extract(NEW.binding_json,'$.oldWorkspacePath') AND run.id=json_extract(NEW.binding_json,'$.executionRunId') AND p.id=json_extract(NEW.binding_json,'$.developmentRequirementId') AND NEW.target_id=json_extract(NEW.binding_json,'$.targetId')
 AND EXISTS(SELECT 1 FROM cleardev_human_decision_requests human WHERE human.id=NEW.decision_request_id AND human.development_project_id=p.id AND human.decision_kind='AUTHORIZE_BUILDER_REPLACEMENT' AND human.status='PENDING' AND human.binding_json=NEW.binding_json)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_message_reservations WHERE logical_step_id=step.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts attempt JOIN cleardev_agent_attempt_events event ON event.attempt_id=attempt.id WHERE attempt.logical_step_id=step.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts attempt JOIN cleardev_agent_step_results result ON result.attempt_id=attempt.id WHERE attempt.logical_step_id=step.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts WHERE logical_step_id=step.id AND attempt_number<>1)
) BEGIN SELECT RAISE(ABORT,'Builder replacement requires an exact current wholly unsent stop'); END;
CREATE TRIGGER cleardev_builder_replacement_grant_valid BEFORE INSERT ON cleardev_builder_replacement_grants WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_requests r JOIN cleardev_human_decision_requests d ON d.id=r.decision_request_id JOIN cleardev_human_decision_effects e ON e.request_id=d.id WHERE r.id=NEW.request_id AND r.logical_step_id=NEW.logical_step_id AND d.id=NEW.decision_request_id AND d.decision_kind='AUTHORIZE_BUILDER_REPLACEMENT' AND d.status='RESOLVED' AND d.decision='APPROVE' AND d.binding_json=r.binding_json AND e.decision='APPROVE'
) BEGIN SELECT RAISE(ABORT,'Builder replacement requires an exact native grant'); END;
CREATE TRIGGER cleardev_builder_session_operation_unfenced BEFORE INSERT ON cleardev_builder_session_operations WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_fences WHERE ao_session_id=NEW.ao_session_id) BEGIN SELECT RAISE(ABORT,'old Builder is fenced'); END;
CREATE TRIGGER cleardev_builder_session_fence_idle BEFORE INSERT ON cleardev_builder_session_fences WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_operations op WHERE op.ao_session_id=NEW.ao_session_id AND NOT EXISTS(SELECT 1 FROM cleardev_builder_session_operation_ends done WHERE done.operation_id=op.id)) BEGIN SELECT RAISE(ABORT,'old Builder has an unresolved external operation'); END;
CREATE TRIGGER cleardev_builder_message_fenced BEFORE INSERT ON cleardev_agent_message_reservations WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_fences WHERE ao_session_id=NEW.ao_session_id) BEGIN SELECT RAISE(ABORT,'old Builder is fenced from sending'); END;
CREATE TRIGGER cleardev_builder_session_fenced_delete BEFORE DELETE ON sessions WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_fences WHERE ao_session_id=OLD.id) BEGIN SELECT RAISE(ABORT,'old Builder history is fenced'); END;
CREATE TRIGGER cleardev_builder_retirement_valid BEFORE INSERT ON cleardev_agent_attempt_events WHEN NEW.status='RETIRED_BEFORE_SEND' AND NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id JOIN cleardev_agent_step_attempts first ON first.logical_step_id=h.logical_step_id AND first.attempt_number=1
 WHERE h.retirement_event_id=NEW.id AND first.id=NEW.attempt_id AND first.id=h.logical_step_id||':attempt:1' AND first.role_binding_id=json_extract(r.binding_json,'$.oldRoleBindingId') AND first.ao_session_id=json_extract(r.binding_json,'$.oldAOSessionId') AND first.prompt_sha256=json_extract(r.binding_json,'$.promptSha256') AND NEW.client_message_id=first.client_message_id AND NEW.prompt_sha256=first.prompt_sha256
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE attempt_id=first.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_message_reservations WHERE logical_step_id=first.logical_step_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_results WHERE attempt_id=first.id)
) BEGIN SELECT RAISE(ABORT,'only the exact authorized wholly unsent Builder may be retired'); END;
CREATE TRIGGER cleardev_builder_retirement_no_replace BEFORE INSERT ON cleardev_agent_attempt_events WHEN EXISTS(SELECT 1 FROM cleardev_agent_attempt_events old WHERE old.status='RETIRED_BEFORE_SEND' AND (old.id=NEW.id OR old.rowid=NEW.rowid OR old.attempt_id=NEW.attempt_id)) BEGIN SELECT RAISE(ABORT,'retired Builder evidence is immutable'); END;
DROP TRIGGER cleardev_complex_execution_candidate_binding_valid;
CREATE TRIGGER cleardev_complex_execution_candidate_binding_valid
BEFORE INSERT ON cleardev_candidate_commits
WHEN (NEW.complex_execution_task_attempt_id IS NULL) <> (NEW.complex_execution_base_commit_sha IS NULL)
 OR (NEW.complex_execution_task_attempt_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
    WHERE attempt.id = NEW.complex_execution_task_attempt_id AND attempt.base_commit_sha = NEW.complex_execution_base_commit_sha
      AND task.work_item_id = NEW.work_item_id AND work_item.complex_execution_task_id = task.id
      AND ((builder.status='BOUND' AND builder.ao_session_id=NEW.ao_session_id) OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_aliases alias JOIN cleardev_builder_replacement_handoffs handoff ON handoff.id=alias.handoff_id JOIN cleardev_builder_replacement_requests request ON request.id=handoff.request_id JOIN cleardev_complex_execution_role_bindings replacement ON replacement.id=alias.new_role_binding_id WHERE alias.logical_step_id=attempt.agent_step_id AND json_extract(request.binding_json,'$.dispatchId')=attempt.id AND json_extract(request.binding_json,'$.oldRoleBindingId')=builder.id AND alias.new_ao_session_id=NEW.ao_session_id AND replacement.status='BOUND' AND replacement.ao_session_id=NEW.ao_session_id AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_agent_steps step JOIN cleardev_agent_step_attempts second ON second.id=alias.second_attempt_id JOIN cleardev_agent_message_reservations reserved ON reserved.attempt_id=second.id JOIN cleardev_agent_message_confirmations confirmed ON confirmed.client_message_id=reserved.client_message_id JOIN cleardev_agent_attempt_events completed ON completed.attempt_id=second.id AND completed.client_message_id=reserved.client_message_id AND completed.turn_id=confirmed.turn_id JOIN cleardev_agent_step_results result ON result.attempt_id=second.id AND result.client_message_id=reserved.client_message_id AND result.turn_id=confirmed.turn_id JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=result.id
 WHERE step.id=alias.logical_step_id AND step.send_status='SETTLED' AND step.turn_id=confirmed.turn_id AND step.final_message_id=result.final_message_id AND second.ao_session_id=NEW.ao_session_id AND second.role_binding_id=alias.new_role_binding_id AND completed.status='COMPLETED' AND completed.turn_state='completed' AND parsed.conclusion='VALID'
 )))
      AND NEW.dispatch_id IS NULL AND NEW.base_commit_sha IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution candidate must use its Builder, task, and fixed base only');
END;

DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
     AND NOT(OLD.status IN('BLOCKED','NEEDS_HUMAN') AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action IN('RETRY_REVIEW','RETRY_CHECK') AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action='RETRY_BUILDER_SESSION' AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code='BUILDER_BUDGET_EXHAUSTED' AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at))
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id WHERE json_extract(r.binding_json,'$.dispatchId')=OLD.id AND h.logical_step_id=OLD.agent_step_id AND r.original_stopped_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;

CREATE TRIGGER cleardev_builder_replacement_handoff_valid BEFORE INSERT ON cleardev_builder_replacement_handoffs WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_requests r JOIN cleardev_builder_replacement_grants g ON g.request_id=r.id JOIN cleardev_complex_execution_agent_steps step ON step.id=r.logical_step_id JOIN cleardev_complex_execution_task_attempts a ON a.id=json_extract(r.binding_json,'$.dispatchId') JOIN cleardev_complex_execution_role_bindings b ON b.id=json_extract(r.binding_json,'$.oldRoleBindingId')
 WHERE r.id=NEW.request_id AND g.decision_request_id=NEW.decision_request_id AND NEW.logical_step_id=r.logical_step_id AND NEW.new_role_binding_id=json_extract(r.binding_json,'$.newRoleBindingId') AND NEW.session_creation_key=json_extract(r.binding_json,'$.sessionCreationKey') AND NEW.retirement_event_id=step.id||':replacement-retired' AND NEW.second_attempt_id=step.id||':attempt:2' AND b.status='BOUND' AND a.status='BLOCKED' AND a.reason_code='BUILDER_SPAWN_FAILED' AND a.settled_at=r.original_stopped_at AND step.send_status='PENDING' AND step.sent_at IS NULL AND step.turn_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_message_reservations WHERE logical_step_id=step.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts first JOIN cleardev_agent_attempt_events e ON e.attempt_id=first.id WHERE first.logical_step_id=step.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts WHERE logical_step_id=step.id AND attempt_number<>1)
) BEGIN SELECT RAISE(ABORT,'Builder handoff requires the exact unused grant and original unsent stop'); END;
CREATE TRIGGER cleardev_builder_replacement_role_valid BEFORE INSERT ON cleardev_complex_execution_role_bindings WHEN NEW.role='BUILDER' AND NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id JOIN cleardev_complex_execution_role_bindings old ON old.id=json_extract(r.binding_json,'$.oldRoleBindingId') JOIN cleardev_builder_session_fences f ON f.handoff_id=h.id
 WHERE NEW.id=h.new_role_binding_id AND NEW.execution_run_id=r.execution_run_id AND NEW.continuation_of_role_binding_id=old.id AND NEW.session_creation_idempotency_key=h.session_creation_key AND NEW.builder_slot=old.builder_slot AND NEW.status='REQUESTED' AND old.status='ENDED' AND old.reason_code='BUILDER_REPLACED' AND f.old_role_binding_id=old.id
) BEGIN SELECT RAISE(ABORT,'replacement Builder requires its exact authorized handoff'); END;
CREATE TRIGGER cleardev_builder_session_operation_exclusive BEFORE INSERT ON cleardev_builder_session_operations WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_operations op WHERE op.ao_session_id=NEW.ao_session_id AND NOT EXISTS(SELECT 1 FROM cleardev_builder_session_operation_ends ended WHERE ended.operation_id=op.id)) BEGIN SELECT RAISE(ABORT,'Builder has an unresolved external operation'); END;
CREATE TRIGGER cleardev_builder_session_fence_valid BEFORE INSERT ON cleardev_builder_session_fences WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id JOIN cleardev_complex_execution_role_bindings b ON b.id=json_extract(r.binding_json,'$.oldRoleBindingId') WHERE h.id=NEW.handoff_id AND b.id=NEW.old_role_binding_id AND b.ao_session_id=NEW.ao_session_id AND b.status='BOUND' AND NEW.ao_session_id=json_extract(r.binding_json,'$.oldAOSessionId')
) BEGIN SELECT RAISE(ABORT,'Builder fence changed its authorized old identity'); END;
CREATE TRIGGER cleardev_builder_handoff_context_valid BEFORE INSERT ON cleardev_builder_handoff_contexts WHEN NOT EXISTS(
 SELECT 1 FROM sessions s JOIN cleardev_builder_replacement_handoffs h ON h.session_creation_key=s.creation_idempotency_key JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id JOIN cleardev_development_projects p ON p.id=r.requirement_id
 WHERE s.id=NEW.ao_session_id AND s.project_id=p.ao_project_id AND s.kind='worker' AND s.harness IN ('codex','opencode') AND s.harness=(SELECT old.harness FROM sessions old WHERE old.id=json_extract(r.binding_json,'$.oldAOSessionId')) AND s.session_mode='chat' AND s.permission_mode='auto' AND s.creation_request_fingerprint=NEW.launch_fingerprint AND s.id<>json_extract(r.binding_json,'$.oldAOSessionId') AND s.workspace_path<>json_extract(r.binding_json,'$.oldWorkspacePath') AND NEW.reference_path=json_extract(r.binding_json,'$.handoffPath') AND NEW.context_sha256=json_extract(r.binding_json,'$.handoffSha256') AND NEW.snapshot_sha256=json_extract(r.binding_json,'$.snapshotSha256') AND json_extract(NEW.context_json,'$.referencePath')=NEW.reference_path AND json_extract(NEW.context_json,'$.sha256')=NEW.context_sha256 AND json_extract(NEW.context_json,'$.snapshotSha256')=NEW.snapshot_sha256
) BEGIN SELECT RAISE(ABORT,'Builder private launch context lacks its exact handoff source'); END;
CREATE TRIGGER cleardev_builder_replacement_alias_valid BEFORE INSERT ON cleardev_builder_replacement_aliases WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id JOIN cleardev_builder_replacement_grants g ON g.request_id=r.id JOIN cleardev_complex_execution_role_bindings b ON b.id=h.new_role_binding_id JOIN sessions s ON s.id=b.ao_session_id JOIN cleardev_builder_handoff_contexts c ON c.ao_session_id=s.id JOIN cleardev_builder_session_fences f ON f.handoff_id=h.id JOIN cleardev_agent_attempt_events retired ON retired.id=h.retirement_event_id
 WHERE h.id=NEW.handoff_id AND h.logical_step_id=NEW.logical_step_id AND h.second_attempt_id=NEW.second_attempt_id AND h.new_role_binding_id=NEW.new_role_binding_id AND b.status='BOUND' AND b.continuation_of_role_binding_id=json_extract(r.binding_json,'$.oldRoleBindingId') AND s.id=NEW.new_ao_session_id AND s.kind='worker' AND s.harness IN ('codex','opencode') AND s.harness=(SELECT old.harness FROM sessions old WHERE old.id=f.ao_session_id) AND s.session_mode='chat' AND s.permission_mode='auto' AND s.id<>f.ao_session_id AND s.workspace_path=NEW.workspace_path AND s.workspace_path<>json_extract(r.binding_json,'$.oldWorkspacePath') AND s.creation_idempotency_key=h.session_creation_key AND s.creation_request_fingerprint=NEW.launch_sha256 AND c.launch_fingerprint=NEW.launch_sha256 AND c.snapshot_sha256=NEW.snapshot_sha256 AND NEW.snapshot_sha256=json_extract(r.binding_json,'$.snapshotSha256') AND retired.status='RETIRED_BEFORE_SEND' AND retired.attempt_id=h.logical_step_id||':attempt:1'
) BEGIN SELECT RAISE(ABORT,'Builder identity alias lacks exact grant, retirement and launch proof'); END;
CREATE TRIGGER cleardev_builder_replacement_attempt_valid BEFORE INSERT ON cleardev_agent_step_attempts WHEN EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs WHERE logical_step_id=NEW.logical_step_id) AND NEW.attempt_number<>1 AND NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_aliases alias JOIN cleardev_builder_replacement_handoffs h ON h.id=alias.handoff_id JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id JOIN cleardev_agent_step_attempts first ON first.id=h.logical_step_id||':attempt:1' JOIN cleardev_agent_attempt_events retired ON retired.id=h.retirement_event_id
 WHERE NEW.attempt_number=2 AND NEW.id=alias.second_attempt_id AND NEW.logical_step_id=alias.logical_step_id AND NEW.development_project_id=r.requirement_id AND NEW.step_category='COMPLEX_EXECUTION' AND NEW.step_kind='BUILDER_TASK' AND NEW.role_binding_id=alias.new_role_binding_id AND NEW.ao_session_id=alias.new_ao_session_id AND NEW.client_message_id=first.client_message_id||':attempt:2' AND NEW.prompt_sha256=first.prompt_sha256 AND NEW.trigger_failure_event_id=retired.id AND retired.status='RETIRED_BEFORE_SEND' AND NEW.created_at>=retired.recorded_at
) BEGIN SELECT RAISE(ABORT,'Builder replacement permits only its exact second attempt'); END;
CREATE TRIGGER cleardev_builder_replacement_first_valid BEFORE INSERT ON cleardev_agent_step_attempts WHEN NEW.attempt_number=1 AND EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs WHERE logical_step_id=NEW.logical_step_id) AND NOT EXISTS(
 SELECT 1 FROM cleardev_builder_replacement_requests r WHERE r.logical_step_id=NEW.logical_step_id AND NEW.id=NEW.logical_step_id||':attempt:1' AND NEW.development_project_id=r.requirement_id AND NEW.step_category='COMPLEX_EXECUTION' AND NEW.step_kind='BUILDER_TASK' AND NEW.role_binding_id=json_extract(r.binding_json,'$.oldRoleBindingId') AND NEW.ao_session_id=json_extract(r.binding_json,'$.oldAOSessionId') AND NEW.client_message_id=json_extract(r.binding_json,'$.clientMessageId') AND NEW.prompt_sha256=json_extract(r.binding_json,'$.promptSha256')
) BEGIN SELECT RAISE(ABORT,'Builder retirement cannot invent a different first attempt'); END;
CREATE TRIGGER cleardev_builder_session_fenced_update BEFORE UPDATE ON sessions WHEN EXISTS(SELECT 1 FROM cleardev_builder_session_fences WHERE ao_session_id=OLD.id) AND (OLD.id IS NOT NEW.id OR OLD.project_id IS NOT NEW.project_id OR OLD.workspace_path IS NOT NEW.workspace_path OR OLD.branch IS NOT NEW.branch OR OLD.diff_base_sha IS NOT NEW.diff_base_sha OR OLD.kind IS NOT NEW.kind OR OLD.harness IS NOT NEW.harness OR OLD.session_mode IS NOT NEW.session_mode OR OLD.permission_mode IS NOT NEW.permission_mode OR OLD.creation_idempotency_key IS NOT NEW.creation_idempotency_key OR OLD.creation_request_fingerprint IS NOT NEW.creation_request_fingerprint OR OLD.agent_session_id IS NOT NEW.agent_session_id OR OLD.provider_conversation_id IS NOT NEW.provider_conversation_id OR OLD.model IS NOT NEW.model OR (OLD.is_terminated=1 AND NEW.is_terminated<>1) OR (OLD.activity_state IS NOT NEW.activity_state AND NEW.activity_state IN ('active','waiting_input')) OR (OLD.runtime_handle_id IS NOT NEW.runtime_handle_id AND NEW.runtime_handle_id<>'') OR (OLD.runtime_launch_id IS NOT NEW.runtime_launch_id AND NEW.runtime_launch_id<>'')) BEGIN SELECT RAISE(ABORT,'old Builder identity remains fenced'); END;
CREATE TRIGGER cleardev_builder_fenced_session_no_replace BEFORE INSERT ON sessions WHEN EXISTS(SELECT 1 FROM sessions old JOIN cleardev_builder_session_fences f ON f.ao_session_id=old.id WHERE old.rowid=NEW.rowid OR old.id=NEW.id OR (old.project_id=NEW.project_id AND old.num=NEW.num) OR (old.creation_idempotency_key<>'' AND old.creation_idempotency_key=NEW.creation_idempotency_key)) BEGIN SELECT RAISE(ABORT,'fenced Builder session identity is immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_attempt_no_replace BEFORE INSERT ON cleardev_agent_step_attempts WHEN EXISTS(SELECT 1 FROM cleardev_agent_step_attempts old JOIN cleardev_builder_replacement_handoffs h ON h.logical_step_id=old.logical_step_id WHERE old.rowid=NEW.rowid OR old.id=NEW.id OR (old.logical_step_id=NEW.logical_step_id AND old.attempt_number=NEW.attempt_number) OR old.client_message_id=NEW.client_message_id) BEGIN SELECT RAISE(ABORT,'Builder handoff attempts are immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_step_no_replace BEFORE INSERT ON cleardev_complex_execution_agent_steps WHEN EXISTS(SELECT 1 FROM cleardev_complex_execution_agent_steps old JOIN cleardev_builder_replacement_handoffs h ON h.logical_step_id=old.id WHERE old.rowid=NEW.rowid OR old.id=NEW.id OR old.client_message_id=NEW.client_message_id OR (old.role_binding_id=NEW.role_binding_id AND old.step_kind=NEW.step_kind AND old.request_id=NEW.request_id)) BEGIN SELECT RAISE(ABORT,'Builder handoff logical step identity is immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_dispatch_no_replace BEFORE INSERT ON cleardev_complex_execution_task_attempts WHEN EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts old JOIN cleardev_builder_replacement_handoffs h ON h.logical_step_id=old.agent_step_id WHERE old.rowid=NEW.rowid OR old.id=NEW.id OR old.agent_step_id=NEW.agent_step_id OR (old.task_mapping_id=NEW.task_mapping_id AND old.round=NEW.round)) BEGIN SELECT RAISE(ABORT,'Builder handoff dispatch identity is immutable'); END;
CREATE TRIGGER cleardev_builder_replacement_role_no_replace BEFORE INSERT ON cleardev_complex_execution_role_bindings WHEN EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings old JOIN cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id WHERE (old.id=h.new_role_binding_id OR old.id=json_extract(r.binding_json,'$.oldRoleBindingId')) AND (old.rowid=NEW.rowid OR old.id=NEW.id OR old.session_creation_idempotency_key=NEW.session_creation_idempotency_key OR (old.execution_run_id=NEW.execution_run_id AND old.ao_session_id IS NOT NULL AND old.ao_session_id=NEW.ao_session_id) OR (old.execution_run_id=NEW.execution_run_id AND old.role='BUILDER' AND NEW.role='BUILDER' AND old.builder_slot=NEW.builder_slot AND old.status IN ('REQUESTED','BOUND') AND NEW.status IN ('REQUESTED','BOUND')))) BEGIN SELECT RAISE(ABORT,'Builder handoff role identity is immutable'); END;
CREATE TEMP TABLE cleardev_builder_handoff_fk_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_builder_handoff_fk_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM pragma_foreign_key_check) THEN 0 ELSE 1 END;
DROP TABLE cleardev_builder_handoff_fk_guard;
COMMIT;
-- +goose StatementEnd
PRAGMA foreign_keys=ON;
PRAGMA legacy_alter_table=OFF;
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_builder_handoff_down_guard(ok INTEGER CHECK(ok=1));
-- Refuse before changing this version when any older protected history would
-- reject a later Down step. A refused historical downgrade must stay at 0185.
INSERT INTO cleardev_builder_handoff_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_builder_replacement_requests)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_grants)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_aliases)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_observations)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_fences)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_operations)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_operation_ends)
 OR EXISTS(SELECT 1 FROM cleardev_builder_handoff_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE status='RETIRED_BEFORE_SEND')
 OR EXISTS(SELECT 1 FROM cleardev_planning_extra_requests)
 OR EXISTS(SELECT 1 FROM cleardev_planning_extra_grants)
 OR EXISTS(SELECT 1 FROM cleardev_planning_extra_continuations)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_planning_step_recoveries)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_reopens)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_checks)
 OR EXISTS(SELECT 1 FROM cleardev_planner_answers)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config,'{}'),'$.cleardev')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budget_occupancies)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_builder_handoff_down_guard;
-- +goose StatementEnd
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
-- +goose StatementBegin
BEGIN IMMEDIATE;
DROP TRIGGER cleardev_builder_fenced_session_no_replace;
DROP TRIGGER cleardev_builder_replacement_attempt_no_replace;
DROP TRIGGER cleardev_builder_replacement_step_no_replace;
DROP TRIGGER cleardev_builder_replacement_dispatch_no_replace;
DROP TRIGGER cleardev_builder_replacement_role_no_replace;
DROP TRIGGER cleardev_builder_replacement_role_valid;
DROP TRIGGER cleardev_builder_replacement_attempt_valid;
DROP TRIGGER cleardev_builder_replacement_first_valid;
DROP TRIGGER cleardev_builder_session_fenced_update;
DROP TRIGGER cleardev_builder_session_fenced_delete;
DROP TRIGGER cleardev_builder_message_fenced;
DROP TRIGGER cleardev_builder_retirement_valid;
DROP TRIGGER cleardev_builder_retirement_no_replace;
DROP TRIGGER cleardev_complex_execution_candidate_binding_valid;
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
DROP TABLE cleardev_builder_handoff_contexts;
DROP TABLE cleardev_builder_session_operation_ends;
DROP TABLE cleardev_builder_session_operations;
DROP TABLE cleardev_builder_session_fences;
DROP TABLE cleardev_builder_replacement_observations;
DROP TABLE cleardev_builder_replacement_aliases;
DROP TABLE cleardev_builder_replacement_handoffs;
DROP TABLE cleardev_builder_replacement_grants;
DROP TABLE cleardev_builder_replacement_requests;
ALTER TABLE cleardev_agent_attempt_events RENAME TO cleardev_agent_attempt_events_with_handoff;
DROP INDEX idx_cleardev_agent_attempt_events_attempt;
CREATE TABLE cleardev_agent_attempt_events (
    id                  TEXT PRIMARY KEY,
    attempt_id          TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
    status              TEXT NOT NULL CHECK (status IN (
        'SENT', 'CORRECTION_SENT', 'COMPLETED', 'FAILED', 'INTERRUPTED',
        'OBSERVATION_TIMEOUT', 'DELIVERY_UNKNOWN'
    )),
    client_message_id   TEXT NOT NULL DEFAULT '',
    prompt_sha256       TEXT NOT NULL DEFAULT '' CHECK (
        prompt_sha256 = '' OR (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*')
    ),
    turn_id             TEXT NOT NULL DEFAULT '',
    turn_state          TEXT NOT NULL DEFAULT '' CHECK (turn_state IN ('', 'queued', 'running', 'completed', 'interrupted', 'failed')),
    failure_category    TEXT NOT NULL DEFAULT '' CHECK (failure_category IN (
        '', 'MODEL_UNAVAILABLE', 'AUTHENTICATION_REQUIRED', 'QUOTA_EXHAUSTED',
        'RATE_LIMITED', 'PROVIDER_UNAVAILABLE', 'DRIVER_INCOMPATIBLE',
        'SESSION_LOST', 'TURN_INTERRUPTED', 'OBSERVATION_TIMEOUT',
        'DELIVERY_UNKNOWN', 'PROVIDER_FAILURE', 'RESULT_INVALID'
    )),
    retryable           INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1)),
    retry_at            TIMESTAMP,
    provider_error_code TEXT NOT NULL DEFAULT '',
    error_summary       TEXT NOT NULL DEFAULT '',
    recorded_at         TIMESTAMP NOT NULL,
    CHECK (
        (status IN ('SENT', 'CORRECTION_SENT', 'COMPLETED') AND failure_category = '')
        OR (status NOT IN ('SENT', 'CORRECTION_SENT', 'COMPLETED') AND failure_category <> '')
    )
);
CREATE INDEX idx_cleardev_agent_attempt_events_attempt
    ON cleardev_agent_attempt_events (attempt_id, recorded_at, id);


INSERT INTO cleardev_agent_attempt_events(rowid,id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at) SELECT rowid,id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at FROM cleardev_agent_attempt_events_with_handoff;
DROP TABLE cleardev_agent_attempt_events_with_handoff;
CREATE TRIGGER cleardev_agent_attempt_events_update_forbidden
BEFORE UPDATE ON cleardev_agent_attempt_events BEGIN
    SELECT RAISE(ABORT, 'cleardev agent attempt events are immutable');
END;
CREATE TRIGGER cleardev_agent_attempt_events_delete_forbidden
BEFORE DELETE ON cleardev_agent_attempt_events BEGIN
    SELECT RAISE(ABORT, 'cleardev agent attempt events are append-only');
END;
CREATE TRIGGER cleardev_agent_attempt_events_cdc_insert
AFTER INSERT ON cleardev_agent_attempt_events BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', attempt.development_project_id,
                       'logicalStepId', attempt.logical_step_id,
                       'agentAttemptId', attempt.id,
                       'attemptStatus', NEW.status),
           NEW.recorded_at
    FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_development_projects AS project ON project.id = attempt.development_project_id
    WHERE attempt.id = NEW.attempt_id;
END;
CREATE TRIGGER cleardev_complex_execution_candidate_binding_valid
BEFORE INSERT ON cleardev_candidate_commits
WHEN (NEW.complex_execution_task_attempt_id IS NULL) <> (NEW.complex_execution_base_commit_sha IS NULL)
 OR (NEW.complex_execution_task_attempt_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
    WHERE attempt.id = NEW.complex_execution_task_attempt_id AND attempt.base_commit_sha = NEW.complex_execution_base_commit_sha
      AND task.work_item_id = NEW.work_item_id AND work_item.complex_execution_task_id = task.id
      AND builder.status = 'BOUND' AND builder.ao_session_id = NEW.ao_session_id
      AND NEW.dispatch_id IS NULL AND NEW.base_commit_sha IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution candidate must use its Builder, task, and fixed base only');
END;

CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
     AND NOT(OLD.status IN('BLOCKED','NEEDS_HUMAN') AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action IN('RETRY_REVIEW','RETRY_CHECK') AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action='RETRY_BUILDER_SESSION' AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code='BUILDER_BUDGET_EXHAUSTED' AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at))
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
COMMIT;
-- +goose StatementEnd
PRAGMA foreign_keys=ON;
PRAGMA legacy_alter_table=OFF;
