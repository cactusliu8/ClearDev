-- Missing control-program failure facts. These receipts never grant recovery,
-- change task state, relax source guards or represent a check/review result.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_workflow_failure_receipts (
 id TEXT PRIMARY KEY NOT NULL,
 requirement_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
 execution_run_id TEXT NOT NULL DEFAULT '',
 source_kind TEXT NOT NULL CHECK(source_kind IN ('CANDIDATE_HANDOFF','CONTROL_OPERATION','RECOVERY_ADMISSION')),
 source_id TEXT NOT NULL,
 receipt_json TEXT NOT NULL CHECK(json_valid(receipt_json) AND json_type(receipt_json)='object' AND length(receipt_json)<=24000),
 receipt_sha256 TEXT NOT NULL CHECK(length(receipt_sha256)=64 AND receipt_sha256 NOT GLOB '*[^0-9a-f]*'),
 created_at TIMESTAMP NOT NULL,
 UNIQUE(requirement_id,source_kind,source_id),
 CHECK(COALESCE(json_extract(receipt_json,'$.id')=id AND json_extract(receipt_json,'$.requirementId')=requirement_id
   AND json_extract(receipt_json,'$.sourceKind')=source_kind AND json_extract(receipt_json,'$.sourceId')=source_id
   AND COALESCE(json_extract(receipt_json,'$.executionRunId'),'')=execution_run_id
   AND length(json_extract(receipt_json,'$.bindingSha256'))=64
   AND json_type(receipt_json,'$.beforePublish') IN ('true','false'),0))
);
CREATE INDEX cleardev_failure_receipts_requirement ON cleardev_workflow_failure_receipts(requirement_id,created_at,id);
CREATE TRIGGER cleardev_failure_receipt_immutable BEFORE UPDATE ON cleardev_workflow_failure_receipts
BEGIN SELECT RAISE(ABORT,'workflow failure receipt is immutable'); END;
CREATE TRIGGER cleardev_failure_receipt_keep_history BEFORE DELETE ON cleardev_workflow_failure_receipts
BEGIN SELECT RAISE(ABORT,'workflow failure receipt history is immutable'); END;
CREATE TRIGGER cleardev_failure_receipt_no_replace BEFORE INSERT ON cleardev_workflow_failure_receipts
WHEN EXISTS(SELECT 1 FROM cleardev_workflow_failure_receipts old WHERE old.id=NEW.id OR (old.requirement_id=NEW.requirement_id AND old.source_kind=NEW.source_kind AND old.source_id=NEW.source_id))
BEGIN SELECT RAISE(ABORT,'workflow failure receipt cannot replace history'); END;
CREATE TRIGGER cleardev_failure_receipt_handoff_source BEFORE INSERT ON cleardev_workflow_failure_receipts
WHEN NEW.source_kind='CANDIDATE_HANDOFF' AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_complex_execution_agent_steps step ON step.id=attempt.agent_step_id
 WHERE attempt.id=NEW.source_id AND run.id=NEW.execution_run_id AND run.development_project_id=NEW.requirement_id
 AND run.mode='STANDARD' AND run.status='ACCEPTED' AND run.settled_at IS NULL
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND step.send_status='SETTLED' AND step.role_binding_id=attempt.builder_role_binding_id AND step.request_id=attempt.id
 AND json_extract(NEW.receipt_json,'$.role')='BUILDER'
 AND NOT EXISTS(SELECT 1 FROM cleardev_candidate_commits candidate WHERE candidate.complex_execution_task_attempt_id=attempt.id)
) BEGIN SELECT RAISE(ABORT,'handoff failure requires the exact unpublished current project source'); END;
CREATE TRIGGER cleardev_failure_receipt_cdc AFTER INSERT ON cleardev_workflow_failure_receipts BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',id,'failureId',NEW.id),NEW.created_at
 FROM cleardev_development_projects WHERE id=NEW.requirement_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_failure_downgrade_guard(value INTEGER CHECK(value=0));
INSERT INTO cleardev_failure_downgrade_guard SELECT count(*) FROM cleardev_workflow_failure_receipts;
DROP TABLE cleardev_failure_downgrade_guard;
DROP TABLE cleardev_workflow_failure_receipts;
-- +goose StatementEnd
