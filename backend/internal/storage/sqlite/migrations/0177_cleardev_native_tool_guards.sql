-- Preserve every existing role/authority/candidate/budget predicate and change
-- only the eight live Codex-only role guards and reviewer source views. This
-- uses the repository's existing surgical schema-rewrite pattern (0082 etc.).
-- No session, evidence, approval or historical migration is rewritten.
-- RESET reparses the schema before any controlled work can start. Both rewrite
-- spellings are intentionally distinct so an allowed downgrade restores exact
-- original SQL, including whitespace. Up is restart-idempotent.
-- +goose NO TRANSACTION
-- +goose Up
CREATE VIEW IF NOT EXISTS cleardev_project_tool_sessions AS
SELECT bound.id AS session_id
FROM sessions bound JOIN projects p ON p.id = bound.project_id
WHERE bound.harness IN ('codex', 'opencode')
  AND bound.harness = COALESCE(json_extract(COALESCE(p.config, '{}'), '$.cleardev.agent'), 'codex')
  AND (
       json_type(COALESCE(p.config, '{}'), '$.cleardev') IS NULL
    OR json_type(COALESCE(p.config, '{}'), '$.cleardev') = 'null'
    OR (json_type(COALESCE(p.config, '{}'), '$.cleardev') = 'object'
        AND bound.model = json_extract(p.config, '$.cleardev.model'))
  );

CREATE TEMP TABLE IF NOT EXISTS cleardev_tool_guard_migration_check (ok INTEGER CHECK (ok = 1));
DELETE FROM cleardev_tool_guard_migration_check;
INSERT INTO cleardev_tool_guard_migration_check
SELECT CASE WHEN count(*) = 8 THEN 1 ELSE 0 END FROM sqlite_master
WHERE type IN ('trigger', 'view') AND name IN (
    'cleardev_standard_role_binding_insert_valid',
    'cleardev_standard_role_binding_update_valid',
    'cleardev_complex_role_binding_insert_valid',
    'cleardev_complex_role_binding_update_valid',
    'cleardev_complex_execution_role_binding_insert_valid',
    'cleardev_complex_execution_role_binding_update_valid',
    'cleardev_mail_reviewer_rework_sources',
    'cleardev_final_review_update_guard'
) AND (instr(sql, 'session.harness = ''codex''') > 0
    OR instr(sql, 'session.harness=''codex''') > 0
    OR instr(sql, 'cleardev_project_tool_sessions') > 0);
DROP TABLE cleardev_tool_guard_migration_check;

PRAGMA writable_schema = ON;
UPDATE sqlite_master SET sql = replace(replace(sql,
    'session.harness = ''codex''', 'session.id IN (SELECT session_id FROM cleardev_project_tool_sessions)'),
    'session.harness=''codex''', 'session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)')
WHERE type IN ('trigger', 'view') AND name IN (
    'cleardev_standard_role_binding_insert_valid',
    'cleardev_standard_role_binding_update_valid',
    'cleardev_complex_role_binding_insert_valid',
    'cleardev_complex_role_binding_update_valid',
    'cleardev_complex_execution_role_binding_insert_valid',
    'cleardev_complex_execution_role_binding_update_valid',
    'cleardev_mail_reviewer_rework_sources',
    'cleardev_final_review_update_guard'
);
PRAGMA writable_schema = RESET;

-- +goose Down
-- Restore old Codex-only guards only when no explicit controlled selection
-- exists. A failed guard leaves schema and evidence intact and can be retried.
CREATE TEMP TABLE IF NOT EXISTS cleardev_tool_guard_downgrade_check (ok INTEGER CHECK (ok = 1));
DELETE FROM cleardev_tool_guard_downgrade_check;
INSERT INTO cleardev_tool_guard_downgrade_check
SELECT CASE WHEN EXISTS (SELECT 1 FROM projects WHERE json_type(COALESCE(config, '{}'), '$.cleardev') = 'object')
    OR EXISTS (SELECT 1 FROM cleardev_complex_execution_runs)
    OR EXISTS (SELECT 1 FROM cleardev_workflow_recoveries)
    THEN 0 ELSE 1 END;
DROP TABLE cleardev_tool_guard_downgrade_check;

PRAGMA writable_schema = ON;
UPDATE sqlite_master SET sql = replace(replace(sql,
    'session.id IN (SELECT session_id FROM cleardev_project_tool_sessions)', 'session.harness = ''codex'''),
    'session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)', 'session.harness=''codex''')
WHERE type IN ('trigger', 'view') AND name IN (
    'cleardev_standard_role_binding_insert_valid',
    'cleardev_standard_role_binding_update_valid',
    'cleardev_complex_role_binding_insert_valid',
    'cleardev_complex_role_binding_update_valid',
    'cleardev_complex_execution_role_binding_insert_valid',
    'cleardev_complex_execution_role_binding_update_valid',
    'cleardev_mail_reviewer_rework_sources',
    'cleardev_final_review_update_guard'
);
PRAGMA writable_schema = RESET;
DROP VIEW cleardev_project_tool_sessions;
