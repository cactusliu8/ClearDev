-- A dispatched rework round produces a new candidate for the same task, but
-- the verified-candidates table allows one row per task mapping, so the second
-- round's verification could never be recorded and the requirement final
-- review had nothing new to judge. The table is rebuilt without that single
-- uniqueness; every other constraint, the stored rows, and all triggers that
-- reference it (four on this table, six on tables it validates) are carried
-- over unchanged.

-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_batch_update_valid;
DROP TRIGGER cleardev_complex_execution_check_run_insert_valid;
DROP TRIGGER cleardev_complex_execution_composition_insert_valid;
DROP TRIGGER cleardev_complex_execution_result_insert_valid;
DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
DROP TRIGGER cleardev_complex_execution_verified_candidate_append_only_delete;
DROP TRIGGER cleardev_complex_execution_verified_candidate_append_only_update;
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
DROP TRIGGER cleardev_fixed_verification_immutable;
DROP TRIGGER cleardev_complex_execution_integration_candidate_binding_valid;
CREATE TABLE cleardev_complex_execution_verified_candidates_v163 (
    id                       TEXT PRIMARY KEY,
    task_mapping_id          TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    task_attempt_id          TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id      TEXT NOT NULL UNIQUE REFERENCES cleardev_candidate_commits(id),
    scope_check_run_id       TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_check_runs(id),
    required_check_runs_json TEXT NOT NULL CHECK (json_valid(required_check_runs_json) AND json_type(required_check_runs_json) = 'array'),
    review_id                TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_reviews(id),
    verified_at              TIMESTAMP NOT NULL,
    replacement_recovery_id TEXT REFERENCES cleardev_replacement_review_results(recovery_request_id),
    replacement_attempt_id TEXT REFERENCES cleardev_agent_step_attempts(id),
    replacement_result_id TEXT REFERENCES cleardev_agent_step_results(id)
);
INSERT INTO cleardev_complex_execution_verified_candidates_v163 (id, task_mapping_id, task_attempt_id, candidate_commit_id, scope_check_run_id, required_check_runs_json, review_id, verified_at, replacement_recovery_id, replacement_attempt_id, replacement_result_id)
SELECT id, task_mapping_id, task_attempt_id, candidate_commit_id, scope_check_run_id, required_check_runs_json, review_id, verified_at, replacement_recovery_id, replacement_attempt_id, replacement_result_id FROM cleardev_complex_execution_verified_candidates;
DROP TABLE cleardev_complex_execution_verified_candidates;
ALTER TABLE cleardev_complex_execution_verified_candidates_v163 RENAME TO cleardev_complex_execution_verified_candidates;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_batch_update_valid
BEFORE UPDATE ON cleardev_complex_execution_batches
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.task_keys_json IS NOT NEW.task_keys_json
 OR OLD.ordinal IS NOT NEW.ordinal
 OR OLD.created_at IS NOT NEW.created_at
 OR (OLD.common_base_sha <> '' AND NEW.common_base_sha <> OLD.common_base_sha)
 OR (OLD.status = 'COMPOSED' OR OLD.status = 'BLOCKED')
 OR (NEW.common_base_sha <> OLD.common_base_sha AND NOT (
    OLD.common_base_sha = '' AND NEW.status IN ('PENDING', 'RUNNING')
    AND (
      (OLD.ordinal = 0 AND NEW.common_base_sha = (
          SELECT builder.base_commit_sha
          FROM cleardev_complex_execution_role_bindings AS builder
          JOIN cleardev_complex_execution_runs AS run ON run.id = builder.execution_run_id
          WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER'
            AND builder.builder_slot = 1 AND builder.status = 'BOUND' AND run.mode = 'PARALLEL'
      ))
      OR (OLD.ordinal > 0 AND NEW.common_base_sha = (
          SELECT composition.output_commit_sha
          FROM cleardev_complex_execution_compositions AS composition
          JOIN cleardev_complex_execution_batches AS prior_batch ON prior_batch.id = composition.batch_id
          WHERE prior_batch.execution_run_id = NEW.execution_run_id
            AND prior_batch.ordinal = NEW.ordinal - 1
            AND composition.status = 'COMPOSED'
            AND prior_batch.status = 'COMPOSED'
      ))
    )
 ))
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('RUNNING', 'BLOCKED'))
 OR (OLD.status = 'RUNNING' AND NEW.status NOT IN ('COMPOSING', 'BLOCKED'))
 OR (OLD.status = 'COMPOSING' AND NEW.status NOT IN ('COMPOSED', 'BLOCKED'))
 OR (NEW.status = 'RUNNING' AND length(NEW.common_base_sha) <> 40)
 OR (NEW.status IN ('COMPOSING', 'COMPOSED') AND EXISTS (
    SELECT 1 FROM json_each(NEW.task_keys_json) AS key
    WHERE NOT EXISTS (
        SELECT 1
        FROM cleardev_complex_execution_task_mappings AS task
        WHERE task.execution_run_id = NEW.execution_run_id AND task.plan_task_key = key.value
          AND EXISTS (SELECT 1 FROM cleardev_complex_execution_verified_candidates AS verified WHERE verified.task_mapping_id = task.id)
    )
 ))
 OR (NEW.status = 'COMPOSED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_compositions AS composition
    WHERE composition.batch_id = NEW.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha <> ''
 ))
 OR (NEW.status = 'BLOCKED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_compositions AS composition
    WHERE composition.batch_id = NEW.id AND composition.status IN ('BLOCKED', 'FAILED')
 ))
 OR (NEW.composed_at IS NOT NULL AND NEW.status NOT IN ('COMPOSED', 'BLOCKED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution batch is immutable or invalid');
END;
CREATE TRIGGER cleardev_complex_execution_check_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = NEW.task_attempt_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = attempt.execution_run_id
    WHERE spec.id = NEW.check_spec_id AND spec.execution_run_id = run.id
      AND (spec.task_mapping_id IS NULL OR spec.task_mapping_id = task.id)
      AND (
        (NOT (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL')
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.commit_sha = NEW.candidate_commit_sha)
        OR (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL'
          AND task.plan_task_key = (
              SELECT last_key.value
              FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS last_key
              WHERE batch.execution_run_id = run.id
                AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
              ORDER BY last_key.key DESC LIMIT 1
          )
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.id = (
              SELECT verified.candidate_commit_id
              FROM cleardev_complex_execution_verified_candidates AS verified
              WHERE verified.task_mapping_id = task.id
          )
          AND NEW.candidate_commit_sha = (
              SELECT composition.output_commit_sha
              FROM cleardev_complex_execution_compositions AS composition
              JOIN cleardev_complex_execution_batches AS composition_batch ON composition_batch.id = composition.batch_id
              WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
                AND composition_batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
          ))
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
      AND (
        NEW.retry_ordinal = 0
        OR (
          NEW.retry_ordinal = 1
          AND EXISTS (
              SELECT 1 FROM cleardev_complex_execution_check_runs AS prior
              WHERE prior.check_spec_id = NEW.check_spec_id
                AND prior.task_attempt_id = NEW.task_attempt_id
                AND prior.candidate_commit_id = NEW.candidate_commit_id
                AND prior.retry_ordinal = 0
                AND prior.status = 'FAILED'
                AND prior.reason_code = 'CHECKER_UNAVAILABLE'
          )
        )
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run must match its candidate, attempt, fixed check, and gate');
END;
CREATE TRIGGER cleardev_complex_execution_composition_insert_valid
BEFORE INSERT ON cleardev_complex_execution_compositions
WHEN NEW.status <> 'PENDING'
 OR json_array_length(NEW.input_candidate_ids_json) NOT BETWEEN 1 AND 6
 OR json_array_length(NEW.input_candidate_shas_json) NOT BETWEEN 1 AND 6
 OR EXISTS (SELECT 1 FROM json_each(NEW.input_candidate_ids_json) AS candidate WHERE typeof(candidate.value) <> 'text' OR trim(candidate.value) = '')
 OR EXISTS (SELECT 1 FROM json_each(NEW.input_candidate_shas_json) AS sha WHERE typeof(sha.value) <> 'text' OR length(sha.value) <> 40 OR sha.value GLOB '*[^0-9a-f]*')
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_batches AS batch
    JOIN cleardev_complex_execution_runs AS run ON run.id = batch.execution_run_id
    WHERE batch.id = NEW.batch_id AND run.id = NEW.execution_run_id AND run.mode = 'PARALLEL'
      AND run.status = 'ACCEPTED'
      AND batch.status = 'COMPOSING'
      AND batch.common_base_sha = NEW.input_base_sha
      AND json_array_length(batch.task_keys_json) = json_array_length(NEW.input_candidate_ids_json)
      AND NOT EXISTS (
          SELECT 1 FROM json_each(batch.task_keys_json) AS expected_key
          WHERE NOT EXISTS (
              SELECT 1
              FROM json_each(NEW.input_candidate_ids_json) AS actual_id
              WHERE actual_id.key = expected_key.key
                AND EXISTS (
                    SELECT 1
                    FROM cleardev_complex_execution_task_mappings AS task
                    JOIN cleardev_complex_execution_verified_candidates AS verified ON verified.task_mapping_id = task.id
                    JOIN cleardev_candidate_commits AS candidate ON candidate.id = verified.candidate_commit_id
                    WHERE task.execution_run_id = batch.execution_run_id
                      AND task.plan_task_key = expected_key.value
                      AND verified.candidate_commit_id = actual_id.value
                      AND candidate.commit_sha = json_extract(NEW.input_candidate_shas_json, '$[' || expected_key.key || ']')
                )
          )
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution composition must combine its batch in plan order');
END;
CREATE TRIGGER cleardev_complex_execution_result_insert_valid
BEFORE INSERT ON cleardev_complex_execution_results
WHEN NEW.completion_status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      WHERE task.execution_run_id = run.id
                        AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_verified_candidates AS verified WHERE verified.task_mapping_id = task.id))
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result lacks all verified tasks, final integration, or gate');
END;
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
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
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
CREATE TRIGGER cleardev_complex_execution_verified_candidate_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_verified_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidates are append-only');
END;
CREATE TRIGGER cleardev_complex_execution_verified_candidate_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_verified_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidates are append-only');
END;
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_complex_execution_runs run ON run.id=task.execution_run_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=NEW.candidate_commit_id
 JOIN cleardev_complex_execution_reviews review ON review.task_attempt_id=attempt.id AND review.candidate_commit_id=candidate.id
 WHERE task.id=NEW.task_mapping_id AND attempt.id=NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id=attempt.id AND attempt.status='REVIEWING'
 AND ((NEW.replacement_recovery_id IS NULL AND NEW.replacement_attempt_id IS NULL AND NEW.replacement_result_id IS NULL AND review.status='SETTLED' AND review.verdict='PASS')
 OR EXISTS(
 SELECT 1 FROM cleardev_replacement_review_results replacement
 JOIN cleardev_fixed_recovery_requests request ON request.id=replacement.recovery_request_id
 JOIN cleardev_fixed_recovery_results result ON result.request_id=request.id AND result.outcome='PASS'
 JOIN cleardev_agent_step_attempts agent_attempt ON agent_attempt.id=replacement.attempt_id
 JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=replacement.result_id AND parsed.conclusion='VALID'
 WHERE replacement.recovery_request_id=NEW.replacement_recovery_id AND replacement.original_review_id=review.id AND replacement.attempt_id=NEW.replacement_attempt_id AND replacement.result_id=NEW.replacement_result_id AND replacement.verdict='PASS'
 AND request.execution_run_id=run.id AND json_extract(request.request_json,'$.candidateId')=candidate.id AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND agent_attempt.ao_session_id=json_extract(result.result_json,'$.sessionId')
 AND ((agent_attempt.attempt_number=2 AND agent_attempt.logical_step_id=review.agent_step_id AND NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id))
 OR EXISTS(SELECT 1 FROM cleardev_mail_replacement_final_sources source WHERE source.recovery_id=replacement.recovery_request_id AND source.review_id=review.id AND source.attempt_id=replacement.attempt_id AND source.result_id=replacement.result_id))
 ))
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs scope JOIN cleardev_complex_execution_check_runs scope_run ON scope_run.check_spec_id=scope.id WHERE scope_run.id=NEW.scope_check_run_id AND scope.task_mapping_id=task.id AND scope.check_kind='SCOPE' AND scope_run.task_attempt_id=attempt.id AND scope_run.candidate_commit_id=candidate.id AND scope_run.status='SETTLED' AND scope_run.result='PASS')
 AND review.id=NEW.review_id
 AND json_array_length(NEW.required_check_runs_json)=(SELECT count(*) FROM cleardev_complex_execution_check_specs WHERE task_mapping_id=task.id AND check_kind='REQUIRED_CHECK')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.required_check_runs_json) required_id WHERE typeof(required_id.value)<>'text' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN cleardev_complex_execution_check_specs required_spec ON required_spec.id=required_run.check_spec_id WHERE required_run.id=required_id.value AND required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs required_spec WHERE required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN json_each(NEW.required_check_runs_json) required_id ON required_id.value=required_run.id WHERE required_run.check_spec_id=required_spec.id AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs spec WHERE spec.task_mapping_id=task.id AND spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs check_run WHERE check_run.check_spec_id=spec.id AND check_run.task_attempt_id=attempt.id AND check_run.candidate_commit_id=candidate.id AND check_run.status='SETTLED' AND check_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
) BEGIN SELECT RAISE(ABORT,'cleardev complex execution verified candidate lacks current scope, checks, review, or gate'); END;
CREATE TRIGGER cleardev_fixed_verification_immutable BEFORE UPDATE ON cleardev_complex_execution_verified_candidates
WHEN OLD.replacement_recovery_id IS NOT NEW.replacement_recovery_id OR OLD.replacement_attempt_id IS NOT NEW.replacement_attempt_id OR OLD.replacement_result_id IS NOT NEW.replacement_result_id
BEGIN SELECT RAISE(ABORT,'replacement verification evidence is immutable'); END;
CREATE TRIGGER cleardev_complex_execution_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN (NEW.complex_execution_result_id IS NOT NULL AND NEW.complex_quick_result_id IS NOT NULL)
 OR (NEW.complex_quick_result_id IS NULL) <> (NEW.complex_quick_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NULL) <> (NEW.complex_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NOT NULL AND NOT EXISTS (
	 SELECT 1 FROM cleardev_complex_execution_results AS result
	 JOIN cleardev_complex_execution_runs AS run ON run.id = result.execution_run_id
	 JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_source_candidate_commit_id
	 JOIN cleardev_complex_execution_role_bindings AS builder ON builder.execution_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND' AND builder.builder_slot = 1
	 WHERE result.id = NEW.complex_execution_result_id AND result.integration_candidate_id = NEW.id
	   AND run.development_project_id = NEW.development_project_id
	   AND NEW.requirement_version_id = run.requirement_version_id
	   AND NEW.task_set_version = run.accepted_task_set_version
	   AND NEW.ao_session_id = builder.ao_session_id
	   AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
	   AND (
	     (run.mode = 'STANDARD'
	       AND candidate.commit_sha = NEW.commit_sha
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC
	           LIMIT 1
	       ))
	     OR (run.mode = 'PARALLEL'
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC
	           LIMIT 1
	       )
	       AND NEW.commit_sha = (
	           SELECT composition.output_commit_sha
	           FROM cleardev_complex_execution_compositions AS composition
	           JOIN cleardev_complex_execution_batches AS batch ON batch.id = composition.batch_id
	           WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
	             AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
	       ))
	   )
 ))
 OR (NEW.complex_quick_result_id IS NOT NULL AND NOT EXISTS (
	    SELECT 1 FROM cleardev_complex_quick_results AS result
	    JOIN cleardev_complex_quick_runs AS run ON run.id = result.quick_run_id
	    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_quick_source_candidate_commit_id
	    JOIN cleardev_complex_quick_role_bindings AS builder ON builder.quick_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND'
	    WHERE result.id = NEW.complex_quick_result_id AND result.integration_candidate_id = NEW.id
	      AND run.development_project_id = NEW.development_project_id
	      AND NEW.requirement_version_id = run.requirement_version_id
	      AND NEW.task_set_version = run.accepted_task_set_version
	      AND NEW.ao_session_id = builder.ao_session_id
	      AND candidate.commit_sha = NEW.commit_sha
	      AND candidate.complex_quick_task_attempt_id IS NOT NULL
	      AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution integration candidate must use its final result and candidate');
END;
-- +goose StatementEnd

-- +goose Down
-- The guard repeats the downstream history checks because goose commits each
-- Down separately.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_verified_candidate_down_guard;
CREATE TEMP TABLE cleardev_verified_candidate_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_verified_candidate_down_guard_valid BEFORE INSERT ON cleardev_verified_candidate_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_verified_candidate_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM (SELECT task_mapping_id FROM cleardev_complex_execution_verified_candidates GROUP BY task_mapping_id HAVING count(*)>1))
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_verified_candidate_down_guard;
DROP TRIGGER cleardev_complex_execution_batch_update_valid;
DROP TRIGGER cleardev_complex_execution_check_run_insert_valid;
DROP TRIGGER cleardev_complex_execution_composition_insert_valid;
DROP TRIGGER cleardev_complex_execution_result_insert_valid;
DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
DROP TRIGGER cleardev_complex_execution_verified_candidate_append_only_delete;
DROP TRIGGER cleardev_complex_execution_verified_candidate_append_only_update;
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
DROP TRIGGER cleardev_fixed_verification_immutable;
DROP TRIGGER cleardev_complex_execution_integration_candidate_binding_valid;
CREATE TABLE cleardev_complex_execution_verified_candidates_down (
    id                       TEXT PRIMARY KEY,
    task_mapping_id          TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_mappings(id),
    task_attempt_id          TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id      TEXT NOT NULL UNIQUE REFERENCES cleardev_candidate_commits(id),
    scope_check_run_id       TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_check_runs(id),
    required_check_runs_json TEXT NOT NULL CHECK (json_valid(required_check_runs_json) AND json_type(required_check_runs_json) = 'array'),
    review_id                TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_reviews(id),
    verified_at              TIMESTAMP NOT NULL,
    replacement_recovery_id TEXT REFERENCES cleardev_replacement_review_results(recovery_request_id),
    replacement_attempt_id TEXT REFERENCES cleardev_agent_step_attempts(id),
    replacement_result_id TEXT REFERENCES cleardev_agent_step_results(id)
);
INSERT INTO cleardev_complex_execution_verified_candidates_down (id, task_mapping_id, task_attempt_id, candidate_commit_id, scope_check_run_id, required_check_runs_json, review_id, verified_at, replacement_recovery_id, replacement_attempt_id, replacement_result_id)
SELECT id, task_mapping_id, task_attempt_id, candidate_commit_id, scope_check_run_id, required_check_runs_json, review_id, verified_at, replacement_recovery_id, replacement_attempt_id, replacement_result_id FROM cleardev_complex_execution_verified_candidates;
DROP TABLE cleardev_complex_execution_verified_candidates;
ALTER TABLE cleardev_complex_execution_verified_candidates_down RENAME TO cleardev_complex_execution_verified_candidates;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_batch_update_valid
BEFORE UPDATE ON cleardev_complex_execution_batches
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.task_keys_json IS NOT NEW.task_keys_json
 OR OLD.ordinal IS NOT NEW.ordinal
 OR OLD.created_at IS NOT NEW.created_at
 OR (OLD.common_base_sha <> '' AND NEW.common_base_sha <> OLD.common_base_sha)
 OR (OLD.status = 'COMPOSED' OR OLD.status = 'BLOCKED')
 OR (NEW.common_base_sha <> OLD.common_base_sha AND NOT (
    OLD.common_base_sha = '' AND NEW.status IN ('PENDING', 'RUNNING')
    AND (
      (OLD.ordinal = 0 AND NEW.common_base_sha = (
          SELECT builder.base_commit_sha
          FROM cleardev_complex_execution_role_bindings AS builder
          JOIN cleardev_complex_execution_runs AS run ON run.id = builder.execution_run_id
          WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER'
            AND builder.builder_slot = 1 AND builder.status = 'BOUND' AND run.mode = 'PARALLEL'
      ))
      OR (OLD.ordinal > 0 AND NEW.common_base_sha = (
          SELECT composition.output_commit_sha
          FROM cleardev_complex_execution_compositions AS composition
          JOIN cleardev_complex_execution_batches AS prior_batch ON prior_batch.id = composition.batch_id
          WHERE prior_batch.execution_run_id = NEW.execution_run_id
            AND prior_batch.ordinal = NEW.ordinal - 1
            AND composition.status = 'COMPOSED'
            AND prior_batch.status = 'COMPOSED'
      ))
    )
 ))
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('RUNNING', 'BLOCKED'))
 OR (OLD.status = 'RUNNING' AND NEW.status NOT IN ('COMPOSING', 'BLOCKED'))
 OR (OLD.status = 'COMPOSING' AND NEW.status NOT IN ('COMPOSED', 'BLOCKED'))
 OR (NEW.status = 'RUNNING' AND length(NEW.common_base_sha) <> 40)
 OR (NEW.status IN ('COMPOSING', 'COMPOSED') AND EXISTS (
    SELECT 1 FROM json_each(NEW.task_keys_json) AS key
    WHERE NOT EXISTS (
        SELECT 1
        FROM cleardev_complex_execution_task_mappings AS task
        WHERE task.execution_run_id = NEW.execution_run_id AND task.plan_task_key = key.value
          AND EXISTS (SELECT 1 FROM cleardev_complex_execution_verified_candidates AS verified WHERE verified.task_mapping_id = task.id)
    )
 ))
 OR (NEW.status = 'COMPOSED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_compositions AS composition
    WHERE composition.batch_id = NEW.id AND composition.status = 'COMPOSED' AND composition.output_commit_sha <> ''
 ))
 OR (NEW.status = 'BLOCKED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_compositions AS composition
    WHERE composition.batch_id = NEW.id AND composition.status IN ('BLOCKED', 'FAILED')
 ))
 OR (NEW.composed_at IS NOT NULL AND NEW.status NOT IN ('COMPOSED', 'BLOCKED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution batch is immutable or invalid');
END;
CREATE TRIGGER cleardev_complex_execution_check_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = NEW.task_attempt_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = attempt.execution_run_id
    WHERE spec.id = NEW.check_spec_id AND spec.execution_run_id = run.id
      AND (spec.task_mapping_id IS NULL OR spec.task_mapping_id = task.id)
      AND (
        (NOT (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL')
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.commit_sha = NEW.candidate_commit_sha)
        OR (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL'
          AND task.plan_task_key = (
              SELECT last_key.value
              FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS last_key
              WHERE batch.execution_run_id = run.id
                AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
              ORDER BY last_key.key DESC LIMIT 1
          )
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.id = (
              SELECT verified.candidate_commit_id
              FROM cleardev_complex_execution_verified_candidates AS verified
              WHERE verified.task_mapping_id = task.id
          )
          AND NEW.candidate_commit_sha = (
              SELECT composition.output_commit_sha
              FROM cleardev_complex_execution_compositions AS composition
              JOIN cleardev_complex_execution_batches AS composition_batch ON composition_batch.id = composition.batch_id
              WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
                AND composition_batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
          ))
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
      AND (
        NEW.retry_ordinal = 0
        OR (
          NEW.retry_ordinal = 1
          AND EXISTS (
              SELECT 1 FROM cleardev_complex_execution_check_runs AS prior
              WHERE prior.check_spec_id = NEW.check_spec_id
                AND prior.task_attempt_id = NEW.task_attempt_id
                AND prior.candidate_commit_id = NEW.candidate_commit_id
                AND prior.retry_ordinal = 0
                AND prior.status = 'FAILED'
                AND prior.reason_code = 'CHECKER_UNAVAILABLE'
          )
        )
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run must match its candidate, attempt, fixed check, and gate');
END;
CREATE TRIGGER cleardev_complex_execution_composition_insert_valid
BEFORE INSERT ON cleardev_complex_execution_compositions
WHEN NEW.status <> 'PENDING'
 OR json_array_length(NEW.input_candidate_ids_json) NOT BETWEEN 1 AND 6
 OR json_array_length(NEW.input_candidate_shas_json) NOT BETWEEN 1 AND 6
 OR EXISTS (SELECT 1 FROM json_each(NEW.input_candidate_ids_json) AS candidate WHERE typeof(candidate.value) <> 'text' OR trim(candidate.value) = '')
 OR EXISTS (SELECT 1 FROM json_each(NEW.input_candidate_shas_json) AS sha WHERE typeof(sha.value) <> 'text' OR length(sha.value) <> 40 OR sha.value GLOB '*[^0-9a-f]*')
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_batches AS batch
    JOIN cleardev_complex_execution_runs AS run ON run.id = batch.execution_run_id
    WHERE batch.id = NEW.batch_id AND run.id = NEW.execution_run_id AND run.mode = 'PARALLEL'
      AND run.status = 'ACCEPTED'
      AND batch.status = 'COMPOSING'
      AND batch.common_base_sha = NEW.input_base_sha
      AND json_array_length(batch.task_keys_json) = json_array_length(NEW.input_candidate_ids_json)
      AND NOT EXISTS (
          SELECT 1 FROM json_each(batch.task_keys_json) AS expected_key
          WHERE NOT EXISTS (
              SELECT 1
              FROM json_each(NEW.input_candidate_ids_json) AS actual_id
              WHERE actual_id.key = expected_key.key
                AND EXISTS (
                    SELECT 1
                    FROM cleardev_complex_execution_task_mappings AS task
                    JOIN cleardev_complex_execution_verified_candidates AS verified ON verified.task_mapping_id = task.id
                    JOIN cleardev_candidate_commits AS candidate ON candidate.id = verified.candidate_commit_id
                    WHERE task.execution_run_id = batch.execution_run_id
                      AND task.plan_task_key = expected_key.value
                      AND verified.candidate_commit_id = actual_id.value
                      AND candidate.commit_sha = json_extract(NEW.input_candidate_shas_json, '$[' || expected_key.key || ']')
                )
          )
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution composition must combine its batch in plan order');
END;
CREATE TRIGGER cleardev_complex_execution_result_insert_valid
BEFORE INSERT ON cleardev_complex_execution_results
WHEN NEW.completion_status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      WHERE task.execution_run_id = run.id
                        AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_verified_candidates AS verified WHERE verified.task_mapping_id = task.id))
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result lacks all verified tasks, final integration, or gate');
END;
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
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
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
CREATE TRIGGER cleardev_complex_execution_verified_candidate_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_verified_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidates are append-only');
END;
CREATE TRIGGER cleardev_complex_execution_verified_candidate_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_verified_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidates are append-only');
END;
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_complex_execution_runs run ON run.id=task.execution_run_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=NEW.candidate_commit_id
 JOIN cleardev_complex_execution_reviews review ON review.task_attempt_id=attempt.id AND review.candidate_commit_id=candidate.id
 WHERE task.id=NEW.task_mapping_id AND attempt.id=NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id=attempt.id AND attempt.status='REVIEWING'
 AND ((NEW.replacement_recovery_id IS NULL AND NEW.replacement_attempt_id IS NULL AND NEW.replacement_result_id IS NULL AND review.status='SETTLED' AND review.verdict='PASS')
 OR EXISTS(
 SELECT 1 FROM cleardev_replacement_review_results replacement
 JOIN cleardev_fixed_recovery_requests request ON request.id=replacement.recovery_request_id
 JOIN cleardev_fixed_recovery_results result ON result.request_id=request.id AND result.outcome='PASS'
 JOIN cleardev_agent_step_attempts agent_attempt ON agent_attempt.id=replacement.attempt_id
 JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=replacement.result_id AND parsed.conclusion='VALID'
 WHERE replacement.recovery_request_id=NEW.replacement_recovery_id AND replacement.original_review_id=review.id AND replacement.attempt_id=NEW.replacement_attempt_id AND replacement.result_id=NEW.replacement_result_id AND replacement.verdict='PASS'
 AND request.execution_run_id=run.id AND json_extract(request.request_json,'$.candidateId')=candidate.id AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND agent_attempt.ao_session_id=json_extract(result.result_json,'$.sessionId')
 AND ((agent_attempt.attempt_number=2 AND agent_attempt.logical_step_id=review.agent_step_id AND NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id))
 OR EXISTS(SELECT 1 FROM cleardev_mail_replacement_final_sources source WHERE source.recovery_id=replacement.recovery_request_id AND source.review_id=review.id AND source.attempt_id=replacement.attempt_id AND source.result_id=replacement.result_id))
 ))
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs scope JOIN cleardev_complex_execution_check_runs scope_run ON scope_run.check_spec_id=scope.id WHERE scope_run.id=NEW.scope_check_run_id AND scope.task_mapping_id=task.id AND scope.check_kind='SCOPE' AND scope_run.task_attempt_id=attempt.id AND scope_run.candidate_commit_id=candidate.id AND scope_run.status='SETTLED' AND scope_run.result='PASS')
 AND review.id=NEW.review_id
 AND json_array_length(NEW.required_check_runs_json)=(SELECT count(*) FROM cleardev_complex_execution_check_specs WHERE task_mapping_id=task.id AND check_kind='REQUIRED_CHECK')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.required_check_runs_json) required_id WHERE typeof(required_id.value)<>'text' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN cleardev_complex_execution_check_specs required_spec ON required_spec.id=required_run.check_spec_id WHERE required_run.id=required_id.value AND required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs required_spec WHERE required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN json_each(NEW.required_check_runs_json) required_id ON required_id.value=required_run.id WHERE required_run.check_spec_id=required_spec.id AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs spec WHERE spec.task_mapping_id=task.id AND spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs check_run WHERE check_run.check_spec_id=spec.id AND check_run.task_attempt_id=attempt.id AND check_run.candidate_commit_id=candidate.id AND check_run.status='SETTLED' AND check_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
) BEGIN SELECT RAISE(ABORT,'cleardev complex execution verified candidate lacks current scope, checks, review, or gate'); END;
CREATE TRIGGER cleardev_fixed_verification_immutable BEFORE UPDATE ON cleardev_complex_execution_verified_candidates
WHEN OLD.replacement_recovery_id IS NOT NEW.replacement_recovery_id OR OLD.replacement_attempt_id IS NOT NEW.replacement_attempt_id OR OLD.replacement_result_id IS NOT NEW.replacement_result_id
BEGIN SELECT RAISE(ABORT,'replacement verification evidence is immutable'); END;
CREATE TRIGGER cleardev_complex_execution_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN (NEW.complex_execution_result_id IS NOT NULL AND NEW.complex_quick_result_id IS NOT NULL)
 OR (NEW.complex_quick_result_id IS NULL) <> (NEW.complex_quick_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NULL) <> (NEW.complex_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NOT NULL AND NOT EXISTS (
	 SELECT 1 FROM cleardev_complex_execution_results AS result
	 JOIN cleardev_complex_execution_runs AS run ON run.id = result.execution_run_id
	 JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_source_candidate_commit_id
	 JOIN cleardev_complex_execution_role_bindings AS builder ON builder.execution_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND' AND builder.builder_slot = 1
	 WHERE result.id = NEW.complex_execution_result_id AND result.integration_candidate_id = NEW.id
	   AND run.development_project_id = NEW.development_project_id
	   AND NEW.requirement_version_id = run.requirement_version_id
	   AND NEW.task_set_version = run.accepted_task_set_version
	   AND NEW.ao_session_id = builder.ao_session_id
	   AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
	   AND (
	     (run.mode = 'STANDARD'
	       AND candidate.commit_sha = NEW.commit_sha
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC
	           LIMIT 1
	       ))
	     OR (run.mode = 'PARALLEL'
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC
	           LIMIT 1
	       )
	       AND NEW.commit_sha = (
	           SELECT composition.output_commit_sha
	           FROM cleardev_complex_execution_compositions AS composition
	           JOIN cleardev_complex_execution_batches AS batch ON batch.id = composition.batch_id
	           WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
	             AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
	       ))
	   )
 ))
 OR (NEW.complex_quick_result_id IS NOT NULL AND NOT EXISTS (
	    SELECT 1 FROM cleardev_complex_quick_results AS result
	    JOIN cleardev_complex_quick_runs AS run ON run.id = result.quick_run_id
	    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_quick_source_candidate_commit_id
	    JOIN cleardev_complex_quick_role_bindings AS builder ON builder.quick_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND'
	    WHERE result.id = NEW.complex_quick_result_id AND result.integration_candidate_id = NEW.id
	      AND run.development_project_id = NEW.development_project_id
	      AND NEW.requirement_version_id = run.requirement_version_id
	      AND NEW.task_set_version = run.accepted_task_set_version
	      AND NEW.ao_session_id = builder.ao_session_id
	      AND candidate.commit_sha = NEW.commit_sha
	      AND candidate.complex_quick_task_attempt_id IS NOT NULL
	      AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution integration candidate must use its final result and candidate');
END;
-- +goose StatementEnd
