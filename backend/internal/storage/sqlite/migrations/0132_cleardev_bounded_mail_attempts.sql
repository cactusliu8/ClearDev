-- +goose Up
-- +goose NO TRANSACTION
-- The two old table CHECKs physically capped dispatches at two and Reviewer
-- steps at two. Rebuild atomically, retaining every old row and all guards.
-- Only a newly frozen MAIL_ATTEMPTS_V1 execution can use the larger bounds.
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
PRAGMA legacy_alter_table=ON;
BEGIN IMMEDIATE;
-- Preserve unrelated historical integrity reports. Only this migration's new
-- violations block the rebuild; no old user data is repaired or deleted here.
CREATE TEMP TABLE cleardev_mail_fk_before AS SELECT "table",rowid,parent,fkid FROM pragma_foreign_key_check;
CREATE TABLE cleardev_complex_execution_task_attempts_new (
 id TEXT PRIMARY KEY,
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 task_mapping_id TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
 builder_role_binding_id TEXT NOT NULL REFERENCES cleardev_complex_execution_role_bindings(id),
 agent_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
 round INTEGER NOT NULL CHECK(round BETWEEN 0 AND 4),
 base_commit_sha TEXT NOT NULL CHECK(length(base_commit_sha)=40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'),
 status TEXT NOT NULL CHECK(status IN ('PENDING','RUNNING','OBSERVED','REVIEWING','VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED')),
 reason_code TEXT NOT NULL DEFAULT '', dispatched_at TIMESTAMP, settled_at TIMESTAMP,
 batch_id TEXT REFERENCES cleardev_complex_execution_batches(id),
 UNIQUE(task_mapping_id,round),
 CHECK((status='PENDING' AND dispatched_at IS NULL AND settled_at IS NULL AND reason_code='')
 OR (status IN ('RUNNING','OBSERVED','REVIEWING') AND dispatched_at IS NOT NULL AND settled_at IS NULL AND reason_code='')
 OR (status IN ('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED') AND dispatched_at IS NOT NULL AND settled_at IS NOT NULL AND (status='VERIFIED' OR reason_code<>'')))
);
INSERT INTO cleardev_complex_execution_task_attempts_new SELECT * FROM cleardev_complex_execution_task_attempts;
DROP TABLE cleardev_complex_execution_task_attempts;
ALTER TABLE cleardev_complex_execution_task_attempts_new RENAME TO cleardev_complex_execution_task_attempts;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_active_attempt ON cleardev_complex_execution_task_attempts(task_mapping_id) WHERE status IN ('PENDING','RUNNING','OBSERVED','REVIEWING');
CREATE UNIQUE INDEX idx_cleardev_complex_execution_builder_active_attempt ON cleardev_complex_execution_task_attempts(builder_role_binding_id) WHERE status IN ('PENDING','RUNNING','OBSERVED','REVIEWING');
CREATE TABLE cleardev_complex_exception_budgets_new (
 id TEXT PRIMARY KEY, execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 complex_execution_task_id TEXT REFERENCES cleardev_complex_execution_task_mappings(id),
 role_kind TEXT NOT NULL CHECK(role_kind IN ('BUILDER','REVIEWER','STEWARD_EXCEPTION','SPECIALIST','RECOVERY')),
 allowed_agent_types_json TEXT NOT NULL CHECK(json_valid(allowed_agent_types_json) AND json_type(allowed_agent_types_json)='array'),
 model_selection TEXT NOT NULL CHECK(model_selection='codex-chat'),
 max_turns INTEGER NOT NULL CHECK(max_turns BETWEEN 1 AND 6),
 used_turns INTEGER NOT NULL DEFAULT 0 CHECK(used_turns>=0 AND used_turns<=max_turns),
 max_rework_count INTEGER NOT NULL DEFAULT 0 CHECK(max_rework_count>=0), created_at TIMESTAMP NOT NULL,
 CHECK((role_kind='RECOVERY' AND complex_execution_task_id IS NULL AND max_turns=1)
 OR (role_kind='BUILDER' AND complex_execution_task_id IS NOT NULL AND max_turns IN (3,5) AND max_rework_count=1)
 OR (role_kind='REVIEWER' AND complex_execution_task_id IS NOT NULL AND max_turns IN (2,6))
 OR (role_kind IN ('STEWARD_EXCEPTION','SPECIALIST') AND complex_execution_task_id IS NOT NULL AND max_turns=1))
);
INSERT INTO cleardev_complex_exception_budgets_new SELECT * FROM cleardev_complex_exception_budgets;
DROP TABLE cleardev_complex_exception_budgets;
ALTER TABLE cleardev_complex_exception_budgets_new RENAME TO cleardev_complex_exception_budgets;
CREATE UNIQUE INDEX idx_cleardev_complex_exception_budget_role ON cleardev_complex_exception_budgets(execution_run_id,IFNULL(complex_execution_task_id,''),role_kind);
CREATE VIEW IF NOT EXISTS cleardev_bounded_mail_runs AS
 SELECT * FROM cleardev_complex_execution_runs WHERE mode='STANDARD' AND fixed_builder_count=1
 AND json_extract(execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
 AND json_extract(execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1';
CREATE TABLE IF NOT EXISTS cleardev_mail_attempt_slots (
 dispatch_id TEXT PRIMARY KEY REFERENCES cleardev_complex_execution_task_attempts(id) DEFERRABLE INITIALLY DEFERRED,
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 task_id TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
 round INTEGER NOT NULL CHECK(round BETWEEN 0 AND 4),
 attempt_kind TEXT NOT NULL CHECK(attempt_kind IN ('DEVELOPMENT','REVIEW_REPAIR','HUMAN_EXTRA')),
 grant_request_id TEXT UNIQUE REFERENCES cleardev_human_decision_requests(id), created_at TIMESTAMP NOT NULL,
 UNIQUE(task_id,round), CHECK((attempt_kind='HUMAN_EXTRA')=(grant_request_id IS NOT NULL))
);
CREATE TRIGGER IF NOT EXISTS cleardev_mail_attempt_slot_insert BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs r JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 JOIN cleardev_development_projects p ON p.id=r.development_project_id JOIN cleardev_contract_versions v ON v.id=r.requirement_version_id
 WHERE r.id=NEW.execution_run_id AND t.id=NEW.task_id AND r.status='ACCEPTED' AND p.cancelled_at IS NULL AND p.state<>'PAUSED'
 AND v.state='APPROVED' AND v.superseded_by_id IS NULL AND v.sha256=r.requirement_sha256 AND v.task_set_version=r.accepted_task_set_version
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR NEW.round<>(SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id)
 OR (NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1
 AND (a.status='REWORK' OR (NEW.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN' AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED'))))
 OR (NEW.attempt_kind='DEVELOPMENT' AND ((SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='DEVELOPMENT')>=3
 OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind<>'DEVELOPMENT')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='REVIEW_REPAIR' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='REVIEW_REPAIR')
 OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1 AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='HUMAN_EXTRA' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
 OR NOT EXISTS(SELECT 1 FROM cleardev_human_decision_requests h JOIN cleardev_complex_execution_task_attempts a ON a.id=json_extract(h.binding_json,'$.dispatchId')
 JOIN cleardev_candidate_commits c ON c.complex_execution_task_attempt_id=a.id JOIN cleardev_bounded_mail_runs r ON r.id=a.execution_run_id
 WHERE h.id=NEW.grant_request_id AND h.decision_kind='AUTHORIZE_MAIL_EXTRA_ATTEMPT' AND h.status='RESOLVED' AND h.decision='APPROVE'
 AND json_extract(h.binding_json,'$.executionRunId')=NEW.execution_run_id AND json_extract(h.binding_json,'$.taskId')=NEW.task_id
 AND json_extract(h.binding_json,'$.nextRound')=NEW.round AND json_extract(h.binding_json,'$.candidateSha')=c.commit_sha
 AND json_extract(h.binding_json,'$.planSha256')=r.plan_sha256 AND json_extract(h.binding_json,'$.requirementVersionSha256')=r.requirement_sha256
 AND a.round=NEW.round-1 AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED')))
BEGIN SELECT RAISE(ABORT,'mail attempt has no bounded automatic or exact human authorization'); END;
CREATE TRIGGER IF NOT EXISTS cleardev_mail_attempt_slot_update BEFORE UPDATE ON cleardev_mail_attempt_slots BEGIN SELECT RAISE(ABORT,'mail attempt slots are immutable'); END;
CREATE TRIGGER IF NOT EXISTS cleardev_mail_attempt_slot_delete BEFORE DELETE ON cleardev_mail_attempt_slots BEGIN SELECT RAISE(ABORT,'mail attempt slots are append-only'); END;
CREATE TRIGGER cleardev_complex_execution_attempt_insert_valid BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id
 JOIN cleardev_complex_execution_role_bindings b ON b.id=NEW.builder_role_binding_id JOIN cleardev_complex_execution_agent_steps s ON s.id=NEW.agent_step_id
 WHERE t.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.status='ACCEPTED' AND b.execution_run_id=r.id AND b.role='BUILDER' AND b.status='BOUND'
 AND s.role_binding_id=b.id AND s.step_kind='BUILDER_TASK' AND s.request_id=NEW.id
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.execution_run_id=NEW.execution_run_id AND s.task_id=NEW.task_mapping_id AND s.round=NEW.round))
 OR (NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NEW.round NOT IN(0,1))
 OR (NEW.batch_id IS NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND mode='STANDARD'))
 OR (NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b JOIN cleardev_complex_execution_runs r ON r.id=b.execution_run_id JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 WHERE b.id=NEW.batch_id AND r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND b.status='RUNNING' AND b.common_base_sha=NEW.base_commit_sha AND t.id=NEW.task_mapping_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key)))
 OR (NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_mapping_id AND a.round=NEW.round-1 AND a.base_commit_sha=NEW.base_commit_sha AND (NEW.batch_id IS NULL OR a.batch_id=NEW.batch_id)
 AND (a.status='REWORK' OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN'))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d LEFT JOIN cleardev_complex_execution_verified_candidates v ON v.task_mapping_id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND v.id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d JOIN cleardev_complex_execution_task_mappings t ON t.id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b WHERE b.execution_run_id=NEW.execution_run_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key) AND b.ordinal<(SELECT ordinal FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
CREATE TRIGGER cleardev_complex_execution_attempt_append_only_delete BEFORE DELETE ON cleardev_complex_execution_task_attempts BEGIN SELECT RAISE(ABORT,'cleardev complex execution task attempts are append-only'); END;
CREATE TRIGGER cleardev_complex_execution_attempts_cdc_insert AFTER INSERT ON cleardev_complex_execution_task_attempts BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT p.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',p.id,'executionRunId',r.id),COALESCE(NEW.dispatched_at,r.requested_at) FROM cleardev_complex_execution_runs r JOIN cleardev_development_projects p ON p.id=r.development_project_id WHERE r.id=NEW.execution_run_id;
END;
CREATE TRIGGER cleardev_complex_execution_attempts_cdc_update AFTER UPDATE ON cleardev_complex_execution_task_attempts BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at) SELECT p.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',p.id,'executionRunId',r.id),COALESCE(NEW.settled_at,NEW.dispatched_at,r.requested_at) FROM cleardev_complex_execution_runs r JOIN cleardev_development_projects p ON p.id=r.development_project_id WHERE r.id=NEW.execution_run_id;
END;
CREATE TRIGGER cleardev_mail_role_budget_insert BEFORE INSERT ON cleardev_complex_exception_budgets
WHEN (NEW.role_kind='BUILDER' AND NEW.max_turns<>CASE WHEN EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) THEN 5 ELSE 3 END)
 OR (NEW.role_kind='REVIEWER' AND NEW.max_turns<>CASE WHEN EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) THEN 6 ELSE 2 END)
BEGIN SELECT RAISE(ABORT,'role budget does not match the frozen mail attempt policy'); END;
CREATE TRIGGER cleardev_complex_exception_budget_update_valid BEFORE UPDATE ON cleardev_complex_exception_budgets
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.complex_execution_task_id IS NOT NEW.complex_execution_task_id OR OLD.role_kind IS NOT NEW.role_kind OR OLD.allowed_agent_types_json IS NOT NEW.allowed_agent_types_json OR OLD.model_selection IS NOT NEW.model_selection OR OLD.max_turns IS NOT NEW.max_turns OR OLD.max_rework_count IS NOT NEW.max_rework_count OR OLD.created_at IS NOT NEW.created_at OR NEW.used_turns<>OLD.used_turns+1 OR NEW.used_turns>NEW.max_turns
BEGIN SELECT RAISE(ABORT,'complex exception budget is immutable or exhausted'); END;
-- Extend the existing same-session relation only for the versioned policy.
CREATE VIEW IF NOT EXISTS cleardev_mail_reviewer_ancestry AS
 WITH RECURSIVE chain(descendant,ancestor,depth) AS (
 SELECT id AS descendant,id AS ancestor,0 AS depth FROM cleardev_complex_execution_role_bindings WHERE role='REVIEWER'
 UNION ALL SELECT chain.descendant AS descendant,p.id AS ancestor,chain.depth+1 AS depth FROM chain
 JOIN cleardev_complex_execution_role_bindings b ON b.id=chain.ancestor
 JOIN cleardev_complex_execution_role_bindings p ON p.id=b.continuation_of_role_binding_id
 WHERE chain.depth<4 AND p.role='REVIEWER' AND p.execution_run_id=b.execution_run_id AND p.task_mapping_id=b.task_mapping_id AND p.ao_session_id=b.ao_session_id
 ) SELECT descendant,ancestor FROM chain;
DROP VIEW cleardev_mail_reviewer_rework_sources;
CREATE VIEW cleardev_mail_reviewer_rework_sources AS
SELECT prior.id AS prior_binding_id, prior.execution_run_id, prior.task_mapping_id,
 prior.ao_session_id,prior.workspace_path,prior.session_creation_idempotency_key AS prior_session_key,
 old_candidate.commit_sha AS prior_sha,candidate.id AS candidate_id,candidate.commit_sha AS candidate_sha
FROM cleardev_complex_execution_role_bindings prior
JOIN cleardev_complex_execution_reviews review ON review.reviewer_role_binding_id=prior.id
JOIN cleardev_complex_execution_task_attempts old_attempt ON old_attempt.id=review.task_attempt_id
JOIN cleardev_candidate_commits old_candidate ON old_candidate.id=prior.candidate_commit_id
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.task_mapping_id=prior.task_mapping_id
JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_runs run ON run.id=prior.execution_run_id
JOIN cleardev_development_projects project ON project.id=run.development_project_id
JOIN sessions session ON session.id=prior.ao_session_id
WHERE prior.role='REVIEWER' AND prior.status='ENDED' AND review.status='SETTLED' AND review.verdict='REWORK'
 AND review.candidate_commit_id=old_candidate.id AND old_candidate.complex_execution_task_attempt_id=old_attempt.id
 AND old_attempt.execution_run_id=run.id AND old_attempt.task_mapping_id=prior.task_mapping_id
 AND attempt.execution_run_id=run.id AND attempt.base_commit_sha=old_attempt.base_commit_sha AND candidate.commit_sha<>old_candidate.commit_sha
 AND ((old_attempt.round=0 AND attempt.round=1 AND old_attempt.status='REWORK' AND session.creation_idempotency_key=prior.session_creation_idempotency_key)
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id) AND attempt.round>old_attempt.round
 AND (old_attempt.status='REWORK' OR (old_attempt.status='NEEDS_HUMAN' AND old_attempt.reason_code='MAIL_ATTEMPTS_EXHAUSTED'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews newer JOIN cleardev_complex_execution_task_attempts a ON a.id=newer.task_attempt_id WHERE a.task_mapping_id=prior.task_mapping_id AND a.round>old_attempt.round AND a.round<attempt.round)
 AND EXISTS(SELECT 1 FROM cleardev_mail_reviewer_ancestry chain JOIN cleardev_complex_execution_role_bindings root ON root.id=chain.ancestor WHERE chain.descendant=prior.id AND root.session_creation_idempotency_key=session.creation_idempotency_key)))
 AND run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED'
 AND json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
 AND json_extract(run.execution_package_json,'$.deliveryBaseSha')=old_attempt.base_commit_sha
 AND (SELECT count(*) FROM cleardev_complex_execution_task_mappings WHERE execution_run_id=run.id)=1
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND session.project_id=project.ao_project_id AND session.kind='worker' AND session.harness='codex' AND session.session_mode='chat' AND session.permission_mode='auto'
 AND session.is_terminated=FALSE AND session.activity_state<>'exited' AND session.workspace_path=prior.workspace_path
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_role_bindings WHERE development_project_id=run.development_project_id AND ao_session_id=session.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE execution_run_id=run.id AND role<>'REVIEWER' AND (ao_session_id=session.id OR workspace_path=prior.workspace_path));
DROP TRIGGER cleardev_complex_execution_role_binding_update_valid;
CREATE TRIGGER cleardev_complex_execution_role_binding_update_valid BEFORE UPDATE ON cleardev_complex_execution_role_bindings
WHEN (
 OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.role IS NOT NEW.role OR OLD.source_complex_role_binding_id IS NOT NEW.source_complex_role_binding_id
 OR OLD.continuation_of_role_binding_id IS NOT NEW.continuation_of_role_binding_id OR OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.session_creation_idempotency_key IS NOT NEW.session_creation_idempotency_key OR OLD.builder_slot IS NOT NEW.builder_slot
 OR (NEW.status='BOUND' AND NOT EXISTS(SELECT 1 FROM sessions session JOIN cleardev_development_projects project ON project.ao_project_id=session.project_id JOIN cleardev_complex_execution_runs run ON run.development_project_id=project.id
 WHERE run.id=NEW.execution_run_id AND session.id=NEW.ao_session_id AND session.harness='codex' AND session.session_mode='chat' AND session.permission_mode='auto' AND session.workspace_path=NEW.workspace_path
 AND ((NEW.role='STEWARD' AND session.kind='orchestrator' AND NEW.base_commit_sha='' AND session.creation_idempotency_key=NEW.session_creation_idempotency_key)
 OR (NEW.role='BUILDER' AND session.kind='worker' AND session.creation_idempotency_key=NEW.session_creation_idempotency_key AND (session.diff_base_sha=NEW.base_commit_sha OR EXISTS(SELECT 1 FROM cleardev_complex_execution_compositions c WHERE c.execution_run_id=run.id AND c.status='COMPOSED' AND c.output_commit_sha=NEW.base_commit_sha)))
 OR (NEW.role='REVIEWER' AND session.kind='worker' AND session.creation_idempotency_key=NEW.session_creation_idempotency_key))))
 OR (NEW.status='BOUND' AND NEW.role IN('BUILDER','REVIEWER') AND (length(trim(NEW.workspace_path))=0 OR length(NEW.base_commit_sha)<>40 OR NEW.base_commit_sha GLOB '*[^0-9a-f]*'))
 OR (NEW.role='REVIEWER' AND NEW.status='BOUND' AND NOT EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts a ON a.id=c.complex_execution_task_attempt_id JOIN cleardev_complex_execution_task_mappings t ON t.id=a.task_mapping_id WHERE c.id=NEW.candidate_commit_id AND c.commit_sha=NEW.base_commit_sha AND t.id=NEW.task_mapping_id AND t.execution_run_id=NEW.execution_run_id))
 OR (NEW.role='REVIEWER' AND NEW.status='BOUND' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.execution_run_id=NEW.execution_run_id AND b.id<>NEW.id AND b.ao_session_id=NEW.ao_session_id))
 OR (NEW.role='REVIEWER' AND NEW.status='BOUND' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_runs r JOIN cleardev_complex_role_bindings b ON b.development_project_id=r.development_project_id WHERE r.id=NEW.execution_run_id AND b.role IN('STEWARD','ENGINEERING_PLANNER') AND b.status='BOUND' AND b.ao_session_id=NEW.ao_session_id))
 OR (NEW.role='REVIEWER' AND NEW.status='BOUND' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.execution_run_id=NEW.execution_run_id AND b.role='BUILDER' AND b.workspace_path=NEW.workspace_path))
 OR (NEW.role='STEWARD' AND NEW.status='BOUND' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_role_bindings b JOIN sessions s ON s.id=b.ao_session_id WHERE b.id=NEW.source_complex_role_binding_id AND (s.is_terminated=TRUE OR s.activity_state='exited') AND b.ao_session_id<>NEW.ao_session_id))
 OR (OLD.status='BOUND' AND (OLD.ao_session_id IS NOT NEW.ao_session_id OR OLD.workspace_path IS NOT NEW.workspace_path OR OLD.bound_at IS NOT NEW.bound_at
 OR (OLD.base_commit_sha IS NOT NEW.base_commit_sha AND NOT(NEW.role='BUILDER' AND NEW.status='BOUND' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_runs r WHERE r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_compositions c WHERE c.execution_run_id=r.id AND c.status='COMPOSED' AND c.output_commit_sha=NEW.base_commit_sha))))))
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status='REQUESTED' AND NEW.status NOT IN('BOUND','FAILED'))
 OR (OLD.status='BOUND' AND NEW.status<>'ENDED' AND NOT(NEW.role='BUILDER' AND NEW.status='BOUND' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_runs r WHERE r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_compositions c WHERE c.execution_run_id=r.id AND c.status='COMPOSED' AND c.output_commit_sha=NEW.base_commit_sha))))
 OR OLD.status IN('FAILED','ENDED')
) AND NOT (
 OLD.status='REQUESTED' AND NEW.status='BOUND' AND NEW.role='REVIEWER' AND OLD.id IS NEW.id AND OLD.execution_run_id IS NEW.execution_run_id AND OLD.role IS NEW.role
 AND OLD.source_complex_role_binding_id IS NEW.source_complex_role_binding_id AND OLD.continuation_of_role_binding_id IS NEW.continuation_of_role_binding_id
 AND OLD.task_mapping_id IS NEW.task_mapping_id AND OLD.candidate_commit_id IS NEW.candidate_commit_id AND OLD.session_creation_idempotency_key IS NEW.session_creation_idempotency_key
 AND OLD.builder_slot IS NEW.builder_slot AND OLD.requested_at IS NEW.requested_at
 AND EXISTS(SELECT 1 FROM cleardev_mail_reviewer_rework_sources source WHERE NEW.execution_run_id=source.execution_run_id AND NEW.continuation_of_role_binding_id=source.prior_binding_id
 AND NEW.task_mapping_id=source.task_mapping_id AND NEW.candidate_commit_id=source.candidate_id AND NEW.ao_session_id=source.ao_session_id AND NEW.workspace_path=source.workspace_path AND NEW.base_commit_sha=source.candidate_sha
 AND NEW.session_creation_idempotency_key='cleardev-mail-review-rework:'||source.candidate_id
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings other WHERE other.execution_run_id=source.execution_run_id AND other.ao_session_id=source.ao_session_id AND other.id NOT IN(source.prior_binding_id,NEW.id)
 AND NOT(EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=source.execution_run_id) AND other.status='ENDED' AND EXISTS(SELECT 1 FROM cleardev_mail_reviewer_ancestry chain WHERE chain.descendant=source.prior_binding_id AND chain.ancestor=other.id))))
)
BEGIN SELECT RAISE(ABORT,'cleardev complex execution role binding is immutable or invalid'); END;
CREATE TEMP TABLE cleardev_mail_fk_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_mail_fk_guard SELECT CASE WHEN EXISTS(
 SELECT "table",rowid,parent,fkid FROM pragma_foreign_key_check
 EXCEPT SELECT "table",rowid,parent,fkid FROM cleardev_mail_fk_before
) THEN 0 ELSE 1 END;
DROP TABLE cleardev_mail_fk_guard;
DROP TABLE cleardev_mail_fk_before;
COMMIT;
PRAGMA legacy_alter_table=OFF;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- Fail rather than erase any new policy or attempt/authorization history.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_mail_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_mail_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs) OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots) THEN 0 ELSE 1 END;
DROP TABLE cleardev_mail_down_guard;
-- Retain the compatible wider physical tables, but prohibit their new states.
CREATE TRIGGER cleardev_mail_attempt_legacy_only BEFORE INSERT ON cleardev_complex_execution_task_attempts WHEN NEW.round NOT IN(0,1) BEGIN SELECT RAISE(ABORT,'legacy attempt limit'); END;
CREATE TRIGGER cleardev_mail_budget_legacy_only BEFORE INSERT ON cleardev_complex_exception_budgets WHEN (NEW.role_kind='BUILDER' AND NEW.max_turns<>3) OR (NEW.role_kind='REVIEWER' AND NEW.max_turns<>2) BEGIN SELECT RAISE(ABORT,'legacy role budget'); END;
-- +goose StatementEnd
