-- +goose Up
-- S01 ClearDev control-plane facts. These tables remain separate from AO
-- project/session/review state; only their foreign keys link the two domains.
-- +goose StatementBegin
CREATE TABLE cleardev_development_projects (
    id                TEXT PRIMARY KEY,
    ao_project_id     TEXT NOT NULL REFERENCES projects(id),
    name              TEXT NOT NULL CHECK (length(trim(name)) > 0),
    state             TEXT NOT NULL CHECK (state IN (
        'INTAKE', 'CONTRACTING', 'PLANNING', 'READY', 'RUNNING',
        'INTEGRATING', 'NEEDS_HUMAN', 'BLOCKED', 'CANCELLED', 'COMPLETED'
    )),
    paused_from_state TEXT,
    created_at        TIMESTAMP NOT NULL,
    updated_at        TIMESTAMP NOT NULL,
    CHECK (
        (state IN ('NEEDS_HUMAN', 'BLOCKED') AND paused_from_state IS NOT NULL)
        OR (state NOT IN ('NEEDS_HUMAN', 'BLOCKED') AND paused_from_state IS NULL)
    )
);
CREATE INDEX idx_cleardev_development_projects_ao_project
    ON cleardev_development_projects (ao_project_id, created_at);

CREATE TABLE cleardev_contract_versions (
    id                     TEXT PRIMARY KEY,
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    version                INTEGER NOT NULL CHECK (version > 0),
    contract_text          TEXT NOT NULL,
    sha256                 TEXT NOT NULL CHECK (
        length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    state                  TEXT NOT NULL CHECK (state IN (
        'DRAFT', 'IN_REVIEW', 'APPROVED', 'REJECTED', 'SUPERSEDED'
    )),
    superseded_by_id       TEXT REFERENCES cleardev_contract_versions(id),
    created_at             TIMESTAMP NOT NULL,
    approved_at            TIMESTAMP,
    UNIQUE (development_project_id, version)
);
CREATE INDEX idx_cleardev_contract_versions_project
    ON cleardev_contract_versions (development_project_id, version DESC);

CREATE TABLE cleardev_work_items (
    id                     TEXT PRIMARY KEY,
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    contract_version_id    TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    title                  TEXT NOT NULL CHECK (length(trim(title)) > 0),
    mode                   TEXT NOT NULL CHECK (mode IN ('QUICK', 'STANDARD', 'PARALLEL')),
    state                  TEXT NOT NULL CHECK (state IN (
        'PLANNED', 'READY', 'RUNNING', 'REVIEW', 'REWORK',
        'NEEDS_HUMAN', 'BLOCKED', 'CANCELLED', 'DONE'
    )),
    paused_from_state      TEXT,
    max_rework_count       INTEGER NOT NULL CHECK (max_rework_count >= 0),
    rework_count           INTEGER NOT NULL DEFAULT 0 CHECK (
        rework_count >= 0 AND rework_count <= max_rework_count
    ),
    created_at             TIMESTAMP NOT NULL,
    updated_at             TIMESTAMP NOT NULL,
    CHECK (
        (state IN ('NEEDS_HUMAN', 'BLOCKED') AND paused_from_state IS NOT NULL)
        OR (state NOT IN ('NEEDS_HUMAN', 'BLOCKED') AND paused_from_state IS NULL)
    )
);
CREATE INDEX idx_cleardev_work_items_project
    ON cleardev_work_items (development_project_id, created_at);
CREATE INDEX idx_cleardev_work_items_contract
    ON cleardev_work_items (contract_version_id, created_at);

CREATE TABLE cleardev_path_permission_versions (
    id           TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL REFERENCES cleardev_work_items(id),
    version      INTEGER NOT NULL CHECK (version > 0),
    write_paths  TEXT NOT NULL CHECK (json_valid(write_paths) AND json_type(write_paths) = 'array'),
    forbidden_paths TEXT NOT NULL CHECK (json_valid(forbidden_paths) AND json_type(forbidden_paths) = 'array'),
    shared_paths_require_approval TEXT NOT NULL CHECK (
        json_valid(shared_paths_require_approval)
        AND json_type(shared_paths_require_approval) = 'array'
    ),
    generated_paths TEXT NOT NULL CHECK (json_valid(generated_paths) AND json_type(generated_paths) = 'array'),
    created_at   TIMESTAMP NOT NULL,
    UNIQUE (work_item_id, version)
);
CREATE INDEX idx_cleardev_permission_versions_work_item
    ON cleardev_path_permission_versions (work_item_id, version DESC);

CREATE TABLE cleardev_required_checks (
    id           TEXT PRIMARY KEY,
    work_item_id TEXT NOT NULL REFERENCES cleardev_work_items(id),
    name         TEXT NOT NULL CHECK (length(trim(name)) > 0),
    check_kind   TEXT NOT NULL CHECK (length(trim(check_kind)) > 0),
    created_at   TIMESTAMP NOT NULL,
    UNIQUE (work_item_id, name)
);
CREATE INDEX idx_cleardev_required_checks_work_item
    ON cleardev_required_checks (work_item_id, created_at);

CREATE TABLE cleardev_candidate_commits (
    id                    TEXT PRIMARY KEY,
    work_item_id          TEXT NOT NULL REFERENCES cleardev_work_items(id),
    sequence              INTEGER NOT NULL CHECK (sequence > 0),
    ao_session_id         TEXT NOT NULL REFERENCES sessions(id),
    permission_version_id TEXT NOT NULL REFERENCES cleardev_path_permission_versions(id),
    commit_sha            TEXT NOT NULL CHECK (
        length(commit_sha) IN (40, 64) AND commit_sha NOT GLOB '*[^0-9a-f]*'
    ),
    created_at            TIMESTAMP NOT NULL,
    UNIQUE (work_item_id, sequence)
);
CREATE INDEX idx_cleardev_candidates_work_item
    ON cleardev_candidate_commits (work_item_id, sequence DESC);

CREATE TABLE cleardev_integration_candidates (
    id                     TEXT PRIMARY KEY,
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    sequence               INTEGER NOT NULL CHECK (sequence > 0),
    ao_session_id          TEXT NOT NULL REFERENCES sessions(id),
    commit_sha             TEXT NOT NULL CHECK (
        length(commit_sha) IN (40, 64) AND commit_sha NOT GLOB '*[^0-9a-f]*'
    ),
    created_at             TIMESTAMP NOT NULL,
    UNIQUE (development_project_id, sequence)
);
CREATE INDEX idx_cleardev_integration_candidates_project
    ON cleardev_integration_candidates (development_project_id, sequence DESC);

CREATE TABLE cleardev_evidence (
    sequence                 INTEGER PRIMARY KEY AUTOINCREMENT,
    id                       TEXT NOT NULL UNIQUE,
    development_project_id   TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    subject_type             TEXT NOT NULL CHECK (subject_type IN ('WORK_ITEM', 'DEVELOPMENT_PROJECT')),
    subject_id               TEXT NOT NULL,
    evidence_kind            TEXT NOT NULL CHECK (evidence_kind IN (
        'SCOPE', 'REQUIRED_CHECK', 'INTEGRATION', 'REVIEW'
    )),
    evidence_key             TEXT NOT NULL DEFAULT '',
    result                   TEXT NOT NULL CHECK (result IN ('PASS', 'FAIL')),
    candidate_commit_id      TEXT REFERENCES cleardev_candidate_commits(id),
    integration_candidate_id TEXT REFERENCES cleardev_integration_candidates(id),
    commit_sha               TEXT NOT NULL CHECK (
        length(commit_sha) IN (40, 64) AND commit_sha NOT GLOB '*[^0-9a-f]*'
    ),
    source_type              TEXT NOT NULL CHECK (source_type IN (
        'CONTROL_PLANE_CHECKER', 'REVIEW_ADAPTER'
    )),
    source_ao_session_id     TEXT REFERENCES sessions(id),
    created_at               TIMESTAMP NOT NULL,
    expires_at               TIMESTAMP,
    CHECK ((candidate_commit_id IS NULL) <> (integration_candidate_id IS NULL)),
    CHECK (
        (subject_type = 'WORK_ITEM' AND candidate_commit_id IS NOT NULL)
        OR (
            subject_type = 'DEVELOPMENT_PROJECT'
            AND evidence_kind = 'INTEGRATION'
            AND integration_candidate_id IS NOT NULL
        )
    ),
    CHECK (
        (evidence_kind = 'REQUIRED_CHECK' AND length(trim(evidence_key)) > 0)
        OR (evidence_kind <> 'REQUIRED_CHECK' AND evidence_key = '')
    )
);
CREATE INDEX idx_cleardev_evidence_candidate_key
    ON cleardev_evidence (candidate_commit_id, evidence_kind, evidence_key, sequence);
CREATE INDEX idx_cleardev_evidence_integration_candidate
    ON cleardev_evidence (integration_candidate_id, evidence_kind, sequence);

CREATE TABLE cleardev_project_events (
    sequence               INTEGER PRIMARY KEY AUTOINCREMENT,
    ao_project_id          TEXT NOT NULL REFERENCES projects(id),
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    subject_type           TEXT NOT NULL,
    subject_id             TEXT NOT NULL,
    action                 TEXT NOT NULL,
    previous_state         TEXT,
    target_state           TEXT,
	outcome                TEXT NOT NULL CHECK (outcome IN ('ACCEPTED', 'REJECTED')),
	reason_code            TEXT NOT NULL,
	reason_text            TEXT NOT NULL DEFAULT '',
	source                 TEXT NOT NULL,
    source_ao_session_id   TEXT REFERENCES sessions(id),
    created_at             TIMESTAMP NOT NULL
);
CREATE INDEX idx_cleardev_project_events_project_sequence
    ON cleardev_project_events (development_project_id, sequence);
CREATE INDEX idx_cleardev_project_events_ao_project_sequence
    ON cleardev_project_events (ao_project_id, sequence);

CREATE TRIGGER cleardev_development_projects_ao_project_immutable
BEFORE UPDATE OF ao_project_id ON cleardev_development_projects
WHEN OLD.ao_project_id IS NOT NEW.ao_project_id
BEGIN
    SELECT RAISE(ABORT, 'cleardev development project AO project is immutable');
END;

CREATE TRIGGER cleardev_contract_versions_approved_content_immutable
BEFORE UPDATE OF contract_text, sha256 ON cleardev_contract_versions
WHEN OLD.state <> 'DRAFT'
 AND (OLD.contract_text IS NOT NEW.contract_text OR OLD.sha256 IS NOT NEW.sha256)
BEGIN
    SELECT RAISE(ABORT, 'cleardev non-draft contract content is immutable');
END;

CREATE TRIGGER cleardev_work_items_identity_immutable
BEFORE UPDATE OF development_project_id, contract_version_id ON cleardev_work_items
WHEN OLD.development_project_id IS NOT NEW.development_project_id
  OR OLD.contract_version_id IS NOT NEW.contract_version_id
BEGIN
    SELECT RAISE(ABORT, 'cleardev work item project and contract are immutable');
END;

CREATE TRIGGER cleardev_work_item_contract_binding_valid
BEFORE INSERT ON cleardev_work_items
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_contract_versions AS c
    WHERE c.id = NEW.contract_version_id
      AND c.development_project_id = NEW.development_project_id
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev work item contract belongs to another project');
END;

CREATE TRIGGER cleardev_contract_superseded_binding_valid
BEFORE UPDATE OF superseded_by_id ON cleardev_contract_versions
WHEN NEW.superseded_by_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_contract_versions AS replacement
    WHERE replacement.id = NEW.superseded_by_id
      AND replacement.id <> NEW.id
      AND replacement.development_project_id = NEW.development_project_id
      AND replacement.state = 'APPROVED'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev superseding contract must be an approved version in the same project');
END;

CREATE TRIGGER cleardev_path_permissions_append_only_update
BEFORE UPDATE ON cleardev_path_permission_versions
BEGIN
    SELECT RAISE(ABORT, 'cleardev permission versions are append only');
END;

CREATE TRIGGER cleardev_path_permissions_append_only_delete
BEFORE DELETE ON cleardev_path_permission_versions
BEGIN
    SELECT RAISE(ABORT, 'cleardev permission versions are append only');
END;

CREATE TRIGGER cleardev_required_checks_ready_immutable_update
BEFORE UPDATE ON cleardev_required_checks
WHEN EXISTS (
    SELECT 1 FROM cleardev_work_items AS w
    WHERE w.id = OLD.work_item_id AND w.state <> 'PLANNED'
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev required checks are immutable after planning');
END;

CREATE TRIGGER cleardev_required_checks_ready_immutable_delete
BEFORE DELETE ON cleardev_required_checks
WHEN EXISTS (
    SELECT 1 FROM cleardev_work_items AS w
    WHERE w.id = OLD.work_item_id AND w.state <> 'PLANNED'
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev required checks are immutable after planning');
END;

CREATE TRIGGER cleardev_candidate_commit_binding_valid
BEFORE INSERT ON cleardev_candidate_commits
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_path_permission_versions AS p
    WHERE p.id = NEW.permission_version_id AND p.work_item_id = NEW.work_item_id
)
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_work_items AS w
    JOIN cleardev_development_projects AS d ON d.id = w.development_project_id
    JOIN sessions AS s ON s.id = NEW.ao_session_id
    WHERE w.id = NEW.work_item_id AND s.project_id = d.ao_project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev candidate binding does not match work item or AO project');
END;

CREATE TRIGGER cleardev_candidate_commits_append_only_update
BEFORE UPDATE ON cleardev_candidate_commits
BEGIN
    SELECT RAISE(ABORT, 'cleardev candidate commits are append only');
END;

CREATE TRIGGER cleardev_candidate_commits_append_only_delete
BEFORE DELETE ON cleardev_candidate_commits
BEGIN
    SELECT RAISE(ABORT, 'cleardev candidate commits are append only');
END;

CREATE TRIGGER cleardev_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_development_projects AS d
    JOIN sessions AS s ON s.id = NEW.ao_session_id
    WHERE d.id = NEW.development_project_id AND s.project_id = d.ao_project_id
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev integration candidate session is outside AO project');
END;

CREATE TRIGGER cleardev_integration_candidates_append_only_update
BEFORE UPDATE ON cleardev_integration_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev integration candidates are append only');
END;

CREATE TRIGGER cleardev_integration_candidates_append_only_delete
BEFORE DELETE ON cleardev_integration_candidates
BEGIN
    SELECT RAISE(ABORT, 'cleardev integration candidates are append only');
END;

CREATE TRIGGER cleardev_evidence_binding_valid
BEFORE INSERT ON cleardev_evidence
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_candidate_commits AS c
    JOIN cleardev_work_items AS w ON w.id = c.work_item_id
    WHERE c.id = NEW.candidate_commit_id
      AND NEW.integration_candidate_id IS NULL
      AND NEW.development_project_id = w.development_project_id
      AND NEW.subject_type = 'WORK_ITEM'
      AND NEW.subject_id = w.id
      AND NEW.commit_sha = c.commit_sha
)
AND NOT EXISTS (
    SELECT 1
    FROM cleardev_integration_candidates AS c
    WHERE c.id = NEW.integration_candidate_id
      AND NEW.candidate_commit_id IS NULL
      AND NEW.development_project_id = c.development_project_id
      AND NEW.subject_type = 'DEVELOPMENT_PROJECT'
      AND NEW.subject_id = c.development_project_id
      AND NEW.commit_sha = c.commit_sha
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev evidence does not match its candidate');
END;

CREATE TRIGGER cleardev_evidence_source_session_valid
BEFORE INSERT ON cleardev_evidence
WHEN NEW.source_ao_session_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM cleardev_development_projects AS d
    JOIN sessions AS s ON s.id = NEW.source_ao_session_id
    WHERE d.id = NEW.development_project_id AND s.project_id = d.ao_project_id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev evidence source session is outside AO project');
END;

CREATE TRIGGER cleardev_review_evidence_source_independent
BEFORE INSERT ON cleardev_evidence
WHEN NEW.evidence_kind = 'REVIEW'
 AND (
    NEW.source_type <> 'REVIEW_ADAPTER'
    OR NEW.source_ao_session_id IS NULL
    OR EXISTS (
        SELECT 1 FROM cleardev_candidate_commits AS c
        WHERE c.id = NEW.candidate_commit_id
          AND c.ao_session_id = NEW.source_ao_session_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev review evidence must use an independent review adapter session');
END;

CREATE TRIGGER cleardev_non_review_evidence_source_valid
BEFORE INSERT ON cleardev_evidence
WHEN NEW.evidence_kind <> 'REVIEW'
 AND (NEW.source_type <> 'CONTROL_PLANE_CHECKER' OR NEW.source_ao_session_id IS NOT NULL)
BEGIN
    SELECT RAISE(ABORT, 'cleardev non-review evidence must come from the control-plane checker');
END;

CREATE TRIGGER cleardev_evidence_append_only_update
BEFORE UPDATE ON cleardev_evidence
BEGIN
    SELECT RAISE(ABORT, 'cleardev evidence is append only');
END;

CREATE TRIGGER cleardev_evidence_append_only_delete
BEFORE DELETE ON cleardev_evidence
BEGIN
    SELECT RAISE(ABORT, 'cleardev evidence is append only');
END;

CREATE TRIGGER cleardev_project_event_binding_valid
BEFORE INSERT ON cleardev_project_events
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_development_projects AS d
    WHERE d.id = NEW.development_project_id AND d.ao_project_id = NEW.ao_project_id
)
 OR (
    NEW.source_ao_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1 FROM sessions AS s
        WHERE s.id = NEW.source_ao_session_id AND s.project_id = NEW.ao_project_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev project event binding is invalid');
END;

CREATE TRIGGER cleardev_project_events_append_only_update
BEFORE UPDATE ON cleardev_project_events
BEGIN
    SELECT RAISE(ABORT, 'cleardev project events are append only');
END;

CREATE TRIGGER cleardev_project_events_append_only_delete
BEFORE DELETE ON cleardev_project_events
BEGIN
    SELECT RAISE(ABORT, 'cleardev project events are append only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_switches_cdc_insert;
DROP TRIGGER IF EXISTS agent_switches_cdc_update;
DROP TRIGGER IF EXISTS conversation_activities_cdc_insert;
DROP TRIGGER IF EXISTS conversation_activities_cdc_update;
DROP TRIGGER IF EXISTS conversation_messages_cdc_insert;
DROP TRIGGER IF EXISTS conversation_messages_cdc_update;
DROP TRIGGER IF EXISTS conversation_turns_cdc_update;
DROP TRIGGER IF EXISTS pr_cdc_insert;
DROP TRIGGER IF EXISTS pr_cdc_update;
DROP TRIGGER IF EXISTS pr_checks_cdc_insert;
DROP TRIGGER IF EXISTS pr_checks_cdc_update;
DROP TRIGGER IF EXISTS pr_review_threads_cdc_insert;
DROP TRIGGER IF EXISTS pr_review_threads_cdc_update;
DROP TRIGGER IF EXISTS pr_session_cdc_update;
DROP TRIGGER IF EXISTS review_run_cdc_insert;
DROP TRIGGER IF EXISTS review_run_cdc_update;
DROP TRIGGER IF EXISTS session_cleanup_facts_cdc_insert;
DROP TRIGGER IF EXISTS session_cleanup_facts_cdc_update;
DROP TRIGGER IF EXISTS session_interface_transitions_cdc_insert;
DROP TRIGGER IF EXISTS session_interface_transitions_cdc_update;
DROP TRIGGER IF EXISTS sessions_cdc_insert;
DROP TRIGGER IF EXISTS sessions_cdc_update;
DROP TRIGGER IF EXISTS usage_bindings_cdc_insert;
DROP TRIGGER IF EXISTS usage_bindings_cdc_update;
DROP TRIGGER IF EXISTS usage_sources_cdc_update;

CREATE TABLE change_log_new (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects (id),
    session_id TEXT REFERENCES sessions (id),
    event_type TEXT NOT NULL
        CHECK (event_type IN (
            'session_created',
            'session_updated',
            'pr_created',
            'pr_updated',
            'pr_check_recorded',
            'pr_session_changed',
            'pr_review_thread_added',
            'pr_review_thread_resolved',
            'review_run_created',
            'review_run_updated',
            'cleardev_project_updated'
        )),
    payload    TEXT NOT NULL CHECK (json_valid(payload)),
    created_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO change_log_new (seq, project_id, session_id, event_type, payload, created_at)
SELECT seq, project_id, session_id, event_type, payload, created_at
FROM change_log;

DROP INDEX IF EXISTS idx_change_log_project;
DROP TRIGGER IF EXISTS change_log_old_insert;
DROP VIEW IF EXISTS change_log_old;
DROP TABLE change_log;
ALTER TABLE change_log_new RENAME TO change_log;
CREATE INDEX idx_change_log_project ON change_log (project_id, seq);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER agent_switches_cdc_insert
AFTER INSERT ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at
    );
END;

CREATE TRIGGER agent_switches_cdc_update
AFTER UPDATE ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at
    );
END;

CREATE TRIGGER conversation_activities_cdc_insert
AFTER INSERT ON conversation_activities
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_activities_cdc_update
AFTER UPDATE ON conversation_activities
WHEN OLD.revision <> NEW.revision
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_insert
AFTER INSERT ON conversation_messages
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', NEW.conversation_id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_update
AFTER UPDATE ON conversation_messages
WHEN OLD.revision <> NEW.revision
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_turns_cdc_update
AFTER UPDATE ON conversation_turns
WHEN OLD.state <> NEW.state
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', NEW.conversation_id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           COALESCE(NEW.completed_at, NEW.started_at, NEW.requested_at)
    FROM sessions s
    WHERE s.id = NEW.handled_by_session_id;
END;

CREATE TRIGGER pr_cdc_insert
AFTER INSERT ON pr
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'pr_created',
        json_object('url', NEW.url, 'session', NEW.session_id, 'state', NEW.pr_state,
                    'ci', NEW.ci_state, 'review', NEW.review_decision, 'mergeability', NEW.mergeability),
        NEW.updated_at);
END;

CREATE TRIGGER pr_cdc_update
AFTER UPDATE ON pr
WHEN OLD.pr_state <> NEW.pr_state
    OR OLD.ci_state <> NEW.ci_state
    OR OLD.review_decision <> NEW.review_decision
    OR OLD.mergeability <> NEW.mergeability
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'pr_updated',
        json_object('url', NEW.url, 'session', NEW.session_id, 'state', NEW.pr_state,
                    'ci', NEW.ci_state, 'review', NEW.review_decision, 'mergeability', NEW.mergeability),
        NEW.updated_at);
END;

CREATE TRIGGER pr_checks_cdc_insert
AFTER INSERT ON pr_checks
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_check_recorded',
        json_object('pr', NEW.pr_url, 'name', NEW.name, 'commit', NEW.commit_hash, 'status', NEW.status),
        NEW.created_at);
END;

CREATE TRIGGER pr_checks_cdc_update
AFTER UPDATE ON pr_checks
WHEN OLD.status <> NEW.status
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_check_recorded',
        json_object('pr', NEW.pr_url, 'name', NEW.name, 'commit', NEW.commit_hash, 'status', NEW.status),
        datetime('now'));
END;

CREATE TRIGGER pr_review_threads_cdc_insert
AFTER INSERT ON pr_review_threads
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_review_thread_added',
        json_object(
            'pr', NEW.pr_url,
            'thread', NEW.thread_id,
            'path', NEW.path,
            'line', NEW.line,
            'resolved', json(CASE WHEN NEW.resolved THEN 'true' ELSE 'false' END),
            'isBot', json(CASE WHEN NEW.is_bot THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;

CREATE TRIGGER pr_review_threads_cdc_update
AFTER UPDATE ON pr_review_threads
WHEN OLD.resolved <> NEW.resolved
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_review_thread_resolved',
        json_object(
            'pr', NEW.pr_url,
            'thread', NEW.thread_id,
            'path', NEW.path,
            'line', NEW.line,
            'resolved', json(CASE WHEN NEW.resolved THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;

CREATE TRIGGER pr_session_cdc_update
AFTER UPDATE ON pr
WHEN OLD.session_id <> NEW.session_id
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'pr_session_changed',
        json_object(
            'url', NEW.url,
            'fromSession', OLD.session_id,
            'toSession', NEW.session_id),
        NEW.updated_at);
END;

CREATE TRIGGER session_cleanup_facts_cdc_insert
AFTER INSERT ON session_cleanup_facts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'session_updated',
        json_object('id', NEW.session_id),
        datetime('now'));
END;

CREATE TRIGGER session_cleanup_facts_cdc_update
AFTER UPDATE ON session_cleanup_facts
WHEN OLD.workspace_disposition <> NEW.workspace_disposition
    OR (OLD.runtime_released_at IS NULL) <> (NEW.runtime_released_at IS NULL)
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'session_updated',
        json_object('id', NEW.session_id),
        datetime('now'));
END;

CREATE TRIGGER session_interface_transitions_cdc_insert
AFTER INSERT ON session_interface_transitions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id,
                       'interfaceTransitionId', NEW.id,
                       'interfaceTransitionPhase', NEW.phase,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM sessions s WHERE s.id = NEW.session_id;
END;

CREATE TRIGGER session_interface_transitions_cdc_update
AFTER UPDATE ON session_interface_transitions
WHEN OLD.phase <> NEW.phase
    OR OLD.error_code <> NEW.error_code
    OR OLD.error_detail <> NEW.error_detail
    OR OLD.notice_acknowledged_at IS NOT NEW.notice_acknowledged_at
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id,
                       'interfaceTransitionId', NEW.id,
                       'interfaceTransitionPhase', NEW.phase,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           COALESCE(NEW.notice_acknowledged_at, NEW.updated_at)
    FROM sessions s WHERE s.id = NEW.session_id;
END;

CREATE TRIGGER sessions_cdc_insert
AFTER INSERT ON sessions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_created',
        json_object('id', NEW.id, 'activity', NEW.activity_state, 'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END)),
        NEW.updated_at);
END;

CREATE TRIGGER sessions_cdc_update
AFTER UPDATE ON sessions
WHEN OLD.activity_state <> NEW.activity_state
    OR OLD.is_terminated <> NEW.is_terminated
    OR (OLD.first_signal_at IS NULL AND NEW.first_signal_at IS NOT NULL)
    OR OLD.preview_url <> NEW.preview_url
    OR OLD.preview_revision <> NEW.preview_revision
    OR OLD.display_name <> NEW.display_name
    OR OLD.terminate_on_pr_merge <> NEW.terminate_on_pr_merge
    OR OLD.is_pinned <> NEW.is_pinned
    OR OLD.pinned_at <> NEW.pinned_at
    OR (OLD.pinned_at IS NULL AND NEW.pinned_at IS NOT NULL)
    OR (OLD.pinned_at IS NOT NULL AND NEW.pinned_at IS NULL)
    OR OLD.session_mode <> NEW.session_mode
    OR OLD.auto_inject_review <> NEW.auto_inject_review
    OR OLD.auto_review_enabled <> NEW.auto_review_enabled
    OR OLD.harness <> NEW.harness
    OR OLD.runtime_launch_id <> NEW.runtime_launch_id
    OR OLD.agent_session_id <> NEW.agent_session_id
    OR OLD.native_transcript_path <> NEW.native_transcript_path
    OR OLD.auto_inject_ci <> NEW.auto_inject_ci
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object(
            'id', NEW.id,
            'activity', NEW.activity_state,
            'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END),
            'terminateOnPrMerge', json(CASE WHEN NEW.terminate_on_pr_merge THEN 'true' ELSE 'false' END),
            'previewUrl', NEW.preview_url,
            'previewRevision', NEW.preview_revision,
            'isPinned', json(CASE WHEN NEW.is_pinned THEN 'true' ELSE 'false' END),
            'mode', NEW.session_mode,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END),
            'autoInjectCI', json(CASE WHEN NEW.auto_inject_ci THEN 'true' ELSE 'false' END),
            'autoReviewEnabled', json(CASE WHEN NEW.auto_review_enabled THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;

CREATE TRIGGER usage_bindings_cdc_insert AFTER INSERT ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_bindings_cdc_update AFTER UPDATE ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_sources_cdc_update AFTER UPDATE ON usage_sources
WHEN OLD.anomaly_count IS NOT NEW.anomaly_count
  OR OLD.last_error_code IS NOT NEW.last_error_code
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, ub.session_id, 'session_updated', json_object('id', ub.session_id), NEW.updated_at
    FROM usage_bindings ub JOIN sessions s ON s.id = ub.session_id WHERE ub.id = NEW.binding_id;
END;

CREATE TRIGGER review_run_cdc_insert
AFTER INSERT ON review_run
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'review_run_created',
        json_object(
            'id', NEW.id,
            'reviewId', NEW.review_id,
            'sessionId', NEW.session_id,
            'pr', NEW.pr_url,
            'targetSha', NEW.target_sha,
            'status', NEW.status,
            'verdict', NEW.verdict,
            'triggerSource', NEW.trigger_source,
            'githubReviewId', NEW.github_review_id,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END)
        ),
        NEW.created_at);
END;

CREATE TRIGGER review_run_cdc_update
AFTER UPDATE ON review_run
WHEN OLD.status <> NEW.status
    OR OLD.verdict <> NEW.verdict
    OR OLD.body <> NEW.body
    OR OLD.github_review_id <> NEW.github_review_id
    OR OLD.auto_inject_review <> NEW.auto_inject_review
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'review_run_updated',
        json_object(
            'id', NEW.id,
            'reviewId', NEW.review_id,
            'sessionId', NEW.session_id,
            'pr', NEW.pr_url,
            'targetSha', NEW.target_sha,
            'status', NEW.status,
            'verdict', NEW.verdict,
            'triggerSource', NEW.trigger_source,
            'githubReviewId', NEW.github_review_id,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END)
        ),
        datetime('now'));
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_project_events_cdc_insert
AFTER INSERT ON cleardev_project_events
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        NEW.ao_project_id,
        NEW.source_ao_session_id,
        'cleardev_project_updated',
        json_object(
            'developmentProjectId', NEW.development_project_id,
            'projectEventSequence', NEW.sequence,
            'action', NEW.action,
            'outcome', NEW.outcome
        ),
        NEW.created_at
    );
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_project_events_cdc_insert;
DROP TRIGGER IF EXISTS cleardev_project_events_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_project_events_append_only_update;
DROP TRIGGER IF EXISTS cleardev_project_event_binding_valid;
DROP TRIGGER IF EXISTS cleardev_evidence_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_evidence_append_only_update;
DROP TRIGGER IF EXISTS cleardev_non_review_evidence_source_valid;
DROP TRIGGER IF EXISTS cleardev_review_evidence_source_independent;
DROP TRIGGER IF EXISTS cleardev_evidence_source_session_valid;
DROP TRIGGER IF EXISTS cleardev_evidence_binding_valid;
DROP TRIGGER IF EXISTS cleardev_integration_candidates_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_integration_candidates_append_only_update;
DROP TRIGGER IF EXISTS cleardev_integration_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_candidate_commits_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_candidate_commits_append_only_update;
DROP TRIGGER IF EXISTS cleardev_candidate_commit_binding_valid;
DROP TRIGGER IF EXISTS cleardev_required_checks_ready_immutable_delete;
DROP TRIGGER IF EXISTS cleardev_required_checks_ready_immutable_update;
DROP TRIGGER IF EXISTS cleardev_path_permissions_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_path_permissions_append_only_update;
DROP TRIGGER IF EXISTS cleardev_contract_superseded_binding_valid;
DROP TRIGGER IF EXISTS cleardev_work_item_contract_binding_valid;
DROP TRIGGER IF EXISTS cleardev_work_items_identity_immutable;
DROP TRIGGER IF EXISTS cleardev_contract_versions_approved_content_immutable;
DROP TRIGGER IF EXISTS cleardev_development_projects_ao_project_immutable;
-- +goose StatementEnd

DROP TABLE IF EXISTS cleardev_project_events;
DROP TABLE IF EXISTS cleardev_evidence;
DROP TABLE IF EXISTS cleardev_integration_candidates;
DROP TABLE IF EXISTS cleardev_candidate_commits;
DROP TABLE IF EXISTS cleardev_required_checks;
DROP TABLE IF EXISTS cleardev_path_permission_versions;
DROP TABLE IF EXISTS cleardev_work_items;
DROP TABLE IF EXISTS cleardev_contract_versions;
DROP TABLE IF EXISTS cleardev_development_projects;

-- +goose StatementBegin
DROP TRIGGER IF EXISTS agent_switches_cdc_insert;
DROP TRIGGER IF EXISTS agent_switches_cdc_update;
DROP TRIGGER IF EXISTS conversation_activities_cdc_insert;
DROP TRIGGER IF EXISTS conversation_activities_cdc_update;
DROP TRIGGER IF EXISTS conversation_messages_cdc_insert;
DROP TRIGGER IF EXISTS conversation_messages_cdc_update;
DROP TRIGGER IF EXISTS conversation_turns_cdc_update;
DROP TRIGGER IF EXISTS pr_cdc_insert;
DROP TRIGGER IF EXISTS pr_cdc_update;
DROP TRIGGER IF EXISTS pr_checks_cdc_insert;
DROP TRIGGER IF EXISTS pr_checks_cdc_update;
DROP TRIGGER IF EXISTS pr_review_threads_cdc_insert;
DROP TRIGGER IF EXISTS pr_review_threads_cdc_update;
DROP TRIGGER IF EXISTS pr_session_cdc_update;
DROP TRIGGER IF EXISTS session_cleanup_facts_cdc_insert;
DROP TRIGGER IF EXISTS session_cleanup_facts_cdc_update;
DROP TRIGGER IF EXISTS session_interface_transitions_cdc_insert;
DROP TRIGGER IF EXISTS session_interface_transitions_cdc_update;
DROP TRIGGER IF EXISTS sessions_cdc_insert;
DROP TRIGGER IF EXISTS sessions_cdc_update;
DROP TRIGGER IF EXISTS usage_bindings_cdc_insert;
DROP TRIGGER IF EXISTS usage_bindings_cdc_update;
DROP TRIGGER IF EXISTS usage_sources_cdc_update;
DROP TRIGGER IF EXISTS change_log_old_insert;
DROP VIEW IF EXISTS change_log_old;
DROP TRIGGER IF EXISTS change_log_new_insert;
DROP VIEW IF EXISTS change_log_new;
DROP TRIGGER IF EXISTS review_run_cdc_insert;
DROP TRIGGER IF EXISTS review_run_cdc_update;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE change_log_old (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT NOT NULL REFERENCES projects (id),
    session_id TEXT REFERENCES sessions (id),
    event_type TEXT NOT NULL
        CHECK (event_type IN (
            'session_created',
            'session_updated',
            'pr_created',
            'pr_updated',
            'pr_check_recorded',
            'pr_session_changed',
            'pr_review_thread_added',
            'pr_review_thread_resolved',
            'review_run_created',
            'review_run_updated'
        )),
    payload    TEXT NOT NULL CHECK (json_valid(payload)),
    created_at TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO change_log_old (seq, project_id, session_id, event_type, payload, created_at)
SELECT seq, project_id, session_id, event_type, payload, created_at
FROM change_log
WHERE event_type <> 'cleardev_project_updated';

DROP INDEX IF EXISTS idx_change_log_project;
DROP TABLE change_log;
ALTER TABLE change_log_old RENAME TO change_log;
CREATE INDEX idx_change_log_project ON change_log (project_id, seq);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER agent_switches_cdc_insert
AFTER INSERT ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at
    );
END;

CREATE TRIGGER agent_switches_cdc_update
AFTER UPDATE ON agent_switches
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at
    );
END;

CREATE TRIGGER conversation_activities_cdc_insert
AFTER INSERT ON conversation_activities
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_activities_cdc_update
AFTER UPDATE ON conversation_activities
WHEN OLD.revision <> NEW.revision
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_insert
AFTER INSERT ON conversation_messages
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', NEW.conversation_id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_messages_cdc_update
AFTER UPDATE ON conversation_messages
WHEN OLD.revision <> NEW.revision
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', c.id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM conversations c
    JOIN sessions s ON s.id = c.current_session_id
    WHERE c.id = NEW.conversation_id;
END;

CREATE TRIGGER conversation_turns_cdc_update
AFTER UPDATE ON conversation_turns
WHEN OLD.state <> NEW.state
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id, 'conversationId', NEW.conversation_id,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           COALESCE(NEW.completed_at, NEW.started_at, NEW.requested_at)
    FROM sessions s
    WHERE s.id = NEW.handled_by_session_id;
END;

CREATE TRIGGER pr_cdc_insert
AFTER INSERT ON pr
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'pr_created',
        json_object('url', NEW.url, 'session', NEW.session_id, 'state', NEW.pr_state,
                    'ci', NEW.ci_state, 'review', NEW.review_decision, 'mergeability', NEW.mergeability),
        NEW.updated_at);
END;

CREATE TRIGGER pr_cdc_update
AFTER UPDATE ON pr
WHEN OLD.pr_state <> NEW.pr_state
    OR OLD.ci_state <> NEW.ci_state
    OR OLD.review_decision <> NEW.review_decision
    OR OLD.mergeability <> NEW.mergeability
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'pr_updated',
        json_object('url', NEW.url, 'session', NEW.session_id, 'state', NEW.pr_state,
                    'ci', NEW.ci_state, 'review', NEW.review_decision, 'mergeability', NEW.mergeability),
        NEW.updated_at);
END;

CREATE TRIGGER pr_checks_cdc_insert
AFTER INSERT ON pr_checks
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_check_recorded',
        json_object('pr', NEW.pr_url, 'name', NEW.name, 'commit', NEW.commit_hash, 'status', NEW.status),
        NEW.created_at);
END;

CREATE TRIGGER pr_checks_cdc_update
AFTER UPDATE ON pr_checks
WHEN OLD.status <> NEW.status
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_check_recorded',
        json_object('pr', NEW.pr_url, 'name', NEW.name, 'commit', NEW.commit_hash, 'status', NEW.status),
        datetime('now'));
END;

CREATE TRIGGER pr_review_threads_cdc_insert
AFTER INSERT ON pr_review_threads
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_review_thread_added',
        json_object(
            'pr', NEW.pr_url,
            'thread', NEW.thread_id,
            'path', NEW.path,
            'line', NEW.line,
            'resolved', json(CASE WHEN NEW.resolved THEN 'true' ELSE 'false' END),
            'isBot', json(CASE WHEN NEW.is_bot THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;

CREATE TRIGGER pr_review_threads_cdc_update
AFTER UPDATE ON pr_review_threads
WHEN OLD.resolved <> NEW.resolved
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT s.project_id FROM pr p JOIN sessions s ON s.id = p.session_id WHERE p.url = NEW.pr_url),
        (SELECT session_id FROM pr WHERE url = NEW.pr_url),
        'pr_review_thread_resolved',
        json_object(
            'pr', NEW.pr_url,
            'thread', NEW.thread_id,
            'path', NEW.path,
            'line', NEW.line,
            'resolved', json(CASE WHEN NEW.resolved THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;

CREATE TRIGGER pr_session_cdc_update
AFTER UPDATE ON pr
WHEN OLD.session_id <> NEW.session_id
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'pr_session_changed',
        json_object(
            'url', NEW.url,
            'fromSession', OLD.session_id,
            'toSession', NEW.session_id),
        NEW.updated_at);
END;

CREATE TRIGGER session_cleanup_facts_cdc_insert
AFTER INSERT ON session_cleanup_facts
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'session_updated',
        json_object('id', NEW.session_id),
        datetime('now'));
END;

CREATE TRIGGER session_cleanup_facts_cdc_update
AFTER UPDATE ON session_cleanup_facts
WHEN OLD.workspace_disposition <> NEW.workspace_disposition
    OR (OLD.runtime_released_at IS NULL) <> (NEW.runtime_released_at IS NULL)
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id, 'session_updated',
        json_object('id', NEW.session_id),
        datetime('now'));
END;

CREATE TRIGGER session_interface_transitions_cdc_insert
AFTER INSERT ON session_interface_transitions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id,
                       'interfaceTransitionId', NEW.id,
                       'interfaceTransitionPhase', NEW.phase,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           NEW.updated_at
    FROM sessions s WHERE s.id = NEW.session_id;
END;

CREATE TRIGGER session_interface_transitions_cdc_update
AFTER UPDATE ON session_interface_transitions
WHEN OLD.phase <> NEW.phase
    OR OLD.error_code <> NEW.error_code
    OR OLD.error_detail <> NEW.error_detail
    OR OLD.notice_acknowledged_at IS NOT NEW.notice_acknowledged_at
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, s.id, 'session_updated',
           json_object('id', s.id, 'sessionId', s.id,
                       'interfaceTransitionId', NEW.id,
                       'interfaceTransitionPhase', NEW.phase,
                       'activity', s.activity_state,
                       'isTerminated', json(CASE WHEN s.is_terminated THEN 'true' ELSE 'false' END)),
           COALESCE(NEW.notice_acknowledged_at, NEW.updated_at)
    FROM sessions s WHERE s.id = NEW.session_id;
END;

CREATE TRIGGER sessions_cdc_insert
AFTER INSERT ON sessions
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_created',
        json_object('id', NEW.id, 'activity', NEW.activity_state, 'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END)),
        NEW.updated_at);
END;

CREATE TRIGGER sessions_cdc_update
AFTER UPDATE ON sessions
WHEN OLD.activity_state <> NEW.activity_state
    OR OLD.is_terminated <> NEW.is_terminated
    OR (OLD.first_signal_at IS NULL AND NEW.first_signal_at IS NOT NULL)
    OR OLD.preview_url <> NEW.preview_url
    OR OLD.preview_revision <> NEW.preview_revision
    OR OLD.display_name <> NEW.display_name
    OR OLD.terminate_on_pr_merge <> NEW.terminate_on_pr_merge
    OR OLD.is_pinned <> NEW.is_pinned
    OR OLD.pinned_at <> NEW.pinned_at
    OR (OLD.pinned_at IS NULL AND NEW.pinned_at IS NOT NULL)
    OR (OLD.pinned_at IS NOT NULL AND NEW.pinned_at IS NULL)
    OR OLD.session_mode <> NEW.session_mode
    OR OLD.auto_inject_review <> NEW.auto_inject_review
    OR OLD.auto_review_enabled <> NEW.auto_review_enabled
    OR OLD.harness <> NEW.harness
    OR OLD.runtime_launch_id <> NEW.runtime_launch_id
    OR OLD.agent_session_id <> NEW.agent_session_id
    OR OLD.native_transcript_path <> NEW.native_transcript_path
    OR OLD.auto_inject_ci <> NEW.auto_inject_ci
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (NEW.project_id, NEW.id, 'session_updated',
        json_object(
            'id', NEW.id,
            'activity', NEW.activity_state,
            'isTerminated', json(CASE WHEN NEW.is_terminated THEN 'true' ELSE 'false' END),
            'terminateOnPrMerge', json(CASE WHEN NEW.terminate_on_pr_merge THEN 'true' ELSE 'false' END),
            'previewUrl', NEW.preview_url,
            'previewRevision', NEW.preview_revision,
            'isPinned', json(CASE WHEN NEW.is_pinned THEN 'true' ELSE 'false' END),
            'mode', NEW.session_mode,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END),
            'autoInjectCI', json(CASE WHEN NEW.auto_inject_ci THEN 'true' ELSE 'false' END),
            'autoReviewEnabled', json(CASE WHEN NEW.auto_review_enabled THEN 'true' ELSE 'false' END)
        ),
        NEW.updated_at);
END;

CREATE TRIGGER usage_bindings_cdc_insert AFTER INSERT ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_bindings_cdc_update AFTER UPDATE ON usage_bindings BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id),
            NEW.session_id, 'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;

CREATE TRIGGER usage_sources_cdc_update AFTER UPDATE ON usage_sources
WHEN OLD.anomaly_count IS NOT NEW.anomaly_count
  OR OLD.last_error_code IS NOT NEW.last_error_code
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    SELECT s.project_id, ub.session_id, 'session_updated', json_object('id', ub.session_id), NEW.updated_at
    FROM usage_bindings ub JOIN sessions s ON s.id = ub.session_id WHERE ub.id = NEW.binding_id;
END;

CREATE TRIGGER review_run_cdc_insert
AFTER INSERT ON review_run
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'review_run_created',
        json_object(
            'id', NEW.id,
            'reviewId', NEW.review_id,
            'sessionId', NEW.session_id,
            'pr', NEW.pr_url,
            'targetSha', NEW.target_sha,
            'status', NEW.status,
            'verdict', NEW.verdict,
            'triggerSource', NEW.trigger_source,
            'githubReviewId', NEW.github_review_id,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END)
        ),
        NEW.created_at);
END;

CREATE TRIGGER review_run_cdc_update
AFTER UPDATE ON review_run
WHEN OLD.status <> NEW.status
    OR OLD.verdict <> NEW.verdict
    OR OLD.body <> NEW.body
    OR OLD.github_review_id <> NEW.github_review_id
    OR OLD.auto_inject_review <> NEW.auto_inject_review
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        (SELECT project_id FROM sessions WHERE id = NEW.session_id),
        NEW.session_id,
        'review_run_updated',
        json_object(
            'id', NEW.id,
            'reviewId', NEW.review_id,
            'sessionId', NEW.session_id,
            'pr', NEW.pr_url,
            'targetSha', NEW.target_sha,
            'status', NEW.status,
            'verdict', NEW.verdict,
            'triggerSource', NEW.trigger_source,
            'githubReviewId', NEW.github_review_id,
            'autoInjectReview', json(CASE WHEN NEW.auto_inject_review THEN 'true' ELSE 'false' END)
        ),
        datetime('now'));
END;
-- +goose StatementEnd
