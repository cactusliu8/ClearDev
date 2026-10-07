-- ClearDev S12A6: append-only controlled session preflight records.
-- 0106-0119 remain immutable. These rows are coordinator facts, not Agent results.
-- +goose Up

-- +goose StatementBegin
CREATE TABLE cleardev_controlled_preflights (
    id                         TEXT PRIMARY KEY,
    development_project_id     TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    role_binding_id            TEXT NOT NULL CHECK (length(trim(role_binding_id)) > 0),
    ao_project_id              TEXT NOT NULL CHECK (length(trim(ao_project_id)) > 0),
    requested_model            TEXT NOT NULL DEFAULT '',
    resolved_model             TEXT NOT NULL DEFAULT '',
    provider                   TEXT NOT NULL CHECK (length(trim(provider)) > 0),
    catalog_json               TEXT NOT NULL CHECK (json_valid(catalog_json)),
    catalog_sha256             TEXT NOT NULL CHECK (length(catalog_sha256) = 64 AND catalog_sha256 NOT GLOB '*[^0-9a-f]*'),
    outcome                    TEXT NOT NULL CHECK (outcome IN ('PASSED', 'FAILED')),
    reason_code                TEXT NOT NULL DEFAULT '',
    retryable                  INTEGER NOT NULL CHECK (retryable IN (0, 1)),
    retry_at                   TIMESTAMP,
    provider_error_code        TEXT NOT NULL DEFAULT '',
    error_summary              TEXT NOT NULL DEFAULT '',
    checked_at                 TIMESTAMP NOT NULL,
    CHECK (
        (outcome = 'PASSED' AND reason_code = '')
        OR (outcome = 'FAILED' AND reason_code <> '')
    )
);
CREATE INDEX idx_cleardev_controlled_preflights_requirement
    ON cleardev_controlled_preflights (development_project_id, checked_at DESC, id DESC);
CREATE INDEX idx_cleardev_controlled_preflights_binding
    ON cleardev_controlled_preflights (role_binding_id, checked_at DESC, id DESC);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_controlled_preflights_append_only_delete
BEFORE DELETE ON cleardev_controlled_preflights
BEGIN
    SELECT RAISE(ABORT, 'cleardev controlled preflights are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_controlled_preflights_update_forbidden
BEFORE UPDATE ON cleardev_controlled_preflights
BEGIN
    SELECT RAISE(ABORT, 'cleardev controlled preflights are immutable');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_controlled_preflights_cdc_insert
AFTER INSERT ON cleardev_controlled_preflights
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT NEW.ao_project_id, NULL, 'cleardev_project_updated',
           json_object(
               'developmentProjectId', NEW.development_project_id,
               'controlledPreflightId', NEW.id,
               'outcome', NEW.outcome,
               'reasonCode', NEW.reason_code
           ),
           NEW.checked_at;
END;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_controlled_preflights_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_controlled_preflights_update_forbidden;
DROP TRIGGER IF EXISTS cleardev_controlled_preflights_append_only_delete;
DROP INDEX IF EXISTS idx_cleardev_controlled_preflights_binding;
DROP INDEX IF EXISTS idx_cleardev_controlled_preflights_requirement;
DROP TABLE IF EXISTS cleardev_controlled_preflights;
-- +goose StatementEnd
