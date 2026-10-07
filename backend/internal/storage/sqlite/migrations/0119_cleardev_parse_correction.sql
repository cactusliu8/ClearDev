-- ClearDev S12A5: one mechanical parse-correction turn per Agent step.
-- 0110-0118 remain immutable. This table is coordinator state, not a public
-- Agent result, so it has no CDC payload of its own.
-- +goose Up

-- +goose StatementBegin
CREATE TABLE cleardev_parse_corrections (
    step_id            TEXT PRIMARY KEY,
    client_message_id  TEXT NOT NULL CHECK (length(trim(client_message_id)) > 0),
    prompt_text        TEXT NOT NULL CHECK (length(trim(prompt_text)) > 0),
    prompt_sha256      TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    sent_at            TIMESTAMP NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_parse_corrections_append_only_delete
BEFORE DELETE ON cleardev_parse_corrections
BEGIN
    SELECT RAISE(ABORT, 'cleardev parse corrections are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_parse_corrections_update_forbidden
BEFORE UPDATE ON cleardev_parse_corrections
BEGIN
    SELECT RAISE(ABORT, 'cleardev parse corrections are immutable');
END;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_parse_corrections_update_forbidden;
DROP TRIGGER IF EXISTS cleardev_parse_corrections_append_only_delete;
DROP TABLE IF EXISTS cleardev_parse_corrections;
-- +goose StatementEnd
