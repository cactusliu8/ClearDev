-- One deterministic retry of a settled project dependency-preparation failure.
-- Original check rows and the original blocked-attempt facts are retained.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_project_check_recoveries (
 original_check_run_id TEXT PRIMARY KEY REFERENCES cleardev_complex_execution_check_runs(id),
 retry_check_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_check_runs(id),
 original_attempt_settled_at TIMESTAMP NOT NULL,
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TRIGGER cleardev_project_check_recovery_valid BEFORE INSERT ON cleardev_project_check_recoveries
WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_runs prior
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=NEW.retry_check_run_id
 JOIN cleardev_complex_execution_check_specs spec ON spec.id=prior.check_spec_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=prior.task_attempt_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id
 JOIN cleardev_product_stages stage ON stage.id=admission.stage_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_development_projects parent ON parent.id=stage.product_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=prior.candidate_commit_id
 WHERE prior.id=NEW.original_check_run_id AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND prior.retry_ordinal=0
   AND spec.check_kind='REQUIRED_CHECK' AND spec.task_mapping_id=task.id
   AND retry.check_spec_id=prior.check_spec_id AND retry.task_attempt_id=prior.task_attempt_id
   AND retry.candidate_commit_id=prior.candidate_commit_id AND retry.candidate_commit_sha=prior.candidate_commit_sha
   AND retry.status='PENDING' AND retry.retry_ordinal=1
   AND candidate.complex_execution_task_attempt_id=attempt.id AND candidate.work_item_id=item.id AND candidate.commit_sha=prior.candidate_commit_sha
   AND attempt.status='BLOCKED' AND attempt.reason_code='CHECKER_UNAVAILABLE' AND attempt.settled_at=NEW.original_attempt_settled_at
   AND attempt.round=(SELECT max(round) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
   AND item.state='BLOCKED' AND item.paused_from_state='RUNNING'
   AND run.status='ACCEPTED' AND run.mode='STANDARD' AND run.reason_code='' AND run.settled_at IS NULL
   AND json_extract(run.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
   AND project.cancelled_at IS NULL AND parent.cancelled_at IS NULL AND project.state<>'PAUSED' AND parent.state<>'PAUSED'
   AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
   AND version.task_set_version=run.accepted_task_set_version
   AND stage.definition_sha256=json_extract(admission.contract_json,'$.stageDefinitionSha256')
   AND stage.discussion_id=(SELECT id FROM cleardev_product_discussions WHERE product_id=stage.product_id ORDER BY ordinal DESC LIMIT 1)
   AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews WHERE task_attempt_id=attempt.id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_project_check_recoveries recovered
      JOIN cleardev_complex_execution_check_runs recovered_check ON recovered_check.id=recovered.original_check_run_id
      WHERE recovered_check.task_attempt_id=attempt.id AND recovered_check.candidate_commit_id=prior.candidate_commit_id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE task_mapping_id=task.id AND candidate_commit_id=candidate.id AND role='REVIEWER')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs scope
      JOIN cleardev_complex_execution_check_specs scope_spec ON scope_spec.id=scope.check_spec_id
      WHERE scope.task_attempt_id=attempt.id AND scope.candidate_commit_id=candidate.id
        AND scope_spec.check_kind='SCOPE' AND scope.status='SETTLED' AND scope.result='PASS')
)
BEGIN SELECT RAISE(ABORT,'project dependency check recovery requires the same current candidate and settled infrastructure failure'); END;
CREATE TRIGGER cleardev_project_check_recovery_immutable BEFORE UPDATE ON cleardev_project_check_recoveries
BEGIN SELECT RAISE(ABORT,'project check recovery is immutable'); END;
CREATE TRIGGER cleardev_project_check_recovery_keep_history BEFORE DELETE ON cleardev_project_check_recoveries
BEGIN SELECT RAISE(ABORT,'project check recovery history cannot be deleted'); END;
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
     AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code='BUILDER_BUDGET_EXHAUSTED' AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
CREATE TRIGGER cleardev_project_check_recovery_apply AFTER INSERT ON cleardev_project_check_recoveries BEGIN
 UPDATE cleardev_complex_execution_task_attempts SET status='OBSERVED',reason_code='',settled_at=NULL
 WHERE id=(SELECT task_attempt_id FROM cleardev_complex_execution_check_runs WHERE id=NEW.retry_check_run_id);
 UPDATE cleardev_work_items SET state='RUNNING',paused_from_state=NULL,updated_at=NEW.created_at
 WHERE id=(SELECT candidate.work_item_id FROM cleardev_candidate_commits candidate
    JOIN cleardev_complex_execution_check_runs retry ON retry.candidate_commit_id=candidate.id WHERE retry.id=NEW.retry_check_run_id);
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',
    json_object('developmentProjectId',project.id,'originalCheckRunId',NEW.original_check_run_id,'retryCheckRunId',NEW.retry_check_run_id),NEW.created_at
 FROM cleardev_development_projects project
 JOIN cleardev_complex_execution_runs run ON run.development_project_id=project.id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.execution_run_id=run.id
 JOIN cleardev_complex_execution_check_runs retry ON retry.task_attempt_id=attempt.id WHERE retry.id=NEW.retry_check_run_id;
END;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_check_recovery_down_guard(ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_check_recovery_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_project_check_recoveries) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_check_recovery_down_guard;
DROP TRIGGER cleardev_project_check_recovery_apply;
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
     AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code='BUILDER_BUDGET_EXHAUSTED' AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
DROP TABLE cleardev_project_check_recoveries;
-- +goose StatementEnd
