-- +goose Up
-- Reapplication after a no-history compatibility downgrade is idempotent.
DROP VIEW IF EXISTS cleardev_mail_review_check_sources;
DROP VIEW IF EXISTS cleardev_mail_effective_review_outcomes;
DROP VIEW IF EXISTS cleardev_mail_replacement_final_sources;
DROP VIEW IF EXISTS cleardev_mail_replacement_replies;
DROP VIEW IF EXISTS cleardev_mail_replacement_authorities;
DROP TRIGGER IF EXISTS cleardev_mail_replacement_result_exact;
-- The original failed review is never changed. These read-only relations join
-- exact authorized replacement attempts/results to that original candidate.
CREATE VIEW cleardev_mail_replacement_authorities AS
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
JOIN cleardev_bounded_mail_runs run ON run.id=attempt.execution_run_id AND run.id=request.execution_run_id
JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id AND candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_role_bindings binding ON binding.id=claim.operation_id||':reviewer'
WHERE json_extract(request.request_json,'$.logicalStepId')=review.agent_step_id
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
CREATE VIEW cleardev_mail_replacement_replies AS
SELECT source.*,a.id AS source_attempt_id,raw.id AS source_result_id,raw.raw_message_text AS reply_text
FROM cleardev_mail_replacement_authorities source
JOIN cleardev_agent_step_attempts a ON a.logical_step_id=source.request_step_id AND a.attempt_number=2
 AND a.role_binding_id=source.binding_id AND a.ao_session_id=source.session_id
 AND a.step_category='COMPLEX_EXECUTION' AND a.step_kind='LOCAL_REVIEW'
 AND a.prompt_sha256=source.prompt_sha AND a.trigger_failure_event_id=source.failure_event_id
JOIN cleardev_agent_step_results raw ON raw.attempt_id=a.id
JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=raw.id AND parsed.conclusion='VALID';
CREATE VIEW cleardev_mail_review_check_sources AS
SELECT review.id AS review_id,step.id AS request_step_id,review.reviewer_role_binding_id AS binding_id,
 '' AS recovery_id,'' AS source_attempt_id,'' AS source_result_id,step.final_message_text AS reply_text
FROM cleardev_complex_execution_reviews review JOIN cleardev_complex_execution_agent_steps step ON step.id=review.agent_step_id
WHERE review.status='PENDING' AND step.send_status='SETTLED'
UNION ALL
SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text FROM cleardev_mail_replacement_replies;
DROP TRIGGER cleardev_review_check_request_valid;
-- +goose StatementBegin
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
-- +goose StatementEnd
-- This relation covers direct replacement verdicts and separately budgeted
-- check-result replies. It never equates the source attempt with a later one.
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
-- +goose StatementBegin
CREATE TRIGGER cleardev_mail_replacement_result_exact BEFORE INSERT ON cleardev_replacement_review_results
WHEN EXISTS(SELECT 1 FROM cleardev_fixed_recovery_requests request JOIN cleardev_bounded_mail_runs run ON run.id=request.execution_run_id WHERE request.id=NEW.recovery_request_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_mail_replacement_final_sources source
 WHERE source.recovery_id=NEW.recovery_request_id AND source.review_id=NEW.original_review_id AND source.attempt_id=NEW.attempt_id AND source.result_id=NEW.result_id
 AND json_extract(source.reply_text,'$.verdict')=NEW.verdict
 AND json_extract(NEW.result_json,'$.recoveryRequestId')=NEW.recovery_request_id
 AND json_extract(NEW.result_json,'$.originalReviewId')=NEW.original_review_id
 AND json_extract(NEW.result_json,'$.attemptId')=NEW.attempt_id AND json_extract(NEW.result_json,'$.resultId')=NEW.result_id
 AND json_extract(NEW.result_json,'$.verdict')=NEW.verdict
 -- Go re-parses and compares the normalized summary at insert/read/completion.
 -- SQLite trim is not Unicode TrimSpace and must not reject valid wire text.
 AND json_type(NEW.result_json,'$.summary')='text'
 AND json_extract(NEW.result_json,'$.reasonCode')=json_extract(source.reply_text,'$.reasonCode'))
BEGIN SELECT RAISE(ABORT,'replacement result must reference its actual authorized final reply'); END;
-- +goose StatementEnd
CREATE VIEW cleardev_mail_effective_review_outcomes AS
SELECT id,task_attempt_id,candidate_commit_id,reviewer_role_binding_id,status,verdict FROM cleardev_complex_execution_reviews WHERE status='SETTLED'
UNION ALL
SELECT source.review_id,source.task_attempt_id,source.candidate_id,source.binding_id,'SETTLED',result.verdict
FROM cleardev_replacement_review_results result JOIN cleardev_mail_replacement_final_sources source
 ON source.recovery_id=result.recovery_request_id AND source.review_id=result.original_review_id AND source.attempt_id=result.attempt_id AND source.result_id=result.result_id;
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
DROP TRIGGER cleardev_mail_attempt_slot_insert;
-- +goose StatementBegin
CREATE TRIGGER cleardev_mail_attempt_slot_insert BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs r JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 JOIN cleardev_development_projects p ON p.id=r.development_project_id JOIN cleardev_contract_versions v ON v.id=r.requirement_version_id
 WHERE r.id=NEW.execution_run_id AND t.id=NEW.task_id AND r.status='ACCEPTED' AND p.cancelled_at IS NULL AND p.state<>'PAUSED'
 AND v.state='APPROVED' AND v.superseded_by_id IS NULL AND v.sha256=r.requirement_sha256 AND v.task_set_version=r.accepted_task_set_version
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR NEW.round<>(SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id)
 OR (NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1
 AND (a.status='REWORK' OR (NEW.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN' AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED'))))
 OR (NEW.attempt_kind='DEVELOPMENT' AND ((SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='DEVELOPMENT')>=3
 OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind<>'DEVELOPMENT')
 OR EXISTS(SELECT 1 FROM cleardev_mail_effective_review_outcomes v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='REVIEW_REPAIR' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='REVIEW_REPAIR')
 OR NOT EXISTS(SELECT 1 FROM cleardev_mail_effective_review_outcomes v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1 AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='HUMAN_EXTRA' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
 OR NOT EXISTS(SELECT 1 FROM cleardev_human_decision_requests h JOIN cleardev_complex_execution_task_attempts a ON a.id=json_extract(h.binding_json,'$.dispatchId')
 JOIN cleardev_candidate_commits c ON c.complex_execution_task_attempt_id=a.id JOIN cleardev_bounded_mail_runs r ON r.id=a.execution_run_id
 WHERE h.id=NEW.grant_request_id AND h.decision_kind='AUTHORIZE_MAIL_EXTRA_ATTEMPT' AND h.status='RESOLVED' AND h.decision='APPROVE'
 AND json_extract(h.binding_json,'$.executionRunId')=NEW.execution_run_id AND json_extract(h.binding_json,'$.taskId')=NEW.task_id
 AND json_extract(h.binding_json,'$.nextRound')=NEW.round AND json_extract(h.binding_json,'$.candidateSha')=c.commit_sha
 AND json_extract(h.binding_json,'$.planSha256')=r.plan_sha256 AND json_extract(h.binding_json,'$.requirementVersionSha256')=r.requirement_sha256
 AND a.round=NEW.round-1 AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED')))
BEGIN SELECT RAISE(ABORT,'mail attempt has no bounded automatic or exact human authorization'); END;
-- +goose StatementEnd
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_complex_execution_runs run ON run.id=task.execution_run_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=NEW.candidate_commit_id
 JOIN cleardev_complex_execution_reviews review ON review.task_attempt_id=attempt.id AND review.candidate_commit_id=candidate.id
 WHERE task.id=NEW.task_mapping_id AND attempt.id=NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id=attempt.id AND attempt.status='REVIEWING'
 AND ((NEW.replacement_recovery_id IS NULL AND NEW.replacement_attempt_id IS NULL AND NEW.replacement_result_id IS NULL AND review.status='SETTLED' AND review.verdict='PASS')
 OR EXISTS(
 SELECT 1 FROM cleardev_replacement_review_results replacement
 JOIN cleardev_fixed_recovery_requests request ON request.id=replacement.recovery_request_id
 JOIN cleardev_fixed_recovery_results result ON result.request_id=request.id AND result.outcome='PASS'
 JOIN cleardev_agent_step_attempts agent_attempt ON agent_attempt.id=replacement.attempt_id
 JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=replacement.result_id AND parsed.conclusion='VALID'
 WHERE replacement.recovery_request_id=NEW.replacement_recovery_id AND replacement.original_review_id=review.id AND replacement.attempt_id=NEW.replacement_attempt_id AND replacement.result_id=NEW.replacement_result_id AND replacement.verdict='PASS'
 AND request.execution_run_id=run.id AND json_extract(request.request_json,'$.candidateId')=candidate.id AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND agent_attempt.ao_session_id=json_extract(result.result_json,'$.sessionId')
 AND ((agent_attempt.attempt_number=2 AND agent_attempt.logical_step_id=review.agent_step_id AND NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id))
 OR EXISTS(SELECT 1 FROM cleardev_mail_replacement_final_sources source WHERE source.recovery_id=replacement.recovery_request_id AND source.review_id=review.id AND source.attempt_id=replacement.attempt_id AND source.result_id=replacement.result_id))
 ))
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs scope JOIN cleardev_complex_execution_check_runs scope_run ON scope_run.check_spec_id=scope.id WHERE scope_run.id=NEW.scope_check_run_id AND scope.task_mapping_id=task.id AND scope.check_kind='SCOPE' AND scope_run.task_attempt_id=attempt.id AND scope_run.candidate_commit_id=candidate.id AND scope_run.status='SETTLED' AND scope_run.result='PASS')
 AND review.id=NEW.review_id
 AND json_array_length(NEW.required_check_runs_json)=(SELECT count(*) FROM cleardev_complex_execution_check_specs WHERE task_mapping_id=task.id AND check_kind='REQUIRED_CHECK')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.required_check_runs_json) required_id WHERE typeof(required_id.value)<>'text' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN cleardev_complex_execution_check_specs required_spec ON required_spec.id=required_run.check_spec_id WHERE required_run.id=required_id.value AND required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs required_spec WHERE required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN json_each(NEW.required_check_runs_json) required_id ON required_id.value=required_run.id WHERE required_run.check_spec_id=required_spec.id AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs spec WHERE spec.task_mapping_id=task.id AND spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs check_run WHERE check_run.check_spec_id=spec.id AND check_run.task_attempt_id=attempt.id AND check_run.candidate_commit_id=candidate.id AND check_run.status='SETTLED' AND check_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
) BEGIN SELECT RAISE(ABORT,'cleardev complex execution verified candidate lacks current scope, checks, review, or gate'); END;
-- +goose StatementEnd

-- Keep the terminal-grant guard first even when multiple insert guards reject.
DROP TRIGGER cleardev_mail_attempt_after_extra_forbidden;
-- +goose StatementBegin
CREATE TRIGGER cleardev_mail_attempt_after_extra_forbidden BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
BEGIN SELECT RAISE(ABORT,'mail human extra attempt is terminal'); END;
-- +goose StatementEnd

-- +goose Down
-- No history is deleted. When no new authority exists, restore the prior
-- relations so older migrations cannot encounter dangling helper views.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_mail_replacement_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_mail_replacement_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_mail_replacement_authorities) THEN 0 ELSE 1 END;
DROP TABLE cleardev_mail_replacement_down_guard;
-- +goose StatementEnd
DROP TRIGGER cleardev_mail_replacement_result_exact;
DROP TRIGGER cleardev_review_check_request_valid;
DROP TRIGGER cleardev_mail_attempt_slot_insert;
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
DROP VIEW cleardev_mail_reviewer_rework_sources;
DROP VIEW cleardev_mail_effective_review_outcomes;
DROP VIEW cleardev_mail_review_check_sources;
DROP VIEW cleardev_mail_replacement_final_sources;
DROP VIEW cleardev_mail_replacement_replies;
DROP VIEW cleardev_mail_replacement_authorities;
CREATE VIEW cleardev_mail_reviewer_rework_sources AS
SELECT prior.id AS prior_binding_id,prior.execution_run_id,prior.task_mapping_id,
 prior.ao_session_id,prior.workspace_path,prior.session_creation_idempotency_key AS prior_session_key,
 old_candidate.commit_sha AS prior_sha,candidate.id AS candidate_id,candidate.commit_sha AS candidate_sha
FROM cleardev_complex_execution_role_bindings prior
JOIN cleardev_complex_execution_reviews review ON review.reviewer_role_binding_id=prior.id
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
-- +goose StatementBegin
CREATE TRIGGER cleardev_mail_attempt_slot_insert BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs r JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 JOIN cleardev_development_projects p ON p.id=r.development_project_id JOIN cleardev_contract_versions v ON v.id=r.requirement_version_id
 WHERE r.id=NEW.execution_run_id AND t.id=NEW.task_id AND r.status='ACCEPTED' AND p.cancelled_at IS NULL AND p.state<>'PAUSED'
 AND v.state='APPROVED' AND v.superseded_by_id IS NULL AND v.sha256=r.requirement_sha256 AND v.task_set_version=r.accepted_task_set_version
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR NEW.round<>(SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id)
 OR (NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1
 AND (a.status='REWORK' OR (NEW.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN' AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED'))))
 OR (NEW.attempt_kind='DEVELOPMENT' AND ((SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='DEVELOPMENT')>=3
 OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind<>'DEVELOPMENT')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='REVIEW_REPAIR' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='REVIEW_REPAIR')
 OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1 AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='HUMAN_EXTRA' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
 OR NOT EXISTS(SELECT 1 FROM cleardev_human_decision_requests h JOIN cleardev_complex_execution_task_attempts a ON a.id=json_extract(h.binding_json,'$.dispatchId')
 JOIN cleardev_candidate_commits c ON c.complex_execution_task_attempt_id=a.id JOIN cleardev_bounded_mail_runs r ON r.id=a.execution_run_id
 WHERE h.id=NEW.grant_request_id AND h.decision_kind='AUTHORIZE_MAIL_EXTRA_ATTEMPT' AND h.status='RESOLVED' AND h.decision='APPROVE'
 AND json_extract(h.binding_json,'$.executionRunId')=NEW.execution_run_id AND json_extract(h.binding_json,'$.taskId')=NEW.task_id
 AND json_extract(h.binding_json,'$.nextRound')=NEW.round AND json_extract(h.binding_json,'$.candidateSha')=c.commit_sha
 AND json_extract(h.binding_json,'$.planSha256')=r.plan_sha256 AND json_extract(h.binding_json,'$.requirementVersionSha256')=r.requirement_sha256
 AND a.round=NEW.round-1 AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED')))
BEGIN SELECT RAISE(ABORT,'mail attempt has no bounded automatic or exact human authorization'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_request_valid BEFORE INSERT ON cleardev_review_check_requests
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_reviews review
 JOIN cleardev_complex_execution_agent_steps step ON step.id=review.agent_step_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=review.reviewer_role_binding_id
 WHERE review.id=NEW.review_id AND review.status='PENDING' AND binding.status='BOUND'
 AND step.send_status='SETTLED' AND json_valid(step.final_message_text) AND json_extract(step.final_message_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
 AND run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED' AND project.cancelled_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND json_extract(NEW.request_json,'$.reviewId')=review.id AND json_extract(NEW.request_json,'$.requestStepId')=step.id
 AND json_extract(NEW.request_json,'$.candidateId')=candidate.id AND json_extract(NEW.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(NEW.request_json,'$.packetSha256')=review.review_packet_sha256
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN('demo-backend','demo-api','demo-frontend','demo-integration'))
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(step.final_message_text,'$.checkIds')
 AND NOT EXISTS(SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(step.final_message_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review check request must bind an exact pending mail review'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_complex_execution_runs run ON run.id=task.execution_run_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=NEW.candidate_commit_id
 JOIN cleardev_complex_execution_reviews review ON review.task_attempt_id=attempt.id AND review.candidate_commit_id=candidate.id
 WHERE task.id=NEW.task_mapping_id AND attempt.id=NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id=attempt.id AND attempt.status='REVIEWING'
 AND ((NEW.replacement_recovery_id IS NULL AND NEW.replacement_attempt_id IS NULL AND NEW.replacement_result_id IS NULL AND review.status='SETTLED' AND review.verdict='PASS')
 OR EXISTS(
 SELECT 1 FROM cleardev_replacement_review_results replacement
 JOIN cleardev_fixed_recovery_requests request ON request.id=replacement.recovery_request_id
 JOIN cleardev_fixed_recovery_results result ON result.request_id=request.id AND result.outcome='PASS'
 JOIN cleardev_agent_step_attempts agent_attempt ON agent_attempt.id=replacement.attempt_id AND agent_attempt.attempt_number=2
 JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=replacement.result_id AND parsed.conclusion='VALID'
 WHERE replacement.recovery_request_id=NEW.replacement_recovery_id AND replacement.original_review_id=review.id AND replacement.attempt_id=NEW.replacement_attempt_id AND replacement.result_id=NEW.replacement_result_id AND replacement.verdict='PASS'
 AND request.execution_run_id=run.id AND json_extract(request.request_json,'$.candidateId')=candidate.id AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND agent_attempt.logical_step_id=review.agent_step_id AND agent_attempt.ao_session_id=json_extract(result.result_json,'$.sessionId')
 ))
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs scope JOIN cleardev_complex_execution_check_runs scope_run ON scope_run.check_spec_id=scope.id WHERE scope_run.id=NEW.scope_check_run_id AND scope.task_mapping_id=task.id AND scope.check_kind='SCOPE' AND scope_run.task_attempt_id=attempt.id AND scope_run.candidate_commit_id=candidate.id AND scope_run.status='SETTLED' AND scope_run.result='PASS')
 AND review.id=NEW.review_id
 AND json_array_length(NEW.required_check_runs_json)=(SELECT count(*) FROM cleardev_complex_execution_check_specs WHERE task_mapping_id=task.id AND check_kind='REQUIRED_CHECK')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.required_check_runs_json) required_id WHERE typeof(required_id.value)<>'text' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN cleardev_complex_execution_check_specs required_spec ON required_spec.id=required_run.check_spec_id WHERE required_run.id=required_id.value AND required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs required_spec WHERE required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN json_each(NEW.required_check_runs_json) required_id ON required_id.value=required_run.id WHERE required_run.check_spec_id=required_spec.id AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs spec WHERE spec.task_mapping_id=task.id AND spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs check_run WHERE check_run.check_spec_id=spec.id AND check_run.task_attempt_id=attempt.id AND check_run.candidate_commit_id=candidate.id AND check_run.status='SETTLED' AND check_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
) BEGIN SELECT RAISE(ABORT,'cleardev complex execution verified candidate lacks current scope, checks, review, or gate'); END;
-- +goose StatementEnd
DROP TRIGGER cleardev_mail_attempt_after_extra_forbidden;
-- +goose StatementBegin
CREATE TRIGGER cleardev_mail_attempt_after_extra_forbidden BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
BEGIN SELECT RAISE(ABORT,'mail human extra attempt is terminal'); END;
-- +goose StatementEnd
