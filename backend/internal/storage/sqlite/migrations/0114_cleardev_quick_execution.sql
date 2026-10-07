-- ClearDev S08 QUICK follow-up execution facts. S06/S07 runs stay unique per
-- confirmed v2; this migration adds independent tables so a completed complex
-- execution can accept one low-risk follow-up without rewriting 0106-0113.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_complex_quick_runs (
    id                         TEXT PRIMARY KEY,
    development_project_id     TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id     TEXT NOT NULL UNIQUE REFERENCES cleardev_contract_versions(id),
    requirement_sha256         TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    source_execution_run_id    TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    plan_id                    TEXT NOT NULL REFERENCES cleardev_complex_engineering_plans(id),
    plan_sha256                TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    source_task_key            TEXT NOT NULL DEFAULT '' CHECK (source_task_key = '' OR length(trim(source_task_key)) > 0),
    integration_base_sha       TEXT NOT NULL CHECK (length(integration_base_sha) = 40 AND integration_base_sha NOT GLOB '*[^0-9a-f]*'),
    steward_role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    mode                       TEXT NOT NULL CHECK (mode = 'QUICK'),
    selection_reason_code      TEXT NOT NULL DEFAULT '',
    expected_task_set_version  INTEGER NOT NULL CHECK (expected_task_set_version = 1),
    accepted_task_set_version  INTEGER NOT NULL CHECK (accepted_task_set_version = 2),
    execution_package_json     TEXT NOT NULL CHECK (json_valid(execution_package_json)),
    execution_package_sha256   TEXT NOT NULL CHECK (length(execution_package_sha256) = 64 AND execution_package_sha256 NOT GLOB '*[^0-9a-f]*'),
    status                     TEXT NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED', 'REJECTED', 'FAILED', 'BLOCKED', 'NEEDS_HUMAN', 'COMPLETED')),
    reason_code                TEXT NOT NULL DEFAULT '',
    requested_at               TIMESTAMP NOT NULL,
    accepted_at                TIMESTAMP,
    settled_at                 TIMESTAMP,
    UNIQUE (source_execution_run_id),
    CHECK (
        (status = 'PENDING' AND accepted_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'ACCEPTED' AND accepted_at IS NOT NULL AND settled_at IS NULL AND reason_code = '' AND length(trim(source_task_key)) > 0)
        OR (status = 'COMPLETED' AND accepted_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code = '' AND length(trim(source_task_key)) > 0)
        OR (status IN ('REJECTED', 'FAILED') AND accepted_at IS NULL AND settled_at IS NOT NULL AND reason_code <> '')
        OR (status IN ('BLOCKED', 'NEEDS_HUMAN') AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
CREATE INDEX idx_cleardev_complex_quick_runs_requirement
    ON cleardev_complex_quick_runs (development_project_id, requirement_version_id, requested_at);
CREATE UNIQUE INDEX idx_cleardev_complex_quick_active_requirement
    ON cleardev_complex_quick_runs (requirement_version_id)
    WHERE status IN ('PENDING', 'ACCEPTED');

CREATE TABLE cleardev_complex_quick_role_bindings (
    id                               TEXT PRIMARY KEY,
    quick_run_id                     TEXT NOT NULL REFERENCES cleardev_complex_quick_runs(id),
    role                             TEXT NOT NULL CHECK (role IN ('STEWARD', 'BUILDER')),
    source_complex_role_binding_id   TEXT REFERENCES cleardev_complex_role_bindings(id),
    continuation_of_role_binding_id  TEXT REFERENCES cleardev_complex_quick_role_bindings(id),
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
    CHECK ((role = 'STEWARD' AND source_complex_role_binding_id IS NOT NULL) OR (role = 'BUILDER' AND source_complex_role_binding_id IS NULL)),
    CHECK (role <> 'BUILDER' OR status <> 'BOUND' OR
        (length(trim(workspace_path)) > 0 AND length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'))
);
CREATE UNIQUE INDEX idx_cleardev_complex_quick_one_builder
    ON cleardev_complex_quick_role_bindings (quick_run_id)
    WHERE role = 'BUILDER';
CREATE UNIQUE INDEX idx_cleardev_complex_quick_one_steward
    ON cleardev_complex_quick_role_bindings (quick_run_id)
    WHERE role = 'STEWARD' AND status IN ('REQUESTED', 'BOUND');
CREATE UNIQUE INDEX idx_cleardev_complex_quick_session
    ON cleardev_complex_quick_role_bindings (quick_run_id, ao_session_id)
    WHERE ao_session_id IS NOT NULL;

CREATE TABLE cleardev_complex_quick_agent_steps (
    id                 TEXT PRIMARY KEY,
    role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_quick_role_bindings(id),
    step_kind          TEXT NOT NULL CHECK (step_kind IN ('DISPATCH_REQUEST', 'BUILDER_TASK')),
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

CREATE TABLE cleardev_complex_quick_task_mappings (
    id                   TEXT PRIMARY KEY,
    quick_run_id         TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_quick_runs(id),
    plan_task_key        TEXT NOT NULL CHECK (length(trim(plan_task_key)) > 0),
    work_item_id         TEXT NOT NULL UNIQUE REFERENCES cleardev_work_items(id) DEFERRABLE INITIALLY DEFERRED,
    ordinal              INTEGER NOT NULL CHECK (ordinal = 0),
    task_packet_json     TEXT NOT NULL CHECK (json_valid(task_packet_json)),
    task_packet_sha256   TEXT NOT NULL CHECK (length(task_packet_sha256) = 64 AND task_packet_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at           TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_complex_quick_task_attempts (
    id                      TEXT PRIMARY KEY,
    quick_run_id            TEXT NOT NULL REFERENCES cleardev_complex_quick_runs(id),
    task_mapping_id         TEXT NOT NULL REFERENCES cleardev_complex_quick_task_mappings(id),
    builder_role_binding_id TEXT NOT NULL REFERENCES cleardev_complex_quick_role_bindings(id),
    agent_step_id           TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_quick_agent_steps(id),
    round                   INTEGER NOT NULL CHECK (round IN (0, 1)),
    base_commit_sha         TEXT NOT NULL CHECK (length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'),
    status                  TEXT NOT NULL CHECK (status IN ('PENDING', 'RUNNING', 'OBSERVED', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED')),
    reason_code             TEXT NOT NULL DEFAULT '',
    dispatched_at           TIMESTAMP,
    settled_at              TIMESTAMP,
    UNIQUE (task_mapping_id, round),
    CHECK (
        (status = 'PENDING' AND dispatched_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status IN ('RUNNING', 'OBSERVED') AND dispatched_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status IN ('REWORK', 'BLOCKED', 'NEEDS_HUMAN', 'FAILED') AND dispatched_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
CREATE UNIQUE INDEX idx_cleardev_complex_quick_one_active_attempt
    ON cleardev_complex_quick_task_attempts (quick_run_id)
    WHERE status IN ('PENDING', 'RUNNING', 'OBSERVED');

CREATE TABLE cleardev_complex_quick_check_specs (
    id                    TEXT PRIMARY KEY,
    quick_run_id          TEXT NOT NULL REFERENCES cleardev_complex_quick_runs(id),
    task_mapping_id       TEXT REFERENCES cleardev_complex_quick_task_mappings(id),
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
CREATE UNIQUE INDEX idx_cleardev_complex_quick_task_check_spec
    ON cleardev_complex_quick_check_specs (task_mapping_id, check_kind, check_name)
    WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_quick_integration_check_spec
    ON cleardev_complex_quick_check_specs (quick_run_id, check_kind, check_name)
    WHERE task_mapping_id IS NULL;

CREATE TABLE cleardev_complex_quick_check_runs (
    id                    TEXT PRIMARY KEY,
    check_spec_id         TEXT NOT NULL REFERENCES cleardev_complex_quick_check_specs(id),
    task_attempt_id       TEXT NOT NULL REFERENCES cleardev_complex_quick_task_attempts(id),
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

CREATE TABLE cleardev_complex_quick_results (
    id                       TEXT PRIMARY KEY,
    quick_run_id             TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_quick_runs(id),
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

CREATE TABLE cleardev_complex_quick_result_checks (
    result_id    TEXT NOT NULL REFERENCES cleardev_complex_quick_results(id),
    check_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_quick_check_runs(id),
    PRIMARY KEY (result_id, check_run_id)
);

ALTER TABLE cleardev_work_items
    ADD COLUMN complex_quick_task_id TEXT REFERENCES cleardev_complex_quick_task_mappings(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_path_permission_versions
    ADD COLUMN complex_quick_task_id TEXT REFERENCES cleardev_complex_quick_task_mappings(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_required_checks
    ADD COLUMN complex_quick_check_spec_id TEXT REFERENCES cleardev_complex_quick_check_specs(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_candidate_commits
    ADD COLUMN complex_quick_task_attempt_id TEXT REFERENCES cleardev_complex_quick_task_attempts(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_candidate_commits
    ADD COLUMN complex_quick_base_commit_sha TEXT CHECK (complex_quick_base_commit_sha IS NULL OR (length(complex_quick_base_commit_sha) = 40 AND complex_quick_base_commit_sha NOT GLOB '*[^0-9a-f]*'));
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN complex_quick_result_id TEXT REFERENCES cleardev_complex_quick_results(id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN complex_quick_source_candidate_commit_id TEXT REFERENCES cleardev_candidate_commits(id);
ALTER TABLE cleardev_evidence
    ADD COLUMN complex_quick_check_run_id TEXT REFERENCES cleardev_complex_quick_check_runs(id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_quick_run_insert_valid
BEFORE INSERT ON cleardev_complex_quick_runs
WHEN NEW.status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    JOIN cleardev_complex_execution_runs AS source ON source.id = NEW.source_execution_run_id
    JOIN cleardev_complex_engineering_plans AS plan ON plan.id = NEW.plan_id
    JOIN cleardev_complex_role_bindings AS steward ON steward.id = NEW.steward_role_binding_id
    JOIN cleardev_complex_execution_results AS result ON result.execution_run_id = source.id
    JOIN cleardev_integration_candidates AS integration ON integration.id = result.integration_candidate_id
    WHERE version.id = NEW.requirement_version_id
      AND project.id = NEW.development_project_id
      AND project.cancelled_at IS NULL
      AND version.state = 'APPROVED'
      AND version.version = 2
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND source.development_project_id = project.id
      AND source.requirement_version_id = version.id
      AND source.requirement_sha256 = NEW.requirement_sha256
      AND source.plan_id = NEW.plan_id
      AND source.plan_sha256 = NEW.plan_sha256
      AND source.status = 'COMPLETED'
      AND source.accepted_task_set_version = 1
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.plan_sha256 = NEW.plan_sha256
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
      AND result.completion_status = 'COMPLETED'
      AND integration.commit_sha = NEW.integration_base_sha
      AND integration.task_set_version = 1
 )
 OR EXISTS (
    SELECT 1 FROM cleardev_direction_stop_gates AS gate
    WHERE gate.requirement_version_id = NEW.requirement_version_id AND gate.status = 'ACTIVE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution requires a completed current v2 integration');
END;

CREATE TRIGGER cleardev_complex_quick_run_update_valid
BEFORE UPDATE ON cleardev_complex_quick_runs
WHEN NEW.id <> OLD.id
 OR NEW.development_project_id <> OLD.development_project_id
 OR NEW.requirement_version_id <> OLD.requirement_version_id
 OR NEW.requirement_sha256 <> OLD.requirement_sha256
 OR NEW.source_execution_run_id <> OLD.source_execution_run_id
 OR NEW.plan_id <> OLD.plan_id
 OR NEW.plan_sha256 <> OLD.plan_sha256
 OR (NEW.source_task_key <> OLD.source_task_key AND NOT (OLD.status = 'PENDING' AND OLD.source_task_key = '' AND length(trim(NEW.source_task_key)) > 0))
 OR NEW.integration_base_sha <> OLD.integration_base_sha
 OR NEW.steward_role_binding_id <> OLD.steward_role_binding_id
 OR NEW.mode <> OLD.mode
 OR NEW.expected_task_set_version <> OLD.expected_task_set_version
 OR NEW.accepted_task_set_version <> OLD.accepted_task_set_version
 OR NEW.execution_package_json <> OLD.execution_package_json
 OR NEW.execution_package_sha256 <> OLD.execution_package_sha256
 OR NEW.requested_at <> OLD.requested_at
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution run immutable fields cannot change');
END;

CREATE TRIGGER cleardev_complex_quick_run_append_only_delete
BEFORE DELETE ON cleardev_complex_quick_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution runs are append-only');
END;

CREATE TRIGGER cleardev_complex_quick_task_mapping_insert_valid
BEFORE INSERT ON cleardev_complex_quick_task_mappings
WHEN NEW.ordinal <> 0
 OR ifnull(json_extract(NEW.task_packet_json, '$.taskKey'), '') <> NEW.plan_task_key
 OR ifnull(json_extract(NEW.task_packet_json, '$.mode'), '') <> 'QUICK'
 OR CAST(ifnull(json_extract(NEW.task_packet_json, '$.taskSetVersion'), 0) AS INTEGER) <> 2
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_runs AS run
    JOIN cleardev_complex_execution_task_mappings AS source ON source.execution_run_id = run.source_execution_run_id
    JOIN cleardev_work_items AS work_item ON work_item.id = source.work_item_id
    WHERE run.id = NEW.quick_run_id
      AND run.status = 'ACCEPTED'
      AND run.source_task_key = NEW.plan_task_key
      AND source.plan_task_key = NEW.plan_task_key
      AND work_item.state = 'DONE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution task must match the accepted source task');
END;

CREATE TRIGGER cleardev_complex_quick_work_item_xor
BEFORE INSERT ON cleardev_work_items
WHEN NEW.complex_execution_task_id IS NOT NULL AND NEW.complex_quick_task_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'cleardev work item cannot bind both complex and quick execution');
END;

CREATE TRIGGER cleardev_complex_quick_work_item_binding_valid
BEFORE INSERT ON cleardev_work_items
WHEN NEW.complex_quick_task_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_task_mappings AS task
    JOIN cleardev_complex_quick_runs AS run ON run.id = task.quick_run_id
    WHERE task.id = NEW.complex_quick_task_id AND task.work_item_id = NEW.id
      AND run.development_project_id = NEW.development_project_id
      AND run.requirement_version_id = NEW.contract_version_id
      AND run.status = 'ACCEPTED' AND NEW.mode = 'QUICK' AND NEW.state = 'PLANNED'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution work item must match its accepted task mapping');
END;

CREATE TRIGGER cleardev_complex_quick_permission_binding_valid
BEFORE INSERT ON cleardev_path_permission_versions
WHEN EXISTS (SELECT 1 FROM cleardev_work_items WHERE id = NEW.work_item_id AND complex_quick_task_id IS NOT NULL)
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_task_mappings AS task
    JOIN cleardev_work_items AS work_item ON work_item.id = NEW.work_item_id
    WHERE task.id = work_item.complex_quick_task_id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution permission must bind its task mapping');
END;

CREATE TRIGGER cleardev_complex_quick_required_check_binding_valid
BEFORE INSERT ON cleardev_required_checks
WHEN EXISTS (SELECT 1 FROM cleardev_work_items WHERE id = NEW.work_item_id AND complex_quick_task_id IS NOT NULL)
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_check_specs AS spec
    JOIN cleardev_complex_quick_task_mappings AS task ON task.id = spec.task_mapping_id
    WHERE spec.id = NEW.complex_quick_check_spec_id AND task.work_item_id = NEW.work_item_id
      AND spec.required_check_id = NEW.id AND spec.check_name = NEW.name
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution required check must bind its fixed spec');
END;

CREATE TRIGGER cleardev_complex_quick_candidate_binding_valid
BEFORE INSERT ON cleardev_candidate_commits
WHEN (NEW.complex_quick_task_attempt_id IS NULL) <> (NEW.complex_quick_base_commit_sha IS NULL)
 OR (NEW.complex_quick_task_attempt_id IS NOT NULL AND NEW.complex_execution_task_attempt_id IS NOT NULL)
 OR (NEW.complex_quick_task_attempt_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_task_attempts AS attempt
    JOIN cleardev_complex_quick_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_quick_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
    WHERE attempt.id = NEW.complex_quick_task_attempt_id AND attempt.base_commit_sha = NEW.complex_quick_base_commit_sha
      AND task.work_item_id = NEW.work_item_id AND work_item.complex_quick_task_id = task.id
      AND builder.status = 'BOUND' AND builder.ao_session_id = NEW.ao_session_id
      AND NEW.dispatch_id IS NULL AND NEW.base_commit_sha IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution candidate must use its Builder, task, and fixed base only');
END;

CREATE TRIGGER cleardev_complex_quick_work_item_done_valid
BEFORE UPDATE OF state ON cleardev_work_items
WHEN NEW.state = 'DONE' AND OLD.state <> 'DONE' AND NEW.complex_quick_task_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_task_mappings AS task
    JOIN cleardev_complex_quick_runs AS run ON run.id = task.quick_run_id
    JOIN cleardev_complex_quick_results AS result ON result.quick_run_id = run.id
    WHERE task.id = NEW.complex_quick_task_id AND run.status = 'ACCEPTED'
      AND result.completion_status = 'COMMITTING'
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution work item DONE requires the final atomic completion transaction');
END;

CREATE TRIGGER cleardev_complex_quick_completion_finalize
AFTER UPDATE ON cleardev_complex_quick_results
WHEN OLD.completion_status = 'PENDING' AND NEW.completion_status = 'COMMITTING'
BEGIN
    UPDATE cleardev_work_items
    SET state = 'DONE', paused_from_state = NULL, updated_at = NEW.committing_at
    WHERE complex_quick_task_id IN (
        SELECT task.id FROM cleardev_complex_quick_task_mappings AS task WHERE task.quick_run_id = NEW.quick_run_id
    ) AND state <> 'DONE';

    UPDATE cleardev_complex_quick_results
    SET completion_status = 'COMPLETED', completed_at = NEW.committing_at
    WHERE id = NEW.id AND completion_status = 'COMMITTING';

    UPDATE cleardev_complex_quick_runs
    SET status = 'COMPLETED', settled_at = NEW.committing_at
    WHERE id = NEW.quick_run_id AND status = 'ACCEPTED';

    SELECT RAISE(ABORT, 'cleardev quick execution atomic completion did not settle every fact')
    WHERE NOT EXISTS (
        SELECT 1 FROM cleardev_complex_quick_results WHERE id = NEW.id AND completion_status = 'COMPLETED'
    ) OR NOT EXISTS (
        SELECT 1 FROM cleardev_complex_quick_runs WHERE id = NEW.quick_run_id AND status = 'COMPLETED'
    ) OR EXISTS (
        SELECT 1 FROM cleardev_complex_quick_task_mappings AS task
        JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
        WHERE task.quick_run_id = NEW.quick_run_id AND work_item.state <> 'DONE'
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_integration_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_evidence_source_valid;

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

-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_quick_runs_cdc_insert
AFTER INSERT ON cleardev_complex_quick_runs
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'complexQuickRunId', NEW.id, 'status', NEW.status), NEW.requested_at
    FROM cleardev_development_projects AS project WHERE project.id = NEW.development_project_id;
END;
CREATE TRIGGER cleardev_complex_quick_runs_cdc_update
AFTER UPDATE ON cleardev_complex_quick_runs
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'complexQuickRunId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.accepted_at)
    FROM cleardev_development_projects AS project WHERE project.id = NEW.development_project_id;
END;
CREATE TRIGGER cleardev_complex_quick_attempts_cdc_insert
AFTER INSERT ON cleardev_complex_quick_task_attempts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexQuickTaskAttemptId', NEW.id, 'status', NEW.status), datetime('now')
    FROM cleardev_complex_quick_task_mappings AS task
    JOIN cleardev_complex_quick_runs AS run ON run.id = task.quick_run_id
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE task.id = NEW.task_mapping_id;
END;
CREATE TRIGGER cleardev_complex_quick_attempts_cdc_update
AFTER UPDATE ON cleardev_complex_quick_task_attempts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexQuickTaskAttemptId', NEW.id, 'status', NEW.status), COALESCE(NEW.settled_at, NEW.dispatched_at)
    FROM cleardev_complex_quick_task_mappings AS task
    JOIN cleardev_complex_quick_runs AS run ON run.id = task.quick_run_id
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE task.id = NEW.task_mapping_id;
END;
CREATE TRIGGER cleardev_complex_quick_results_cdc_update
AFTER UPDATE ON cleardev_complex_quick_results
WHEN NEW.completion_status = 'COMPLETED'
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('complexQuickResultId', NEW.id, 'status', NEW.completion_status), COALESCE(NEW.completed_at, NEW.committing_at)
    FROM cleardev_complex_quick_runs AS run
    JOIN cleardev_development_projects AS project ON project.id = run.development_project_id
    WHERE run.id = NEW.quick_run_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_integration_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_evidence_source_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_results_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_quick_attempts_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_quick_attempts_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_quick_runs_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_quick_runs_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_quick_completion_finalize;
DROP TRIGGER IF EXISTS cleardev_complex_quick_work_item_done_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_required_check_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_permission_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_work_item_binding_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_work_item_xor;
DROP TRIGGER IF EXISTS cleardev_complex_quick_task_mapping_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_run_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_quick_run_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_run_insert_valid;
ALTER TABLE cleardev_evidence DROP COLUMN complex_quick_check_run_id;
ALTER TABLE cleardev_integration_candidates DROP COLUMN complex_quick_source_candidate_commit_id;
ALTER TABLE cleardev_integration_candidates DROP COLUMN complex_quick_result_id;
ALTER TABLE cleardev_candidate_commits DROP COLUMN complex_quick_base_commit_sha;
ALTER TABLE cleardev_candidate_commits DROP COLUMN complex_quick_task_attempt_id;
ALTER TABLE cleardev_required_checks DROP COLUMN complex_quick_check_spec_id;
ALTER TABLE cleardev_path_permission_versions DROP COLUMN complex_quick_task_id;
ALTER TABLE cleardev_work_items DROP COLUMN complex_quick_task_id;
DROP TABLE IF EXISTS cleardev_complex_quick_result_checks;
DROP TABLE IF EXISTS cleardev_complex_quick_results;
DROP TABLE IF EXISTS cleardev_complex_quick_check_runs;
DROP TABLE IF EXISTS cleardev_complex_quick_check_specs;
DROP TABLE IF EXISTS cleardev_complex_quick_task_attempts;
DROP TABLE IF EXISTS cleardev_complex_quick_task_mappings;
DROP TABLE IF EXISTS cleardev_complex_quick_agent_steps;
DROP TABLE IF EXISTS cleardev_complex_quick_role_bindings;
DROP TABLE IF EXISTS cleardev_complex_quick_runs;

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
-- +goose StatementEnd
