-- name: GetClearDevProductPlanDecision :one
SELECT * FROM cleardev_human_decision_requests WHERE decision_kind='CONFIRM_PRODUCT_PLAN'
AND development_project_id=sqlc.arg(product_id) AND json_extract(binding_json,'$.discussionId')=sqlc.arg(discussion_id);

-- name: GetClearDevApprovedProductPlan :one
SELECT * FROM cleardev_approved_product_plans WHERE discussion_id=?;

-- name: ListClearDevApprovedProductPlans :many
SELECT * FROM cleardev_approved_product_plans;

-- name: GetClearDevEffectiveStageContext :one
SELECT * FROM cleardev_effective_stage_contexts WHERE stage_id=?;

-- name: InsertClearDevProductStageSource :exec
INSERT INTO cleardev_product_stage_sources(stage_id,authorization_id,selection_json,selection_sha256,created_at) VALUES (?,?,?,?,?);

-- name: GetClearDevProductStageSource :one
SELECT * FROM cleardev_product_stage_sources WHERE stage_id=?;
