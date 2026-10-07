-- Runtime coordination is opt-in per execution. Original plans, task mappings,
-- permissions, attempts, candidates, checks and reviews remain immutable.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_planner_runtime_events (
    id TEXT PRIMARY KEY NOT NULL,
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    dispatch_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
    source_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
    source_message_sha256 TEXT NOT NULL CHECK(length(source_message_sha256)=64 AND source_message_sha256 NOT GLOB '*[^0-9a-f]*'),
    report_json TEXT NOT NULL CHECK(json_valid(report_json) AND json_type(report_json)='object'),
    created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;

CREATE TABLE cleardev_planner_runtime_requests (
    event_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_planner_runtime_events(id),
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 1 AND 2),
    agent_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id) DEFERRABLE INITIALLY DEFERRED,
    planner_role_binding_id TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    ao_session_id TEXT NOT NULL REFERENCES sessions(id),
    context_json TEXT NOT NULL CHECK(json_valid(context_json) AND json_type(context_json)='object'),
    context_sha256 TEXT NOT NULL CHECK(length(context_sha256)=64 AND context_sha256 NOT GLOB '*[^0-9a-f]*'),
    prompt TEXT NOT NULL CHECK(length(trim(prompt))>0),
    created_at TIMESTAMP NOT NULL,
    UNIQUE(execution_run_id,ordinal)
) WITHOUT ROWID;

CREATE TABLE cleardev_planner_runtime_decisions (
    event_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_planner_runtime_events(id),
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

CREATE TABLE cleardev_planner_runtime_task_amendments (
    id TEXT PRIMARY KEY NOT NULL,
    event_id TEXT NOT NULL REFERENCES cleardev_planner_runtime_decisions(event_id),
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    task_mapping_id TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    ordinal INTEGER NOT NULL CHECK(ordinal BETWEEN 1 AND 2),
    previous_package_sha256 TEXT NOT NULL CHECK(length(previous_package_sha256)=64 AND previous_package_sha256 NOT GLOB '*[^0-9a-f]*'),
    task_packet_json TEXT NOT NULL CHECK(json_valid(task_packet_json) AND json_type(task_packet_json)='object'),
    task_packet_sha256 TEXT NOT NULL CHECK(length(task_packet_sha256)=64 AND task_packet_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at TIMESTAMP NOT NULL,
    UNIQUE(event_id,task_mapping_id),
    UNIQUE(task_mapping_id,ordinal)
) WITHOUT ROWID;

-- The effective packet is selected without UPDATE/DELETE of the original
-- task mapping. An amendment is only admitted before any attempt exists.
CREATE VIEW cleardev_planner_runtime_effective_tasks AS
SELECT task.id,task.execution_run_id,task.plan_task_key,task.work_item_id,task.ordinal,
       COALESCE(amend.task_packet_json,task.task_packet_json) AS task_packet_json,
       COALESCE(amend.task_packet_sha256,task.task_packet_sha256) AS task_packet_sha256
FROM cleardev_complex_execution_task_mappings task
LEFT JOIN cleardev_planner_runtime_task_amendments amend ON amend.task_mapping_id=task.id
 AND amend.ordinal=(SELECT max(prior.ordinal) FROM cleardev_planner_runtime_task_amendments prior WHERE prior.task_mapping_id=task.id);

-- Incomplete application is a barrier too. A transaction either appends the
-- decision and all of its amendments or exposes no executable decision.
CREATE VIEW cleardev_planner_runtime_barriers AS
SELECT event.id AS event_id,event.execution_run_id,
       COALESCE(decision.reason_code,'PLANNER_COORDINATION_PENDING') AS reason_code
FROM cleardev_planner_runtime_events event
LEFT JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=event.id
WHERE decision.event_id IS NULL OR decision.outcome NOT IN('CONTINUE','AMEND_REMAINING')
 OR (decision.outcome='AMEND_REMAINING' AND
     (SELECT count(*) FROM cleardev_planner_runtime_task_amendments amendment WHERE amendment.event_id=event.id)
       <>json_array_length(decision.result_json,'$.amendments'));

CREATE TRIGGER cleardev_planner_runtime_policy_insert
BEFORE INSERT ON cleardev_complex_execution_runs
WHEN (json_extract(NEW.execution_package_json,'$.plannerCoordinationPolicy') IS NOT NULL
   OR json_extract(NEW.execution_package_json,'$.plannerCoordinationMaxRounds') IS NOT NULL
   OR json_extract(NEW.execution_package_json,'$.plannerCoordinationMaxRevisions') IS NOT NULL)
 AND (COALESCE(json_extract(NEW.execution_package_json,'$.plannerCoordinationPolicy'),'')<>'PLANNER_RUNTIME_COORDINATION_V1'
   OR COALESCE(json_extract(NEW.execution_package_json,'$.plannerCoordinationMaxRounds'),0)<>2
   OR COALESCE(json_extract(NEW.execution_package_json,'$.plannerCoordinationMaxRevisions'),0)<>2
   OR COALESCE(json_extract(NEW.execution_package_json,'$.planValidationPolicy'),'')<>'PLANNER_TASK_CONTRACT_V1'
   OR COALESCE(json_extract(NEW.execution_package_json,'$.finalReviewPolicy'),'')<>'REQUIREMENT_FINAL_REVIEW_V1')
BEGIN SELECT RAISE(ABORT,'runtime coordination requires frozen admission, final review and bounded policy'); END;

CREATE TRIGGER cleardev_planner_runtime_event_insert
BEFORE INSERT ON cleardev_planner_runtime_events
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_events old
            WHERE old.id=NEW.id OR old.dispatch_id=NEW.dispatch_id OR old.source_step_id=NEW.source_step_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
    JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
    JOIN cleardev_complex_execution_agent_steps step ON step.id=attempt.agent_step_id
    WHERE attempt.id=NEW.dispatch_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1'
      AND step.id=NEW.source_step_id AND step.step_kind='BUILDER_TASK' AND step.send_status='SETTLED'
      AND step.request_id=attempt.id AND step.role_binding_id=attempt.builder_role_binding_id
      AND step.message_sha256=NEW.source_message_sha256 AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='BUILDER_RESULT'
      AND json_type(step.final_message_text,'$.coordination')='object'
      AND json_extract(NEW.report_json,'$.category') IN('ENGINEERING','PRODUCT')
      AND json_extract(NEW.report_json,'$.category')=json_extract(step.final_message_text,'$.coordination.category')
      AND length(trim(COALESCE(json_extract(NEW.report_json,'$.summary'),'')))>0
      AND json_array_length(NEW.report_json,'$.evidence') BETWEEN 1 AND 6
      AND json_array_length(NEW.report_json,'$.affectedTaskKeys') BETWEEN 1 AND 3
      AND NOT EXISTS(SELECT 1 FROM json_each(NEW.report_json,'$.affectedTaskKeys') affected
                     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                                      WHERE task.execution_run_id=run.id AND task.plan_task_key=affected.value))
 )
BEGIN SELECT RAISE(ABORT,'runtime event requires the exact settled Builder report and cannot replace history'); END;
CREATE TRIGGER cleardev_planner_runtime_event_update BEFORE UPDATE ON cleardev_planner_runtime_events
BEGIN SELECT RAISE(ABORT,'runtime event is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_event_delete BEFORE DELETE ON cleardev_planner_runtime_events
BEGIN SELECT RAISE(ABORT,'runtime event is append-only'); END;

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
      AND json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1'
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND version.task_set_version=run.accepted_task_set_version AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND planner.id=NEW.planner_role_binding_id AND planner.role='ENGINEERING_PLANNER'
      AND planner.status='BOUND' AND planner.ao_session_id=NEW.ao_session_id
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id)
 )
BEGIN SELECT RAISE(ABORT,'runtime request requires quiescent current facts, the original Planner and remaining durable quota'); END;
CREATE TRIGGER cleardev_planner_runtime_request_update BEFORE UPDATE ON cleardev_planner_runtime_requests
BEGIN SELECT RAISE(ABORT,'runtime request and quota reservation are immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_request_delete BEFORE DELETE ON cleardev_planner_runtime_requests
BEGIN SELECT RAISE(ABORT,'runtime request quota is append-only'); END;

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
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id)
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 2
     OR (SELECT count(*) FROM cleardev_planner_runtime_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
CREATE TRIGGER cleardev_planner_runtime_decision_update BEFORE UPDATE ON cleardev_planner_runtime_decisions
BEGIN SELECT RAISE(ABORT,'runtime decision is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_decision_delete BEFORE DELETE ON cleardev_planner_runtime_decisions
BEGIN SELECT RAISE(ABORT,'runtime decision is append-only'); END;

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
      AND item.state='PLANNED' AND item.rework_count=0
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=task.id)
      AND task.task_packet_sha256=NEW.previous_package_sha256 AND NEW.task_packet_sha256<>NEW.previous_package_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.eventId')=NEW.event_id
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.decisionSha256')=decision.result_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.previousPackageSha256')=NEW.previous_package_sha256
      AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision')
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria') BETWEEN 1 AND 12
      AND json_array_length(amendment.value,'$.additionalReviewCriteria') BETWEEN 1 AND 6
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria')=json_array_length(task.task_packet_json,'$.reviewCriteria')+json_array_length(amendment.value,'$.additionalReviewCriteria')
      AND NOT EXISTS(SELECT 1 FROM json_each(task.task_packet_json,'$.reviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||criterion.key||']') IS NOT criterion.value)
      AND NOT EXISTS(SELECT 1 FROM json_each(amendment.value,'$.additionalReviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||(json_array_length(task.task_packet_json,'$.reviewCriteria')+criterion.key)||']') IS NOT criterion.value)
 )
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;
CREATE TRIGGER cleardev_planner_runtime_amendment_update BEFORE UPDATE ON cleardev_planner_runtime_task_amendments
BEGIN SELECT RAISE(ABORT,'runtime task amendment is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_amendment_delete BEFORE DELETE ON cleardev_planner_runtime_task_amendments
BEGIN SELECT RAISE(ABORT,'runtime task amendment is append-only'); END;

-- Existing work may reach its normal frozen/check/review boundary. No new
-- dispatch, composition or final completion can cross an unresolved event.
CREATE TRIGGER cleardev_planner_runtime_dispatch_guard BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_barriers WHERE execution_run_id=NEW.execution_run_id)
BEGIN SELECT RAISE(ABORT,'runtime coordination must resolve before another dispatch'); END;
CREATE TRIGGER cleardev_planner_runtime_composition_guard BEFORE INSERT ON cleardev_complex_execution_compositions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_barriers WHERE execution_run_id=NEW.execution_run_id)
BEGIN SELECT RAISE(ABORT,'runtime coordination must resolve before composition'); END;
CREATE TRIGGER cleardev_planner_runtime_final_review_guard BEFORE INSERT ON cleardev_requirement_final_reviews
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_barriers WHERE execution_run_id=NEW.execution_run_id)
BEGIN SELECT RAISE(ABORT,'runtime coordination must resolve before final review'); END;
CREATE TRIGGER cleardev_planner_runtime_result_guard BEFORE INSERT ON cleardev_complex_execution_results
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_barriers WHERE execution_run_id=NEW.execution_run_id)
BEGIN SELECT RAISE(ABORT,'runtime coordination cannot bypass protected completion'); END;
CREATE TRIGGER cleardev_planner_runtime_completion_guard BEFORE UPDATE ON cleardev_complex_execution_runs
WHEN NEW.status='COMPLETED' AND EXISTS(SELECT 1 FROM cleardev_planner_runtime_barriers WHERE execution_run_id=NEW.id)
BEGIN SELECT RAISE(ABORT,'unresolved runtime coordination forbids completion'); END;

CREATE TRIGGER cleardev_planner_runtime_event_cdc AFTER INSERT ON cleardev_planner_runtime_events
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'plannerRuntimeEventId',NEW.id),NEW.created_at
    FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
CREATE TRIGGER cleardev_planner_runtime_request_cdc AFTER INSERT ON cleardev_planner_runtime_requests
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'plannerRuntimeRequestId',NEW.event_id),NEW.created_at
    FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
CREATE TRIGGER cleardev_planner_runtime_decision_cdc AFTER INSERT ON cleardev_planner_runtime_decisions
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'plannerRuntimeDecisionId',NEW.event_id,'outcome',NEW.outcome),NEW.created_at
    FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
CREATE TRIGGER cleardev_planner_runtime_amendment_cdc AFTER INSERT ON cleardev_planner_runtime_task_amendments
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'plannerRuntimeAmendmentId',NEW.id,'taskId',NEW.task_mapping_id),NEW.created_at
    FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
-- +goose StatementEnd

-- Removing this policy while any opted-in run exists would let old software
-- ignore an unresolved event. Refuse instead of silently downgrading authority.
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_planner_runtime_down_guard (allowed INTEGER CHECK(allowed=1));
-- Refuse at the current version, before dropping any schema, for the same
-- historical facts protected by 0148 as well as new runtime history. A failed
-- multi-migration downgrade must not partially move an old run back to 0148.
INSERT INTO cleardev_planner_runtime_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion')=2)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) THEN 0 ELSE 1 END;
DROP TABLE cleardev_planner_runtime_down_guard;
DROP TRIGGER cleardev_planner_runtime_completion_guard;
DROP TRIGGER cleardev_planner_runtime_result_guard;
DROP TRIGGER cleardev_planner_runtime_final_review_guard;
DROP TRIGGER cleardev_planner_runtime_composition_guard;
DROP TRIGGER cleardev_planner_runtime_dispatch_guard;
DROP TRIGGER cleardev_planner_runtime_policy_insert;
DROP VIEW cleardev_planner_runtime_barriers;
DROP VIEW cleardev_planner_runtime_effective_tasks;
DROP TABLE cleardev_planner_runtime_task_amendments;
DROP TABLE cleardev_planner_runtime_decisions;
DROP TABLE cleardev_planner_runtime_requests;
DROP TABLE cleardev_planner_runtime_events;
-- +goose StatementEnd
