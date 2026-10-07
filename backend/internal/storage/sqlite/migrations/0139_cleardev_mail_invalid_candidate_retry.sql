-- +goose Up
-- A trusted freeze that produces no new SHA still leaves the last frozen
-- candidate on the task. Automatic DEVELOPMENT and one Extra grant may continue
-- from that SHA; the failed dispatch itself stays NEEDS_HUMAN.
-- +goose StatementBegin
DROP TRIGGER cleardev_mail_attempt_slot_insert;
CREATE TRIGGER cleardev_mail_attempt_slot_insert BEFORE INSERT ON cleardev_mail_attempt_slots
WHEN NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs r JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 JOIN cleardev_development_projects p ON p.id=r.development_project_id JOIN cleardev_contract_versions v ON v.id=r.requirement_version_id
 WHERE r.id=NEW.execution_run_id AND t.id=NEW.task_id AND r.status='ACCEPTED' AND p.cancelled_at IS NULL AND p.state<>'PAUSED'
 AND v.state='APPROVED' AND v.superseded_by_id IS NULL AND v.sha256=r.requirement_sha256 AND v.task_set_version=r.accepted_task_set_version
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR NEW.round<>(SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id)
 OR (NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1
 AND (a.status='REWORK'
  OR (NEW.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN' AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED')
  OR (NEW.attempt_kind='DEVELOPMENT' AND a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_id AND p.execution_run_id=NEW.execution_run_id)
   AND (SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='DEVELOPMENT')<3))))
 OR (NEW.attempt_kind='DEVELOPMENT' AND ((SELECT count(*) FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='DEVELOPMENT')>=3
 OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind<>'DEVELOPMENT')
 OR EXISTS(SELECT 1 FROM cleardev_mail_effective_review_outcomes v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='REVIEW_REPAIR' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='REVIEW_REPAIR')
 OR NOT EXISTS(SELECT 1 FROM cleardev_mail_effective_review_outcomes v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1 AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='HUMAN_EXTRA' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
 OR NOT EXISTS(SELECT 1 FROM cleardev_human_decision_requests h JOIN cleardev_complex_execution_task_attempts a ON a.id=json_extract(h.binding_json,'$.dispatchId')
 JOIN cleardev_candidate_commits c ON c.commit_sha=json_extract(h.binding_json,'$.candidateSha')
 JOIN cleardev_complex_execution_task_attempts owned ON owned.id=c.complex_execution_task_attempt_id
 JOIN cleardev_bounded_mail_runs r ON r.id=a.execution_run_id
 WHERE h.id=NEW.grant_request_id AND h.decision_kind='AUTHORIZE_MAIL_EXTRA_ATTEMPT' AND h.status='RESOLVED' AND h.decision='APPROVE'
 AND json_extract(h.binding_json,'$.executionRunId')=NEW.execution_run_id AND json_extract(h.binding_json,'$.taskId')=NEW.task_id
 AND json_extract(h.binding_json,'$.nextRound')=NEW.round AND json_extract(h.binding_json,'$.planSha256')=r.plan_sha256
 AND json_extract(h.binding_json,'$.requirementVersionSha256')=r.requirement_sha256
 AND a.round=NEW.round-1 AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED'
 AND owned.task_mapping_id=NEW.task_id AND owned.execution_run_id=NEW.execution_run_id)))
BEGIN SELECT RAISE(ABORT,'mail attempt has no bounded automatic or exact human authorization'); END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
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
 AND (a.status='REWORK'
  OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN')
  OR (a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='DEVELOPMENT')
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_mapping_id AND p.execution_run_id=NEW.execution_run_id)))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d LEFT JOIN cleardev_complex_execution_verified_candidates v ON v.task_mapping_id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND v.id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d JOIN cleardev_complex_execution_task_mappings t ON t.id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b WHERE b.execution_run_id=NEW.execution_run_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key) AND b.ordinal<(SELECT ordinal FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER cleardev_mail_attempt_slot_insert;
CREATE TRIGGER cleardev_mail_attempt_slot_insert BEFORE INSERT ON cleardev_mail_attempt_slots
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
 OR EXISTS(SELECT 1 FROM cleardev_mail_effective_review_outcomes v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='REVIEW_REPAIR' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=NEW.task_id AND attempt_kind='REVIEW_REPAIR')
 OR NOT EXISTS(SELECT 1 FROM cleardev_mail_effective_review_outcomes v JOIN cleardev_complex_execution_task_attempts a ON a.id=v.task_attempt_id WHERE a.task_mapping_id=NEW.task_id AND a.round=NEW.round-1 AND v.status='SETTLED' AND v.verdict='REWORK')))
 OR (NEW.attempt_kind='HUMAN_EXTRA' AND (EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE execution_run_id=NEW.execution_run_id AND attempt_kind='HUMAN_EXTRA')
 OR NOT EXISTS(SELECT 1 FROM cleardev_human_decision_requests h JOIN cleardev_complex_execution_task_attempts a ON a.id=json_extract(h.binding_json,'$.dispatchId')
 JOIN cleardev_candidate_commits c ON c.complex_execution_task_attempt_id=a.id JOIN cleardev_bounded_mail_runs r ON r.id=a.execution_run_id
 WHERE h.id=NEW.grant_request_id AND h.decision_kind='AUTHORIZE_MAIL_EXTRA_ATTEMPT' AND h.status='RESOLVED' AND h.decision='APPROVE'
 AND json_extract(h.binding_json,'$.executionRunId')=NEW.execution_run_id AND json_extract(h.binding_json,'$.taskId')=NEW.task_id
 AND json_extract(h.binding_json,'$.nextRound')=NEW.round AND json_extract(h.binding_json,'$.candidateSha')=c.commit_sha
 AND json_extract(h.binding_json,'$.planSha256')=r.plan_sha256 AND json_extract(h.binding_json,'$.requirementVersionSha256')=r.requirement_sha256
 AND a.round=NEW.round-1 AND a.reason_code='MAIL_ATTEMPTS_EXHAUSTED')))
BEGIN SELECT RAISE(ABORT,'mail attempt has no bounded automatic or exact human authorization'); END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
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
-- +goose StatementEnd
