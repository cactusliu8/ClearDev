-- Admission is supplied only by the backend after an explicit execution request.
-- The deferred run reference requires admission and run to commit together.
-- name: InsertClearDevProjectExecutionAdmission :exec
INSERT INTO cleardev_project_execution_admissions
(execution_run_id, stage_id, plan_id, contract_json, contract_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetClearDevProjectExecutionAdmission :one
SELECT * FROM cleardev_project_execution_admissions WHERE execution_run_id = ?;

-- name: GetClearDevProjectExecutionAdmissionForPlan :one
SELECT * FROM cleardev_project_execution_admissions WHERE plan_id = ?;

-- name: GetClearDevProjectExecutionSourcePlan :one
SELECT * FROM cleardev_complex_engineering_plans WHERE id = ?;

-- The same check evidence table is used by old and project execution policies.
-- name: GetClearDevProjectCheckSpec :one
SELECT * FROM cleardev_complex_execution_check_specs WHERE id = ?;
