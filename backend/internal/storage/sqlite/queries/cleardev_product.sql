-- name: InsertClearDevProductDiscussionContext :exec
INSERT INTO cleardev_product_discussion_contexts (discussion_id, protocol_version, selection_json, selection_sha256)
VALUES (?, ?, ?, ?);

-- name: GetClearDevProductDiscussionContext :one
SELECT * FROM cleardev_product_discussion_contexts WHERE discussion_id=?;

-- name: HasClearDevProductExecution :one
-- Historical/unfinished executions still freeze discussion. Only the exact
-- completed generic delivery defined by the versioned view is exempt.
SELECT EXISTS(
 SELECT 1 FROM cleardev_product_stages s
 JOIN cleardev_complex_execution_runs r ON r.development_project_id=s.development_requirement_id
 WHERE s.product_id=? AND NOT EXISTS(
   SELECT 1 FROM cleardev_completed_project_deliveries delivered
   WHERE delivered.execution_run_id=r.id AND delivered.stage_id=s.id
 )
);

-- name: InsertClearDevProductGoal :exec
INSERT INTO cleardev_product_goals (id, request_id, created_at, requested_execution) VALUES (?, ?, ?, ?);

-- name: GetClearDevProductGoal :one
SELECT p.id, p.request_id, r.ao_project_id, r.name, c.original_prd_text, p.created_at, p.requested_execution
FROM cleardev_product_goals p
JOIN cleardev_development_projects r ON r.id=p.id
JOIN cleardev_complex_requirements c ON c.development_project_id=p.id
WHERE p.id=?;

-- name: GetClearDevProductGoalByRequest :one
SELECT id FROM cleardev_product_goals WHERE request_id=?;

-- name: ListClearDevProductGoalIDs :many
SELECT p.id FROM cleardev_product_goals p
JOIN cleardev_development_projects r ON r.id=p.id
WHERE r.ao_project_id=? ORDER BY p.created_at,p.id;

-- name: InsertClearDevProductDiscussion :exec
INSERT INTO cleardev_product_discussions (id, product_id, ordinal, user_message, created_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetClearDevProductDiscussion :one
SELECT * FROM cleardev_product_discussions WHERE id=?;

-- name: ListClearDevProductDiscussions :many
SELECT * FROM cleardev_product_discussions WHERE product_id=? ORDER BY ordinal;

-- name: SettleClearDevProductDiscussion :execrows
UPDATE cleardev_product_discussions
SET agent_step_id=sqlc.arg(agent_step_id), result_json=sqlc.arg(result_json),
    result_sha256=sqlc.arg(result_sha256), settled_at=sqlc.arg(settled_at)
WHERE id=sqlc.arg(id) AND settled_at IS NULL;

-- name: FailClearDevProductDiscussion :execrows
UPDATE cleardev_product_discussions
SET failure_reason='PRODUCT_STEWARD_WORKSPACE_CHANGED', settled_at=sqlc.arg(settled_at)
WHERE id=sqlc.arg(id) AND product_id=sqlc.arg(product_id) AND settled_at IS NULL;

-- name: FailInvalidClearDevProductDiscussion :execrows
UPDATE cleardev_product_discussions
SET failure_reason='PRODUCT_DISCOVERY_INVALID', settled_at=sqlc.arg(settled_at)
WHERE id=sqlc.arg(id) AND product_id=sqlc.arg(product_id) AND settled_at IS NULL;

-- name: InsertClearDevProductStage :exec
INSERT INTO cleardev_product_stages (id, product_id, discussion_id, ordinal, definition_json, definition_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevProductStage :one
SELECT * FROM cleardev_product_stages WHERE id=?;

-- name: GetClearDevProductStageByRequirement :one
SELECT * FROM cleardev_product_stages WHERE development_requirement_id=?;

-- name: ListClearDevProductStages :many
SELECT * FROM cleardev_product_stages WHERE product_id=? ORDER BY created_at,discussion_id,ordinal;

-- name: BindClearDevProductStage :execrows
UPDATE cleardev_product_stages SET development_requirement_id=sqlc.arg(development_requirement_id), base_commit_sha=sqlc.arg(base_commit_sha)
WHERE id=sqlc.arg(id) AND development_requirement_id IS NULL;

-- name: GetClearDevProductStagePredecessorDelivery :one
SELECT i.commit_sha FROM cleardev_product_stages stage
JOIN cleardev_development_projects requirement ON requirement.id=stage.development_requirement_id AND requirement.cancelled_at IS NULL
JOIN cleardev_complex_execution_runs run ON run.development_project_id=requirement.id
JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id AND version.development_project_id=requirement.id AND version.state='APPROVED'
JOIN cleardev_complex_execution_results result ON result.execution_run_id=run.id AND result.completion_status='COMPLETED'
JOIN cleardev_integration_candidates i ON i.id=result.integration_candidate_id AND i.development_project_id=stage.development_requirement_id AND i.requirement_version_id=run.requirement_version_id
WHERE stage.discussion_id=sqlc.arg(discussion_id) AND stage.ordinal=sqlc.arg(ordinal)
  AND run.status='COMPLETED'
ORDER BY run.requested_at DESC LIMIT 1;
