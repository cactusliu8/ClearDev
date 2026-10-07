-- ClearDev S07 PARALLEL complex-execution facts.  The S06 runs table is
-- rebuilt to accept both modes and a frozen builder count; role bindings gain
-- numbered builder slots; task attempts gain their dispatch batch; the new
-- batches and compositions tables keep the parallel waves and controlled
-- candidate combining durable.  S06 rows keep their exact meaning.
-- +goose Up
-- +goose NO TRANSACTION
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
-- +goose StatementEnd

-- Step 1: drop every trigger that references the runs table, plus the attempt
-- update guard that step 5 extends.  They are all recreated in step 7, so the
-- runs rebuild below cannot leave a dangling trigger reference.
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_run_insert_valid;
DROP TRIGGER cleardev_complex_execution_run_update_valid;
DROP TRIGGER cleardev_complex_execution_role_binding_insert_valid;
DROP TRIGGER cleardev_complex_execution_role_binding_update_valid;
DROP TRIGGER cleardev_complex_execution_task_mapping_insert_valid;
DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
DROP TRIGGER cleardev_complex_execution_check_spec_insert_valid;
DROP TRIGGER cleardev_complex_execution_check_run_insert_valid;
DROP TRIGGER cleardev_complex_execution_review_insert_valid;
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
DROP TRIGGER cleardev_complex_execution_result_insert_valid;
DROP TRIGGER cleardev_complex_execution_result_update_valid;
DROP TRIGGER cleardev_complex_execution_run_append_only_delete;
DROP TRIGGER cleardev_complex_execution_result_check_insert_valid;
DROP TRIGGER cleardev_complex_execution_work_item_binding_valid;
DROP TRIGGER cleardev_complex_execution_integration_candidate_binding_valid;
DROP TRIGGER cleardev_complex_execution_work_item_done_valid;
DROP TRIGGER cleardev_complex_execution_completion_finalize;
DROP TRIGGER cleardev_complex_execution_runs_cdc_insert;
DROP TRIGGER cleardev_complex_execution_runs_cdc_update;
DROP TRIGGER cleardev_complex_execution_attempts_cdc_insert;
DROP TRIGGER cleardev_complex_execution_attempts_cdc_update;
DROP TRIGGER cleardev_complex_execution_results_cdc_update;
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
-- +goose StatementEnd

-- Step 2: rebuild the runs table with mode selection facts.
-- +goose StatementBegin
CREATE TABLE cleardev_complex_execution_runs_rebuilt (
    id                         TEXT PRIMARY KEY,
    development_project_id     TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id     TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256         TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    plan_id                    TEXT NOT NULL REFERENCES cleardev_complex_engineering_plans(id),
    plan_review_id             TEXT NOT NULL REFERENCES cleardev_complex_plan_reviews(id),
    plan_sha256                TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    steward_role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    mode                       TEXT NOT NULL CHECK (mode IN ('STANDARD', 'PARALLEL')),
    selection_reason_code      TEXT NOT NULL CHECK (selection_reason_code IN ('ONE_BUILDER_REQUIRED', 'PARALLEL_PLAN_APPROVED', 'PARALLEL_UNSAFE_DEGRADED')),
    fixed_builder_count        INTEGER NOT NULL DEFAULT 1 CHECK (fixed_builder_count BETWEEN 1 AND 3),
    expected_task_set_version  INTEGER NOT NULL CHECK (expected_task_set_version = 0),
    accepted_task_set_version  INTEGER NOT NULL CHECK (accepted_task_set_version = 1),
    execution_package_json     TEXT NOT NULL CHECK (json_valid(execution_package_json)),
    execution_package_sha256   TEXT NOT NULL CHECK (length(execution_package_sha256) = 64 AND execution_package_sha256 NOT GLOB '*[^0-9a-f]*'),
    status                     TEXT NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED', 'REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN', 'COMPLETED')),
    reason_code                TEXT NOT NULL DEFAULT '',
    requested_at               TIMESTAMP NOT NULL,
    accepted_at                TIMESTAMP,
    settled_at                 TIMESTAMP,
    UNIQUE (requirement_version_id, plan_id),
    CHECK ((mode = 'STANDARD' AND fixed_builder_count = 1) OR (mode = 'PARALLEL' AND fixed_builder_count BETWEEN 2 AND 3)),
    CHECK (
        (status = 'PENDING' AND accepted_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'ACCEPTED' AND accepted_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'COMPLETED' AND accepted_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code = '')
        OR (status IN ('REJECTED', 'FAILED') AND accepted_at IS NULL AND settled_at IS NOT NULL AND reason_code <> '')
        OR (status IN ('BLOCKED', 'NEEDS_HUMAN') AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
INSERT INTO cleardev_complex_execution_runs_rebuilt (
    id, development_project_id, requirement_version_id, requirement_sha256, plan_id, plan_review_id,
    plan_sha256, steward_role_binding_id, mode, selection_reason_code, fixed_builder_count,
    expected_task_set_version, accepted_task_set_version, execution_package_json, execution_package_sha256,
    status, reason_code, requested_at, accepted_at, settled_at
)
SELECT
    id, development_project_id, requirement_version_id, requirement_sha256, plan_id, plan_review_id,
    plan_sha256, steward_role_binding_id, mode, selection_reason_code, 1,
    expected_task_set_version, accepted_task_set_version, execution_package_json, execution_package_sha256,
    status, reason_code, requested_at, accepted_at, settled_at
FROM cleardev_complex_execution_runs;
DROP TABLE cleardev_complex_execution_runs;
ALTER TABLE cleardev_complex_execution_runs_rebuilt RENAME TO cleardev_complex_execution_runs;
CREATE INDEX idx_cleardev_complex_execution_runs_requirement
    ON cleardev_complex_execution_runs (development_project_id, requirement_version_id, requested_at);
CREATE UNIQUE INDEX idx_cleardev_complex_execution_active_requirement
    ON cleardev_complex_execution_runs (requirement_version_id)
    WHERE status IN ('PENDING', 'ACCEPTED');
-- +goose StatementEnd

-- Step 3: number the builder slots and allow one active attempt per task.
-- +goose StatementBegin
ALTER TABLE cleardev_complex_execution_role_bindings ADD COLUMN builder_slot INTEGER;
UPDATE cleardev_complex_execution_role_bindings SET builder_slot = 1 WHERE role = 'BUILDER' AND builder_slot IS NULL;
DROP INDEX idx_cleardev_complex_execution_one_builder;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_builder_slots
    ON cleardev_complex_execution_role_bindings (execution_run_id, builder_slot)
    WHERE role = 'BUILDER' AND builder_slot IS NOT NULL;
DROP INDEX idx_cleardev_complex_execution_one_active_attempt;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_active_attempt
    ON cleardev_complex_execution_task_attempts (task_mapping_id)
    WHERE status IN ('PENDING', 'RUNNING', 'OBSERVED', 'REVIEWING');
CREATE UNIQUE INDEX idx_cleardev_complex_execution_builder_active_attempt
    ON cleardev_complex_execution_task_attempts (builder_role_binding_id)
    WHERE status IN ('PENDING', 'RUNNING', 'OBSERVED', 'REVIEWING');
-- +goose StatementEnd

-- Step 4: dispatch batches and controlled compositions for PARALLEL runs.
-- +goose StatementBegin
CREATE TABLE cleardev_complex_execution_batches (
    id               TEXT PRIMARY KEY,
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    ordinal          INTEGER NOT NULL CHECK (ordinal >= 0),
    task_keys_json   TEXT NOT NULL CHECK (json_valid(task_keys_json) AND json_type(task_keys_json) = 'array'),
    common_base_sha  TEXT NOT NULL DEFAULT '' CHECK (common_base_sha = '' OR (length(common_base_sha) = 40 AND common_base_sha NOT GLOB '*[^0-9a-f]*')),
    status           TEXT NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'COMPOSING', 'COMPOSED', 'BLOCKED')),
    created_at       TIMESTAMP NOT NULL,
    composed_at      TIMESTAMP,
    UNIQUE (execution_run_id, ordinal),
    CHECK ((status = 'PENDING' AND composed_at IS NULL) OR (status IN ('RUNNING', 'COMPOSING') AND composed_at IS NULL) OR (status IN ('COMPOSED', 'BLOCKED') AND composed_at IS NOT NULL))
);

CREATE TABLE cleardev_complex_execution_compositions (
    id                        TEXT PRIMARY KEY,
    execution_run_id          TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    batch_id                  TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_batches(id),
    request_id                TEXT NOT NULL UNIQUE CHECK (length(trim(request_id)) > 0),
    input_base_sha            TEXT NOT NULL CHECK (length(input_base_sha) = 40 AND input_base_sha NOT GLOB '*[^0-9a-f]*'),
    input_candidate_ids_json  TEXT NOT NULL CHECK (json_valid(input_candidate_ids_json) AND json_type(input_candidate_ids_json) = 'array'),
    input_candidate_shas_json TEXT NOT NULL CHECK (json_valid(input_candidate_shas_json) AND json_type(input_candidate_shas_json) = 'array'),
    workspace_path            TEXT NOT NULL DEFAULT '',
    output_commit_sha         TEXT NOT NULL DEFAULT '' CHECK (output_commit_sha = '' OR (length(output_commit_sha) = 40 AND output_commit_sha NOT GLOB '*[^0-9a-f]*')),
    status                    TEXT NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'COMPOSED', 'BLOCKED', 'FAILED')),
    conflict_paths_json       TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(conflict_paths_json) AND json_type(conflict_paths_json) = 'array'),
    reason_code               TEXT NOT NULL DEFAULT '',
    created_at                TIMESTAMP NOT NULL,
    settled_at                TIMESTAMP,
    CHECK (json_array_length(input_candidate_ids_json) = json_array_length(input_candidate_shas_json)),
    CHECK (
        (status = 'PENDING' AND settled_at IS NULL AND workspace_path = '' AND output_commit_sha = '' AND conflict_paths_json = '[]' AND reason_code = '')
        OR (status = 'RUNNING' AND settled_at IS NULL AND output_commit_sha = '' AND conflict_paths_json = '[]' AND reason_code = '')
        OR (status = 'COMPOSED' AND settled_at IS NOT NULL AND length(trim(workspace_path)) > 0 AND length(output_commit_sha) = 40 AND conflict_paths_json = '[]' AND reason_code = '')
        OR (status IN ('BLOCKED', 'FAILED') AND settled_at IS NOT NULL AND (json_array_length(conflict_paths_json) > 0 OR reason_code <> ''))
    )
);
-- +goose StatementEnd

-- Step 5: bind attempts to their batch.
-- +goose StatementBegin
ALTER TABLE cleardev_complex_execution_task_attempts ADD COLUMN batch_id TEXT REFERENCES cleardev_complex_execution_batches(id);
-- +goose StatementEnd

-- Step 6: recreate the unchanged S06 guards that step 1 dropped verbatim.
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_task_mapping_insert_valid
BEFORE INSERT ON cleardev_complex_execution_task_mappings
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    WHERE run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution task mapping requires an accepted unstopped execution');
END;

CREATE TRIGGER cleardev_complex_execution_check_spec_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_specs
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    LEFT JOIN cleardev_complex_execution_task_mappings AS task ON task.id = NEW.task_mapping_id
    WHERE run.id = NEW.execution_run_id
      AND ((NEW.check_kind = 'INTEGRATION' AND NEW.task_mapping_id IS NULL)
        OR (NEW.check_kind <> 'INTEGRATION' AND task.execution_run_id = run.id))
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check spec must bind its execution task');
END;

CREATE TRIGGER cleardev_complex_execution_review_insert_valid
BEFORE INSERT ON cleardev_complex_execution_reviews
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_role_bindings AS reviewer ON reviewer.id = NEW.reviewer_role_binding_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_agent_steps AS step ON step.id = NEW.agent_step_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    WHERE attempt.id = NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id = attempt.id
      AND reviewer.execution_run_id = run.id AND reviewer.role = 'REVIEWER' AND reviewer.status = 'BOUND'
      AND reviewer.task_mapping_id = task.id AND reviewer.candidate_commit_id = candidate.id
      AND reviewer.workspace_path = NEW.candidate_worktree_path
      AND reviewer.workspace_path <> (SELECT workspace_path FROM cleardev_complex_execution_role_bindings WHERE id = attempt.builder_role_binding_id)
      AND reviewer.ao_session_id <> builder.ao_session_id
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS steward
          WHERE steward.id = run.steward_role_binding_id
            AND steward.ao_session_id = reviewer.ao_session_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS planner
          WHERE planner.development_project_id = run.development_project_id
            AND planner.role = 'ENGINEERING_PLANNER'
            AND planner.ao_session_id = reviewer.ao_session_id
      )
      AND step.role_binding_id = reviewer.id AND step.step_kind = 'LOCAL_REVIEW' AND step.request_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution review must use an independent Reviewer');
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

CREATE TRIGGER cleardev_complex_execution_result_insert_valid
BEFORE INSERT ON cleardev_complex_execution_results
WHEN NEW.completion_status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    WHERE run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND version.version = 2 AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND run.accepted_task_set_version = 1
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      WHERE task.execution_run_id = run.id
                        AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_verified_candidates AS verified WHERE verified.task_mapping_id = task.id))
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result lacks all verified tasks, final integration, or gate');
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

CREATE TRIGGER cleardev_complex_execution_work_item_binding_valid
BEFORE INSERT ON cleardev_work_items
WHEN NEW.complex_execution_task_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    WHERE task.id = NEW.complex_execution_task_id AND task.work_item_id = NEW.id
      AND run.development_project_id = NEW.development_project_id
      AND run.requirement_version_id = NEW.contract_version_id
      AND run.status = 'ACCEPTED' AND NEW.mode = 'STANDARD' AND NEW.state = 'PLANNED'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution work item must match its accepted task mapping');
END;

CREATE TRIGGER cleardev_complex_execution_work_item_done_valid
BEFORE UPDATE OF state ON cleardev_work_items
WHEN NEW.state = 'DONE' AND OLD.state <> 'DONE' AND NEW.complex_execution_task_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_results AS result ON result.execution_run_id = run.id
    WHERE task.id = NEW.complex_execution_task_id AND run.status = 'ACCEPTED'
      AND result.completion_status = 'COMMITTING'
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution work item DONE requires the final atomic completion transaction');
END;

CREATE TRIGGER cleardev_complex_execution_completion_finalize
AFTER UPDATE ON cleardev_complex_execution_results
WHEN OLD.completion_status = 'PENDING' AND NEW.completion_status = 'COMMITTING'
BEGIN
    UPDATE cleardev_work_items
    SET state = 'DONE', paused_from_state = NULL, updated_at = NEW.committing_at
    WHERE complex_execution_task_id IN (
        SELECT task.id
        FROM cleardev_complex_execution_task_mappings AS task
        WHERE task.execution_run_id = NEW.execution_run_id
    ) AND state <> 'DONE';

    UPDATE cleardev_complex_execution_results
    SET completion_status = 'COMPLETED', completed_at = NEW.committing_at
    WHERE id = NEW.id AND completion_status = 'COMMITTING';

    UPDATE cleardev_complex_execution_runs
    SET status = 'COMPLETED', settled_at = NEW.committing_at
    WHERE id = NEW.execution_run_id AND status = 'ACCEPTED';

    SELECT RAISE(ABORT, 'cleardev complex execution atomic completion did not settle every fact')
    WHERE NOT EXISTS (
        SELECT 1 FROM cleardev_complex_execution_results
        WHERE id = NEW.id AND completion_status = 'COMPLETED'
    ) OR NOT EXISTS (
        SELECT 1 FROM cleardev_complex_execution_runs
        WHERE id = NEW.execution_run_id AND status = 'COMPLETED'
    ) OR NOT EXISTS (
        SELECT 1 FROM cleardev_complex_execution_task_mappings
        WHERE execution_run_id = NEW.execution_run_id
    ) OR EXISTS (
        SELECT 1
        FROM cleardev_complex_execution_task_mappings AS task
        JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
        WHERE task.execution_run_id = NEW.execution_run_id AND work_item.state <> 'DONE'
    );
END;

CREATE TRIGGER cleardev_complex_execution_attempts_cdc_insert
AFTER INSERT ON cleardev_complex_execution_task_attempts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionTaskAttemptId', NEW.id, 'status', NEW.status), datetime('now')
    FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE task.id = NEW.task_mapping_id;
END;

CREATE TRIGGER cleardev_complex_execution_attempts_cdc_update
AFTER UPDATE ON cleardev_complex_execution_task_attempts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionTaskAttemptId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.dispatched_at)
    FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE task.id = NEW.task_mapping_id;
END;

CREATE TRIGGER cleardev_complex_execution_results_cdc_update
AFTER UPDATE ON cleardev_complex_execution_results
WHEN NEW.completion_status = 'COMPLETED'
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionResultId', NEW.id, 'status', NEW.completion_status), COALESCE(NEW.completed_at, NEW.committing_at)
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;

-- +goose StatementEnd

-- Step 7: recreate the rewritten guards and the new S07 facts.  The run
-- trigger accepts both modes and cross-checks the plan's suggested count; the
-- binding trigger numbers builder slots and allows rebasing onto composed
-- commits; the attempt trigger accepts the frozen batch baseline; the batch
-- and composition triggers keep waves and combines durable.
-- +goose StatementBegin
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

CREATE TRIGGER cleardev_complex_execution_run_update_valid
BEFORE UPDATE ON cleardev_complex_execution_runs
WHEN OLD.development_project_id IS NOT NEW.development_project_id
 OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256
 OR OLD.plan_id IS NOT NEW.plan_id
 OR OLD.plan_review_id IS NOT NEW.plan_review_id
 OR OLD.plan_sha256 IS NOT NEW.plan_sha256
 OR OLD.steward_role_binding_id IS NOT NEW.steward_role_binding_id
 OR OLD.mode IS NOT NEW.mode
 OR OLD.selection_reason_code IS NOT NEW.selection_reason_code
 OR OLD.fixed_builder_count IS NOT NEW.fixed_builder_count
 OR OLD.expected_task_set_version IS NOT NEW.expected_task_set_version
 OR OLD.accepted_task_set_version IS NOT NEW.accepted_task_set_version
 OR OLD.execution_package_json IS NOT NEW.execution_package_json
 OR OLD.execution_package_sha256 IS NOT NEW.execution_package_sha256
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('ACCEPTED', 'REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR (OLD.status = 'ACCEPTED' AND NEW.status NOT IN ('COMPLETED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR OLD.status IN ('REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN', 'COMPLETED')
 OR (NEW.mode = 'PARALLEL' AND NEW.status = 'ACCEPTED' AND OLD.status = 'PENDING' AND (
     EXISTS (
        SELECT 1 FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS key
        WHERE batch.execution_run_id = NEW.id
        GROUP BY key.value HAVING count(*) > 1
     )
     OR EXISTS (
        SELECT 1 FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS key
        WHERE batch.execution_run_id = NEW.id
          AND NOT EXISTS (
              SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
              WHERE task.execution_run_id = NEW.id AND task.plan_task_key = key.value
          )
     )
 ))
 OR (NEW.status = 'COMPLETED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_results AS result
    WHERE result.execution_run_id = NEW.id AND result.completion_status = 'COMPLETED'
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run is immutable or settles once');
END;

CREATE TRIGGER cleardev_complex_execution_run_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution runs are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_runs_cdc_insert
AFTER INSERT ON cleardev_complex_execution_runs
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'complexExecutionRunId', NEW.id, 'status', NEW.status), NEW.requested_at
    FROM cleardev_development_projects AS project WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_complex_execution_runs_cdc_update
AFTER UPDATE ON cleardev_complex_execution_runs
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'complexExecutionRunId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.accepted_at)
    FROM cleardev_development_projects AS project WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_complex_execution_role_binding_insert_valid
BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_runs WHERE id = NEW.execution_run_id)
 OR (NEW.role = 'STEWARD' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_complex_role_bindings AS source ON source.id = NEW.source_complex_role_binding_id
    WHERE run.id = NEW.execution_run_id AND source.id = run.steward_role_binding_id
      AND source.role = 'STEWARD' AND source.status = 'BOUND'
 ))
 OR (NEW.ao_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions AS session
	JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
	JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
    WHERE session.id = NEW.ao_session_id AND session.harness = 'codex'
      AND session.session_mode = 'chat' AND session.permission_mode = 'auto'
 ))
 OR (NEW.role = 'BUILDER' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    WHERE run.id = NEW.execution_run_id
      AND NEW.builder_slot IS NOT NULL
      AND NEW.builder_slot BETWEEN 1 AND run.fixed_builder_count
 ))
 OR (NEW.role <> 'BUILDER' AND NEW.builder_slot IS NOT NULL)
 OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER')
     AND (length(trim(NEW.workspace_path)) = 0 OR length(NEW.base_commit_sha) <> 40 OR NEW.base_commit_sha GLOB '*[^0-9a-f]*'))
	OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER') AND NOT EXISTS (
	   SELECT 1
	   FROM sessions AS session
	   JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
	   JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
	   WHERE run.id = NEW.execution_run_id AND session.id = NEW.ao_session_id
	     AND session.kind = 'worker' AND session.harness = 'codex'
	     AND session.session_mode = 'chat' AND session.permission_mode = 'auto'
	     AND session.creation_idempotency_key = NEW.session_creation_idempotency_key
	     AND session.workspace_path = NEW.workspace_path
	     AND (NEW.role = 'REVIEWER' OR session.diff_base_sha = NEW.base_commit_sha)
	))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND NOT EXISTS (
	   SELECT 1
	   FROM cleardev_candidate_commits AS candidate
	   JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = candidate.complex_execution_task_attempt_id
	   JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
	   WHERE candidate.id = NEW.candidate_commit_id AND candidate.commit_sha = NEW.base_commit_sha
	     AND task.id = NEW.task_mapping_id AND task.execution_run_id = NEW.execution_run_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
    SELECT 1 FROM cleardev_complex_execution_role_bindings AS existing
    WHERE existing.execution_run_id = NEW.execution_run_id
      AND existing.id <> NEW.id
      AND existing.status = 'BOUND'
      AND existing.ao_session_id = NEW.ao_session_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_complex_role_bindings AS source ON source.development_project_id = run.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND source.role IN ('STEWARD', 'ENGINEERING_PLANNER')
      AND source.status = 'BOUND'
      AND source.ao_session_id = NEW.ao_session_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
    SELECT 1 FROM cleardev_complex_execution_role_bindings AS builder
    WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER'
      AND builder.workspace_path = NEW.workspace_path
 ))
 OR (NEW.role = 'STEWARD' AND NEW.status = 'BOUND' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_role_bindings AS source
    WHERE source.id = NEW.source_complex_role_binding_id
      AND source.ao_session_id = NEW.ao_session_id
 ))
 OR (NEW.role = 'STEWARD' AND NEW.status = 'REQUESTED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_role_bindings AS source
    JOIN sessions AS session ON session.id = source.ao_session_id
    WHERE source.id = NEW.source_complex_role_binding_id
      AND (session.is_terminated = TRUE OR session.activity_state = 'exited')
 ))
 OR (NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_role_bindings AS prior
    WHERE prior.id = NEW.continuation_of_role_binding_id
      AND prior.execution_run_id = NEW.execution_run_id
      AND prior.role = NEW.role
      AND prior.status IN ('FAILED', 'ENDED')
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution role binding is not an exact auto Codex Chat role');
END;

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
	   SELECT 1
	   FROM sessions AS session
	   JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
	   JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
	   WHERE run.id = NEW.execution_run_id AND session.id = NEW.ao_session_id
	     AND session.harness = 'codex' AND session.session_mode = 'chat'
	     AND session.permission_mode = 'auto'
	     AND session.workspace_path = NEW.workspace_path
	     AND ((NEW.role = 'STEWARD' AND session.kind = 'orchestrator'
	           AND NEW.base_commit_sha = '' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key)
	       OR (NEW.role = 'BUILDER' AND session.kind = 'worker'
	           AND session.creation_idempotency_key = NEW.session_creation_idempotency_key
	           AND (session.diff_base_sha = NEW.base_commit_sha
	                OR EXISTS (
	                   SELECT 1 FROM cleardev_complex_execution_compositions AS composition
	                   WHERE composition.execution_run_id = run.id
	                     AND composition.status = 'COMPOSED'
	                     AND composition.output_commit_sha = NEW.base_commit_sha)))
	       OR (NEW.role = 'REVIEWER' AND session.kind = 'worker'
	           AND session.creation_idempotency_key = NEW.session_creation_idempotency_key))
	))
 OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER')
     AND (length(trim(NEW.workspace_path)) = 0 OR length(NEW.base_commit_sha) <> 40 OR NEW.base_commit_sha GLOB '*[^0-9a-f]*'))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND NOT EXISTS (
	   SELECT 1
	   FROM cleardev_candidate_commits AS candidate
	   JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = candidate.complex_execution_task_attempt_id
	   JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
	   WHERE candidate.id = NEW.candidate_commit_id AND candidate.commit_sha = NEW.base_commit_sha
	     AND task.id = NEW.task_mapping_id AND task.execution_run_id = NEW.execution_run_id
	))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
	   SELECT 1 FROM cleardev_complex_execution_role_bindings AS existing
	   WHERE existing.execution_run_id = NEW.execution_run_id AND existing.id <> NEW.id
	     AND existing.ao_session_id = NEW.ao_session_id
	))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
	   SELECT 1
	   FROM cleardev_complex_execution_runs AS run
	   JOIN cleardev_complex_role_bindings AS source ON source.development_project_id = run.development_project_id
	   WHERE run.id = NEW.execution_run_id AND source.role IN ('STEWARD', 'ENGINEERING_PLANNER')
	     AND source.status = 'BOUND'
	     AND source.ao_session_id = NEW.ao_session_id
	))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
	   SELECT 1 FROM cleardev_complex_execution_role_bindings AS builder
	   WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER'
	     AND builder.workspace_path = NEW.workspace_path
	))
	OR (NEW.role = 'STEWARD' AND NEW.status = 'BOUND' AND NOT EXISTS (
	   SELECT 1
	   FROM cleardev_complex_role_bindings AS source
	   JOIN sessions AS source_session ON source_session.id = source.ao_session_id
	   WHERE source.id = NEW.source_complex_role_binding_id
	     AND (source_session.is_terminated = TRUE OR source_session.activity_state = 'exited')
	     AND source.ao_session_id <> NEW.ao_session_id
	))
	OR (OLD.status = 'BOUND' AND (
	   OLD.ao_session_id IS NOT NEW.ao_session_id
	   OR OLD.workspace_path IS NOT NEW.workspace_path
	   OR (OLD.base_commit_sha IS NOT NEW.base_commit_sha AND NOT (
	       NEW.role = 'BUILDER' AND NEW.status = 'BOUND' AND EXISTS (
	          SELECT 1 FROM cleardev_complex_execution_runs AS run
	          WHERE run.id = NEW.execution_run_id AND run.mode = 'PARALLEL'
	            AND EXISTS (
	              SELECT 1 FROM cleardev_complex_execution_compositions AS composition
	              WHERE composition.execution_run_id = run.id
	                AND composition.status = 'COMPOSED'
	                AND composition.output_commit_sha = NEW.base_commit_sha
	            )
	       )))
	   OR OLD.bound_at IS NOT NEW.bound_at
	))
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status = 'REQUESTED' AND NEW.status NOT IN ('BOUND', 'FAILED'))
 OR (OLD.status = 'BOUND' AND NEW.status <> 'ENDED' AND NOT (
     NEW.role = 'BUILDER' AND NEW.status = 'BOUND'
     AND EXISTS (
         SELECT 1 FROM cleardev_complex_execution_runs AS run
         WHERE run.id = NEW.execution_run_id AND run.mode = 'PARALLEL'
           AND EXISTS (
             SELECT 1 FROM cleardev_complex_execution_compositions AS composition
             WHERE composition.execution_run_id = run.id
               AND composition.status = 'COMPOSED'
               AND composition.output_commit_sha = NEW.base_commit_sha
           )
     )
 ))
 OR OLD.status IN ('FAILED', 'ENDED')
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution role binding is immutable or invalid');
END;

CREATE TRIGGER cleardev_complex_execution_attempt_insert_valid
BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = NEW.builder_role_binding_id
    JOIN cleardev_complex_execution_agent_steps AS step ON step.id = NEW.agent_step_id
    WHERE task.id = NEW.task_mapping_id AND run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND builder.execution_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND'
      AND step.role_binding_id = builder.id AND step.step_kind = 'BUILDER_TASK' AND step.request_id = NEW.id
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
 OR (NEW.round = 1 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts
    WHERE task_mapping_id = NEW.task_mapping_id AND round = 0 AND status = 'REWORK'
 ))
 OR (NEW.batch_id IS NOT NULL AND NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_batches AS batch
    JOIN cleardev_complex_execution_runs AS run ON run.id = batch.execution_run_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.execution_run_id = run.id
    WHERE batch.id = NEW.batch_id AND run.id = NEW.execution_run_id AND run.mode = 'PARALLEL'
      AND batch.status = 'RUNNING' AND NEW.base_commit_sha = batch.common_base_sha
      AND task.id = NEW.task_mapping_id
      AND EXISTS (SELECT 1 FROM json_each(batch.task_keys_json) AS key WHERE key.value = task.plan_task_key)
 ))
 OR (NEW.batch_id IS NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    WHERE run.id = NEW.execution_run_id AND run.mode = 'STANDARD'
 ))
 OR (NEW.round = 0 AND NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    WHERE task.id = NEW.task_mapping_id
      AND ((run.mode = 'STANDARD' AND EXISTS (
              SELECT 1 FROM cleardev_complex_execution_role_bindings AS builder
              WHERE builder.id = NEW.builder_role_binding_id
                AND ((task.ordinal = 0 AND builder.base_commit_sha = NEW.base_commit_sha)
                  OR (task.ordinal > 0 AND NEW.base_commit_sha = (
                      SELECT candidate.commit_sha
                      FROM cleardev_complex_execution_verified_candidates AS verified
                      JOIN cleardev_complex_execution_task_mappings AS prior_task ON prior_task.id = verified.task_mapping_id
                      JOIN cleardev_candidate_commits AS candidate ON candidate.id = verified.candidate_commit_id
                      WHERE prior_task.execution_run_id = task.execution_run_id
                        AND prior_task.ordinal < task.ordinal
                      ORDER BY prior_task.ordinal DESC
                      LIMIT 1
                  )))
          ))
        OR (run.mode = 'PARALLEL' AND NEW.base_commit_sha = (
              SELECT batch.common_base_sha
              FROM cleardev_complex_execution_batches AS batch
              WHERE batch.id = NEW.batch_id AND batch.status = 'RUNNING'
          )))
 ))
 OR (NEW.round = 1 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS prior
    WHERE prior.task_mapping_id = NEW.task_mapping_id AND prior.round = 0
      AND prior.status = 'REWORK' AND prior.base_commit_sha = NEW.base_commit_sha
 ))
 OR (NEW.round = 1 AND NEW.batch_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS prior
    WHERE prior.task_mapping_id = NEW.task_mapping_id AND prior.round = 0 AND prior.batch_id = NEW.batch_id
 ))
 OR EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_dependencies AS dependency
    LEFT JOIN cleardev_complex_execution_verified_candidates AS verified
      ON verified.task_mapping_id = dependency.depends_on_mapping_id
    WHERE dependency.task_mapping_id = NEW.task_mapping_id AND verified.id IS NULL
 )
 OR EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_dependencies AS dependency
    JOIN cleardev_complex_execution_task_mappings AS depends_on ON depends_on.id = dependency.depends_on_mapping_id
    WHERE dependency.task_mapping_id = NEW.task_mapping_id
      AND NEW.batch_id IS NOT NULL
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_execution_batches AS dependency_batch
          WHERE dependency_batch.execution_run_id = NEW.execution_run_id
            AND EXISTS (SELECT 1 FROM json_each(dependency_batch.task_keys_json) AS key WHERE key.value = depends_on.plan_task_key)
            AND dependency_batch.ordinal < (SELECT ordinal FROM cleardev_complex_execution_batches WHERE id = NEW.batch_id)
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution attempt has an invalid Builder, gate, or rework round');
END;

CREATE TRIGGER cleardev_complex_execution_attempt_update_valid
BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id
 OR OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id
 OR OLD.agent_step_id IS NOT NEW.agent_step_id
 OR OLD.round IS NOT NEW.round
 OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
 OR OLD.batch_id IS NOT NEW.batch_id
 OR OLD.status NOT IN ('PENDING', 'RUNNING', 'OBSERVED', 'REVIEWING')
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('RUNNING', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR (OLD.status = 'RUNNING' AND NEW.status NOT IN ('OBSERVED', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED'))
 OR (OLD.status = 'OBSERVED' AND NEW.status NOT IN ('REVIEWING', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED'))
 OR (OLD.status = 'REVIEWING' AND NEW.status NOT IN ('VERIFIED', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution attempt is immutable or invalid');
END;

CREATE TRIGGER cleardev_complex_execution_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN (NEW.complex_execution_result_id IS NULL) <> (NEW.complex_source_candidate_commit_id IS NULL)
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
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution integration candidate must use its final result and candidate');
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
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run must match its candidate, attempt, fixed check, and gate');
END;

CREATE TRIGGER cleardev_complex_execution_batch_insert_valid
BEFORE INSERT ON cleardev_complex_execution_batches
WHEN NEW.status <> 'PENDING' OR NEW.composed_at IS NOT NULL OR NEW.common_base_sha <> ''
 OR json_array_length(NEW.task_keys_json) NOT BETWEEN 1 AND 6
 OR EXISTS (SELECT 1 FROM json_each(NEW.task_keys_json) AS key WHERE typeof(key.value) <> 'text' OR trim(key.value) = '')
 OR (SELECT count(DISTINCT key.value) FROM json_each(NEW.task_keys_json) AS key) <> json_array_length(NEW.task_keys_json)
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    WHERE run.id = NEW.execution_run_id AND run.mode = 'PARALLEL' AND run.status = 'ACCEPTED'
 )
 OR EXISTS (
    SELECT 1 FROM json_each(NEW.task_keys_json) AS key
    WHERE NOT EXISTS (
        SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
        WHERE task.execution_run_id = NEW.execution_run_id AND task.plan_task_key = key.value
    )
 )
 OR EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_batches AS existing, json_each(existing.task_keys_json) AS taken, json_each(NEW.task_keys_json) AS key
    WHERE existing.execution_run_id = NEW.execution_run_id AND taken.value = key.value
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution batch must bind unique accepted tasks');
END;

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

CREATE TRIGGER cleardev_complex_execution_batch_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_batches
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution batches are append-only');
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

CREATE TRIGGER cleardev_complex_execution_composition_update_valid
BEFORE UPDATE ON cleardev_complex_execution_compositions
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.batch_id IS NOT NEW.batch_id
 OR OLD.request_id IS NOT NEW.request_id
 OR OLD.input_base_sha IS NOT NEW.input_base_sha
 OR OLD.input_candidate_ids_json IS NOT NEW.input_candidate_ids_json
 OR OLD.input_candidate_shas_json IS NOT NEW.input_candidate_shas_json
 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status IN ('COMPOSED', 'BLOCKED', 'FAILED')
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('RUNNING', 'FAILED'))
 OR (OLD.status = 'RUNNING' AND NEW.status NOT IN ('COMPOSED', 'BLOCKED', 'FAILED'))
 OR (NEW.status = 'COMPOSED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_batches AS batch
    WHERE batch.id = NEW.batch_id AND batch.status = 'COMPOSING'
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution composition is immutable or settles once');
END;

CREATE TRIGGER cleardev_complex_execution_composition_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_compositions
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution compositions are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_batches_cdc_insert
AFTER INSERT ON cleardev_complex_execution_batches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionBatchId', NEW.id, 'status', NEW.status), NEW.created_at
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;

CREATE TRIGGER cleardev_complex_execution_batches_cdc_update
AFTER UPDATE ON cleardev_complex_execution_batches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionBatchId', NEW.id, 'status', NEW.status), NEW.created_at
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;

CREATE TRIGGER cleardev_complex_execution_compositions_cdc_insert
AFTER INSERT ON cleardev_complex_execution_compositions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionCompositionId', NEW.id, 'status', NEW.status), NEW.created_at
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;

CREATE TRIGGER cleardev_complex_execution_compositions_cdc_update
AFTER UPDATE ON cleardev_complex_execution_compositions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionCompositionId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.created_at)
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
DROP TRIGGER IF EXISTS cleardev_complex_execution_agent_step_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_agent_step_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_agent_step_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempts_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempts_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_batch_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_batch_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_batch_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_batches_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_execution_batches_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_completion_finalize;
DROP TRIGGER IF EXISTS cleardev_complex_execution_composition_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_composition_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_composition_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_compositions_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_execution_compositions_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_dependency_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_dependency_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_dependency_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_evidence_source_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_integration_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_permission_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_required_check_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_results_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_review_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_review_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_review_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_role_binding_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_role_binding_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_role_binding_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_runs_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_execution_runs_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_task_mapping_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_task_mapping_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_task_mapping_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_work_item_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_work_item_done_valid;
DROP TABLE cleardev_complex_execution_compositions;
DROP TABLE cleardev_complex_execution_batches;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TABLE cleardev_complex_execution_task_attempts_s06 (
    id                    TEXT PRIMARY KEY,
    execution_run_id      TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    task_mapping_id       TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    builder_role_binding_id TEXT NOT NULL REFERENCES cleardev_complex_execution_role_bindings(id),
    agent_step_id         TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
    round                 INTEGER NOT NULL CHECK (round IN (0, 1)),
    base_commit_sha       TEXT NOT NULL CHECK (length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'),
    status                TEXT NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'OBSERVED', 'REVIEWING', 'VERIFIED', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED')),
    reason_code           TEXT NOT NULL DEFAULT '',
    dispatched_at         TIMESTAMP,
    settled_at            TIMESTAMP,
    UNIQUE (task_mapping_id, round),
    CHECK (
        (status = 'PENDING' AND dispatched_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status IN ('RUNNING', 'OBSERVED', 'REVIEWING') AND dispatched_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status IN ('VERIFIED', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED') AND dispatched_at IS NOT NULL AND settled_at IS NOT NULL AND (status = 'VERIFIED' OR reason_code <> ''))
    )
);
INSERT INTO cleardev_complex_execution_task_attempts_s06 (
    id, execution_run_id, task_mapping_id, builder_role_binding_id, agent_step_id, round,
    base_commit_sha, status, reason_code, dispatched_at, settled_at
)
SELECT
    id, execution_run_id, task_mapping_id, builder_role_binding_id, agent_step_id, round,
    base_commit_sha, status, reason_code, dispatched_at, settled_at
FROM cleardev_complex_execution_task_attempts;
DROP TABLE cleardev_complex_execution_task_attempts;
ALTER TABLE cleardev_complex_execution_task_attempts_s06 RENAME TO cleardev_complex_execution_task_attempts;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_one_active_attempt
    ON cleardev_complex_execution_task_attempts (execution_run_id)
    WHERE status IN ('PENDING', 'RUNNING', 'OBSERVED', 'REVIEWING');
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_cleardev_complex_execution_task_active_attempt;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_builder_active_attempt;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_builder_slots;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_one_builder
    ON cleardev_complex_execution_role_bindings (execution_run_id)
    WHERE role = 'BUILDER';
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TABLE cleardev_complex_execution_runs_s06 (
    id                         TEXT PRIMARY KEY,
    development_project_id     TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id     TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256         TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    plan_id                    TEXT NOT NULL REFERENCES cleardev_complex_engineering_plans(id),
    plan_review_id             TEXT NOT NULL REFERENCES cleardev_complex_plan_reviews(id),
    plan_sha256                TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    steward_role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    mode                       TEXT NOT NULL CHECK (mode = 'STANDARD'),
    selection_reason_code      TEXT NOT NULL CHECK (selection_reason_code = 'ONE_BUILDER_REQUIRED'),
    expected_task_set_version  INTEGER NOT NULL CHECK (expected_task_set_version = 0),
    accepted_task_set_version  INTEGER NOT NULL CHECK (accepted_task_set_version = 1),
    execution_package_json     TEXT NOT NULL CHECK (json_valid(execution_package_json)),
    execution_package_sha256   TEXT NOT NULL CHECK (length(execution_package_sha256) = 64 AND execution_package_sha256 NOT GLOB '*[^0-9a-f]*'),
    status                     TEXT NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED', 'REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN', 'COMPLETED')),
    reason_code                TEXT NOT NULL DEFAULT '',
    requested_at               TIMESTAMP NOT NULL,
    accepted_at                TIMESTAMP,
    settled_at                 TIMESTAMP,
    UNIQUE (requirement_version_id, plan_id),
    CHECK (
        (status = 'PENDING' AND accepted_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'ACCEPTED' AND accepted_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'COMPLETED' AND accepted_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code = '')
        OR (status IN ('REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN') AND accepted_at IS NULL AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
INSERT INTO cleardev_complex_execution_runs_s06 (
    id, development_project_id, requirement_version_id, requirement_sha256, plan_id, plan_review_id,
    plan_sha256, steward_role_binding_id, mode, selection_reason_code,
    expected_task_set_version, accepted_task_set_version, execution_package_json, execution_package_sha256,
    status, reason_code, requested_at, accepted_at, settled_at
)
SELECT
    id, development_project_id, requirement_version_id, requirement_sha256, plan_id, plan_review_id,
    plan_sha256, steward_role_binding_id, mode, selection_reason_code,
    expected_task_set_version, accepted_task_set_version, execution_package_json, execution_package_sha256,
    status, reason_code, requested_at, accepted_at, settled_at
FROM cleardev_complex_execution_runs
WHERE mode = 'STANDARD';
DROP TABLE cleardev_complex_execution_runs;
ALTER TABLE cleardev_complex_execution_runs_s06 RENAME TO cleardev_complex_execution_runs;
CREATE INDEX idx_cleardev_complex_execution_runs_requirement
    ON cleardev_complex_execution_runs (development_project_id, requirement_version_id, requested_at);
CREATE UNIQUE INDEX idx_cleardev_complex_execution_active_requirement
    ON cleardev_complex_execution_runs (requirement_version_id)
    WHERE status IN ('PENDING', 'ACCEPTED');
-- +goose StatementEnd
-- Restore the complete S06 trigger set.
-- +goose StatementBegin
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
      AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') = 1
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

CREATE TRIGGER cleardev_complex_execution_run_update_valid
BEFORE UPDATE ON cleardev_complex_execution_runs
WHEN OLD.development_project_id IS NOT NEW.development_project_id
 OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256
 OR OLD.plan_id IS NOT NEW.plan_id
 OR OLD.plan_review_id IS NOT NEW.plan_review_id
 OR OLD.plan_sha256 IS NOT NEW.plan_sha256
 OR OLD.steward_role_binding_id IS NOT NEW.steward_role_binding_id
 OR OLD.mode IS NOT NEW.mode
 OR OLD.selection_reason_code IS NOT NEW.selection_reason_code
 OR OLD.expected_task_set_version IS NOT NEW.expected_task_set_version
 OR OLD.accepted_task_set_version IS NOT NEW.accepted_task_set_version
 OR OLD.execution_package_json IS NOT NEW.execution_package_json
 OR OLD.execution_package_sha256 IS NOT NEW.execution_package_sha256
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('ACCEPTED', 'REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR (OLD.status = 'ACCEPTED' AND NEW.status NOT IN ('COMPLETED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR OLD.status IN ('REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN', 'COMPLETED')
 OR (NEW.status = 'COMPLETED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_results AS result
    WHERE result.execution_run_id = NEW.id AND result.completion_status = 'COMPLETED'
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run is immutable or settles once');
END;

CREATE TRIGGER cleardev_complex_execution_role_binding_insert_valid
BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_runs WHERE id = NEW.execution_run_id)
 OR (NEW.role = 'STEWARD' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_complex_role_bindings AS source ON source.id = NEW.source_complex_role_binding_id
    WHERE run.id = NEW.execution_run_id AND source.id = run.steward_role_binding_id
      AND source.role = 'STEWARD' AND source.status = 'BOUND'
 ))
 OR (NEW.ao_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions AS session
	JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
	JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
    WHERE session.id = NEW.ao_session_id AND session.harness = 'codex'
      AND session.session_mode = 'chat' AND session.permission_mode = 'auto'
 ))
 OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER')
     AND (length(trim(NEW.workspace_path)) = 0 OR length(NEW.base_commit_sha) <> 40 OR NEW.base_commit_sha GLOB '*[^0-9a-f]*'))
	OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER') AND NOT EXISTS (
	   SELECT 1
	   FROM sessions AS session
	   JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
	   JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
	   WHERE run.id = NEW.execution_run_id AND session.id = NEW.ao_session_id
	     AND session.kind = 'worker' AND session.harness = 'codex'
	     AND session.session_mode = 'chat' AND session.permission_mode = 'auto'
	     AND session.creation_idempotency_key = NEW.session_creation_idempotency_key
	     AND session.workspace_path = NEW.workspace_path
	     AND (NEW.role = 'REVIEWER' OR session.diff_base_sha = NEW.base_commit_sha)
	))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND NOT EXISTS (
	   SELECT 1
	   FROM cleardev_candidate_commits AS candidate
	   JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = candidate.complex_execution_task_attempt_id
	   JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
	   WHERE candidate.id = NEW.candidate_commit_id AND candidate.commit_sha = NEW.base_commit_sha
	     AND task.id = NEW.task_mapping_id AND task.execution_run_id = NEW.execution_run_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
    SELECT 1 FROM cleardev_complex_execution_role_bindings AS existing
    WHERE existing.execution_run_id = NEW.execution_run_id
      AND existing.id <> NEW.id
      AND existing.status = 'BOUND'
      AND existing.ao_session_id = NEW.ao_session_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_complex_role_bindings AS source ON source.development_project_id = run.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND source.role IN ('STEWARD', 'ENGINEERING_PLANNER')
      AND source.status = 'BOUND'
      AND source.ao_session_id = NEW.ao_session_id
 ))
 OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
    SELECT 1 FROM cleardev_complex_execution_role_bindings AS builder
    WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER'
      AND builder.workspace_path = NEW.workspace_path
 ))
 OR (NEW.role = 'STEWARD' AND NEW.status = 'BOUND' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_role_bindings AS source
    WHERE source.id = NEW.source_complex_role_binding_id
      AND source.ao_session_id = NEW.ao_session_id
 ))
 OR (NEW.role = 'STEWARD' AND NEW.status = 'REQUESTED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_role_bindings AS source
    JOIN sessions AS session ON session.id = source.ao_session_id
    WHERE source.id = NEW.source_complex_role_binding_id
      AND (session.is_terminated = TRUE OR session.activity_state = 'exited')
 ))
 OR (NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_role_bindings AS prior
    WHERE prior.id = NEW.continuation_of_role_binding_id
      AND prior.execution_run_id = NEW.execution_run_id
      AND prior.role = NEW.role
      AND prior.status IN ('FAILED', 'ENDED')
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution role binding is not an exact auto Codex Chat role');
END;

CREATE TRIGGER cleardev_complex_execution_role_binding_update_valid
BEFORE UPDATE ON cleardev_complex_execution_role_bindings
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.role IS NOT NEW.role
 OR OLD.source_complex_role_binding_id IS NOT NEW.source_complex_role_binding_id
 OR OLD.continuation_of_role_binding_id IS NOT NEW.continuation_of_role_binding_id
 OR OLD.task_mapping_id IS NOT NEW.task_mapping_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.session_creation_idempotency_key IS NOT NEW.session_creation_idempotency_key
	OR (NEW.status = 'BOUND' AND NOT EXISTS (
	   SELECT 1
	   FROM sessions AS session
	   JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
	   JOIN cleardev_complex_execution_runs AS run ON run.development_project_id = project.id
	   WHERE run.id = NEW.execution_run_id AND session.id = NEW.ao_session_id
	     AND session.harness = 'codex' AND session.session_mode = 'chat'
	     AND session.permission_mode = 'auto'
	     AND session.workspace_path = NEW.workspace_path
	     AND ((NEW.role = 'STEWARD' AND session.kind = 'orchestrator'
	           AND NEW.base_commit_sha = '' AND session.creation_idempotency_key = NEW.session_creation_idempotency_key)
	       OR (NEW.role = 'BUILDER' AND session.kind = 'worker'
	           AND session.diff_base_sha = NEW.base_commit_sha
	           AND session.creation_idempotency_key = NEW.session_creation_idempotency_key)
	       OR (NEW.role = 'REVIEWER' AND session.kind = 'worker'
	           AND session.creation_idempotency_key = NEW.session_creation_idempotency_key))
	))
 OR (NEW.status = 'BOUND' AND NEW.role IN ('BUILDER', 'REVIEWER')
     AND (length(trim(NEW.workspace_path)) = 0 OR length(NEW.base_commit_sha) <> 40 OR NEW.base_commit_sha GLOB '*[^0-9a-f]*'))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND NOT EXISTS (
	   SELECT 1
	   FROM cleardev_candidate_commits AS candidate
	   JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = candidate.complex_execution_task_attempt_id
	   JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
	   WHERE candidate.id = NEW.candidate_commit_id AND candidate.commit_sha = NEW.base_commit_sha
	     AND task.id = NEW.task_mapping_id AND task.execution_run_id = NEW.execution_run_id
	))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
	   SELECT 1 FROM cleardev_complex_execution_role_bindings AS existing
	   WHERE existing.execution_run_id = NEW.execution_run_id AND existing.id <> NEW.id
	     AND existing.ao_session_id = NEW.ao_session_id
	))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
	   SELECT 1
	   FROM cleardev_complex_execution_runs AS run
	   JOIN cleardev_complex_role_bindings AS source ON source.development_project_id = run.development_project_id
	   WHERE run.id = NEW.execution_run_id AND source.role IN ('STEWARD', 'ENGINEERING_PLANNER')
	     AND source.ao_session_id = NEW.ao_session_id
	))
	OR (NEW.role = 'REVIEWER' AND NEW.status = 'BOUND' AND EXISTS (
	   SELECT 1 FROM cleardev_complex_execution_role_bindings AS builder
	   WHERE builder.execution_run_id = NEW.execution_run_id AND builder.role = 'BUILDER'
	     AND builder.workspace_path = NEW.workspace_path
	))
	OR (NEW.role = 'STEWARD' AND NEW.status = 'BOUND' AND NOT EXISTS (
	   SELECT 1
	   FROM cleardev_complex_role_bindings AS source
	   JOIN sessions AS source_session ON source_session.id = source.ao_session_id
	   WHERE source.id = NEW.source_complex_role_binding_id
	     AND (source_session.is_terminated = TRUE OR source_session.activity_state = 'exited')
	     AND source.ao_session_id <> NEW.ao_session_id
	))
	OR (OLD.status = 'BOUND' AND (
	   OLD.ao_session_id IS NOT NEW.ao_session_id
	   OR OLD.workspace_path IS NOT NEW.workspace_path
	   OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
	   OR OLD.bound_at IS NOT NEW.bound_at
	))
 OR OLD.requested_at IS NOT NEW.requested_at
 OR (OLD.status = 'REQUESTED' AND NEW.status NOT IN ('BOUND', 'FAILED'))
 OR (OLD.status = 'BOUND' AND NEW.status <> 'ENDED')
 OR OLD.status IN ('FAILED', 'ENDED')
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution role binding is immutable or invalid');
END;

CREATE TRIGGER cleardev_complex_execution_agent_step_insert_valid
BEFORE INSERT ON cleardev_complex_execution_agent_steps
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_role_bindings AS binding
    WHERE binding.id = NEW.role_binding_id
      AND ((NEW.step_kind = 'DISPATCH_REQUEST' AND binding.role = 'STEWARD')
        OR (NEW.step_kind = 'BUILDER_TASK' AND binding.role = 'BUILDER')
        OR (NEW.step_kind = 'LOCAL_REVIEW' AND binding.role = 'REVIEWER'))
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution agent step has the wrong role');
END;

CREATE TRIGGER cleardev_complex_execution_agent_step_update_valid
BEFORE UPDATE ON cleardev_complex_execution_agent_steps
WHEN OLD.role_binding_id IS NOT NEW.role_binding_id
 OR OLD.step_kind IS NOT NEW.step_kind
 OR OLD.request_id IS NOT NEW.request_id
 OR OLD.client_message_id IS NOT NEW.client_message_id
 OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
 OR OLD.requested_at IS NOT NEW.requested_at
 OR OLD.send_status NOT IN ('PENDING', 'SENT')
 OR (OLD.send_status = 'PENDING' AND NEW.send_status NOT IN ('SENT', 'FAILED'))
 OR (OLD.send_status = 'SENT' AND NEW.send_status NOT IN ('SETTLED', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution agent step is immutable or invalid');
END;

CREATE TRIGGER cleardev_complex_execution_task_mapping_insert_valid
BEFORE INSERT ON cleardev_complex_execution_task_mappings
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    WHERE run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution task mapping requires an accepted unstopped execution');
END;

CREATE TRIGGER cleardev_complex_execution_task_mapping_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_task_mappings
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution task mappings are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_task_mapping_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_task_mappings
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution task mappings are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_dependency_insert_valid
BEFORE INSERT ON cleardev_complex_execution_dependencies
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_task_mappings AS dependency ON dependency.id = NEW.depends_on_mapping_id
    WHERE task.id = NEW.task_mapping_id AND task.execution_run_id = dependency.execution_run_id
      AND dependency.ordinal < task.ordinal
      AND json_extract(task.task_packet_json, '$.dependencyTaskKeys[' || NEW.dependency_ordinal || ']') = dependency.plan_task_key
      AND json_extract(task.task_packet_json, '$.dependencyTaskIds[' || NEW.dependency_ordinal || ']') = dependency.work_item_id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution dependency must stay inside one execution');
END;

CREATE TRIGGER cleardev_complex_execution_attempt_insert_valid
BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = NEW.builder_role_binding_id
    JOIN cleardev_complex_execution_agent_steps AS step ON step.id = NEW.agent_step_id
    WHERE task.id = NEW.task_mapping_id AND run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND builder.execution_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND'
      AND step.role_binding_id = builder.id AND step.step_kind = 'BUILDER_TASK' AND step.request_id = NEW.id
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
 OR (NEW.round = 1 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts
    WHERE task_mapping_id = NEW.task_mapping_id AND round = 0 AND status = 'REWORK'
 ))
 OR (NEW.round = 0 AND NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = NEW.builder_role_binding_id
    WHERE task.id = NEW.task_mapping_id
      AND ((task.ordinal = 0 AND builder.base_commit_sha = NEW.base_commit_sha)
        OR (task.ordinal > 0 AND NEW.base_commit_sha = (
            SELECT candidate.commit_sha
            FROM cleardev_complex_execution_verified_candidates AS verified
            JOIN cleardev_complex_execution_task_mappings AS prior_task ON prior_task.id = verified.task_mapping_id
            JOIN cleardev_candidate_commits AS candidate ON candidate.id = verified.candidate_commit_id
            WHERE prior_task.execution_run_id = task.execution_run_id
              AND prior_task.ordinal < task.ordinal
            ORDER BY prior_task.ordinal DESC
            LIMIT 1
        )))
 ))
 OR (NEW.round = 1 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS prior
    WHERE prior.task_mapping_id = NEW.task_mapping_id AND prior.round = 0
      AND prior.status = 'REWORK' AND prior.base_commit_sha = NEW.base_commit_sha
 ))
 OR EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_dependencies AS dependency
    LEFT JOIN cleardev_complex_execution_verified_candidates AS verified
      ON verified.task_mapping_id = dependency.depends_on_mapping_id
    WHERE dependency.task_mapping_id = NEW.task_mapping_id AND verified.id IS NULL
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution attempt has an invalid Builder, gate, or rework round');
END;

CREATE TRIGGER cleardev_complex_execution_attempt_update_valid
BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id
 OR OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id
 OR OLD.agent_step_id IS NOT NEW.agent_step_id
 OR OLD.round IS NOT NEW.round
 OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
 OR OLD.status NOT IN ('PENDING', 'RUNNING', 'OBSERVED', 'REVIEWING')
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('RUNNING', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN'))
 OR (OLD.status = 'RUNNING' AND NEW.status NOT IN ('OBSERVED', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED'))
 OR (OLD.status = 'OBSERVED' AND NEW.status NOT IN ('REVIEWING', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED'))
 OR (OLD.status = 'REVIEWING' AND NEW.status NOT IN ('VERIFIED', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution attempt is immutable or invalid');
END;

CREATE TRIGGER cleardev_complex_execution_check_spec_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_specs
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    LEFT JOIN cleardev_complex_execution_task_mappings AS task ON task.id = NEW.task_mapping_id
    WHERE run.id = NEW.execution_run_id
      AND ((NEW.check_kind = 'INTEGRATION' AND NEW.task_mapping_id IS NULL)
        OR (NEW.check_kind <> 'INTEGRATION' AND task.execution_run_id = run.id))
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check spec must bind its execution task');
END;

CREATE TRIGGER cleardev_complex_execution_check_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = NEW.task_attempt_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = attempt.execution_run_id
    WHERE spec.id = NEW.check_spec_id AND spec.execution_run_id = run.id
      AND (spec.task_mapping_id IS NULL OR spec.task_mapping_id = task.id)
      AND candidate.work_item_id = task.work_item_id
      AND candidate.complex_execution_task_attempt_id = attempt.id
      AND candidate.commit_sha = NEW.candidate_commit_sha
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
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
 OR OLD.status NOT IN ('PENDING', 'STARTED')
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('STARTED', 'FAILED'))
 OR (OLD.status = 'STARTED' AND NEW.status NOT IN ('SETTLED', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run is immutable or settles once');
END;

CREATE TRIGGER cleardev_complex_execution_review_insert_valid
BEFORE INSERT ON cleardev_complex_execution_reviews
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_role_bindings AS reviewer ON reviewer.id = NEW.reviewer_role_binding_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_agent_steps AS step ON step.id = NEW.agent_step_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    WHERE attempt.id = NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id = attempt.id
      AND reviewer.execution_run_id = run.id AND reviewer.role = 'REVIEWER' AND reviewer.status = 'BOUND'
      AND reviewer.task_mapping_id = task.id AND reviewer.candidate_commit_id = candidate.id
      AND reviewer.workspace_path = NEW.candidate_worktree_path
      AND reviewer.workspace_path <> (SELECT workspace_path FROM cleardev_complex_execution_role_bindings WHERE id = attempt.builder_role_binding_id)
      AND reviewer.ao_session_id <> builder.ao_session_id
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS steward
          WHERE steward.id = run.steward_role_binding_id
            AND steward.ao_session_id = reviewer.ao_session_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS planner
          WHERE planner.development_project_id = run.development_project_id
            AND planner.role = 'ENGINEERING_PLANNER'
            AND planner.ao_session_id = reviewer.ao_session_id
      )
      AND step.role_binding_id = reviewer.id AND step.step_kind = 'LOCAL_REVIEW' AND step.request_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution review must use an independent Reviewer');
END;

CREATE TRIGGER cleardev_complex_execution_review_update_valid
BEFORE UPDATE ON cleardev_complex_execution_reviews
WHEN OLD.task_attempt_id IS NOT NEW.task_attempt_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.reviewer_role_binding_id IS NOT NEW.reviewer_role_binding_id
 OR OLD.agent_step_id IS NOT NEW.agent_step_id
 OR OLD.review_packet_json IS NOT NEW.review_packet_json
 OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256
 OR OLD.candidate_worktree_path IS NOT NEW.candidate_worktree_path
 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status <> 'PENDING'
 OR NEW.status NOT IN ('SETTLED', 'FAILED')
 OR (NEW.status = 'SETTLED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_agent_steps AS step
    WHERE step.id = NEW.agent_step_id AND step.send_status = 'SETTLED'
      AND step.turn_id = NEW.turn_id AND step.final_message_id = NEW.final_message_id
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution review is immutable or settles once');
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

CREATE TRIGGER cleardev_complex_execution_result_insert_valid
BEFORE INSERT ON cleardev_complex_execution_results
WHEN NEW.completion_status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    WHERE run.id = NEW.execution_run_id AND run.status = 'ACCEPTED'
      AND version.version = 2 AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND run.accepted_task_set_version = 1
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      WHERE task.execution_run_id = run.id
                        AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_verified_candidates AS verified WHERE verified.task_mapping_id = task.id))
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result lacks all verified tasks, final integration, or gate');
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

CREATE TRIGGER cleardev_complex_execution_run_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution runs are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_role_binding_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_role_bindings
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution role bindings are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_agent_step_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_agent_steps
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution agent steps are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_dependency_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_dependencies
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution dependencies are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_dependency_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_dependencies
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution dependencies are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_attempt_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_task_attempts
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution task attempts are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_check_specs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check specs are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_check_specs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check specs are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_check_run_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_check_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check runs are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_review_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution reviews are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_verified_candidate_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_verified_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidates are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_verified_candidate_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_verified_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidates are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_result_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_results
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution results are append-only');
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

CREATE TRIGGER cleardev_complex_execution_result_check_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_result_checks
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result checks are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_result_check_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_result_checks
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result checks are append-only');
END;

CREATE TRIGGER cleardev_complex_execution_work_item_binding_valid
BEFORE INSERT ON cleardev_work_items
WHEN NEW.complex_execution_task_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    WHERE task.id = NEW.complex_execution_task_id AND task.work_item_id = NEW.id
      AND run.development_project_id = NEW.development_project_id
      AND run.requirement_version_id = NEW.contract_version_id
      AND run.status = 'ACCEPTED' AND NEW.mode = 'STANDARD' AND NEW.state = 'PLANNED'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution work item must match its accepted task mapping');
END;

CREATE TRIGGER cleardev_complex_execution_permission_binding_valid
BEFORE INSERT ON cleardev_path_permission_versions
WHEN EXISTS (SELECT 1 FROM cleardev_work_items WHERE id = NEW.work_item_id AND complex_execution_task_id IS NOT NULL)
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_work_items AS work_item ON work_item.id = NEW.work_item_id
    WHERE task.id = work_item.complex_execution_task_id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution permission must bind its task mapping');
END;

CREATE TRIGGER cleardev_complex_execution_required_check_binding_valid
BEFORE INSERT ON cleardev_required_checks
WHEN EXISTS (SELECT 1 FROM cleardev_work_items WHERE id = NEW.work_item_id AND complex_execution_task_id IS NOT NULL)
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = spec.task_mapping_id
    WHERE spec.id = NEW.complex_execution_check_spec_id AND task.work_item_id = NEW.work_item_id
      AND spec.required_check_id = NEW.id AND spec.check_name = NEW.name
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution required check must bind its fixed spec');
END;

CREATE TRIGGER cleardev_complex_execution_candidate_binding_valid
BEFORE INSERT ON cleardev_candidate_commits
WHEN (NEW.complex_execution_task_attempt_id IS NULL) <> (NEW.complex_execution_base_commit_sha IS NULL)
 OR (NEW.complex_execution_task_attempt_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
    WHERE attempt.id = NEW.complex_execution_task_attempt_id AND attempt.base_commit_sha = NEW.complex_execution_base_commit_sha
      AND task.work_item_id = NEW.work_item_id AND work_item.complex_execution_task_id = task.id
      AND builder.status = 'BOUND' AND builder.ao_session_id = NEW.ao_session_id
      AND NEW.dispatch_id IS NULL AND NEW.base_commit_sha IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution candidate must use its Builder, task, and fixed base only');
END;

CREATE TRIGGER cleardev_complex_execution_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN (NEW.complex_execution_result_id IS NULL) <> (NEW.complex_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NOT NULL AND NOT EXISTS (
	    SELECT 1 FROM cleardev_complex_execution_results AS result
	    JOIN cleardev_complex_execution_runs AS run ON run.id = result.execution_run_id
	    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_source_candidate_commit_id
	    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.execution_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND'
	    WHERE result.id = NEW.complex_execution_result_id AND result.integration_candidate_id = NEW.id
	      AND run.development_project_id = NEW.development_project_id
	      AND NEW.requirement_version_id = run.requirement_version_id
	      AND NEW.task_set_version = run.accepted_task_set_version
	      AND NEW.ao_session_id = builder.ao_session_id
	      AND candidate.commit_sha = NEW.commit_sha
	      AND candidate.id = (
	          SELECT verified.candidate_commit_id
	          FROM cleardev_complex_execution_verified_candidates AS verified
	          JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	          WHERE task.execution_run_id = run.id
	          ORDER BY task.ordinal DESC
	          LIMIT 1
	      )
	      AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution integration candidate must use its final result and candidate');
END;

CREATE TRIGGER cleardev_complex_execution_evidence_source_valid
BEFORE INSERT ON cleardev_evidence
WHEN (NEW.complex_execution_check_run_id IS NOT NULL AND NEW.complex_execution_review_id IS NOT NULL)
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
 OR (NEW.complex_execution_check_run_id IS NULL AND NEW.complex_execution_review_id IS NULL AND (
    EXISTS (SELECT 1 FROM cleardev_candidate_commits WHERE id = NEW.candidate_commit_id AND complex_execution_task_attempt_id IS NOT NULL)
    OR EXISTS (SELECT 1 FROM cleardev_integration_candidates WHERE id = NEW.integration_candidate_id AND complex_execution_result_id IS NOT NULL)
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution evidence must derive from its exact check or Reviewer');
END;

CREATE TRIGGER cleardev_complex_execution_work_item_done_valid
BEFORE UPDATE OF state ON cleardev_work_items
WHEN NEW.state = 'DONE' AND OLD.state <> 'DONE' AND NEW.complex_execution_task_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_results AS result ON result.execution_run_id = run.id
    WHERE task.id = NEW.complex_execution_task_id AND run.status = 'ACCEPTED'
      AND result.completion_status = 'COMMITTING'
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution work item DONE requires the final atomic completion transaction');
END;

CREATE TRIGGER cleardev_complex_execution_completion_finalize
AFTER UPDATE ON cleardev_complex_execution_results
WHEN OLD.completion_status = 'PENDING' AND NEW.completion_status = 'COMMITTING'
BEGIN
    UPDATE cleardev_work_items
    SET state = 'DONE', paused_from_state = NULL, updated_at = NEW.committing_at
    WHERE complex_execution_task_id IN (
        SELECT task.id
        FROM cleardev_complex_execution_task_mappings AS task
        WHERE task.execution_run_id = NEW.execution_run_id
    ) AND state <> 'DONE';

    UPDATE cleardev_complex_execution_results
    SET completion_status = 'COMPLETED', completed_at = NEW.committing_at
    WHERE id = NEW.id AND completion_status = 'COMMITTING';

    UPDATE cleardev_complex_execution_runs
    SET status = 'COMPLETED', settled_at = NEW.committing_at
    WHERE id = NEW.execution_run_id AND status = 'ACCEPTED';

    SELECT RAISE(ABORT, 'cleardev complex execution atomic completion did not settle every fact')
    WHERE NOT EXISTS (
        SELECT 1 FROM cleardev_complex_execution_results
        WHERE id = NEW.id AND completion_status = 'COMPLETED'
    ) OR NOT EXISTS (
        SELECT 1 FROM cleardev_complex_execution_runs
        WHERE id = NEW.execution_run_id AND status = 'COMPLETED'
    ) OR NOT EXISTS (
        SELECT 1 FROM cleardev_complex_execution_task_mappings
        WHERE execution_run_id = NEW.execution_run_id
    ) OR EXISTS (
        SELECT 1
        FROM cleardev_complex_execution_task_mappings AS task
        JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
        WHERE task.execution_run_id = NEW.execution_run_id AND work_item.state <> 'DONE'
    );
END;

CREATE TRIGGER cleardev_complex_execution_runs_cdc_insert
AFTER INSERT ON cleardev_complex_execution_runs
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'complexExecutionRunId', NEW.id, 'status', NEW.status), NEW.requested_at
    FROM cleardev_development_projects AS project WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_complex_execution_runs_cdc_update
AFTER UPDATE ON cleardev_complex_execution_runs
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'complexExecutionRunId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.accepted_at)
    FROM cleardev_development_projects AS project WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_complex_execution_attempts_cdc_insert
AFTER INSERT ON cleardev_complex_execution_task_attempts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionTaskAttemptId', NEW.id, 'status', NEW.status), datetime('now')
    FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE task.id = NEW.task_mapping_id;
END;

CREATE TRIGGER cleardev_complex_execution_attempts_cdc_update
AFTER UPDATE ON cleardev_complex_execution_task_attempts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionTaskAttemptId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.dispatched_at)
    FROM cleardev_complex_execution_task_mappings AS task
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE task.id = NEW.task_mapping_id;
END;

CREATE TRIGGER cleardev_complex_execution_results_cdc_update
AFTER UPDATE ON cleardev_complex_execution_results
WHEN NEW.completion_status = 'COMPLETED'
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexExecutionResultId', NEW.id, 'status', NEW.completion_status), COALESCE(NEW.completed_at, NEW.committing_at)
    FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.execution_run_id;
END;

-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA foreign_keys = ON;
-- +goose StatementEnd
