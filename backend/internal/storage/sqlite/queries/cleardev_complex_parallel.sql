-- S07 complex PARALLEL execution facts.  These queries only expose the S07
-- batch, composition, and builder-rebase facts; task dispatches reuse the S06
-- attempt queries with their batch binding.

-- name: ListClearDevComplexExecutionBatches :many
SELECT * FROM cleardev_complex_execution_batches
WHERE execution_run_id = ? ORDER BY ordinal;

-- name: GetClearDevComplexExecutionBatch :one
SELECT * FROM cleardev_complex_execution_batches WHERE id = ?;

-- name: InsertClearDevComplexExecutionBatch :exec
INSERT INTO cleardev_complex_execution_batches (
    id, execution_run_id, ordinal, task_keys_json, common_base_sha, status, created_at, composed_at
) VALUES (?, ?, ?, ?, '', 'PENDING', ?, NULL);

-- name: ActivateClearDevComplexExecutionBatchCAS :execrows
UPDATE cleardev_complex_execution_batches
SET common_base_sha = sqlc.arg(common_base_sha), status = 'RUNNING'
WHERE id = sqlc.arg(id) AND status = 'PENDING' AND common_base_sha = '';

-- name: AdvanceClearDevComplexExecutionBatchCAS :execrows
UPDATE cleardev_complex_execution_batches
SET status = sqlc.arg(status), composed_at = sqlc.narg(composed_at)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);

-- name: ListClearDevComplexExecutionCompositions :many
SELECT * FROM cleardev_complex_execution_compositions
WHERE execution_run_id = ? ORDER BY created_at, id;

-- name: GetClearDevComplexExecutionComposition :one
SELECT * FROM cleardev_complex_execution_compositions WHERE id = ?;

-- name: GetClearDevComplexExecutionCompositionByBatch :one
SELECT * FROM cleardev_complex_execution_compositions WHERE batch_id = ?;

-- name: InsertClearDevComplexExecutionComposition :exec
INSERT INTO cleardev_complex_execution_compositions (
    id, execution_run_id, batch_id, request_id, input_base_sha,
    input_candidate_ids_json, input_candidate_shas_json,
    workspace_path, output_commit_sha, status, conflict_paths_json, reason_code,
    created_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, '', '', 'PENDING', '[]', '', ?, NULL);

-- name: StartClearDevComplexExecutionCompositionCAS :execrows
UPDATE cleardev_complex_execution_compositions
SET status = 'RUNNING'
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: SettleClearDevComplexExecutionCompositionCAS :execrows
UPDATE cleardev_complex_execution_compositions
SET status = sqlc.arg(status), workspace_path = sqlc.arg(workspace_path),
    output_commit_sha = sqlc.arg(output_commit_sha),
    conflict_paths_json = sqlc.arg(conflict_paths_json),
    reason_code = sqlc.arg(reason_code), settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'RUNNING';

-- name: RebaseClearDevComplexExecutionBuilderCAS :execrows
UPDATE cleardev_complex_execution_role_bindings
SET base_commit_sha = sqlc.arg(base_commit_sha)
WHERE id = sqlc.arg(id) AND role = 'BUILDER' AND status = 'BOUND'
  AND base_commit_sha <> sqlc.arg(base_commit_sha);
