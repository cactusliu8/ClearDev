-- ClearDev S03 stores generic desktop human-decision facts. Production only
-- registers CONFIRM_REQUIREMENT_VERSION; the tables stay kind-agnostic so S05
-- can add a direction-change spec without another envelope schema.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_human_decision_requests (
    id TEXT PRIMARY KEY,
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    decision_kind TEXT NOT NULL,
    binding_schema_version INTEGER NOT NULL CHECK (binding_schema_version >= 1),
    binding_json TEXT NOT NULL,
    display_json TEXT NOT NULL,
    content_sha256 TEXT NOT NULL CHECK (length(content_sha256) = 64),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'RESOLVED')),
    decision TEXT NOT NULL DEFAULT '' CHECK (decision IN ('', 'APPROVE', 'REJECT')),
    resolved_at TIMESTAMP,
    created_at TIMESTAMP NOT NULL,
    CHECK (
        (status = 'PENDING' AND decision = '' AND resolved_at IS NULL)
        OR (status = 'RESOLVED' AND decision IN ('APPROVE', 'REJECT') AND resolved_at IS NOT NULL)
    )
);
CREATE INDEX idx_cleardev_human_decision_requests_project_status
    ON cleardev_human_decision_requests (development_project_id, status, created_at);
CREATE UNIQUE INDEX idx_cleardev_human_decision_requests_confirm_version
    ON cleardev_human_decision_requests (json_extract(binding_json, '$.requirementVersionId'))
    WHERE decision_kind = 'CONFIRM_REQUIREMENT_VERSION';

CREATE TABLE cleardev_human_decision_dispatches (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL REFERENCES cleardev_human_decision_requests(id),
    desktop_run_id TEXT NOT NULL,
    nonce_sha256 TEXT NOT NULL UNIQUE CHECK (length(nonce_sha256) = 64),
    issued_at TIMESTAMP NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    consumed_at TIMESTAMP,
    outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('', 'APPROVE', 'REJECT', 'LATER', 'EXPIRED', 'DISCONNECTED', 'INVALID')),
    CHECK (
        (consumed_at IS NULL AND outcome = '')
        OR (consumed_at IS NOT NULL AND outcome IN ('APPROVE', 'REJECT', 'LATER', 'EXPIRED', 'DISCONNECTED', 'INVALID'))
    )
);
CREATE INDEX idx_cleardev_human_decision_dispatches_request
    ON cleardev_human_decision_dispatches (request_id, issued_at);
CREATE INDEX idx_cleardev_human_decision_dispatches_desktop_open
    ON cleardev_human_decision_dispatches (desktop_run_id, consumed_at);

CREATE TABLE cleardev_human_decision_effects (
    request_id TEXT PRIMARY KEY REFERENCES cleardev_human_decision_requests(id),
    decision TEXT NOT NULL CHECK (decision IN ('APPROVE', 'REJECT')),
    event_sequence INTEGER NOT NULL,
    created_at TIMESTAMP NOT NULL
);

CREATE TRIGGER cleardev_human_decision_requests_content_immutable
BEFORE UPDATE ON cleardev_human_decision_requests
FOR EACH ROW
WHEN NEW.id IS NOT OLD.id
  OR NEW.development_project_id IS NOT OLD.development_project_id
  OR NEW.decision_kind IS NOT OLD.decision_kind
  OR NEW.binding_schema_version IS NOT OLD.binding_schema_version
  OR NEW.binding_json IS NOT OLD.binding_json
  OR NEW.display_json IS NOT OLD.display_json
  OR NEW.content_sha256 IS NOT OLD.content_sha256
  OR NEW.created_at IS NOT OLD.created_at
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision request content is immutable');
END;

CREATE TRIGGER cleardev_human_decision_requests_settle_once
BEFORE UPDATE ON cleardev_human_decision_requests
FOR EACH ROW
WHEN OLD.status = 'RESOLVED'
 OR NOT (
        OLD.status = 'PENDING'
    AND NEW.status = 'RESOLVED'
    AND NEW.decision IN ('APPROVE', 'REJECT')
    AND NEW.resolved_at IS NOT NULL
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision request can only settle once');
END;

CREATE TRIGGER cleardev_human_decision_requests_append_only_delete
BEFORE DELETE ON cleardev_human_decision_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision requests are append-only');
END;

CREATE TRIGGER cleardev_human_decision_dispatches_identity_immutable
BEFORE UPDATE ON cleardev_human_decision_dispatches
FOR EACH ROW
WHEN NEW.id IS NOT OLD.id
  OR NEW.request_id IS NOT OLD.request_id
  OR NEW.desktop_run_id IS NOT OLD.desktop_run_id
  OR NEW.nonce_sha256 IS NOT OLD.nonce_sha256
  OR NEW.issued_at IS NOT OLD.issued_at
  OR NEW.expires_at IS NOT OLD.expires_at
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision dispatch identity is immutable');
END;

CREATE TRIGGER cleardev_human_decision_dispatches_consume_once
BEFORE UPDATE ON cleardev_human_decision_dispatches
FOR EACH ROW
WHEN OLD.consumed_at IS NOT NULL
 OR NEW.consumed_at IS NULL
 OR NEW.outcome NOT IN ('APPROVE', 'REJECT', 'LATER', 'EXPIRED', 'DISCONNECTED', 'INVALID')
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision dispatch can only be consumed once');
END;

CREATE TRIGGER cleardev_human_decision_dispatches_append_only_delete
BEFORE DELETE ON cleardev_human_decision_dispatches
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision dispatches are append-only');
END;

CREATE TRIGGER cleardev_human_decision_effects_append_only_update
BEFORE UPDATE ON cleardev_human_decision_effects
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision effects are append-only');
END;

CREATE TRIGGER cleardev_human_decision_effects_append_only_delete
BEFORE DELETE ON cleardev_human_decision_effects
BEGIN
    SELECT RAISE(ABORT, 'cleardev human decision effects are append-only');
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_human_decision_effects_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_human_decision_effects_append_only_update;
DROP TRIGGER IF EXISTS cleardev_human_decision_dispatches_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_human_decision_dispatches_consume_once;
DROP TRIGGER IF EXISTS cleardev_human_decision_dispatches_identity_immutable;
DROP TRIGGER IF EXISTS cleardev_human_decision_requests_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_human_decision_requests_settle_once;
DROP TRIGGER IF EXISTS cleardev_human_decision_requests_content_immutable;
DROP TABLE IF EXISTS cleardev_human_decision_effects;
DROP TABLE IF EXISTS cleardev_human_decision_dispatches;
DROP TABLE IF EXISTS cleardev_human_decision_requests;
-- +goose StatementEnd
