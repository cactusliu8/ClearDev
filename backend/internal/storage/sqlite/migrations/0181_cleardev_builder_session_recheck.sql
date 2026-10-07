-- An explicit original-Builder recheck is diagnostic, not a new development
-- attempt. Its immutable checkpoints never replace message admission.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_builder_session_checks (
 recovery_id TEXT NOT NULL REFERENCES cleardev_workflow_recoveries(id),
 checkpoint TEXT NOT NULL CHECK(checkpoint IN ('STARTED','FINISHED','BEFORE_SEND')),
 stage TEXT NOT NULL CHECK(stage IN ('RECHECK','SOURCE','DELIVERY','SESSION','WORKSPACE','PREFLIGHT','RESTORE','IDENTITY','READY')),
 outcome TEXT NOT NULL CHECK(outcome IN ('PENDING','READY','FAILED')),
 reason_code TEXT NOT NULL DEFAULT '',
 preflight_id TEXT NOT NULL DEFAULT '',
 binding_sha256 TEXT NOT NULL CHECK(length(binding_sha256)=64),
 checked_at TIMESTAMP NOT NULL,
 PRIMARY KEY(recovery_id,checkpoint),
 CHECK((checkpoint='STARTED' AND stage='RECHECK' AND outcome='PENDING' AND reason_code='')
    OR (checkpoint='FINISHED' AND ((outcome='READY' AND stage='READY' AND reason_code='') OR (outcome='FAILED' AND reason_code<>'')))
    OR (checkpoint='BEFORE_SEND' AND outcome='FAILED' AND reason_code<>''))
);
CREATE TRIGGER cleardev_builder_session_checks_immutable BEFORE UPDATE ON cleardev_builder_session_checks
BEGIN SELECT RAISE(ABORT,'Builder recheck evidence is immutable'); END;
CREATE TRIGGER cleardev_builder_session_checks_keep_history BEFORE DELETE ON cleardev_builder_session_checks
BEGIN SELECT RAISE(ABORT,'Builder recheck evidence must be retained'); END;
CREATE TRIGGER cleardev_builder_session_checks_bound BEFORE INSERT ON cleardev_builder_session_checks
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_workflow_recoveries recovery
 JOIN cleardev_complex_execution_task_attempts dispatch ON dispatch.id=recovery.dispatch_id
 JOIN cleardev_complex_execution_agent_steps step ON step.id=recovery.step_id
 WHERE recovery.id=NEW.recovery_id AND recovery.action='RETRY_BUILDER_SESSION'
 AND recovery.execution_run_id=dispatch.execution_run_id AND recovery.task_id=dispatch.task_mapping_id
 AND recovery.step_id=dispatch.agent_step_id AND recovery.binding_id=dispatch.builder_role_binding_id
 AND step.role_binding_id=recovery.binding_id AND step.request_id=dispatch.id
 AND (NEW.checkpoint<>'STARTED' OR (step.send_status='PENDING' AND dispatch.status='RUNNING'))
) OR (NEW.checkpoint<>'STARTED' AND NOT EXISTS (
 SELECT 1 FROM cleardev_builder_session_checks started
 WHERE started.recovery_id=NEW.recovery_id AND started.checkpoint='STARTED' AND started.binding_sha256=NEW.binding_sha256
)) OR (NEW.checkpoint='BEFORE_SEND' AND NOT EXISTS (
 SELECT 1 FROM cleardev_builder_session_checks ready
 WHERE ready.recovery_id=NEW.recovery_id AND ready.checkpoint='FINISHED' AND ready.outcome='READY'
))
BEGIN SELECT RAISE(ABORT,'Builder recheck checkpoint has no exact original request'); END;
CREATE TRIGGER cleardev_builder_session_checks_cdc AFTER INSERT ON cleardev_builder_session_checks BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'recoveryId',NEW.recovery_id),NEW.checked_at
 FROM cleardev_workflow_recoveries recovery
 JOIN cleardev_complex_execution_runs run ON run.id=recovery.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 WHERE recovery.id=NEW.recovery_id;
END;
-- +goose StatementEnd

-- +goose Down
-- Preserve the previous migrations' refusal to downgrade occupied histories.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_builder_session_checks_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_builder_session_checks_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_builder_session_checks)
 OR EXISTS(SELECT 1 FROM cleardev_planning_step_recoveries)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config, '{}'), '$.cleardev')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_builder_session_checks_down_guard;
DROP TABLE cleardev_builder_session_checks;
-- +goose StatementEnd
