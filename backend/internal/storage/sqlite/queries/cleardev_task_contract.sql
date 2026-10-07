-- A validation is a deterministic control-plane admission, never an Agent review.
-- name: InsertClearDevComplexPlanValidation :exec
INSERT INTO cleardev_complex_plan_validations (
    plan_id, plan_sha256, policy, check_catalog_sha256, created_at
) VALUES (?, ?, ?, ?, ?);

-- name: GetClearDevComplexPlanValidation :one
SELECT * FROM cleardev_complex_plan_validations WHERE plan_id = ?;

-- name: ListClearDevComplexPlanValidations :many
SELECT validation.* FROM cleardev_complex_plan_validations AS validation
JOIN cleardev_complex_engineering_plans AS plan ON plan.id = validation.plan_id
WHERE plan.development_project_id = ? ORDER BY plan.version;

-- A confirmed specification may come from initial Steward compilation or the
-- existing human-authorized product direction process. Both remain immutable.
-- name: ListClearDevPlannerReadyCompilations :many
SELECT initial.normalized_requirement_json FROM cleardev_complex_compilations AS initial
WHERE initial.outcome = 'READY'
  AND initial.development_project_id = sqlc.arg(development_project_id)
  AND initial.compilation_sha256 = sqlc.arg(compilation_sha256)
  AND initial.normalized_requirement_json = sqlc.arg(normalized_requirement_json)
UNION ALL
SELECT direction.normalized_requirement_json FROM cleardev_direction_compilations AS direction
WHERE direction.outcome = 'READY'
  AND direction.development_project_id = sqlc.arg(development_project_id)
  AND direction.compilation_sha256 = sqlc.arg(compilation_sha256)
  AND direction.normalized_requirement_json = sqlc.arg(normalized_requirement_json);
