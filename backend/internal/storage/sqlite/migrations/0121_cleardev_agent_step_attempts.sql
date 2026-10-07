-- ClearDev S12A8: append-only logical-step attempt and result evidence.
-- 0106-0120 remain immutable. Existing step rows remain compatibility summaries.
-- +goose Up

-- +goose StatementBegin
ALTER TABLE conversation_turns ADD COLUMN failure_category TEXT NOT NULL DEFAULT ''
    CHECK (failure_category IN (
        '', 'MODEL_UNAVAILABLE', 'AUTHENTICATION_REQUIRED', 'QUOTA_EXHAUSTED',
        'RATE_LIMITED', 'PROVIDER_UNAVAILABLE', 'DRIVER_INCOMPATIBLE',
        'SESSION_LOST', 'TURN_INTERRUPTED', 'OBSERVATION_TIMEOUT',
        'DELIVERY_UNKNOWN', 'PROVIDER_FAILURE', 'RESULT_INVALID'
    ));
ALTER TABLE conversation_turns ADD COLUMN provider_error_code TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_turns ADD COLUMN failure_summary TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_turns ADD COLUMN failure_retryable INTEGER NOT NULL DEFAULT 0
    CHECK (failure_retryable IN (0, 1));
ALTER TABLE conversation_turns ADD COLUMN failure_retry_at TIMESTAMP;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cleardev_agent_step_attempts (
    id                     TEXT PRIMARY KEY,
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    logical_step_id        TEXT NOT NULL CHECK (length(trim(logical_step_id)) > 0),
    step_category          TEXT NOT NULL CHECK (step_category IN (
        'STANDARD', 'COMPLEX_PLANNING', 'DIRECTION_CHANGE', 'COMPLEX_EXECUTION',
        'QUICK_EXECUTION', 'CONTROLLED_EXCEPTION', 'PROGRESS_EXPLANATION'
    )),
    step_kind              TEXT NOT NULL CHECK (length(trim(step_kind)) > 0),
    attempt_number         INTEGER NOT NULL CHECK (attempt_number = 1),
    role_binding_id        TEXT NOT NULL DEFAULT '',
    ao_session_id          TEXT NOT NULL CHECK (length(trim(ao_session_id)) > 0),
    client_message_id      TEXT NOT NULL CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256          TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    requested_at           TIMESTAMP NOT NULL,
    UNIQUE (logical_step_id, attempt_number),
    UNIQUE (client_message_id)
);
CREATE INDEX idx_cleardev_agent_step_attempts_requirement
    ON cleardev_agent_step_attempts (development_project_id, requested_at, id);

CREATE TABLE cleardev_agent_attempt_events (
    id                  TEXT PRIMARY KEY,
    attempt_id          TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
    status              TEXT NOT NULL CHECK (status IN (
        'SENT', 'CORRECTION_SENT', 'COMPLETED', 'FAILED', 'INTERRUPTED',
        'OBSERVATION_TIMEOUT', 'DELIVERY_UNKNOWN'
    )),
    client_message_id   TEXT NOT NULL DEFAULT '',
    prompt_sha256       TEXT NOT NULL DEFAULT '' CHECK (
        prompt_sha256 = '' OR (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*')
    ),
    turn_id             TEXT NOT NULL DEFAULT '',
    turn_state          TEXT NOT NULL DEFAULT '' CHECK (turn_state IN ('', 'queued', 'running', 'completed', 'interrupted', 'failed')),
    failure_category    TEXT NOT NULL DEFAULT '' CHECK (failure_category IN (
        '', 'MODEL_UNAVAILABLE', 'AUTHENTICATION_REQUIRED', 'QUOTA_EXHAUSTED',
        'RATE_LIMITED', 'PROVIDER_UNAVAILABLE', 'DRIVER_INCOMPATIBLE',
        'SESSION_LOST', 'TURN_INTERRUPTED', 'OBSERVATION_TIMEOUT',
        'DELIVERY_UNKNOWN', 'PROVIDER_FAILURE', 'RESULT_INVALID'
    )),
    retryable           INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1)),
    retry_at            TIMESTAMP,
    provider_error_code TEXT NOT NULL DEFAULT '',
    error_summary       TEXT NOT NULL DEFAULT '',
    recorded_at         TIMESTAMP NOT NULL,
    CHECK (
        (status IN ('SENT', 'CORRECTION_SENT', 'COMPLETED') AND failure_category = '')
        OR (status NOT IN ('SENT', 'CORRECTION_SENT', 'COMPLETED') AND failure_category <> '')
    )
);
CREATE INDEX idx_cleardev_agent_attempt_events_attempt
    ON cleardev_agent_attempt_events (attempt_id, recorded_at, id);

CREATE TABLE cleardev_agent_step_results (
    id                 TEXT PRIMARY KEY,
    attempt_id         TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
    result_index       INTEGER NOT NULL CHECK (result_index IN (1, 2)),
    source             TEXT NOT NULL CHECK (source IN ('ORIGINAL', 'PARSE_CORRECTION')),
    client_message_id  TEXT NOT NULL CHECK (length(trim(client_message_id)) > 0),
    turn_id            TEXT NOT NULL CHECK (length(trim(turn_id)) > 0),
    final_message_id   TEXT NOT NULL CHECK (length(trim(final_message_id)) > 0),
    raw_message_text   TEXT NOT NULL,
    raw_message_sha256 TEXT NOT NULL CHECK (length(raw_message_sha256) = 64 AND raw_message_sha256 NOT GLOB '*[^0-9a-f]*'),
    observed_at        TIMESTAMP NOT NULL,
    UNIQUE (attempt_id, result_index),
    UNIQUE (attempt_id, turn_id, final_message_id)
);
CREATE INDEX idx_cleardev_agent_step_results_attempt
    ON cleardev_agent_step_results (attempt_id, result_index);

CREATE TABLE cleardev_agent_step_result_parses (
    result_id      TEXT PRIMARY KEY REFERENCES cleardev_agent_step_results(id),
    conclusion     TEXT NOT NULL CHECK (conclusion IN ('VALID', 'INVALID')),
    error_summary  TEXT NOT NULL DEFAULT '',
    parsed_at      TIMESTAMP NOT NULL,
    CHECK (
        (conclusion = 'VALID' AND error_summary = '')
        OR (conclusion = 'INVALID' AND length(trim(error_summary)) > 0)
    )
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_step_attempts_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are immutable');
END;
CREATE TRIGGER cleardev_agent_step_attempts_delete_forbidden
BEFORE DELETE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
CREATE TRIGGER cleardev_agent_attempt_events_update_forbidden
BEFORE UPDATE ON cleardev_agent_attempt_events BEGIN
    SELECT RAISE(ABORT, 'cleardev agent attempt events are immutable');
END;
CREATE TRIGGER cleardev_agent_attempt_events_delete_forbidden
BEFORE DELETE ON cleardev_agent_attempt_events BEGIN
    SELECT RAISE(ABORT, 'cleardev agent attempt events are append-only');
END;
CREATE TRIGGER cleardev_agent_step_results_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_results BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step results are immutable');
END;
CREATE TRIGGER cleardev_agent_step_results_delete_forbidden
BEFORE DELETE ON cleardev_agent_step_results BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step results are append-only');
END;
CREATE TRIGGER cleardev_agent_step_result_parses_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_result_parses BEGIN
    SELECT RAISE(ABORT, 'cleardev agent result parses are immutable');
END;
CREATE TRIGGER cleardev_agent_step_result_parses_delete_forbidden
BEFORE DELETE ON cleardev_agent_step_result_parses BEGIN
    SELECT RAISE(ABORT, 'cleardev agent result parses are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_step_attempts_cdc_insert
AFTER INSERT ON cleardev_agent_step_attempts BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id,
                       'logicalStepId', NEW.logical_step_id,
                       'agentAttemptId', NEW.id),
           NEW.requested_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;
CREATE TRIGGER cleardev_agent_attempt_events_cdc_insert
AFTER INSERT ON cleardev_agent_attempt_events BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', attempt.development_project_id,
                       'logicalStepId', attempt.logical_step_id,
                       'agentAttemptId', attempt.id,
                       'attemptStatus', NEW.status),
           NEW.recorded_at
    FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_development_projects AS project ON project.id = attempt.development_project_id
    WHERE attempt.id = NEW.attempt_id;
END;
CREATE TRIGGER cleardev_agent_step_results_cdc_insert
AFTER INSERT ON cleardev_agent_step_results BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', attempt.development_project_id,
                       'logicalStepId', attempt.logical_step_id,
                       'agentAttemptId', attempt.id,
                       'agentResultId', NEW.id),
           NEW.observed_at
    FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_development_projects AS project ON project.id = attempt.development_project_id
    WHERE attempt.id = NEW.attempt_id;
END;
CREATE TRIGGER cleardev_agent_step_result_parses_cdc_insert
AFTER INSERT ON cleardev_agent_step_result_parses BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', attempt.development_project_id,
                       'logicalStepId', attempt.logical_step_id,
                       'agentAttemptId', attempt.id,
                       'agentResultId', result.id,
                       'parseConclusion', NEW.conclusion),
           NEW.parsed_at
    FROM cleardev_agent_step_results AS result
    JOIN cleardev_agent_step_attempts AS attempt ON attempt.id = result.attempt_id
    JOIN cleardev_development_projects AS project ON project.id = attempt.development_project_id
    WHERE result.id = NEW.result_id;
END;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_agent_step_result_parses_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_results_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_attempt_events_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_result_parses_delete_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_step_result_parses_update_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_step_results_delete_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_step_results_update_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_attempt_events_delete_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_attempt_events_update_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_delete_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_update_forbidden;
DROP TABLE IF EXISTS cleardev_agent_step_result_parses;
DROP TABLE IF EXISTS cleardev_agent_step_results;
DROP TABLE IF EXISTS cleardev_agent_attempt_events;
DROP TABLE IF EXISTS cleardev_agent_step_attempts;
ALTER TABLE conversation_turns DROP COLUMN failure_retry_at;
ALTER TABLE conversation_turns DROP COLUMN failure_retryable;
ALTER TABLE conversation_turns DROP COLUMN failure_summary;
ALTER TABLE conversation_turns DROP COLUMN provider_error_code;
ALTER TABLE conversation_turns DROP COLUMN failure_category;
-- +goose StatementEnd
