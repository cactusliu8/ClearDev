-- S06 complex STANDARD execution facts.  These queries deliberately do not
-- expose S02 dispatches, which are one-task facts with different invariants.

-- name: GetClearDevComplexExecutionRun :one
SELECT * FROM cleardev_complex_execution_runs WHERE id = ?;

-- name: GetClearDevComplexExecutionRunForPlan :one
SELECT * FROM cleardev_complex_execution_runs
WHERE requirement_version_id = ? AND plan_id = ?;

-- name: ListClearDevComplexExecutionRuns :many
SELECT * FROM cleardev_complex_execution_runs
WHERE development_project_id = ? ORDER BY requested_at, id;

-- name: ListClearDevRunnableComplexExecutions :many
SELECT run.development_project_id
FROM cleardev_complex_execution_runs AS run
WHERE run.status IN ('PENDING', 'ACCEPTED')
  AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
ORDER BY run.requested_at, run.id;

-- name: InsertClearDevComplexExecutionRun :exec
INSERT INTO cleardev_complex_execution_runs (
    id, development_project_id, requirement_version_id, requirement_sha256,
    plan_id, plan_review_id, plan_sha256, steward_role_binding_id, mode,
    selection_reason_code, fixed_builder_count, expected_task_set_version, accepted_task_set_version,
    execution_package_json, execution_package_sha256, status, reason_code,
    requested_at, accepted_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AcceptClearDevComplexExecutionRunCAS :execrows
UPDATE cleardev_complex_execution_runs
SET status = 'ACCEPTED', accepted_at = sqlc.arg(accepted_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: SettleClearDevComplexExecutionRunCAS :execrows
UPDATE cleardev_complex_execution_runs
SET status = sqlc.arg(status), reason_code = sqlc.arg(reason_code), settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status IN ('PENDING', 'ACCEPTED');

-- name: CompleteClearDevComplexExecutionRunCAS :execrows
UPDATE cleardev_complex_execution_runs
SET status = 'COMPLETED', settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'ACCEPTED';

-- name: ListClearDevComplexExecutionRoleBindings :many
SELECT * FROM cleardev_complex_execution_role_bindings
WHERE execution_run_id = ? ORDER BY requested_at, id;

-- name: GetClearDevComplexExecutionRoleBinding :one
SELECT * FROM cleardev_complex_execution_role_bindings WHERE id = ?;

-- name: InsertClearDevComplexExecutionRoleBinding :exec
INSERT INTO cleardev_complex_execution_role_bindings (
    id, execution_run_id, role, source_complex_role_binding_id, continuation_of_role_binding_id,
    builder_slot, task_mapping_id, candidate_commit_id, session_creation_idempotency_key, ao_session_id,
    workspace_path, base_commit_sha, status, reason_code, requested_at, bound_at, ended_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: BindClearDevComplexExecutionRoleBindingCAS :execrows
UPDATE cleardev_complex_execution_role_bindings
SET ao_session_id = sqlc.arg(ao_session_id), workspace_path = sqlc.arg(workspace_path),
    base_commit_sha = sqlc.arg(base_commit_sha), status = 'BOUND', bound_at = sqlc.arg(bound_at), reason_code = ''
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: FailClearDevComplexExecutionRoleBindingCAS :execrows
UPDATE cleardev_complex_execution_role_bindings
SET status = 'FAILED', reason_code = sqlc.arg(reason_code), ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: EndClearDevComplexExecutionRoleBindingCAS :execrows
UPDATE cleardev_complex_execution_role_bindings
SET status = 'ENDED', reason_code = sqlc.arg(reason_code), ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'BOUND';

-- name: ListClearDevComplexExecutionAgentSteps :many
SELECT step.* FROM cleardev_complex_execution_agent_steps AS step
JOIN cleardev_complex_execution_role_bindings AS binding ON binding.id = step.role_binding_id
WHERE binding.execution_run_id = ? ORDER BY step.requested_at, step.id;

-- name: GetClearDevComplexExecutionAgentStep :one
SELECT * FROM cleardev_complex_execution_agent_steps WHERE id = ?;

-- name: InsertClearDevComplexExecutionAgentStep :exec
INSERT INTO cleardev_complex_execution_agent_steps (
    id, role_binding_id, step_kind, request_id, client_message_id, prompt_sha256,
    send_status, turn_id, final_message_id, final_message_text, message_sha256,
    requested_at, sent_at, completed_at, failed_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexExecutionAgentStepSentCAS :execrows
UPDATE cleardev_complex_execution_agent_steps SET send_status = 'SENT', sent_at = sqlc.arg(sent_at)
WHERE id = sqlc.arg(id) AND send_status = 'PENDING';

-- name: SettleClearDevComplexExecutionAgentStepCAS :execrows
UPDATE cleardev_complex_execution_agent_steps
SET send_status = 'SETTLED', turn_id = sqlc.arg(turn_id), final_message_id = sqlc.arg(final_message_id),
    final_message_text = sqlc.arg(final_message_text), message_sha256 = sqlc.arg(message_sha256),
    completed_at = sqlc.arg(completed_at), reason_code = ''
WHERE id = sqlc.arg(id) AND send_status = 'SENT';

-- name: FailClearDevComplexExecutionAgentStepCAS :execrows
UPDATE cleardev_complex_execution_agent_steps
SET send_status = 'FAILED', reason_code = sqlc.arg(reason_code), failed_at = sqlc.arg(failed_at)
WHERE id = sqlc.arg(id) AND send_status IN ('PENDING', 'SENT');

-- name: ListClearDevComplexExecutionTaskMappings :many
SELECT * FROM cleardev_complex_execution_task_mappings WHERE execution_run_id = ? ORDER BY ordinal, id;

-- name: GetClearDevComplexExecutionTaskMapping :one
SELECT * FROM cleardev_complex_execution_task_mappings WHERE id = ?;

-- name: GetClearDevComplexExecutionTaskMappingByWorkItem :one
SELECT * FROM cleardev_complex_execution_task_mappings WHERE work_item_id = ?;

-- name: RecoverClearDevComplexExecutionBuilderAttempt :execrows
-- The controlled recovery of one budget-exhausted builder round reopens its
-- blocked attempt as the rework successor; the stopped dispatch row and every
-- recorded failure stay untouched.
UPDATE cleardev_complex_execution_task_attempts
SET status = 'REWORK'
WHERE execution_run_id = sqlc.arg(execution_run_id)
  AND task_mapping_id = sqlc.arg(task_mapping_id)
  AND round = sqlc.arg(round)
  AND status = 'BLOCKED'
  AND reason_code = 'BUILDER_BUDGET_EXHAUSTED';

-- name: RecoverClearDevCoordinationRepairBuilderAttempt :execrows
-- The human coordination repair grant reopens one settled Builder-blocked
-- attempt as its rework successor; the stopped dispatch row and every
-- recorded failure stay untouched.
UPDATE cleardev_complex_execution_task_attempts
SET status = 'REWORK'
WHERE execution_run_id = sqlc.arg(execution_run_id)
  AND task_mapping_id = sqlc.arg(task_mapping_id)
  AND round = sqlc.arg(round)
  AND status = 'BLOCKED'
  AND reason_code = 'BUILDER_BLOCKED';

-- name: GetClearDevComplexExecutionTaskByWorkItem :one
SELECT work_item.* FROM cleardev_work_items AS work_item
JOIN cleardev_complex_execution_task_mappings AS task ON task.work_item_id = work_item.id
WHERE task.id = ?;

-- name: InsertClearDevComplexExecutionTaskMapping :exec
INSERT INTO cleardev_complex_execution_task_mappings (
    id, execution_run_id, plan_task_key, work_item_id, ordinal,
    task_packet_json, task_packet_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexExecutionDependencies :many
SELECT dependency.* FROM cleardev_complex_execution_dependencies AS dependency
JOIN cleardev_complex_execution_task_mappings AS task ON task.id = dependency.task_mapping_id
WHERE task.execution_run_id = ? ORDER BY task.ordinal, dependency.dependency_ordinal;

-- name: InsertClearDevComplexExecutionDependency :exec
INSERT INTO cleardev_complex_execution_dependencies (task_mapping_id, depends_on_mapping_id, dependency_ordinal)
VALUES (?, ?, ?);

-- name: ListClearDevComplexExecutionTaskAttempts :many
SELECT attempt.* FROM cleardev_complex_execution_task_attempts AS attempt
JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
WHERE task.execution_run_id = ? ORDER BY task.ordinal, attempt.round;

-- name: GetClearDevComplexExecutionTaskAttempt :one
SELECT * FROM cleardev_complex_execution_task_attempts WHERE id = ?;

-- name: InsertClearDevComplexExecutionTaskAttempt :exec
INSERT INTO cleardev_complex_execution_task_attempts (
    id, execution_run_id, task_mapping_id, builder_role_binding_id, agent_step_id, round,
    base_commit_sha, status, reason_code, dispatched_at, settled_at, batch_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: StartClearDevComplexExecutionTaskAttemptCAS :execrows
UPDATE cleardev_complex_execution_task_attempts
SET status = 'RUNNING', dispatched_at = sqlc.arg(dispatched_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: AdvanceClearDevComplexExecutionTaskAttemptCAS :execrows
UPDATE cleardev_complex_execution_task_attempts
SET status = sqlc.arg(status), reason_code = sqlc.arg(reason_code), settled_at = sqlc.narg(settled_at)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);

-- name: ListClearDevComplexExecutionCheckSpecs :many
SELECT spec.* FROM cleardev_complex_execution_check_specs AS spec
LEFT JOIN cleardev_complex_execution_task_mappings AS task ON task.id = spec.task_mapping_id
WHERE spec.execution_run_id = ? ORDER BY COALESCE(task.ordinal, 2147483647), spec.check_kind, spec.check_name;

-- name: InsertClearDevComplexExecutionCheckSpec :exec
INSERT INTO cleardev_complex_execution_check_specs (
    id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexExecutionCheckRuns :many
SELECT check_run.* FROM cleardev_complex_execution_check_runs AS check_run
JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = check_run.task_attempt_id
JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
WHERE task.execution_run_id = ? ORDER BY task.ordinal, attempt.round, check_run.created_at, check_run.id;

-- name: GetClearDevComplexExecutionCheckRun :one
SELECT * FROM cleardev_complex_execution_check_runs WHERE id = ?;

-- name: InsertClearDevComplexExecutionCheckRun :exec
INSERT INTO cleardev_complex_execution_check_runs (
    id, check_spec_id, task_attempt_id, candidate_commit_id, candidate_commit_sha,
    container_image_id, exit_code, status, timed_out, output_summary, output_sha256,
    changed_paths_json, result, created_at, started_at, settled_at, reason_code, retry_ordinal
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevComplexExecutionCheckRunStartedCAS :execrows
UPDATE cleardev_complex_execution_check_runs
SET status = 'STARTED', started_at = sqlc.arg(started_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: SettleClearDevComplexExecutionCheckRunCAS :execrows
UPDATE cleardev_complex_execution_check_runs
SET container_image_id = sqlc.narg(container_image_id), exit_code = sqlc.narg(exit_code),
    status = sqlc.arg(status), timed_out = sqlc.narg(timed_out), output_summary = sqlc.narg(output_summary),
    output_sha256 = sqlc.narg(output_sha256), changed_paths_json = sqlc.narg(changed_paths_json),
    result = sqlc.narg(result), settled_at = sqlc.arg(settled_at), reason_code = sqlc.arg(reason_code)
WHERE id = sqlc.arg(id) AND status = 'STARTED';

-- name: ListClearDevComplexExecutionReviews :many
SELECT review.* FROM cleardev_complex_execution_reviews AS review
JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = review.task_attempt_id
JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
WHERE task.execution_run_id = ? ORDER BY task.ordinal, attempt.round, review.created_at;

-- name: GetClearDevComplexExecutionReview :one
SELECT * FROM cleardev_complex_execution_reviews WHERE id = ?;

-- name: InsertClearDevComplexExecutionReview :exec
INSERT INTO cleardev_complex_execution_reviews (
    id, task_attempt_id, candidate_commit_id, reviewer_role_binding_id, agent_step_id,
    review_packet_json, review_packet_sha256, candidate_worktree_path, status, turn_id,
    final_message_id, verdict, reason_code, summary, created_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SettleClearDevComplexExecutionReviewCAS :execrows
UPDATE cleardev_complex_execution_reviews
SET status = sqlc.arg(status), turn_id = sqlc.narg(turn_id), final_message_id = sqlc.narg(final_message_id),
    verdict = sqlc.narg(verdict), reason_code = sqlc.arg(reason_code), summary = sqlc.narg(summary),
    settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: FailClearDevComplexExecutionReviewCAS :execrows
UPDATE cleardev_complex_execution_reviews
SET status = 'FAILED', reason_code = sqlc.arg(reason_code), settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: ListClearDevComplexExecutionVerifiedCandidates :many
SELECT verified.* FROM cleardev_complex_execution_verified_candidates AS verified
JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
WHERE task.execution_run_id = ? ORDER BY task.ordinal;

-- name: InsertClearDevComplexExecutionVerifiedCandidate :exec
INSERT INTO cleardev_complex_execution_verified_candidates (
    id, task_mapping_id, task_attempt_id, candidate_commit_id, scope_check_run_id,
    required_check_runs_json, review_id, verified_at, replacement_recovery_id, replacement_attempt_id, replacement_result_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevComplexExecutionResult :one
SELECT * FROM cleardev_complex_execution_results WHERE execution_run_id = ?;

-- name: InsertClearDevComplexExecutionResult :exec
INSERT INTO cleardev_complex_execution_results (
    id, execution_run_id, integration_candidate_id, completion_status,
    created_at, committing_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevComplexExecutionResultChecks :many
SELECT result_check.* FROM cleardev_complex_execution_result_checks AS result_check
WHERE result_check.result_id = ? ORDER BY result_check.check_run_id;

-- name: InsertClearDevComplexExecutionResultCheck :exec
INSERT INTO cleardev_complex_execution_result_checks (result_id, check_run_id)
VALUES (?, ?);

-- name: StartClearDevComplexExecutionCompletionCAS :execrows
UPDATE cleardev_complex_execution_results
SET completion_status = 'COMMITTING', committing_at = sqlc.arg(committing_at)
WHERE id = sqlc.arg(id) AND completion_status = 'PENDING';

-- name: CompleteClearDevComplexExecutionResultCAS :execrows
UPDATE cleardev_complex_execution_results
SET completion_status = 'COMPLETED', completed_at = sqlc.arg(completed_at)
WHERE id = sqlc.arg(id) AND completion_status = 'COMMITTING';

-- name: CountClearDevComplexExecutionIncompleteDependencies :one
SELECT COUNT(*)
FROM cleardev_complex_execution_dependencies AS dependency
JOIN cleardev_complex_execution_task_mappings AS prerequisite ON prerequisite.id = dependency.depends_on_mapping_id
LEFT JOIN cleardev_complex_execution_verified_candidates AS verified ON verified.task_mapping_id = prerequisite.id
WHERE dependency.task_mapping_id = ? AND verified.id IS NULL;

-- name: InsertClearDevComplexExecutionWorkItem :exec
INSERT INTO cleardev_work_items (
    id, development_project_id, contract_version_id, title, mode, state,
    paused_from_state, max_rework_count, rework_count, complex_execution_task_id,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertClearDevComplexExecutionPermission :exec
INSERT INTO cleardev_path_permission_versions (
    id, work_item_id, version, write_paths, forbidden_paths,
    shared_paths_require_approval, generated_paths, complex_execution_task_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertClearDevComplexExecutionRequiredCheck :exec
INSERT INTO cleardev_required_checks (
    id, work_item_id, name, check_kind, complex_execution_check_spec_id, created_at
) VALUES (?, ?, ?, ?, ?, ?);

-- name: InsertClearDevComplexExecutionCandidate :exec
INSERT INTO cleardev_candidate_commits (
    id, work_item_id, sequence, ao_session_id, permission_version_id,
    dispatch_id, base_commit_sha, complex_execution_task_attempt_id,
    complex_execution_base_commit_sha, commit_sha, created_at
) VALUES (?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?);

-- name: GetClearDevComplexExecutionCandidate :one
SELECT candidate.* FROM cleardev_candidate_commits AS candidate
WHERE candidate.complex_execution_task_attempt_id = ?
ORDER BY candidate.sequence DESC, candidate.id DESC LIMIT 1;

-- name: InsertClearDevComplexExecutionIntegrationCandidate :exec
INSERT INTO cleardev_integration_candidates (
    id, development_project_id, sequence, ao_session_id, commit_sha,
    requirement_version_id, task_set_version, dispatch_id, source_candidate_commit_id,
    complex_execution_result_id, complex_source_candidate_commit_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?);

-- name: InsertClearDevComplexExecutionEvidence :exec
INSERT INTO cleardev_evidence (
    id, development_project_id, subject_type, subject_id, evidence_kind, evidence_key,
    result, candidate_commit_id, integration_candidate_id, commit_sha, source_type,
    source_ao_session_id, candidate_check_run_id, local_review_id,
    complex_execution_check_run_id, complex_execution_review_id, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?);

-- name: ListClearDevComplexExecutionEvidence :many
SELECT evidence.* FROM cleardev_evidence AS evidence
LEFT JOIN cleardev_candidate_commits AS candidate ON candidate.id = evidence.candidate_commit_id
LEFT JOIN cleardev_integration_candidates AS integration ON integration.id = evidence.integration_candidate_id
WHERE candidate.complex_execution_task_attempt_id IN (
    SELECT attempt.id FROM cleardev_complex_execution_task_attempts AS attempt WHERE attempt.execution_run_id = ?
) OR integration.complex_execution_result_id IN (
    SELECT result.id FROM cleardev_complex_execution_results AS result WHERE result.execution_run_id = ?
)
ORDER BY evidence.sequence;
