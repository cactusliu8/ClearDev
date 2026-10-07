-- name: CountClearDevRequirementFinalReviewReworks :one
SELECT count(*) FROM cleardev_requirement_final_reviews WHERE execution_run_id=? AND status='SETTLED' AND verdict='REWORK';

-- name: GetClearDevRequirementFinalReview :one
SELECT * FROM cleardev_requirement_final_reviews WHERE execution_run_id=? ORDER BY rowid DESC LIMIT 1;

-- name: GetLatestClearDevRequirementFinalReview :one
SELECT * FROM cleardev_requirement_final_reviews WHERE development_project_id=? ORDER BY rowid DESC LIMIT 1;

-- name: GetClearDevRequirementFinalReviewByID :one
SELECT * FROM cleardev_requirement_final_reviews WHERE id=?;

-- name: GetClearDevRequirementFinalReviewForStep :one
SELECT * FROM cleardev_requirement_final_reviews WHERE id||':step'=?;

-- name: InsertClearDevRequirementFinalReview :exec
INSERT INTO cleardev_requirement_final_reviews (
 id,execution_run_id,development_project_id,requirement_version_id,requirement_sha256,
 plan_id,plan_sha256,candidate_commit_sha,base_commit_sha,source_workspace_path,
 review_packet_json,review_packet_sha256,prompt_sha256,check_run_ids_json,status,created_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,'REQUESTED',?);

-- name: BindClearDevRequirementFinalReview :execrows
UPDATE cleardev_requirement_final_reviews SET ao_session_id=?,workspace_path=?,bound_at=?,status='PENDING'
WHERE id=? AND status='REQUESTED';

-- name: SendClearDevRequirementFinalReview :execrows
UPDATE cleardev_requirement_final_reviews SET sent_at=?,status='SENT'
WHERE id=? AND status='PENDING';

-- name: SettleClearDevRequirementFinalReview :execrows
UPDATE cleardev_requirement_final_reviews SET result_id=?,verdict=?,reason_code=?,summary=?,settled_at=?,status='SETTLED'
WHERE id=? AND status='SENT';

-- name: FailClearDevRequirementFinalReview :execrows
UPDATE cleardev_requirement_final_reviews SET reason_code=?,settled_at=?,status='FAILED'
WHERE id=? AND status IN ('REQUESTED','PENDING','SENT');

-- name: GetClearDevFinalReviewProviderResult :one
SELECT result.* FROM cleardev_agent_step_results AS result
JOIN cleardev_agent_step_attempts AS attempt ON attempt.id=result.attempt_id
JOIN cleardev_agent_step_result_parses AS parsed ON parsed.result_id=result.id
JOIN cleardev_requirement_final_reviews AS review ON attempt.logical_step_id=review.id||':step'
WHERE review.id=sqlc.arg(review_id) AND attempt.role_binding_id=review.id
 AND attempt.development_project_id=review.development_project_id
 AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
 AND attempt.ao_session_id=review.ao_session_id AND attempt.prompt_sha256=review.prompt_sha256
 AND result.turn_id=sqlc.arg(turn_id) AND result.final_message_id=sqlc.arg(final_message_id)
 AND parsed.conclusion='VALID'
ORDER BY attempt.attempt_number DESC,result.result_index DESC LIMIT 1;

-- name: CountClearDevRequirementFinalReviewSends :one
SELECT count(*) FROM cleardev_requirement_final_reviews AS review
JOIN cleardev_agent_step_attempts AS attempt ON attempt.logical_step_id=review.id||':step'
JOIN cleardev_agent_message_reservations AS message ON message.attempt_id=attempt.id AND message.client_message_id=attempt.client_message_id
JOIN cleardev_agent_attempt_events AS event ON event.attempt_id=attempt.id AND event.client_message_id=message.client_message_id
WHERE review.id=? AND attempt.role_binding_id=review.id
 AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
 AND attempt.development_project_id=review.development_project_id
 AND attempt.ao_session_id=review.ao_session_id AND attempt.prompt_sha256=review.prompt_sha256
 AND message.ao_session_id=review.ao_session_id AND message.prompt_sha256=review.prompt_sha256
 AND message.source IN ('ORIGINAL','RECOVERY_ORIGINAL') AND event.status='SENT' AND length(event.turn_id)>0;

-- name: GetClearDevFinalReviewSavedResult :one
SELECT result.* FROM cleardev_agent_step_results AS result
JOIN cleardev_requirement_final_reviews AS review ON review.result_id=result.id
WHERE review.id=?;

-- name: ListClearDevFinalReviewRecheckCandidates :many
SELECT * FROM cleardev_requirement_final_reviews r
WHERE json_extract(review_packet_json,'$.previousReview') IS NULL AND status='SETTLED' AND verdict='NEEDS_HUMAN'
 AND json_extract(review_packet_json,'$.attemptEvidence') IS NULL
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_runs run WHERE run.id=r.execution_run_id AND run.status='ACCEPTED' AND run.mode='STANDARD' AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1')
 AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews newer WHERE newer.execution_run_id=r.execution_run_id AND json_extract(newer.review_packet_json,'$.previousReview') IS NOT NULL);

-- name: InsertClearDevFinalReviewRecheck :exec
INSERT INTO cleardev_requirement_final_reviews (
 id,execution_run_id,development_project_id,requirement_version_id,requirement_sha256,
 plan_id,plan_sha256,candidate_commit_sha,base_commit_sha,source_workspace_path,
 review_packet_json,review_packet_sha256,prompt_sha256,check_run_ids_json,status,created_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,'REQUESTED',?);
-- name: ListClearDevRequirementFinalReviews :many
SELECT * FROM cleardev_requirement_final_reviews WHERE execution_run_id=? ORDER BY rowid;
