-- ClearDev S11: expand frozen check catalogs and accept any current confirmed
-- requirement version for STANDARD/PARALLEL execution. 0106-0116 stay unchanged.
-- QUICK remains on 0114. This rebuilds only the two check fact tables and the
-- six named lifecycle triggers. It does not rebuild execution runs or add
-- business-state tables.
-- +goose Up
-- +goose NO TRANSACTION

-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
-- +goose StatementEnd

-- Rebuild check-spec constraints so demo checks can be stored. Existing rows
-- and the original four identifiers stay valid. Only the three check-spec
-- triggers on this table are dropped, because SQLite fires BEFORE DELETE
-- during DROP TABLE. Other tables' triggers stay in place: the old table is
-- renamed with legacy_alter_table so SQLite does not rewrite or recompile them.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_insert_valid;
CREATE TABLE cleardev_complex_execution_check_specs_rebuilt (
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
            AND check_spec_sha256 = '82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586') OR
        (check_name = 'demo-backend'
            AND json(argv_json) = json('["npm","run","test:backend"]')
            AND check_spec_sha256 = '002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22') OR
        (check_name = 'demo-database'
            AND json(argv_json) = json('["npm","run","test:database"]')
            AND check_spec_sha256 = '7545c63f42eca5bb8b9db44014a4850045b83c27375beaeb6fc5424cd2d3f787') OR
        (check_name = 'demo-api'
            AND json(argv_json) = json('["npm","run","test:api"]')
            AND check_spec_sha256 = 'c6c74e01bd47039cc69b2f71fa8948be8bcb1af25fcd4b2d467f9bd3d81b6be7') OR
        (check_name = 'demo-frontend'
            AND json(argv_json) = json('["npm","run","test:frontend"]')
            AND check_spec_sha256 = '153f7220bfe994393806417c9b0259ecf1148c573f963a3e39ccfe3abc5196f1') OR
        (check_name = 'demo-integration'
            AND json(argv_json) = json('["npm","test"]')
            AND check_spec_sha256 = '527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4')),
    CHECK ((check_kind = 'REQUIRED_CHECK' AND required_check_id IS NOT NULL) OR (check_kind <> 'REQUIRED_CHECK' AND required_check_id IS NULL))
);
INSERT INTO cleardev_complex_execution_check_specs_rebuilt (
    id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
)
SELECT
    id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
FROM cleardev_complex_execution_check_specs;
PRAGMA legacy_alter_table = ON;
ALTER TABLE cleardev_complex_execution_check_specs RENAME TO cleardev_complex_execution_check_specs_obsolete;
ALTER TABLE cleardev_complex_execution_check_specs_rebuilt RENAME TO cleardev_complex_execution_check_specs;
DROP TABLE cleardev_complex_execution_check_specs_obsolete;
PRAGMA legacy_alter_table = OFF;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_check_spec
    ON cleardev_complex_execution_check_specs (task_mapping_id, check_kind, check_name)
    WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_integration_check_spec
    ON cleardev_complex_execution_check_specs (execution_run_id, check_kind, check_name)
    WHERE task_mapping_id IS NULL;
-- +goose StatementEnd

-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_update
BEFORE UPDATE ON cleardev_complex_execution_check_specs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check specs are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_check_specs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check specs are append-only');
END;
-- +goose StatementEnd

-- Rebuild specialist-check constraints so sqlite-migration-specialist can be stored.
-- +goose StatementBegin
CREATE TABLE cleardev_complex_exception_specialist_checks_rebuilt (
    id                         TEXT PRIMARY KEY,
    execution_run_id           TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    complex_execution_task_id  TEXT NOT NULL REFERENCES cleardev_complex_execution_task_mappings(id),
    specialist_result_id       TEXT NOT NULL REFERENCES cleardev_complex_exception_specialist_results(id),
    check_id                   TEXT NOT NULL,
    argv_json                  TEXT NOT NULL CHECK (json_valid(argv_json) AND json_type(argv_json) = 'array'),
    container_image_id         TEXT,
    status                     TEXT NOT NULL CHECK (status IN ('PENDING', 'STARTED', 'SETTLED', 'FAILED')),
    result                     TEXT CHECK (result IN ('PASS', 'FAIL')),
    reason_code                TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMP NOT NULL,
    settled_at                 TIMESTAMP,
    UNIQUE (specialist_result_id, check_id),
    CHECK (
        (check_id = 'package-json-specialist'
            AND json(argv_json) = json('["node","-e","const fs=require(\"fs\");JSON.parse(fs.readFileSync(\"package.json\",\"utf8\"));"]'))
        OR (check_id = 'sqlite-migration-specialist'
            AND json(argv_json) = json('["npm","run","check:migrations"]'))
    )
);
INSERT INTO cleardev_complex_exception_specialist_checks_rebuilt (
    id, execution_run_id, complex_execution_task_id, specialist_result_id, check_id,
    argv_json, container_image_id, status, result, reason_code, created_at, settled_at
)
SELECT
    id, execution_run_id, complex_execution_task_id, specialist_result_id, check_id,
    argv_json, container_image_id, status, result, reason_code, created_at, settled_at
FROM cleardev_complex_exception_specialist_checks;
DROP TABLE cleardev_complex_exception_specialist_checks;
ALTER TABLE cleardev_complex_exception_specialist_checks_rebuilt RENAME TO cleardev_complex_exception_specialist_checks;
-- +goose StatementEnd

-- Lifecycle triggers: bind the current confirmed version by id, hash, project,
-- approval, and non-supersession. Do not require version number 2. Read the
-- run's bound task-set version instead of repeating accepted_task_set_version = 1.
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
      AND version.superseded_by_id IS NULL
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

-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_insert_valid;
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
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_update_valid;
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
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND run.status = 'ACCEPTED'
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
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
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
                      WHERE task.execution_run_id = run.id AND work_item.state <> 'DONE')
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result is immutable or cannot complete');
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
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_result_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_execution_check_spec_insert_valid;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cleardev_complex_execution_check_specs_rebuilt (
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
INSERT INTO cleardev_complex_execution_check_specs_rebuilt (
    id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
)
SELECT
    id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
    check_spec_sha256, argv_json, timeout_seconds, created_at
FROM cleardev_complex_execution_check_specs
WHERE check_name IN ('', 'email-unit', 'deduplicate-unit', 'summary-unit', 'all-tests');
PRAGMA legacy_alter_table = ON;
ALTER TABLE cleardev_complex_execution_check_specs RENAME TO cleardev_complex_execution_check_specs_obsolete;
ALTER TABLE cleardev_complex_execution_check_specs_rebuilt RENAME TO cleardev_complex_execution_check_specs;
DROP TABLE cleardev_complex_execution_check_specs_obsolete;
PRAGMA legacy_alter_table = OFF;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_check_spec
    ON cleardev_complex_execution_check_specs (task_mapping_id, check_kind, check_name)
    WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_integration_check_spec
    ON cleardev_complex_execution_check_specs (execution_run_id, check_kind, check_name)
    WHERE task_mapping_id IS NULL;
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cleardev_complex_exception_specialist_checks_rebuilt (
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
INSERT INTO cleardev_complex_exception_specialist_checks_rebuilt (
    id, execution_run_id, complex_execution_task_id, specialist_result_id, check_id,
    argv_json, container_image_id, status, result, reason_code, created_at, settled_at
)
SELECT
    id, execution_run_id, complex_execution_task_id, specialist_result_id, check_id,
    argv_json, container_image_id, status, result, reason_code, created_at, settled_at
FROM cleardev_complex_exception_specialist_checks
WHERE check_id = 'package-json-specialist';
DROP TABLE cleardev_complex_exception_specialist_checks;
ALTER TABLE cleardev_complex_exception_specialist_checks_rebuilt RENAME TO cleardev_complex_exception_specialist_checks;
-- +goose StatementEnd

-- Restore the 0115 run/result triggers, including version.version = 2.
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
-- +goose StatementEnd

-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose StatementBegin
PRAGMA foreign_keys = ON;
-- +goose StatementEnd
