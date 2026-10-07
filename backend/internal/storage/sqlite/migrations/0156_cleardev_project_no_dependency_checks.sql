-- +goose Up
-- A project with no npm dependencies can run its admitted npm check without a lockfile.
-- The exact candidate package manifest, image and environment remain required.
-- +goose StatementBegin
DROP TRIGGER cleardev_review_check_result_valid;
CREATE TRIGGER cleardev_review_check_result_valid BEFORE INSERT ON cleardev_review_check_results
WHEN EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=NEW.review_id AND check_id=NEW.check_id OR run_id=NEW.run_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_review_check_requests request
 JOIN cleardev_complex_execution_reviews review ON review.id=request.review_id
 WHERE request.review_id=NEW.review_id AND review.status='PENDING'
 AND NEW.run_id=NEW.review_id||':requested-check:'||NEW.check_id
 AND EXISTS(SELECT 1 FROM json_each(request.request_json,'$.checkIds') WHERE value=NEW.check_id)
 AND json_extract(NEW.result_json,'$.reviewId') IS NEW.review_id
 AND json_extract(NEW.result_json,'$.checkId') IS NEW.check_id
 AND json_extract(NEW.result_json,'$.proof.runId') IS NEW.run_id
 AND json_extract(NEW.result_json,'$.proof.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
 AND json_extract(NEW.result_json,'$.outcome') IN ('PASS','FAIL','TIMED_OUT','INFRA_ERROR')
 AND (
   (json_type(request.request_json,'$.projectExecution') IS NULL
    AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
    AND NEW.check_id IN('demo-backend','demo-api','demo-frontend','demo-integration'))
   OR EXISTS(
    SELECT 1 FROM cleardev_project_execution_admissions admission
    JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id AND attempt.execution_run_id=admission.execution_run_id
    JOIN json_each(admission.contract_json,'$.basis.checks') approved
    WHERE json_extract(request.request_json,'$.projectExecution') IS admission.contract_json
      AND json_extract(approved.value,'$.id') IS NEW.check_id
      AND json(json_extract(NEW.result_json,'$.proof.argv')) IS json(json_extract(approved.value,'$.argv'))
      AND (
       (json_extract(NEW.result_json,'$.outcome') IS 'INFRA_ERROR'
        AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
        AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
        AND json_extract(NEW.result_json,'$.proof.exitCode') IS -1)
       OR
       (json_type(NEW.result_json,'$.projectReceipt') IS 'object'
        AND json_extract(NEW.result_json,'$.projectReceipt.schemaVersion') IS 1
        AND json_extract(NEW.result_json,'$.projectReceipt.policy') IS 'PROJECT_CHECK_V1'
        AND json_extract(NEW.result_json,'$.projectReceipt.executionRunId') IS admission.execution_run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.contractSha256') IS admission.contract_sha256
        AND json_extract(NEW.result_json,'$.projectReceipt.checkRunId') IS NEW.run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.checkId') IS NEW.check_id
        AND json_extract(NEW.result_json,'$.projectReceipt.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
        AND json(json_extract(NEW.result_json,'$.projectReceipt.argv')) IS json(json_extract(approved.value,'$.argv'))
        AND json_extract(NEW.result_json,'$.projectReceipt.timeoutSeconds') IS json_extract(approved.value,'$.timeoutSeconds')
        AND json_extract(NEW.result_json,'$.projectReceipt.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') IS json_extract(NEW.result_json,'$.proof.imageId')
        AND length(json_extract(NEW.result_json,'$.projectReceipt.imageId'))=71
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') LIKE 'sha256:%'
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid'))=40
        AND length(json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.approvedArgvSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.outputSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.nodeVersion'))>0
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId') IS json_extract(NEW.result_json,'$.proof.sourceManifestId')
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid') IS json_extract(NEW.result_json,'$.proof.sourceTreeOid')
        AND json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId') IS json_extract(NEW.result_json,'$.proof.environmentId')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSha256') IS json_extract(NEW.result_json,'$.proof.outputSha256')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSummary') IS json_extract(NEW.result_json,'$.output')
        AND json_extract(NEW.result_json,'$.projectReceipt.outcome') IS json_extract(NEW.result_json,'$.outcome')
        AND json_extract(NEW.result_json,'$.projectReceipt.exitCode') IS json_extract(NEW.result_json,'$.proof.exitCode')
        AND json_extract(NEW.result_json,'$.projectReceipt.timedOut') IS json_extract(NEW.result_json,'$.proof.timedOut')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputTruncated') IS json_extract(NEW.result_json,'$.proof.truncated')
        AND (json_extract(approved.value,'$.argv[0]') IS NOT 'npm' OR (
          length(json_extract(NEW.result_json,'$.projectReceipt.npmVersion'))>0
          AND length(json_extract(NEW.result_json,'$.projectReceipt.packageJsonSha256'))=64
          AND ((length(json_extract(NEW.result_json,'$.projectReceipt.packageLockSha256'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyCacheKey'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyEnvironment'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyTreeSha256'))=64)
           OR (json_type(NEW.result_json,'$.projectReceipt.packageLockSha256') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyCacheKey') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyEnvironment') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyTreeSha256') IS NULL))))
        AND (
         (json_extract(NEW.result_json,'$.outcome') IS 'PASS'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'true'
          AND json_extract(NEW.result_json,'$.proof.exitCode') IS 0
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND json_type(NEW.result_json,'$.proof.truncated') IS 'false')
         OR (json_extract(NEW.result_json,'$.outcome') IS 'FAIL'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND (json_extract(NEW.result_json,'$.proof.exitCode')<>0 OR json_type(NEW.result_json,'$.proof.truncated') IS 'true'))
         OR (json_extract(NEW.result_json,'$.outcome') IS 'TIMED_OUT'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'true')))
      )
   )
 )
) BEGIN SELECT RAISE(ABORT,'review check result must bind its exact versioned request and actual runtime receipt'); END;
-- +goose StatementEnd

-- +goose Down
-- Refuse before restoring the older receipt rule. Goose commits each Down
-- separately, so relying on 0155 would already change the recorded version
-- and the live receipt trigger before that later guard rejects the downgrade.
-- The Up migration and all stored receipt/history bytes remain unchanged.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_no_dependency_down_guard(ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_no_dependency_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_no_dependency_down_guard;
DROP TRIGGER cleardev_review_check_result_valid;
CREATE TRIGGER cleardev_review_check_result_valid BEFORE INSERT ON cleardev_review_check_results
WHEN EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=NEW.review_id AND check_id=NEW.check_id OR run_id=NEW.run_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_review_check_requests request
 JOIN cleardev_complex_execution_reviews review ON review.id=request.review_id
 WHERE request.review_id=NEW.review_id AND review.status='PENDING'
 AND NEW.run_id=NEW.review_id||':requested-check:'||NEW.check_id
 AND EXISTS(SELECT 1 FROM json_each(request.request_json,'$.checkIds') WHERE value=NEW.check_id)
 AND json_extract(NEW.result_json,'$.reviewId') IS NEW.review_id
 AND json_extract(NEW.result_json,'$.checkId') IS NEW.check_id
 AND json_extract(NEW.result_json,'$.proof.runId') IS NEW.run_id
 AND json_extract(NEW.result_json,'$.proof.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
 AND json_extract(NEW.result_json,'$.outcome') IN ('PASS','FAIL','TIMED_OUT','INFRA_ERROR')
 AND (
   (json_type(request.request_json,'$.projectExecution') IS NULL
    AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
    AND NEW.check_id IN('demo-backend','demo-api','demo-frontend','demo-integration'))
   OR EXISTS(
    SELECT 1 FROM cleardev_project_execution_admissions admission
    JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id AND attempt.execution_run_id=admission.execution_run_id
    JOIN json_each(admission.contract_json,'$.basis.checks') approved
    WHERE json_extract(request.request_json,'$.projectExecution') IS admission.contract_json
      AND json_extract(approved.value,'$.id') IS NEW.check_id
      AND json(json_extract(NEW.result_json,'$.proof.argv')) IS json(json_extract(approved.value,'$.argv'))
      AND (
       (json_extract(NEW.result_json,'$.outcome') IS 'INFRA_ERROR'
        AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
        AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
        AND json_extract(NEW.result_json,'$.proof.exitCode') IS -1)
       OR
       (json_type(NEW.result_json,'$.projectReceipt') IS 'object'
        AND json_extract(NEW.result_json,'$.projectReceipt.schemaVersion') IS 1
        AND json_extract(NEW.result_json,'$.projectReceipt.policy') IS 'PROJECT_CHECK_V1'
        AND json_extract(NEW.result_json,'$.projectReceipt.executionRunId') IS admission.execution_run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.contractSha256') IS admission.contract_sha256
        AND json_extract(NEW.result_json,'$.projectReceipt.checkRunId') IS NEW.run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.checkId') IS NEW.check_id
        AND json_extract(NEW.result_json,'$.projectReceipt.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
        AND json(json_extract(NEW.result_json,'$.projectReceipt.argv')) IS json(json_extract(approved.value,'$.argv'))
        AND json_extract(NEW.result_json,'$.projectReceipt.timeoutSeconds') IS json_extract(approved.value,'$.timeoutSeconds')
        AND json_extract(NEW.result_json,'$.projectReceipt.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') IS json_extract(NEW.result_json,'$.proof.imageId')
        AND length(json_extract(NEW.result_json,'$.projectReceipt.imageId'))=71
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') LIKE 'sha256:%'
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid'))=40
        AND length(json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.approvedArgvSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.outputSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.nodeVersion'))>0
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId') IS json_extract(NEW.result_json,'$.proof.sourceManifestId')
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid') IS json_extract(NEW.result_json,'$.proof.sourceTreeOid')
        AND json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId') IS json_extract(NEW.result_json,'$.proof.environmentId')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSha256') IS json_extract(NEW.result_json,'$.proof.outputSha256')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSummary') IS json_extract(NEW.result_json,'$.output')
        AND json_extract(NEW.result_json,'$.projectReceipt.outcome') IS json_extract(NEW.result_json,'$.outcome')
        AND json_extract(NEW.result_json,'$.projectReceipt.exitCode') IS json_extract(NEW.result_json,'$.proof.exitCode')
        AND json_extract(NEW.result_json,'$.projectReceipt.timedOut') IS json_extract(NEW.result_json,'$.proof.timedOut')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputTruncated') IS json_extract(NEW.result_json,'$.proof.truncated')
        AND (json_extract(approved.value,'$.argv[0]') IS NOT 'npm' OR (
          length(json_extract(NEW.result_json,'$.projectReceipt.npmVersion'))>0
          AND length(json_extract(NEW.result_json,'$.projectReceipt.packageJsonSha256'))=64
          AND length(json_extract(NEW.result_json,'$.projectReceipt.packageLockSha256'))=64
          AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyCacheKey'))=64
          AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyEnvironment'))=64
          AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyTreeSha256'))=64))
        AND (
         (json_extract(NEW.result_json,'$.outcome') IS 'PASS'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'true'
          AND json_extract(NEW.result_json,'$.proof.exitCode') IS 0
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND json_type(NEW.result_json,'$.proof.truncated') IS 'false')
         OR (json_extract(NEW.result_json,'$.outcome') IS 'FAIL'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND (json_extract(NEW.result_json,'$.proof.exitCode')<>0 OR json_type(NEW.result_json,'$.proof.truncated') IS 'true'))
         OR (json_extract(NEW.result_json,'$.outcome') IS 'TIMED_OUT'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'true')))
      )
   )
 )
) BEGIN SELECT RAISE(ABORT,'review check result must bind its exact versioned request and actual runtime receipt'); END;
-- +goose StatementEnd
