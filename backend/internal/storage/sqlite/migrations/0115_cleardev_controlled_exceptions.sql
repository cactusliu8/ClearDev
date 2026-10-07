-- ClearDev S09 controlled-exception facts for STANDARD/PARALLEL runs.
-- 0106-0114 stay unchanged. QUICK remains on 0114 and still rejects shared
-- and generated paths in application code.
-- +goose Up
-- +goose NO TRANSACTION
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
-- +goose StatementEnd

-- Step 1: allow STANDARD/PARALLEL plans that list shared or generated paths.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid;
CREATE TRIGGER cleardev_complex_execution_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_runs
WHEN NEW.status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    JOIN cleardev_complex_engineering_plans AS plan ON plan.id = NEW.plan_id
    JOIN cleardev_complex_plan_reviews AS review ON review.id = NEW.plan_review_id
    JOIN cleardev_complex_role_bindings AS steward ON steward.id = NEW.steward_role_binding_id
    WHERE version.id = NEW.requirement_version_id
      AND project.id = NEW.development_project_id
      AND project.cancelled_at IS NULL
      AND version.state = 'APPROVED'
      AND version.version = 2
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.plan_sha256 = NEW.plan_sha256
      AND review.plan_id = plan.id
      AND review.plan_sha256 = plan.plan_sha256
      AND review.verdict = 'APPROVED'
      AND (
        (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'ONE_BUILDER_REQUIRED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') = 1)
        OR (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'PARALLEL_UNSAFE_DEGRADED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3)
        OR (NEW.mode = 'PARALLEL' AND NEW.selection_reason_code = 'PARALLEL_PLAN_APPROVED'
           AND NEW.fixed_builder_count BETWEEN 2 AND 3
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
           AND NEW.fixed_builder_count <= json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount'))
      )
      AND json_array_length(plan.plan_json, '$.tasks') BETWEEN 2 AND 6
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
 )
 OR EXISTS (
    SELECT 1 FROM cleardev_direction_stop_gates AS gate
    WHERE gate.requirement_version_id = NEW.requirement_version_id AND gate.status = 'ACTIVE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run lacks the exact approved current plan or is stopped');
END;
-- +goose StatementEnd

-- Step 2: rebuild check runs so a settled infrastructure failure can be
-- retried once without rewriting the original FAILED row.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_evidence_source_valid;
CREATE TABLE cleardev_complex_execution_check_runs_rebuilt (
    id                    TEXT PRIMARY KEY,
    check_spec_id         TEXT NOT NULL REFERENCES cleardev_complex_execution_check_specs(id),
    task_attempt_id       TEXT NOT NULL REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id   TEXT NOT NULL REFERENCES cleardev_candidate_commits(id),
    candidate_commit_sha  TEXT NOT NULL CHECK (length(candidate_commit_sha) = 40 AND candidate_commit_sha NOT GLOB '*[^0-9a-f]*'),
    container_image_id    TEXT,
    exit_code             INTEGER,
    status                TEXT NOT NULL CHECK (status IN ('PENDING', 'STARTED', 'SETTLED', 'FAILED')),
    timed_out             BOOLEAN,
    output_summary        TEXT,
    output_sha256         TEXT CHECK (output_sha256 IS NULL OR (length(output_sha256) = 64 AND output_sha256 NOT GLOB '*[^0-9a-f]*')),
    changed_paths_json    TEXT CHECK (changed_paths_json IS NULL OR (json_valid(changed_paths_json) AND json_type(changed_paths_json) = 'array')),
    result                TEXT CHECK (result IN ('PASS', 'FAIL')),
    created_at            TIMESTAMP NOT NULL,
    started_at            TIMESTAMP,
    settled_at            TIMESTAMP,
    reason_code           TEXT NOT NULL DEFAULT '',
    retry_ordinal         INTEGER NOT NULL DEFAULT 0 CHECK (retry_ordinal IN (0, 1)),
    UNIQUE (check_spec_id, task_attempt_id, candidate_commit_id, retry_ordinal),
    CHECK (
        (status = 'PENDING' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'STARTED' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'SETTLED' AND timed_out IS NOT NULL AND output_summary IS NOT NULL AND output_sha256 IS NOT NULL AND changed_paths_json IS NOT NULL AND result IS NOT NULL AND started_at IS NOT NULL AND settled_at IS NOT NULL AND ((result = 'PASS' AND reason_code = '') OR (result = 'FAIL' AND reason_code <> '')))
        OR (status = 'FAILED' AND started_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
INSERT INTO cleardev_complex_execution_check_runs_rebuilt (
    id, check_spec_id, task_attempt_id, candidate_commit_id, candidate_commit_sha,
    container_image_id, exit_code, status, timed_out, output_summary, output_sha256,
    changed_paths_json, result, created_at, started_at, settled_at, reason_code, retry_ordinal
)
SELECT
    id, check_spec_id, task_attempt_id, candidate_commit_id, candidate_commit_sha,
    container_image_id, exit_code, status, timed_out, output_summary, output_sha256,
    changed_paths_json, result, created_at, started_at, settled_at, reason_code, 0
FROM cleardev_complex_execution_check_runs;
DROP TABLE cleardev_complex_execution_check_runs;
ALTER TABLE cleardev_complex_execution_check_runs_rebuilt RENAME TO cleardev_complex_execution_check_runs;
-- +goose StatementEnd

-- +goose StatementBegin
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

CREATE TRIGGER cleardev_complex_execution_check_run_update_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN OLD.check_spec_id IS NOT NEW.check_spec_id
 OR OLD.task_attempt_id IS NOT NEW.task_attempt_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha
 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.retry_ordinal IS NOT NEW.retry_ordinal
 OR OLD.status NOT IN ('PENDING', 'STARTED')
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('STARTED', 'FAILED'))
 OR (OLD.status = 'STARTED' AND NEW.status NOT IN ('SETTLED', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run is immutable or settles once');
END;

CREATE TRIGGER cleardev_complex_execution_check_run_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_check_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check runs are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid
BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_reviews AS review ON review.task_attempt_id = attempt.id AND review.candidate_commit_id = candidate.id
    WHERE task.id = NEW.task_mapping_id AND attempt.id = NEW.task_attempt_id
      AND candidate.complex_execution_task_attempt_id = attempt.id
      AND attempt.status = 'REVIEWING' AND review.status = 'SETTLED' AND review.verdict = 'PASS'
      AND EXISTS (SELECT 1 FROM cleardev_complex_execution_check_specs AS scope
                  JOIN cleardev_complex_execution_check_runs AS scope_run ON scope_run.check_spec_id = scope.id
                  WHERE scope_run.id = NEW.scope_check_run_id AND scope.task_mapping_id = task.id AND scope.check_kind = 'SCOPE'
                    AND scope_run.task_attempt_id = attempt.id AND scope_run.candidate_commit_id = candidate.id
                    AND scope_run.status = 'SETTLED' AND scope_run.result = 'PASS')
      AND review.id = NEW.review_id
	  AND json_array_length(NEW.required_check_runs_json) = (
	      SELECT count(*) FROM cleardev_complex_execution_check_specs AS required_spec
	      WHERE required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM json_each(NEW.required_check_runs_json) AS required_id
	      WHERE typeof(required_id.value) <> 'text'
	         OR NOT EXISTS (
	             SELECT 1
	             FROM cleardev_complex_execution_check_runs AS required_run
	             JOIN cleardev_complex_execution_check_specs AS required_spec ON required_spec.id = required_run.check_spec_id
	             WHERE required_run.id = required_id.value
	               AND required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	               AND required_run.task_attempt_id = attempt.id AND required_run.candidate_commit_id = candidate.id
	               AND required_run.status = 'SETTLED' AND required_run.result = 'PASS'
	         )
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM cleardev_complex_execution_check_specs AS required_spec
	      WHERE required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	        AND NOT EXISTS (
	            SELECT 1
	            FROM cleardev_complex_execution_check_runs AS required_run
	            JOIN json_each(NEW.required_check_runs_json) AS required_id ON required_id.value = required_run.id
	            WHERE required_run.check_spec_id = required_spec.id
	              AND required_run.task_attempt_id = attempt.id AND required_run.candidate_commit_id = candidate.id
	              AND required_run.status = 'SETTLED' AND required_run.result = 'PASS'
	        )
	  )
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_check_specs AS spec
                      WHERE spec.task_mapping_id = task.id AND spec.check_kind = 'REQUIRED_CHECK'
                        AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_check_runs AS check_run
                                        WHERE check_run.check_spec_id = spec.id AND check_run.task_attempt_id = attempt.id
                                          AND check_run.candidate_commit_id = candidate.id AND check_run.status = 'SETTLED' AND check_run.result = 'PASS'))
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidate lacks current scope, checks, review, or gate');
END;

CREATE TRIGGER cleardev_complex_execution_result_update_valid
BEFORE UPDATE ON cleardev_complex_execution_results
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.integration_candidate_id IS NOT NEW.integration_candidate_id
 OR OLD.created_at IS NOT NEW.created_at
 OR (OLD.completion_status = 'PENDING' AND NEW.completion_status <> 'COMMITTING')
 OR (OLD.completion_status = 'COMMITTING' AND NEW.completion_status <> 'COMPLETED')
 OR OLD.completion_status = 'COMPLETED'
 OR (NEW.completion_status IN ('COMMITTING', 'COMPLETED') AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    WHERE run.id = NEW.execution_run_id
      AND run.status = 'ACCEPTED'
      AND version.version = 2 AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND run.accepted_task_set_version = 1
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 ))
 OR (NEW.completion_status = 'COMMITTING' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_integration_candidates AS integration ON integration.id = NEW.integration_candidate_id
    WHERE run.id = NEW.execution_run_id
      AND integration.complex_execution_result_id = NEW.id
      AND NOT EXISTS (
          SELECT 1
          FROM cleardev_complex_execution_check_specs AS spec
          WHERE spec.execution_run_id = run.id AND spec.check_kind = 'INTEGRATION'
            AND NOT EXISTS (
                SELECT 1
                FROM cleardev_complex_execution_result_checks AS result_check
                JOIN cleardev_complex_execution_check_runs AS check_run ON check_run.id = result_check.check_run_id
                WHERE result_check.result_id = NEW.id
                  AND check_run.check_spec_id = spec.id
                  AND check_run.candidate_commit_id = integration.complex_source_candidate_commit_id
                  AND check_run.status = 'SETTLED' AND check_run.result = 'PASS'
            )
      )
 ))
 OR (NEW.completion_status = 'COMPLETED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    WHERE run.id = NEW.execution_run_id
      AND version.version = 2 AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND run.accepted_task_set_version = 1
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
                      WHERE task.execution_run_id = run.id AND work_item.state <> 'DONE')
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result is immutable or cannot complete');
END;

CREATE TRIGGER cleardev_complex_execution_result_check_insert_valid
BEFORE INSERT ON cleardev_complex_execution_result_checks
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_results AS result
    JOIN cleardev_complex_execution_runs AS run ON run.id = result.execution_run_id
    JOIN cleardev_integration_candidates AS integration ON integration.id = result.integration_candidate_id
    JOIN cleardev_complex_execution_check_runs AS check_run ON check_run.id = NEW.check_run_id
    JOIN cleardev_complex_execution_check_specs AS spec ON spec.id = check_run.check_spec_id
    WHERE result.id = NEW.result_id AND integration.complex_execution_result_id = result.id
      AND spec.execution_run_id = run.id AND spec.check_kind = 'INTEGRATION'
      AND check_run.candidate_commit_id = integration.complex_source_candidate_commit_id
      AND check_run.status = 'SETTLED' AND check_run.result = 'PASS'
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result check must be a final-candidate integration PASS');
END;

CREATE TRIGGER cleardev_complex_execution_evidence_source_valid
BEFORE INSERT ON cleardev_evidence
WHEN (NEW.complex_execution_check_run_id IS NOT NULL AND NEW.complex_execution_review_id IS NOT NULL)
 OR (NEW.complex_execution_check_run_id IS NOT NULL AND NEW.complex_quick_check_run_id IS NOT NULL)
 OR (NEW.complex_execution_review_id IS NOT NULL AND NEW.complex_quick_check_run_id IS NOT NULL)
 OR (NEW.complex_execution_check_run_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_check_runs AS run
    WHERE run.id = NEW.complex_execution_check_run_id AND run.status = 'SETTLED'
      AND run.result = NEW.result
      AND ((NEW.candidate_commit_id = run.candidate_commit_id AND NEW.commit_sha = run.candidate_commit_sha)
        OR (NEW.integration_candidate_id IS NOT NULL AND EXISTS (
            SELECT 1 FROM cleardev_complex_execution_results AS result
            JOIN cleardev_integration_candidates AS integration ON integration.id = NEW.integration_candidate_id
            JOIN cleardev_complex_execution_result_checks AS result_check ON result_check.result_id = result.id
            WHERE result_check.check_run_id = run.id AND result.integration_candidate_id = integration.id
              AND integration.commit_sha = run.candidate_commit_sha)))
 ))
 OR (NEW.complex_execution_review_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_reviews AS review
    JOIN cleardev_complex_execution_role_bindings AS reviewer ON reviewer.id = review.reviewer_role_binding_id
    WHERE review.id = NEW.complex_execution_review_id AND NEW.evidence_kind = 'REVIEW'
      AND NEW.candidate_commit_id = review.candidate_commit_id
      AND NEW.result = CASE WHEN review.verdict = 'PASS' THEN 'PASS' ELSE 'FAIL' END
      AND NEW.source_type = 'REVIEW_ADAPTER' AND NEW.source_ao_session_id = reviewer.ao_session_id
 ))
 OR (NEW.complex_quick_check_run_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_check_runs AS run
    WHERE run.id = NEW.complex_quick_check_run_id AND run.status = 'SETTLED'
      AND run.result = NEW.result
      AND ((NEW.candidate_commit_id = run.candidate_commit_id AND NEW.commit_sha = run.candidate_commit_sha)
        OR (NEW.integration_candidate_id IS NOT NULL AND EXISTS (
            SELECT 1 FROM cleardev_complex_quick_results AS result
            JOIN cleardev_integration_candidates AS integration ON integration.id = NEW.integration_candidate_id
            JOIN cleardev_complex_quick_result_checks AS result_check ON result_check.result_id = result.id
            WHERE result_check.check_run_id = run.id AND result.integration_candidate_id = integration.id
              AND integration.commit_sha = run.candidate_commit_sha)))
 ))
 OR (NEW.complex_execution_check_run_id IS NULL AND NEW.complex_execution_review_id IS NULL AND NEW.complex_quick_check_run_id IS NULL AND (
    EXISTS (SELECT 1 FROM cleardev_candidate_commits WHERE id = NEW.candidate_commit_id AND (complex_execution_task_attempt_id IS NOT NULL OR complex_quick_task_attempt_id IS NOT NULL))
    OR EXISTS (SELECT 1 FROM cleardev_integration_candidates WHERE id = NEW.integration_candidate_id AND (complex_execution_result_id IS NOT NULL OR complex_quick_result_id IS NOT NULL))
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution evidence must derive from its exact check or Reviewer');
END;
-- +goose StatementEnd

-- Step 3: independent exception tables. Old generic write interfaces do not
-- target these names.
-- +goose StatementBegin
CREATE TABLE cleardev_complex_exception_budgets (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT REFERENCES cleardev_complex_execution_task_mappings(id),
    role_kind                  TEXT NOT NULL CHECK (role_kind IN ('BUILDER', 'REVIEWER', 'STEWARD_EXCEPTION', 'SPECIALIST', 'RECOVERY')),
    allowed_agent_types_json   TEXT NOT NULL CHECK (json_valid(allowed_agent_types_json) AND json_type(allowed_agent_types_json) = 'array'),
    model_selection            TEXT NOT NULL CHECK (model_selection = 'codex-chat'),
    max_turns                  INTEGER NOT NULL CHECK (max_turns > 0),
    used_turns                 INTEGER NOT NULL DEFAULT 0 CHECK (used_turns >= 0 AND used_turns <= max_turns),
    max_rework_count           INTEGER NOT NULL DEFAULT 0 CHECK (max_rework_count >= 0),
    created_at                 TIMESTAMP NOT NULL,
    CHECK (
        (role_kind = 'RECOVERY' AND complex_execution_task_id IS NULL AND max_turns = 1)
        OR (role_kind = 'BUILDER' AND complex_execution_task_id IS NOT NULL AND max_turns = 3 AND max_rework_count = 1)
        OR (role_kind = 'REVIEWER' AND complex_execution_task_id IS NOT NULL AND max_turns = 2)
        OR (role_kind IN ('STEWARD_EXCEPTION', 'SPECIALIST') AND complex_execution_task_id IS NOT NULL AND max_turns = 1)
    )
);
CREATE UNIQUE INDEX idx_cleardev_complex_exception_budget_role
    ON cleardev_complex_exception_budgets (execution_run_id, IFNULL(complex_execution_task_id, ''), role_kind);

CREATE TABLE cleardev_complex_exception_budget_occupancies (
    id            TEXT PRIMARY KEY,
    budget_id     TEXT NOT NULL REFERENCES cleardev_complex_exception_budgets(id),
    agent_step_id TEXT NOT NULL UNIQUE CHECK (length(trim(agent_step_id)) > 0),
    occupied_at   TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_complex_exception_scope_requests (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    dispatch_id                TEXT NOT NULL REFERENCES cleardev_complex_execution_task_attempts(id),
    round                      INTEGER NOT NULL CHECK (round >= 0),
    requirement_version_id     TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256         TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    plan_id                    TEXT NOT NULL REFERENCES cleardev_complex_engineering_plans(id),
    plan_sha256                TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    requested_paths_json       TEXT NOT NULL CHECK (json_valid(requested_paths_json) AND json_type(requested_paths_json) = 'array' AND json_array_length(requested_paths_json) BETWEEN 1 AND 8),
    agent_step_id              TEXT NOT NULL UNIQUE CHECK (length(trim(agent_step_id)) > 0),
    status                     TEXT NOT NULL CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED', 'NEEDS_HUMAN')),
    reason_code                TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMP NOT NULL,
    settled_at                 TIMESTAMP,
    UNIQUE (dispatch_id),
    CHECK (
        (status = 'PENDING' AND settled_at IS NULL AND reason_code = '')
        OR (status IN ('APPROVED', 'REJECTED', 'NEEDS_HUMAN') AND settled_at IS NOT NULL)
    )
);

CREATE TABLE cleardev_complex_exception_scope_decisions (
    id                         TEXT PRIMARY KEY,
    request_id                 TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_exception_scope_requests(id),
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    requirement_version_id     TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256         TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    plan_id                    TEXT NOT NULL REFERENCES cleardev_complex_engineering_plans(id),
    plan_sha256                TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    paths_json                 TEXT NOT NULL CHECK (json_valid(paths_json) AND json_type(paths_json) = 'array'),
    decision                   TEXT NOT NULL CHECK (decision IN ('APPROVE', 'NEEDS_HUMAN')),
    reason_code                TEXT NOT NULL,
    summary                    TEXT NOT NULL CHECK (length(trim(summary)) > 0),
    steward_role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_execution_role_bindings(id),
    agent_step_id              TEXT NOT NULL UNIQUE CHECK (length(trim(agent_step_id)) > 0),
    permission_version_id      TEXT REFERENCES cleardev_path_permission_versions(id),
    control_accepted           BOOLEAN NOT NULL DEFAULT 0,
    created_at                 TIMESTAMP NOT NULL,
    CHECK ((decision = 'APPROVE' AND control_accepted = 1 AND permission_version_id IS NOT NULL)
        OR (decision = 'NEEDS_HUMAN' AND control_accepted = 0 AND permission_version_id IS NULL))
);

CREATE TABLE cleardev_complex_exception_generated_commands (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    command_id                 TEXT NOT NULL CHECK (command_id = 'npm-package-lock'),
    argv_json                  TEXT NOT NULL CHECK (json_valid(argv_json) AND json_type(argv_json) = 'array' AND json_array_length(argv_json) > 0),
    timeout_seconds            INTEGER NOT NULL CHECK (timeout_seconds = 60),
    output_paths_json          TEXT NOT NULL CHECK (json_valid(output_paths_json) AND json(output_paths_json) = json('["package-lock.json"]')),
    image                      TEXT NOT NULL CHECK (image = 'node:22-bookworm-slim'),
    command_spec_sha256        TEXT NOT NULL CHECK (length(command_spec_sha256) = 64 AND command_spec_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at                 TIMESTAMP NOT NULL,
    UNIQUE (complex_execution_task_id)
);

CREATE TABLE cleardev_complex_exception_generated_proofs (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    dispatch_id                TEXT NOT NULL REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id        TEXT NOT NULL REFERENCES cleardev_candidate_commits(id),
    candidate_commit_sha       TEXT NOT NULL CHECK (length(candidate_commit_sha) = 40 AND candidate_commit_sha NOT GLOB '*[^0-9a-f]*'),
    command_fact_id            TEXT NOT NULL REFERENCES cleardev_complex_exception_generated_commands(id),
    container_image_id         TEXT,
    output_sha256_json         TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_sha256_json)),
    status                     TEXT NOT NULL CHECK (status IN ('PENDING', 'STARTED', 'SETTLED', 'FAILED')),
    result                     TEXT CHECK (result IN ('PASS', 'FAIL')),
    reason_code                TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMP NOT NULL,
    settled_at                 TIMESTAMP,
    UNIQUE (dispatch_id, command_fact_id),
    CHECK (
        (status = 'PENDING' AND settled_at IS NULL AND result IS NULL AND reason_code = '')
        OR (status = 'STARTED' AND settled_at IS NULL AND result IS NULL)
        OR (status = 'SETTLED' AND settled_at IS NOT NULL AND result IS NOT NULL)
        OR (status = 'FAILED' AND settled_at IS NOT NULL AND reason_code <> '')
    )
);

CREATE TABLE cleardev_complex_exception_path_leases (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    path                       TEXT NOT NULL CHECK (length(trim(path)) > 0),
    status                     TEXT NOT NULL CHECK (status IN ('HELD', 'RELEASED')),
    created_at                 TIMESTAMP NOT NULL,
    released_at                TIMESTAMP,
    CHECK ((status = 'HELD' AND released_at IS NULL) OR (status = 'RELEASED' AND released_at IS NOT NULL))
);
CREATE UNIQUE INDEX idx_cleardev_complex_exception_held_path
    ON cleardev_complex_exception_path_leases (execution_run_id, path)
    WHERE status = 'HELD';

CREATE TABLE cleardev_complex_exception_ondemand_bindings (
    id                                TEXT PRIMARY KEY,
    execution_run_id                  TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id         TEXT REFERENCES cleardev_complex_execution_task_mappings(id),
    mode                              TEXT NOT NULL CHECK (mode IN ('SPECIALIST', 'RECOVERY')),
    trigger_reason                    TEXT NOT NULL DEFAULT '',
    session_creation_idempotency_key  TEXT NOT NULL UNIQUE CHECK (length(trim(session_creation_idempotency_key)) > 0),
    ao_session_id                     TEXT REFERENCES sessions(id),
    workspace_path                    TEXT NOT NULL DEFAULT '',
    base_commit_sha                   TEXT NOT NULL DEFAULT '' CHECK (base_commit_sha = '' OR (length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*')),
    status                            TEXT NOT NULL CHECK (status IN ('REQUESTED', 'BOUND', 'FAILED', 'ENDED')),
    reason_code                       TEXT NOT NULL DEFAULT '',
    binding_fingerprint               TEXT NOT NULL DEFAULT '',
    requested_at                      TIMESTAMP NOT NULL,
    bound_at                          TIMESTAMP,
    ended_at                          TIMESTAMP,
    CHECK (
        (status = 'REQUESTED' AND ao_session_id IS NULL AND bound_at IS NULL AND ended_at IS NULL)
        OR (status = 'BOUND' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NULL)
        OR (status = 'FAILED' AND ended_at IS NOT NULL)
        OR (status = 'ENDED' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NOT NULL)
    )
);
CREATE UNIQUE INDEX idx_cleardev_complex_exception_one_ondemand
    ON cleardev_complex_exception_ondemand_bindings (execution_run_id)
    WHERE status IN ('REQUESTED', 'BOUND');

CREATE TABLE cleardev_complex_exception_agent_steps (
    id                  TEXT PRIMARY KEY,
    execution_run_id    TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    role_binding_id     TEXT REFERENCES cleardev_complex_execution_role_bindings(id),
    ondemand_binding_id TEXT REFERENCES cleardev_complex_exception_ondemand_bindings(id),
    step_kind           TEXT NOT NULL CHECK (step_kind IN ('SPECIALIST_RESULT', 'RECOVERY_RESULT', 'SCOPE_EXPANSION_DECISION', 'BUILDER_CONTINUE')),
    request_id          TEXT NOT NULL CHECK (length(trim(request_id)) > 0),
    client_message_id   TEXT NOT NULL UNIQUE CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256       TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    send_status         TEXT NOT NULL CHECK (send_status IN ('PENDING', 'SENT', 'SETTLED', 'FAILED')),
    turn_id             TEXT,
    final_message_id    TEXT,
    final_message_text  TEXT,
    message_sha256      TEXT CHECK (message_sha256 IS NULL OR (length(message_sha256) = 64 AND message_sha256 NOT GLOB '*[^0-9a-f]*')),
    requested_at        TIMESTAMP NOT NULL,
    sent_at             TIMESTAMP,
    completed_at        TIMESTAMP,
    failed_at           TIMESTAMP,
    reason_code         TEXT NOT NULL DEFAULT '',
    UNIQUE (execution_run_id, step_kind, request_id),
    CHECK (
        (step_kind IN ('SPECIALIST_RESULT', 'RECOVERY_RESULT') AND ondemand_binding_id IS NOT NULL AND role_binding_id IS NULL)
        OR (step_kind IN ('SCOPE_EXPANSION_DECISION', 'BUILDER_CONTINUE') AND role_binding_id IS NOT NULL AND ondemand_binding_id IS NULL)
    ),
    CHECK (
        (send_status = 'PENDING' AND sent_at IS NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SENT' AND sent_at IS NOT NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SETTLED' AND sent_at IS NOT NULL AND turn_id IS NOT NULL AND final_message_id IS NOT NULL AND length(final_message_text) > 0 AND message_sha256 IS NOT NULL AND completed_at IS NOT NULL AND failed_at IS NULL)
        OR (send_status = 'FAILED' AND failed_at IS NOT NULL AND reason_code <> '')
    )
);

CREATE TABLE cleardev_complex_exception_specialist_results (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    ondemand_binding_id        TEXT NOT NULL REFERENCES cleardev_complex_exception_ondemand_bindings(id),
    agent_step_id              TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_exception_agent_steps(id),
    binding_fingerprint        TEXT NOT NULL CHECK (length(trim(binding_fingerprint)) > 0),
    outcome                    TEXT NOT NULL CHECK (outcome IN ('PASS', 'BLOCKED', 'NEEDS_HUMAN')),
    constraints_json           TEXT NOT NULL CHECK (json_valid(constraints_json) AND json_type(constraints_json) = 'array'),
    reason_code                TEXT NOT NULL DEFAULT '',
    summary                    TEXT NOT NULL CHECK (length(trim(summary)) > 0),
    created_at                 TIMESTAMP NOT NULL,
    UNIQUE (complex_execution_task_id, binding_fingerprint)
);

CREATE TABLE cleardev_complex_exception_specialist_checks (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    specialist_result_id       TEXT NOT NULL REFERENCES cleardev_complex_exception_specialist_results(id),
    check_id                   TEXT NOT NULL CHECK (check_id = 'package-json-specialist'),
    argv_json                  TEXT NOT NULL CHECK (json_valid(argv_json) AND json_type(argv_json) = 'array'),
    container_image_id         TEXT,
    status                     TEXT NOT NULL CHECK (status IN ('PENDING', 'STARTED', 'SETTLED', 'FAILED')),
    result                     TEXT CHECK (result IN ('PASS', 'FAIL')),
    reason_code                TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMP NOT NULL,
    settled_at                 TIMESTAMP,
    UNIQUE (specialist_result_id, check_id)
);

CREATE TABLE cleardev_complex_exception_recovery_actions (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT REFERENCES cleardev_complex_execution_task_mappings(id),
    ondemand_binding_id        TEXT NOT NULL REFERENCES cleardev_complex_exception_ondemand_bindings(id),
    agent_step_id              TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_exception_agent_steps(id),
    trigger_reason             TEXT NOT NULL,
    trigger_fact_id            TEXT NOT NULL,
    action                     TEXT NOT NULL CHECK (action IN ('RETRY_SETTLED_INFRA_CHECK', 'REBUILD_INDEPENDENT_REVIEWER', 'RESTORE_ORIGINAL_SESSION')),
    outcome                    TEXT NOT NULL CHECK (outcome IN ('PASS', 'NEEDS_HUMAN')),
    retry_check_run_id         TEXT REFERENCES cleardev_complex_execution_check_runs(id),
    reason_code                TEXT NOT NULL DEFAULT '',
    summary                    TEXT NOT NULL CHECK (length(trim(summary)) > 0),
    created_at                 TIMESTAMP NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_exception_budget_update_valid
BEFORE UPDATE ON cleardev_complex_exception_budgets
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.complex_execution_task_id IS NOT NEW.complex_execution_task_id
 OR OLD.role_kind IS NOT NEW.role_kind
 OR OLD.allowed_agent_types_json IS NOT NEW.allowed_agent_types_json
 OR OLD.model_selection IS NOT NEW.model_selection
 OR OLD.max_turns IS NOT NEW.max_turns
 OR OLD.max_rework_count IS NOT NEW.max_rework_count
 OR OLD.created_at IS NOT NEW.created_at
 OR NEW.used_turns < OLD.used_turns
 OR NEW.used_turns > OLD.used_turns + 1
BEGIN
    SELECT RAISE(ABORT, 'cleardev exception budget is immutable except for one-turn occupancy');
END;

CREATE TRIGGER cleardev_complex_exception_runs_cdc_insert
AFTER INSERT ON cleardev_complex_exception_scope_requests
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExceptionScopeRequestId', NEW.id, 'status', NEW.status), NEW.created_at
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;

CREATE TRIGGER cleardev_complex_exception_ondemand_cdc_insert
AFTER INSERT ON cleardev_complex_exception_ondemand_bindings
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExceptionOnDemandId', NEW.id, 'mode', NEW.mode, 'status', NEW.status), NEW.requested_at
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;

CREATE TRIGGER cleardev_complex_exception_ondemand_cdc_update
AFTER UPDATE ON cleardev_complex_exception_ondemand_bindings
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExceptionOnDemandId', NEW.id, 'mode', NEW.mode, 'status', NEW.status), datetime('now')
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
PRAGMA foreign_keys = ON;
-- +goose StatementEnd

-- +goose Down
-- +goose NO TRANSACTION
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_exception_ondemand_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_exception_ondemand_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_exception_runs_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_exception_budget_update_valid;
DROP TABLE IF EXISTS cleardev_complex_exception_recovery_actions;
DROP TABLE IF EXISTS cleardev_complex_exception_specialist_checks;
DROP TABLE IF EXISTS cleardev_complex_exception_specialist_results;
DROP TABLE IF EXISTS cleardev_complex_exception_agent_steps;
DROP TABLE IF EXISTS cleardev_complex_exception_ondemand_bindings;
DROP TABLE IF EXISTS cleardev_complex_exception_path_leases;
DROP TABLE IF EXISTS cleardev_complex_exception_generated_proofs;
DROP TABLE IF EXISTS cleardev_complex_exception_generated_commands;
DROP TABLE IF EXISTS cleardev_complex_exception_scope_decisions;
DROP TABLE IF EXISTS cleardev_complex_exception_scope_requests;
DROP TABLE IF EXISTS cleardev_complex_exception_budget_occupancies;
DROP TABLE IF EXISTS cleardev_complex_exception_budgets;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid;
CREATE TRIGGER cleardev_complex_execution_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_runs
WHEN NEW.status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    JOIN cleardev_complex_engineering_plans AS plan ON plan.id = NEW.plan_id
    JOIN cleardev_complex_plan_reviews AS review ON review.id = NEW.plan_review_id
    JOIN cleardev_complex_role_bindings AS steward ON steward.id = NEW.steward_role_binding_id
    WHERE version.id = NEW.requirement_version_id
      AND project.id = NEW.development_project_id
      AND project.cancelled_at IS NULL
      AND version.state = 'APPROVED'
      AND version.version = 2
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.plan_sha256 = NEW.plan_sha256
      AND review.plan_id = plan.id
      AND review.plan_sha256 = plan.plan_sha256
      AND review.verdict = 'APPROVED'
      AND (
        (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'ONE_BUILDER_REQUIRED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') = 1)
        OR (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'PARALLEL_UNSAFE_DEGRADED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3)
        OR (NEW.mode = 'PARALLEL' AND NEW.selection_reason_code = 'PARALLEL_PLAN_APPROVED'
           AND NEW.fixed_builder_count BETWEEN 2 AND 3
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
           AND NEW.fixed_builder_count <= json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount'))
      )
      AND json_array_length(plan.plan_json, '$.tasks') BETWEEN 2 AND 6
      AND NOT EXISTS (
          SELECT 1 FROM json_each(plan.plan_json, '$.tasks') AS planned_task
          WHERE json_array_length(planned_task.value, '$.generatedPaths') <> 0
             OR json_array_length(planned_task.value, '$.sharedPathsRequireApproval') <> 0
      )
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
 )
 OR EXISTS (
    SELECT 1 FROM cleardev_direction_stop_gates AS gate
    WHERE gate.requirement_version_id = NEW.requirement_version_id AND gate.status = 'ACTIVE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run lacks the exact approved current plan or is stopped');
END;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA foreign_keys = ON;
-- +goose StatementEnd
