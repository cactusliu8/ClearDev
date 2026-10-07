-- ClearDev S01 v3 keeps the 0106 physical names for compatibility, but moves
-- the authoritative lifecycle to requirement versions and their own task-set
-- counters.  Existing facts are retained; only conflicting legacy states are
-- repaired with append-only migration events.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE cleardev_development_projects
    ADD COLUMN cancelled_at TIMESTAMP;
ALTER TABLE cleardev_development_projects
    ADD COLUMN cancel_reason_code TEXT NOT NULL DEFAULT '';
ALTER TABLE cleardev_development_projects
    ADD COLUMN cancel_reason_text TEXT NOT NULL DEFAULT '';

ALTER TABLE cleardev_contract_versions
    ADD COLUMN task_set_version INTEGER NOT NULL DEFAULT 0
        CHECK (task_set_version >= 0);

-- NULL is intentionally the legacy marker.  Pre-0107 integration candidates
-- remain readable history, but cannot satisfy v3 completion because they lack
-- both bindings.  The replacement insert trigger below requires both values
-- for every newly written candidate.
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN requirement_version_id TEXT REFERENCES cleardev_contract_versions(id);
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN task_set_version INTEGER
        CHECK (task_set_version IS NULL OR task_set_version >= 0);

-- A cancelled legacy container has no cancellation facts.  Prefer the latest
-- accepted cancellation event; deterministic fallbacks preserve the old row
-- even when an old build omitted a reason or timestamp.
UPDATE cleardev_development_projects AS project
SET cancelled_at = COALESCE(
        (SELECT NULLIF(event.created_at, '')
         FROM cleardev_project_events AS event
         WHERE event.development_project_id = project.id
           AND event.action = 'CANCEL_PROJECT'
           AND event.outcome = 'ACCEPTED'
         ORDER BY event.sequence DESC
         LIMIT 1),
        project.updated_at
    ),
    cancel_reason_code = COALESCE(
        NULLIF((SELECT event.reason_code
                FROM cleardev_project_events AS event
                WHERE event.development_project_id = project.id
                  AND event.action = 'CANCEL_PROJECT'
                  AND event.outcome = 'ACCEPTED'
                ORDER BY event.sequence DESC
                LIMIT 1), ''),
        'LEGACY_CANCELLED'
    ),
    cancel_reason_text = COALESCE(
        (SELECT event.reason_text
         FROM cleardev_project_events AS event
         WHERE event.development_project_id = project.id
           AND event.action = 'CANCEL_PROJECT'
           AND event.outcome = 'ACCEPTED'
         ORDER BY event.sequence DESC
         LIMIT 1),
        ''
    )
WHERE project.state = 'CANCELLED';

-- Repair conflicts before partial unique indexes are created.  Record the
-- exact legacy rows first; after the state update a pre-existing rejected or
-- superseded row would otherwise be indistinguishable from a repaired row.
INSERT INTO cleardev_project_events (
    ao_project_id, development_project_id, subject_type, subject_id, action,
    previous_state, target_state, outcome, reason_code, reason_text, source,
    source_ao_session_id, created_at
)
SELECT project.ao_project_id, version.development_project_id,
       'CONTRACT_VERSION', version.id, 'MIGRATE_REQUIREMENT_VERSION',
       'APPROVED', 'SUPERSEDED', 'ACCEPTED', 'LEGACY_MULTIPLE_CONFIRMED',
       'A lower legacy confirmed version was superseded during migration.',
       'MIGRATION', NULL, project.updated_at
FROM cleardev_contract_versions AS version
JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
WHERE version.state = 'APPROVED'
  AND version.id <> (
      SELECT winner.id
      FROM cleardev_contract_versions AS winner
      WHERE winner.development_project_id = version.development_project_id
        AND winner.state = 'APPROVED'
      ORDER BY winner.version DESC
      LIMIT 1
  );

UPDATE cleardev_contract_versions AS version
SET state = 'SUPERSEDED',
    superseded_by_id = (
        SELECT winner.id
        FROM cleardev_contract_versions AS winner
        WHERE winner.development_project_id = version.development_project_id
          AND winner.state = 'APPROVED'
        ORDER BY winner.version DESC
        LIMIT 1
    )
WHERE version.state = 'APPROVED'
  AND version.id <> (
      SELECT winner.id
      FROM cleardev_contract_versions AS winner
      WHERE winner.development_project_id = version.development_project_id
        AND winner.state = 'APPROVED'
      ORDER BY winner.version DESC
      LIMIT 1
  );

INSERT INTO cleardev_project_events (
    ao_project_id, development_project_id, subject_type, subject_id, action,
    previous_state, target_state, outcome, reason_code, reason_text, source,
    source_ao_session_id, created_at
)
SELECT project.ao_project_id, version.development_project_id,
       'CONTRACT_VERSION', version.id, 'MIGRATE_REQUIREMENT_VERSION',
       version.state, 'REJECTED', 'ACCEPTED', 'LEGACY_OPEN_VERSION_RETIRED',
       'A lower legacy open version was retired during migration; this was not a human rejection.',
       'MIGRATION', NULL, project.updated_at
FROM cleardev_contract_versions AS version
JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
WHERE version.state IN ('DRAFT', 'IN_REVIEW')
  AND version.id <> (
      SELECT winner.id
      FROM cleardev_contract_versions AS winner
      WHERE winner.development_project_id = version.development_project_id
        AND winner.state IN ('DRAFT', 'IN_REVIEW')
      ORDER BY winner.version DESC
      LIMIT 1
  );

UPDATE cleardev_contract_versions AS version
SET state = 'REJECTED'
WHERE version.state IN ('DRAFT', 'IN_REVIEW')
  AND version.id <> (
      SELECT winner.id
      FROM cleardev_contract_versions AS winner
      WHERE winner.development_project_id = version.development_project_id
        AND winner.state IN ('DRAFT', 'IN_REVIEW')
      ORDER BY winner.version DESC
      LIMIT 1
  );

INSERT INTO cleardev_project_events (
    ao_project_id, development_project_id, subject_type, subject_id, action,
    previous_state, target_state, outcome, reason_code, reason_text, source,
    source_ao_session_id, created_at
)
SELECT project.ao_project_id, project.id,
       'DEVELOPMENT_PROJECT', project.id, 'MIGRATE_LEGACY_CANCELLATION',
       'CANCELLED', 'CANCELLED', 'ACCEPTED', 'LEGACY_CANCELLED',
       project.cancel_reason_text, 'MIGRATION', NULL, project.cancelled_at
FROM cleardev_development_projects AS project
WHERE project.state = 'CANCELLED';

CREATE UNIQUE INDEX idx_cleardev_contract_versions_current_confirmed
    ON cleardev_contract_versions (development_project_id)
    WHERE state = 'APPROVED';
CREATE UNIQUE INDEX idx_cleardev_contract_versions_current_open
    ON cleardev_contract_versions (development_project_id)
    WHERE state IN ('DRAFT', 'IN_REVIEW');
CREATE INDEX idx_cleardev_work_items_contract_state
    ON cleardev_work_items (contract_version_id, state, created_at);
CREATE INDEX idx_cleardev_integration_candidates_requirement_task_set
    ON cleardev_integration_candidates (
        development_project_id, requirement_version_id, task_set_version, sequence DESC
    );
CREATE INDEX idx_cleardev_project_events_action_outcome_sequence
    ON cleardev_project_events (development_project_id, action, outcome, sequence DESC);

-- 0106 allowed only an already APPROVED replacement.  That prevents the v3
-- transaction order (old current -> SUPERSEDED, then new pending -> APPROVED)
-- once the partial unique index exists.  A higher same-container pending
-- version is safe here because the surrounding store transaction must finish
-- the confirmation or roll back.
DROP TRIGGER IF EXISTS cleardev_contract_superseded_binding_valid;
CREATE TRIGGER cleardev_contract_superseded_binding_valid
BEFORE UPDATE OF superseded_by_id ON cleardev_contract_versions
WHEN NEW.superseded_by_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_contract_versions AS replacement
    WHERE replacement.id = NEW.superseded_by_id
      AND replacement.id <> NEW.id
      AND replacement.development_project_id = NEW.development_project_id
      AND replacement.version > NEW.version
      AND replacement.state IN ('IN_REVIEW', 'APPROVED')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev superseding version must be a higher pending or confirmed version in the same project');
END;

CREATE TRIGGER cleardev_contract_task_set_version_monotonic
BEFORE UPDATE OF task_set_version ON cleardev_contract_versions
WHEN NEW.task_set_version <> OLD.task_set_version + 1
BEGIN
    SELECT RAISE(ABORT, 'cleardev task set version must increase by one');
END;

CREATE TRIGGER cleardev_development_project_cancellation_immutable
BEFORE UPDATE OF cancelled_at, cancel_reason_code, cancel_reason_text
ON cleardev_development_projects
WHEN OLD.cancelled_at IS NOT NULL
 AND (
    NEW.cancelled_at IS NOT OLD.cancelled_at
    OR NEW.cancel_reason_code <> OLD.cancel_reason_code
    OR NEW.cancel_reason_text <> OLD.cancel_reason_text
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev development project cancellation is immutable');
END;

DROP TRIGGER IF EXISTS cleardev_integration_candidate_binding_valid;
CREATE TRIGGER cleardev_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN NEW.requirement_version_id IS NULL
 OR NEW.task_set_version IS NULL
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_development_projects AS project
    JOIN cleardev_contract_versions AS version
      ON version.id = NEW.requirement_version_id
     AND version.development_project_id = project.id
    JOIN sessions AS session ON session.id = NEW.ao_session_id
    WHERE project.id = NEW.development_project_id
      AND session.project_id = project.ao_project_id
      AND version.state = 'APPROVED'
      AND version.task_set_version = NEW.task_set_version
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev integration candidate must bind the current requirement version and task set');
END;
-- +goose StatementEnd

-- +goose Down
-- Best effort only: compatibility repair events and retired legacy states are
-- append-only audit history and deliberately are not removed or reconstructed.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_integration_candidate_binding_valid;
DROP TRIGGER IF EXISTS cleardev_development_project_cancellation_immutable;
DROP TRIGGER IF EXISTS cleardev_contract_task_set_version_monotonic;
DROP TRIGGER IF EXISTS cleardev_contract_superseded_binding_valid;
DROP INDEX IF EXISTS idx_cleardev_project_events_action_outcome_sequence;
DROP INDEX IF EXISTS idx_cleardev_integration_candidates_requirement_task_set;
DROP INDEX IF EXISTS idx_cleardev_work_items_contract_state;
DROP INDEX IF EXISTS idx_cleardev_contract_versions_current_open;
DROP INDEX IF EXISTS idx_cleardev_contract_versions_current_confirmed;

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

ALTER TABLE cleardev_integration_candidates DROP COLUMN task_set_version;
ALTER TABLE cleardev_integration_candidates DROP COLUMN requirement_version_id;
ALTER TABLE cleardev_contract_versions DROP COLUMN task_set_version;
ALTER TABLE cleardev_development_projects DROP COLUMN cancel_reason_text;
ALTER TABLE cleardev_development_projects DROP COLUMN cancel_reason_code;
ALTER TABLE cleardev_development_projects DROP COLUMN cancelled_at;
-- +goose StatementEnd
