-- name: InsertClearDevProductSourcePreparation :exec
INSERT INTO cleardev_product_source_preparations
(id,product_id,previous_id,input_json,selection_json,branch,status,created_at,updated_at)
VALUES (?,?,?,?,?,?,'PENDING',?,?);

-- name: GetClearDevProductSourcePreparation :one
SELECT * FROM cleardev_product_source_preparations WHERE id=?;

-- name: GetLatestClearDevProductSourcePreparation :one
SELECT * FROM cleardev_product_source_preparations WHERE product_id=? ORDER BY rowid DESC LIMIT 1;

-- name: SetClearDevProductSourcePreparation :execrows
UPDATE cleardev_product_source_preparations SET status=sqlc.arg(status),failure=sqlc.arg(failure),
 prepared_json=sqlc.arg(prepared_json),updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) AND status=sqlc.arg(expected_status);

-- name: ListClearDevPendingProductSources :many
SELECT product_id FROM cleardev_product_source_preparations WHERE status IN ('PENDING','READY');
