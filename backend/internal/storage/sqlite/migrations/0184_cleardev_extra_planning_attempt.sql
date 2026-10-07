-- One explicit native grant adds one original planning message without resetting history.
-- +goose Up
-- +goose NO TRANSACTION
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
-- +goose StatementBegin
BEGIN IMMEDIATE;
ALTER TABLE cleardev_agent_step_attempts RENAME TO cleardev_agent_step_attempts_extra_previous;
CREATE TABLE "cleardev_agent_step_attempts" (
    id                       TEXT PRIMARY KEY,
    development_project_id   TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    logical_step_id          TEXT NOT NULL CHECK (length(trim(logical_step_id)) > 0),
    step_category            TEXT NOT NULL CHECK (step_category IN (
        'STANDARD', 'COMPLEX_PLANNING', 'DIRECTION_CHANGE', 'COMPLEX_EXECUTION',
        'QUICK_EXECUTION', 'CONTROLLED_EXCEPTION', 'PROGRESS_EXPLANATION'
    )),
    step_kind                TEXT NOT NULL CHECK (length(trim(step_kind)) > 0),
    attempt_number           INTEGER NOT NULL CHECK (attempt_number IN (1, 2, 3)),
    role_binding_id          TEXT NOT NULL DEFAULT '',
    ao_session_id            TEXT NOT NULL CHECK (length(trim(ao_session_id)) > 0),
    client_message_id        TEXT NOT NULL CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256            TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    trigger_failure_event_id TEXT REFERENCES cleardev_agent_attempt_events(id),
    requested_at             TIMESTAMP NOT NULL, created_at DATETIME, requested_at_semantics TEXT NOT NULL DEFAULT 'LEGACY_STEP_REQUEST'
    CHECK (requested_at_semantics IN ('ACTUAL_CREATION', 'LEGACY_STEP_REQUEST', 'LEGACY_RETRY_BOUNDARY')),
    UNIQUE (logical_step_id, attempt_number),
    UNIQUE (client_message_id),
    CHECK (
        (attempt_number = 1 AND trigger_failure_event_id IS NULL)
        OR (attempt_number IN (2,3) AND length(trim(trigger_failure_event_id)) > 0)
    )
);
INSERT INTO cleardev_agent_step_attempts(rowid,id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,trigger_failure_event_id,requested_at,created_at,requested_at_semantics) SELECT rowid,id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,trigger_failure_event_id,requested_at,created_at,requested_at_semantics FROM cleardev_agent_step_attempts_extra_previous;
DROP TABLE cleardev_agent_step_attempts_extra_previous;
CREATE INDEX idx_cleardev_agent_step_attempts_requirement
    ON cleardev_agent_step_attempts (development_project_id, requested_at, id);
CREATE TRIGGER cleardev_agent_step_attempts_cdc_insert
AFTER INSERT ON cleardev_agent_step_attempts BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id,
                       'logicalStepId', NEW.logical_step_id,
                       'agentAttemptId', NEW.id),
           NEW.requested_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;
CREATE TRIGGER cleardev_agent_step_attempts_creation_required
BEFORE INSERT ON cleardev_agent_step_attempts
WHEN NEW.requested_at_semantics != 'ACTUAL_CREATION' OR NEW.created_at IS NULL OR NEW.created_at != NEW.requested_at
BEGIN
    SELECT RAISE(ABORT, 'new cleardev attempts require actual creation time');
END;
CREATE TRIGGER cleardev_agent_step_attempts_delete_forbidden
BEFORE DELETE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
CREATE TRIGGER cleardev_agent_step_attempts_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
ALTER TABLE cleardev_agent_message_reservations RENAME TO cleardev_agent_message_reservations_extra_previous;
CREATE TABLE cleardev_agent_message_reservations (
 client_message_id TEXT PRIMARY KEY,
 development_project_id TEXT NOT NULL REFERENCES cleardev_message_budget_versions(development_project_id),
 budget_version TEXT NOT NULL CHECK(budget_version='MESSAGE_BUDGET_V1'),
 logical_step_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
 source TEXT NOT NULL CHECK(source IN ('ORIGINAL','RECOVERY_ORIGINAL','PARSE_CORRECTION','HUMAN_AUTHORIZED_ORIGINAL')),
 ao_session_id TEXT NOT NULL,
 prompt_sha256 TEXT NOT NULL CHECK(length(prompt_sha256)=64),
 budget_id TEXT REFERENCES cleardev_complex_exception_budgets(id),
 reserved_at TIMESTAMP NOT NULL,
 UNIQUE(logical_step_id,source)
);
INSERT INTO cleardev_agent_message_reservations(rowid,client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at) SELECT rowid,client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at FROM cleardev_agent_message_reservations_extra_previous;
DROP TABLE cleardev_agent_message_reservations_extra_previous;
CREATE INDEX idx_cleardev_message_budget_requirement ON cleardev_agent_message_reservations(development_project_id);
CREATE INDEX idx_cleardev_message_budget_role ON cleardev_agent_message_reservations(budget_id);
CREATE TRIGGER cleardev_agent_message_reservations_cdc_insert AFTER INSERT ON cleardev_agent_message_reservations BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id),NEW.reserved_at
 FROM cleardev_development_projects AS project  WHERE project.id=NEW.development_project_id;
END;
CREATE TRIGGER cleardev_agent_message_reservations_delete_forbidden BEFORE DELETE ON cleardev_agent_message_reservations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
CREATE TRIGGER cleardev_agent_message_reservations_update_forbidden BEFORE UPDATE ON cleardev_agent_message_reservations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
CREATE TABLE cleardev_planning_extra_requests (
 id TEXT PRIMARY KEY NOT NULL,
 requirement_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
 logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
 decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id),
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json) AND json_type(binding_json)='object'),
 old_step_json TEXT NOT NULL CHECK(json_valid(old_step_json) AND json_type(old_step_json)='object'),
 original_status TEXT NOT NULL CHECK(original_status IN ('SENT','FAILED')),
 original_reason TEXT NOT NULL,
 original_summary TEXT NOT NULL,
 original_stopped_at TIMESTAMP NOT NULL,
 supplement TEXT NOT NULL CHECK(length(supplement)<=16000),
 created_at TIMESTAMP NOT NULL
);
CREATE TABLE cleardev_planning_extra_grants (
 decision_request_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_planning_extra_requests(decision_request_id),
 logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
 created_at TIMESTAMP NOT NULL
);
CREATE TABLE cleardev_planning_extra_continuations (
 id TEXT PRIMARY KEY NOT NULL,
 requirement_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
 logical_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
 decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id),
 third_attempt_id TEXT NOT NULL UNIQUE REFERENCES cleardev_agent_step_attempts(id) DEFERRABLE INITIALLY DEFERRED,
 old_step_json TEXT NOT NULL CHECK(json_valid(old_step_json) AND json_type(old_step_json)='object'),
 original_status TEXT NOT NULL CHECK(original_status IN ('SENT','FAILED')),
 original_reason TEXT NOT NULL,
 original_summary TEXT NOT NULL,
 original_stopped_at TIMESTAMP NOT NULL,
 supplement TEXT NOT NULL CHECK(length(supplement)<=16000),
 created_at TIMESTAMP NOT NULL
);
-- Reject every conflict identity before SQLite REPLACE can delete old facts.
-- UPDATE/DELETE guards alone do not protect ordinary recursive_triggers=0.
CREATE TRIGGER cleardev_planning_extra_requests_no_replace
BEFORE INSERT ON cleardev_planning_extra_requests
WHEN EXISTS(SELECT 1 FROM cleardev_planning_extra_requests old
 WHERE old.rowid=NEW.rowid OR old.id=NEW.id
 OR old.logical_step_id=NEW.logical_step_id OR old.decision_request_id=NEW.decision_request_id)
BEGIN SELECT RAISE(ABORT,'extra planning identities are immutable'); END;
CREATE TRIGGER cleardev_planning_extra_grants_no_replace
BEFORE INSERT ON cleardev_planning_extra_grants
WHEN EXISTS(SELECT 1 FROM cleardev_planning_extra_grants old
 WHERE old.rowid=NEW.rowid OR old.decision_request_id=NEW.decision_request_id
 OR old.logical_step_id=NEW.logical_step_id)
BEGIN SELECT RAISE(ABORT,'extra planning identities are immutable'); END;
CREATE TRIGGER cleardev_planning_extra_continuations_no_replace
BEFORE INSERT ON cleardev_planning_extra_continuations
WHEN EXISTS(SELECT 1 FROM cleardev_planning_extra_continuations old
 WHERE old.rowid=NEW.rowid OR old.id=NEW.id OR old.logical_step_id=NEW.logical_step_id
 OR old.decision_request_id=NEW.decision_request_id OR old.third_attempt_id=NEW.third_attempt_id)
BEGIN SELECT RAISE(ABORT,'extra planning identities are immutable'); END;
CREATE TRIGGER cleardev_planning_extra_requests_immutable BEFORE UPDATE ON cleardev_planning_extra_requests BEGIN SELECT RAISE(ABORT,'extra planning facts are immutable'); END;
CREATE TRIGGER cleardev_planning_extra_requests_keep_history BEFORE DELETE ON cleardev_planning_extra_requests BEGIN SELECT RAISE(ABORT,'extra planning history is immutable'); END;
CREATE TRIGGER cleardev_planning_extra_requests_cdc AFTER INSERT ON cleardev_planning_extra_requests BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'extraPlanningDecisionId',NEW.decision_request_id),NEW.created_at
 FROM cleardev_development_projects project JOIN cleardev_planning_extra_requests request ON request.requirement_id=project.id
 WHERE request.decision_request_id=NEW.decision_request_id;
END;
CREATE TRIGGER cleardev_planning_extra_grants_immutable BEFORE UPDATE ON cleardev_planning_extra_grants BEGIN SELECT RAISE(ABORT,'extra planning facts are immutable'); END;
CREATE TRIGGER cleardev_planning_extra_grants_keep_history BEFORE DELETE ON cleardev_planning_extra_grants BEGIN SELECT RAISE(ABORT,'extra planning history is immutable'); END;
CREATE TRIGGER cleardev_planning_extra_grants_cdc AFTER INSERT ON cleardev_planning_extra_grants BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'extraPlanningDecisionId',NEW.decision_request_id),NEW.created_at
 FROM cleardev_development_projects project JOIN cleardev_planning_extra_requests request ON request.requirement_id=project.id
 WHERE request.decision_request_id=NEW.decision_request_id;
END;
CREATE TRIGGER cleardev_planning_extra_continuations_immutable BEFORE UPDATE ON cleardev_planning_extra_continuations BEGIN SELECT RAISE(ABORT,'extra planning facts are immutable'); END;
CREATE TRIGGER cleardev_planning_extra_continuations_keep_history BEFORE DELETE ON cleardev_planning_extra_continuations BEGIN SELECT RAISE(ABORT,'extra planning history is immutable'); END;
CREATE TRIGGER cleardev_planning_extra_continuations_cdc AFTER INSERT ON cleardev_planning_extra_continuations BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'extraPlanningDecisionId',NEW.decision_request_id),NEW.created_at
 FROM cleardev_development_projects project JOIN cleardev_planning_extra_requests request ON request.requirement_id=project.id
 WHERE request.decision_request_id=NEW.decision_request_id;
END;
CREATE TRIGGER cleardev_planning_extra_grant_authorized BEFORE INSERT ON cleardev_planning_extra_grants
WHEN NOT EXISTS(SELECT 1 FROM cleardev_planning_extra_requests intent JOIN cleardev_human_decision_requests request ON request.id=intent.decision_request_id
 WHERE intent.decision_request_id=NEW.decision_request_id AND intent.logical_step_id=NEW.logical_step_id
 AND request.decision_kind='AUTHORIZE_EXTRA_PLANNING_ATTEMPT' AND request.status='RESOLVED' AND request.decision='APPROVE'
 AND request.binding_json=intent.binding_json)
BEGIN SELECT RAISE(ABORT,'extra planning grant requires exact resolved native approval'); END;
CREATE TRIGGER cleardev_agent_third_attempt_authorized BEFORE INSERT ON cleardev_agent_step_attempts
WHEN NEW.attempt_number=3 AND NOT EXISTS(
 SELECT 1 FROM cleardev_planning_extra_continuations continuation
 JOIN cleardev_planning_extra_requests intent ON intent.decision_request_id=continuation.decision_request_id
 JOIN cleardev_planning_extra_grants grant ON grant.decision_request_id=intent.decision_request_id AND grant.logical_step_id=intent.logical_step_id
 JOIN cleardev_agent_step_attempts second ON second.id=json_extract(intent.binding_json,'$.secondAttemptId')
 WHERE continuation.third_attempt_id=NEW.id AND NEW.id=continuation.logical_step_id||':attempt:3'
 AND NEW.logical_step_id=continuation.logical_step_id AND NEW.development_project_id=continuation.requirement_id
 AND NEW.step_category='COMPLEX_PLANNING' AND NEW.step_kind='REQUIREMENT_COMPILATION'
 AND NEW.role_binding_id=second.role_binding_id AND NEW.ao_session_id=second.ao_session_id
 AND NEW.client_message_id=json_extract(intent.binding_json,'$.source.clientMessageId')||':attempt:3'
 AND NEW.prompt_sha256=second.prompt_sha256 AND NEW.trigger_failure_event_id=json_extract(intent.binding_json,'$.failureEventId')
 AND second.logical_step_id=NEW.logical_step_id AND second.attempt_number=2
)
BEGIN SELECT RAISE(ABORT,'third planning attempt requires one exact native grant and continuation'); END;
CREATE TRIGGER cleardev_human_planning_message_authorized BEFORE INSERT ON cleardev_agent_message_reservations
WHEN NEW.source='HUMAN_AUTHORIZED_ORIGINAL' AND NOT EXISTS(
 SELECT 1 FROM cleardev_agent_step_attempts attempt JOIN cleardev_planning_extra_continuations continuation ON continuation.third_attempt_id=attempt.id
 JOIN cleardev_planning_extra_grants grant ON grant.decision_request_id=continuation.decision_request_id
 WHERE attempt.id=NEW.attempt_id AND attempt.attempt_number=3 AND attempt.logical_step_id=NEW.logical_step_id
 AND attempt.client_message_id=NEW.client_message_id AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
 AND attempt.development_project_id=NEW.development_project_id AND NEW.budget_id IS NULL
)
BEGIN SELECT RAISE(ABORT,'human-authorized original requires the exact third attempt'); END;
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
COMMIT;
-- +goose StatementEnd
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_extra_planning_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_extra_planning_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_planning_extra_requests)
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
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_extra_planning_down_guard;
-- +goose StatementEnd
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
-- +goose StatementBegin
BEGIN IMMEDIATE;
DROP TRIGGER cleardev_agent_third_attempt_authorized;
DROP TRIGGER cleardev_human_planning_message_authorized;
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
DROP TABLE cleardev_planning_extra_continuations;
DROP TABLE cleardev_planning_extra_grants;
DROP TABLE cleardev_planning_extra_requests;
ALTER TABLE cleardev_agent_message_reservations RENAME TO cleardev_agent_message_reservations_extra_previous;
CREATE TABLE cleardev_agent_message_reservations (
 client_message_id TEXT PRIMARY KEY,
 development_project_id TEXT NOT NULL REFERENCES cleardev_message_budget_versions(development_project_id),
 budget_version TEXT NOT NULL CHECK(budget_version='MESSAGE_BUDGET_V1'),
 logical_step_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
 source TEXT NOT NULL CHECK(source IN ('ORIGINAL','RECOVERY_ORIGINAL','PARSE_CORRECTION')),
 ao_session_id TEXT NOT NULL,
 prompt_sha256 TEXT NOT NULL CHECK(length(prompt_sha256)=64),
 budget_id TEXT REFERENCES cleardev_complex_exception_budgets(id),
 reserved_at TIMESTAMP NOT NULL,
 UNIQUE(logical_step_id,source)
);
INSERT INTO cleardev_agent_message_reservations(rowid,client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at) SELECT rowid,client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at FROM cleardev_agent_message_reservations_extra_previous;
DROP TABLE cleardev_agent_message_reservations_extra_previous;
CREATE INDEX idx_cleardev_message_budget_requirement ON cleardev_agent_message_reservations(development_project_id);
CREATE INDEX idx_cleardev_message_budget_role ON cleardev_agent_message_reservations(budget_id);
CREATE TRIGGER cleardev_agent_message_reservations_cdc_insert AFTER INSERT ON cleardev_agent_message_reservations BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id),NEW.reserved_at
 FROM cleardev_development_projects AS project  WHERE project.id=NEW.development_project_id;
END;
CREATE TRIGGER cleardev_agent_message_reservations_delete_forbidden BEFORE DELETE ON cleardev_agent_message_reservations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
CREATE TRIGGER cleardev_agent_message_reservations_update_forbidden BEFORE UPDATE ON cleardev_agent_message_reservations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
ALTER TABLE cleardev_agent_step_attempts RENAME TO cleardev_agent_step_attempts_extra_previous;
CREATE TABLE "cleardev_agent_step_attempts" (
    id                       TEXT PRIMARY KEY,
    development_project_id   TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    logical_step_id          TEXT NOT NULL CHECK (length(trim(logical_step_id)) > 0),
    step_category            TEXT NOT NULL CHECK (step_category IN (
        'STANDARD', 'COMPLEX_PLANNING', 'DIRECTION_CHANGE', 'COMPLEX_EXECUTION',
        'QUICK_EXECUTION', 'CONTROLLED_EXCEPTION', 'PROGRESS_EXPLANATION'
    )),
    step_kind                TEXT NOT NULL CHECK (length(trim(step_kind)) > 0),
    attempt_number           INTEGER NOT NULL CHECK (attempt_number IN (1, 2)),
    role_binding_id          TEXT NOT NULL DEFAULT '',
    ao_session_id            TEXT NOT NULL CHECK (length(trim(ao_session_id)) > 0),
    client_message_id        TEXT NOT NULL CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256            TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    trigger_failure_event_id TEXT REFERENCES cleardev_agent_attempt_events(id),
    requested_at             TIMESTAMP NOT NULL, created_at DATETIME, requested_at_semantics TEXT NOT NULL DEFAULT 'LEGACY_STEP_REQUEST'
    CHECK (requested_at_semantics IN ('ACTUAL_CREATION', 'LEGACY_STEP_REQUEST', 'LEGACY_RETRY_BOUNDARY')),
    UNIQUE (logical_step_id, attempt_number),
    UNIQUE (client_message_id),
    CHECK (
        (attempt_number = 1 AND trigger_failure_event_id IS NULL)
        OR (attempt_number = 2 AND length(trim(trigger_failure_event_id)) > 0)
    )
);
INSERT INTO cleardev_agent_step_attempts(rowid,id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,trigger_failure_event_id,requested_at,created_at,requested_at_semantics) SELECT rowid,id,development_project_id,logical_step_id,step_category,step_kind,attempt_number,role_binding_id,ao_session_id,client_message_id,prompt_sha256,trigger_failure_event_id,requested_at,created_at,requested_at_semantics FROM cleardev_agent_step_attempts_extra_previous;
DROP TABLE cleardev_agent_step_attempts_extra_previous;
CREATE INDEX idx_cleardev_agent_step_attempts_requirement
    ON cleardev_agent_step_attempts (development_project_id, requested_at, id);
CREATE TRIGGER cleardev_agent_step_attempts_cdc_insert
AFTER INSERT ON cleardev_agent_step_attempts BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id,
                       'logicalStepId', NEW.logical_step_id,
                       'agentAttemptId', NEW.id),
           NEW.requested_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;
CREATE TRIGGER cleardev_agent_step_attempts_creation_required
BEFORE INSERT ON cleardev_agent_step_attempts
WHEN NEW.requested_at_semantics != 'ACTUAL_CREATION' OR NEW.created_at IS NULL OR NEW.created_at != NEW.requested_at
BEGIN
    SELECT RAISE(ABORT, 'new cleardev attempts require actual creation time');
END;
CREATE TRIGGER cleardev_agent_step_attempts_delete_forbidden
BEFORE DELETE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
CREATE TRIGGER cleardev_agent_step_attempts_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
COMMIT;
-- +goose StatementEnd
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
