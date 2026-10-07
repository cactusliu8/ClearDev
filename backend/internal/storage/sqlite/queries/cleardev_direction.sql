-- S05 direction-change facts. These queries stay in their own file so S04
-- compilation readers cannot treat v2 rounds as initial-version facts.

-- name: InsertClearDevDirectionIntent :exec
INSERT INTO cleardev_direction_intents (
    request_id, development_project_id, requirement_version_id, requirement_sha256,
    message, message_sha256, steward_role_binding_id, direction_request_id,
    agent_step_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevDirectionIntent :one
SELECT *
FROM cleardev_direction_intents
WHERE request_id = ?;

-- name: GetClearDevDirectionIntentByRequirement :one
SELECT *
FROM cleardev_direction_intents
WHERE development_project_id = ?
ORDER BY created_at, request_id
LIMIT 1;

-- name: GetClearDevDirectionIntentByVersion :one
SELECT *
FROM cleardev_direction_intents
WHERE requirement_version_id = ?;

-- name: InsertClearDevDirectionAgentStep :exec
INSERT INTO cleardev_direction_agent_steps (
    id, role_binding_id, step_kind, request_id, client_message_id,
    prompt_sha256, send_status, turn_id, final_message_id, final_message_text,
    message_sha256, requested_at, sent_at, completed_at, failed_at, reason_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevDirectionAgentStep :one
SELECT *
FROM cleardev_direction_agent_steps
WHERE id = ?;

-- name: GetClearDevDirectionAgentStepByRequest :one
SELECT *
FROM cleardev_direction_agent_steps
WHERE role_binding_id = ? AND step_kind = ? AND request_id = ?;

-- name: ListClearDevDirectionAgentStepsForRequirement :many
SELECT step.*
FROM cleardev_direction_agent_steps AS step
JOIN cleardev_complex_role_bindings AS binding ON binding.id = step.role_binding_id
WHERE binding.development_project_id = ?
ORDER BY step.requested_at, step.id;

-- name: MarkClearDevDirectionAgentStepSentCAS :execrows
UPDATE cleardev_direction_agent_steps
SET send_status = 'SENT', sent_at = sqlc.arg(sent_at)
WHERE id = sqlc.arg(id) AND send_status = 'PENDING';

-- name: SettleClearDevDirectionAgentStepCAS :execrows
UPDATE cleardev_direction_agent_steps
SET send_status = 'SETTLED',
    turn_id = sqlc.arg(turn_id),
    final_message_id = sqlc.arg(final_message_id),
    final_message_text = sqlc.arg(final_message_text),
    message_sha256 = sqlc.arg(message_sha256),
    completed_at = sqlc.arg(completed_at)
WHERE id = sqlc.arg(id) AND send_status = 'SENT';

-- name: FailClearDevDirectionAgentStepCAS :execrows
UPDATE cleardev_direction_agent_steps
SET send_status = 'FAILED',
    failed_at = sqlc.arg(failed_at),
    reason_code = sqlc.arg(reason_code)
WHERE id = sqlc.arg(id) AND send_status IN ('PENDING', 'SENT');

-- name: InsertClearDevDirectionRequest :exec
INSERT INTO cleardev_direction_requests (
    id, intent_request_id, development_project_id, requirement_version_id,
    summary, affected_requirement_ids_json, result_sha256, agent_step_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevDirectionRequest :one
SELECT *
FROM cleardev_direction_requests
WHERE id = ?;

-- name: GetClearDevDirectionRequestByRequirement :one
SELECT *
FROM cleardev_direction_requests
WHERE development_project_id = ?
ORDER BY created_at, id
LIMIT 1;

-- name: InsertClearDevDirectionStopGate :exec
INSERT INTO cleardev_direction_stop_gates (
    id, direction_request_id, development_project_id, requirement_version_id,
    task_set_version, snapshot_sha256, status, closed_at, close_reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, 'ACTIVE', NULL, '', ?);

-- name: GetClearDevDirectionStopGate :one
SELECT *
FROM cleardev_direction_stop_gates
WHERE id = ?;

-- name: GetClearDevDirectionStopGateByRequest :one
SELECT *
FROM cleardev_direction_stop_gates
WHERE direction_request_id = ?;

-- name: GetActiveClearDevDirectionStopGateByVersion :one
SELECT *
FROM cleardev_direction_stop_gates
WHERE requirement_version_id = ? AND status = 'ACTIVE';

-- name: GetActiveClearDevDirectionStopGateByRequirement :one
SELECT gate.*
FROM cleardev_direction_stop_gates AS gate
JOIN cleardev_contract_versions AS version ON version.id = gate.requirement_version_id
WHERE version.development_project_id = ?
  AND gate.status = 'ACTIVE'
ORDER BY gate.created_at, gate.id
LIMIT 1;

-- name: CloseClearDevDirectionStopGateCAS :execrows
UPDATE cleardev_direction_stop_gates
SET status = 'CLOSED',
    closed_at = sqlc.arg(closed_at),
    close_reason = sqlc.arg(close_reason)
WHERE id = sqlc.arg(id) AND status = 'ACTIVE';

-- name: InsertClearDevDirectionTaskSnapshot :exec
INSERT INTO cleardev_direction_task_snapshots (
    gate_id, task_id, status, paused_from_status, ordinal
) VALUES (?, ?, ?, ?, ?);

-- name: ListClearDevDirectionTaskSnapshots :many
SELECT *
FROM cleardev_direction_task_snapshots
WHERE gate_id = ?
ORDER BY ordinal, task_id;

-- name: InsertClearDevDirectionTaskProcessing :exec
INSERT INTO cleardev_direction_task_processings (
    id, direction_request_id, task_id, occupancy, interrupt_result,
    checkpoint_id, cancel_result, reason_code, occupied_at, finalized_at
) VALUES (?, ?, ?, 'PENDING', '', NULL, '', '', NULL, NULL);

-- name: GetClearDevDirectionTaskProcessing :one
SELECT *
FROM cleardev_direction_task_processings
WHERE id = ?;

-- name: GetClearDevDirectionTaskProcessingByTask :one
SELECT *
FROM cleardev_direction_task_processings
WHERE direction_request_id = ? AND task_id = ?;

-- name: ListClearDevDirectionTaskProcessings :many
SELECT *
FROM cleardev_direction_task_processings
WHERE direction_request_id = ?
ORDER BY id;

-- name: OccupyClearDevDirectionTaskProcessingCAS :execrows
UPDATE cleardev_direction_task_processings
SET occupancy = 'OCCUPIED',
    occupied_at = sqlc.arg(occupied_at),
    interrupt_result = sqlc.arg(interrupt_result)
WHERE id = sqlc.arg(id) AND occupancy = 'PENDING';

-- name: MarkClearDevDirectionTaskInterruptUnknownCAS :execrows
UPDATE cleardev_direction_task_processings
SET interrupt_result = sqlc.arg(interrupt_result)
WHERE id = sqlc.arg(id) AND occupancy = 'OCCUPIED' AND interrupt_result = '';

-- name: FinalizeClearDevDirectionTaskProcessingCAS :execrows
UPDATE cleardev_direction_task_processings
SET occupancy = 'FINAL',
    occupied_at = sqlc.arg(occupied_at),
    finalized_at = sqlc.arg(finalized_at),
    checkpoint_id = sqlc.narg(checkpoint_id),
    cancel_result = sqlc.arg(cancel_result),
    reason_code = sqlc.arg(reason_code),
    interrupt_result = sqlc.arg(next_interrupt_result)
WHERE id = sqlc.arg(id) AND occupancy IN ('PENDING', 'OCCUPIED');

-- name: InsertClearDevDirectionCheckpoint :exec
INSERT INTO cleardev_direction_checkpoints (
    id, processing_id, task_id, session_id, worktree_path, baseline_sha, head_sha,
    dirty, staged, untracked, change_summary_json, candidate_commit_id, kind, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevDirectionCheckpoint :one
SELECT *
FROM cleardev_direction_checkpoints
WHERE id = ?;

-- name: ListClearDevDirectionCheckpoints :many
SELECT checkpoint.*
FROM cleardev_direction_checkpoints AS checkpoint
JOIN cleardev_direction_task_processings AS processing ON processing.id = checkpoint.processing_id
WHERE processing.direction_request_id = ?
ORDER BY checkpoint.created_at, checkpoint.id;

-- name: InsertClearDevDirectionRevision :exec
INSERT INTO cleardev_direction_revisions (
    direction_request_id, previous_requirement_version_id, target_requirement_version_id, created_at
) VALUES (?, ?, ?, ?);

-- name: GetClearDevDirectionRevision :one
SELECT *
FROM cleardev_direction_revisions
WHERE direction_request_id = ?;

-- name: GetClearDevDirectionRevisionByRequirement :one
SELECT revision.*
FROM cleardev_direction_revisions AS revision
JOIN cleardev_direction_requests AS request ON request.id = revision.direction_request_id
WHERE request.development_project_id = ?
ORDER BY revision.created_at, revision.direction_request_id
LIMIT 1;

-- name: InsertClearDevDirectionCompilationRequest :exec
INSERT INTO cleardev_direction_compilation_requests (
    id, direction_request_id, development_project_id, agent_step_id,
    clarification_round, compilation_context_sha256, additional_round_reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevDirectionCompilationRequest :one
SELECT *
FROM cleardev_direction_compilation_requests
WHERE id = ?;

-- name: ListClearDevDirectionCompilationRequests :many
SELECT *
FROM cleardev_direction_compilation_requests
WHERE direction_request_id = ?
ORDER BY clarification_round, id;

-- name: InsertClearDevDirectionClarificationQuestion :exec
INSERT INTO cleardev_direction_clarification_questions (
    compilation_request_id, question_key, text, reason, requirement_keys_json, ordinal
) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListClearDevDirectionClarificationQuestions :many
SELECT *
FROM cleardev_direction_clarification_questions
WHERE compilation_request_id = ?
ORDER BY ordinal, question_key;

-- name: InsertClearDevDirectionClarificationAnswer :exec
INSERT INTO cleardev_direction_clarification_answers (
    compilation_request_id, question_key, text, created_at
) VALUES (?, ?, ?, ?);

-- name: ListClearDevDirectionClarificationAnswers :many
SELECT *
FROM cleardev_direction_clarification_answers
WHERE compilation_request_id = ?
ORDER BY question_key;

-- name: InsertClearDevDirectionCompilation :exec
INSERT INTO cleardev_direction_compilations (
    id, direction_request_id, development_project_id, compilation_request_id, agent_step_id,
    outcome, summary, normalized_requirement_json, compilation_sha256, turn_id,
    final_message_id, raw_message_text, raw_message_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevDirectionCompilation :one
SELECT *
FROM cleardev_direction_compilations
WHERE id = ?;

-- name: ListClearDevDirectionCompilations :many
SELECT *
FROM cleardev_direction_compilations
WHERE direction_request_id = ?
ORDER BY created_at, id;

-- name: InsertClearDevDirectionIDMap :exec
INSERT INTO cleardev_direction_id_maps (
    compilation_id, kind, temporary_key, stable_id, ordinal
) VALUES (?, ?, ?, ?, ?);

-- name: ListClearDevDirectionIDMaps :many
SELECT *
FROM cleardev_direction_id_maps
WHERE compilation_id = ?
ORDER BY kind, ordinal, temporary_key;

-- name: ListClearDevRunnableDirectionRequirements :many
SELECT DISTINCT requirement.id
FROM cleardev_development_projects AS requirement
JOIN cleardev_direction_intents AS intent ON intent.development_project_id = requirement.id
WHERE requirement.cancelled_at IS NULL
ORDER BY requirement.id;

-- name: GetApprovedClearDevComplexPlanForVersion :one
SELECT plan.id, plan.planning_request_id, plan.development_project_id, plan.requirement_version_id,
       plan.requirement_sha256, plan.compilation_sha256, plan.version, plan.planner_role_binding_id,
       plan.agent_step_id, plan.turn_id, plan.final_message_id, plan.plan_json, plan.plan_sha256, plan.created_at
FROM cleardev_complex_engineering_plans AS plan
WHERE plan.requirement_version_id = ?
  AND NOT EXISTS (
      SELECT 1 FROM cleardev_complex_engineering_plans AS later
      WHERE later.requirement_version_id = plan.requirement_version_id AND later.version > plan.version
  )
  AND (
      (COALESCE(json_extract(plan.plan_json, '$.schemaVersion'), 1) = 1 AND EXISTS (
          SELECT 1 FROM cleardev_complex_plan_reviews AS review
          WHERE review.plan_id = plan.id AND review.plan_sha256 = plan.plan_sha256 AND review.verdict = 'APPROVED'
      ))
      OR (json_extract(plan.plan_json, '$.schemaVersion') = 2 AND EXISTS (
          SELECT 1 FROM cleardev_complex_plan_validations AS validation
          WHERE validation.plan_id = plan.id AND validation.plan_sha256 = plan.plan_sha256
            AND validation.policy = 'PLANNER_TASK_CONTRACT_V1'
      ))
      OR (json_extract(plan.plan_json, '$.schemaVersion') = 3 AND EXISTS (
          SELECT 1 FROM cleardev_project_execution_admissions AS admission
          WHERE admission.plan_id = plan.id
            AND json_extract(admission.contract_json, '$.policy') IS 'PROJECT_EXECUTION_V1'
            AND json_extract(admission.contract_json, '$.planSha256') = plan.plan_sha256
            AND json_extract(admission.contract_json, '$.requirementVersionId') = plan.requirement_version_id
            AND json_extract(admission.contract_json, '$.requirementSha256') = plan.requirement_sha256
      ))
  )
ORDER BY plan.version DESC
LIMIT 1;

-- name: GetClearDevWorkItemDispatchBinding :one
SELECT id, accepted_dispatch_id, dispatch_base_commit_sha
FROM cleardev_work_items
WHERE id = ?;

-- name: GetClearDevHumanDecisionRequestByDirectionRequestID :one
SELECT *
FROM cleardev_human_decision_requests
WHERE decision_kind = 'APPROVE_DIRECTION_CHANGE'
  AND json_extract(binding_json, '$.directionRequestId') = sqlc.arg(direction_request_id);

-- name: CountClearDevDirectionHistoryForVersion :one
SELECT COUNT(*)
FROM cleardev_direction_intents
WHERE requirement_version_id = ?;

-- name: BindClearDevWorkItemAcceptedDispatch :execrows
UPDATE cleardev_work_items
SET accepted_dispatch_id = sqlc.arg(accepted_dispatch_id),
    dispatch_base_commit_sha = sqlc.arg(dispatch_base_commit_sha),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND accepted_dispatch_id IS NULL
  AND dispatch_base_commit_sha IS NULL
  AND state = 'RUNNING';
