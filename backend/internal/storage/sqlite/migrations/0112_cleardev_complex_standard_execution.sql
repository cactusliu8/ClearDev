-- ClearDev S06 STANDARD complex-execution facts.  S02 remains a one-task
-- flow: none of its dispatch, check, review, or completion tables is reused.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_complex_execution_runs (
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
CREATE INDEX idx_cleardev_complex_execution_runs_requirement
    ON cleardev_complex_execution_runs (development_project_id, requirement_version_id, requested_at);
CREATE UNIQUE INDEX idx_cleardev_complex_execution_active_requirement
    ON cleardev_complex_execution_runs (requirement_version_id)
    WHERE status IN ('PENDING', 'ACCEPTED');

CREATE TABLE cleardev_complex_execution_role_bindings (
    id                               TEXT PRIMARY KEY,
    execution_run_id                 TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    role                             TEXT NOT NULL CHECK (role IN ('STEWARD', 'BUILDER', 'REVIEWER')),
    source_complex_role_binding_id   TEXT REFERENCES cleardev_complex_role_bindings(id),
    continuation_of_role_binding_id  TEXT REFERENCES cleardev_complex_execution_role_bindings(id),
    task_mapping_id                  TEXT,
    candidate_commit_id              TEXT REFERENCES cleardev_candidate_commits(id),
    session_creation_idempotency_key TEXT NOT NULL UNIQUE CHECK (length(trim(session_creation_idempotency_key)) > 0),
    ao_session_id                    TEXT REFERENCES sessions(id),
    workspace_path                   TEXT NOT NULL DEFAULT '',
    base_commit_sha                  TEXT NOT NULL DEFAULT '' CHECK (base_commit_sha = '' OR (length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*')),
    status                           TEXT NOT NULL CHECK (status IN ('REQUESTED', 'BOUND', 'FAILED', 'ENDED')),
    reason_code                      TEXT NOT NULL DEFAULT '',
    requested_at                     TIMESTAMP NOT NULL,
    bound_at                         TIMESTAMP,
    ended_at                         TIMESTAMP,
    CHECK (
        (status = 'REQUESTED' AND ao_session_id IS NULL AND bound_at IS NULL AND ended_at IS NULL)
        OR (status = 'BOUND' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NULL)
        OR (status = 'FAILED' AND ao_session_id IS NULL AND ended_at IS NOT NULL)
        OR (status = 'ENDED' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NOT NULL)
    ),
    CHECK ((role = 'STEWARD' AND source_complex_role_binding_id IS NOT NULL) OR (role <> 'STEWARD' AND source_complex_role_binding_id IS NULL)),
    CHECK ((role = 'REVIEWER' AND task_mapping_id IS NOT NULL AND candidate_commit_id IS NOT NULL) OR (role <> 'REVIEWER' AND task_mapping_id IS NULL AND candidate_commit_id IS NULL)),
    CHECK (role NOT IN ('BUILDER', 'REVIEWER') OR status <> 'BOUND' OR
        (length(trim(workspace_path)) > 0 AND length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'))
);
CREATE UNIQUE INDEX idx_cleardev_complex_execution_one_builder
    ON cleardev_complex_execution_role_bindings (execution_run_id)
    WHERE role = 'BUILDER';
CREATE UNIQUE INDEX idx_cleardev_complex_execution_one_steward
    ON cleardev_complex_execution_role_bindings (execution_run_id)
    WHERE role = 'STEWARD' AND status IN ('REQUESTED', 'BOUND');
CREATE UNIQUE INDEX idx_cleardev_complex_execution_reviewer_candidate
    ON cleardev_complex_execution_role_bindings (execution_run_id, candidate_commit_id)
    WHERE role = 'REVIEWER';
CREATE UNIQUE INDEX idx_cleardev_complex_execution_active_reviewer
    ON cleardev_complex_execution_role_bindings (execution_run_id)
    WHERE role = 'REVIEWER' AND status IN ('REQUESTED', 'BOUND');
CREATE UNIQUE INDEX idx_cleardev_complex_execution_session
    ON cleardev_complex_execution_role_bindings (execution_run_id, ao_session_id)
    WHERE ao_session_id IS NOT NULL;

CREATE TABLE cleardev_complex_execution_agent_steps (
    id                 TEXT PRIMARY KEY,
    role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_execution_role_bindings(id),
    step_kind          TEXT NOT NULL CHECK (step_kind IN ('DISPATCH_REQUEST', 'BUILDER_TASK', 'LOCAL_REVIEW')),
    request_id         TEXT NOT NULL CHECK (length(trim(request_id)) > 0),
    client_message_id  TEXT NOT NULL UNIQUE CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256      TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    send_status        TEXT NOT NULL CHECK (send_status IN ('PENDING', 'SENT', 'SETTLED', 'FAILED')),
    turn_id            TEXT,
    final_message_id   TEXT,
    final_message_text TEXT,
    message_sha256     TEXT CHECK (message_sha256 IS NULL OR (length(message_sha256) = 64 AND message_sha256 NOT GLOB '*[^0-9a-f]*')),
    requested_at       TIMESTAMP NOT NULL,
    sent_at            TIMESTAMP,
    completed_at       TIMESTAMP,
    failed_at          TIMESTAMP,
    reason_code        TEXT NOT NULL DEFAULT '',
    UNIQUE (role_binding_id, step_kind, request_id),
    CHECK (
        (send_status = 'PENDING' AND sent_at IS NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SENT' AND sent_at IS NOT NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SETTLED' AND sent_at IS NOT NULL AND turn_id IS NOT NULL AND final_message_id IS NOT NULL AND length(final_message_text) > 0 AND message_sha256 IS NOT NULL AND completed_at IS NOT NULL AND failed_at IS NULL)
        OR (send_status = 'FAILED' AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NOT NULL AND reason_code <> '')
    )
);

CREATE TABLE cleardev_complex_execution_task_mappings (
    id                   TEXT PRIMARY KEY,
    execution_run_id     TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    plan_task_key        TEXT NOT NULL CHECK (length(trim(plan_task_key)) > 0),
    work_item_id         TEXT NOT NULL UNIQUE REFERENCES cleardev_work_items(id) DEFERRABLE INITIALLY DEFERRED,
    ordinal              INTEGER NOT NULL CHECK (ordinal >= 0),
    task_packet_json     TEXT NOT NULL CHECK (json_valid(task_packet_json)),
    task_packet_sha256   TEXT NOT NULL CHECK (length(task_packet_sha256) = 64 AND task_packet_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at           TIMESTAMP NOT NULL,
    UNIQUE (execution_run_id, plan_task_key),
    UNIQUE (execution_run_id, ordinal)
);

CREATE TABLE cleardev_complex_execution_dependencies (
    task_mapping_id       TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    depends_on_mapping_id TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    dependency_ordinal    INTEGER NOT NULL CHECK (dependency_ordinal >= 0),
    PRIMARY KEY (task_mapping_id, depends_on_mapping_id),
    UNIQUE (task_mapping_id, dependency_ordinal),
    CHECK (task_mapping_id <> depends_on_mapping_id)
);

CREATE TABLE cleardev_complex_execution_task_attempts (
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
CREATE UNIQUE INDEX idx_cleardev_complex_execution_one_active_attempt
    ON cleardev_complex_execution_task_attempts (execution_run_id)
    WHERE status IN ('PENDING', 'RUNNING', 'OBSERVED', 'REVIEWING');

CREATE TABLE cleardev_complex_execution_check_specs (
    id                    TEXT PRIMARY KEY,
    execution_run_id      TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    task_mapping_id       TEXT REFERENCES cleardev_complex_execution_task_mappings(id),
    required_check_id     TEXT REFERENCES cleardev_required_checks(id) DEFERRABLE INITIALLY DEFERRED,
    check_kind            TEXT NOT NULL CHECK (check_kind IN ('SCOPE', 'REQUIRED_CHECK', 'INTEGRATION')),
    check_name            TEXT NOT NULL DEFAULT '',
    check_spec_sha256     TEXT NOT NULL CHECK (length(check_spec_sha256) = 64 AND check_spec_sha256 NOT GLOB '*[^0-9a-f]*'),
    argv_json             TEXT NOT NULL CHECK (json_valid(argv_json) AND json_type(argv_json) = 'array'),
    timeout_seconds       INTEGER NOT NULL CHECK (timeout_seconds >= 0),
    created_at            TIMESTAMP NOT NULL,
    CHECK ((check_kind = 'SCOPE' AND task_mapping_id IS NOT NULL AND check_name = '' AND json(argv_json) = json('[]') AND timeout_seconds = 0)
        OR (check_kind = 'REQUIRED_CHECK' AND task_mapping_id IS NOT NULL AND check_name <> '' AND json_array_length(argv_json) > 0 AND timeout_seconds = 60)
        OR (check_kind = 'INTEGRATION' AND task_mapping_id IS NULL AND check_name <> '' AND json_array_length(argv_json) > 0 AND timeout_seconds = 60)),
    CHECK (check_kind = 'SCOPE' OR
        (check_name = 'email-unit'
            AND json(argv_json) = json('["node","--test","test/email.test.js"]')
            AND check_spec_sha256 = 'feafc89ad5e5314058f839a1f280aa8cff20503583f40379c01f4d97dd031466') OR
        (check_name = 'deduplicate-unit'
            AND json(argv_json) = json('["node","--test","test/deduplicate.test.js"]')
            AND check_spec_sha256 = '04f123a6661183016601642f5aea4db485ef025a371e884a52b6ed4a11cee37e') OR
        (check_name = 'summary-unit'
            AND json(argv_json) = json('["node","--test","test/summary.test.js"]')
            AND check_spec_sha256 = '7de135c41f08e1eb60ace8d93642a74645118f3dfb54d515e0099a39059a372f') OR
        (check_name = 'all-tests'
            AND json(argv_json) = json('["node","--test"]')
            AND check_spec_sha256 = '82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586')),
    CHECK ((check_kind = 'REQUIRED_CHECK' AND required_check_id IS NOT NULL) OR (check_kind <> 'REQUIRED_CHECK' AND required_check_id IS NULL))
);
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_check_spec
    ON cleardev_complex_execution_check_specs (task_mapping_id, check_kind, check_name)
    WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_integration_check_spec
    ON cleardev_complex_execution_check_specs (execution_run_id, check_kind, check_name)
    WHERE task_mapping_id IS NULL;

CREATE TABLE cleardev_complex_execution_check_runs (
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
    UNIQUE (check_spec_id, task_attempt_id, candidate_commit_id),
    CHECK (
        (status = 'PENDING' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'STARTED' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'SETTLED' AND timed_out IS NOT NULL AND output_summary IS NOT NULL AND output_sha256 IS NOT NULL AND changed_paths_json IS NOT NULL AND result IS NOT NULL AND started_at IS NOT NULL AND settled_at IS NOT NULL AND ((result = 'PASS' AND reason_code = '') OR (result = 'FAIL' AND reason_code <> '')))
        OR (status = 'FAILED' AND started_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code <> '')
    )
);

CREATE TABLE cleardev_complex_execution_reviews (
    id                       TEXT PRIMARY KEY,
    task_attempt_id          TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id      TEXT NOT NULL UNIQUE REFERENCES cleardev_candidate_commits(id),
    reviewer_role_binding_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_role_bindings(id),
    agent_step_id            TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
    review_packet_json       TEXT NOT NULL CHECK (json_valid(review_packet_json)),
    review_packet_sha256     TEXT NOT NULL CHECK (length(review_packet_sha256) = 64 AND review_packet_sha256 NOT GLOB '*[^0-9a-f]*'),
    candidate_worktree_path  TEXT NOT NULL CHECK (length(trim(candidate_worktree_path)) > 0),
    status                   TEXT NOT NULL CHECK (status IN ('PENDING', 'SETTLED', 'FAILED')),
    turn_id                  TEXT,
    final_message_id         TEXT,
    verdict                  TEXT CHECK (verdict IN ('PASS', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN')),
    reason_code              TEXT NOT NULL DEFAULT '',
    summary                  TEXT,
    created_at               TIMESTAMP NOT NULL,
    settled_at               TIMESTAMP,
    CHECK (
        (status = 'PENDING' AND turn_id IS NULL AND final_message_id IS NULL AND verdict IS NULL AND reason_code = '' AND summary IS NULL AND settled_at IS NULL)
        OR (status = 'SETTLED' AND turn_id IS NOT NULL AND final_message_id IS NOT NULL AND verdict IS NOT NULL AND reason_code <> '' AND summary IS NOT NULL AND settled_at IS NOT NULL)
        OR (status = 'FAILED' AND reason_code <> '' AND settled_at IS NOT NULL)
    )
);

CREATE TABLE cleardev_complex_execution_verified_candidates (
    id                       TEXT PRIMARY KEY,
    task_mapping_id          TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_mappings(id),
    task_attempt_id          TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id      TEXT NOT NULL UNIQUE REFERENCES cleardev_candidate_commits(id),
    scope_check_run_id       TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_check_runs(id),
    required_check_runs_json TEXT NOT NULL CHECK (json_valid(required_check_runs_json) AND json_type(required_check_runs_json) = 'array'),
    review_id                TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_reviews(id),
    verified_at              TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_complex_execution_results (
    id                       TEXT PRIMARY KEY,
    execution_run_id         TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_runs(id),
    integration_candidate_id TEXT NOT NULL UNIQUE REFERENCES cleardev_integration_candidates(id) DEFERRABLE INITIALLY DEFERRED,
    completion_status        TEXT NOT NULL CHECK (completion_status IN ('PENDING', 'COMMITTING', 'COMPLETED')),
    created_at               TIMESTAMP NOT NULL,
    committing_at            TIMESTAMP,
    completed_at             TIMESTAMP,
    CHECK (
        (completion_status = 'PENDING' AND committing_at IS NULL AND completed_at IS NULL)
        OR (completion_status = 'COMMITTING' AND committing_at IS NOT NULL AND completed_at IS NULL)
        OR (completion_status = 'COMPLETED' AND committing_at IS NOT NULL AND completed_at IS NOT NULL)
    )
);

CREATE TABLE cleardev_complex_execution_result_checks (
    result_id    TEXT NOT NULL REFERENCES cleardev_complex_execution_results(id),
    check_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_check_runs(id),
    PRIMARY KEY (result_id, check_run_id)
);

ALTER TABLE cleardev_work_items
    ADD COLUMN complex_execution_task_id TEXT REFERENCES cleardev_complex_execution_task_mappings(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_path_permission_versions
    ADD COLUMN complex_execution_task_id TEXT REFERENCES cleardev_complex_execution_task_mappings(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_required_checks
    ADD COLUMN complex_execution_check_spec_id TEXT REFERENCES cleardev_complex_execution_check_specs(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_candidate_commits
    ADD COLUMN complex_execution_task_attempt_id TEXT REFERENCES cleardev_complex_execution_task_attempts(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_candidate_commits
    ADD COLUMN complex_execution_base_commit_sha TEXT CHECK (complex_execution_base_commit_sha IS NULL OR (length(complex_execution_base_commit_sha) = 40 AND complex_execution_base_commit_sha NOT GLOB '*[^0-9a-f]*'));
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN complex_execution_result_id TEXT REFERENCES cleardev_complex_execution_results(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN complex_source_candidate_commit_id TEXT REFERENCES cleardev_candidate_commits(id);
ALTER TABLE cleardev_evidence
    ADD COLUMN complex_execution_check_run_id TEXT REFERENCES cleardev_complex_execution_check_runs(id);
ALTER TABLE cleardev_evidence
    ADD COLUMN complex_execution_review_id TEXT REFERENCES cleardev_complex_execution_reviews(id);
-- +goose StatementEnd

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
-- +goose StatementEnd

-- +goose StatementBegin
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

-- A single PENDING -> COMMITTING statement is the completion command.  The
-- trigger finishes every S06 task, the result, and the run inside that same
-- SQLite statement so COMMITTING or a partial set of DONE tasks can never be
-- committed as an intermediate durable state.
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
-- +goose StatementEnd

-- +goose StatementBegin
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

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_results_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempts_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempts_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_execution_runs_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_runs_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_execution_completion_finalize;
DROP TRIGGER IF EXISTS cleardev_complex_execution_work_item_done_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_evidence_source_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_check_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_review_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_dependency_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_dependency_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_agent_step_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_role_binding_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_integration_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_required_check_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_permission_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_work_item_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_verified_candidate_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_review_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_review_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_run_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_attempt_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_dependency_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_task_mapping_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_task_mapping_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_task_mapping_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_agent_step_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_agent_step_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_role_binding_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_role_binding_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_one_active_attempt;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_session;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_active_reviewer;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_reviewer_candidate;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_one_steward;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_one_builder;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_active_requirement;
DROP INDEX IF EXISTS idx_cleardev_complex_execution_runs_requirement;
DROP TABLE IF EXISTS cleardev_complex_execution_result_checks;
DROP TABLE IF EXISTS cleardev_complex_execution_results;
DROP TABLE IF EXISTS cleardev_complex_execution_verified_candidates;
DROP TABLE IF EXISTS cleardev_complex_execution_reviews;
DROP TABLE IF EXISTS cleardev_complex_execution_check_runs;
DROP TABLE IF EXISTS cleardev_complex_execution_check_specs;
DROP TABLE IF EXISTS cleardev_complex_execution_task_attempts;
DROP TABLE IF EXISTS cleardev_complex_execution_dependencies;
DROP TABLE IF EXISTS cleardev_complex_execution_task_mappings;
DROP TABLE IF EXISTS cleardev_complex_execution_agent_steps;
DROP TABLE IF EXISTS cleardev_complex_execution_role_bindings;
DROP TABLE IF EXISTS cleardev_complex_execution_runs;
ALTER TABLE cleardev_evidence DROP COLUMN complex_execution_review_id;
ALTER TABLE cleardev_evidence DROP COLUMN complex_execution_check_run_id;
ALTER TABLE cleardev_integration_candidates DROP COLUMN complex_source_candidate_commit_id;
ALTER TABLE cleardev_integration_candidates DROP COLUMN complex_execution_result_id;
ALTER TABLE cleardev_candidate_commits DROP COLUMN complex_execution_base_commit_sha;
ALTER TABLE cleardev_candidate_commits DROP COLUMN complex_execution_task_attempt_id;
ALTER TABLE cleardev_required_checks DROP COLUMN complex_execution_check_spec_id;
ALTER TABLE cleardev_path_permission_versions DROP COLUMN complex_execution_task_id;
ALTER TABLE cleardev_work_items DROP COLUMN complex_execution_task_id;
-- +goose StatementEnd
