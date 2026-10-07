-- Keep historical installed-cache receipts distinct from fresh per-check installations.
-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_project_check_receipt_settle_valid;
CREATE TRIGGER cleardev_project_check_receipt_settle_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN NEW.status='SETTLED' AND NEW.result IN ('PASS','FAIL') AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
   AND json_valid(NEW.output_summary)
   AND json_extract(NEW.output_summary,'$.schemaVersion') IS 1
   AND ((json_extract(NEW.output_summary,'$.policy') IS 'PROJECT_CHECK_V1' AND json_extract(NEW.output_summary,'$.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436')
        OR (json_extract(NEW.output_summary,'$.policy') IS 'PROJECT_CHECK_FRESH_INSTALL_V2' AND json_extract(NEW.output_summary,'$.image') IS 'node@sha256:fbe64f0a038c6117d58d6a160c52994ee586cab9e36812802b564ac2d464bb24' AND json_type(NEW.output_summary,'$.dependencyCacheKey') IS NULL AND json_type(NEW.output_summary,'$.dependencyEnvironment') IS NULL AND json_type(NEW.output_summary,'$.dependencyTreeSha256') IS NULL))
   AND json_extract(NEW.output_summary,'$.executionRunId') IS spec.execution_run_id
   AND json_extract(NEW.output_summary,'$.contractSha256') IS admission.contract_sha256
   AND json_extract(NEW.output_summary,'$.checkRunId') IS NEW.id
   AND json_extract(NEW.output_summary,'$.checkId') IS spec.check_name
   AND json_extract(NEW.output_summary,'$.candidateSha') IS NEW.candidate_commit_sha
   AND json(json_extract(NEW.output_summary,'$.argv')) IS json(spec.argv_json)
   AND json_extract(NEW.output_summary,'$.timeoutSeconds') IS spec.timeout_seconds

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
    SELECT 1 FROM cleardev_planner_project_contracts admission
    JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id AND attempt.execution_run_id=admission.execution_run_id
    JOIN json_each(admission.contract_json,'$.basis.checks') approved
    WHERE replace(replace(replace(json_extract(request.request_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
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
        AND ((json_extract(NEW.result_json,'$.projectReceipt.policy') IS 'PROJECT_CHECK_V1' AND json_extract(NEW.result_json,'$.projectReceipt.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436')
        OR (json_extract(NEW.result_json,'$.projectReceipt.policy') IS 'PROJECT_CHECK_FRESH_INSTALL_V2' AND json_extract(NEW.result_json,'$.projectReceipt.image') IS 'node@sha256:fbe64f0a038c6117d58d6a160c52994ee586cab9e36812802b564ac2d464bb24' AND json_type(NEW.result_json,'$.projectReceipt.dependencyCacheKey') IS NULL AND json_type(NEW.result_json,'$.projectReceipt.dependencyEnvironment') IS NULL AND json_type(NEW.result_json,'$.projectReceipt.dependencyTreeSha256') IS NULL))
        AND json_extract(NEW.result_json,'$.projectReceipt.executionRunId') IS admission.execution_run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.contractSha256') IS admission.contract_sha256
        AND json_extract(NEW.result_json,'$.projectReceipt.checkRunId') IS NEW.run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.checkId') IS NEW.check_id
        AND json_extract(NEW.result_json,'$.projectReceipt.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
        AND json(json_extract(NEW.result_json,'$.projectReceipt.argv')) IS json(json_extract(approved.value,'$.argv'))
        AND json_extract(NEW.result_json,'$.projectReceipt.timeoutSeconds') IS json_extract(approved.value,'$.timeoutSeconds')

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
          AND ((json_extract(NEW.result_json,'$.projectReceipt.policy') IS 'PROJECT_CHECK_FRESH_INSTALL_V2' AND (json_type(NEW.result_json,'$.projectReceipt.packageLockSha256') IS NULL OR length(json_extract(NEW.result_json,'$.projectReceipt.packageLockSha256'))=64))
           OR (length(json_extract(NEW.result_json,'$.projectReceipt.packageLockSha256'))=64
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
-- +goose StatementBegin
DROP TRIGGER cleardev_project_check_receipt_settle_valid;
CREATE TRIGGER cleardev_project_check_receipt_settle_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN NEW.status='SETTLED' AND NEW.result IN ('PASS','FAIL') AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=spec.execution_run_id
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
    SELECT 1 FROM cleardev_planner_project_contracts admission
    JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id AND attempt.execution_run_id=admission.execution_run_id
    JOIN json_each(admission.contract_json,'$.basis.checks') approved
    WHERE replace(replace(replace(json_extract(request.request_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
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
