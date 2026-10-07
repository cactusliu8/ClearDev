-- S10 trusted-progress explanation requests. These rows never store overall phase.

-- name: InsertClearDevProgressExplanationRequest :exec
INSERT INTO cleardev_progress_explanation_requests (
    id, development_project_id, fact_summary_sha256, max_event_sequence,
    source_steward_session_id, continuation_of_session_id, ao_session_id,
    session_creation_idempotency_key, client_message_id, prompt_text, prompt_sha256,
    status, result_json, result_sha256, reason_code, created_at, sent_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'PENDING', NULL, NULL, '', ?, NULL, NULL);

-- name: GetClearDevProgressExplanationBySnapshot :one
SELECT *
FROM cleardev_progress_explanation_requests
WHERE development_project_id = ? AND fact_summary_sha256 = ?;

-- name: GetClearDevProgressExplanation :one
SELECT *
FROM cleardev_progress_explanation_requests
WHERE id = ?;

-- name: ListClearDevProgressExplanations :many
SELECT *
FROM cleardev_progress_explanation_requests
WHERE development_project_id = ?
ORDER BY created_at, id;

-- name: ListClearDevRunnableProgressExplanations :many
SELECT *
FROM cleardev_progress_explanation_requests
WHERE status IN ('PENDING', 'SENT')
ORDER BY created_at, id;

-- name: BindClearDevProgressExplanationSessionCAS :execrows
UPDATE cleardev_progress_explanation_requests
SET ao_session_id = sqlc.arg(ao_session_id),
    continuation_of_session_id = sqlc.narg(continuation_of_session_id)
WHERE id = sqlc.arg(id)
  AND status = 'PENDING'
  AND ao_session_id IS NULL;

-- name: MarkClearDevProgressExplanationSentCAS :execrows
UPDATE cleardev_progress_explanation_requests
SET status = 'SENT',
    sent_at = sqlc.arg(sent_at)
WHERE id = sqlc.arg(id)
  AND status = 'PENDING'
  AND ao_session_id IS NOT NULL;

-- name: SettleClearDevProgressExplanationCAS :execrows
UPDATE cleardev_progress_explanation_requests
SET status = 'SETTLED',
    result_json = sqlc.arg(result_json),
    result_sha256 = sqlc.arg(result_sha256),
    settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'SENT';

-- name: FailClearDevProgressExplanationCAS :execrows
UPDATE cleardev_progress_explanation_requests
SET status = 'FAILED',
    reason_code = sqlc.arg(reason_code),
    settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status IN ('PENDING', 'SENT');
