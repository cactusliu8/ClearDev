-- S09 controlled-exception facts. These queries never write through the S01
-- generic fact methods.

-- name: ListClearDevComplexExceptionBudgets :many
SELECT * FROM cleardev_complex_exception_budgets
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevComplexExceptionBudget :exec
INSERT INTO cleardev_complex_exception_budgets (
    id, execution_run_id, complex_execution_task_id, role_kind, allowed_agent_types_json,
    model_selection, max_turns, used_turns, max_rework_count, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: OccupyClearDevComplexExceptionBudgetCAS :execrows
UPDATE cleardev_complex_exception_budgets
SET used_turns = used_turns + 1
WHERE id = sqlc.arg(id) AND used_turns < max_turns + authorized_extra_turns;

-- name: AuthorizeClearDevComplexExceptionBudgetExtra :execrows
UPDATE cleardev_complex_exception_budgets
SET authorized_extra_turns = authorized_extra_turns + 1
WHERE id = sqlc.arg(id) AND authorized_extra_turns < 2;

-- name: GetClearDevComplexExceptionBudgetOccupancyByStep :one
SELECT * FROM cleardev_complex_exception_budget_occupancies WHERE agent_step_id = ?;

-- name: GetClearDevComplexExceptionBudgetOccupancyByRoundKey :one
SELECT * FROM cleardev_complex_exception_budget_occupancies WHERE round_key = ? ORDER BY occupied_at, id LIMIT 1;

-- name: InsertClearDevComplexExceptionBudgetOccupancy :exec
INSERT INTO cleardev_complex_exception_budget_occupancies (id, budget_id, agent_step_id, round_key, occupied_at)
VALUES (?, ?, ?, ?, ?);

-- name: ListClearDevComplexExceptionScopeRequests :many
SELECT * FROM cleardev_complex_exception_scope_requests
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: GetClearDevComplexExceptionScopeRequest :one
SELECT * FROM cleardev_complex_exception_scope_requests WHERE id = ?;

-- name: InsertClearDevComplexExceptionScopeRequest :exec
INSERT INTO cleardev_complex_exception_scope_requests (
    id, execution_run_id, complex_execution_task_id, dispatch_id, round,
    requirement_version_id, requirement_sha256, plan_id, plan_sha256,
    requested_paths_json, agent_step_id, status, reason_code, created_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SettleClearDevComplexExceptionScopeRequestCAS :execrows
UPDATE cleardev_complex_exception_scope_requests
SET status = sqlc.arg(status), reason_code = sqlc.arg(reason_code), settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: ListClearDevComplexExceptionScopeDecisions :many
SELECT * FROM cleardev_complex_exception_scope_decisions
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevComplexExceptionScopeDecision :exec
INSERT INTO cleardev_complex_exception_scope_decisions (
    id, request_id, execution_run_id, requirement_version_id, requirement_sha256,
    plan_id, plan_sha256, paths_json, decision, reason_code, summary,
    steward_role_binding_id, agent_step_id, permission_version_id, control_accepted, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexExceptionGeneratedCommands :many
SELECT * FROM cleardev_complex_exception_generated_commands
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevComplexExceptionGeneratedCommand :exec
INSERT INTO cleardev_complex_exception_generated_commands (
    id, execution_run_id, complex_execution_task_id, command_id, argv_json,
    timeout_seconds, output_paths_json, image, command_spec_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexExceptionGeneratedProofs :many
SELECT * FROM cleardev_complex_exception_generated_proofs
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: GetClearDevComplexExceptionGeneratedProof :one
SELECT * FROM cleardev_complex_exception_generated_proofs WHERE id = ?;

-- name: InsertClearDevComplexExceptionGeneratedProof :exec
INSERT INTO cleardev_complex_exception_generated_proofs (
    id, execution_run_id, complex_execution_task_id, dispatch_id, candidate_commit_id,
    candidate_commit_sha, command_fact_id, container_image_id, output_sha256_json,
    status, result, reason_code, created_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexExceptionGeneratedProofStartedCAS :execrows
UPDATE cleardev_complex_exception_generated_proofs
SET status = 'STARTED'
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: SettleClearDevComplexExceptionGeneratedProofCAS :execrows
UPDATE cleardev_complex_exception_generated_proofs
SET container_image_id = sqlc.narg(container_image_id), output_sha256_json = sqlc.arg(output_sha256_json),
    status = sqlc.arg(status), result = sqlc.narg(result), reason_code = sqlc.arg(reason_code),
    settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'STARTED';

-- name: ListClearDevComplexExceptionPathLeases :many
SELECT * FROM cleardev_complex_exception_path_leases
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevComplexExceptionPathLease :exec
INSERT INTO cleardev_complex_exception_path_leases (
    id, execution_run_id, complex_execution_task_id, path, status, created_at, released_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ReleaseClearDevComplexExceptionPathLeaseCAS :execrows
UPDATE cleardev_complex_exception_path_leases
SET status = 'RELEASED', released_at = sqlc.arg(released_at)
WHERE id = sqlc.arg(id) AND status = 'HELD';

-- name: ListClearDevComplexExceptionOnDemandBindings :many
SELECT * FROM cleardev_complex_exception_ondemand_bindings
WHERE execution_run_id = ?
ORDER BY requested_at, id;

-- name: GetClearDevComplexExceptionOnDemandBinding :one
SELECT * FROM cleardev_complex_exception_ondemand_bindings WHERE id = ?;

-- name: InsertClearDevComplexExceptionOnDemandBinding :exec
INSERT INTO cleardev_complex_exception_ondemand_bindings (
    id, execution_run_id, complex_execution_task_id, mode, trigger_reason,
    session_creation_idempotency_key, ao_session_id, workspace_path, base_commit_sha,
    status, reason_code, binding_fingerprint, requested_at, bound_at, ended_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: BindClearDevComplexExceptionOnDemandCAS :execrows
UPDATE cleardev_complex_exception_ondemand_bindings
SET ao_session_id = sqlc.arg(ao_session_id), workspace_path = sqlc.arg(workspace_path),
    base_commit_sha = sqlc.arg(base_commit_sha), status = 'BOUND', bound_at = sqlc.arg(bound_at), reason_code = ''
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: FailClearDevComplexExceptionOnDemandCAS :execrows
UPDATE cleardev_complex_exception_ondemand_bindings
SET status = 'FAILED', reason_code = sqlc.arg(reason_code), ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: EndClearDevComplexExceptionOnDemandCAS :execrows
UPDATE cleardev_complex_exception_ondemand_bindings
SET status = 'ENDED', reason_code = sqlc.arg(reason_code), ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'BOUND';

-- name: ListClearDevComplexExceptionAgentSteps :many
SELECT * FROM cleardev_complex_exception_agent_steps
WHERE execution_run_id = ?
ORDER BY requested_at, id;

-- name: GetClearDevComplexExceptionAgentStep :one
SELECT * FROM cleardev_complex_exception_agent_steps WHERE id = ?;

-- name: InsertClearDevComplexExceptionAgentStep :exec
INSERT INTO cleardev_complex_exception_agent_steps (
    id, execution_run_id, role_binding_id, ondemand_binding_id, step_kind, request_id,
    client_message_id, prompt_sha256, send_status, turn_id, final_message_id,
    final_message_text, message_sha256, requested_at, sent_at, completed_at, failed_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexExceptionAgentStepSentCAS :execrows
UPDATE cleardev_complex_exception_agent_steps
SET send_status = 'SENT', sent_at = sqlc.arg(sent_at)
WHERE id = sqlc.arg(id) AND send_status = 'PENDING';

-- name: SettleClearDevComplexExceptionAgentStepCAS :execrows
UPDATE cleardev_complex_exception_agent_steps
SET send_status = 'SETTLED', turn_id = sqlc.arg(turn_id), final_message_id = sqlc.arg(final_message_id),
    final_message_text = sqlc.arg(final_message_text), message_sha256 = sqlc.arg(message_sha256),
    completed_at = sqlc.arg(completed_at), reason_code = ''
WHERE id = sqlc.arg(id) AND send_status = 'SENT';

-- name: FailClearDevComplexExceptionAgentStepCAS :execrows
UPDATE cleardev_complex_exception_agent_steps
SET send_status = 'FAILED', reason_code = sqlc.arg(reason_code), failed_at = sqlc.arg(failed_at)
WHERE id = sqlc.arg(id) AND send_status IN ('PENDING', 'SENT');

-- name: ListClearDevComplexExceptionSpecialistResults :many
SELECT * FROM cleardev_complex_exception_specialist_results
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevComplexExceptionSpecialistResult :exec
INSERT INTO cleardev_complex_exception_specialist_results (
    id, execution_run_id, complex_execution_task_id, ondemand_binding_id, agent_step_id,
    binding_fingerprint, outcome, constraints_json, reason_code, summary, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexExceptionSpecialistChecks :many
SELECT * FROM cleardev_complex_exception_specialist_checks
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: GetClearDevComplexExceptionSpecialistCheck :one
SELECT * FROM cleardev_complex_exception_specialist_checks WHERE id = ?;

-- name: InsertClearDevComplexExceptionSpecialistCheck :exec
INSERT INTO cleardev_complex_exception_specialist_checks (
    id, execution_run_id, complex_execution_task_id, specialist_result_id, check_id,
    argv_json, container_image_id, status, result, reason_code, created_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexExceptionSpecialistCheckStartedCAS :execrows
UPDATE cleardev_complex_exception_specialist_checks
SET status = 'STARTED'
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: SettleClearDevComplexExceptionSpecialistCheckCAS :execrows
UPDATE cleardev_complex_exception_specialist_checks
SET container_image_id = sqlc.narg(container_image_id), status = sqlc.arg(status),
    result = sqlc.narg(result), reason_code = sqlc.arg(reason_code), settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'STARTED';

-- name: ListClearDevComplexExceptionRecoveryActions :many
SELECT * FROM cleardev_complex_exception_recovery_actions
WHERE execution_run_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevComplexExceptionRecoveryAction :exec
INSERT INTO cleardev_complex_exception_recovery_actions (
    id, execution_run_id, complex_execution_task_id, ondemand_binding_id, agent_step_id,
    trigger_reason, trigger_fact_id, action, outcome, retry_check_run_id, reason_code, summary, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexExecutionPermissionVersions :many
SELECT perm.* FROM cleardev_path_permission_versions AS perm
JOIN cleardev_complex_execution_task_mappings AS task ON task.id = perm.complex_execution_task_id
WHERE task.execution_run_id = ?
ORDER BY task.ordinal, perm.version;
