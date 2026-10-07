-- S12A8 logical-step attempts and immutable evidence.

-- name: InsertClearDevAgentStepAttempt :execrows
INSERT INTO cleardev_agent_step_attempts (
    id, development_project_id, logical_step_id, step_category, step_kind,
    attempt_number, role_binding_id, ao_session_id, client_message_id,
    prompt_sha256, trigger_failure_event_id, requested_at, created_at, requested_at_semantics
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(logical_step_id, attempt_number) DO NOTHING;

-- name: GetClearDevAgentStepAttempt :one
SELECT * FROM cleardev_agent_step_attempts
WHERE logical_step_id = ? AND attempt_number = ?;

-- name: InsertClearDevAgentAttemptEvent :execrows
INSERT INTO cleardev_agent_attempt_events (
    id, attempt_id, status, client_message_id, prompt_sha256, turn_id,
    turn_state, failure_category, retryable, retry_at, provider_error_code,
    error_summary, recorded_at
)
SELECT sqlc.arg(id), sqlc.arg(attempt_id), sqlc.arg(status),
       sqlc.arg(client_message_id), sqlc.arg(prompt_sha256), sqlc.arg(turn_id),
       sqlc.arg(turn_state), sqlc.arg(failure_category), sqlc.arg(retryable),
       sqlc.narg(retry_at), sqlc.arg(provider_error_code),
       sqlc.arg(error_summary), sqlc.arg(recorded_at)
WHERE NOT EXISTS (SELECT 1 FROM cleardev_agent_attempt_events WHERE id=sqlc.arg(id));

-- name: GetClearDevAgentAttemptEvent :one
SELECT * FROM cleardev_agent_attempt_events WHERE id = ?;

-- name: GetLatestClearDevAgentAttemptEventForAttempt :one
SELECT * FROM cleardev_agent_attempt_events
WHERE attempt_id = ?
ORDER BY rowid DESC
LIMIT 1;

-- name: InsertClearDevAgentStepResult :execrows
INSERT INTO cleardev_agent_step_results (
    id, attempt_id, result_index, source, client_message_id, turn_id,
    final_message_id, raw_message_text, raw_message_sha256, observed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(attempt_id, result_index) DO NOTHING;

-- name: GetClearDevAgentStepResult :one
SELECT * FROM cleardev_agent_step_results
WHERE attempt_id = ? AND result_index = ?;

-- name: InsertClearDevAgentStepResultParse :execrows
INSERT INTO cleardev_agent_step_result_parses (
    result_id, conclusion, error_summary, parsed_at
) VALUES (?, ?, ?, ?)
ON CONFLICT(result_id) DO NOTHING;

-- name: GetClearDevAgentStepResultParse :one
SELECT * FROM cleardev_agent_step_result_parses WHERE result_id = ?;

-- name: GetClearDevCompletedAgentResultEvent :one
SELECT * FROM cleardev_agent_attempt_events
WHERE attempt_id = ? AND client_message_id = ? AND turn_id = ?
  AND status = 'COMPLETED' AND turn_state = 'completed'
ORDER BY rowid DESC LIMIT 1;

-- name: GetClearDevBoundAgentSendEvent :one
SELECT * FROM cleardev_agent_attempt_events
WHERE attempt_id = ? AND client_message_id = ? AND turn_id = ?
  AND prompt_sha256 = ? AND status = ?
ORDER BY rowid DESC LIMIT 1;

-- name: CountClearDevUnsettledSessionTurns :one
SELECT count(*) FROM conversation_turns
WHERE handled_by_session_id = ? AND state NOT IN ('completed','failed','interrupted');

-- name: ListClearDevAgentStepAttempts :many
SELECT * FROM cleardev_agent_step_attempts
WHERE development_project_id = ?
ORDER BY requested_at, id;

-- name: ListClearDevAgentAttemptEvents :many
SELECT event.*
FROM cleardev_agent_attempt_events AS event
JOIN cleardev_agent_step_attempts AS attempt ON attempt.id = event.attempt_id
WHERE attempt.development_project_id = ?
ORDER BY event.rowid;

-- name: ListClearDevAgentStepResults :many
SELECT result.*
FROM cleardev_agent_step_results AS result
JOIN cleardev_agent_step_attempts AS attempt ON attempt.id = result.attempt_id
WHERE attempt.development_project_id = ?
ORDER BY result.observed_at, result.id;

-- name: ListClearDevAgentStepResultParses :many
SELECT parse.*
FROM cleardev_agent_step_result_parses AS parse
JOIN cleardev_agent_step_results AS result ON result.id = parse.result_id
JOIN cleardev_agent_step_attempts AS attempt ON attempt.id = result.attempt_id
WHERE attempt.development_project_id = ?
ORDER BY parse.parsed_at, parse.result_id;

-- name: ListClearDevAgentStepAttemptStates :many
SELECT * FROM cleardev_agent_step_attempts
WHERE development_project_id = ? AND logical_step_id = ?
ORDER BY attempt_number;

-- name: GetClearDevAgentAttemptState :one
SELECT * FROM cleardev_agent_step_attempts
WHERE development_project_id = ? AND id = ?;

-- name: ListLatestClearDevAgentAttemptStates :many
SELECT attempt.* FROM cleardev_agent_step_attempts AS attempt
WHERE attempt.development_project_id = ? AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_attempts AS newer
    WHERE newer.logical_step_id = attempt.logical_step_id AND newer.attempt_number > attempt.attempt_number
)
ORDER BY attempt.logical_step_id;

-- name: GetLatestClearDevAgentMessageEvent :one
SELECT * FROM cleardev_agent_attempt_events
WHERE attempt_id = ? AND client_message_id = ?
ORDER BY rowid DESC LIMIT 1;
