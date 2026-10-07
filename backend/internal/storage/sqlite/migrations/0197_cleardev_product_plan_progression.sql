-- Approved product plans retain immutable stage definitions; each successor's
-- delivery source is frozen separately. No existing approval is expanded.
-- +goose Up
-- +goose StatementBegin
CREATE UNIQUE INDEX cleardev_product_plan_decision_once ON cleardev_human_decision_requests
(json_extract(binding_json,'$.discussionId')) WHERE decision_kind='CONFIRM_PRODUCT_PLAN';

CREATE VIEW cleardev_approved_product_plans AS
SELECT request.id AS request_id, discussion.product_id, discussion.id AS discussion_id
FROM cleardev_human_decision_requests request
JOIN cleardev_human_decision_effects effect ON effect.request_id=request.id AND effect.decision='APPROVE'
JOIN cleardev_product_discussions discussion ON discussion.id=json_extract(request.binding_json,'$.discussionId')
JOIN cleardev_product_discussion_contexts context ON context.discussion_id=discussion.id
JOIN cleardev_development_projects parent ON parent.id=discussion.product_id
WHERE request.decision_kind='CONFIRM_PRODUCT_PLAN' AND request.status='RESOLVED' AND request.decision='APPROVE'
 AND request.development_project_id=discussion.product_id
 AND json_extract(request.binding_json,'$.productId') IS discussion.product_id
 AND json_extract(request.binding_json,'$.resultSha256') IS discussion.result_sha256
 AND json_extract(request.binding_json,'$.selectionSha256') IS context.selection_sha256
 AND discussion.settled_at IS NOT NULL AND json_extract(discussion.result_json,'$.outcome')='READY'
 AND discussion.id=(SELECT id FROM cleardev_product_discussions WHERE product_id=discussion.product_id ORDER BY ordinal DESC LIMIT 1)
 AND parent.cancelled_at IS NULL AND parent.state<>'PAUSED' AND parent.paused_from_state IS NULL;

CREATE TABLE cleardev_product_stage_sources (
 stage_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_product_stages(id),
 authorization_id TEXT NOT NULL REFERENCES cleardev_human_decision_requests(id),
 selection_json TEXT NOT NULL CHECK(json_valid(selection_json)),
 selection_sha256 TEXT NOT NULL CHECK(length(selection_sha256)=64),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TRIGGER cleardev_product_stage_source_guard BEFORE INSERT ON cleardev_product_stage_sources
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_product_stages stage
 JOIN cleardev_approved_product_plans grant ON grant.discussion_id=stage.discussion_id AND grant.request_id=NEW.authorization_id
 JOIN cleardev_product_stages previous ON previous.discussion_id=stage.discussion_id AND previous.ordinal=stage.ordinal-1
 JOIN cleardev_completed_project_deliveries delivered ON delivered.stage_id=previous.id
 WHERE stage.id=NEW.stage_id AND stage.development_requirement_id IS NULL
 AND json_extract(NEW.selection_json,'$.planAuthorizationId') IS NEW.authorization_id
 AND json_extract(NEW.selection_json,'$.sourceDiscussionId') IS stage.discussion_id
 AND json_type(NEW.selection_json,'$.choiceDiscussionId') IS NULL
 AND json_extract(NEW.selection_json,'$.delivery.requirementId') IS delivered.requirement_id
 AND json_extract(NEW.selection_json,'$.delivery.executionRunId') IS delivered.execution_run_id
 AND json_extract(NEW.selection_json,'$.delivery.resultId') IS delivered.result_id
 AND json_extract(NEW.selection_json,'$.delivery.integrationCandidateId') IS delivered.integration_candidate_id
 AND json_extract(NEW.selection_json,'$.delivery.candidateSha') IS delivered.candidate_sha
 AND json_extract(NEW.selection_json,'$.baseCommitSha') IS delivered.candidate_sha
 AND json_extract(NEW.selection_json,'$.aoProjectId') IS json_extract(delivered.contract_json,'$.selection.aoProjectId')
 AND json_extract(NEW.selection_json,'$.repositoryPath') IS json_extract(delivered.contract_json,'$.selection.repositoryPath')
 AND json_extract(NEW.selection_json,'$.repositoryUrl') IS json_extract(delivered.contract_json,'$.selection.repositoryUrl')
 AND json_extract(NEW.selection_json,'$.option') IS json_extract(delivered.contract_json,'$.selection.option')
)
BEGIN SELECT RAISE(ABORT,'automatic stage source requires approved plan and exact preceding final delivery'); END;
CREATE TRIGGER cleardev_product_stage_source_immutable BEFORE UPDATE ON cleardev_product_stage_sources
BEGIN SELECT RAISE(ABORT,'stage source is immutable'); END;
CREATE TRIGGER cleardev_product_stage_source_keep BEFORE DELETE ON cleardev_product_stage_sources
BEGIN SELECT RAISE(ABORT,'stage source history is retained'); END;
CREATE TRIGGER cleardev_product_stage_source_cdc AFTER INSERT ON cleardev_product_stage_sources BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT parent.ao_project_id,NULL,'cleardev_project_updated',json_object('productId',stage.product_id),NEW.created_at
 FROM cleardev_product_stages stage JOIN cleardev_development_projects parent ON parent.id=stage.product_id WHERE stage.id=NEW.stage_id;
END;
CREATE VIEW cleardev_effective_stage_contexts AS
SELECT stage.id AS stage_id, context.discussion_id, context.protocol_version,
 COALESCE(source.selection_json,context.selection_json,'') AS selection_json,
 COALESCE(source.selection_sha256,context.selection_sha256,'') AS selection_sha256
FROM cleardev_product_stages stage
JOIN cleardev_product_discussion_contexts context ON context.discussion_id=stage.discussion_id
LEFT JOIN cleardev_product_stage_sources source ON source.stage_id=stage.id;

DROP TRIGGER cleardev_product_stage_bind_guard;
CREATE TRIGGER cleardev_product_stage_bind_guard BEFORE UPDATE ON cleardev_product_stages
WHEN OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.discussion_id IS NOT NEW.discussion_id
 OR OLD.ordinal IS NOT NEW.ordinal OR OLD.definition_json IS NOT NEW.definition_json
 OR OLD.definition_sha256 IS NOT NEW.definition_sha256 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.development_requirement_id IS NOT NULL OR NEW.development_requirement_id IS NULL OR NEW.base_commit_sha IS NULL
 OR NEW.development_requirement_id=NEW.product_id
 OR NEW.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=NEW.product_id ORDER BY ordinal DESC LIMIT 1)
 OR (json_type(NEW.definition_json,'$.executionBasis') IS NOT 'object' AND json_extract(NEW.definition_json,'$.feasibility')<>'SUPPORTED')
 OR NOT EXISTS (
   SELECT 1 FROM cleardev_development_projects child, cleardev_development_projects parent
   WHERE child.id=NEW.development_requirement_id AND parent.id=NEW.product_id
     AND child.cancelled_at IS NULL AND parent.cancelled_at IS NULL AND child.state<>'PAUSED' AND child.paused_from_state IS NULL AND parent.state<>'PAUSED' AND parent.paused_from_state IS NULL
     AND (
       (json_type(NEW.definition_json,'$.executionBasis') IS NOT 'object' AND child.ao_project_id=parent.ao_project_id)
       OR EXISTS(SELECT 1 FROM cleardev_effective_stage_contexts c JOIN cleardev_product_discussions d ON d.id=c.discussion_id
          WHERE c.stage_id=NEW.id AND c.discussion_id=NEW.discussion_id AND c.selection_json IS NOT NULL
            AND child.ao_project_id=json_extract(c.selection_json,'$.aoProjectId')
            AND NEW.base_commit_sha=json_extract(c.selection_json,'$.baseCommitSha')
            AND json_extract(d.result_json,'$.selectedOptionKey')=json_extract(c.selection_json,'$.option.key'))
     )
 )
BEGIN SELECT RAISE(ABORT,'stage link is stale, unselected, changed or cross-project'); END;
DROP TRIGGER cleardev_project_admission_insert_guard;
CREATE TRIGGER cleardev_project_admission_insert_guard BEFORE INSERT ON cleardev_project_execution_admissions
WHEN EXISTS(SELECT 1 FROM cleardev_project_execution_admissions WHERE execution_run_id=NEW.execution_run_id OR plan_id=NEW.plan_id)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id)
 OR NOT EXISTS(
   SELECT 1 FROM cleardev_product_stages stage
   JOIN cleardev_effective_stage_contexts context ON context.stage_id=stage.id
   JOIN cleardev_product_discussions discussion ON discussion.id=stage.discussion_id
   JOIN cleardev_development_projects parent ON parent.id=stage.product_id
   JOIN cleardev_development_projects child ON child.id=stage.development_requirement_id
   JOIN cleardev_complex_engineering_plans plan ON plan.id=NEW.plan_id AND plan.development_project_id=child.id
   JOIN cleardev_contract_versions version ON version.id=plan.requirement_version_id AND version.development_project_id=child.id
   WHERE stage.id=NEW.stage_id AND context.protocol_version=2 AND context.selection_json IS NOT NULL
     AND json_type(stage.definition_json,'$.executionBasis') IS 'object'
     AND parent.cancelled_at IS NULL AND child.cancelled_at IS NULL AND parent.state<>'PAUSED' AND parent.paused_from_state IS NULL AND child.state<>'PAUSED' AND child.paused_from_state IS NULL
     AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.task_set_version=0
     AND json_extract(plan.plan_json,'$.schemaVersion') IS 3
     AND json_extract(plan.plan_json,'$.kind') IS 'COMPLEX_ENGINEERING_PLAN'
     AND json_array_length(plan.plan_json,'$.tasks') BETWEEN 1 AND 3
     AND NOT EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans later WHERE later.requirement_version_id=version.id AND later.version>plan.version)
     AND stage.discussion_id IS (SELECT id FROM cleardev_product_discussions WHERE product_id=stage.product_id ORDER BY ordinal DESC LIMIT 1)
     AND child.ao_project_id IS json_extract(context.selection_json,'$.aoProjectId')
     AND stage.base_commit_sha IS json_extract(context.selection_json,'$.baseCommitSha')
     AND (json_extract(NEW.contract_json,'$.requestId') NOT LIKE 'product-plan:%' OR EXISTS(SELECT 1 FROM cleardev_approved_product_plans grant WHERE grant.discussion_id=stage.discussion_id))
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
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE product_plan_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO product_plan_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_product_stage_sources) OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests WHERE decision_kind='CONFIRM_PRODUCT_PLAN') THEN 0 ELSE 1 END;
DROP TABLE product_plan_down_guard;
DROP TRIGGER cleardev_product_stage_bind_guard;
CREATE TRIGGER cleardev_product_stage_bind_guard BEFORE UPDATE ON cleardev_product_stages
WHEN OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.discussion_id IS NOT NEW.discussion_id
 OR OLD.ordinal IS NOT NEW.ordinal OR OLD.definition_json IS NOT NEW.definition_json
 OR OLD.definition_sha256 IS NOT NEW.definition_sha256 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.development_requirement_id IS NOT NULL OR NEW.development_requirement_id IS NULL OR NEW.base_commit_sha IS NULL
 OR NEW.development_requirement_id=NEW.product_id
 OR NEW.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=NEW.product_id ORDER BY ordinal DESC LIMIT 1)
 OR (json_type(NEW.definition_json,'$.executionBasis') IS NOT 'object' AND json_extract(NEW.definition_json,'$.feasibility')<>'SUPPORTED')
 OR NOT EXISTS (
   SELECT 1 FROM cleardev_development_projects child, cleardev_development_projects parent
   WHERE child.id=NEW.development_requirement_id AND parent.id=NEW.product_id
     AND child.cancelled_at IS NULL AND parent.cancelled_at IS NULL
     AND (
       (json_type(NEW.definition_json,'$.executionBasis') IS NOT 'object' AND child.ao_project_id=parent.ao_project_id)
       OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts c JOIN cleardev_product_discussions d ON d.id=c.discussion_id
          WHERE c.discussion_id=NEW.discussion_id AND c.selection_json IS NOT NULL
            AND child.ao_project_id=json_extract(c.selection_json,'$.aoProjectId')
            AND NEW.base_commit_sha=json_extract(c.selection_json,'$.baseCommitSha')
            AND json_extract(d.result_json,'$.selectedOptionKey')=json_extract(c.selection_json,'$.option.key'))
     )
 )
BEGIN SELECT RAISE(ABORT,'stage link is stale, unselected, changed or cross-project'); END;
DROP TRIGGER cleardev_project_admission_insert_guard;
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
DROP VIEW cleardev_effective_stage_contexts;
DROP TABLE cleardev_product_stage_sources;
DROP VIEW cleardev_approved_product_plans;
DROP INDEX cleardev_product_plan_decision_once;
-- +goose StatementEnd
