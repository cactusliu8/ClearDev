-- +goose Up
-- +goose NO TRANSACTION
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
BEGIN IMMEDIATE;
CREATE TEMP TABLE coordination_fk_before AS SELECT "table",rowid,parent,fkid FROM pragma_foreign_key_check;
CREATE TABLE cleardev_planner_runtime_events_new (
    id TEXT PRIMARY KEY NOT NULL,
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    dispatch_id TEXT NOT NULL REFERENCES cleardev_complex_execution_task_attempts(id),
    source_step_id TEXT NOT NULL UNIQUE,
    source_message_sha256 TEXT NOT NULL CHECK(length(source_message_sha256)=64 AND source_message_sha256 NOT GLOB '*[^0-9a-f]*'),
    report_json TEXT NOT NULL CHECK(json_valid(report_json) AND json_type(report_json)='object'),
    created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
INSERT INTO cleardev_planner_runtime_events_new SELECT * FROM cleardev_planner_runtime_events;
DROP TABLE cleardev_planner_runtime_events;
ALTER TABLE cleardev_planner_runtime_events_new RENAME TO cleardev_planner_runtime_events;
CREATE TRIGGER cleardev_planner_runtime_event_insert
BEFORE INSERT ON cleardev_planner_runtime_events
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_events old
            WHERE old.id=NEW.id OR old.source_step_id=NEW.source_step_id)
 OR (NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
    JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
    JOIN cleardev_complex_execution_agent_steps step ON step.id=attempt.agent_step_id
    WHERE attempt.id=NEW.dispatch_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND (json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1' OR json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1')
      AND step.id=NEW.source_step_id AND step.step_kind='BUILDER_TASK' AND step.send_status='SETTLED'
      AND step.request_id=attempt.id AND step.role_binding_id=attempt.builder_role_binding_id
      AND step.message_sha256=NEW.source_message_sha256 AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='BUILDER_RESULT'
      AND (json_type(step.final_message_text,'$.coordination')='object' OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND json_extract(step.final_message_text,'$.outcome')='BLOCKED'))
      AND json_extract(NEW.report_json,'$.category') IN('ENGINEERING','PRODUCT')
      AND (json_extract(NEW.report_json,'$.category')=json_extract(step.final_message_text,'$.coordination.category') OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews fr WHERE fr.execution_run_id=run.id AND fr.status='SETTLED' AND fr.verdict IN('BLOCKED','REWORK')) AND json_type(step.final_message_text,'$.coordination') IS NULL AND json_extract(step.final_message_text,'$.outcome')='BLOCKED' AND json_extract(NEW.report_json,'$.category')='ENGINEERING' AND json_extract(NEW.report_json,'$.evidence[0]')=json_extract(step.final_message_text,'$.summary')))
      AND length(trim(COALESCE(json_extract(NEW.report_json,'$.summary'),'')))>0
      AND json_array_length(NEW.report_json,'$.evidence') BETWEEN 1 AND 6
      AND json_array_length(NEW.report_json,'$.affectedTaskKeys') BETWEEN 1 AND 3
      AND NOT EXISTS(SELECT 1 FROM json_each(NEW.report_json,'$.affectedTaskKeys') affected
                     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                                      WHERE task.execution_run_id=run.id AND task.plan_task_key=affected.value))
 )
 AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_reviews review
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_complex_execution_agent_steps step ON step.id=NEW.source_step_id
 WHERE attempt.id=NEW.dispatch_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND review.status='SETTLED' AND review.verdict='BLOCKED'
 AND step.id=NEW.source_step_id AND step.send_status='SETTLED'
 AND step.role_binding_id=review.reviewer_role_binding_id
 AND ((step.id=review.agent_step_id AND step.request_id=review.id
       AND NOT EXISTS(SELECT 1 FROM cleardev_review_check_requests checks WHERE checks.review_id=review.id))
   OR (step.id=review.id||':check-results' AND step.request_id=step.id
       AND EXISTS(SELECT 1 FROM cleardev_review_check_requests checks WHERE checks.review_id=review.id)))
 AND step.message_sha256=NEW.source_message_sha256
 AND json_extract(step.final_message_text,'$.coordination')=NEW.report_json
 )
 AND NOT EXISTS(
 SELECT 1 FROM cleardev_requirement_final_reviews review
 JOIN cleardev_agent_step_results result ON result.id=review.result_id
 JOIN cleardev_complex_execution_runs run ON run.id=review.execution_run_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.execution_run_id=run.id
 JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=attempt.id
 WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED'
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND review.status='SETTLED' AND review.verdict='BLOCKED'
 AND review.id||':step'=NEW.source_step_id AND result.raw_message_sha256=NEW.source_message_sha256
 AND attempt.id=NEW.dispatch_id AND candidate.commit_sha=review.candidate_commit_sha
 AND json_extract(result.raw_message_text,'$.coordination')=NEW.report_json
 ))
 OR json_extract(NEW.report_json,'$.category') NOT IN('ENGINEERING','PRODUCT')
 OR json_array_length(NEW.report_json,'$.affectedTaskKeys') NOT BETWEEN 1 AND 3
 OR EXISTS(SELECT 1 FROM json_each(NEW.report_json,'$.affectedTaskKeys') key
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
 WHERE task.execution_run_id=NEW.execution_run_id AND task.plan_task_key=key.value))

BEGIN SELECT RAISE(ABORT,'runtime event requires the exact settled Builder report and cannot replace history'); END;
CREATE TRIGGER cleardev_planner_runtime_event_update BEFORE UPDATE ON cleardev_planner_runtime_events
BEGIN SELECT RAISE(ABORT,'runtime event is immutable'); END;
CREATE TRIGGER cleardev_planner_runtime_event_delete BEFORE DELETE ON cleardev_planner_runtime_events
BEGIN SELECT RAISE(ABORT,'runtime event is append-only'); END;
CREATE TRIGGER cleardev_planner_runtime_event_cdc AFTER INSERT ON cleardev_planner_runtime_events
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'plannerRuntimeEventId',NEW.id),NEW.created_at
    FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
CREATE TEMP TABLE coordination_fk_guard (valid INTEGER CHECK(valid=1));
INSERT INTO coordination_fk_guard SELECT 0 WHERE EXISTS(
 SELECT "table",rowid,parent,fkid FROM pragma_foreign_key_check EXCEPT SELECT * FROM coordination_fk_before);
DROP TABLE coordination_fk_guard;
DROP TABLE coordination_fk_before;
COMMIT;
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
BEGIN IMMEDIATE;
CREATE TEMP TABLE coordination_downgrade_guard (valid INTEGER CHECK(valid=1));
INSERT INTO coordination_downgrade_guard SELECT 0 WHERE EXISTS(
 SELECT 1 FROM cleardev_planner_runtime_events event
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.id=event.dispatch_id AND a.agent_step_id=event.source_step_id));
DROP TABLE coordination_downgrade_guard;
CREATE TABLE cleardev_planner_runtime_events_new (
    id TEXT PRIMARY KEY NOT NULL,
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    dispatch_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
    source_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
    source_message_sha256 TEXT NOT NULL CHECK(length(source_message_sha256)=64 AND source_message_sha256 NOT GLOB '*[^0-9a-f]*'),
    report_json TEXT NOT NULL CHECK(json_valid(report_json) AND json_type(report_json)='object'),
    created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
INSERT INTO cleardev_planner_runtime_events_new SELECT * FROM cleardev_planner_runtime_events;
DROP TABLE cleardev_planner_runtime_events;
ALTER TABLE cleardev_planner_runtime_events_new RENAME TO cleardev_planner_runtime_events;
CREATE TRIGGER cleardev_planner_runtime_event_insert
BEFORE INSERT ON cleardev_planner_runtime_events
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_events old
            WHERE old.id=NEW.id OR old.dispatch_id=NEW.dispatch_id OR old.source_step_id=NEW.source_step_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
    JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
    JOIN cleardev_complex_execution_agent_steps step ON step.id=attempt.agent_step_id
    WHERE attempt.id=NEW.dispatch_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND (json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1' OR json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1')
      AND step.id=NEW.source_step_id AND step.step_kind='BUILDER_TASK' AND step.send_status='SETTLED'
      AND step.request_id=attempt.id AND step.role_binding_id=attempt.builder_role_binding_id
      AND step.message_sha256=NEW.source_message_sha256 AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='BUILDER_RESULT'
      AND (json_type(step.final_message_text,'$.coordination')='object' OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND json_extract(step.final_message_text,'$.outcome')='BLOCKED'))
      AND json_extract(NEW.report_json,'$.category') IN('ENGINEERING','PRODUCT')
      AND (json_extract(NEW.report_json,'$.category')=json_extract(step.final_message_text,'$.coordination.category') OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews fr WHERE fr.execution_run_id=run.id AND fr.status='SETTLED' AND fr.verdict IN('BLOCKED','REWORK')) AND json_type(step.final_message_text,'$.coordination') IS NULL AND json_extract(step.final_message_text,'$.outcome')='BLOCKED' AND json_extract(NEW.report_json,'$.category')='ENGINEERING' AND json_extract(NEW.report_json,'$.evidence[0]')=json_extract(step.final_message_text,'$.summary')))
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
CREATE TRIGGER cleardev_planner_runtime_event_cdc AFTER INSERT ON cleardev_planner_runtime_events
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'plannerRuntimeEventId',NEW.id),NEW.created_at
    FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
COMMIT;
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd
