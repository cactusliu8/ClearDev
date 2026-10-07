-- Project checks retain the existing runner and evidence tables. Only an exact
-- admitted PROJECT_EXECUTION_V1 run may use its own argv and timeout; historical
-- mail/benchmark runs retain the original frozen catalogue and 60-second limit.
-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
BEGIN IMMEDIATE;
CREATE TEMP TABLE cleardev_project_checks_fk_before AS SELECT * FROM pragma_foreign_key_check;
DROP TRIGGER cleardev_complex_execution_check_spec_append_only_delete;
DROP TRIGGER cleardev_complex_execution_check_spec_append_only_update;
DROP TRIGGER cleardev_complex_execution_check_spec_insert_valid;
CREATE TABLE cleardev_complex_execution_check_specs_0152 (
 id TEXT PRIMARY KEY,
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 task_mapping_id TEXT REFERENCES cleardev_complex_execution_task_mappings(id),
 required_check_id TEXT REFERENCES cleardev_required_checks(id) DEFERRABLE INITIALLY DEFERRED,
 check_kind TEXT NOT NULL CHECK(check_kind IN ('SCOPE','REQUIRED_CHECK','INTEGRATION')),
 check_name TEXT NOT NULL DEFAULT '',
 check_spec_sha256 TEXT NOT NULL CHECK(length(check_spec_sha256)=64 AND check_spec_sha256 NOT GLOB '*[^0-9a-f]*'),
 argv_json TEXT NOT NULL CHECK(json_valid(argv_json) AND json_type(argv_json)='array'),
 timeout_seconds INTEGER NOT NULL CHECK(timeout_seconds BETWEEN 0 AND 3600),
 created_at TIMESTAMP NOT NULL,
 CHECK((check_kind='SCOPE' AND task_mapping_id IS NOT NULL AND check_name='' AND json(argv_json)=json('[]') AND timeout_seconds=0)
    OR (check_kind='REQUIRED_CHECK' AND task_mapping_id IS NOT NULL AND check_name<>'' AND json_array_length(argv_json)>0 AND timeout_seconds BETWEEN 1 AND 3600)
    OR (check_kind='INTEGRATION' AND task_mapping_id IS NULL AND check_name<>'' AND json_array_length(argv_json)>0 AND timeout_seconds BETWEEN 1 AND 3600)),
 CHECK((check_kind='REQUIRED_CHECK' AND required_check_id IS NOT NULL) OR (check_kind<>'REQUIRED_CHECK' AND required_check_id IS NULL))
);
INSERT INTO cleardev_complex_execution_check_specs_0152 SELECT * FROM cleardev_complex_execution_check_specs;
PRAGMA legacy_alter_table=ON;
ALTER TABLE cleardev_complex_execution_check_specs RENAME TO cleardev_complex_execution_check_specs_old_0152;
ALTER TABLE cleardev_complex_execution_check_specs_0152 RENAME TO cleardev_complex_execution_check_specs;
DROP TABLE cleardev_complex_execution_check_specs_old_0152;
PRAGMA legacy_alter_table=OFF;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_check_spec
 ON cleardev_complex_execution_check_specs(task_mapping_id,check_kind,check_name) WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_integration_check_spec
 ON cleardev_complex_execution_check_specs(execution_run_id,check_kind,check_name) WHERE task_mapping_id IS NULL;
CREATE TRIGGER cleardev_complex_execution_check_spec_insert_valid BEFORE INSERT ON cleardev_complex_execution_check_specs
WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_runs run
 LEFT JOIN cleardev_complex_execution_task_mappings task ON task.id=NEW.task_mapping_id
 WHERE run.id=NEW.execution_run_id AND
 ((NEW.check_kind='INTEGRATION' AND NEW.task_mapping_id IS NULL) OR (NEW.check_kind<>'INTEGRATION' AND task.execution_run_id=run.id))
) OR (
 NEW.check_kind<>'SCOPE' AND NOT EXISTS(
 SELECT 1 FROM cleardev_project_execution_admissions admission
 JOIN cleardev_complex_execution_runs run ON run.id=admission.execution_run_id
 JOIN json_each(admission.contract_json,'$.basis.checks') approved
 WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED'
   AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
   AND json_extract(approved.value,'$.id') IS NEW.check_name
   AND json_extract(approved.value,'$.timeoutSeconds') IS NEW.timeout_seconds
   AND json(json_extract(approved.value,'$.argv')) IS json(NEW.argv_json)
   AND (NEW.check_kind='INTEGRATION' OR EXISTS(
     SELECT 1 FROM cleardev_complex_execution_task_mappings task
     JOIN json_each(task.task_packet_json,'$.requiredChecks') task_check
     WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=run.id
       AND json_extract(task.task_packet_json,'$.schemaVersion') IS 4
       AND json_extract(task_check.value,'$.id') IS NEW.check_name
       AND json_extract(task_check.value,'$.timeoutSeconds') IS NEW.timeout_seconds
       AND json(json_extract(task_check.value,'$.argv')) IS json(NEW.argv_json)))
 ) AND NOT (
 NOT EXISTS(SELECT 1 FROM cleardev_project_execution_admissions WHERE execution_run_id=NEW.execution_run_id)
 AND NEW.timeout_seconds=60 AND (
 (NEW.check_name='email-unit' AND json(NEW.argv_json)=json('["node","--test","test/email.test.js"]') AND NEW.check_spec_sha256='feafc89ad5e5314058f839a1f280aa8cff20503583f40379c01f4d97dd031466') OR
 (NEW.check_name='deduplicate-unit' AND json(NEW.argv_json)=json('["node","--test","test/deduplicate.test.js"]') AND NEW.check_spec_sha256='04f123a6661183016601642f5aea4db485ef025a371e884a52b6ed4a11cee37e') OR
 (NEW.check_name='summary-unit' AND json(NEW.argv_json)=json('["node","--test","test/summary.test.js"]') AND NEW.check_spec_sha256='7de135c41f08e1eb60ace8d93642a74645118f3dfb54d515e0099a39059a372f') OR
 (NEW.check_name='all-tests' AND json(NEW.argv_json)=json('["node","--test"]') AND NEW.check_spec_sha256='82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586') OR
 (NEW.check_name IN ('demo-backend','CHK-LIB-BACKEND','CHK-INV-BACKEND','CHK-TKT-BACKEND','CHK-DEV-BACKEND') AND json(NEW.argv_json)=json('["npm","run","test:backend"]') AND NEW.check_spec_sha256='002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22') OR
 (NEW.check_name IN ('demo-database','CHK-LIB-DATABASE','CHK-INV-DATABASE','CHK-TKT-DATABASE','CHK-DEV-DATABASE') AND json(NEW.argv_json)=json('["npm","run","test:database"]') AND NEW.check_spec_sha256='7545c63f42eca5bb8b9db44014a4850045b83c27375beaeb6fc5424cd2d3f787') OR
 (NEW.check_name IN ('demo-api','CHK-LIB-API','CHK-INV-API','CHK-TKT-API','CHK-DEV-API') AND json(NEW.argv_json)=json('["npm","run","test:api"]') AND NEW.check_spec_sha256='c6c74e01bd47039cc69b2f71fa8948be8bcb1af25fcd4b2d467f9bd3d81b6be7') OR
 (NEW.check_name='demo-frontend' AND json(NEW.argv_json)=json('["npm","run","test:frontend"]') AND NEW.check_spec_sha256='153f7220bfe994393806417c9b0259ecf1148c573f963a3e39ccfe3abc5196f1') OR
 (NEW.check_name IN ('demo-integration','CHK-LIB-ALL','CHK-INV-ALL','CHK-TKT-ALL','CHK-DEV-ALL') AND json(NEW.argv_json)=json('["npm","test"]') AND NEW.check_spec_sha256='527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4') OR
 (NEW.check_name IN ('CHK-LIB-TYPE','CHK-INV-TYPE','CHK-TKT-TYPE','CHK-DEV-TYPE') AND json(NEW.argv_json)=json('["npm","run","typecheck"]') AND NEW.check_spec_sha256='e7f1a6143601f8eaeebf1837c7854c14f118864f0568ad75956bdba45af9e92a')
 ))
)
BEGIN SELECT RAISE(ABORT,'check specification must match its immutable admitted project or historical catalogue'); END;
CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_update BEFORE UPDATE ON cleardev_complex_execution_check_specs
BEGIN SELECT RAISE(ABORT,'cleardev complex execution check specs are append-only'); END;
CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_delete BEFORE DELETE ON cleardev_complex_execution_check_specs
BEGIN SELECT RAISE(ABORT,'cleardev complex execution check specs are append-only'); END;
CREATE TEMP TABLE cleardev_project_checks_fk_guard(ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_checks_fk_guard SELECT CASE WHEN EXISTS(
 SELECT * FROM pragma_foreign_key_check EXCEPT SELECT * FROM cleardev_project_checks_fk_before
) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_checks_fk_guard;
DROP TABLE cleardev_project_checks_fk_before;
COMMIT;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- The old binary cannot interpret a project check. Refuse any downgrade with
-- execution evidence instead of deleting or weakening it. An unused database
-- can return to the exact historical table and append-only guards.
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_checks_down_guard(ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_checks_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_checks_down_guard;
PRAGMA foreign_keys=OFF;
BEGIN IMMEDIATE;
DROP TRIGGER cleardev_complex_execution_check_spec_append_only_delete;
DROP TRIGGER cleardev_complex_execution_check_spec_append_only_update;
DROP TRIGGER cleardev_complex_execution_check_spec_insert_valid;
CREATE TABLE cleardev_complex_execution_check_specs_0152 (
 id TEXT PRIMARY KEY,
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 task_mapping_id TEXT REFERENCES cleardev_complex_execution_task_mappings(id),
 required_check_id TEXT REFERENCES cleardev_required_checks(id) DEFERRABLE INITIALLY DEFERRED,
 check_kind TEXT NOT NULL CHECK(check_kind IN ('SCOPE','REQUIRED_CHECK','INTEGRATION')),
 check_name TEXT NOT NULL DEFAULT '',
 check_spec_sha256 TEXT NOT NULL CHECK(length(check_spec_sha256)=64 AND check_spec_sha256 NOT GLOB '*[^0-9a-f]*'),
 argv_json TEXT NOT NULL CHECK(json_valid(argv_json) AND json_type(argv_json)='array'),
 timeout_seconds INTEGER NOT NULL CHECK(timeout_seconds>=0),
 created_at TIMESTAMP NOT NULL,
 CHECK((check_kind='SCOPE' AND task_mapping_id IS NOT NULL AND check_name='' AND json(argv_json)=json('[]') AND timeout_seconds=0)
    OR (check_kind='REQUIRED_CHECK' AND task_mapping_id IS NOT NULL AND check_name<>'' AND json_array_length(argv_json)>0 AND timeout_seconds=60)
    OR (check_kind='INTEGRATION' AND task_mapping_id IS NULL AND check_name<>'' AND json_array_length(argv_json)>0 AND timeout_seconds=60)),
 CHECK(check_kind='SCOPE' OR
 (check_name='email-unit' AND json(argv_json)=json('["node","--test","test/email.test.js"]') AND check_spec_sha256='feafc89ad5e5314058f839a1f280aa8cff20503583f40379c01f4d97dd031466') OR
 (check_name='deduplicate-unit' AND json(argv_json)=json('["node","--test","test/deduplicate.test.js"]') AND check_spec_sha256='04f123a6661183016601642f5aea4db485ef025a371e884a52b6ed4a11cee37e') OR
 (check_name='summary-unit' AND json(argv_json)=json('["node","--test","test/summary.test.js"]') AND check_spec_sha256='7de135c41f08e1eb60ace8d93642a74645118f3dfb54d515e0099a39059a372f') OR
 (check_name='all-tests' AND json(argv_json)=json('["node","--test"]') AND check_spec_sha256='82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586') OR
 (check_name IN ('demo-backend','CHK-LIB-BACKEND','CHK-INV-BACKEND','CHK-TKT-BACKEND','CHK-DEV-BACKEND') AND json(argv_json)=json('["npm","run","test:backend"]') AND check_spec_sha256='002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22') OR
 (check_name IN ('demo-database','CHK-LIB-DATABASE','CHK-INV-DATABASE','CHK-TKT-DATABASE','CHK-DEV-DATABASE') AND json(argv_json)=json('["npm","run","test:database"]') AND check_spec_sha256='7545c63f42eca5bb8b9db44014a4850045b83c27375beaeb6fc5424cd2d3f787') OR
 (check_name IN ('demo-api','CHK-LIB-API','CHK-INV-API','CHK-TKT-API','CHK-DEV-API') AND json(argv_json)=json('["npm","run","test:api"]') AND check_spec_sha256='c6c74e01bd47039cc69b2f71fa8948be8bcb1af25fcd4b2d467f9bd3d81b6be7') OR
 (check_name='demo-frontend' AND json(argv_json)=json('["npm","run","test:frontend"]') AND check_spec_sha256='153f7220bfe994393806417c9b0259ecf1148c573f963a3e39ccfe3abc5196f1') OR
 (check_name IN ('demo-integration','CHK-LIB-ALL','CHK-INV-ALL','CHK-TKT-ALL','CHK-DEV-ALL') AND json(argv_json)=json('["npm","test"]') AND check_spec_sha256='527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4') OR
 (check_name IN ('CHK-LIB-TYPE','CHK-INV-TYPE','CHK-TKT-TYPE','CHK-DEV-TYPE') AND json(argv_json)=json('["npm","run","typecheck"]') AND check_spec_sha256='e7f1a6143601f8eaeebf1837c7854c14f118864f0568ad75956bdba45af9e92a')),
 CHECK((check_kind='REQUIRED_CHECK' AND required_check_id IS NOT NULL) OR (check_kind<>'REQUIRED_CHECK' AND required_check_id IS NULL))
);
PRAGMA legacy_alter_table=ON;
ALTER TABLE cleardev_complex_execution_check_specs RENAME TO cleardev_complex_execution_check_specs_old_0152;
ALTER TABLE cleardev_complex_execution_check_specs_0152 RENAME TO cleardev_complex_execution_check_specs;
DROP TABLE cleardev_complex_execution_check_specs_old_0152;
PRAGMA legacy_alter_table=OFF;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_check_spec
 ON cleardev_complex_execution_check_specs(task_mapping_id,check_kind,check_name) WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_integration_check_spec
 ON cleardev_complex_execution_check_specs(execution_run_id,check_kind,check_name) WHERE task_mapping_id IS NULL;
CREATE TRIGGER cleardev_complex_execution_check_spec_insert_valid BEFORE INSERT ON cleardev_complex_execution_check_specs
WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_runs run
 LEFT JOIN cleardev_complex_execution_task_mappings task ON task.id=NEW.task_mapping_id
 WHERE run.id=NEW.execution_run_id AND
 ((NEW.check_kind='INTEGRATION' AND NEW.task_mapping_id IS NULL) OR (NEW.check_kind<>'INTEGRATION' AND task.execution_run_id=run.id))
)
BEGIN SELECT RAISE(ABORT,'cleardev complex execution check spec must bind its execution task'); END;
CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_update BEFORE UPDATE ON cleardev_complex_execution_check_specs
BEGIN SELECT RAISE(ABORT,'cleardev complex execution check specs are append-only'); END;
CREATE TRIGGER cleardev_complex_execution_check_spec_append_only_delete BEFORE DELETE ON cleardev_complex_execution_check_specs
BEGIN SELECT RAISE(ABORT,'cleardev complex execution check specs are append-only'); END;
COMMIT;
PRAGMA foreign_keys=ON;
-- +goose StatementEnd
