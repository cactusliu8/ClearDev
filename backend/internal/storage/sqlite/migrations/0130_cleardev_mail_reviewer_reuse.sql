-- +goose Up
-- One mail task may reuse its ended REWORK reviewer's session for round 1.
-- No old binding, review, attempt, or candidate is changed. Fixed recovery
-- remains a different authorization path; its same-candidate rules are intact.
CREATE VIEW cleardev_mail_reviewer_rework_sources AS
SELECT prior.id AS prior_binding_id, prior.execution_run_id, prior.task_mapping_id,
       prior.ao_session_id, prior.workspace_path, prior.session_creation_idempotency_key AS prior_session_key,
       old_candidate.commit_sha AS prior_sha, candidate.id AS candidate_id, candidate.commit_sha AS candidate_sha
FROM cleardev_complex_execution_role_bindings AS prior
JOIN cleardev_complex_execution_reviews AS review ON review.reviewer_role_binding_id = prior.id
JOIN cleardev_complex_execution_task_attempts AS old_attempt ON old_attempt.id = review.task_attempt_id
JOIN cleardev_candidate_commits AS old_candidate ON old_candidate.id = prior.candidate_commit_id
JOIN cleardev_complex_execution_task_attempts AS attempt
  ON attempt.task_mapping_id = prior.task_mapping_id AND attempt.round = 1
JOIN cleardev_candidate_commits AS candidate ON candidate.complex_execution_task_attempt_id = attempt.id
JOIN cleardev_complex_execution_runs AS run ON run.id = prior.execution_run_id
JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
JOIN sessions AS session ON session.id = prior.ao_session_id
WHERE prior.role = 'REVIEWER' AND prior.status = 'ENDED'
  AND review.status = 'SETTLED' AND review.verdict = 'REWORK'
  AND review.candidate_commit_id = old_candidate.id
  AND old_attempt.execution_run_id = run.id AND old_attempt.task_mapping_id = prior.task_mapping_id
  AND old_attempt.round = 0 AND old_attempt.status = 'REWORK'
  AND attempt.execution_run_id = run.id AND attempt.base_commit_sha = old_attempt.base_commit_sha
  AND old_candidate.complex_execution_task_attempt_id = old_attempt.id
  AND candidate.commit_sha <> old_candidate.commit_sha
  AND run.mode = 'STANDARD' AND run.fixed_builder_count = 1 AND run.status = 'ACCEPTED'
  AND json_extract(run.execution_package_json, '$.deliveryPolicy') = 'MAIL_INCREMENT_V1'
  AND json_extract(run.execution_package_json, '$.deliveryBaseSha') = old_attempt.base_commit_sha
  AND (SELECT count(*) FROM cleardev_complex_execution_task_mappings WHERE execution_run_id = run.id) = 1
  AND project.cancelled_at IS NULL AND project.state <> 'PAUSED'
  AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id = run.requirement_version_id AND status = 'ACTIVE')
  AND session.project_id = project.ao_project_id AND session.kind = 'worker' AND session.harness = 'codex'
  AND session.session_mode = 'chat' AND session.permission_mode = 'auto'
  AND session.is_terminated = FALSE AND session.activity_state <> 'exited'
  AND session.workspace_path = prior.workspace_path
  AND session.creation_idempotency_key = prior.session_creation_idempotency_key
  AND NOT EXISTS (SELECT 1 FROM cleardev_complex_role_bindings WHERE development_project_id = run.development_project_id AND ao_session_id = session.id)
  AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE execution_run_id = run.id AND role <> 'REVIEWER' AND (ao_session_id = session.id OR workspace_path = prior.workspace_path));

DROP INDEX idx_cleardev_complex_execution_session;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_session
 ON cleardev_complex_execution_role_bindings(execution_run_id, ao_session_id)
 WHERE ao_session_id IS NOT NULL AND NOT (role = 'REVIEWER' AND session_creation_idempotency_key GLOB 'cleardev-mail-review-rework:*');

DROP TRIGGER cleardev_fixed_reviewer_authorization;
-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_reviewer_authorization BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NEW.role='REVIEWER' AND NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_fixed_recovery_claims AS claim ON claim.request_id=request.id
 WHERE request.execution_run_id=NEW.execution_run_id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
 AND NEW.id=claim.operation_id||':reviewer' AND NEW.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND NEW.continuation_of_role_binding_id=json_extract(request.request_json,'$.roleBindingId')
 AND NEW.task_mapping_id=json_extract(request.request_json,'$.taskId') AND NEW.candidate_commit_id=json_extract(request.request_json,'$.candidateId')
) AND NOT EXISTS (
 SELECT 1 FROM cleardev_mail_reviewer_rework_sources AS source
 WHERE NEW.status='REQUESTED' AND NEW.execution_run_id=source.execution_run_id
 AND NEW.continuation_of_role_binding_id=source.prior_binding_id AND NEW.task_mapping_id=source.task_mapping_id
 AND NEW.candidate_commit_id=source.candidate_id
 AND NEW.session_creation_idempotency_key='cleardev-mail-review-rework:'||source.candidate_id
) BEGIN SELECT RAISE(ABORT,'replacement Reviewer requires exact fixed recovery or mail rework authorization'); END;
-- +goose StatementEnd

-- Reserve the deterministic reuse key; no other role can exploit the index exception.
-- +goose StatementBegin
CREATE TRIGGER cleardev_mail_reviewer_rework_key BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NEW.session_creation_idempotency_key GLOB 'cleardev-mail-review-rework:*' AND NOT EXISTS (
 SELECT 1 FROM cleardev_mail_reviewer_rework_sources AS source
 WHERE NEW.role='REVIEWER' AND NEW.status='REQUESTED' AND NEW.execution_run_id=source.execution_run_id
 AND NEW.continuation_of_role_binding_id=source.prior_binding_id AND NEW.task_mapping_id=source.task_mapping_id
 AND NEW.candidate_commit_id=source.candidate_id
 AND NEW.session_creation_idempotency_key='cleardev-mail-review-rework:'||source.candidate_id
) BEGIN SELECT RAISE(ABORT,'mail Reviewer reuse key has no exact REWORK predecessor'); END;
-- +goose StatementEnd

DROP TRIGGER cleardev_complex_execution_role_binding_update_valid;
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_role_binding_update_valid
BEFORE UPDATE ON cleardev_complex_execution_role_bindings
WHEN (
 OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.role IS NOT NEW.role
 OR OLD.source_complex_role_binding_id IS NOT NEW.source_complex_role_binding_id
 OR OLD.continuation_of_role_binding_id IS NOT NEW.continuation_of_role_binding_id
 OR OLD.task_mapping_id IS NOT NEW.task_mapping_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.session_creation_idempotency_key IS NOT NEW.session_creation_idempotency_key
 OR OLD.builder_slot IS NOT NEW.builder_slot
 OR (NEW.status = 'BOUND' AND NOT EXISTS (
   SELECT 1 FROM sessions AS session
   JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
   JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
   WHERE run.id = NEW.execution_run_id AND session.id = NEW.ao_session_id
     AND session.harness = 'codex' AND session.session_mode = 'chat' AND session.permission_mode = 'auto'
     AND session.workspace_path = NEW.workspace_path
     AND ((NEW.role = 'STEWARD' AND session.kind = 'orchestrator' AND NEW.base_commit_sha = '' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key)
       OR (NEW.role = 'BUILDER' AND session.kind = 'worker' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key
           AND (session.diff_base_sha = NEW.base_commit_sha OR EXISTS (SELECT 1 FROM cleardev_complex_execution_compositions AS composition WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha = NEW.base_commit_sha)))
       OR (NEW.role = 'REVIEWER' AND session.kind = 'worker' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key))
 ))
 OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER') AND (length(trim(NEW.workspace_path)) = 0 OR length(NEW.base_commit_sha) <> 40 OR NEW.base_commit_sha GLOB '*[^0-9a-f]*'))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND NOT EXISTS (
   SELECT 1 FROM cleardev_candidate_commits AS candidate
   JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = candidate.complex_execution_task_attempt_id
   JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
   WHERE candidate.id = NEW.candidate_commit_id AND candidate.commit_sha = NEW.base_commit_sha AND task.id = NEW.task_mapping_id AND task.execution_run_id = NEW.execution_run_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_role_bindings AS existing WHERE existing.execution_run_id = NEW.execution_run_id AND existing.id <> NEW.id AND existing.ao_session_id = NEW.ao_session_id))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_runs AS run JOIN cleardev_complex_role_bindings AS source ON source.development_project_id = run.development_project_id WHERE run.id = NEW.execution_run_id AND source.role IN ('STEWARD', 'ENGINEERING_PLANNER') AND source.status = 'BOUND' AND source.ao_session_id = NEW.ao_session_id))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_role_bindings AS builder WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER' AND builder.workspace_path = NEW.workspace_path))
 OR (NEW.role = 'STEWARD' AND NEW.status = 'BOUND' AND NOT EXISTS (SELECT 1 FROM cleardev_complex_role_bindings AS source JOIN sessions AS source_session ON source_session.id = source.ao_session_id WHERE source.id = NEW.source_complex_role_binding_id AND (source_session.is_terminated = TRUE OR source_session.activity_state = 'exited') AND source.ao_session_id <> NEW.ao_session_id))
 OR (OLD.status = 'BOUND' AND (
   OLD.ao_session_id IS NOT NEW.ao_session_id OR OLD.workspace_path IS NOT NEW.workspace_path
   OR (OLD.base_commit_sha IS NOT NEW.base_commit_sha AND NOT (NEW.role = 'BUILDER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_runs AS run WHERE run.id = NEW.execution_run_id AND run.mode = 'PARALLEL' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_compositions AS composition WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha = NEW.base_commit_sha))))
   OR OLD.bound_at IS NOT NEW.bound_at
 ))
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status = 'REQUESTED' AND NEW.status NOT IN ('BOUND', 'FAILED'))
 OR (OLD.status = 'BOUND' AND NEW.status <> 'ENDED' AND NOT (NEW.role = 'BUILDER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_runs AS run WHERE run.id = NEW.execution_run_id AND run.mode = 'PARALLEL' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_compositions AS composition WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha = NEW.base_commit_sha))))
 OR OLD.status IN ('FAILED', 'ENDED')
) AND NOT (
 OLD.status='REQUESTED' AND NEW.status='BOUND' AND NEW.role='REVIEWER'
 AND OLD.id IS NEW.id AND OLD.execution_run_id IS NEW.execution_run_id AND OLD.role IS NEW.role
 AND OLD.source_complex_role_binding_id IS NEW.source_complex_role_binding_id
 AND OLD.continuation_of_role_binding_id IS NEW.continuation_of_role_binding_id
 AND OLD.task_mapping_id IS NEW.task_mapping_id AND OLD.candidate_commit_id IS NEW.candidate_commit_id
 AND OLD.session_creation_idempotency_key IS NEW.session_creation_idempotency_key
 AND OLD.builder_slot IS NEW.builder_slot AND OLD.requested_at IS NEW.requested_at
 AND EXISTS (
   SELECT 1 FROM cleardev_mail_reviewer_rework_sources AS source
   WHERE NEW.execution_run_id=source.execution_run_id AND NEW.continuation_of_role_binding_id=source.prior_binding_id
   AND NEW.task_mapping_id=source.task_mapping_id AND NEW.candidate_commit_id=source.candidate_id
   AND NEW.ao_session_id=source.ao_session_id AND NEW.workspace_path=source.workspace_path AND NEW.base_commit_sha=source.candidate_sha
   AND NEW.session_creation_idempotency_key='cleardev-mail-review-rework:'||source.candidate_id
   AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_role_bindings AS other WHERE other.execution_run_id=source.execution_run_id AND other.ao_session_id=source.ao_session_id AND other.id NOT IN (source.prior_binding_id, NEW.id))
 )
)
BEGIN SELECT RAISE(ABORT, 'cleardev complex execution role binding is immutable or invalid'); END;
-- +goose StatementEnd

-- +goose Down
-- Do not erase a recorded reuse relationship to make an older binary accept it.
CREATE TEMP TABLE cleardev_reviewer_reuse_down_guard (n INTEGER CHECK(n=0));
INSERT INTO cleardev_reviewer_reuse_down_guard SELECT count(*) FROM cleardev_complex_execution_role_bindings WHERE session_creation_idempotency_key GLOB 'cleardev-mail-review-rework:*';
DROP TABLE cleardev_reviewer_reuse_down_guard;
DROP TRIGGER cleardev_mail_reviewer_rework_key;
DROP TRIGGER cleardev_fixed_reviewer_authorization;
-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_reviewer_authorization BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NEW.role='REVIEWER' AND NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_fixed_recovery_claims AS claim ON claim.request_id=request.id
 WHERE request.execution_run_id=NEW.execution_run_id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
 AND NEW.id=claim.operation_id||':reviewer' AND NEW.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND NEW.continuation_of_role_binding_id=json_extract(request.request_json,'$.roleBindingId')
 AND NEW.task_mapping_id=json_extract(request.request_json,'$.taskId') AND NEW.candidate_commit_id=json_extract(request.request_json,'$.candidateId')
) BEGIN SELECT RAISE(ABORT,'replacement Reviewer requires exact fixed recovery authorization'); END;
-- +goose StatementEnd
DROP INDEX idx_cleardev_complex_execution_session;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_session ON cleardev_complex_execution_role_bindings(execution_run_id, ao_session_id) WHERE ao_session_id IS NOT NULL;
DROP TRIGGER cleardev_complex_execution_role_binding_update_valid;
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_role_binding_update_valid
BEFORE UPDATE ON cleardev_complex_execution_role_bindings
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.role IS NOT NEW.role
 OR OLD.source_complex_role_binding_id IS NOT NEW.source_complex_role_binding_id
 OR OLD.continuation_of_role_binding_id IS NOT NEW.continuation_of_role_binding_id
 OR OLD.task_mapping_id IS NOT NEW.task_mapping_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.session_creation_idempotency_key IS NOT NEW.session_creation_idempotency_key
 OR OLD.builder_slot IS NOT NEW.builder_slot
 OR (NEW.status = 'BOUND' AND NOT EXISTS (
   SELECT 1 FROM sessions AS session
   JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
   JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
   WHERE run.id = NEW.execution_run_id AND session.id = NEW.ao_session_id
     AND session.harness = 'codex' AND session.session_mode = 'chat' AND session.permission_mode = 'auto'
     AND session.workspace_path = NEW.workspace_path
     AND ((NEW.role = 'STEWARD' AND session.kind = 'orchestrator' AND NEW.base_commit_sha = '' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key)
       OR (NEW.role = 'BUILDER' AND session.kind = 'worker' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key
           AND (session.diff_base_sha = NEW.base_commit_sha OR EXISTS (SELECT 1 FROM cleardev_complex_execution_compositions AS composition WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha = NEW.base_commit_sha)))
       OR (NEW.role = 'REVIEWER' AND session.kind = 'worker' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key))
 ))
 OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER') AND (length(trim(NEW.workspace_path)) = 0 OR length(NEW.base_commit_sha) <> 40 OR NEW.base_commit_sha GLOB '*[^0-9a-f]*'))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND NOT EXISTS (
   SELECT 1 FROM cleardev_candidate_commits AS candidate
   JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = candidate.complex_execution_task_attempt_id
   JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
   WHERE candidate.id = NEW.candidate_commit_id AND candidate.commit_sha = NEW.base_commit_sha AND task.id = NEW.task_mapping_id AND task.execution_run_id = NEW.execution_run_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_role_bindings AS existing WHERE existing.execution_run_id = NEW.execution_run_id AND existing.id <> NEW.id AND existing.ao_session_id = NEW.ao_session_id))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_runs AS run JOIN cleardev_complex_role_bindings AS source ON source.development_project_id = run.development_project_id WHERE run.id = NEW.execution_run_id AND source.role IN ('STEWARD', 'ENGINEERING_PLANNER') AND source.status = 'BOUND' AND source.ao_session_id = NEW.ao_session_id))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_role_bindings AS builder WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER' AND builder.workspace_path = NEW.workspace_path))
 OR (NEW.role = 'STEWARD' AND NEW.status = 'BOUND' AND NOT EXISTS (SELECT 1 FROM cleardev_complex_role_bindings AS source JOIN sessions AS source_session ON source_session.id = source.ao_session_id WHERE source.id = NEW.source_complex_role_binding_id AND (source_session.is_terminated = TRUE OR source_session.activity_state = 'exited') AND source.ao_session_id <> NEW.ao_session_id))
 OR (OLD.status = 'BOUND' AND (
   OLD.ao_session_id IS NOT NEW.ao_session_id OR OLD.workspace_path IS NOT NEW.workspace_path
   OR (OLD.base_commit_sha IS NOT NEW.base_commit_sha AND NOT (NEW.role = 'BUILDER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_runs AS run WHERE run.id = NEW.execution_run_id AND run.mode = 'PARALLEL' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_compositions AS composition WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha = NEW.base_commit_sha))))
   OR OLD.bound_at IS NOT NEW.bound_at
 ))
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status = 'REQUESTED' AND NEW.status NOT IN ('BOUND', 'FAILED'))
 OR (OLD.status = 'BOUND' AND NEW.status <> 'ENDED' AND NOT (NEW.role = 'BUILDER' AND NEW.status = 'BOUND' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_runs AS run WHERE run.id = NEW.execution_run_id AND run.mode = 'PARALLEL' AND EXISTS (SELECT 1 FROM cleardev_complex_execution_compositions AS composition WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha = NEW.base_commit_sha))))
 OR OLD.status IN ('FAILED', 'ENDED')
BEGIN SELECT RAISE(ABORT, 'cleardev complex execution role binding is immutable or invalid'); END;
-- +goose StatementEnd
DROP VIEW cleardev_mail_reviewer_rework_sources;
