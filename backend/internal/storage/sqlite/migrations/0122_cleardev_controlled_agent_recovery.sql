-- ClearDev S12A8B: one controlled retry for a terminated, retryable attempt.
-- 0121 remains immutable; this rebuild only widens its attempt cardinality.
-- +goose Up
-- +goose NO TRANSACTION

PRAGMA foreign_keys = OFF;

-- +goose StatementBegin
ALTER TABLE cleardev_parse_corrections ADD COLUMN attempt_number INTEGER NOT NULL DEFAULT 1
    CHECK (attempt_number IN (1, 2));

DROP TRIGGER IF EXISTS cleardev_agent_step_result_parses_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_results_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_attempt_events_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_delete_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_update_forbidden;

CREATE TABLE cleardev_agent_step_attempts_next (
    id                       TEXT PRIMARY KEY,
    development_project_id   TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    logical_step_id          TEXT NOT NULL CHECK (length(trim(logical_step_id)) > 0),
    step_category            TEXT NOT NULL CHECK (step_category IN (
        'STANDARD', 'COMPLEX_PLANNING', 'DIRECTION_CHANGE', 'COMPLEX_EXECUTION',
        'QUICK_EXECUTION', 'CONTROLLED_EXCEPTION', 'PROGRESS_EXPLANATION'
    )),
    step_kind                TEXT NOT NULL CHECK (length(trim(step_kind)) > 0),
    attempt_number           INTEGER NOT NULL CHECK (attempt_number IN (1, 2)),
    role_binding_id          TEXT NOT NULL DEFAULT '',
    ao_session_id            TEXT NOT NULL CHECK (length(trim(ao_session_id)) > 0),
    client_message_id        TEXT NOT NULL CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256            TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    trigger_failure_event_id TEXT REFERENCES cleardev_agent_attempt_events(id),
    requested_at             TIMESTAMP NOT NULL,
    UNIQUE (logical_step_id, attempt_number),
    UNIQUE (client_message_id),
    CHECK (
        (attempt_number = 1 AND trigger_failure_event_id IS NULL)
        OR (attempt_number = 2 AND length(trim(trigger_failure_event_id)) > 0)
    )
);
INSERT INTO cleardev_agent_step_attempts_next (
    id, development_project_id, logical_step_id, step_category, step_kind,
    attempt_number, role_binding_id, ao_session_id, client_message_id,
    prompt_sha256, trigger_failure_event_id, requested_at
)
SELECT id, development_project_id, logical_step_id, step_category, step_kind,
       attempt_number, role_binding_id, ao_session_id, client_message_id,
       prompt_sha256, NULL, requested_at
FROM cleardev_agent_step_attempts;
DROP TABLE cleardev_agent_step_attempts;
ALTER TABLE cleardev_agent_step_attempts_next RENAME TO cleardev_agent_step_attempts;

CREATE INDEX idx_cleardev_agent_step_attempts_requirement
    ON cleardev_agent_step_attempts (development_project_id, requested_at, id);
CREATE TRIGGER cleardev_agent_step_attempts_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
CREATE TRIGGER cleardev_agent_step_attempts_delete_forbidden
BEFORE DELETE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
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

PRAGMA foreign_keys = ON;

-- +goose Down

PRAGMA foreign_keys = OFF;

-- +goose StatementBegin
DELETE FROM cleardev_agent_step_result_parses
WHERE result_id IN (
    SELECT result.id FROM cleardev_agent_step_results AS result
    JOIN cleardev_agent_step_attempts AS attempt ON attempt.id = result.attempt_id
    WHERE attempt.attempt_number = 2
);
DELETE FROM cleardev_agent_step_results
WHERE attempt_id IN (SELECT id FROM cleardev_agent_step_attempts WHERE attempt_number = 2);
DELETE FROM cleardev_agent_attempt_events
WHERE attempt_id IN (SELECT id FROM cleardev_agent_step_attempts WHERE attempt_number = 2);

DROP TRIGGER IF EXISTS cleardev_agent_step_result_parses_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_results_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_attempt_events_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_delete_forbidden;
DROP TRIGGER IF EXISTS cleardev_agent_step_attempts_update_forbidden;

CREATE TABLE cleardev_agent_step_attempts_prev (
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
INSERT INTO cleardev_agent_step_attempts_prev (
    id, development_project_id, logical_step_id, step_category, step_kind,
    attempt_number, role_binding_id, ao_session_id, client_message_id,
    prompt_sha256, requested_at
)
SELECT id, development_project_id, logical_step_id, step_category, step_kind,
       attempt_number, role_binding_id, ao_session_id, client_message_id,
       prompt_sha256, requested_at
FROM cleardev_agent_step_attempts
WHERE attempt_number = 1;
DROP TABLE cleardev_agent_step_attempts;
ALTER TABLE cleardev_agent_step_attempts_prev RENAME TO cleardev_agent_step_attempts;

CREATE INDEX idx_cleardev_agent_step_attempts_requirement
    ON cleardev_agent_step_attempts (development_project_id, requested_at, id);
CREATE TRIGGER cleardev_agent_step_attempts_update_forbidden
BEFORE UPDATE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
CREATE TRIGGER cleardev_agent_step_attempts_delete_forbidden
BEFORE DELETE ON cleardev_agent_step_attempts BEGIN
    SELECT RAISE(ABORT, 'cleardev agent step attempts are append-only');
END;
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

ALTER TABLE cleardev_parse_corrections DROP COLUMN attempt_number;
-- +goose StatementEnd

PRAGMA foreign_keys = ON;
