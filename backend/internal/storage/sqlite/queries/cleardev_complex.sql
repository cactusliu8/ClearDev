-- S04 complex-requirement planning facts. These queries stay in their own
-- file so S01/S02 readers cannot accidentally treat them as STANDARD facts.

-- name: InsertClearDevComplexRequirement :exec
INSERT INTO cleardev_complex_requirements (
    development_project_id, original_prd_text, original_prd_sha256,
    target_requirement_version_id, created_at
) VALUES (?, ?, ?, ?, ?);

-- name: GetClearDevComplexRequirement :one
SELECT *
FROM cleardev_complex_requirements
WHERE development_project_id = ?;

-- name: ListClearDevRunnableComplexFlows :many
SELECT requirement.id
FROM cleardev_development_projects AS requirement
JOIN cleardev_complex_requirements AS complex
  ON complex.development_project_id = requirement.id
WHERE requirement.cancelled_at IS NULL
ORDER BY requirement.id;

-- name: ListClearDevComplexRoleBindings :many
SELECT *
FROM cleardev_complex_role_bindings
WHERE development_project_id = ?
ORDER BY requested_at, id;

-- name: GetClearDevComplexRoleBinding :one
SELECT *
FROM cleardev_complex_role_bindings
WHERE id = ?;

-- name: InsertClearDevComplexRoleBinding :exec
INSERT INTO cleardev_complex_role_bindings (
    id, development_project_id, role, session_creation_idempotency_key,
    ao_session_id, status, reason_code, requested_at, bound_at, ended_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: BindClearDevComplexRoleBindingCAS :execrows
UPDATE cleardev_complex_role_bindings
SET ao_session_id = sqlc.arg(ao_session_id), status = 'BOUND',
    bound_at = sqlc.arg(bound_at), reason_code = ''
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: FailClearDevComplexRoleBindingCAS :execrows
UPDATE cleardev_complex_role_bindings
SET status = 'FAILED', reason_code = sqlc.arg(reason_code),
    ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: ListClearDevComplexAgentSteps :many
SELECT *
FROM cleardev_complex_agent_steps
WHERE role_binding_id = ?
ORDER BY requested_at, id;

-- name: GetClearDevComplexAgentStep :one
SELECT *
FROM cleardev_complex_agent_steps
WHERE id = ?;

-- name: InsertClearDevComplexAgentStep :exec
INSERT INTO cleardev_complex_agent_steps (
    id, role_binding_id, step_kind, request_id, client_message_id,
    prompt_sha256, send_status, turn_id, final_message_id, final_message_text,
    message_sha256, requested_at, sent_at, completed_at, failed_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexAgentStepSentCAS :execrows
UPDATE cleardev_complex_agent_steps
SET send_status = 'SENT', sent_at = sqlc.arg(sent_at)
WHERE id = sqlc.arg(id) AND send_status = 'PENDING';

-- name: SettleClearDevComplexAgentStepCAS :execrows
UPDATE cleardev_complex_agent_steps
SET send_status = 'SETTLED',
    turn_id = sqlc.arg(turn_id),
    final_message_id = sqlc.arg(final_message_id),
    final_message_text = sqlc.arg(final_message_text),
    message_sha256 = sqlc.arg(message_sha256),
    completed_at = sqlc.arg(completed_at),
    reason_code = ''
WHERE id = sqlc.arg(id) AND send_status = 'SENT';

-- name: FailClearDevComplexAgentStepCAS :execrows
UPDATE cleardev_complex_agent_steps
SET send_status = 'FAILED',
    reason_code = sqlc.arg(reason_code),
    failed_at = sqlc.arg(failed_at)
WHERE id = sqlc.arg(id) AND send_status IN ('PENDING', 'SENT');

-- name: ListClearDevComplexCompilationRequests :many
SELECT *
FROM cleardev_complex_compilation_requests
WHERE development_project_id = ?
ORDER BY clarification_round, created_at, id;

-- name: GetClearDevComplexCompilationRequest :one
SELECT *
FROM cleardev_complex_compilation_requests
WHERE id = ?;

-- name: InsertClearDevComplexCompilationRequest :exec
INSERT INTO cleardev_complex_compilation_requests (
    id, development_project_id, agent_step_id, clarification_round,
    compilation_context_sha256, additional_round_reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexClarificationQuestions :many
SELECT *
FROM cleardev_complex_clarification_questions
WHERE compilation_request_id = ?
ORDER BY ordinal, question_key;

-- name: InsertClearDevComplexClarificationQuestion :exec
INSERT INTO cleardev_complex_clarification_questions (
    compilation_request_id, question_key, text, reason, requirement_keys_json, ordinal
) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexClarificationAnswers :many
SELECT *
FROM cleardev_complex_clarification_answers
WHERE compilation_request_id = ?
ORDER BY created_at, question_key;

-- name: InsertClearDevComplexClarificationAnswer :exec
INSERT INTO cleardev_complex_clarification_answers (
    compilation_request_id, question_key, text, created_at
) VALUES (?, ?, ?, ?);

-- name: ListClearDevComplexCompilations :many
SELECT *
FROM cleardev_complex_compilations
WHERE development_project_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevComplexCompilation :exec
INSERT INTO cleardev_complex_compilations (
    id, development_project_id, compilation_request_id, agent_step_id, outcome,
    summary, normalized_requirement_json, compilation_sha256, turn_id,
    final_message_id, raw_message_text, raw_message_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexIDMaps :many
SELECT *
FROM cleardev_complex_id_maps
WHERE compilation_id = ?
ORDER BY kind, ordinal, stable_id;

-- name: InsertClearDevComplexIDMap :exec
INSERT INTO cleardev_complex_id_maps (
    compilation_id, kind, temporary_key, stable_id, ordinal
) VALUES (?, ?, ?, ?, ?);

-- name: ListClearDevComplexEngineeringPlans :many
SELECT *
FROM cleardev_complex_engineering_plans
WHERE development_project_id = ?
ORDER BY version;

-- name: InsertClearDevComplexEngineeringPlan :exec
INSERT INTO cleardev_complex_engineering_plans (
    id, planning_request_id, development_project_id, requirement_version_id,
    requirement_sha256, compilation_sha256, version, planner_role_binding_id,
    agent_step_id, turn_id, final_message_id, plan_json, plan_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexPlanReviews :many
SELECT *
FROM cleardev_complex_plan_reviews
WHERE plan_id IN (
    SELECT id FROM cleardev_complex_engineering_plans WHERE development_project_id = ?
)
ORDER BY created_at, id;

-- name: InsertClearDevComplexPlanReview :exec
INSERT INTO cleardev_complex_plan_reviews (
    id, plan_id, steward_role_binding_id, agent_step_id, review_request_id,
    verdict, reason_code, summary, findings_json, plan_sha256, turn_id,
    final_message_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CountClearDevStandardRoleBindingsForRequirement :one
SELECT COUNT(*)
FROM cleardev_standard_role_bindings
WHERE development_project_id = ?;

-- name: CountClearDevComplexValidReplans :one
SELECT COUNT(*)
FROM cleardev_complex_plan_reviews AS review
JOIN cleardev_complex_engineering_plans AS plan ON plan.id = review.plan_id
WHERE plan.requirement_version_id = ?
  AND review.verdict = 'REPLAN'
  AND review.plan_sha256 = plan.plan_sha256;

-- name: CountClearDevDevelopmentTasksForRequirement :one
SELECT COUNT(*)
FROM cleardev_work_items
WHERE development_project_id = ?;
