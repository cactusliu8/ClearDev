-- +goose Up
-- A historical generic execution stops blocking discussion only after its
-- exact integrated candidate, complete transaction and latest final PASS.
-- This view derives existing facts; it does not store a second product state.
-- +goose StatementBegin
CREATE VIEW cleardev_completed_project_deliveries AS
SELECT s.product_id, s.id AS stage_id, r.id AS execution_run_id,
 r.development_project_id AS requirement_id, result.id AS result_id,
 candidate.id AS integration_candidate_id, candidate.commit_sha AS candidate_sha,
 admission.contract_json, review.id AS final_review_id
FROM cleardev_product_stages s
JOIN cleardev_complex_execution_runs r ON r.development_project_id=s.development_requirement_id
JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=r.id AND admission.stage_id=s.id AND admission.plan_id=r.plan_id
JOIN cleardev_complex_execution_results result ON result.execution_run_id=r.id
JOIN cleardev_integration_candidates candidate ON candidate.id=result.integration_candidate_id AND candidate.complex_execution_result_id=result.id
JOIN cleardev_requirement_final_reviews review ON review.execution_run_id=r.id
WHERE r.status IS 'COMPLETED' AND r.settled_at IS NOT NULL
 AND result.completion_status IS 'COMPLETED' AND result.completed_at IS NOT NULL
 AND json_extract(admission.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(admission.contract_json,'$.productId') IS s.product_id
 AND json_extract(admission.contract_json,'$.stageId') IS s.id
 AND json_extract(r.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(r.execution_package_json,'$.projectExecution') IS admission.contract_json
 AND candidate.requirement_version_id IS r.requirement_version_id
 AND candidate.development_project_id IS r.development_project_id
 AND review.rowid=(SELECT max(latest.rowid) FROM cleardev_requirement_final_reviews latest WHERE latest.execution_run_id=r.id)
 AND review.status IS 'SETTLED' AND review.verdict IS 'PASS' AND review.result_id IS NOT NULL AND review.settled_at IS NOT NULL
 AND review.development_project_id IS r.development_project_id
 AND review.requirement_version_id IS r.requirement_version_id AND review.requirement_sha256 IS r.requirement_sha256
 AND review.plan_id IS r.plan_id AND review.plan_sha256 IS r.plan_sha256
 AND review.candidate_commit_sha IS candidate.commit_sha
 AND review.base_commit_sha IS json_extract(admission.contract_json,'$.baseCommitSha')
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id
     AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected WHERE expected.value IS checks.check_run_id))
 AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected
     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id AND checks.check_run_id IS expected.value));

DROP TRIGGER cleardev_product_discussion_insert_guard;
CREATE TRIGGER cleardev_product_discussion_insert_guard BEFORE INSERT ON cleardev_product_discussions
WHEN NEW.settled_at IS NOT NULL OR NEW.ordinal<>(SELECT count(*) FROM cleardev_product_discussions WHERE product_id=NEW.product_id)
 OR EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.product_id=NEW.product_id AND s.development_requirement_id IS NOT NULL
   AND (json_type(s.definition_json,'$.executionBasis') IS NOT 'object'
        OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs r WHERE r.development_project_id=s.development_requirement_id
          AND NOT EXISTS(SELECT 1 FROM cleardev_completed_project_deliveries delivered WHERE delivered.execution_run_id=r.id AND delivered.stage_id=s.id))))
 OR EXISTS(SELECT 1 FROM cleardev_development_projects WHERE id=NEW.product_id AND cancelled_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'discussion is stale, executing, legacy-frozen or not pending'); END;

-- A UI/CLI-supplied SHA is not proof of completion. Repeat the exact delivery
-- provenance at context insertion, in addition to Service and Store checks.
CREATE TRIGGER cleardev_project_delivery_context_guard BEFORE INSERT ON cleardev_product_discussion_contexts
WHEN json_type(NEW.selection_json,'$.delivery') IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM cleardev_completed_project_deliveries delivered
 JOIN cleardev_product_discussions discussion ON discussion.id=NEW.discussion_id AND discussion.product_id=delivered.product_id
 WHERE json_type(NEW.selection_json,'$.delivery') IS 'object'
  AND delivered.execution_run_id IS json_extract(NEW.selection_json,'$.delivery.executionRunId')
  AND delivered.requirement_id IS json_extract(NEW.selection_json,'$.delivery.requirementId')
  AND delivered.result_id IS json_extract(NEW.selection_json,'$.delivery.resultId')
  AND delivered.integration_candidate_id IS json_extract(NEW.selection_json,'$.delivery.integrationCandidateId')
  AND delivered.candidate_sha IS json_extract(NEW.selection_json,'$.delivery.candidateSha')
  AND delivered.candidate_sha IS json_extract(NEW.selection_json,'$.baseCommitSha')
  AND json_extract(delivered.contract_json,'$.selection.aoProjectId') IS json_extract(NEW.selection_json,'$.aoProjectId')
  AND json_extract(delivered.contract_json,'$.selection.repositoryPath') IS json_extract(NEW.selection_json,'$.repositoryPath')
  AND COALESCE(json_extract(delivered.contract_json,'$.selection.repositoryUrl'),'') IS COALESCE(json_extract(NEW.selection_json,'$.repositoryUrl'),'')
  AND json_extract(delivered.contract_json,'$.selection.option.key') IS json_extract(NEW.selection_json,'$.option.key')
  AND json_extract(delivered.contract_json,'$.selection.option.title') IS json_extract(NEW.selection_json,'$.option.title')
  AND json_extract(delivered.contract_json,'$.selection.option.origin') IS json_extract(NEW.selection_json,'$.option.origin')
  AND COALESCE(json_extract(delivered.contract_json,'$.selection.option.repositoryUrl'),'') IS COALESCE(json_extract(NEW.selection_json,'$.option.repositoryUrl'),'')
  AND json_extract(delivered.contract_json,'$.selection.option.description') IS json_extract(NEW.selection_json,'$.option.description')
  AND COALESCE(json_extract(delivered.contract_json,'$.selection.option.tradeoffs'),'[]') IS COALESCE(json_extract(NEW.selection_json,'$.option.tradeoffs'),'[]')
)
BEGIN SELECT RAISE(ABORT,'project baseline requires the exact completed and independently reviewed delivery'); END;
-- +goose StatementEnd

-- +goose Down
-- Do not reinterpret any admitted execution or continuation under older rules.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_continuation_down_guard (ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_continuation_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_continuation_down_guard;
DROP TRIGGER cleardev_project_delivery_context_guard;
DROP TRIGGER cleardev_product_discussion_insert_guard;
DROP VIEW cleardev_completed_project_deliveries;
CREATE TRIGGER cleardev_product_discussion_insert_guard BEFORE INSERT ON cleardev_product_discussions
WHEN NEW.settled_at IS NOT NULL OR NEW.ordinal<>(SELECT count(*) FROM cleardev_product_discussions WHERE product_id=NEW.product_id)
 OR EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.product_id=NEW.product_id AND s.development_requirement_id IS NOT NULL
   AND (json_type(s.definition_json,'$.executionBasis') IS NOT 'object'
        OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs r WHERE r.development_project_id=s.development_requirement_id)))
 OR EXISTS(SELECT 1 FROM cleardev_development_projects WHERE id=NEW.product_id AND cancelled_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'discussion is stale, executing, legacy-frozen or not pending'); END;
-- +goose StatementEnd
