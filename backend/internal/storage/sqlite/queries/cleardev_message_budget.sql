-- name: GetClearDevMessageBudgetVersion :one
SELECT * FROM cleardev_message_budget_versions WHERE development_project_id=?;

-- name: GetClearDevAgentMessageReservation :one
SELECT * FROM cleardev_agent_message_reservations WHERE client_message_id=?;

-- name: ListClearDevAgentMessageReservations :many
SELECT * FROM cleardev_agent_message_reservations WHERE development_project_id=? ORDER BY client_message_id;

-- name: CountClearDevRoleMessages :one
SELECT count(*) FROM cleardev_agent_message_reservations WHERE budget_id=?;

-- name: InsertClearDevAgentMessageReservation :exec
INSERT INTO cleardev_agent_message_reservations(client_message_id,development_project_id,budget_version,logical_step_id,attempt_id,source,ao_session_id,prompt_sha256,budget_id,reserved_at)
VALUES(?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevAgentMessageConfirmation :one
SELECT * FROM cleardev_agent_message_confirmations WHERE client_message_id=?;

-- name: InsertClearDevAgentMessageConfirmation :exec
INSERT INTO cleardev_agent_message_confirmations(client_message_id,turn_id,confirmed_at) VALUES(?,?,?);

-- name: ListClearDevAgentMessageConfirmations :many
SELECT confirmation.* FROM cleardev_agent_message_confirmations AS confirmation
JOIN cleardev_agent_message_reservations AS reservation ON reservation.client_message_id=confirmation.client_message_id
WHERE reservation.development_project_id=?;

-- name: GetClearDevMessageRoleBudget :one
SELECT budget.* FROM cleardev_complex_exception_budgets AS budget
JOIN cleardev_complex_exception_budget_occupancies AS occupancy ON occupancy.budget_id=budget.id
WHERE occupancy.agent_step_id=?;

-- name: ListClearDevRequirementRoleBudgets :many
SELECT budget.* FROM cleardev_complex_exception_budgets AS budget
JOIN cleardev_complex_execution_runs AS run ON run.id=budget.execution_run_id
WHERE run.development_project_id=? ORDER BY budget.id;

-- name: GetClearDevMessageExecutionBinding :many
SELECT step.id, step.role_binding_id, binding.ao_session_id, binding.execution_run_id,
 step.step_kind, step.client_message_id, step.prompt_sha256, binding.role,
 run.development_project_id, 'COMPLEX_EXECUTION' AS category,
 CAST(COALESCE((SELECT task_mapping_id FROM cleardev_complex_execution_task_attempts WHERE agent_step_id=step.id),binding.task_mapping_id,'') AS TEXT) AS task_id
FROM cleardev_complex_execution_agent_steps AS step
JOIN cleardev_complex_execution_role_bindings AS binding ON binding.id=step.role_binding_id
JOIN cleardev_complex_execution_runs AS run ON run.id=binding.execution_run_id
WHERE step.id=sqlc.arg(step_id)
UNION ALL
SELECT step.id, COALESCE(step.role_binding_id,step.ondemand_binding_id), COALESCE(binding.ao_session_id,ondemand.ao_session_id), step.execution_run_id,
 step.step_kind, step.client_message_id, step.prompt_sha256,
 CASE step.step_kind WHEN 'SCOPE_EXPANSION_DECISION' THEN 'STEWARD_EXCEPTION' WHEN 'SPECIALIST_RESULT' THEN 'SPECIALIST' WHEN 'RECOVERY_RESULT' THEN 'RECOVERY' ELSE 'BUILDER' END,
 run.development_project_id, 'CONTROLLED_EXCEPTION',
 CAST(CASE step.step_kind WHEN 'RECOVERY_RESULT' THEN '' ELSE COALESCE(ondemand.complex_execution_task_id,(SELECT complex_execution_task_id FROM cleardev_complex_exception_scope_requests WHERE id=step.request_id),(SELECT task_mapping_id FROM cleardev_complex_execution_task_attempts WHERE id=step.request_id),'') END AS TEXT)
FROM cleardev_complex_exception_agent_steps AS step
LEFT JOIN cleardev_complex_execution_role_bindings AS binding ON binding.id=step.role_binding_id
LEFT JOIN cleardev_complex_exception_ondemand_bindings AS ondemand ON ondemand.id=step.ondemand_binding_id
JOIN cleardev_complex_execution_runs AS run ON run.id=step.execution_run_id
WHERE step.id=sqlc.arg(step_id);

-- name: GetClearDevAgentStepAttemptByID :one
SELECT * FROM cleardev_agent_step_attempts WHERE id=?;

-- name: ListClearDevStepMessageReservations :many
SELECT * FROM cleardev_agent_message_reservations WHERE logical_step_id=? ORDER BY client_message_id;

-- name: ListClearDevMessageBudgetStepIDs :many
SELECT attempt.logical_step_id AS id FROM cleardev_agent_step_attempts AS attempt WHERE attempt.development_project_id=sqlc.arg(requirement_id)
UNION
SELECT step.id FROM cleardev_standard_agent_steps AS step JOIN cleardev_standard_role_bindings AS binding ON binding.id=step.role_binding_id WHERE binding.development_project_id=sqlc.arg(requirement_id)
UNION
SELECT step.id FROM cleardev_complex_agent_steps AS step JOIN cleardev_complex_role_bindings AS binding ON binding.id=step.role_binding_id WHERE binding.development_project_id=sqlc.arg(requirement_id)
UNION
SELECT step.id FROM cleardev_direction_agent_steps AS step JOIN cleardev_complex_role_bindings AS binding ON binding.id=step.role_binding_id WHERE binding.development_project_id=sqlc.arg(requirement_id)
UNION
SELECT step.id FROM cleardev_complex_execution_agent_steps AS step JOIN cleardev_complex_execution_role_bindings AS binding ON binding.id=step.role_binding_id JOIN cleardev_complex_execution_runs AS run ON run.id=binding.execution_run_id WHERE run.development_project_id=sqlc.arg(requirement_id)
UNION
SELECT step.id FROM cleardev_complex_exception_agent_steps AS step JOIN cleardev_complex_execution_runs AS run ON run.id=step.execution_run_id WHERE run.development_project_id=sqlc.arg(requirement_id)
UNION
SELECT step.id FROM cleardev_complex_quick_agent_steps AS step JOIN cleardev_complex_quick_role_bindings AS binding ON binding.id=step.role_binding_id JOIN cleardev_complex_quick_runs AS run ON run.id=binding.quick_run_id WHERE run.development_project_id=sqlc.arg(requirement_id)
ORDER BY id;
