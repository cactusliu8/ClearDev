-- Explicit project admission is separate from a planning-only V3 result.
-- No historical row is changed or admitted by this migration.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_project_execution_admissions (
 execution_run_id TEXT NOT NULL PRIMARY KEY REFERENCES cleardev_complex_execution_runs(id) DEFERRABLE INITIALLY DEFERRED,
 stage_id TEXT NOT NULL REFERENCES cleardev_product_stages(id),
 plan_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_engineering_plans(id),
 contract_json TEXT NOT NULL CHECK(json_valid(contract_json)),
 contract_sha256 TEXT NOT NULL CHECK(length(contract_sha256)=64 AND contract_sha256 NOT GLOB '*[^0-9a-f]*'),
 created_at DATETIME NOT NULL
) WITHOUT ROWID;

CREATE TRIGGER cleardev_project_admission_insert_guard BEFORE INSERT ON cleardev_project_execution_admissions
WHEN EXISTS(SELECT 1 FROM cleardev_project_execution_admissions WHERE execution_run_id=NEW.execution_run_id OR plan_id=NEW.plan_id)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id)
 OR NOT EXISTS(
   SELECT 1 FROM cleardev_product_stages stage
   JOIN cleardev_product_discussion_contexts context ON context.discussion_id=stage.discussion_id
   JOIN cleardev_product_discussions discussion ON discussion.id=stage.discussion_id
   JOIN cleardev_development_projects parent ON parent.id=stage.product_id
   JOIN cleardev_development_projects child ON child.id=stage.development_requirement_id
   JOIN cleardev_complex_engineering_plans plan ON plan.id=NEW.plan_id AND plan.development_project_id=child.id
   JOIN cleardev_contract_versions version ON version.id=plan.requirement_version_id AND version.development_project_id=child.id
   WHERE stage.id=NEW.stage_id AND context.protocol_version=2 AND context.selection_json IS NOT NULL
     AND json_type(stage.definition_json,'$.executionBasis') IS 'object'
     AND parent.cancelled_at IS NULL AND child.cancelled_at IS NULL AND parent.state<>'PAUSED' AND child.state<>'PAUSED'
     AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.task_set_version=0
     AND json_extract(plan.plan_json,'$.schemaVersion') IS 3
     AND json_extract(plan.plan_json,'$.kind') IS 'COMPLEX_ENGINEERING_PLAN'
     AND json_array_length(plan.plan_json,'$.tasks') BETWEEN 1 AND 3
     AND NOT EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans later WHERE later.requirement_version_id=version.id AND later.version>plan.version)
     AND stage.discussion_id IS (SELECT id FROM cleardev_product_discussions WHERE product_id=stage.product_id ORDER BY ordinal DESC LIMIT 1)
     AND child.ao_project_id IS json_extract(context.selection_json,'$.aoProjectId')
     AND stage.base_commit_sha IS json_extract(context.selection_json,'$.baseCommitSha')
     AND json_extract(NEW.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'
     AND json_type(NEW.contract_json,'$.requestId') IS 'text'
     AND length(trim(json_extract(NEW.contract_json,'$.requestId'))) BETWEEN 1 AND 200
     AND json_extract(NEW.contract_json,'$.executionRunId') IS NEW.execution_run_id
     AND json_extract(NEW.contract_json,'$.productId') IS stage.product_id
     AND json_extract(NEW.contract_json,'$.stageId') IS stage.id
     AND json_extract(NEW.contract_json,'$.discussionId') IS stage.discussion_id
     AND json_extract(NEW.contract_json,'$.stageDefinitionSha256') IS stage.definition_sha256
     AND json_extract(NEW.contract_json,'$.selectionSha256') IS context.selection_sha256
     AND json_extract(NEW.contract_json,'$.baseCommitSha') IS stage.base_commit_sha
     AND json_extract(NEW.contract_json,'$.requirementVersionId') IS version.id
     AND json_extract(NEW.contract_json,'$.requirementSha256') IS version.sha256
     AND json_extract(NEW.contract_json,'$.planId') IS plan.id
     AND json_extract(NEW.contract_json,'$.planSha256') IS plan.plan_sha256
     AND json_type(NEW.contract_json,'$.selection') IS 'object'
     AND json_type(NEW.contract_json,'$.basis') IS 'object'
     AND json_type(NEW.contract_json,'$.runtime') IS 'object'
     AND json_extract(NEW.contract_json,'$.runtime.environment') IS 'NODE_NPM_V1'
     AND NOT EXISTS(
       SELECT fullkey,type,atom FROM json_tree(json_extract(NEW.contract_json,'$.selection'))
       EXCEPT SELECT fullkey,type,atom FROM json_tree(context.selection_json))
     AND NOT EXISTS(
       SELECT fullkey,type,atom FROM json_tree(context.selection_json)
       EXCEPT SELECT fullkey,type,atom FROM json_tree(json_extract(NEW.contract_json,'$.selection')))
     AND NOT EXISTS(
       SELECT fullkey,type,atom FROM json_tree(json_extract(NEW.contract_json,'$.basis'))
       EXCEPT SELECT fullkey,type,atom FROM json_tree(json_extract(stage.definition_json,'$.executionBasis')))
     AND NOT EXISTS(
       SELECT fullkey,type,atom FROM json_tree(json_extract(stage.definition_json,'$.executionBasis'))
       EXCEPT SELECT fullkey,type,atom FROM json_tree(json_extract(NEW.contract_json,'$.basis')))
     AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 )
BEGIN SELECT RAISE(ABORT,'project admission requires exact current confirmed sources and cannot replace history'); END;
CREATE TRIGGER cleardev_project_admission_immutable BEFORE UPDATE ON cleardev_project_execution_admissions
BEGIN SELECT RAISE(ABORT,'project execution admission is immutable'); END;
CREATE TRIGGER cleardev_project_admission_keep_history BEFORE DELETE ON cleardev_project_execution_admissions
BEGIN SELECT RAISE(ABORT,'project execution admission history cannot be deleted'); END;
CREATE TRIGGER cleardev_project_admission_cdc AFTER INSERT ON cleardev_project_execution_admissions BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',
        json_object('developmentProjectId',stage.development_requirement_id,'projectExecutionRunId',NEW.execution_run_id),NEW.created_at
 FROM cleardev_product_stages stage JOIN cleardev_development_projects project ON project.id=stage.development_requirement_id WHERE stage.id=NEW.stage_id;
END;

DROP TRIGGER cleardev_project_planning_no_execution;
CREATE TRIGGER cleardev_project_planning_no_execution BEFORE INSERT ON cleardev_complex_execution_runs
WHEN (
 EXISTS(SELECT 1 FROM cleardev_product_stages WHERE development_requirement_id=NEW.development_project_id AND json_type(definition_json,'$.executionBasis')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE id=NEW.plan_id AND json_extract(plan_json,'$.schemaVersion')=3)
 OR json_type(NEW.execution_package_json,'$.projectExecution') IS NOT NULL
 OR json_extract(NEW.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_project_execution_admissions admission
 JOIN cleardev_product_stages stage ON stage.id=admission.stage_id
 WHERE admission.execution_run_id=NEW.id AND admission.plan_id=NEW.plan_id
   AND stage.development_requirement_id=NEW.development_project_id
   AND json_type(NEW.execution_package_json,'$.projectExecution') IS 'object'
   AND json_extract(NEW.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
   AND json_extract(NEW.execution_package_json,'$.finalReviewPolicy') IS 'REQUIREMENT_FINAL_REVIEW_V1'
   AND json_type(NEW.execution_package_json,'$.deliveryPolicy') IS NULL
   AND json_type(NEW.execution_package_json,'$.attemptPolicy') IS NULL
   AND json_type(NEW.execution_package_json,'$.freezePolicy') IS NULL
   AND json_type(NEW.execution_package_json,'$.plannerCoordinationPolicy') IS NULL
   AND NEW.plan_review_id IS NULL AND NEW.mode='STANDARD' AND NEW.fixed_builder_count=1
   AND json_extract(admission.contract_json,'$.requirementVersionId') IS NEW.requirement_version_id
   AND json_extract(admission.contract_json,'$.requirementSha256') IS NEW.requirement_sha256
   AND json_extract(admission.contract_json,'$.planSha256') IS NEW.plan_sha256
   AND json_extract(NEW.execution_package_json,'$.schemaVersion') IS 1
   AND json_extract(NEW.execution_package_json,'$.executionRunId') IS NEW.id
   AND json_extract(NEW.execution_package_json,'$.requirementVersionId') IS NEW.requirement_version_id
   AND json_extract(NEW.execution_package_json,'$.requirementSha256') IS NEW.requirement_sha256
   AND json_extract(NEW.execution_package_json,'$.planId') IS NEW.plan_id
   AND json_extract(NEW.execution_package_json,'$.planSha256') IS NEW.plan_sha256
   AND json_extract(NEW.execution_package_json,'$.mode') IS NEW.mode
   AND json_extract(NEW.execution_package_json,'$.taskSetVersion') IS NEW.accepted_task_set_version
   AND NOT EXISTS(
     SELECT fullkey,type,atom FROM json_tree(json_extract(NEW.execution_package_json,'$.projectExecution'))
     EXCEPT SELECT fullkey,type,atom FROM json_tree(admission.contract_json))
   AND NOT EXISTS(
     SELECT fullkey,type,atom FROM json_tree(admission.contract_json)
     EXCEPT SELECT fullkey,type,atom FROM json_tree(json_extract(NEW.execution_package_json,'$.projectExecution')))
)
BEGIN SELECT RAISE(ABORT,'generic project planning alone does not authorize execution'); END;

DROP TRIGGER cleardev_project_planning_no_tasks;
CREATE TRIGGER cleardev_project_planning_no_tasks BEFORE INSERT ON cleardev_work_items
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages WHERE development_requirement_id=NEW.development_project_id AND json_type(definition_json,'$.executionBasis')='object')
 AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_task_mappings task
 JOIN cleardev_complex_execution_runs run ON run.id=task.execution_run_id
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id
 WHERE task.id=NEW.complex_execution_task_id AND task.work_item_id=NEW.id
   AND run.development_project_id=NEW.development_project_id AND run.requirement_version_id=NEW.contract_version_id
   AND run.status='ACCEPTED' AND admission.plan_id=run.plan_id
   AND json_extract(task.task_packet_json,'$.schemaVersion') IS 4
   AND json_extract(task.task_packet_json,'$.executionRunId') IS run.id
   AND json_extract(task.task_packet_json,'$.requirementVersionId') IS run.requirement_version_id
   AND json_extract(task.task_packet_json,'$.planId') IS run.plan_id
   AND json_extract(task.task_packet_json,'$.planSha256') IS run.plan_sha256
   AND json_extract(task.task_packet_json,'$.taskId') IS NEW.id
 )
BEGIN SELECT RAISE(ABORT,'generic project planning requires explicit execution admission and an exact V4 packet for development tasks'); END;

DROP TRIGGER cleardev_complex_execution_run_insert_valid;
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
        OR (json_extract(plan.plan_json,'$.schemaVersion') IS 3
            AND json_extract(plan.plan_json,'$.kind') IS 'COMPLEX_ENGINEERING_PLAN'
            AND json_extract(NEW.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
            AND NEW.plan_review_id IS NULL AND project.state<>'PAUSED'
            AND NEW.mode='STANDARD' AND NEW.fixed_builder_count=1
            AND json_array_length(plan.plan_json,'$.tasks') BETWEEN 1 AND 3
            AND EXISTS(SELECT 1 FROM cleardev_project_execution_admissions admission WHERE admission.execution_run_id=NEW.id AND admission.plan_id=plan.id)
            AND NOT EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans later WHERE later.requirement_version_id=version.id AND later.version>plan.version))
      )
      AND (
        (NEW.mode='STANDARD' AND NEW.selection_reason_code='ONE_BUILDER_REQUIRED' AND NEW.fixed_builder_count=1
         AND (json_extract(plan.plan_json,'$.parallelSuggestion.recommendedBuilderCount')=1
              OR json_extract(NEW.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'))
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
BEGIN
 SELECT CASE WHEN
  EXISTS(SELECT 1 FROM cleardev_product_stages WHERE development_requirement_id=NEW.development_project_id AND json_type(definition_json,'$.executionBasis')='object')
  OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE id=NEW.plan_id AND json_extract(plan_json,'$.schemaVersion')=3)
 THEN RAISE(ABORT,'generic project planning alone does not authorize execution; an exact current admission is required')
 ELSE RAISE(ABORT,'execution requires the exact admitted current plan and cannot replace history') END;
END;
-- +goose StatementEnd

-- An execution admission cannot be downgraded into a planning-only record.
-- The empty-database down path restores the prior guards exactly.
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_execution_down_guard (ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_execution_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_execution_down_guard;
DROP TRIGGER cleardev_project_planning_no_execution;
CREATE TRIGGER cleardev_project_planning_no_execution BEFORE INSERT ON cleardev_complex_execution_runs
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.development_requirement_id=NEW.development_project_id AND json_type(s.definition_json,'$.executionBasis')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans p WHERE p.id=NEW.plan_id AND json_extract(p.plan_json,'$.schemaVersion')=3)
BEGIN SELECT RAISE(ABORT,'generic project planning does not authorize execution'); END;
DROP TRIGGER cleardev_project_planning_no_tasks;
CREATE TRIGGER cleardev_project_planning_no_tasks BEFORE INSERT ON cleardev_work_items
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.development_requirement_id=NEW.development_project_id AND json_type(s.definition_json,'$.executionBasis')='object')
BEGIN SELECT RAISE(ABORT,'generic project planning does not authorize development tasks'); END;
DROP TRIGGER cleardev_complex_execution_run_insert_valid;
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
DROP TABLE cleardev_project_execution_admissions;
-- +goose StatementEnd
