-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_planner_continue_reapplications (
 event_id TEXT PRIMARY KEY REFERENCES cleardev_planner_runtime_events(id),
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 source TEXT NOT NULL CHECK(source='PLANNER'), outcome TEXT NOT NULL CHECK(outcome='CONTINUE'),
 reason_code TEXT NOT NULL CHECK(reason_code=''), result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64), summary TEXT NOT NULL, created_at TIMESTAMP NOT NULL
);
DROP VIEW cleardev_planner_runtime_current_decisions;
CREATE VIEW cleardev_planner_runtime_prior_decisions AS
 SELECT * FROM cleardev_planner_runtime_decisions original
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=original.event_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=original.event_id)
 UNION ALL SELECT * FROM cleardev_planner_runtime_recovery_decisions
 UNION ALL SELECT * FROM cleardev_extra_coordination_decisions;
CREATE VIEW cleardev_planner_runtime_current_decisions AS
 SELECT prior.* FROM cleardev_planner_runtime_prior_decisions prior
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_continue_reapplications fixed WHERE fixed.event_id=prior.event_id)
 UNION ALL SELECT * FROM cleardev_planner_continue_reapplications;
CREATE TRIGGER cleardev_planner_continue_reapplication_insert BEFORE INSERT ON cleardev_planner_continue_reapplications
WHEN EXISTS(SELECT 1 FROM cleardev_planner_continue_reapplications WHERE event_id=NEW.event_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_prior_decisions prior
 JOIN cleardev_planner_runtime_requests request ON request.event_id=prior.event_id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 JOIN cleardev_complex_execution_runs run ON run.id=prior.execution_run_id
 WHERE prior.event_id=NEW.event_id AND prior.execution_run_id=NEW.execution_run_id
 AND prior.source='CONTROL_PLANE' AND prior.outcome='STOP' AND prior.reason_code='PLANNER_COORDINATION_STOPPED'
 AND prior.summary='Planner coordination cannot reopen an in-flight or stopped task or consume an unapproved recovery attempt. The proposed decision was not applied.'
 AND prior.result_json=NEW.result_json AND prior.result_sha256=NEW.result_sha256
 AND json_extract(NEW.result_json,'$.decision')='CONTINUE'
 AND json_extract(NEW.result_json,'$.summary')=NEW.summary
 AND json_extract(NEW.result_json,'$.contextSha256')=request.context_sha256
 AND json_extract(step.final_message_text,'$.decision')='CONTINUE'
 AND step.send_status='SETTLED' AND step.request_id=NEW.event_id AND step.role_binding_id=request.planner_role_binding_id
 AND run.status='ACCEPTED' AND run.settled_at IS NULL
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts attempt WHERE attempt.execution_run_id=run.id AND attempt.status IN('PENDING','RUNNING','OBSERVED','REVIEWING')))
BEGIN SELECT RAISE(ABORT,'continue reapplication requires the exact rejected settled Planner decision'); END;
CREATE TRIGGER cleardev_planner_continue_reapplication_update BEFORE UPDATE ON cleardev_planner_continue_reapplications
BEGIN SELECT RAISE(ABORT,'continue reapplication is immutable'); END;
CREATE TRIGGER cleardev_planner_continue_reapplication_delete BEFORE DELETE ON cleardev_planner_continue_reapplications
BEGIN SELECT RAISE(ABORT,'continue reapplication is immutable'); END;
CREATE TRIGGER cleardev_planner_continue_reapplication_cdc AFTER INSERT ON cleardev_planner_continue_reapplications BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_continue_reapplication_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_continue_reapplication_down_guard SELECT NOT (
 EXISTS(SELECT 1 FROM cleardev_planner_continue_reapplications)
 OR EXISTS(SELECT 1 FROM cleardev_product_goals)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs));
DROP TABLE cleardev_continue_reapplication_down_guard;
DROP VIEW cleardev_planner_runtime_current_decisions;
DROP VIEW cleardev_planner_runtime_prior_decisions;
CREATE VIEW cleardev_planner_runtime_current_decisions AS
 SELECT * FROM cleardev_planner_runtime_decisions original
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries recovery WHERE recovery.event_id=original.event_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=original.event_id)
 UNION ALL SELECT * FROM cleardev_planner_runtime_recovery_decisions
 UNION ALL SELECT * FROM cleardev_extra_coordination_decisions;
DROP TABLE cleardev_planner_continue_reapplications;
-- +goose StatementEnd
