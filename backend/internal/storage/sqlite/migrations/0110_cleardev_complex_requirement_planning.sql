-- ClearDev S04 complex-requirement planning facts. S02 standard-flow tables
-- stay unused: this migration stores original PRD input, steward/planner
-- bindings, compilation, immutable plans, and plan reviews only.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_complex_requirements (
    development_project_id TEXT PRIMARY KEY REFERENCES cleardev_development_projects(id),
    original_prd_text TEXT NOT NULL CHECK (length(trim(original_prd_text)) > 0 AND length(original_prd_text) <= 65536),
    original_prd_sha256 TEXT NOT NULL CHECK (length(original_prd_sha256) = 64 AND original_prd_sha256 NOT GLOB '*[^0-9a-f]*'),
    target_requirement_version_id TEXT NOT NULL UNIQUE CHECK (length(trim(target_requirement_version_id)) > 0),
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_complex_role_bindings (
    id                               TEXT PRIMARY KEY,
    development_project_id           TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    role                             TEXT NOT NULL CHECK (role IN ('STEWARD', 'ENGINEERING_PLANNER')),
    session_creation_idempotency_key TEXT NOT NULL CHECK (length(trim(session_creation_idempotency_key)) > 0),
    ao_session_id                    TEXT REFERENCES sessions(id),
    status                           TEXT NOT NULL CHECK (status IN ('REQUESTED', 'BOUND', 'FAILED', 'ENDED')),
    reason_code                      TEXT NOT NULL DEFAULT '',
    requested_at                     TIMESTAMP NOT NULL,
    bound_at                         TIMESTAMP,
    ended_at                         TIMESTAMP,
    UNIQUE (session_creation_idempotency_key),
    UNIQUE (development_project_id, role),
    CHECK (
        (status = 'REQUESTED' AND ao_session_id IS NULL AND bound_at IS NULL AND ended_at IS NULL)
        OR (status = 'BOUND' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NULL)
        OR (status = 'FAILED' AND ao_session_id IS NULL AND ended_at IS NOT NULL)
        OR (status = 'ENDED' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NOT NULL)
    )
);
CREATE INDEX idx_cleardev_complex_role_bindings_project
    ON cleardev_complex_role_bindings (development_project_id, role);

CREATE UNIQUE INDEX idx_cleardev_complex_role_bindings_session
    ON cleardev_complex_role_bindings (development_project_id, ao_session_id)
    WHERE ao_session_id IS NOT NULL;

CREATE TABLE cleardev_complex_agent_steps (
    id                 TEXT PRIMARY KEY,
    role_binding_id    TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    step_kind          TEXT NOT NULL CHECK (step_kind IN (
        'REQUIREMENT_COMPILATION', 'COMPLEX_ENGINEERING_PLAN', 'COMPLEX_PLAN_REVIEW'
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
CREATE INDEX idx_cleardev_complex_agent_steps_binding
    ON cleardev_complex_agent_steps (role_binding_id, requested_at);

CREATE TABLE cleardev_complex_compilation_requests (
    id                            TEXT PRIMARY KEY,
    development_project_id        TEXT NOT NULL REFERENCES cleardev_complex_requirements(development_project_id),
    agent_step_id                 TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
    clarification_round           INTEGER NOT NULL CHECK (clarification_round IN (0, 1, 2)),
    compilation_context_sha256    TEXT NOT NULL CHECK (length(compilation_context_sha256) = 64 AND compilation_context_sha256 NOT GLOB '*[^0-9a-f]*'),
    additional_round_reason       TEXT NOT NULL DEFAULT '',
    created_at                    TIMESTAMP NOT NULL,
    UNIQUE (development_project_id, clarification_round)
);

CREATE TABLE cleardev_complex_clarification_questions (
    compilation_request_id TEXT NOT NULL REFERENCES cleardev_complex_compilation_requests(id),
    question_key           TEXT NOT NULL CHECK (length(trim(question_key)) > 0),
    text                   TEXT NOT NULL CHECK (length(trim(text)) > 0),
    reason                 TEXT NOT NULL CHECK (length(trim(reason)) > 0),
    requirement_keys_json  TEXT NOT NULL CHECK (json_valid(requirement_keys_json) AND json_type(requirement_keys_json) = 'array'),
    ordinal                INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (compilation_request_id, question_key)
);

CREATE TABLE cleardev_complex_clarification_answers (
    compilation_request_id TEXT NOT NULL REFERENCES cleardev_complex_compilation_requests(id),
    question_key           TEXT NOT NULL,
    text                   TEXT NOT NULL CHECK (length(trim(text)) > 0),
    created_at             TIMESTAMP NOT NULL,
    PRIMARY KEY (compilation_request_id, question_key),
    FOREIGN KEY (compilation_request_id, question_key)
        REFERENCES cleardev_complex_clarification_questions (compilation_request_id, question_key)
);

CREATE TABLE cleardev_complex_compilations (
    id                           TEXT PRIMARY KEY,
    development_project_id       TEXT NOT NULL REFERENCES cleardev_complex_requirements(development_project_id),
    compilation_request_id       TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_compilation_requests(id),
    agent_step_id                TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
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

CREATE TABLE cleardev_complex_id_maps (
    compilation_id TEXT NOT NULL REFERENCES cleardev_complex_compilations(id),
    kind           TEXT NOT NULL CHECK (kind IN ('REQUIREMENT', 'ACCEPTANCE')),
    temporary_key  TEXT NOT NULL CHECK (length(trim(temporary_key)) > 0),
    stable_id      TEXT NOT NULL CHECK (length(trim(stable_id)) > 0),
    ordinal        INTEGER NOT NULL CHECK (ordinal >= 0),
    PRIMARY KEY (compilation_id, kind, temporary_key),
    UNIQUE (compilation_id, kind, stable_id)
);

CREATE TABLE cleardev_complex_engineering_plans (
    id                       TEXT PRIMARY KEY,
    planning_request_id      TEXT NOT NULL UNIQUE CHECK (length(trim(planning_request_id)) > 0),
    development_project_id   TEXT NOT NULL REFERENCES cleardev_complex_requirements(development_project_id),
    requirement_version_id   TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256       TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    compilation_sha256       TEXT NOT NULL CHECK (length(compilation_sha256) = 64 AND compilation_sha256 NOT GLOB '*[^0-9a-f]*'),
    version                  INTEGER NOT NULL CHECK (version > 0),
    planner_role_binding_id  TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    agent_step_id            TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
    turn_id                  TEXT NOT NULL,
    final_message_id         TEXT NOT NULL,
    plan_json                TEXT NOT NULL CHECK (json_valid(plan_json)),
    plan_sha256              TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at               TIMESTAMP NOT NULL,
    UNIQUE (development_project_id, version)
);
CREATE INDEX idx_cleardev_complex_engineering_plans_project
    ON cleardev_complex_engineering_plans (development_project_id, version DESC);

CREATE TABLE cleardev_complex_plan_reviews (
    id                      TEXT PRIMARY KEY,
    plan_id                 TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_engineering_plans(id),
    steward_role_binding_id TEXT NOT NULL REFERENCES cleardev_complex_role_bindings(id),
    agent_step_id           TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_agent_steps(id),
    review_request_id       TEXT NOT NULL UNIQUE CHECK (length(trim(review_request_id)) > 0),
    verdict                 TEXT NOT NULL CHECK (verdict IN ('APPROVED', 'REPLAN', 'NEEDS_HUMAN')),
    reason_code             TEXT NOT NULL CHECK (reason_code IN ('PLAN_ACCEPTABLE', 'REPLAN_REQUIRED', 'HUMAN_DECISION_REQUIRED')),
    summary                 TEXT NOT NULL CHECK (length(trim(summary)) > 0),
    findings_json           TEXT NOT NULL CHECK (json_valid(findings_json) AND json_type(findings_json) = 'array'),
    plan_sha256             TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    turn_id                 TEXT NOT NULL,
    final_message_id        TEXT NOT NULL,
    created_at              TIMESTAMP NOT NULL,
    CHECK (
        (verdict = 'APPROVED' AND reason_code = 'PLAN_ACCEPTABLE')
        OR (verdict = 'REPLAN' AND reason_code = 'REPLAN_REQUIRED')
        OR (verdict = 'NEEDS_HUMAN' AND reason_code = 'HUMAN_DECISION_REQUIRED')
    )
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_requirements_append_only_update
BEFORE UPDATE ON cleardev_complex_requirements
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex requirements are append-only');
END;

CREATE TRIGGER cleardev_complex_requirements_append_only_delete
BEFORE DELETE ON cleardev_complex_requirements
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex requirements are append-only');
END;

CREATE TRIGGER cleardev_complex_role_binding_insert_valid
BEFORE INSERT ON cleardev_complex_role_bindings
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_requirements WHERE development_project_id = NEW.development_project_id
)
 OR (
    NEW.ao_session_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM sessions AS session
        JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
        WHERE session.id = NEW.ao_session_id
          AND project.id = NEW.development_project_id
          AND session.harness = 'codex'
          AND session.session_mode = 'chat'
          AND session.permission_mode = 'auto'
          AND (
              NEW.role = 'STEWARD'
              OR session.creation_idempotency_key = NEW.session_creation_idempotency_key
          )
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex role binding does not match requirement or auto Codex Chat session');
END;

CREATE TRIGGER cleardev_complex_role_binding_update_valid
BEFORE UPDATE ON cleardev_complex_role_bindings
WHEN OLD.development_project_id IS NOT NEW.development_project_id
  OR OLD.role IS NOT NEW.role
  OR OLD.session_creation_idempotency_key IS NOT NEW.session_creation_idempotency_key
  OR OLD.status NOT IN ('REQUESTED', 'BOUND')
  OR (OLD.status = 'REQUESTED' AND NEW.status NOT IN ('BOUND', 'FAILED'))
  OR (OLD.status = 'BOUND' AND NEW.status <> 'ENDED')
  OR (
      NEW.ao_session_id IS NOT NULL
      AND NOT EXISTS (
          SELECT 1
          FROM sessions AS session
          JOIN cleardev_development_projects AS project ON project.ao_project_id = session.project_id
          WHERE session.id = NEW.ao_session_id
            AND project.id = NEW.development_project_id
            AND session.harness = 'codex'
            AND session.session_mode = 'chat'
            AND session.permission_mode = 'auto'
            AND (
                NEW.role = 'STEWARD'
                OR session.creation_idempotency_key = NEW.session_creation_idempotency_key
            )
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex role binding is immutable or invalid');
END;

CREATE TRIGGER cleardev_complex_role_binding_append_only_delete
BEFORE DELETE ON cleardev_complex_role_bindings
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex role bindings are append-only');
END;

CREATE TRIGGER cleardev_complex_agent_step_insert_valid
BEFORE INSERT ON cleardev_complex_agent_steps
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_role_bindings WHERE id = NEW.role_binding_id
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex agent step is missing its role binding');
END;

CREATE TRIGGER cleardev_complex_agent_step_update_valid
BEFORE UPDATE ON cleardev_complex_agent_steps
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
    SELECT RAISE(ABORT, 'cleardev complex agent step is immutable or invalid');
END;

CREATE TRIGGER cleardev_complex_agent_step_append_only_delete
BEFORE DELETE ON cleardev_complex_agent_steps
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex agent steps are append-only');
END;

CREATE TRIGGER cleardev_complex_compilation_requests_append_only_update
BEFORE UPDATE ON cleardev_complex_compilation_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex compilation requests are append-only');
END;

CREATE TRIGGER cleardev_complex_compilation_requests_append_only_delete
BEFORE DELETE ON cleardev_complex_compilation_requests
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex compilation requests are append-only');
END;

CREATE TRIGGER cleardev_complex_questions_append_only_update
BEFORE UPDATE ON cleardev_complex_clarification_questions
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex clarification questions are append-only');
END;

CREATE TRIGGER cleardev_complex_questions_append_only_delete
BEFORE DELETE ON cleardev_complex_clarification_questions
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex clarification questions are append-only');
END;

CREATE TRIGGER cleardev_complex_answers_insert_valid
BEFORE INSERT ON cleardev_complex_clarification_answers
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_clarification_questions
    WHERE compilation_request_id = NEW.compilation_request_id AND question_key = NEW.question_key
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex answer must bind an exact question key');
END;

CREATE TRIGGER cleardev_complex_answers_append_only_update
BEFORE UPDATE ON cleardev_complex_clarification_answers
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex clarification answers are append-only');
END;

CREATE TRIGGER cleardev_complex_answers_append_only_delete
BEFORE DELETE ON cleardev_complex_clarification_answers
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex clarification answers are append-only');
END;

CREATE TRIGGER cleardev_complex_compilations_append_only_update
BEFORE UPDATE ON cleardev_complex_compilations
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex compilations are append-only');
END;

CREATE TRIGGER cleardev_complex_compilations_append_only_delete
BEFORE DELETE ON cleardev_complex_compilations
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex compilations are append-only');
END;

CREATE TRIGGER cleardev_complex_id_maps_append_only_update
BEFORE UPDATE ON cleardev_complex_id_maps
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex id maps are append-only');
END;

CREATE TRIGGER cleardev_complex_id_maps_append_only_delete
BEFORE DELETE ON cleardev_complex_id_maps
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex id maps are append-only');
END;

CREATE TRIGGER cleardev_complex_plan_insert_valid
BEFORE INSERT ON cleardev_complex_engineering_plans
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_role_bindings
    WHERE id = NEW.planner_role_binding_id
      AND development_project_id = NEW.development_project_id
      AND role = 'ENGINEERING_PLANNER'
)
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_contract_versions
    WHERE id = NEW.requirement_version_id
      AND development_project_id = NEW.development_project_id
      AND sha256 = NEW.requirement_sha256
      AND state = 'APPROVED'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex plan must bind a confirmed version and planner');
END;

CREATE TRIGGER cleardev_complex_plan_append_only_update
BEFORE UPDATE ON cleardev_complex_engineering_plans
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex engineering plans are append-only');
END;

CREATE TRIGGER cleardev_complex_plan_append_only_delete
BEFORE DELETE ON cleardev_complex_engineering_plans
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex engineering plans are append-only');
END;

CREATE TRIGGER cleardev_complex_plan_review_insert_valid
BEFORE INSERT ON cleardev_complex_plan_reviews
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_engineering_plans
    WHERE id = NEW.plan_id AND plan_sha256 = NEW.plan_sha256
)
 OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_role_bindings
    WHERE id = NEW.steward_role_binding_id AND role = 'STEWARD'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex plan review must bind the exact plan and steward');
END;

CREATE TRIGGER cleardev_complex_plan_reviews_append_only_update
BEFORE UPDATE ON cleardev_complex_plan_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex plan reviews are append-only');
END;

CREATE TRIGGER cleardev_complex_plan_reviews_append_only_delete
BEFORE DELETE ON cleardev_complex_plan_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex plan reviews are append-only');
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_complex_plan_reviews_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_plan_reviews_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_plan_review_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_plan_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_plan_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_plan_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_id_maps_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_id_maps_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_compilations_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_compilations_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_answers_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_answers_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_answers_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_questions_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_questions_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_compilation_requests_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_compilation_requests_append_only_update;
DROP TRIGGER IF EXISTS cleardev_complex_agent_step_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_agent_step_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_agent_step_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_role_binding_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_role_binding_update_valid;
DROP TRIGGER IF EXISTS cleardev_complex_role_binding_insert_valid;
DROP TRIGGER IF EXISTS cleardev_complex_requirements_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_complex_requirements_append_only_update;
DROP TABLE IF EXISTS cleardev_complex_plan_reviews;
DROP TABLE IF EXISTS cleardev_complex_engineering_plans;
DROP TABLE IF EXISTS cleardev_complex_id_maps;
DROP TABLE IF EXISTS cleardev_complex_compilations;
DROP TABLE IF EXISTS cleardev_complex_clarification_answers;
DROP TABLE IF EXISTS cleardev_complex_clarification_questions;
DROP TABLE IF EXISTS cleardev_complex_compilation_requests;
DROP TABLE IF EXISTS cleardev_complex_agent_steps;
DROP TABLE IF EXISTS cleardev_complex_role_bindings;
DROP TABLE IF EXISTS cleardev_complex_requirements;
-- +goose StatementEnd
