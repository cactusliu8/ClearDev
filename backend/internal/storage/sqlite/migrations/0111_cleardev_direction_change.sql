-- ClearDev S05 direction-change facts. S04 compilation tables stay unused for
-- v2: this migration stores the user intent, Steward request, v1 stop gate,
-- task snapshot, per-task processing, checkpoints, and isolated v2 compilation.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_direction_intents (
    request_id TEXT PRIMARY KEY,
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256 TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    message TEXT NOT NULL CHECK (length(trim(message)) > 0 AND length(message) <= 32768),
    message_sha256 TEXT NOT NULL CHECK (length(message_sha256) = 64 AND message_sha256 NOT GLOB '*[^0-9a-f]*'),
    steward_role_binding_id TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    direction_request_id TEXT NOT NULL UNIQUE CHECK (length(trim(direction_request_id)) > 0),
    agent_step_id TEXT NOT NULL UNIQUE CHECK (length(trim(agent_step_id)) > 0),
    created_at TIMESTAMP NOT NULL,
    UNIQUE (requirement_version_id)
);

CREATE TABLE cleardev_direction_agent_steps (
    id                 TEXT PRIMARY KEY,
    role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    step_kind          TEXT NOT NULL CHECK (step_kind IN (
        'DIRECTION_CHANGE_REQUEST', 'REQUIREMENT_COMPILATION'
    )),
    request_id         TEXT NOT NULL CHECK (length(trim(request_id)) > 0),
    client_message_id  TEXT NOT NULL UNIQUE CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256      TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    send_status        TEXT NOT NULL CHECK (send_status IN ('PENDING', 'SENT', 'SETTLED', 'FAILED')),
    turn_id            TEXT,
    final_message_id   TEXT,
    final_message_text TEXT,
    message_sha256     TEXT CHECK (message_sha256 IS NULL OR (length(message_sha256) = 64 AND message_sha256 NOT GLOB '*[^0-9a-f]*')),
    requested_at       TIMESTAMP NOT NULL,
    sent_at            TIMESTAMP,
    completed_at       TIMESTAMP,
    failed_at          TIMESTAMP,
    reason_code        TEXT NOT NULL DEFAULT '',
    UNIQUE (role_binding_id, step_kind, request_id),
    CHECK (
        (send_status = 'PENDING' AND sent_at IS NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SENT' AND sent_at IS NOT NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SETTLED' AND sent_at IS NOT NULL AND turn_id IS NOT NULL AND final_message_id IS NOT NULL AND final_message_text IS NOT NULL AND length(final_message_text) > 0 AND message_sha256 IS NOT NULL AND completed_at IS NOT NULL AND failed_at IS NULL)
        OR (send_status = 'FAILED' AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NOT NULL)
    )
);
CREATE INDEX idx_cleardev_direction_agent_steps_binding
    ON cleardev_direction_agent_steps (role_binding_id, requested_at);

CREATE TABLE cleardev_direction_requests (
    id TEXT PRIMARY KEY,
    intent_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_direction_intents(request_id),
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    summary TEXT NOT NULL CHECK (length(trim(summary)) > 0),
    affected_requirement_ids_json TEXT NOT NULL CHECK (json_valid(affected_requirement_ids_json) AND json_type(affected_requirement_ids_json) = 'array'),
    result_sha256 TEXT NOT NULL CHECK (length(result_sha256) = 64 AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
    agent_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_direction_agent_steps(id),
    created_at TIMESTAMP NOT NULL,
    UNIQUE (requirement_version_id)
);

CREATE TABLE cleardev_direction_stop_gates (
    id TEXT PRIMARY KEY,
    direction_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_direction_requests(id),
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    task_set_version INTEGER NOT NULL CHECK (task_set_version >= 0),
    snapshot_sha256 TEXT NOT NULL CHECK (length(snapshot_sha256) = 64 AND snapshot_sha256 NOT GLOB '*[^0-9a-f]*'),
    status TEXT NOT NULL CHECK (status IN ('ACTIVE', 'CLOSED')),
    closed_at TIMESTAMP,
    close_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    CHECK (
        (status = 'ACTIVE' AND closed_at IS NULL AND close_reason = '')
        OR (status = 'CLOSED' AND closed_at IS NOT NULL)
    )
);
CREATE UNIQUE INDEX idx_cleardev_direction_stop_gates_active_version
    ON cleardev_direction_stop_gates (requirement_version_id)
    WHERE status = 'ACTIVE';

CREATE TABLE cleardev_direction_task_snapshots (
    gate_id TEXT NOT NULL REFERENCES cleardev_direction_stop_gates(id),
    task_id TEXT NOT NULL REFERENCES cleardev_work_items(id),
    status TEXT NOT NULL,
    paused_from_status TEXT NOT NULL DEFAULT '',
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (gate_id, task_id)
);

CREATE TABLE cleardev_direction_task_processings (
    id TEXT PRIMARY KEY,
    direction_request_id TEXT NOT NULL REFERENCES cleardev_direction_requests(id),
    task_id TEXT NOT NULL REFERENCES cleardev_work_items(id),
    occupancy TEXT NOT NULL CHECK (occupancy IN ('PENDING', 'OCCUPIED', 'FINAL')),
    interrupt_result TEXT NOT NULL DEFAULT '',
    checkpoint_id TEXT,
    cancel_result TEXT NOT NULL DEFAULT '',
    reason_code TEXT NOT NULL DEFAULT '',
    occupied_at TIMESTAMP,
    finalized_at TIMESTAMP,
    UNIQUE (direction_request_id, task_id),
    CHECK (
        (occupancy = 'PENDING' AND occupied_at IS NULL AND finalized_at IS NULL AND checkpoint_id IS NULL)
        OR (occupancy = 'OCCUPIED' AND occupied_at IS NOT NULL AND finalized_at IS NULL)
        OR (occupancy = 'FINAL' AND occupied_at IS NOT NULL AND finalized_at IS NOT NULL)
    )
);

CREATE TABLE cleardev_direction_checkpoints (
    id TEXT PRIMARY KEY,
    processing_id TEXT NOT NULL UNIQUE REFERENCES cleardev_direction_task_processings(id),
    task_id TEXT NOT NULL REFERENCES cleardev_work_items(id),
    session_id TEXT,
    worktree_path TEXT NOT NULL DEFAULT '',
    baseline_sha TEXT NOT NULL DEFAULT '',
    head_sha TEXT NOT NULL DEFAULT '',
    dirty INTEGER NOT NULL CHECK (dirty IN (0, 1)),
    staged INTEGER NOT NULL CHECK (staged IN (0, 1)),
    untracked INTEGER NOT NULL CHECK (untracked IN (0, 1)),
    change_summary_json TEXT NOT NULL CHECK (json_valid(change_summary_json) AND json_type(change_summary_json) = 'array'),
    candidate_commit_id TEXT REFERENCES cleardev_candidate_commits(id),
    kind TEXT NOT NULL CHECK (kind IN (
        'CLEAN_CANDIDATE', 'DIRTY_PRESERVED', 'NO_WORKTREE', 'NO_SESSION',
        'UNSTARTED', 'TERMINAL', 'OBSERVE_FAILED'
    )),
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_direction_revisions (
    direction_request_id TEXT PRIMARY KEY REFERENCES cleardev_direction_requests(id),
    previous_requirement_version_id TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    target_requirement_version_id TEXT NOT NULL UNIQUE CHECK (length(trim(target_requirement_version_id)) > 0),
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_direction_compilation_requests (
    id                            TEXT PRIMARY KEY,
    direction_request_id          TEXT NOT NULL REFERENCES cleardev_direction_revisions(direction_request_id),
    development_project_id        TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    agent_step_id                 TEXT NOT NULL UNIQUE REFERENCES cleardev_direction_agent_steps(id),
    clarification_round           INTEGER NOT NULL CHECK (clarification_round IN (0, 1, 2)),
    compilation_context_sha256    TEXT NOT NULL CHECK (length(compilation_context_sha256) = 64 AND compilation_context_sha256 NOT GLOB '*[^0-9a-f]*'),
    additional_round_reason       TEXT NOT NULL DEFAULT '',
    created_at                    TIMESTAMP NOT NULL,
    UNIQUE (direction_request_id, clarification_round)
);

CREATE TABLE cleardev_direction_clarification_questions (
    compilation_request_id TEXT NOT NULL REFERENCES cleardev_direction_compilation_requests(id),
    question_key           TEXT NOT NULL CHECK (length(trim(question_key)) > 0),
    text                   TEXT NOT NULL CHECK (length(trim(text)) > 0),
    reason                 TEXT NOT NULL CHECK (length(trim(reason)) > 0),
    requirement_keys_json  TEXT NOT NULL CHECK (json_valid(requirement_keys_json) AND json_type(requirement_keys_json) = 'array'),
    ordinal                INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (compilation_request_id, question_key)
);

CREATE TABLE cleardev_direction_clarification_answers (
    compilation_request_id TEXT NOT NULL REFERENCES cleardev_direction_compilation_requests(id),
    question_key           TEXT NOT NULL,
    text                   TEXT NOT NULL CHECK (length(trim(text)) > 0),
    created_at             TIMESTAMP NOT NULL,
    PRIMARY KEY (compilation_request_id, question_key),
    FOREIGN KEY (compilation_request_id, question_key)
        REFERENCES cleardev_direction_clarification_questions (compilation_request_id, question_key)
);

CREATE TABLE cleardev_direction_compilations (
    id                           TEXT PRIMARY KEY,
    direction_request_id         TEXT NOT NULL REFERENCES cleardev_direction_revisions(direction_request_id),
    development_project_id       TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    compilation_request_id       TEXT NOT NULL UNIQUE REFERENCES cleardev_direction_compilation_requests(id),
    agent_step_id                TEXT NOT NULL UNIQUE REFERENCES cleardev_direction_agent_steps(id),
    outcome                      TEXT NOT NULL CHECK (outcome IN ('READY', 'NEEDS_HUMAN')),
    summary                      TEXT NOT NULL CHECK (length(trim(summary)) > 0),
    normalized_requirement_json  TEXT NOT NULL DEFAULT '',
    compilation_sha256           TEXT NOT NULL DEFAULT '' CHECK (
        compilation_sha256 = ''
        OR (length(compilation_sha256) = 64 AND compilation_sha256 NOT GLOB '*[^0-9a-f]*')
    ),
    turn_id                      TEXT NOT NULL,
    final_message_id             TEXT NOT NULL,
    raw_message_text             TEXT NOT NULL CHECK (length(raw_message_text) > 0),
    raw_message_sha256           TEXT NOT NULL CHECK (length(raw_message_sha256) = 64 AND raw_message_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at                   TIMESTAMP NOT NULL,
    CHECK (
        (outcome = 'READY' AND length(trim(normalized_requirement_json)) > 0 AND length(compilation_sha256) = 64)
        OR (outcome = 'NEEDS_HUMAN' AND normalized_requirement_json = '' AND compilation_sha256 = '')
    )
);

CREATE TABLE cleardev_direction_id_maps (
    compilation_id TEXT NOT NULL REFERENCES cleardev_direction_compilations(id),
    kind           TEXT NOT NULL CHECK (kind IN ('REQUIREMENT', 'ACCEPTANCE')),
    temporary_key  TEXT NOT NULL CHECK (length(trim(temporary_key)) > 0),
    stable_id      TEXT NOT NULL CHECK (length(trim(stable_id)) > 0),
    ordinal        INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (compilation_id, kind, temporary_key),
    UNIQUE (compilation_id, kind, stable_id)
);

CREATE UNIQUE INDEX idx_cleardev_human_decision_requests_approve_direction
    ON cleardev_human_decision_requests (json_extract(binding_json, '$.directionRequestId'))
    WHERE decision_kind = 'APPROVE_DIRECTION_CHANGE';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_direction_intents_append_only_update
BEFORE UPDATE ON cleardev_direction_intents
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction intents are append-only');
END;
CREATE TRIGGER cleardev_direction_intents_append_only_delete
BEFORE DELETE ON cleardev_direction_intents
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction intents are append-only');
END;

CREATE TRIGGER cleardev_direction_agent_step_update_valid
BEFORE UPDATE ON cleardev_direction_agent_steps
WHEN OLD.role_binding_id IS NOT NEW.role_binding_id
  OR OLD.step_kind IS NOT NEW.step_kind
  OR OLD.request_id IS NOT NEW.request_id
  OR OLD.client_message_id IS NOT NEW.client_message_id
  OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
  OR OLD.requested_at IS NOT NEW.requested_at
  OR OLD.send_status NOT IN ('PENDING', 'SENT')
  OR (OLD.send_status = 'PENDING' AND NEW.send_status NOT IN ('SENT', 'FAILED'))
  OR (OLD.send_status = 'SENT' AND NEW.send_status NOT IN ('SETTLED', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction agent step is immutable or invalid');
END;
CREATE TRIGGER cleardev_direction_agent_step_append_only_delete
BEFORE DELETE ON cleardev_direction_agent_steps
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction agent steps are append-only');
END;

CREATE TRIGGER cleardev_direction_requests_append_only_update
BEFORE UPDATE ON cleardev_direction_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction requests are append-only');
END;
CREATE TRIGGER cleardev_direction_requests_append_only_delete
BEFORE DELETE ON cleardev_direction_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction requests are append-only');
END;

CREATE TRIGGER cleardev_direction_stop_gate_update_valid
BEFORE UPDATE ON cleardev_direction_stop_gates
WHEN OLD.direction_request_id IS NOT NEW.direction_request_id
  OR OLD.development_project_id IS NOT NEW.development_project_id
  OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
  OR OLD.task_set_version IS NOT NEW.task_set_version
  OR OLD.snapshot_sha256 IS NOT NEW.snapshot_sha256
  OR OLD.created_at IS NOT NEW.created_at
  OR OLD.status <> 'ACTIVE'
  OR NEW.status <> 'CLOSED'
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction stop gate is immutable or invalid');
END;
CREATE TRIGGER cleardev_direction_stop_gate_append_only_delete
BEFORE DELETE ON cleardev_direction_stop_gates
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction stop gates are append-only');
END;

CREATE TRIGGER cleardev_direction_task_snapshots_append_only_update
BEFORE UPDATE ON cleardev_direction_task_snapshots
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction task snapshots are append-only');
END;
CREATE TRIGGER cleardev_direction_task_snapshots_append_only_delete
BEFORE DELETE ON cleardev_direction_task_snapshots
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction task snapshots are append-only');
END;

CREATE TRIGGER cleardev_direction_task_processing_update_valid
BEFORE UPDATE ON cleardev_direction_task_processings
WHEN OLD.direction_request_id IS NOT NEW.direction_request_id
  OR OLD.task_id IS NOT NEW.task_id
  OR OLD.id IS NOT NEW.id
  OR OLD.occupancy NOT IN ('PENDING', 'OCCUPIED')
  OR (OLD.occupancy = 'PENDING' AND NEW.occupancy NOT IN ('OCCUPIED', 'FINAL'))
  OR (OLD.occupancy = 'OCCUPIED' AND NEW.occupancy NOT IN ('OCCUPIED', 'FINAL'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction task processing is immutable or invalid');
END;
CREATE TRIGGER cleardev_direction_task_processing_append_only_delete
BEFORE DELETE ON cleardev_direction_task_processings
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction task processings are append-only');
END;

CREATE TRIGGER cleardev_direction_checkpoints_append_only_update
BEFORE UPDATE ON cleardev_direction_checkpoints
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction checkpoints are append-only');
END;
CREATE TRIGGER cleardev_direction_checkpoints_append_only_delete
BEFORE DELETE ON cleardev_direction_checkpoints
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction checkpoints are append-only');
END;

CREATE TRIGGER cleardev_direction_revisions_append_only_update
BEFORE UPDATE ON cleardev_direction_revisions
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction revisions are append-only');
END;
CREATE TRIGGER cleardev_direction_revisions_append_only_delete
BEFORE DELETE ON cleardev_direction_revisions
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction revisions are append-only');
END;

CREATE TRIGGER cleardev_direction_compilation_requests_append_only_update
BEFORE UPDATE ON cleardev_direction_compilation_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction compilation requests are append-only');
END;
CREATE TRIGGER cleardev_direction_compilation_requests_append_only_delete
BEFORE DELETE ON cleardev_direction_compilation_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction compilation requests are append-only');
END;

CREATE TRIGGER cleardev_direction_questions_append_only_update
BEFORE UPDATE ON cleardev_direction_clarification_questions
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction clarification questions are append-only');
END;
CREATE TRIGGER cleardev_direction_questions_append_only_delete
BEFORE DELETE ON cleardev_direction_clarification_questions
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction clarification questions are append-only');
END;

CREATE TRIGGER cleardev_direction_answers_append_only_update
BEFORE UPDATE ON cleardev_direction_clarification_answers
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction clarification answers are append-only');
END;
CREATE TRIGGER cleardev_direction_answers_append_only_delete
BEFORE DELETE ON cleardev_direction_clarification_answers
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction clarification answers are append-only');
END;

CREATE TRIGGER cleardev_direction_compilations_append_only_update
BEFORE UPDATE ON cleardev_direction_compilations
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction compilations are append-only');
END;
CREATE TRIGGER cleardev_direction_compilations_append_only_delete
BEFORE DELETE ON cleardev_direction_compilations
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction compilations are append-only');
END;

CREATE TRIGGER cleardev_direction_id_maps_append_only_update
BEFORE UPDATE ON cleardev_direction_id_maps
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction id maps are append-only');
END;
CREATE TRIGGER cleardev_direction_id_maps_append_only_delete
BEFORE DELETE ON cleardev_direction_id_maps
BEGIN
    SELECT RAISE(ABORT, 'cleardev direction id maps are append-only');
END;

CREATE TRIGGER cleardev_direction_intents_cdc_insert
AFTER INSERT ON cleardev_direction_intents
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'directionIntentId', NEW.request_id),
           NEW.created_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_direction_requests_cdc_insert
AFTER INSERT ON cleardev_direction_requests
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'directionRequestId', NEW.id),
           NEW.created_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_direction_stop_gates_cdc_insert
AFTER INSERT ON cleardev_direction_stop_gates
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'directionStopGateId', NEW.id, 'status', NEW.status),
           NEW.created_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_direction_stop_gates_cdc_update
AFTER UPDATE ON cleardev_direction_stop_gates
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'directionStopGateId', NEW.id, 'status', NEW.status),
           NEW.closed_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;

CREATE TRIGGER cleardev_direction_task_processings_cdc_insert
AFTER INSERT ON cleardev_direction_task_processings
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('directionRequestId', NEW.direction_request_id, 'taskId', NEW.task_id, 'occupancy', NEW.occupancy),
           datetime('now')
    FROM cleardev_direction_requests AS request
    JOIN cleardev_development_projects AS project ON project.id = request.development_project_id
    WHERE request.id = NEW.direction_request_id;
END;

CREATE TRIGGER cleardev_direction_task_processings_cdc_update
AFTER UPDATE ON cleardev_direction_task_processings
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('directionRequestId', NEW.direction_request_id, 'taskId', NEW.task_id, 'occupancy', NEW.occupancy),
           datetime('now')
    FROM cleardev_direction_requests AS request
    JOIN cleardev_development_projects AS project ON project.id = request.development_project_id
    WHERE request.id = NEW.direction_request_id;
END;

CREATE TRIGGER cleardev_direction_checkpoints_cdc_insert
AFTER INSERT ON cleardev_direction_checkpoints
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('checkpointId', NEW.id, 'taskId', NEW.task_id, 'kind', NEW.kind),
           NEW.created_at
    FROM cleardev_direction_task_processings AS processing
    JOIN cleardev_direction_requests AS request ON request.id = processing.direction_request_id
    JOIN cleardev_development_projects AS project ON project.id = request.development_project_id
    WHERE processing.id = NEW.processing_id;
END;

CREATE TRIGGER cleardev_direction_revisions_cdc_insert
AFTER INSERT ON cleardev_direction_revisions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('directionRequestId', NEW.direction_request_id, 'targetRequirementVersionId', NEW.target_requirement_version_id),
           NEW.created_at
    FROM cleardev_direction_requests AS request
    JOIN cleardev_development_projects AS project ON project.id = request.development_project_id
    WHERE request.id = NEW.direction_request_id;
END;

CREATE TRIGGER cleardev_direction_compilations_cdc_insert
AFTER INSERT ON cleardev_direction_compilations
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT project.ao_project_id, NULL, 'cleardev_project_updated',
           json_object('developmentProjectId', NEW.development_project_id, 'directionCompilationId', NEW.id, 'outcome', NEW.outcome),
           NEW.created_at
    FROM cleardev_development_projects AS project
    WHERE project.id = NEW.development_project_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_direction_compilations_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_direction_revisions_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_direction_checkpoints_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_direction_task_processings_cdc_update;
DROP TRIGGER IF EXISTS cleardev_direction_task_processings_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_direction_stop_gates_cdc_update;
DROP TRIGGER IF EXISTS cleardev_direction_stop_gates_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_direction_requests_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_direction_intents_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_direction_id_maps_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_id_maps_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_compilations_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_compilations_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_answers_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_answers_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_questions_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_questions_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_compilation_requests_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_compilation_requests_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_revisions_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_revisions_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_checkpoints_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_checkpoints_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_task_processing_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_task_processing_update_valid;
DROP TRIGGER IF EXISTS cleardev_direction_task_snapshots_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_task_snapshots_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_stop_gate_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_stop_gate_update_valid;
DROP TRIGGER IF EXISTS cleardev_direction_requests_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_requests_append_only_update;
DROP TRIGGER IF EXISTS cleardev_direction_agent_step_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_agent_step_update_valid;
DROP TRIGGER IF EXISTS cleardev_direction_intents_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_direction_intents_append_only_update;
DROP INDEX IF EXISTS idx_cleardev_human_decision_requests_approve_direction;
DROP TABLE IF EXISTS cleardev_direction_id_maps;
DROP TABLE IF EXISTS cleardev_direction_compilations;
DROP TABLE IF EXISTS cleardev_direction_clarification_answers;
DROP TABLE IF EXISTS cleardev_direction_clarification_questions;
DROP TABLE IF EXISTS cleardev_direction_compilation_requests;
DROP TABLE IF EXISTS cleardev_direction_revisions;
DROP TABLE IF EXISTS cleardev_direction_checkpoints;
DROP TABLE IF EXISTS cleardev_direction_task_processings;
DROP TABLE IF EXISTS cleardev_direction_task_snapshots;
DROP TABLE IF EXISTS cleardev_direction_stop_gates;
DROP TABLE IF EXISTS cleardev_direction_requests;
DROP TABLE IF EXISTS cleardev_direction_agent_steps;
DROP TABLE IF EXISTS cleardev_direction_intents;
-- +goose StatementEnd
