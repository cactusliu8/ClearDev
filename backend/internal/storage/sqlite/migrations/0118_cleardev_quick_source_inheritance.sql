-- ClearDev S12A2: derive QUICK task-set versions and check commands from the
-- completed source execution. 0114 and 0117 remain immutable.
-- +goose Up
-- +goose NO TRANSACTION

-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
-- +goose StatementEnd

-- The original table fixed every QUICK run to task-set 1 -> 2. Rebuild it so
-- the successor version is derived from the completed source run.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_quick_runs_cdc_update;
DROP TRIGGER IF EXISTS cleardev_complex_quick_runs_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_complex_quick_run_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_quick_run_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_quick_run_insert_valid;

CREATE TABLE cleardev_complex_quick_runs_rebuilt (
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
    expected_task_set_version  INTEGER NOT NULL CHECK (expected_task_set_version > 0),
    accepted_task_set_version  INTEGER NOT NULL CHECK (accepted_task_set_version = expected_task_set_version + 1),
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
INSERT INTO cleardev_complex_quick_runs_rebuilt (
    id, development_project_id, requirement_version_id, requirement_sha256,
    source_execution_run_id, plan_id, plan_sha256, source_task_key, integration_base_sha,
    steward_role_binding_id, mode, selection_reason_code, expected_task_set_version,
    accepted_task_set_version, execution_package_json, execution_package_sha256,
    status, reason_code, requested_at, accepted_at, settled_at
)
SELECT
    id, development_project_id, requirement_version_id, requirement_sha256,
    source_execution_run_id, plan_id, plan_sha256, source_task_key, integration_base_sha,
    steward_role_binding_id, mode, selection_reason_code, expected_task_set_version,
    accepted_task_set_version, execution_package_json, execution_package_sha256,
    status, reason_code, requested_at, accepted_at, settled_at
FROM cleardev_complex_quick_runs;
PRAGMA legacy_alter_table = ON;
ALTER TABLE cleardev_complex_quick_runs RENAME TO cleardev_complex_quick_runs_obsolete;
ALTER TABLE cleardev_complex_quick_runs_rebuilt RENAME TO cleardev_complex_quick_runs;
DROP TABLE cleardev_complex_quick_runs_obsolete;
PRAGMA legacy_alter_table = OFF;
CREATE INDEX idx_cleardev_complex_quick_runs_requirement
    ON cleardev_complex_quick_runs (development_project_id, requirement_version_id, requested_at);
CREATE UNIQUE INDEX idx_cleardev_complex_quick_active_requirement
    ON cleardev_complex_quick_runs (requirement_version_id)
    WHERE status IN ('PENDING', 'ACCEPTED');
-- +goose StatementEnd

-- The original check table allowed only four mail-list commands. The rebuilt
-- table accepts only facts that a trigger can match exactly to the completed
-- source task or its source integration.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_quick_check_spec_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_quick_check_spec_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_quick_check_spec_insert_valid;

CREATE TABLE cleardev_complex_quick_check_specs_rebuilt (
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
        OR (check_kind = 'REQUIRED_CHECK' AND task_mapping_id IS NOT NULL AND check_name <> '' AND json_array_length(argv_json) > 0 AND timeout_seconds > 0)
        OR (check_kind = 'INTEGRATION' AND task_mapping_id IS NULL AND check_name <> '' AND json_array_length(argv_json) > 0 AND timeout_seconds > 0)),
    CHECK ((check_kind = 'REQUIRED_CHECK' AND required_check_id IS NOT NULL) OR (check_kind <> 'REQUIRED_CHECK' AND required_check_id IS NULL))
);
INSERT INTO cleardev_complex_quick_check_specs_rebuilt (
    id, quick_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
)
SELECT
    id, quick_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
FROM cleardev_complex_quick_check_specs;
PRAGMA legacy_alter_table = ON;
ALTER TABLE cleardev_complex_quick_check_specs RENAME TO cleardev_complex_quick_check_specs_obsolete;
ALTER TABLE cleardev_complex_quick_check_specs_rebuilt RENAME TO cleardev_complex_quick_check_specs;
DROP TABLE cleardev_complex_quick_check_specs_obsolete;
PRAGMA legacy_alter_table = OFF;
CREATE UNIQUE INDEX idx_cleardev_complex_quick_task_check_spec
    ON cleardev_complex_quick_check_specs (task_mapping_id, check_kind, check_name)
    WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_quick_integration_check_spec
    ON cleardev_complex_quick_check_specs (quick_run_id, check_kind, check_name)
    WHERE task_mapping_id IS NULL;
-- +goose StatementEnd

-- A QUICK request binds the current approved, non-superseded version to one
-- completed source execution and its exact integration commit.
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
      AND version.superseded_by_id IS NULL
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND source.development_project_id = project.id
      AND source.requirement_version_id = version.id
      AND source.requirement_sha256 = NEW.requirement_sha256
      AND source.plan_id = NEW.plan_id
      AND source.plan_sha256 = NEW.plan_sha256
      AND source.status = 'COMPLETED'
      AND source.accepted_task_set_version = NEW.expected_task_set_version
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.requirement_sha256 = NEW.requirement_sha256
      AND plan.plan_sha256 = NEW.plan_sha256
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
      AND result.completion_status = 'COMPLETED'
      AND integration.complex_execution_result_id = result.id
      AND integration.requirement_version_id = version.id
      AND integration.commit_sha = NEW.integration_base_sha
      AND integration.task_set_version = NEW.expected_task_set_version
 )
 OR EXISTS (
    SELECT 1 FROM cleardev_direction_stop_gates AS gate
    WHERE gate.requirement_version_id = NEW.requirement_version_id AND gate.status = 'ACTIVE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution requires an exact completed current integration');
END;
-- +goose StatementEnd

-- +goose StatementBegin
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
 OR (OLD.status = 'PENDING' AND NEW.status = 'ACCEPTED' AND NOT EXISTS (
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
      AND version.superseded_by_id IS NULL
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND source.development_project_id = project.id
      AND source.status = 'COMPLETED'
      AND source.requirement_version_id = version.id
      AND source.requirement_sha256 = NEW.requirement_sha256
      AND source.plan_id = NEW.plan_id
      AND source.plan_sha256 = NEW.plan_sha256
      AND source.accepted_task_set_version = NEW.expected_task_set_version
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.requirement_sha256 = NEW.requirement_sha256
      AND plan.plan_sha256 = NEW.plan_sha256
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
      AND result.completion_status = 'COMPLETED'
      AND integration.complex_execution_result_id = result.id
      AND integration.requirement_version_id = version.id
      AND integration.commit_sha = NEW.integration_base_sha
      AND integration.task_set_version = NEW.expected_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = version.id AND gate.status = 'ACTIVE')
 ))
 OR (OLD.status = 'ACCEPTED' AND NEW.status = 'COMPLETED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_contract_versions AS version
    WHERE version.id = NEW.requirement_version_id
      AND version.development_project_id = NEW.development_project_id
      AND version.state = 'APPROVED' AND version.superseded_by_id IS NULL
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = version.id AND gate.status = 'ACTIVE')
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution run is immutable or no longer current');
END;

CREATE TRIGGER cleardev_complex_quick_run_append_only_delete
BEFORE DELETE ON cleardev_complex_quick_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution runs are append-only');
END;
-- +goose StatementEnd

-- The task packet must carry the successor task-set version recorded by the
-- QUICK run and still point at the completed source task.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_quick_task_mapping_insert_valid;
CREATE TRIGGER cleardev_complex_quick_task_mapping_insert_valid
BEFORE INSERT ON cleardev_complex_quick_task_mappings
WHEN NEW.ordinal <> 0
 OR ifnull(json_extract(NEW.task_packet_json, '$.taskKey'), '') <> NEW.plan_task_key
 OR ifnull(json_extract(NEW.task_packet_json, '$.mode'), '') <> 'QUICK'
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_quick_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    JOIN cleardev_complex_execution_task_mappings AS source ON source.execution_run_id = run.source_execution_run_id
    JOIN cleardev_work_items AS work_item ON work_item.id = source.work_item_id
    WHERE run.id = NEW.quick_run_id
      AND run.status = 'ACCEPTED'
      AND run.source_task_key = NEW.plan_task_key
      AND source.plan_task_key = NEW.plan_task_key
      AND work_item.state = 'DONE'
      AND version.state = 'APPROVED' AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.expected_task_set_version
      AND CAST(ifnull(json_extract(NEW.task_packet_json, '$.taskSetVersion'), 0) AS INTEGER) = run.accepted_task_set_version
      AND ifnull(json_extract(NEW.task_packet_json, '$.requirementVersionId'), '') = run.requirement_version_id
      AND ifnull(json_extract(NEW.task_packet_json, '$.requirementSha256'), '') = run.requirement_sha256
      AND ifnull(json_extract(NEW.task_packet_json, '$.planId'), '') = run.plan_id
      AND ifnull(json_extract(NEW.task_packet_json, '$.planSha256'), '') = run.plan_sha256
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution task must match the accepted source task and task set');
END;
-- +goose StatementEnd

-- Scope remains derived from the QUICK packet. Required and integration
-- commands must match one source check in all four frozen fields.
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_quick_check_spec_insert_valid
BEFORE INSERT ON cleardev_complex_quick_check_specs
WHEN (NEW.check_kind = 'SCOPE' AND NOT EXISTS (
        SELECT 1 FROM cleardev_complex_quick_task_mappings AS task
        WHERE task.id = NEW.task_mapping_id AND task.quick_run_id = NEW.quick_run_id
     ))
 OR (NEW.check_kind = 'REQUIRED_CHECK' AND NOT EXISTS (
        SELECT 1
        FROM cleardev_complex_quick_runs AS run
        JOIN cleardev_complex_quick_task_mappings AS quick_task ON quick_task.id = NEW.task_mapping_id AND quick_task.quick_run_id = run.id
        JOIN cleardev_complex_execution_task_mappings AS source_task
          ON source_task.execution_run_id = run.source_execution_run_id
         AND source_task.plan_task_key = quick_task.plan_task_key
        JOIN cleardev_complex_execution_check_specs AS source_spec
          ON source_spec.execution_run_id = run.source_execution_run_id
         AND source_spec.task_mapping_id = source_task.id
         AND source_spec.check_kind = 'REQUIRED_CHECK'
        WHERE run.id = NEW.quick_run_id
          AND run.status = 'ACCEPTED'
          AND quick_task.plan_task_key = run.source_task_key
          AND source_spec.check_name = NEW.check_name
          AND source_spec.check_spec_sha256 = NEW.check_spec_sha256
          AND json(source_spec.argv_json) = json(NEW.argv_json)
          AND source_spec.timeout_seconds = NEW.timeout_seconds
     ))
 OR (NEW.check_kind = 'INTEGRATION' AND NOT EXISTS (
        SELECT 1
        FROM cleardev_complex_quick_runs AS run
        JOIN cleardev_complex_execution_check_specs AS source_spec
          ON source_spec.execution_run_id = run.source_execution_run_id
         AND source_spec.check_kind = 'INTEGRATION'
         AND source_spec.task_mapping_id IS NULL
        WHERE run.id = NEW.quick_run_id
          AND run.status = 'ACCEPTED'
          AND NEW.task_mapping_id IS NULL
          AND source_spec.check_name = NEW.check_name
          AND source_spec.check_spec_sha256 = NEW.check_spec_sha256
          AND json(source_spec.argv_json) = json(NEW.argv_json)
          AND source_spec.timeout_seconds = NEW.timeout_seconds
     ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution check must exactly inherit its completed source check');
END;

CREATE TRIGGER cleardev_complex_quick_check_spec_append_only_update
BEFORE UPDATE ON cleardev_complex_quick_check_specs
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution check specs are append-only');
END;

CREATE TRIGGER cleardev_complex_quick_check_spec_append_only_delete
BEFORE DELETE ON cleardev_complex_quick_check_specs
BEGIN
    SELECT RAISE(ABORT, 'cleardev quick execution check specs are append-only');
END;
-- +goose StatementEnd

-- Restore CDC on the rebuilt run table.
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
-- +goose StatementEnd

-- +goose StatementBegin
PRAGMA foreign_keys = ON;
PRAGMA foreign_key_check;
-- +goose StatementEnd

-- +goose Down
-- The widened constraints may already contain a v1 Campaign and demo checks.
-- Those durable facts cannot fit the old 0114 tables. Keep the widened schema
-- instead of silently deleting execution history during downgrade.
-- +goose StatementBegin
SELECT 1;
-- +goose StatementEnd
