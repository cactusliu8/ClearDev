-- ClearDev S10 trusted-progress explanation requests.
-- 0106-0115 stay unchanged. This table stores steward notes, not overall progress.
-- +goose Up
-- +goose NO TRANSACTION

-- +goose StatementBegin
CREATE TABLE cleardev_progress_explanation_requests (
    id TEXT PRIMARY KEY,
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    fact_summary_sha256 TEXT NOT NULL CHECK (length(fact_summary_sha256) = 64 AND fact_summary_sha256 NOT GLOB '*[^0-9a-f]*'),
    max_event_sequence INTEGER NOT NULL CHECK (max_event_sequence >= 0),
    source_steward_session_id TEXT NOT NULL,
    continuation_of_session_id TEXT,
    ao_session_id TEXT,
    session_creation_idempotency_key TEXT NOT NULL,
    client_message_id TEXT NOT NULL,
    prompt_text TEXT NOT NULL,
    prompt_sha256 TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    status TEXT NOT NULL CHECK (status IN ('PENDING', 'SENT', 'SETTLED', 'FAILED')),
    result_json TEXT,
    result_sha256 TEXT CHECK (result_sha256 IS NULL OR (length(result_sha256) = 64 AND result_sha256 NOT GLOB '*[^0-9a-f]*')),
    reason_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    sent_at TIMESTAMP,
    settled_at TIMESTAMP,
    UNIQUE (development_project_id, fact_summary_sha256),
    UNIQUE (client_message_id),
    UNIQUE (session_creation_idempotency_key),
    CHECK (
        (status = 'PENDING' AND result_json IS NULL AND result_sha256 IS NULL AND sent_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'SENT' AND result_json IS NULL AND result_sha256 IS NULL AND sent_at IS NOT NULL AND settled_at IS NULL AND ao_session_id IS NOT NULL AND reason_code = '')
        OR (status = 'SETTLED' AND result_json IS NOT NULL AND result_sha256 IS NOT NULL AND sent_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code = '')
        OR (status = 'FAILED' AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_progress_explanation_append_only_delete
BEFORE DELETE ON cleardev_progress_explanation_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev progress explanation requests are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_progress_explanation_update_valid
BEFORE UPDATE ON cleardev_progress_explanation_requests
WHEN NOT (
    OLD.id = NEW.id
    AND OLD.development_project_id = NEW.development_project_id
    AND OLD.fact_summary_sha256 = NEW.fact_summary_sha256
    AND OLD.max_event_sequence = NEW.max_event_sequence
    AND OLD.source_steward_session_id = NEW.source_steward_session_id
    AND OLD.session_creation_idempotency_key = NEW.session_creation_idempotency_key
    AND OLD.client_message_id = NEW.client_message_id
    AND OLD.prompt_text = NEW.prompt_text
    AND OLD.prompt_sha256 = NEW.prompt_sha256
    AND OLD.created_at = NEW.created_at
    AND (
        (OLD.status = 'PENDING' AND NEW.status = 'SENT'
            AND COALESCE(OLD.continuation_of_session_id, '') = COALESCE(NEW.continuation_of_session_id, '')
            AND NEW.ao_session_id IS NOT NULL)
        OR (OLD.status = 'PENDING' AND NEW.status = 'FAILED'
            AND NEW.settled_at IS NOT NULL AND NEW.reason_code <> '')
        OR (OLD.status = 'SENT' AND NEW.status = 'SETTLED'
            AND OLD.ao_session_id = NEW.ao_session_id AND OLD.sent_at = NEW.sent_at
            AND COALESCE(OLD.continuation_of_session_id, '') = COALESCE(NEW.continuation_of_session_id, ''))
        OR (OLD.status = 'SENT' AND NEW.status = 'FAILED'
            AND OLD.ao_session_id = NEW.ao_session_id AND OLD.sent_at = NEW.sent_at
            AND COALESCE(OLD.continuation_of_session_id, '') = COALESCE(NEW.continuation_of_session_id, '')
            AND NEW.settled_at IS NOT NULL AND NEW.reason_code <> '')
        OR (OLD.status = 'PENDING' AND NEW.status = 'PENDING'
            AND NEW.sent_at IS NULL AND NEW.settled_at IS NULL AND NEW.result_json IS NULL AND NEW.result_sha256 IS NULL
            AND (OLD.ao_session_id IS NULL OR OLD.ao_session_id = NEW.ao_session_id)
            AND (OLD.continuation_of_session_id IS NULL OR OLD.continuation_of_session_id = NEW.continuation_of_session_id))
    )
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev progress explanation request update is not a valid status transition');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_progress_explanation_cdc_insert
AFTER INSERT ON cleardev_progress_explanation_requests
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'progressExplanationId', NEW.id, 'status', NEW.status),
           NEW.created_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_progress_explanation_cdc_update
AFTER UPDATE ON cleardev_progress_explanation_requests
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'progressExplanationId', NEW.id, 'status', NEW.status),
           COALESCE(NEW.settled_at, NEW.sent_at, NEW.created_at)
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_progress_explanation_cdc_update;
DROP TRIGGER IF EXISTS cleardev_progress_explanation_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_progress_explanation_update_valid;
DROP TRIGGER IF EXISTS cleardev_progress_explanation_append_only_delete;
DROP TABLE IF EXISTS cleardev_progress_explanation_requests;
-- +goose StatementEnd
