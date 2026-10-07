-- S08 QUICK follow-up execution facts. These queries do not reuse the S06/S07
-- run rows; those stay unique per confirmed v2.

-- name: GetClearDevComplexQuickRun :one
SELECT * FROM cleardev_complex_quick_runs WHERE id = ?;

-- name: GetClearDevComplexQuickRunForRequirement :one
SELECT * FROM cleardev_complex_quick_runs
WHERE development_project_id = ?
ORDER BY requested_at DESC, id DESC
LIMIT 1;

-- name: ListClearDevRunnableComplexQuickExecutions :many
SELECT run.development_project_id
FROM cleardev_complex_quick_runs AS run
WHERE run.status IN ('PENDING', 'ACCEPTED')
  AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
ORDER BY run.requested_at, run.id;

-- name: InsertClearDevComplexQuickRun :exec
INSERT INTO cleardev_complex_quick_runs (
    id, development_project_id, requirement_version_id, requirement_sha256,
    source_execution_run_id, plan_id, plan_sha256, source_task_key, integration_base_sha,
    steward_role_binding_id, mode, selection_reason_code, expected_task_set_version,
    accepted_task_set_version, execution_package_json, execution_package_sha256,
    status, reason_code, requested_at, accepted_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AcceptClearDevComplexQuickRunCAS :execrows
UPDATE cleardev_complex_quick_runs
SET status = 'ACCEPTED', accepted_at = sqlc.arg(accepted_at), selection_reason_code = sqlc.arg(selection_reason_code),
    source_task_key = sqlc.arg(source_task_key)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: SettleClearDevComplexQuickRunCAS :execrows
UPDATE cleardev_complex_quick_runs
SET status = sqlc.arg(status), reason_code = sqlc.arg(reason_code), selection_reason_code = sqlc.arg(selection_reason_code), settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status IN ('PENDING', 'ACCEPTED');

-- name: ListClearDevComplexQuickRoleBindings :many
SELECT * FROM cleardev_complex_quick_role_bindings
WHERE quick_run_id = ? ORDER BY requested_at, id;

-- name: GetClearDevComplexQuickRoleBinding :one
SELECT * FROM cleardev_complex_quick_role_bindings WHERE id = ?;

-- name: InsertClearDevComplexQuickRoleBinding :exec
INSERT INTO cleardev_complex_quick_role_bindings (
    id, quick_run_id, role, source_complex_role_binding_id, continuation_of_role_binding_id,
    session_creation_idempotency_key, ao_session_id, workspace_path, base_commit_sha,
    status, reason_code, requested_at, bound_at, ended_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: BindClearDevComplexQuickRoleBindingCAS :execrows
UPDATE cleardev_complex_quick_role_bindings
SET ao_session_id = sqlc.arg(ao_session_id), workspace_path = sqlc.arg(workspace_path),
    base_commit_sha = sqlc.arg(base_commit_sha), status = 'BOUND', bound_at = sqlc.arg(bound_at), reason_code = ''
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: FailClearDevComplexQuickRoleBindingCAS :execrows
UPDATE cleardev_complex_quick_role_bindings
SET status = 'FAILED', reason_code = sqlc.arg(reason_code), ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: EndClearDevComplexQuickRoleBindingCAS :execrows
UPDATE cleardev_complex_quick_role_bindings
SET status = 'ENDED', reason_code = sqlc.arg(reason_code), ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'BOUND';

-- name: ListClearDevComplexQuickAgentSteps :many
SELECT step.* FROM cleardev_complex_quick_agent_steps AS step
JOIN cleardev_complex_quick_role_bindings AS binding ON binding.id = step.role_binding_id
WHERE binding.quick_run_id = ? ORDER BY step.requested_at, step.id;

-- name: GetClearDevComplexQuickAgentStep :one
SELECT * FROM cleardev_complex_quick_agent_steps WHERE id = ?;

-- name: InsertClearDevComplexQuickAgentStep :exec
INSERT INTO cleardev_complex_quick_agent_steps (
    id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256,
    send_status, turn_id, final_message_id, final_message_text, message_sha256,
    requested_at, sent_at, completed_at, failed_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexQuickAgentStepSentCAS :execrows
UPDATE cleardev_complex_quick_agent_steps SET send_status = 'SENT', sent_at = sqlc.arg(sent_at)
WHERE id = sqlc.arg(id) AND send_status = 'PENDING';

-- name: SettleClearDevComplexQuickAgentStepCAS :execrows
UPDATE cleardev_complex_quick_agent_steps
SET send_status = 'SETTLED', turn_id = sqlc.arg(turn_id), final_message_id = sqlc.arg(final_message_id),
    final_message_text = sqlc.arg(final_message_text), message_sha256 = sqlc.arg(message_sha256),
    completed_at = sqlc.arg(completed_at), reason_code = ''
WHERE id = sqlc.arg(id) AND send_status = 'SENT';

-- name: FailClearDevComplexQuickAgentStepCAS :execrows
UPDATE cleardev_complex_quick_agent_steps
SET send_status = 'FAILED', reason_code = sqlc.arg(reason_code), failed_at = sqlc.arg(failed_at)
WHERE id = sqlc.arg(id) AND send_status IN ('PENDING', 'SENT');

-- name: GetClearDevComplexQuickTaskMapping :one
SELECT * FROM cleardev_complex_quick_task_mappings WHERE id = ?;

-- name: GetClearDevComplexQuickTaskMappingForRun :one
SELECT * FROM cleardev_complex_quick_task_mappings WHERE quick_run_id = ?;

-- name: GetClearDevComplexQuickTaskByWorkItem :one
SELECT work_item.* FROM cleardev_work_items AS work_item
JOIN cleardev_complex_quick_task_mappings AS task ON task.work_item_id = work_item.id
WHERE task.id = ?;

-- name: InsertClearDevComplexQuickTaskMapping :exec
INSERT INTO cleardev_complex_quick_task_mappings (
    id, quick_run_id, plan_task_key, work_item_id, ordinal,
    task_packet_json, task_packet_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexQuickTaskAttempts :many
SELECT attempt.* FROM cleardev_complex_quick_task_attempts AS attempt
WHERE attempt.quick_run_id = ? ORDER BY attempt.round;

-- name: GetClearDevComplexQuickTaskAttempt :one
SELECT * FROM cleardev_complex_quick_task_attempts WHERE id = ?;

-- name: InsertClearDevComplexQuickTaskAttempt :exec
INSERT INTO cleardev_complex_quick_task_attempts (
    id, quick_run_id, task_mapping_id, builder_role_binding_id, agent_step_id, round,
    base_commit_sha, status, reason_code, dispatched_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: StartClearDevComplexQuickTaskAttemptCAS :execrows
UPDATE cleardev_complex_quick_task_attempts
SET status = 'RUNNING', dispatched_at = sqlc.arg(dispatched_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: AdvanceClearDevComplexQuickTaskAttemptCAS :execrows
UPDATE cleardev_complex_quick_task_attempts
SET status = sqlc.arg(status), reason_code = sqlc.arg(reason_code), settled_at = sqlc.narg(settled_at)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);

-- name: ListClearDevComplexQuickCheckSpecs :many
SELECT spec.* FROM cleardev_complex_quick_check_specs AS spec
LEFT JOIN cleardev_complex_quick_task_mappings AS task ON task.id = spec.task_mapping_id
WHERE spec.quick_run_id = ? ORDER BY spec.check_kind, spec.check_name;

-- name: InsertClearDevComplexQuickCheckSpec :exec
INSERT INTO cleardev_complex_quick_check_specs (
    id, quick_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexQuickCheckRuns :many
SELECT check_run.* FROM cleardev_complex_quick_check_runs AS check_run
JOIN cleardev_complex_quick_task_attempts AS attempt ON attempt.id = check_run.task_attempt_id
WHERE attempt.quick_run_id = ? ORDER BY attempt.round, check_run.created_at, check_run.id;

-- name: GetClearDevComplexQuickCheckRun :one
SELECT * FROM cleardev_complex_quick_check_runs WHERE id = ?;

-- name: InsertClearDevComplexQuickCheckRun :exec
INSERT INTO cleardev_complex_quick_check_runs (
    id, check_spec_id, task_attempt_id, candidate_commit_id, candidate_commit_sha,
    container_image_id, exit_code, status, timed_out, output_summary, output_sha256,
    changed_paths_json, result, created_at, started_at, settled_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexQuickCheckRunStartedCAS :execrows
UPDATE cleardev_complex_quick_check_runs
SET status = 'STARTED', started_at = sqlc.arg(started_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: SettleClearDevComplexQuickCheckRunCAS :execrows
UPDATE cleardev_complex_quick_check_runs
SET container_image_id = sqlc.narg(container_image_id), exit_code = sqlc.narg(exit_code),
    status = sqlc.arg(status), timed_out = sqlc.narg(timed_out), output_summary = sqlc.narg(output_summary),
    output_sha256 = sqlc.narg(output_sha256), changed_paths_json = sqlc.narg(changed_paths_json),
    result = sqlc.narg(result), settled_at = sqlc.arg(settled_at), reason_code = sqlc.arg(reason_code)
WHERE id = sqlc.arg(id) AND status = 'STARTED';

-- name: GetClearDevComplexQuickResult :one
SELECT * FROM cleardev_complex_quick_results WHERE quick_run_id = ?;

-- name: InsertClearDevComplexQuickResult :exec
INSERT INTO cleardev_complex_quick_results (
    id, quick_run_id, integration_candidate_id, completion_status,
    created_at, committing_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexQuickResultChecks :many
SELECT result_check.* FROM cleardev_complex_quick_result_checks AS result_check
WHERE result_check.result_id = ? ORDER BY result_check.check_run_id;

-- name: InsertClearDevComplexQuickResultCheck :exec
INSERT INTO cleardev_complex_quick_result_checks (result_id, check_run_id)
VALUES (?, ?);

-- name: StartClearDevComplexQuickCompletionCAS :execrows
UPDATE cleardev_complex_quick_results
SET completion_status = 'COMMITTING', committing_at = sqlc.arg(committing_at)
WHERE id = sqlc.arg(id) AND completion_status = 'PENDING';

-- name: InsertClearDevComplexQuickWorkItem :exec
INSERT INTO cleardev_work_items (
    id, development_project_id, contract_version_id, title, mode, state,
    paused_from_state, max_rework_count, rework_count, complex_quick_task_id,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertClearDevComplexQuickPermission :exec
INSERT INTO cleardev_path_permission_versions (
    id, work_item_id, version, write_paths, forbidden_paths,
    shared_paths_require_approval, generated_paths, complex_quick_task_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertClearDevComplexQuickRequiredCheck :exec
INSERT INTO cleardev_required_checks (
    id, work_item_id, name, check_kind, complex_quick_check_spec_id, created_at
) VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertClearDevComplexQuickCandidate :exec
INSERT INTO cleardev_candidate_commits (
    id, work_item_id, sequence, ao_session_id, permission_version_id,
    dispatch_id, base_commit_sha, complex_quick_task_attempt_id,
    complex_quick_base_commit_sha, commit_sha, created_at
) VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?);

-- name: GetClearDevComplexQuickCandidate :one
SELECT candidate.* FROM cleardev_candidate_commits AS candidate
WHERE candidate.complex_quick_task_attempt_id = ?
ORDER BY candidate.sequence DESC, candidate.id DESC LIMIT 1;

-- name: InsertClearDevComplexQuickIntegrationCandidate :exec
INSERT INTO cleardev_integration_candidates (
    id, development_project_id, sequence, ao_session_id, commit_sha,
    requirement_version_id, task_set_version, dispatch_id, source_candidate_commit_id,
    complex_quick_result_id, complex_quick_source_candidate_commit_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?);

-- name: InsertClearDevComplexQuickEvidence :exec
INSERT INTO cleardev_evidence (
    id, development_project_id, subject_type, subject_id, evidence_kind, evidence_key,
    result, candidate_commit_id, integration_candidate_id, commit_sha, source_type,
    source_ao_session_id, candidate_check_run_id, local_review_id,
    complex_quick_check_run_id, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?);
