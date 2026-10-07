-- The physical table names below are the immutable 0106 compatibility names.
-- Store and service code expose requirement/version/task terminology.

-- name: InsertClearDevRequirement :exec
INSERT INTO cleardev_development_projects (
    id, ao_project_id, name, state, paused_from_state,
    cancelled_at, cancel_reason_code, cancel_reason_text, created_at, updated_at
) VALUES (?, ?, ?, 'INTAKE', NULL, NULL, '', '', ?, ?);

-- name: GetClearDevRequirement :one
SELECT *
FROM cleardev_development_projects
WHERE id = ?;

-- name: ListClearDevRequirementsByAOProject :many
SELECT *
FROM cleardev_development_projects
WHERE ao_project_id = ?
  AND NOT EXISTS (SELECT 1 FROM cleardev_product_goals product WHERE product.id=cleardev_development_projects.id)
ORDER BY created_at, id;

-- name: CancelClearDevRequirementCAS :execrows
UPDATE cleardev_development_projects
SET state = 'CANCELLED', paused_from_state = NULL,
    cancelled_at = sqlc.arg(cancelled_at),
    cancel_reason_code = sqlc.arg(cancel_reason_code),
    cancel_reason_text = sqlc.arg(cancel_reason_text),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND cancelled_at IS NULL;

-- name: InsertClearDevRequirementVersion :exec
INSERT INTO cleardev_contract_versions (
    id, development_project_id, version, contract_text, sha256, state,
    superseded_by_id, task_set_version, created_at, approved_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevRequirementVersion :one
SELECT *
FROM cleardev_contract_versions
WHERE id = ?;

-- name: ListClearDevRequirementVersions :many
SELECT *
FROM cleardev_contract_versions
WHERE development_project_id = ?
ORDER BY version;

-- name: GetCurrentClearDevConfirmedRequirementVersion :one
SELECT *
FROM cleardev_contract_versions
WHERE development_project_id = ? AND state = 'APPROVED'
LIMIT 1;

-- name: GetOpenClearDevRequirementVersion :one
SELECT *
FROM cleardev_contract_versions
WHERE development_project_id = ? AND state IN ('DRAFT', 'IN_REVIEW')
LIMIT 1;

-- name: NextClearDevRequirementVersion :one
SELECT CAST(COALESCE(MAX(version), 0) + 1 AS INTEGER)
FROM cleardev_contract_versions
WHERE development_project_id = ?;

-- name: UpdateClearDevRequirementVersionStateCAS :execrows
UPDATE cleardev_contract_versions
SET state = sqlc.arg(next_state),
    superseded_by_id = sqlc.narg(superseded_by_id),
    approved_at = sqlc.narg(approved_at)
WHERE id = sqlc.arg(id) AND state = sqlc.arg(expected_state);

-- name: IncrementClearDevTaskSetVersionCAS :execrows
UPDATE cleardev_contract_versions
SET task_set_version = task_set_version + 1
WHERE id = sqlc.arg(id)
  AND task_set_version = sqlc.arg(expected_task_set_version);

-- name: InsertClearDevDevelopmentTask :exec
INSERT INTO cleardev_work_items (
    id, development_project_id, contract_version_id, title, mode, state,
    paused_from_state, max_rework_count, rework_count, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevDevelopmentTask :one
SELECT id, development_project_id, contract_version_id, title, mode, state,
       paused_from_state, max_rework_count, rework_count, created_at, updated_at
FROM cleardev_work_items
WHERE id = ?;

-- name: ListClearDevDevelopmentTasks :many
SELECT id, development_project_id, contract_version_id, title, mode, state,
       paused_from_state, max_rework_count, rework_count, created_at, updated_at
FROM cleardev_work_items
WHERE development_project_id = ?
ORDER BY created_at, id;

-- name: ListClearDevDevelopmentTasksForVersion :many
SELECT id, development_project_id, contract_version_id, title, mode, state,
       paused_from_state, max_rework_count, rework_count, created_at, updated_at
FROM cleardev_work_items
WHERE contract_version_id = ?
ORDER BY created_at, id;

-- name: UpdateClearDevDevelopmentTaskStateCAS :execrows
UPDATE cleardev_work_items
SET state = sqlc.arg(next_state),
    paused_from_state = sqlc.narg(paused_from_state),
    rework_count = sqlc.arg(rework_count),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND state = sqlc.arg(expected_state)
  AND rework_count = sqlc.arg(expected_rework_count);

-- name: InsertClearDevPermissionVersion :exec
INSERT INTO cleardev_path_permission_versions (
    id, work_item_id, version, write_paths, forbidden_paths,
    shared_paths_require_approval, generated_paths, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevPermissionVersion :one
SELECT id, work_item_id, version, write_paths, forbidden_paths,
       shared_paths_require_approval, generated_paths, created_at
FROM cleardev_path_permission_versions
WHERE id = ?;

-- name: ListClearDevPermissionVersions :many
SELECT id, work_item_id, version, write_paths, forbidden_paths,
       shared_paths_require_approval, generated_paths, created_at
FROM cleardev_path_permission_versions
WHERE work_item_id = ?
ORDER BY version;

-- name: GetLatestClearDevPermissionVersion :one
SELECT id, work_item_id, version, write_paths, forbidden_paths,
       shared_paths_require_approval, generated_paths, created_at
FROM cleardev_path_permission_versions
WHERE work_item_id = ?
ORDER BY version DESC
LIMIT 1;

-- name: NextClearDevPermissionVersion :one
SELECT CAST(COALESCE(MAX(version), 0) + 1 AS INTEGER)
FROM cleardev_path_permission_versions
WHERE work_item_id = ?;

-- name: InsertClearDevRequiredCheck :exec
INSERT INTO cleardev_required_checks (
    id, work_item_id, name, check_kind, created_at
) VALUES (?, ?, ?, ?, ?);

-- name: ListClearDevRequiredChecks :many
SELECT id, work_item_id, name, check_kind, created_at
FROM cleardev_required_checks
WHERE work_item_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevCandidateCommit :exec
INSERT INTO cleardev_candidate_commits (
    id, work_item_id, sequence, ao_session_id, permission_version_id,
    commit_sha, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevCandidateCommit :one
SELECT id, work_item_id, sequence, ao_session_id, permission_version_id,
       commit_sha, created_at
FROM cleardev_candidate_commits
WHERE id = ?;

-- name: ListClearDevCandidateCommits :many
SELECT id, work_item_id, sequence, ao_session_id, permission_version_id,
       commit_sha, created_at
FROM cleardev_candidate_commits
WHERE work_item_id = ?
ORDER BY sequence;

-- name: GetCurrentClearDevCandidateCommit :one
SELECT id, work_item_id, sequence, ao_session_id, permission_version_id,
       commit_sha, created_at
FROM cleardev_candidate_commits
WHERE work_item_id = ?
ORDER BY sequence DESC
LIMIT 1;

-- name: NextClearDevCandidateSequence :one
SELECT CAST(COALESCE(MAX(sequence), 0) + 1 AS INTEGER)
FROM cleardev_candidate_commits
WHERE work_item_id = ?;

-- name: IsClearDevCandidateCurrentRound :one
SELECT CAST(
    COALESCE((
        SELECT MAX(candidate_event.sequence)
        FROM cleardev_project_events AS candidate_event
        WHERE candidate_event.subject_type IN ('CANDIDATE_COMMIT')
          AND candidate_event.subject_id = sqlc.arg(candidate_id)
          AND candidate_event.action = 'REGISTER_CANDIDATE'
          AND candidate_event.outcome = 'ACCEPTED'
    ), 0) > COALESCE((
        SELECT MAX(restart_event.sequence)
        FROM cleardev_project_events AS restart_event
        WHERE restart_event.subject_type IN ('WORK_ITEM', 'DEVELOPMENT_TASK')
          AND restart_event.subject_id = sqlc.arg(development_task_id)
          AND restart_event.action IN ('RESTART_WORK_ITEM', 'RESTART_DEVELOPMENT_TASK')
          AND restart_event.outcome = 'ACCEPTED'
    ), 0)
AS INTEGER);

-- name: InsertClearDevIntegrationCandidate :exec
INSERT INTO cleardev_integration_candidates (
    id, development_project_id, requirement_version_id, task_set_version,
    sequence, ao_session_id, commit_sha, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevIntegrationCandidate :one
SELECT *
FROM cleardev_integration_candidates
WHERE id = ?;

-- name: ListClearDevIntegrationCandidates :many
SELECT *
FROM cleardev_integration_candidates
WHERE development_project_id = ?
ORDER BY sequence;

-- name: GetCurrentClearDevIntegrationCandidate :one
SELECT *
FROM cleardev_integration_candidates
WHERE development_project_id = ?
ORDER BY sequence DESC
LIMIT 1;

-- name: GetCurrentMatchingClearDevIntegrationCandidate :one
SELECT *
FROM cleardev_integration_candidates
WHERE development_project_id = sqlc.arg(development_requirement_id)
  AND requirement_version_id = sqlc.arg(requirement_version_id)
  AND task_set_version = sqlc.arg(task_set_version)
ORDER BY sequence DESC
LIMIT 1;

-- name: NextClearDevIntegrationCandidateSequence :one
SELECT CAST(COALESCE(MAX(sequence), 0) + 1 AS INTEGER)
FROM cleardev_integration_candidates
WHERE development_project_id = ?;

-- name: InsertClearDevEvidence :exec
INSERT INTO cleardev_evidence (
    id, development_project_id, subject_type, subject_id, evidence_kind,
    evidence_key, result, candidate_commit_id, integration_candidate_id,
    commit_sha, source_type, source_ao_session_id, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevEvidence :many
SELECT sequence, id, development_project_id, subject_type, subject_id, evidence_kind,
       evidence_key, result, candidate_commit_id, integration_candidate_id,
       commit_sha, source_type, source_ao_session_id, created_at, expires_at
FROM cleardev_evidence
WHERE development_project_id = ?
ORDER BY sequence;

-- name: ListClearDevCandidateEvidence :many
SELECT sequence, id, development_project_id, subject_type, subject_id, evidence_kind,
       evidence_key, result, candidate_commit_id, integration_candidate_id,
       commit_sha, source_type, source_ao_session_id, created_at, expires_at
FROM cleardev_evidence
WHERE candidate_commit_id = ?
ORDER BY sequence;

-- name: ListClearDevIntegrationCandidateEvidence :many
SELECT sequence, id, development_project_id, subject_type, subject_id, evidence_kind,
       evidence_key, result, candidate_commit_id, integration_candidate_id,
       commit_sha, source_type, source_ao_session_id, created_at, expires_at
FROM cleardev_evidence
WHERE integration_candidate_id = ?
ORDER BY sequence;

-- name: InsertClearDevRequirementEvent :exec
INSERT INTO cleardev_project_events (
    ao_project_id, development_project_id, subject_type, subject_id, action,
    previous_state, target_state, outcome, reason_code, reason_text,
    source, source_ao_session_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevRequirementEvents :many
SELECT sequence, ao_project_id, development_project_id, subject_type, subject_id,
       action, previous_state, target_state, outcome, reason_code, reason_text, source,
       source_ao_session_id, created_at
FROM cleardev_project_events
WHERE development_project_id = ?
ORDER BY sequence;

-- S02 STANDARD single-task facts. Existing S01 queries intentionally remain
-- unchanged so legacy readers do not mistake NULL S02 bindings for authority.

-- name: ListClearDevStandardRoleBindings :many
SELECT *
FROM cleardev_standard_role_bindings
WHERE requirement_version_id = ?
ORDER BY requested_at, id;

-- name: GetClearDevStandardFlowRequirementVersion :one
SELECT version.*
FROM cleardev_contract_versions AS version
JOIN cleardev_standard_role_bindings AS binding
  ON binding.requirement_version_id = version.id
WHERE version.development_project_id = ?
  AND binding.role = 'STEWARD'
ORDER BY binding.requested_at, binding.id
LIMIT 1;

-- name: ListClearDevRunnableStandardFlows :many
SELECT DISTINCT requirement.id
FROM cleardev_development_projects AS requirement
JOIN cleardev_contract_versions AS version
  ON version.development_project_id = requirement.id
JOIN cleardev_standard_role_bindings AS binding
  ON binding.requirement_version_id = version.id
WHERE binding.role = 'STEWARD'
  AND requirement.cancelled_at IS NULL
ORDER BY requirement.id;

-- name: GetClearDevStandardRoleBinding :one
SELECT *
FROM cleardev_standard_role_bindings
WHERE id = ?;

-- name: InsertClearDevStandardRoleBinding :exec
INSERT INTO cleardev_standard_role_bindings (
    id, development_project_id, requirement_version_id, role,
    engineering_plan_id, dispatch_id, work_item_id, candidate_commit_id,
    session_creation_idempotency_key, ao_session_id, status, reason_code,
    requested_at, bound_at, ended_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: BindClearDevStandardRoleBindingCAS :execrows
UPDATE cleardev_standard_role_bindings
SET ao_session_id = sqlc.arg(ao_session_id), status = 'BOUND',
    bound_at = sqlc.arg(bound_at), reason_code = ''
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: FailClearDevStandardRoleBindingCAS :execrows
UPDATE cleardev_standard_role_bindings
SET status = 'FAILED', reason_code = sqlc.arg(reason_code),
    ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'REQUESTED';

-- name: EndClearDevStandardRoleBindingCAS :execrows
UPDATE cleardev_standard_role_bindings
SET status = 'ENDED', reason_code = sqlc.arg(reason_code),
    ended_at = sqlc.arg(ended_at)
WHERE id = sqlc.arg(id) AND status = 'BOUND';

-- name: ListClearDevStandardAgentSteps :many
SELECT *
FROM cleardev_standard_agent_steps
WHERE role_binding_id = ?
ORDER BY requested_at, id;

-- name: GetClearDevStandardAgentStep :one
SELECT *
FROM cleardev_standard_agent_steps
WHERE id = ?;

-- name: InsertClearDevStandardAgentStep :exec
INSERT INTO cleardev_standard_agent_steps (
    id, role_binding_id, step_kind, request_id, client_message_id,
    prompt_sha256, send_status, turn_id, final_message_id, final_message_text, message_sha256,
    requested_at, sent_at, completed_at, failed_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MarkClearDevStandardAgentStepSentCAS :execrows
UPDATE cleardev_standard_agent_steps
SET send_status = 'SENT', sent_at = sqlc.arg(sent_at)
WHERE id = sqlc.arg(id) AND send_status = 'PENDING';

-- name: SettleClearDevStandardAgentStepCAS :execrows
UPDATE cleardev_standard_agent_steps
SET send_status = 'SETTLED', turn_id = sqlc.arg(turn_id),
    final_message_id = sqlc.arg(final_message_id),
    final_message_text = sqlc.arg(final_message_text),
    message_sha256 = sqlc.arg(message_sha256), completed_at = sqlc.arg(completed_at),
    reason_code = ''
WHERE id = sqlc.arg(id) AND send_status = 'SENT';

-- name: FailClearDevStandardAgentStepCAS :execrows
UPDATE cleardev_standard_agent_steps
SET send_status = 'FAILED', failed_at = sqlc.arg(failed_at),
    reason_code = sqlc.arg(reason_code)
WHERE id = sqlc.arg(id) AND send_status IN ('PENDING', 'SENT');

-- name: ListClearDevStandardEngineeringPlans :many
SELECT *
FROM cleardev_standard_engineering_plans
WHERE requirement_version_id = ?
ORDER BY version;

-- name: GetClearDevStandardEngineeringPlan :one
SELECT *
FROM cleardev_standard_engineering_plans
WHERE id = ?;

-- name: InsertClearDevStandardEngineeringPlan :exec
INSERT INTO cleardev_standard_engineering_plans (
    id, requirement_version_id, requirement_sha256, version,
    planner_role_binding_id, agent_step_id, turn_id, final_message_id,
    plan_json, plan_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: NextClearDevStandardEngineeringPlanVersion :one
SELECT CAST(COALESCE(MAX(version), 0) + 1 AS INTEGER)
FROM cleardev_standard_engineering_plans
WHERE requirement_version_id = ?;

-- name: ListClearDevStandardPlanReviews :many
SELECT *
FROM cleardev_standard_plan_reviews
WHERE engineering_plan_id IN (
    SELECT id FROM cleardev_standard_engineering_plans WHERE requirement_version_id = ?
)
ORDER BY created_at, id;

-- name: GetClearDevStandardPlanReview :one
SELECT *
FROM cleardev_standard_plan_reviews
WHERE id = ?;

-- name: InsertClearDevStandardPlanReview :exec
INSERT INTO cleardev_standard_plan_reviews (
    id, engineering_plan_id, steward_role_binding_id, agent_step_id,
    turn_id, final_message_id, verdict, reason_code, summary, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListClearDevStandardDispatches :many
SELECT *
FROM cleardev_standard_dispatches
WHERE requirement_version_id = ?
ORDER BY requested_at, id;

-- name: GetClearDevStandardDispatch :one
SELECT *
FROM cleardev_standard_dispatches
WHERE id = ?;

-- name: InsertClearDevStandardDispatch :exec
INSERT INTO cleardev_standard_dispatches (
    id, requirement_version_id, engineering_plan_id, plan_review_id,
    steward_role_binding_id, agent_step_id, mode, preallocated_work_item_id,
    expected_task_set_version, execution_package_json, execution_package_sha256,
    builder_session_idempotency_key, builder_role_binding_id, base_commit_sha,
    status, reason_code, requested_at, decided_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AcceptClearDevStandardDispatchCAS :execrows
UPDATE cleardev_standard_dispatches
SET status = 'ACCEPTED', base_commit_sha = sqlc.arg(base_commit_sha),
    decided_at = sqlc.arg(decided_at), reason_code = ''
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: RejectClearDevStandardDispatchCAS :execrows
UPDATE cleardev_standard_dispatches
SET status = sqlc.arg(status), reason_code = sqlc.arg(reason_code),
    decided_at = sqlc.arg(decided_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: InsertClearDevStandardDevelopmentTask :exec
INSERT INTO cleardev_work_items (
    id, development_project_id, contract_version_id, title, mode, state,
    paused_from_state, max_rework_count, rework_count, accepted_dispatch_id,
    dispatch_base_commit_sha, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertClearDevStandardCandidateCommit :exec
INSERT INTO cleardev_candidate_commits (
    id, work_item_id, sequence, ao_session_id, permission_version_id,
    dispatch_id, base_commit_sha, commit_sha, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevStandardCandidateCommit :one
SELECT *
FROM cleardev_candidate_commits
WHERE id = ?;

-- name: InsertClearDevStandardIntegrationCandidate :exec
INSERT INTO cleardev_integration_candidates (
    id, development_project_id, requirement_version_id, task_set_version,
    sequence, ao_session_id, commit_sha, dispatch_id, source_candidate_commit_id,
    created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevStandardIntegrationCandidate :one
SELECT *
FROM cleardev_integration_candidates
WHERE id = ?;

-- name: InsertClearDevCandidateCheckRun :exec
INSERT INTO cleardev_candidate_check_runs (
    id, work_item_id, candidate_commit_id, dispatch_id, base_commit_sha,
    candidate_commit_sha, check_kind, check_name, check_spec_sha256, argv_json,
    container_image_id, exit_code, status, timed_out, output_summary, output_sha256,
    changed_paths_json, result, created_at, settled_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevCandidateCheckRun :one
SELECT *
FROM cleardev_candidate_check_runs
WHERE id = ?;

-- name: SettleClearDevCandidateCheckRunCAS :execrows
UPDATE cleardev_candidate_check_runs
SET status = sqlc.arg(status), container_image_id = sqlc.narg(container_image_id), exit_code = sqlc.narg(exit_code),
    timed_out = sqlc.narg(timed_out), output_summary = sqlc.narg(output_summary),
    output_sha256 = sqlc.narg(output_sha256), changed_paths_json = sqlc.narg(changed_paths_json),
    result = sqlc.narg(result), settled_at = sqlc.arg(settled_at),
    reason_code = sqlc.arg(reason_code)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: ListClearDevCandidateCheckRuns :many
SELECT *
FROM cleardev_candidate_check_runs
WHERE work_item_id IN (
    SELECT id FROM cleardev_work_items WHERE contract_version_id = ?
)
ORDER BY created_at, id;

-- name: InsertClearDevStandardLocalReview :exec
INSERT INTO cleardev_standard_local_reviews (
    id, candidate_commit_id, dispatch_id, review_packet_json, review_packet_sha256,
    reviewer_role_binding_id, agent_step_id, status, turn_id, final_message_id,
    verdict, reason_code, summary, created_at, settled_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevStandardLocalReview :one
SELECT *
FROM cleardev_standard_local_reviews
WHERE id = ?;

-- name: SettleClearDevStandardLocalReviewCAS :execrows
UPDATE cleardev_standard_local_reviews
SET status = sqlc.arg(status), turn_id = sqlc.narg(turn_id),
    final_message_id = sqlc.narg(final_message_id), verdict = sqlc.narg(verdict),
    reason_code = sqlc.arg(reason_code), summary = sqlc.narg(summary),
    settled_at = sqlc.arg(settled_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: ListClearDevStandardLocalReviews :many
SELECT *
FROM cleardev_standard_local_reviews
WHERE candidate_commit_id IN (
    SELECT id FROM cleardev_candidate_commits
    WHERE work_item_id IN (
        SELECT id FROM cleardev_work_items WHERE contract_version_id = ?
    )
)
ORDER BY created_at, id;

-- name: InsertClearDevStandardEvidence :exec
INSERT INTO cleardev_evidence (
    id, development_project_id, subject_type, subject_id, evidence_kind,
    evidence_key, result, candidate_commit_id, integration_candidate_id,
    commit_sha, source_type, source_ao_session_id, candidate_check_run_id,
    local_review_id, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- S03 desktop human-decision facts. Request bodies are immutable; only the
-- pending-to-resolved CAS and dispatch consumption are allowed updates.

-- name: InsertClearDevHumanDecisionRequest :exec
INSERT INTO cleardev_human_decision_requests (
    id, development_project_id, decision_kind, binding_schema_version,
    binding_json, display_json, content_sha256, status, decision, resolved_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'PENDING', '', NULL, ?);

-- name: GetClearDevHumanDecisionRequest :one
SELECT *
FROM cleardev_human_decision_requests
WHERE id = ?;

-- name: GetClearDevHumanDecisionRequestByRequirementVersionID :one
SELECT *
FROM cleardev_human_decision_requests
WHERE decision_kind = 'CONFIRM_REQUIREMENT_VERSION'
  AND json_extract(binding_json, '$.requirementVersionId') = sqlc.arg(requirement_version_id);

-- name: ListPendingClearDevHumanDecisionRequests :many
SELECT *
FROM cleardev_human_decision_requests
WHERE status = 'PENDING'
ORDER BY created_at, id;

-- name: ListClearDevHumanDecisionRequestsByKind :many
SELECT *
FROM cleardev_human_decision_requests
WHERE decision_kind = ?
ORDER BY created_at, id;

-- name: ListClearDevHumanDecisionRepairGrants :many
SELECT id, binding_json, created_at
FROM cleardev_human_decision_requests
WHERE decision_kind = 'AUTHORIZE_COORDINATION_REPAIR'
  AND status = 'RESOLVED'
  AND decision = 'APPROVE'
ORDER BY created_at, id;

-- name: ListClearDevPendingConfirmationVersionsMissingHumanDecision :many
SELECT version.*
FROM cleardev_contract_versions AS version
WHERE version.state = 'IN_REVIEW'
  AND NOT EXISTS (
    SELECT 1
    FROM cleardev_human_decision_requests AS request
    WHERE request.decision_kind = 'CONFIRM_REQUIREMENT_VERSION'
      AND json_extract(request.binding_json, '$.requirementVersionId') = version.id
  )
ORDER BY version.created_at, version.id;

-- name: SettleClearDevHumanDecisionRequestCAS :execrows
UPDATE cleardev_human_decision_requests
SET status = 'RESOLVED',
    decision = sqlc.arg(decision),
    resolved_at = sqlc.arg(resolved_at)
WHERE id = sqlc.arg(id) AND status = 'PENDING';

-- name: InsertClearDevHumanDecisionDispatch :exec
INSERT INTO cleardev_human_decision_dispatches (
    id, request_id, desktop_run_id, nonce_sha256, issued_at, expires_at, consumed_at, outcome
) VALUES (?, ?, ?, ?, ?, ?, NULL, '');

-- name: GetClearDevHumanDecisionDispatchByNonceSHA256 :one
SELECT *
FROM cleardev_human_decision_dispatches
WHERE nonce_sha256 = ?;

-- name: ListOpenClearDevHumanDecisionDispatchesForRequest :many
SELECT *
FROM cleardev_human_decision_dispatches
WHERE request_id = ? AND consumed_at IS NULL
ORDER BY issued_at, id;

-- name: ListOpenClearDevHumanDecisionDispatchesForDesktop :many
SELECT *
FROM cleardev_human_decision_dispatches
WHERE desktop_run_id = ? AND consumed_at IS NULL
ORDER BY issued_at, id;

-- name: ConsumeClearDevHumanDecisionDispatchCAS :execrows
UPDATE cleardev_human_decision_dispatches
SET consumed_at = sqlc.arg(consumed_at),
    outcome = sqlc.arg(outcome)
WHERE nonce_sha256 = sqlc.arg(nonce_sha256)
  AND desktop_run_id = sqlc.arg(desktop_run_id)
  AND consumed_at IS NULL;

-- name: InsertClearDevHumanDecisionEffect :exec
INSERT INTO cleardev_human_decision_effects (
    request_id, decision, event_sequence, created_at
) VALUES (?, ?, ?, ?);

-- name: GetClearDevHumanDecisionEffect :one
SELECT *
FROM cleardev_human_decision_effects
WHERE request_id = ?;

-- name: GetLatestClearDevRequirementEventSequenceForSubjectAction :one
SELECT sequence
FROM cleardev_project_events
WHERE subject_id = sqlc.arg(subject_id) AND action = sqlc.arg(action)
ORDER BY sequence DESC
LIMIT 1;
