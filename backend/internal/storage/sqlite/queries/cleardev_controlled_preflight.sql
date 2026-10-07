-- S12A6 controlled session preflight occupancy. Rows are append-only.

-- name: GetClearDevExecutionChoiceLocks :one
SELECT
    CAST(EXISTS(SELECT 1 FROM cleardev_development_projects p WHERE p.ao_project_id = sqlc.arg(project_id)) AS INTEGER) AS tool_locked,
    CAST(EXISTS(SELECT 1 FROM cleardev_controlled_preflights f WHERE f.ao_project_id = sqlc.arg(project_id) AND f.outcome = 'PASSED')
      OR EXISTS(SELECT 1 FROM cleardev_product_stages s JOIN cleardev_development_projects r ON r.id = s.development_requirement_id WHERE r.ao_project_id = sqlc.arg(project_id)) AS INTEGER) AS model_locked;

-- name: InsertClearDevControlledPreflight :exec
INSERT INTO cleardev_controlled_preflights (
    id, development_project_id, role_binding_id, ao_project_id,
    requested_model, resolved_model, provider, catalog_json, catalog_sha256,
    outcome, reason_code, retryable, retry_at, provider_error_code, error_summary, checked_at, capability_evidence
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetLatestClearDevControlledPreflight :one
SELECT *
FROM cleardev_controlled_preflights
WHERE development_project_id = ?
ORDER BY checked_at DESC, id DESC
LIMIT 1;

-- name: GetLatestClearDevControlledPreflightForBinding :one
SELECT *
FROM cleardev_controlled_preflights
WHERE role_binding_id = ?
ORDER BY checked_at DESC, id DESC
LIMIT 1;
