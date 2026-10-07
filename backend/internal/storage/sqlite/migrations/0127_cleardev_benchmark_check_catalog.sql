-- ClearDev S12B: allow the four frozen benchmark check prefixes in durable
-- complex-execution check specs. Planning and store policy validation still
-- decide whether a requirement may use these IDs; this schema only preserves
-- an already validated fixed argv/check digest.
-- +goose NO TRANSACTION
-- +goose Up

-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
-- +goose StatementEnd

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
            AND check_spec_sha256 = '527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4') OR
        (check_name IN ('CHK-LIB-TYPE','CHK-INV-TYPE','CHK-TKT-TYPE','CHK-DEV-TYPE')
            AND json(argv_json) = json('["npm","run","typecheck"]')
            AND check_spec_sha256 = 'e7f1a6143601f8eaeebf1837c7854c14f118864f0568ad75956bdba45af9e92a') OR
        (check_name IN ('CHK-LIB-BACKEND','CHK-INV-BACKEND','CHK-TKT-BACKEND','CHK-DEV-BACKEND')
            AND json(argv_json) = json('["npm","run","test:backend"]')
            AND check_spec_sha256 = '002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22') OR
        (check_name IN ('CHK-LIB-DATABASE','CHK-INV-DATABASE','CHK-TKT-DATABASE','CHK-DEV-DATABASE')
            AND json(argv_json) = json('["npm","run","test:database"]')
            AND check_spec_sha256 = '7545c63f42eca5bb8b9db44014a4850045b83c27375beaeb6fc5424cd2d3f787') OR
        (check_name IN ('CHK-LIB-API','CHK-INV-API','CHK-TKT-API','CHK-DEV-API')
            AND json(argv_json) = json('["npm","run","test:api"]')
            AND check_spec_sha256 = 'c6c74e01bd47039cc69b2f71fa8948be8bcb1af25fcd4b2d467f9bd3d81b6be7') OR
        (check_name IN ('CHK-LIB-ALL','CHK-INV-ALL','CHK-TKT-ALL','CHK-DEV-ALL')
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
PRAGMA foreign_keys = ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA foreign_keys = OFF;
-- +goose StatementEnd

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
FROM cleardev_complex_execution_check_specs
WHERE check_name NOT GLOB 'CHK-*';
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
PRAGMA foreign_keys = ON;
-- +goose StatementEnd
