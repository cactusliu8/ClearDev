-- +goose Up
-- Extend the existing bounded mail-attempt/reviewer authority to the frozen
-- MAIL_INCREMENT_V2 contract. Historical MAIL_INCREMENT_V1 stays exactly
-- STANDARD/one-Builder; generic S07 PARALLEL runs are not admitted.
-- +goose StatementBegin
DROP VIEW cleardev_bounded_mail_runs;
CREATE VIEW cleardev_bounded_mail_runs AS
SELECT * FROM cleardev_complex_execution_runs
WHERE json_extract(execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
 AND (
   (mode='STANDARD' AND fixed_builder_count=1
    AND json_extract(execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1')
   OR
   (json_extract(execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
    AND ((mode='STANDARD' AND fixed_builder_count=1)
      OR (mode='PARALLEL' AND fixed_builder_count=2)))
 );

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
 AND (
   (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
    AND run.mode='STANDARD' AND run.fixed_builder_count=1)
   OR
   (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
    AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
    AND ((run.mode='STANDARD' AND run.fixed_builder_count=1)
      OR (run.mode='PARALLEL' AND run.fixed_builder_count=2)))
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
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN('demo-backend','demo-api','demo-frontend','demo-integration'))
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(source.reply_text,'$.checkIds')
 AND NOT EXISTS(SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(source.reply_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review checks require exact original or authorized replacement reply'); END;

DROP VIEW cleardev_mail_reviewer_rework_sources;
CREATE VIEW cleardev_mail_reviewer_rework_sources AS
SELECT prior.id AS prior_binding_id,prior.execution_run_id,prior.task_mapping_id,
 prior.ao_session_id,prior.workspace_path,prior.session_creation_idempotency_key AS prior_session_key,
 old_candidate.commit_sha AS prior_sha,candidate.id AS candidate_id,candidate.commit_sha AS candidate_sha
FROM cleardev_complex_execution_role_bindings prior
JOIN cleardev_mail_effective_review_outcomes review ON review.reviewer_role_binding_id=prior.id
JOIN cleardev_complex_execution_task_attempts old_attempt ON old_attempt.id=review.task_attempt_id
JOIN cleardev_candidate_commits old_candidate ON old_candidate.id=prior.candidate_commit_id
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.task_mapping_id=prior.task_mapping_id
JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_runs run ON run.id=prior.execution_run_id
JOIN cleardev_development_projects project ON project.id=run.development_project_id
JOIN sessions session ON session.id=prior.ao_session_id
WHERE prior.role='REVIEWER' AND prior.status='ENDED' AND review.status='SETTLED' AND review.verdict='REWORK'
 AND review.candidate_commit_id=old_candidate.id AND old_candidate.complex_execution_task_attempt_id=old_attempt.id
 AND old_attempt.execution_run_id=run.id AND old_attempt.task_mapping_id=prior.task_mapping_id
 AND attempt.execution_run_id=run.id AND attempt.base_commit_sha=old_attempt.base_commit_sha AND candidate.commit_sha<>old_candidate.commit_sha
 AND ((old_attempt.round=0 AND attempt.round=1 AND old_attempt.status='REWORK' AND session.creation_idempotency_key=prior.session_creation_idempotency_key)
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id) AND attempt.round>old_attempt.round
 AND (old_attempt.status='REWORK' OR (old_attempt.status='NEEDS_HUMAN' AND old_attempt.reason_code='MAIL_ATTEMPTS_EXHAUSTED'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews newer JOIN cleardev_complex_execution_task_attempts a ON a.id=newer.task_attempt_id WHERE a.task_mapping_id=prior.task_mapping_id AND a.round>old_attempt.round AND a.round<attempt.round)
 AND EXISTS(SELECT 1 FROM cleardev_mail_reviewer_ancestry chain JOIN cleardev_complex_execution_role_bindings root ON root.id=chain.ancestor WHERE chain.descendant=prior.id AND root.session_creation_idempotency_key=session.creation_idempotency_key)))
 AND run.status='ACCEPTED'
 AND (
   (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
    AND run.mode='STANDARD' AND run.fixed_builder_count=1
    AND json_extract(run.execution_package_json,'$.deliveryBaseSha')=old_attempt.base_commit_sha
    AND (SELECT count(*) FROM cleardev_complex_execution_task_mappings WHERE execution_run_id=run.id)=1)
   OR
   (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
    AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
    AND ((run.mode='STANDARD' AND run.fixed_builder_count=1)
      OR (run.mode='PARALLEL' AND run.fixed_builder_count=2))
    AND (SELECT count(*) FROM cleardev_complex_execution_task_mappings WHERE execution_run_id=run.id) BETWEEN 1 AND 3)
 )
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND session.project_id=project.ao_project_id AND session.kind='worker' AND session.harness='codex' AND session.session_mode='chat' AND session.permission_mode='auto'
 AND session.is_terminated=FALSE AND session.activity_state<>'exited' AND session.workspace_path=prior.workspace_path
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_role_bindings WHERE development_project_id=run.development_project_id AND ao_session_id=session.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE execution_run_id=run.id AND role<>'REVIEWER' AND (ao_session_id=session.id OR workspace_path=prior.workspace_path));
-- +goose StatementEnd

-- +goose Down
-- Never reinterpret or delete a V2 bounded-attempt run during downgrade.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_mail_v2_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_mail_v2_down_guard
SELECT CASE WHEN EXISTS(
 SELECT 1 FROM cleardev_complex_execution_runs
 WHERE json_extract(execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
   AND json_extract(execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
) THEN 0 ELSE 1 END;
DROP TABLE cleardev_mail_v2_down_guard;

DROP VIEW cleardev_bounded_mail_runs;
CREATE VIEW cleardev_bounded_mail_runs AS
 SELECT * FROM cleardev_complex_execution_runs WHERE mode='STANDARD' AND fixed_builder_count=1
 AND json_extract(execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
 AND json_extract(execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1';

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
 AND json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
 AND run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED'
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
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN('demo-backend','demo-api','demo-frontend','demo-integration'))
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(source.reply_text,'$.checkIds')
 AND NOT EXISTS(SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(source.reply_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review checks require exact original or authorized replacement reply'); END;

DROP VIEW cleardev_mail_reviewer_rework_sources;
CREATE VIEW cleardev_mail_reviewer_rework_sources AS
SELECT prior.id AS prior_binding_id,prior.execution_run_id,prior.task_mapping_id,
 prior.ao_session_id,prior.workspace_path,prior.session_creation_idempotency_key AS prior_session_key,
 old_candidate.commit_sha AS prior_sha,candidate.id AS candidate_id,candidate.commit_sha AS candidate_sha
FROM cleardev_complex_execution_role_bindings prior
JOIN cleardev_mail_effective_review_outcomes review ON review.reviewer_role_binding_id=prior.id
JOIN cleardev_complex_execution_task_attempts old_attempt ON old_attempt.id=review.task_attempt_id
JOIN cleardev_candidate_commits old_candidate ON old_candidate.id=prior.candidate_commit_id
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.task_mapping_id=prior.task_mapping_id
JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_runs run ON run.id=prior.execution_run_id
JOIN cleardev_development_projects project ON project.id=run.development_project_id
JOIN sessions session ON session.id=prior.ao_session_id
WHERE prior.role='REVIEWER' AND prior.status='ENDED' AND review.status='SETTLED' AND review.verdict='REWORK'
 AND review.candidate_commit_id=old_candidate.id AND old_candidate.complex_execution_task_attempt_id=old_attempt.id
 AND old_attempt.execution_run_id=run.id AND old_attempt.task_mapping_id=prior.task_mapping_id
 AND attempt.execution_run_id=run.id AND attempt.base_commit_sha=old_attempt.base_commit_sha AND candidate.commit_sha<>old_candidate.commit_sha
 AND ((old_attempt.round=0 AND attempt.round=1 AND old_attempt.status='REWORK' AND session.creation_idempotency_key=prior.session_creation_idempotency_key)
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id) AND attempt.round>old_attempt.round
 AND (old_attempt.status='REWORK' OR (old_attempt.status='NEEDS_HUMAN' AND old_attempt.reason_code='MAIL_ATTEMPTS_EXHAUSTED'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews newer JOIN cleardev_complex_execution_task_attempts a ON a.id=newer.task_attempt_id WHERE a.task_mapping_id=prior.task_mapping_id AND a.round>old_attempt.round AND a.round<attempt.round)
 AND EXISTS(SELECT 1 FROM cleardev_mail_reviewer_ancestry chain JOIN cleardev_complex_execution_role_bindings root ON root.id=chain.ancestor WHERE chain.descendant=prior.id AND root.session_creation_idempotency_key=session.creation_idempotency_key)))
 AND run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED'
 AND json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
 AND json_extract(run.execution_package_json,'$.deliveryBaseSha')=old_attempt.base_commit_sha
 AND (SELECT count(*) FROM cleardev_complex_execution_task_mappings WHERE execution_run_id=run.id)=1
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND session.project_id=project.ao_project_id AND session.kind='worker' AND session.harness='codex' AND session.session_mode='chat' AND session.permission_mode='auto'
 AND session.is_terminated=FALSE AND session.activity_state<>'exited' AND session.workspace_path=prior.workspace_path
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_role_bindings WHERE development_project_id=run.development_project_id AND ao_session_id=session.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE execution_run_id=run.id AND role<>'REVIEWER' AND (ao_session_id=session.id OR workspace_path=prior.workspace_path));
-- +goose StatementEnd
