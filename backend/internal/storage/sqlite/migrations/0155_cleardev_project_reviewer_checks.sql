-- +goose Up
-- Supplemental checks use the original Reviewer, budget, request/result ledger
-- and completion guards. V1 mail keeps its exact four-command interpretation.
-- V2 accepts only IDs from the already admitted immutable project catalog.
-- Historical mail replacement views remain unchanged. This additive view gives
-- only an admitted PROJECT_EXECUTION_V1 replacement Reviewer the same exact
-- immutable source projection needed by the shared supplemental-check ledger.
-- +goose StatementBegin
DROP VIEW IF EXISTS cleardev_project_replacement_final_sources;
DROP VIEW IF EXISTS cleardev_project_replacement_review_check_sources;
DROP VIEW IF EXISTS cleardev_project_replacement_replies;
DROP VIEW IF EXISTS cleardev_project_replacement_authorities;
CREATE VIEW cleardev_project_replacement_authorities AS
SELECT review.id AS review_id,run.id AS execution_run_id,attempt.task_mapping_id,attempt.id AS task_attempt_id,
 candidate.id AS candidate_id,candidate.commit_sha AS candidate_sha,review.review_packet_sha256 AS packet_sha,
 review.agent_step_id AS request_step_id,request.id AS recovery_id,binding.id AS binding_id,
 binding.ao_session_id AS session_id,binding.workspace_path,request.failure_event_id,
 json_extract(request.request_json,'$.promptSha256') AS prompt_sha
FROM cleardev_fixed_recovery_requests request
JOIN cleardev_fixed_recovery_claims claim ON claim.request_id=request.id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
JOIN cleardev_fixed_recovery_results outcome ON outcome.request_id=request.id AND outcome.outcome='PASS'
JOIN cleardev_complex_execution_reviews review ON review.id=json_extract(request.request_json,'$.reviewId')
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id AND run.id=request.execution_run_id
JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id AND candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_role_bindings binding ON binding.id=claim.operation_id||':reviewer'
WHERE run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED'
 AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(run.execution_package_json,'$.projectExecution') IS admission.contract_json
 AND json_extract(request.request_json,'$.logicalStepId')=review.agent_step_id
 AND json_extract(request.request_json,'$.dispatchId')=attempt.id
 AND json_extract(request.request_json,'$.taskId')=attempt.task_mapping_id
 AND json_extract(request.request_json,'$.roleBindingId')=review.reviewer_role_binding_id
 AND json_extract(request.request_json,'$.candidateId')=candidate.id
 AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND binding.execution_run_id=run.id AND binding.role='REVIEWER'
 AND binding.task_mapping_id=attempt.task_mapping_id AND binding.candidate_commit_id=candidate.id
 AND binding.continuation_of_role_binding_id=review.reviewer_role_binding_id
 AND binding.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND binding.base_commit_sha=candidate.commit_sha
 AND json_extract(outcome.result_json,'$.outcome')='PASS'
 AND json_extract(outcome.result_json,'$.roleBindingId')=binding.id
 AND json_extract(outcome.result_json,'$.sessionId')=binding.ao_session_id
 AND json_extract(outcome.result_json,'$.workspacePath')=binding.workspace_path
 AND binding.ao_session_id<>json_extract(request.request_json,'$.sessionId');

CREATE VIEW cleardev_project_replacement_replies AS
SELECT source.*,a.id AS source_attempt_id,raw.id AS source_result_id,raw.raw_message_text AS reply_text
FROM cleardev_project_replacement_authorities source
JOIN cleardev_agent_step_attempts a ON a.logical_step_id=source.request_step_id AND a.attempt_number=2
 AND a.role_binding_id=source.binding_id AND a.ao_session_id=source.session_id
 AND a.step_category='COMPLEX_EXECUTION' AND a.step_kind='LOCAL_REVIEW'
 AND a.prompt_sha256=source.prompt_sha AND a.trigger_failure_event_id=source.failure_event_id
JOIN cleardev_agent_step_results raw ON raw.attempt_id=a.id
JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=raw.id AND parsed.conclusion='VALID';

CREATE VIEW cleardev_project_replacement_review_check_sources AS
SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text
FROM cleardev_project_replacement_replies;

DROP TRIGGER cleardev_review_check_request_valid;
CREATE TRIGGER cleardev_review_check_request_valid BEFORE INSERT ON cleardev_review_check_requests
WHEN EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=NEW.review_id)
 OR NOT EXISTS (
 SELECT 1 FROM (
   SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text
   FROM cleardev_mail_review_check_sources
   UNION ALL
   SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text
   FROM cleardev_project_replacement_review_check_sources
 ) source
 JOIN cleardev_complex_execution_reviews review ON review.id=source.review_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=source.binding_id
 WHERE source.review_id=NEW.review_id AND review.status='PENDING' AND binding.status='BOUND'
 AND json_valid(source.reply_text) AND json_extract(source.reply_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND (
   (json_type(NEW.request_json,'$.projectExecution') IS NULL
    AND ((json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
          AND run.mode='STANDARD' AND run.fixed_builder_count=1)
      OR (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
          AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
          AND ((run.mode='STANDARD' AND run.fixed_builder_count=1) OR (run.mode='PARALLEL' AND run.fixed_builder_count=2))))
    AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN('demo-backend','demo-api','demo-frontend','demo-integration')))
   OR
   (run.mode='STANDARD' AND run.fixed_builder_count=1
    AND json_extract(source.reply_text,'$.schemaVersion') IS 2
    AND json_type(NEW.request_json,'$.projectExecution') IS 'object'
    AND EXISTS(
      SELECT 1 FROM cleardev_project_execution_admissions admission
      WHERE admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
        AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
        AND json_extract(run.execution_package_json,'$.projectExecution') IS admission.contract_json
        AND json_extract(NEW.request_json,'$.projectExecution') IS admission.contract_json
        AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') selected
          WHERE selected.type IS NOT 'text' OR NOT EXISTS(
            SELECT 1 FROM json_each(admission.contract_json,'$.basis.checks') approved
            WHERE json_extract(approved.value,'$.id') IS selected.value))))
 )
 AND run.status='ACCEPTED'
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_replacement_review_results WHERE original_review_id=review.id)
 AND json_extract(NEW.request_json,'$.reviewId')=review.id
 AND json_extract(NEW.request_json,'$.requestStepId')=source.request_step_id
 AND COALESCE(json_extract(NEW.request_json,'$.replacementRecoveryId'),'')=source.recovery_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestAttemptId'),'')=source.source_attempt_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestResultId'),'')=source.source_result_id
 AND json_extract(NEW.request_json,'$.candidateId')=candidate.id
 AND json_extract(NEW.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(NEW.request_json,'$.packetSha256')=review.review_packet_sha256
 AND json_type(NEW.request_json,'$.checkIds') IS 'array'
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(source.reply_text,'$.checkIds')
 AND NOT EXISTS(SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(source.reply_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review checks require exact original or authorized replacement reply and its versioned catalog'); END;

-- No table references the result table by foreign key. A transactional copy
-- recreates it under the SAME name; dependent views/settlement triggers retain
-- their original SQL and every historical receipt is copied before guards/CDC.
CREATE TEMP TABLE cleardev_review_results_backup_0155 AS SELECT * FROM cleardev_review_check_results;
DROP TABLE cleardev_review_check_results;
CREATE TABLE cleardev_review_check_results (
 review_id TEXT NOT NULL REFERENCES cleardev_review_check_requests(review_id),
 check_id TEXT NOT NULL CHECK(length(check_id) BETWEEN 1 AND 160),
 run_id TEXT NOT NULL UNIQUE,
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64),
 created_at TIMESTAMP NOT NULL,
 PRIMARY KEY(review_id,check_id)
);
INSERT INTO cleardev_review_check_results SELECT * FROM cleardev_review_results_backup_0155;
DROP TABLE cleardev_review_results_backup_0155;

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
CREATE TRIGGER cleardev_review_check_result_no_update BEFORE UPDATE ON cleardev_review_check_results BEGIN SELECT RAISE(ABORT,'review check results are immutable'); END;
CREATE TRIGGER cleardev_review_check_result_no_delete BEFORE DELETE ON cleardev_review_check_results BEGIN SELECT RAISE(ABORT,'review check results are append-only'); END;
CREATE TRIGGER cleardev_review_check_result_cdc AFTER INSERT ON cleardev_review_check_results BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'reviewCheckRequestId',NEW.review_id),NEW.created_at
 FROM cleardev_complex_execution_reviews review JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE review.id=NEW.review_id;
END;

-- A project replacement final reply can be either the exact attempt-two verdict
-- or the separately budgeted check-results reply. The original failed review
-- remains immutable in both cases.
CREATE VIEW cleardev_project_replacement_final_sources AS
SELECT source.recovery_id,source.review_id,source.execution_run_id,source.task_mapping_id,source.task_attempt_id,
 source.candidate_id,source.candidate_sha,source.packet_sha,source.binding_id,source.session_id,
 a.id AS attempt_id,raw.id AS result_id,raw.raw_message_text AS reply_text
FROM cleardev_project_replacement_authorities source
JOIN cleardev_agent_step_attempts a ON a.role_binding_id=source.binding_id AND a.ao_session_id=source.session_id
 AND a.step_category='COMPLEX_EXECUTION' AND a.step_kind='LOCAL_REVIEW'
JOIN cleardev_agent_step_results raw ON raw.attempt_id=a.id
JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=raw.id AND parsed.conclusion='VALID'
WHERE json_valid(raw.raw_message_text) AND json_extract(raw.raw_message_text,'$.kind')='LOCAL_REVIEW'
 AND (
   (NOT EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=source.review_id)
    AND a.logical_step_id=source.request_step_id AND a.attempt_number=2
    AND a.prompt_sha256=source.prompt_sha AND a.trigger_failure_event_id=source.failure_event_id)
   OR EXISTS(
    SELECT 1 FROM cleardev_review_check_requests request
    JOIN cleardev_project_replacement_replies original ON original.review_id=request.review_id
    JOIN cleardev_complex_execution_agent_steps final ON final.id=request.review_id||':check-results'
    WHERE request.review_id=source.review_id AND original.recovery_id=source.recovery_id
      AND json_extract(request.request_json,'$.replacementRecoveryId')=original.recovery_id
      AND json_extract(request.request_json,'$.requestAttemptId')=original.source_attempt_id
      AND json_extract(request.request_json,'$.requestResultId')=original.source_result_id
      AND json_type(request.request_json,'$.projectExecution') IS 'object'
      AND json_extract(original.reply_text,'$.kind')='REVIEW_CHECK_REQUEST'
      AND json_extract(original.reply_text,'$.schemaVersion') IS 2
      AND final.role_binding_id=source.binding_id AND final.request_id=final.id AND final.step_kind='LOCAL_REVIEW'
      AND a.logical_step_id=final.id AND a.prompt_sha256=final.prompt_sha256
      AND final.send_status='SETTLED' AND final.turn_id=raw.turn_id
      AND final.final_message_id=raw.final_message_id AND final.final_message_text=raw.raw_message_text
      AND (SELECT count(*) FROM cleardev_review_check_results WHERE review_id=source.review_id)=json_array_length(request.request_json,'$.checkIds')
      AND (json_extract(raw.raw_message_text,'$.verdict')<>'PASS'
        OR NOT EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=source.review_id AND json_extract(result_json,'$.outcome')<>'PASS'))
   )
 );

-- Keep every historical mail row byte-for-byte equivalent and add only the
-- admitted project final-source rows. Existing completion/verification guards
-- can therefore reuse the same read-only relation without a second workflow.
DROP VIEW cleardev_mail_replacement_final_sources;
CREATE VIEW cleardev_mail_replacement_final_sources AS
SELECT source.recovery_id,source.review_id,source.execution_run_id,source.task_mapping_id,source.task_attempt_id,
 source.candidate_id,source.candidate_sha,source.packet_sha,source.binding_id,source.session_id,
 a.id AS attempt_id,raw.id AS result_id,raw.raw_message_text AS reply_text
FROM cleardev_mail_replacement_authorities source
JOIN cleardev_agent_step_attempts a ON a.role_binding_id=source.binding_id AND a.ao_session_id=source.session_id AND a.step_category='COMPLEX_EXECUTION' AND a.step_kind='LOCAL_REVIEW'
JOIN cleardev_agent_step_results raw ON raw.attempt_id=a.id
JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=raw.id AND parsed.conclusion='VALID'
WHERE json_valid(raw.raw_message_text) AND json_extract(raw.raw_message_text,'$.kind')='LOCAL_REVIEW'
 AND (
 (NOT EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=source.review_id)
 AND a.logical_step_id=source.request_step_id AND a.attempt_number=2 AND a.prompt_sha256=source.prompt_sha AND a.trigger_failure_event_id=source.failure_event_id)
 OR EXISTS(
 SELECT 1 FROM cleardev_review_check_requests request
 JOIN cleardev_mail_replacement_replies original ON original.review_id=request.review_id
 JOIN cleardev_complex_execution_agent_steps final ON final.id=request.review_id||':check-results'
 WHERE request.review_id=source.review_id AND original.recovery_id=source.recovery_id
 AND json_extract(request.request_json,'$.replacementRecoveryId')=original.recovery_id
 AND json_extract(request.request_json,'$.requestAttemptId')=original.source_attempt_id
 AND json_extract(request.request_json,'$.requestResultId')=original.source_result_id
 AND json_extract(original.reply_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND final.role_binding_id=source.binding_id AND final.request_id=final.id AND final.step_kind='LOCAL_REVIEW'
 AND a.logical_step_id=final.id AND a.prompt_sha256=final.prompt_sha256
 AND final.send_status='SETTLED' AND final.turn_id=raw.turn_id AND final.final_message_id=raw.final_message_id AND final.final_message_text=raw.raw_message_text
 AND (SELECT count(*) FROM cleardev_review_check_results WHERE review_id=source.review_id)=json_array_length(request.request_json,'$.checkIds')
 AND (json_extract(raw.raw_message_text,'$.verdict')<>'PASS' OR NOT EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=source.review_id AND json_extract(result_json,'$.outcome')<>'PASS'))
 ))
UNION ALL
SELECT recovery_id,review_id,execution_run_id,task_mapping_id,task_attempt_id,
 candidate_id,candidate_sha,packet_sha,binding_id,session_id,attempt_id,result_id,reply_text
FROM cleardev_project_replacement_final_sources;

DROP TRIGGER IF EXISTS cleardev_project_replacement_result_exact;
CREATE TRIGGER cleardev_project_replacement_result_exact BEFORE INSERT ON cleardev_replacement_review_results
WHEN EXISTS(
 SELECT 1 FROM cleardev_fixed_recovery_requests request
 JOIN cleardev_complex_execution_runs run ON run.id=request.execution_run_id
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
 WHERE request.id=NEW.recovery_request_id AND run.mode='STANDARD' AND run.fixed_builder_count=1
   AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
   AND json_extract(run.execution_package_json,'$.projectExecution') IS admission.contract_json
)
AND NOT EXISTS(
 SELECT 1 FROM cleardev_project_replacement_final_sources source
 WHERE source.recovery_id=NEW.recovery_request_id AND source.review_id=NEW.original_review_id
   AND source.attempt_id=NEW.attempt_id AND source.result_id=NEW.result_id
   AND json_extract(source.reply_text,'$.verdict')=NEW.verdict
   AND json_extract(NEW.result_json,'$.recoveryRequestId')=NEW.recovery_request_id
   AND json_extract(NEW.result_json,'$.originalReviewId')=NEW.original_review_id
   AND json_extract(NEW.result_json,'$.attemptId')=NEW.attempt_id
   AND json_extract(NEW.result_json,'$.resultId')=NEW.result_id
   AND json_extract(NEW.result_json,'$.verdict')=NEW.verdict
   AND json_type(NEW.result_json,'$.summary')='text'
   AND json_extract(NEW.result_json,'$.reasonCode')=json_extract(source.reply_text,'$.reasonCode')
)
BEGIN SELECT RAISE(ABORT,'project replacement result must reference its exact authorized final reply'); END;
-- +goose StatementEnd

-- +goose Down
-- Never reinterpret or discard any admitted project or supplemental history.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_review_down_guard(ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_review_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_review_down_guard;
DROP TRIGGER IF EXISTS cleardev_project_replacement_result_exact;
DROP VIEW cleardev_mail_replacement_final_sources;
DROP VIEW IF EXISTS cleardev_project_replacement_final_sources;
DROP VIEW IF EXISTS cleardev_project_replacement_review_check_sources;
DROP VIEW IF EXISTS cleardev_project_replacement_replies;
DROP VIEW IF EXISTS cleardev_project_replacement_authorities;
-- Restore the exact pre-0155 mail relation before project tables disappear.
CREATE VIEW cleardev_mail_replacement_final_sources AS
SELECT source.recovery_id,source.review_id,source.execution_run_id,source.task_mapping_id,source.task_attempt_id,
 source.candidate_id,source.candidate_sha,source.packet_sha,source.binding_id,source.session_id,
 a.id AS attempt_id,raw.id AS result_id,raw.raw_message_text AS reply_text
FROM cleardev_mail_replacement_authorities source
JOIN cleardev_agent_step_attempts a ON a.role_binding_id=source.binding_id AND a.ao_session_id=source.session_id AND a.step_category='COMPLEX_EXECUTION' AND a.step_kind='LOCAL_REVIEW'
JOIN cleardev_agent_step_results raw ON raw.attempt_id=a.id
JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=raw.id AND parsed.conclusion='VALID'
WHERE json_valid(raw.raw_message_text) AND json_extract(raw.raw_message_text,'$.kind')='LOCAL_REVIEW'
 AND (
 (NOT EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=source.review_id)
 AND a.logical_step_id=source.request_step_id AND a.attempt_number=2 AND a.prompt_sha256=source.prompt_sha AND a.trigger_failure_event_id=source.failure_event_id)
 OR EXISTS(
 SELECT 1 FROM cleardev_review_check_requests request
 JOIN cleardev_mail_replacement_replies original ON original.review_id=request.review_id
 JOIN cleardev_complex_execution_agent_steps final ON final.id=request.review_id||':check-results'
 WHERE request.review_id=source.review_id AND original.recovery_id=source.recovery_id
 AND json_extract(request.request_json,'$.replacementRecoveryId')=original.recovery_id
 AND json_extract(request.request_json,'$.requestAttemptId')=original.source_attempt_id
 AND json_extract(request.request_json,'$.requestResultId')=original.source_result_id
 AND json_extract(original.reply_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND final.role_binding_id=source.binding_id AND final.request_id=final.id AND final.step_kind='LOCAL_REVIEW'
 AND a.logical_step_id=final.id AND a.prompt_sha256=final.prompt_sha256
 AND final.send_status='SETTLED' AND final.turn_id=raw.turn_id AND final.final_message_id=raw.final_message_id AND final.final_message_text=raw.raw_message_text
 AND (SELECT count(*) FROM cleardev_review_check_results WHERE review_id=source.review_id)=json_array_length(request.request_json,'$.checkIds')
 AND (json_extract(raw.raw_message_text,'$.verdict')<>'PASS' OR NOT EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=source.review_id AND json_extract(result_json,'$.outcome')<>'PASS'))
 ));
DROP TRIGGER cleardev_review_check_request_valid;
CREATE TRIGGER cleardev_review_check_request_valid BEFORE INSERT ON cleardev_review_check_requests
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_mail_review_check_sources source
 JOIN cleardev_complex_execution_reviews review ON review.id=source.review_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=source.binding_id
 WHERE source.review_id=NEW.review_id AND review.status='PENDING' AND binding.status='BOUND'
 AND json_valid(source.reply_text) AND json_extract(source.reply_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND ((json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1' AND run.mode='STANDARD' AND run.fixed_builder_count=1)
   OR (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2' AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
       AND ((run.mode='STANDARD' AND run.fixed_builder_count=1) OR (run.mode='PARALLEL' AND run.fixed_builder_count=2))))
 AND run.status='ACCEPTED' AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_replacement_review_results WHERE original_review_id=review.id)
 AND json_extract(NEW.request_json,'$.reviewId')=review.id
 AND json_extract(NEW.request_json,'$.requestStepId')=source.request_step_id
 AND COALESCE(json_extract(NEW.request_json,'$.replacementRecoveryId'),'')=source.recovery_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestAttemptId'),'')=source.source_attempt_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestResultId'),'')=source.source_result_id
 AND json_extract(NEW.request_json,'$.candidateId')=candidate.id
 AND json_extract(NEW.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(NEW.request_json,'$.packetSha256')=review.review_packet_sha256
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN('demo-backend','demo-api','demo-frontend','demo-integration'))
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(source.reply_text,'$.checkIds')
 AND NOT EXISTS(SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(source.reply_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review checks require exact original or authorized replacement reply'); END;

CREATE TEMP TABLE cleardev_review_results_backup_0155 AS SELECT * FROM cleardev_review_check_results;
DROP TABLE cleardev_review_check_results;
CREATE TABLE cleardev_review_check_results (
 review_id TEXT NOT NULL REFERENCES cleardev_review_check_requests(review_id),
 check_id TEXT NOT NULL CHECK(check_id IN ('demo-backend','demo-api','demo-frontend','demo-integration')),
 run_id TEXT NOT NULL UNIQUE,
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64),
 created_at TIMESTAMP NOT NULL,
 PRIMARY KEY(review_id,check_id)
);
INSERT INTO cleardev_review_check_results SELECT * FROM cleardev_review_results_backup_0155;
DROP TABLE cleardev_review_results_backup_0155;
CREATE TRIGGER cleardev_review_check_result_valid BEFORE INSERT ON cleardev_review_check_results
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_review_check_requests request JOIN cleardev_complex_execution_reviews review ON review.id=request.review_id
 WHERE request.review_id=NEW.review_id AND review.status='PENDING'
 AND NEW.run_id=NEW.review_id||':requested-check:'||NEW.check_id
 AND EXISTS(SELECT 1 FROM json_each(request.request_json,'$.checkIds') WHERE value=NEW.check_id)
 AND json_extract(NEW.result_json,'$.reviewId')=NEW.review_id
 AND json_extract(NEW.result_json,'$.checkId')=NEW.check_id
 AND json_extract(NEW.result_json,'$.proof.runId')=NEW.run_id
 AND json_extract(NEW.result_json,'$.proof.candidateSha')=json_extract(request.request_json,'$.candidateSha')
 AND json_extract(NEW.result_json,'$.outcome') IN ('PASS','FAIL','TIMED_OUT','INFRA_ERROR')
) BEGIN SELECT RAISE(ABORT,'review check result must bind its approved request'); END;
CREATE TRIGGER cleardev_review_check_result_no_update BEFORE UPDATE ON cleardev_review_check_results BEGIN SELECT RAISE(ABORT,'review check results are immutable'); END;
CREATE TRIGGER cleardev_review_check_result_no_delete BEFORE DELETE ON cleardev_review_check_results BEGIN SELECT RAISE(ABORT,'review check results are append-only'); END;
CREATE TRIGGER cleardev_review_check_result_cdc AFTER INSERT ON cleardev_review_check_results BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'reviewCheckRequestId',NEW.review_id),NEW.created_at
 FROM cleardev_complex_execution_reviews review JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE review.id=NEW.review_id;
END;
-- +goose StatementEnd
