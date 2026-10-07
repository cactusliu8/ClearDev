-- S12A5 mechanical parse-correction occupancy. One row per Agent step.

-- name: InsertClearDevParseCorrection :exec
INSERT INTO cleardev_parse_corrections (
    step_id, attempt_number, client_message_id, prompt_text, prompt_sha256, sent_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(step_id) DO NOTHING;

-- name: GetClearDevParseCorrection :one
SELECT *
FROM cleardev_parse_corrections
WHERE step_id = ?;
