-- Planner-owned contracts and deterministic admission. No Steward verdict is
-- manufactured. Existing run identities, rowids, review bindings and CDC history
-- are preserved; only new admitted V2 runs may omit a plan review.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_complex_plan_validations (
    plan_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_complex_engineering_plans(id),
    plan_sha256 TEXT NOT NULL CHECK(length(plan_sha256)=64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    policy TEXT NOT NULL CHECK(policy='PLANNER_TASK_CONTRACT_V1'),
    check_catalog_sha256 TEXT NOT NULL CHECK(length(check_catalog_sha256)=64 AND check_catalog_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;

CREATE TRIGGER cleardev_complex_plan_validation_insert_guard
BEFORE INSERT ON cleardev_complex_plan_validations
WHEN EXISTS(SELECT 1 FROM cleardev_complex_plan_validations WHERE plan_id=NEW.plan_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_complex_engineering_plans plan
    JOIN cleardev_contract_versions version ON version.id=plan.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=version.development_project_id
    JOIN cleardev_complex_agent_steps step ON step.id=plan.agent_step_id
    WHERE plan.id=NEW.plan_id AND plan.plan_sha256=NEW.plan_sha256
      AND json_extract(plan.plan_json,'$.schemaVersion')=2
      AND json_extract(plan.plan_json,'$.kind')='COMPLEX_ENGINEERING_PLAN'
      AND json_array_length(plan.plan_json,'$.tasks') BETWEEN 1 AND 3
      AND json_extract(plan.plan_json,'$.parallelSuggestion.recommendedBuilderCount') BETWEEN 1 AND 2
      AND NOT EXISTS(SELECT 1 FROM json_each(plan.plan_json,'$.tasks') task
                     WHERE COALESCE(json_array_length(task.value,'$.reviewCriteria'),0)<1)
      AND plan.development_project_id=project.id AND plan.requirement_sha256=version.sha256
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.task_set_version=0
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND step.role_binding_id=plan.planner_role_binding_id
      AND step.step_kind='COMPLEX_ENGINEERING_PLAN' AND step.request_id=plan.planning_request_id
      AND step.send_status='SETTLED' AND step.turn_id=plan.turn_id AND step.final_message_id=plan.final_message_id
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE requirement_version_id=version.id)
 )
BEGIN SELECT RAISE(ABORT,'Planner admission requires the exact settled confirmed contract and cannot replace history'); END;
CREATE TRIGGER cleardev_complex_plan_validation_update_guard
BEFORE UPDATE ON cleardev_complex_plan_validations
BEGIN SELECT RAISE(ABORT,'Planner admission is immutable'); END;
CREATE TRIGGER cleardev_complex_plan_validation_delete_guard
BEFORE DELETE ON cleardev_complex_plan_validations
BEGIN SELECT RAISE(ABORT,'Planner admission is append-only'); END;
CREATE TRIGGER cleardev_complex_plan_validation_cdc_insert
AFTER INSERT ON cleardev_complex_plan_validations
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',
           json_object('developmentProjectId',project.id,'complexPlanId',NEW.plan_id,'planValidationPolicy',NEW.policy),NEW.created_at
    FROM cleardev_complex_engineering_plans plan JOIN cleardev_development_projects project ON project.id=plan.development_project_id
    WHERE plan.id=NEW.plan_id;
END;

-- Guard every conflict target, including explicit rowid, before SQLite REPLACE
-- can silently delete the old plan. The existing UPDATE/DELETE guards remain.
CREATE TRIGGER cleardev_complex_plan_identity_insert_guard
BEFORE INSERT ON cleardev_complex_engineering_plans
WHEN NEW.id IS NULL OR EXISTS(
    SELECT 1 FROM cleardev_complex_engineering_plans old
    WHERE old.rowid=NEW.rowid OR old.id=NEW.id OR old.planning_request_id=NEW.planning_request_id
       OR old.agent_step_id=NEW.agent_step_id
       OR (old.development_project_id=NEW.development_project_id AND old.version=NEW.version)
)
BEGIN SELECT RAISE(ABORT,'engineering plan identity cannot replace history'); END;
-- Task packets carry the contract used by frozen candidates and reviewers.
-- Protect all SQLite REPLACE conflict identities as well as UPDATE/DELETE.
CREATE TRIGGER cleardev_complex_task_contract_identity_insert_guard
BEFORE INSERT ON cleardev_complex_execution_task_mappings
WHEN NEW.id IS NULL OR EXISTS(
    SELECT 1 FROM cleardev_complex_execution_task_mappings old
    WHERE old.rowid=NEW.rowid OR old.id=NEW.id OR old.work_item_id=NEW.work_item_id
       OR (old.execution_run_id=NEW.execution_run_id AND old.plan_task_key=NEW.plan_task_key)
       OR (old.execution_run_id=NEW.execution_run_id AND old.ordinal=NEW.ordinal)
)
BEGIN SELECT RAISE(ABORT,'task contract identity cannot replace candidate or review history'); END;
CREATE TRIGGER cleardev_complex_contract_no_steward_review
BEFORE INSERT ON cleardev_complex_plan_reviews
WHEN EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE id=NEW.plan_id AND json_extract(plan_json,'$.schemaVersion')=2)
BEGIN SELECT RAISE(ABORT,'Planner Task Contracts use control-plane admission, not Steward technical review'); END;

-- Alter the nullable admission reference without dropping its parent table or
-- disabling foreign keys. No run value or rowid changes and no CDC is emitted.
CREATE TEMP TABLE cleardev_run_review_bindings_0148 AS
SELECT rowid AS run_rowid,plan_review_id FROM cleardev_complex_execution_runs;
DROP TRIGGER cleardev_complex_execution_run_insert_valid;
DROP TRIGGER cleardev_complex_execution_run_update_valid;
DROP TRIGGER cleardev_complex_execution_runs_cdc_update;
ALTER TABLE cleardev_complex_execution_runs DROP COLUMN plan_review_id;
ALTER TABLE cleardev_complex_execution_runs ADD COLUMN plan_review_id TEXT REFERENCES cleardev_complex_plan_reviews(id);
UPDATE cleardev_complex_execution_runs SET plan_review_id=(
    SELECT plan_review_id FROM cleardev_run_review_bindings_0148 WHERE run_rowid=cleardev_complex_execution_runs.rowid
);
DROP TABLE cleardev_run_review_bindings_0148;

CREATE TRIGGER cleardev_complex_execution_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_runs
WHEN NEW.id IS NULL OR NEW.status<>'PENDING'
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs old
           WHERE old.rowid=NEW.rowid OR old.id=NEW.id
              OR (old.requirement_version_id=NEW.requirement_version_id AND old.plan_id=NEW.plan_id)
              OR (old.requirement_version_id=NEW.requirement_version_id AND old.status IN('PENDING','ACCEPTED')))
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_contract_versions version
    JOIN cleardev_development_projects project ON project.id=version.development_project_id
    JOIN cleardev_complex_engineering_plans plan ON plan.id=NEW.plan_id
    JOIN cleardev_complex_role_bindings steward ON steward.id=NEW.steward_role_binding_id
    WHERE version.id=NEW.requirement_version_id AND project.id=NEW.development_project_id
      AND project.cancelled_at IS NULL AND version.state='APPROVED' AND version.superseded_by_id IS NULL
      AND version.sha256=NEW.requirement_sha256 AND version.task_set_version=NEW.expected_task_set_version
      AND plan.development_project_id=project.id AND plan.requirement_version_id=version.id AND plan.plan_sha256=NEW.plan_sha256
      AND (
        (json_extract(plan.plan_json,'$.schemaVersion')=2
         AND json_extract(plan.plan_json,'$.kind')='COMPLEX_ENGINEERING_PLAN'
         AND NEW.plan_review_id IS NULL
         AND json_extract(NEW.execution_package_json,'$.planValidationPolicy')='PLANNER_TASK_CONTRACT_V1'
         AND NEW.fixed_builder_count BETWEEN 1 AND 2
         AND json_array_length(plan.plan_json,'$.tasks') BETWEEN 1 AND 3
         AND project.state<>'PAUSED'
         AND EXISTS(SELECT 1 FROM cleardev_complex_plan_validations validation
                    WHERE validation.plan_id=plan.id AND validation.plan_sha256=plan.plan_sha256
                      AND validation.policy='PLANNER_TASK_CONTRACT_V1')
         AND NOT EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans later
                        WHERE later.requirement_version_id=version.id AND later.version>plan.version))
        OR (COALESCE(json_extract(plan.plan_json,'$.schemaVersion'),1)=1
            AND json_extract(NEW.execution_package_json,'$.planValidationPolicy') IS NULL
            AND EXISTS(SELECT 1 FROM cleardev_complex_plan_reviews review
                       WHERE review.id=NEW.plan_review_id AND review.plan_id=plan.id
                         AND review.plan_sha256=plan.plan_sha256 AND review.verdict='APPROVED'))
      )
      AND (
        (NEW.mode='STANDARD' AND NEW.selection_reason_code='ONE_BUILDER_REQUIRED' AND NEW.fixed_builder_count=1
         AND json_extract(plan.plan_json,'$.parallelSuggestion.recommendedBuilderCount')=1)
        OR (NEW.mode='STANDARD' AND NEW.selection_reason_code='PARALLEL_UNSAFE_DEGRADED' AND NEW.fixed_builder_count=1
            AND json_extract(plan.plan_json,'$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
            AND json_array_length(plan.plan_json,'$.tasks')>1)
        OR (NEW.mode='PARALLEL' AND NEW.selection_reason_code='PARALLEL_PLAN_APPROVED' AND NEW.fixed_builder_count BETWEEN 2 AND 3
            AND json_extract(plan.plan_json,'$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
            AND NEW.fixed_builder_count<=json_extract(plan.plan_json,'$.parallelSuggestion.recommendedBuilderCount')
            AND json_array_length(plan.plan_json,'$.tasks')>1)
      )
      AND json_array_length(plan.plan_json,'$.tasks') BETWEEN 1 AND 6
      AND steward.development_project_id=project.id AND steward.role='STEWARD' AND steward.status='BOUND'
 )
 OR EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=NEW.requirement_version_id AND status='ACTIVE')
BEGIN SELECT RAISE(ABORT,'execution requires the exact admitted current plan and cannot replace history'); END;

CREATE TRIGGER cleardev_complex_execution_run_update_valid
BEFORE UPDATE ON cleardev_complex_execution_runs
WHEN OLD.rowid IS NOT NEW.rowid OR OLD.id IS NOT NEW.id
 OR OLD.development_project_id IS NOT NEW.development_project_id
 OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256
 OR OLD.plan_id IS NOT NEW.plan_id OR OLD.plan_review_id IS NOT NEW.plan_review_id
 OR OLD.plan_sha256 IS NOT NEW.plan_sha256 OR OLD.steward_role_binding_id IS NOT NEW.steward_role_binding_id
 OR OLD.mode IS NOT NEW.mode OR OLD.selection_reason_code IS NOT NEW.selection_reason_code
 OR OLD.fixed_builder_count IS NOT NEW.fixed_builder_count
 OR OLD.expected_task_set_version IS NOT NEW.expected_task_set_version
 OR OLD.accepted_task_set_version IS NOT NEW.accepted_task_set_version
 OR OLD.execution_package_json IS NOT NEW.execution_package_json
 OR OLD.execution_package_sha256 IS NOT NEW.execution_package_sha256 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status='PENDING' AND NEW.status NOT IN('ACCEPTED','REJECTED','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='ACCEPTED' AND NEW.status NOT IN('COMPLETED','BLOCKED','NEEDS_HUMAN'))
 OR OLD.status IN('REJECTED','FAILED','BLOCKED','NEEDS_HUMAN','COMPLETED')
 OR (NEW.mode='PARALLEL' AND NEW.status='ACCEPTED' AND OLD.status='PENDING' AND (
     EXISTS(SELECT 1 FROM cleardev_complex_execution_batches batch,json_each(batch.task_keys_json) key
            WHERE batch.execution_run_id=NEW.id GROUP BY key.value HAVING count(*)>1)
     OR EXISTS(SELECT 1 FROM cleardev_complex_execution_batches batch,json_each(batch.task_keys_json) key
               WHERE batch.execution_run_id=NEW.id AND NOT EXISTS(
                   SELECT 1 FROM cleardev_complex_execution_task_mappings task
                   WHERE task.execution_run_id=NEW.id AND task.plan_task_key=key.value))
 ))
 OR (NEW.status='COMPLETED' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_results result
                                         WHERE result.execution_run_id=NEW.id AND result.completion_status='COMPLETED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution run is immutable or settles once'); END;
CREATE TRIGGER cleardev_complex_execution_runs_cdc_update
AFTER UPDATE ON cleardev_complex_execution_runs
BEGIN
    INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
    SELECT project.ao_project_id,NULL,'cleardev_project_updated',
           json_object('developmentProjectId',NEW.development_project_id,'complexExecutionRunId',NEW.id,'status',NEW.status),COALESCE(NEW.settled_at,NEW.accepted_at)
    FROM cleardev_development_projects project WHERE project.id=NEW.development_project_id;
END;
-- +goose StatementEnd

-- Downgrade is intentionally conservative once execution or V2 planning
-- history exists. It must never rewrite a contract or fabricate a review.
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_contract_down_guard_0148 (allowed INTEGER CHECK(allowed=1));
INSERT INTO cleardev_contract_down_guard_0148
SELECT 0 WHERE EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion')=2);
DROP TABLE cleardev_contract_down_guard_0148;
DROP TRIGGER cleardev_complex_execution_run_insert_valid;
DROP TRIGGER cleardev_complex_execution_run_update_valid;
DROP TRIGGER cleardev_complex_execution_runs_cdc_update;
ALTER TABLE cleardev_complex_execution_runs DROP COLUMN plan_review_id;
ALTER TABLE cleardev_complex_execution_runs ADD COLUMN plan_review_id TEXT NOT NULL REFERENCES cleardev_complex_plan_reviews(id);
DROP TRIGGER cleardev_complex_contract_no_steward_review;
DROP TRIGGER cleardev_complex_task_contract_identity_insert_guard;
DROP TRIGGER cleardev_complex_plan_identity_insert_guard;
DROP TABLE cleardev_complex_plan_validations;
CREATE TRIGGER cleardev_complex_execution_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_runs
WHEN NEW.status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    JOIN cleardev_complex_engineering_plans AS plan ON plan.id = NEW.plan_id
    JOIN cleardev_complex_plan_reviews AS review ON review.id = NEW.plan_review_id
    JOIN cleardev_complex_role_bindings AS steward ON steward.id = NEW.steward_role_binding_id
    WHERE version.id = NEW.requirement_version_id
      AND project.id = NEW.development_project_id
      AND project.cancelled_at IS NULL
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.plan_sha256 = NEW.plan_sha256
      AND review.plan_id = plan.id
      AND review.plan_sha256 = plan.plan_sha256
      AND review.verdict = 'APPROVED'
      AND (
        (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'ONE_BUILDER_REQUIRED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') = 1)
        OR (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'PARALLEL_UNSAFE_DEGRADED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
           AND json_array_length(plan.plan_json, '$.tasks') > 1)
        OR (NEW.mode = 'PARALLEL' AND NEW.selection_reason_code = 'PARALLEL_PLAN_APPROVED'
           AND NEW.fixed_builder_count BETWEEN 2 AND 3
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
           AND NEW.fixed_builder_count <= json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount')
           AND json_array_length(plan.plan_json, '$.tasks') > 1)
      )
      AND json_array_length(plan.plan_json, '$.tasks') BETWEEN 1 AND 6
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
 )
 OR EXISTS (
    SELECT 1 FROM cleardev_direction_stop_gates AS gate
    WHERE gate.requirement_version_id = NEW.requirement_version_id AND gate.status = 'ACTIVE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run lacks the exact approved current plan or is stopped');
END;
CREATE TRIGGER cleardev_complex_execution_run_update_valid
BEFORE UPDATE ON cleardev_complex_execution_runs
WHEN OLD.development_project_id IS NOT NEW.development_project_id
 OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256
 OR OLD.plan_id IS NOT NEW.plan_id
 OR OLD.plan_review_id IS NOT NEW.plan_review_id
 OR OLD.plan_sha256 IS NOT NEW.plan_sha256
 OR OLD.steward_role_binding_id IS NOT NEW.steward_role_binding_id
 OR OLD.mode IS NOT NEW.mode
 OR OLD.selection_reason_code IS NOT NEW.selection_reason_code
 OR OLD.fixed_builder_count IS NOT NEW.fixed_builder_count
 OR OLD.expected_task_set_version IS NOT NEW.expected_task_set_version
 OR OLD.accepted_task_set_version IS NOT NEW.accepted_task_set_version
 OR OLD.execution_package_json IS NOT NEW.execution_package_json
 OR OLD.execution_package_sha256 IS NOT NEW.execution_package_sha256
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('ACCEPTED', 'REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR (OLD.status = 'ACCEPTED' AND NEW.status NOT IN ('COMPLETED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR OLD.status IN ('REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN', 'COMPLETED')
 OR (NEW.mode = 'PARALLEL' AND NEW.status = 'ACCEPTED' AND OLD.status = 'PENDING' AND (
     EXISTS (
        SELECT 1 FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS key
        WHERE batch.execution_run_id = NEW.id
        GROUP BY key.value HAVING count(*) > 1
     )
     OR EXISTS (
        SELECT 1 FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS key
        WHERE batch.execution_run_id = NEW.id
          AND NOT EXISTS (
              SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
              WHERE task.execution_run_id = NEW.id AND task.plan_task_key = key.value
          )
     )
 ))
 OR (NEW.status = 'COMPLETED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_results AS result
    WHERE result.execution_run_id = NEW.id AND result.completion_status = 'COMPLETED'
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run is immutable or settles once');
END;
CREATE TRIGGER cleardev_complex_execution_runs_cdc_update
AFTER UPDATE ON cleardev_complex_execution_runs
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'complexExecutionRunId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.accepted_at)
    FROM cleardev_development_projects AS project WHERE project.id = NEW.development_project_id;
END;
-- +goose StatementEnd
