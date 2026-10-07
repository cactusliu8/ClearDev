-- +goose Up
-- +goose StatementBegin
-- A passed task or arbitrary SHA is not a source selection. The existing
-- append-only discussion context carries explicit completed-delivery provenance.
CREATE TRIGGER cleardev_project_delivery_selection_valid
BEFORE INSERT ON cleardev_product_discussion_contexts
WHEN json_type(NEW.selection_json,'$.delivery') IS NOT NULL AND NOT EXISTS(
 SELECT 1
 FROM cleardev_product_discussions discussion
 JOIN cleardev_complex_execution_runs run ON run.id=json_extract(NEW.selection_json,'$.delivery.executionRunId')
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id
 JOIN cleardev_complex_execution_results result ON result.execution_run_id=run.id
 JOIN cleardev_integration_candidates candidate ON candidate.id=result.integration_candidate_id
 JOIN cleardev_requirement_final_reviews review ON review.execution_run_id=run.id
 WHERE discussion.id=NEW.discussion_id
   AND json_type(NEW.selection_json,'$.delivery') IS 'object'
   AND json_type(NEW.selection_json,'$.choiceDiscussionId') IS 'text'
   AND run.status='COMPLETED' AND run.settled_at IS NOT NULL
   AND run.development_project_id=json_extract(NEW.selection_json,'$.delivery.requirementId')
   AND json_extract(admission.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'
   AND json_extract(admission.contract_json,'$.productId') IS discussion.product_id
   AND json_extract(admission.contract_json,'$.selection.aoProjectId') IS json_extract(NEW.selection_json,'$.aoProjectId')
   AND json_extract(admission.contract_json,'$.selection.repositoryPath') IS json_extract(NEW.selection_json,'$.repositoryPath')
   AND json_extract(admission.contract_json,'$.selection.repositoryUrl') IS json_extract(NEW.selection_json,'$.repositoryUrl')
   AND json_extract(admission.contract_json,'$.selection.option.key') IS json_extract(NEW.selection_json,'$.option.key')
   AND result.id=json_extract(NEW.selection_json,'$.delivery.resultId')
   AND result.completion_status='COMPLETED'
   AND result.integration_candidate_id=json_extract(NEW.selection_json,'$.delivery.integrationCandidateId')
   AND candidate.requirement_version_id=run.requirement_version_id
   AND candidate.complex_execution_result_id=result.id AND result.completed_at IS NOT NULL
   AND candidate.commit_sha=json_extract(NEW.selection_json,'$.delivery.candidateSha')
   AND candidate.commit_sha=json_extract(NEW.selection_json,'$.baseCommitSha')
   AND review.status='SETTLED' AND review.verdict='PASS'
   AND review.candidate_commit_sha=candidate.commit_sha
   AND review.plan_id=run.plan_id AND review.plan_sha256=run.plan_sha256
   AND review.requirement_sha256=run.requirement_sha256
)
BEGIN SELECT RAISE(ABORT,'project baseline must be the exact completed integrated delivery with independent final PASS'); END;

-- The service verifies request/result digests, environment manifests and raw
-- bytes as well. SQL independently rejects cross-candidate/contract/command
-- substitution and false PASS/timeout evidence at the existing settle boundary.
CREATE TRIGGER cleardev_project_check_receipt_settle_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN NEW.status='SETTLED' AND NEW.result IN ('PASS','FAIL') AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
   AND json_valid(NEW.output_summary)
   AND json_extract(NEW.output_summary,'$.schemaVersion') IS 1
   AND json_extract(NEW.output_summary,'$.policy') IS 'PROJECT_CHECK_V1'
   AND json_extract(NEW.output_summary,'$.executionRunId') IS spec.execution_run_id
   AND json_extract(NEW.output_summary,'$.contractSha256') IS admission.contract_sha256
   AND json_extract(NEW.output_summary,'$.checkRunId') IS NEW.id
   AND json_extract(NEW.output_summary,'$.checkId') IS spec.check_name
   AND json_extract(NEW.output_summary,'$.candidateSha') IS NEW.candidate_commit_sha
   AND json(json_extract(NEW.output_summary,'$.argv')) IS json(spec.argv_json)
   AND json_extract(NEW.output_summary,'$.timeoutSeconds') IS spec.timeout_seconds
   AND json_extract(NEW.output_summary,'$.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
   AND json_extract(NEW.output_summary,'$.imageId') IS NEW.container_image_id
   AND json_type(NEW.output_summary,'$.exitCode') IS 'integer'
   AND json_extract(NEW.output_summary,'$.exitCode') IS NEW.exit_code
   AND json_extract(NEW.output_summary,'$.timedOut') IS NEW.timed_out
   AND json_type(NEW.output_summary,'$.nodeVersion') IS 'text'
   AND length(json_extract(NEW.output_summary,'$.nodeVersion'))>0
   AND length(json_extract(NEW.output_summary,'$.sourceManifestId'))=64
   AND length(json_extract(NEW.output_summary,'$.sourceRootTreeOid'))=40
   AND length(json_extract(NEW.output_summary,'$.checkEnvironmentId'))=64
   AND length(json_extract(NEW.output_summary,'$.approvedArgvSha256'))=64
   AND length(json_extract(NEW.output_summary,'$.outputSha256'))=64
   AND json_type(NEW.output_summary,'$.outputSummary') IS 'text'
   AND ((NEW.result='PASS' AND NEW.exit_code=0 AND NEW.timed_out=0
         AND json_extract(NEW.output_summary,'$.outcome') IS 'PASS'
         AND json_type(NEW.output_summary,'$.outputTruncated') IS 'false')
     OR (NEW.result='FAIL' AND ((NEW.timed_out=1 AND json_extract(NEW.output_summary,'$.outcome') IS 'TIMED_OUT')
       OR (NEW.timed_out=0 AND json_extract(NEW.output_summary,'$.outcome') IS 'FAIL'
         AND (NEW.exit_code<>0 OR json_type(NEW.output_summary,'$.outputTruncated') IS 'true')))))
)
BEGIN SELECT RAISE(ABORT,'project checks require actual exact candidate/contract/command/environment receipts'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_evidence_down_guard(ok INTEGER NOT NULL CHECK(ok=1));
-- Preserve the earlier no-partial-downgrade contract for both mail and projects.
INSERT INTO cleardev_project_evidence_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_evidence_down_guard;
DROP TRIGGER cleardev_project_check_receipt_settle_valid;
DROP TRIGGER cleardev_project_delivery_selection_valid;
-- +goose StatementEnd
