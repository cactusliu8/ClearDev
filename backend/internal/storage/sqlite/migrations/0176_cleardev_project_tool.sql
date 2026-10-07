-- +goose Up
-- An absent choice preserves legacy Codex behaviour. Do not backfill existing
-- projects from AO's role overrides: those were never ClearDev tool selection.
ALTER TABLE cleardev_controlled_preflights ADD COLUMN capability_evidence TEXT NOT NULL DEFAULT 'null'
    CHECK (json_valid(capability_evidence));
ALTER TABLE cleardev_product_goals ADD COLUMN requested_execution TEXT NOT NULL DEFAULT 'null'
    CHECK (json_valid(requested_execution));
CREATE INDEX idx_cleardev_preflight_project_outcome ON cleardev_controlled_preflights (ao_project_id, outcome);

-- Tool choice freezes at first controlled work. Model/effort may be corrected
-- before the first successful admission, never after a role could have started.
-- +goose StatementBegin
CREATE TRIGGER cleardev_project_tool_frozen
BEFORE UPDATE OF config ON projects
WHEN EXISTS (SELECT 1 FROM cleardev_development_projects WHERE ao_project_id = OLD.id)
 AND (
    json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.agent') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.agent')
 OR (
    (EXISTS (SELECT 1 FROM cleardev_controlled_preflights WHERE ao_project_id = OLD.id AND outcome = 'PASSED')
     OR EXISTS (SELECT 1 FROM cleardev_product_stages s JOIN cleardev_development_projects r ON r.id = s.development_requirement_id WHERE r.ao_project_id = OLD.id))
    AND (
       json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.model') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.model')
    OR json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.effort') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.effort')
    )
 )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev execution choice is frozen');
END;
-- +goose StatementEnd

-- Stage planning may use another registered repository, but never another
-- execution choice. The check and child binding share the same transaction.
-- +goose StatementBegin
CREATE TRIGGER cleardev_stage_execution_choice_guard
BEFORE UPDATE OF development_requirement_id ON cleardev_product_stages
WHEN NEW.development_requirement_id IS NOT NULL
 AND EXISTS (
    SELECT 1
    FROM cleardev_development_projects parent
    JOIN projects source ON source.id = parent.ao_project_id
    JOIN cleardev_development_projects child ON child.id = NEW.development_requirement_id
    JOIN projects target ON target.id = child.ao_project_id
    WHERE parent.id = NEW.product_id
      AND (
         json_extract(COALESCE(source.config, '{}'), '$.cleardev.agent') IS NOT json_extract(COALESCE(target.config, '{}'), '$.cleardev.agent')
      OR json_extract(COALESCE(source.config, '{}'), '$.cleardev.model') IS NOT json_extract(COALESCE(target.config, '{}'), '$.cleardev.model')
      OR json_extract(COALESCE(source.config, '{}'), '$.cleardev.effort') IS NOT json_extract(COALESCE(target.config, '{}'), '$.cleardev.effort')
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev execution project mismatch');
END;
-- +goose StatementEnd

-- +goose Down
-- Downgrading must not silently run an explicitly selected OpenCode project as
-- Codex, erase request identity, or conflate local evidence with remote facts.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_tool_downgrade_guard (ok INTEGER CHECK (ok = 1));
INSERT INTO cleardev_tool_downgrade_guard SELECT CASE WHEN
    EXISTS (SELECT 1 FROM projects WHERE json_type(COALESCE(config, '{}'), '$.cleardev') = 'object')
    OR EXISTS (SELECT 1 FROM cleardev_controlled_preflights WHERE capability_evidence <> 'null')
    OR EXISTS (SELECT 1 FROM cleardev_product_goals WHERE requested_execution <> 'null')
    OR EXISTS (SELECT 1 FROM cleardev_complex_execution_runs)
    OR EXISTS (SELECT 1 FROM cleardev_workflow_recoveries)
    THEN 0 ELSE 1 END;
DROP TABLE cleardev_tool_downgrade_guard;
DROP TRIGGER cleardev_project_tool_frozen;
DROP TRIGGER cleardev_stage_execution_choice_guard;
DROP INDEX idx_cleardev_preflight_project_outcome;
ALTER TABLE cleardev_controlled_preflights DROP COLUMN capability_evidence;
ALTER TABLE cleardev_product_goals DROP COLUMN requested_execution;
-- +goose StatementEnd
