-- ClearDev S02 STANDARD single-task facts.  This migration deliberately keeps
-- 0106/0107 history readable: NULL in every S02 binding column means legacy.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE sessions
    ADD COLUMN creation_idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions
    ADD COLUMN creation_request_fingerprint TEXT NOT NULL DEFAULT ''
        CHECK (
            (creation_idempotency_key = '' AND creation_request_fingerprint = '')
            OR (creation_idempotency_key <> '' AND creation_request_fingerprint <> '')
        );
ALTER TABLE sessions
    ADD COLUMN creation_claim_token TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions
    ADD COLUMN creation_claim_expires_at TIMESTAMP
        CHECK (
            (creation_claim_token = '' AND creation_claim_expires_at IS NULL)
            OR (creation_claim_token <> '' AND creation_claim_expires_at IS NOT NULL)
        );
ALTER TABLE sessions
    ADD COLUMN permission_mode TEXT NOT NULL DEFAULT ''
        CHECK (permission_mode IN ('', 'default', 'accept-edits', 'auto', 'bypass-permissions'));
CREATE UNIQUE INDEX idx_sessions_creation_idempotency_key
    ON sessions (creation_idempotency_key)
    WHERE creation_idempotency_key <> '';

ALTER TABLE cleardev_work_items
    ADD COLUMN accepted_dispatch_id TEXT REFERENCES cleardev_standard_dispatches(id);
ALTER TABLE cleardev_work_items
    ADD COLUMN dispatch_base_commit_sha TEXT CHECK (
        dispatch_base_commit_sha IS NULL
        OR (length(dispatch_base_commit_sha) = 40 AND dispatch_base_commit_sha NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE cleardev_candidate_commits
    ADD COLUMN dispatch_id TEXT REFERENCES cleardev_standard_dispatches(id);
ALTER TABLE cleardev_candidate_commits
    ADD COLUMN base_commit_sha TEXT CHECK (
        base_commit_sha IS NULL
        OR (length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*')
    );
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN dispatch_id TEXT REFERENCES cleardev_standard_dispatches(id);
ALTER TABLE cleardev_integration_candidates
    ADD COLUMN source_candidate_commit_id TEXT REFERENCES cleardev_candidate_commits(id);
ALTER TABLE cleardev_evidence
    ADD COLUMN candidate_check_run_id TEXT REFERENCES cleardev_candidate_check_runs(id);
ALTER TABLE cleardev_evidence
    ADD COLUMN local_review_id TEXT REFERENCES cleardev_standard_local_reviews(id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE cleardev_standard_role_bindings (
    id                               TEXT PRIMARY KEY,
    development_project_id           TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id           TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    role                             TEXT NOT NULL CHECK (role IN (
        'STEWARD', 'ENGINEERING_PLANNER', 'BUILDER', 'REVIEWER'
    )),
    engineering_plan_id              TEXT,
    dispatch_id                      TEXT,
    work_item_id                     TEXT REFERENCES cleardev_work_items(id),
    candidate_commit_id              TEXT REFERENCES cleardev_candidate_commits(id),
    session_creation_idempotency_key TEXT NOT NULL CHECK (length(trim(session_creation_idempotency_key)) > 0),
    ao_session_id                    TEXT REFERENCES sessions(id),
    status                           TEXT NOT NULL CHECK (status IN ('REQUESTED', 'BOUND', 'FAILED', 'ENDED')),
    reason_code                      TEXT NOT NULL DEFAULT '',
    requested_at                     TIMESTAMP NOT NULL,
    bound_at                         TIMESTAMP,
    ended_at                         TIMESTAMP,
    UNIQUE (session_creation_idempotency_key),
    CHECK (
        (status = 'REQUESTED' AND ao_session_id IS NULL AND bound_at IS NULL AND ended_at IS NULL)
        OR (status = 'BOUND' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NULL)
        OR (status = 'FAILED' AND ao_session_id IS NULL AND ended_at IS NOT NULL)
        OR (status = 'ENDED' AND ao_session_id IS NOT NULL AND bound_at IS NOT NULL AND ended_at IS NOT NULL)
    ),
    CHECK (role <> 'REVIEWER' OR candidate_commit_id IS NOT NULL)
);
CREATE INDEX idx_cleardev_standard_role_bindings_requirement
    ON cleardev_standard_role_bindings (requirement_version_id, role);
CREATE UNIQUE INDEX idx_cleardev_standard_role_bindings_singleton_role
    ON cleardev_standard_role_bindings (requirement_version_id, role)
    WHERE role IN ('STEWARD', 'ENGINEERING_PLANNER', 'BUILDER');
CREATE UNIQUE INDEX idx_cleardev_standard_role_bindings_single_flow
    ON cleardev_standard_role_bindings (development_project_id)
    WHERE role = 'STEWARD';
CREATE UNIQUE INDEX idx_cleardev_standard_role_bindings_reviewer_candidate
    ON cleardev_standard_role_bindings (requirement_version_id, candidate_commit_id)
    WHERE role = 'REVIEWER' AND candidate_commit_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_standard_role_bindings_one_active_reviewer
    ON cleardev_standard_role_bindings (requirement_version_id)
    WHERE role = 'REVIEWER' AND status IN ('REQUESTED', 'BOUND');
CREATE UNIQUE INDEX idx_cleardev_standard_role_bindings_session
    ON cleardev_standard_role_bindings (requirement_version_id, ao_session_id)
    WHERE ao_session_id IS NOT NULL;

CREATE TABLE cleardev_standard_agent_steps (
    id                TEXT PRIMARY KEY,
    role_binding_id   TEXT NOT NULL REFERENCES cleardev_standard_role_bindings(id),
    step_kind         TEXT NOT NULL CHECK (step_kind IN (
        'REQUEST_PLANNING', 'ENGINEERING_PLAN', 'PLAN_REVIEW', 'DISPATCH_REQUEST',
        'BUILDER_RESULT', 'LOCAL_REVIEW', 'STATUS_REPORT'
    )),
    request_id        TEXT NOT NULL CHECK (length(trim(request_id)) > 0),
    client_message_id TEXT NOT NULL UNIQUE CHECK (length(trim(client_message_id)) > 0),
    prompt_sha256     TEXT NOT NULL CHECK (length(prompt_sha256) = 64 AND prompt_sha256 NOT GLOB '*[^0-9a-f]*'),
    send_status       TEXT NOT NULL CHECK (send_status IN ('PENDING', 'SENT', 'SETTLED', 'FAILED')),
    turn_id           TEXT,
    final_message_id  TEXT,
    final_message_text TEXT,
    message_sha256    TEXT CHECK (message_sha256 IS NULL OR (length(message_sha256) = 64 AND message_sha256 NOT GLOB '*[^0-9a-f]*')),
    requested_at      TIMESTAMP NOT NULL,
    sent_at           TIMESTAMP,
    completed_at      TIMESTAMP,
    failed_at         TIMESTAMP,
    reason_code       TEXT NOT NULL DEFAULT '',
    UNIQUE (role_binding_id, step_kind, request_id),
    CHECK (
        (send_status = 'PENDING' AND sent_at IS NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SENT' AND sent_at IS NOT NULL AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NULL)
        OR (send_status = 'SETTLED' AND sent_at IS NOT NULL AND turn_id IS NOT NULL AND final_message_id IS NOT NULL AND final_message_text IS NOT NULL AND length(final_message_text) > 0 AND message_sha256 IS NOT NULL AND completed_at IS NOT NULL AND failed_at IS NULL)
        OR (send_status = 'FAILED' AND turn_id IS NULL AND final_message_id IS NULL AND final_message_text IS NULL AND message_sha256 IS NULL AND completed_at IS NULL AND failed_at IS NOT NULL)
    )
);
CREATE INDEX idx_cleardev_standard_agent_steps_binding
    ON cleardev_standard_agent_steps (role_binding_id, requested_at);

CREATE TABLE cleardev_standard_engineering_plans (
    id                      TEXT PRIMARY KEY,
    requirement_version_id  TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256      TEXT NOT NULL CHECK (length(requirement_sha256) = 64 AND requirement_sha256 NOT GLOB '*[^0-9a-f]*'),
    version                 INTEGER NOT NULL CHECK (version > 0),
    planner_role_binding_id TEXT NOT NULL REFERENCES cleardev_standard_role_bindings(id),
    agent_step_id           TEXT NOT NULL UNIQUE REFERENCES cleardev_standard_agent_steps(id),
    turn_id                 TEXT NOT NULL,
    final_message_id        TEXT NOT NULL,
    plan_json               TEXT NOT NULL CHECK (json_valid(plan_json)),
    plan_sha256             TEXT NOT NULL CHECK (length(plan_sha256) = 64 AND plan_sha256 NOT GLOB '*[^0-9a-f]*'),
    created_at              TIMESTAMP NOT NULL,
    UNIQUE (requirement_version_id, version)
);
CREATE INDEX idx_cleardev_standard_engineering_plans_requirement
    ON cleardev_standard_engineering_plans (requirement_version_id, version DESC);

CREATE TABLE cleardev_standard_plan_reviews (
    id                      TEXT PRIMARY KEY,
    engineering_plan_id     TEXT NOT NULL UNIQUE REFERENCES cleardev_standard_engineering_plans(id),
    steward_role_binding_id TEXT NOT NULL REFERENCES cleardev_standard_role_bindings(id),
    agent_step_id           TEXT NOT NULL UNIQUE REFERENCES cleardev_standard_agent_steps(id),
    turn_id                 TEXT NOT NULL,
    final_message_id        TEXT NOT NULL,
    verdict                 TEXT NOT NULL CHECK (verdict IN ('APPROVED', 'REPLAN', 'NEEDS_HUMAN')),
    reason_code             TEXT NOT NULL CHECK (length(trim(reason_code)) > 0),
    summary                 TEXT NOT NULL CHECK (length(trim(summary)) > 0),
    created_at              TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_standard_dispatches (
    id                              TEXT PRIMARY KEY,
    requirement_version_id          TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    engineering_plan_id             TEXT NOT NULL REFERENCES cleardev_standard_engineering_plans(id),
    plan_review_id                  TEXT NOT NULL REFERENCES cleardev_standard_plan_reviews(id),
    steward_role_binding_id         TEXT NOT NULL REFERENCES cleardev_standard_role_bindings(id),
    agent_step_id                   TEXT NOT NULL UNIQUE REFERENCES cleardev_standard_agent_steps(id),
    mode                            TEXT NOT NULL CHECK (mode = 'STANDARD'),
    preallocated_work_item_id       TEXT NOT NULL UNIQUE,
    expected_task_set_version       INTEGER NOT NULL CHECK (expected_task_set_version >= 0),
    execution_package_json          TEXT NOT NULL CHECK (json_valid(execution_package_json)),
    execution_package_sha256        TEXT NOT NULL CHECK (length(execution_package_sha256) = 64 AND execution_package_sha256 NOT GLOB '*[^0-9a-f]*'),
    builder_session_idempotency_key TEXT NOT NULL UNIQUE CHECK (length(trim(builder_session_idempotency_key)) > 0),
    builder_role_binding_id         TEXT NOT NULL UNIQUE REFERENCES cleardev_standard_role_bindings(id),
    base_commit_sha                 TEXT CHECK (base_commit_sha IS NULL OR (length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*')),
    status                          TEXT NOT NULL CHECK (status IN ('PENDING', 'ACCEPTED', 'REJECTED', 'FAILED')),
    reason_code                     TEXT NOT NULL DEFAULT '',
    requested_at                    TIMESTAMP NOT NULL,
    decided_at                      TIMESTAMP,
    CHECK (
        (status = 'PENDING' AND base_commit_sha IS NULL AND reason_code = '' AND decided_at IS NULL)
        OR (status = 'ACCEPTED' AND base_commit_sha IS NOT NULL AND reason_code = '' AND decided_at IS NOT NULL)
        OR (status IN ('REJECTED', 'FAILED') AND base_commit_sha IS NULL AND reason_code <> '' AND decided_at IS NOT NULL)
    )
);
CREATE INDEX idx_cleardev_standard_dispatches_requirement
    ON cleardev_standard_dispatches (requirement_version_id, requested_at);
CREATE INDEX idx_cleardev_standard_dispatches_pending
    ON cleardev_standard_dispatches (status, requested_at) WHERE status = 'PENDING';

CREATE TABLE cleardev_candidate_check_runs (
    id                   TEXT PRIMARY KEY,
    work_item_id         TEXT NOT NULL REFERENCES cleardev_work_items(id),
    candidate_commit_id  TEXT REFERENCES cleardev_candidate_commits(id),
    dispatch_id          TEXT NOT NULL REFERENCES cleardev_standard_dispatches(id),
    base_commit_sha      TEXT NOT NULL CHECK (length(base_commit_sha) = 40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'),
    candidate_commit_sha TEXT NOT NULL DEFAULT '' CHECK (
        candidate_commit_sha = ''
        OR (length(candidate_commit_sha) = 40 AND candidate_commit_sha NOT GLOB '*[^0-9a-f]*')
    ),
    check_kind           TEXT NOT NULL CHECK (check_kind IN ('SCOPE', 'REQUIRED_CHECK', 'INTEGRATION')),
    check_name           TEXT NOT NULL DEFAULT '',
    check_spec_sha256    TEXT NOT NULL CHECK (length(check_spec_sha256) = 64 AND check_spec_sha256 NOT GLOB '*[^0-9a-f]*'),
    argv_json            TEXT NOT NULL CHECK (json_valid(argv_json) AND json_type(argv_json) = 'array'),
    container_image_id   TEXT,
    exit_code            INTEGER,
    status               TEXT NOT NULL CHECK (status IN ('PENDING', 'SETTLED', 'FAILED')),
    timed_out            BOOLEAN,
    output_summary       TEXT,
    output_sha256        TEXT CHECK (output_sha256 IS NULL OR (length(output_sha256) = 64 AND output_sha256 NOT GLOB '*[^0-9a-f]*')),
    changed_paths_json   TEXT CHECK (changed_paths_json IS NULL OR (json_valid(changed_paths_json) AND json_type(changed_paths_json) = 'array')),
    result               TEXT CHECK (result IN ('PASS', 'FAIL')),
    created_at           TIMESTAMP NOT NULL,
    settled_at           TIMESTAMP,
    reason_code          TEXT NOT NULL DEFAULT '',
    CHECK (
        check_kind <> 'REQUIRED_CHECK'
        OR length(trim(check_name)) > 0
    ),
    CHECK (
        (check_kind = 'SCOPE' AND json_array_length(argv_json) = 0)
        OR (check_kind IN ('REQUIRED_CHECK', 'INTEGRATION') AND json_array_length(argv_json) > 0)
    ),
    CHECK (
        (status = 'PENDING' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'SETTLED' AND timed_out IS NOT NULL AND output_summary IS NOT NULL AND output_sha256 IS NOT NULL AND changed_paths_json IS NOT NULL AND result IS NOT NULL AND settled_at IS NOT NULL
            AND ((result = 'PASS' AND reason_code = '') OR (result = 'FAIL' AND reason_code <> '')) AND (
            (check_kind = 'SCOPE' AND container_image_id IS NULL)
            OR (check_kind IN ('REQUIRED_CHECK', 'INTEGRATION') AND container_image_id IS NOT NULL AND length(trim(container_image_id)) > 0)
        ))
        OR (status = 'FAILED' AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
CREATE INDEX idx_cleardev_candidate_check_runs_candidate
    ON cleardev_candidate_check_runs (candidate_commit_id, check_kind, check_name, created_at);

CREATE TABLE cleardev_standard_local_reviews (
    id                       TEXT PRIMARY KEY,
    candidate_commit_id      TEXT NOT NULL UNIQUE REFERENCES cleardev_candidate_commits(id),
    dispatch_id              TEXT NOT NULL REFERENCES cleardev_standard_dispatches(id),
    review_packet_json       TEXT NOT NULL CHECK (json_valid(review_packet_json)),
    review_packet_sha256     TEXT NOT NULL CHECK (length(review_packet_sha256) = 64 AND review_packet_sha256 NOT GLOB '*[^0-9a-f]*'),
    reviewer_role_binding_id TEXT NOT NULL REFERENCES cleardev_standard_role_bindings(id),
    agent_step_id            TEXT NOT NULL UNIQUE REFERENCES cleardev_standard_agent_steps(id),
    status                   TEXT NOT NULL CHECK (status IN ('PENDING', 'SETTLED', 'FAILED')),
    turn_id                  TEXT,
    final_message_id         TEXT,
    verdict                  TEXT CHECK (verdict IN ('PASS', 'REWORK', 'BLOCKED', 'NEEDS_HUMAN')),
    reason_code              TEXT NOT NULL DEFAULT '',
    summary                  TEXT,
    created_at               TIMESTAMP NOT NULL,
    settled_at               TIMESTAMP,
    CHECK (
        (status = 'PENDING' AND turn_id IS NULL AND final_message_id IS NULL AND verdict IS NULL AND reason_code = '' AND summary IS NULL AND settled_at IS NULL)
        OR (status = 'SETTLED' AND turn_id IS NOT NULL AND final_message_id IS NOT NULL AND verdict IS NOT NULL AND reason_code <> '' AND summary IS NOT NULL AND settled_at IS NOT NULL)
        OR (status = 'FAILED' AND reason_code <> '' AND settled_at IS NOT NULL)
    )
);
CREATE INDEX idx_cleardev_standard_local_reviews_dispatch
    ON cleardev_standard_local_reviews (dispatch_id, created_at);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_standard_role_binding_insert_valid
BEFORE INSERT ON cleardev_standard_role_bindings
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE version.id = NEW.requirement_version_id
      AND project.id = NEW.development_project_id
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
    SELECT RAISE(ABORT, 'cleardev standard role binding does not match requirement or auto Codex Chat session');
END;

CREATE TRIGGER cleardev_standard_role_binding_update_valid
BEFORE UPDATE ON cleardev_standard_role_bindings
WHEN OLD.requirement_version_id IS NOT NEW.requirement_version_id
  OR OLD.development_project_id IS NOT NEW.development_project_id
  OR OLD.role IS NOT NEW.role
  OR OLD.engineering_plan_id IS NOT NEW.engineering_plan_id
  OR OLD.dispatch_id IS NOT NEW.dispatch_id
  OR OLD.work_item_id IS NOT NEW.work_item_id
  OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
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
    SELECT RAISE(ABORT, 'cleardev standard role binding is immutable or invalid');
END;

CREATE TRIGGER cleardev_standard_agent_step_insert_valid
BEFORE INSERT ON cleardev_standard_agent_steps
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_standard_role_bindings
    WHERE id = NEW.role_binding_id AND status = 'BOUND'
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard agent step requires a bound role');
END;

CREATE TRIGGER cleardev_standard_agent_step_update_valid
BEFORE UPDATE ON cleardev_standard_agent_steps
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
    SELECT RAISE(ABORT, 'cleardev standard agent step is immutable or invalid');
END;

CREATE TRIGGER cleardev_standard_plan_insert_valid
BEFORE INSERT ON cleardev_standard_engineering_plans
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_standard_role_bindings AS binding ON binding.requirement_version_id = version.id
    JOIN cleardev_standard_agent_steps AS step ON step.id = NEW.agent_step_id
    WHERE version.id = NEW.requirement_version_id
      AND version.sha256 = NEW.requirement_sha256
      AND binding.id = NEW.planner_role_binding_id
      AND binding.role = 'ENGINEERING_PLANNER'
      AND binding.status = 'BOUND'
      AND step.role_binding_id = binding.id
      AND step.step_kind = 'ENGINEERING_PLAN'
      AND step.send_status = 'SETTLED'
      AND step.turn_id = NEW.turn_id
      AND step.final_message_id = NEW.final_message_id
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard plan must match a settled planner step');
END;

CREATE TRIGGER cleardev_standard_plan_append_only_update
BEFORE UPDATE ON cleardev_standard_engineering_plans
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard plans are append only');
END;
CREATE TRIGGER cleardev_standard_plan_append_only_delete
BEFORE DELETE ON cleardev_standard_engineering_plans
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard plans are append only');
END;

CREATE TRIGGER cleardev_standard_plan_review_insert_valid
BEFORE INSERT ON cleardev_standard_plan_reviews
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_standard_engineering_plans AS plan
    JOIN cleardev_standard_role_bindings AS binding ON binding.requirement_version_id = plan.requirement_version_id
    JOIN cleardev_standard_agent_steps AS step ON step.id = NEW.agent_step_id
    WHERE plan.id = NEW.engineering_plan_id
      AND binding.id = NEW.steward_role_binding_id
      AND binding.role = 'STEWARD'
      AND binding.status = 'BOUND'
      AND step.role_binding_id = binding.id
      AND step.step_kind = 'PLAN_REVIEW'
      AND step.send_status = 'SETTLED'
      AND step.turn_id = NEW.turn_id
      AND step.final_message_id = NEW.final_message_id
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard plan review must match a settled steward step');
END;
CREATE TRIGGER cleardev_standard_plan_reviews_append_only_update
BEFORE UPDATE ON cleardev_standard_plan_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard plan reviews are append only');
END;
CREATE TRIGGER cleardev_standard_plan_reviews_append_only_delete
BEFORE DELETE ON cleardev_standard_plan_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard plan reviews are append only');
END;

CREATE TRIGGER cleardev_standard_dispatch_insert_valid
BEFORE INSERT ON cleardev_standard_dispatches
WHEN NEW.status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_standard_engineering_plans AS plan ON plan.id = NEW.engineering_plan_id
    JOIN cleardev_standard_plan_reviews AS review ON review.id = NEW.plan_review_id
    JOIN cleardev_standard_role_bindings AS steward ON steward.id = NEW.steward_role_binding_id
    JOIN cleardev_standard_role_bindings AS builder ON builder.id = NEW.builder_role_binding_id
    JOIN cleardev_standard_agent_steps AS step ON step.id = NEW.agent_step_id
    WHERE version.id = NEW.requirement_version_id
      AND version.state = 'APPROVED'
      AND plan.requirement_version_id = version.id
      AND review.engineering_plan_id = plan.id
      AND review.verdict = 'APPROVED'
      AND steward.requirement_version_id = version.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
      AND step.role_binding_id = steward.id
      AND step.step_kind = 'DISPATCH_REQUEST'
      AND step.send_status = 'SETTLED'
      AND builder.requirement_version_id = version.id
      AND builder.role = 'BUILDER'
      AND builder.status = 'REQUESTED'
      AND builder.dispatch_id = NEW.id
      AND builder.session_creation_idempotency_key = NEW.builder_session_idempotency_key
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard dispatch lacks an approved current plan or pending Builder binding');
END;

CREATE TRIGGER cleardev_standard_dispatch_update_valid
BEFORE UPDATE ON cleardev_standard_dispatches
WHEN OLD.requirement_version_id IS NOT NEW.requirement_version_id
  OR OLD.engineering_plan_id IS NOT NEW.engineering_plan_id
  OR OLD.plan_review_id IS NOT NEW.plan_review_id
  OR OLD.steward_role_binding_id IS NOT NEW.steward_role_binding_id
  OR OLD.agent_step_id IS NOT NEW.agent_step_id
  OR OLD.mode IS NOT NEW.mode
  OR OLD.preallocated_work_item_id IS NOT NEW.preallocated_work_item_id
  OR OLD.expected_task_set_version IS NOT NEW.expected_task_set_version
  OR OLD.execution_package_json IS NOT NEW.execution_package_json
  OR OLD.execution_package_sha256 IS NOT NEW.execution_package_sha256
  OR OLD.builder_session_idempotency_key IS NOT NEW.builder_session_idempotency_key
  OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id
  OR OLD.requested_at IS NOT NEW.requested_at
  OR OLD.status <> 'PENDING'
  OR NEW.status NOT IN ('ACCEPTED', 'REJECTED', 'FAILED')
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard dispatch request is immutable and settles once');
END;

CREATE TRIGGER cleardev_standard_dispatch_accept_valid
BEFORE UPDATE OF status ON cleardev_standard_dispatches
WHEN NEW.status = 'ACCEPTED'
 AND NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    JOIN cleardev_standard_engineering_plans AS plan ON plan.id = NEW.engineering_plan_id
    JOIN cleardev_standard_plan_reviews AS review ON review.id = NEW.plan_review_id
    JOIN cleardev_standard_role_bindings AS builder ON builder.id = NEW.builder_role_binding_id
    JOIN sessions AS session ON session.id = builder.ao_session_id
    JOIN cleardev_work_items AS task ON task.id = NEW.preallocated_work_item_id
    WHERE version.id = NEW.requirement_version_id
      AND version.state = 'APPROVED'
      AND project.cancelled_at IS NULL
      AND version.task_set_version = NEW.expected_task_set_version + 1
      AND plan.requirement_version_id = version.id
      AND review.engineering_plan_id = plan.id
      AND review.verdict = 'APPROVED'
      AND builder.requirement_version_id = version.id
      AND builder.role = 'BUILDER'
      AND builder.status = 'BOUND'
      AND session.permission_mode = 'auto'
      AND task.development_project_id = project.id
      AND task.contract_version_id = version.id
      AND task.accepted_dispatch_id = NEW.id
      AND task.dispatch_base_commit_sha = NEW.base_commit_sha
      AND task.state = 'RUNNING'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard accepted dispatch is stale or missing its bound task');
END;

CREATE TRIGGER cleardev_standard_work_item_dispatch_binding_valid
BEFORE INSERT ON cleardev_work_items
WHEN (NEW.accepted_dispatch_id IS NULL) <> (NEW.dispatch_base_commit_sha IS NULL)
 OR (
    NEW.accepted_dispatch_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM cleardev_standard_dispatches AS dispatch
        JOIN cleardev_contract_versions AS version ON version.id = dispatch.requirement_version_id
        WHERE dispatch.id = NEW.accepted_dispatch_id
          AND dispatch.status = 'PENDING'
          AND dispatch.preallocated_work_item_id = NEW.id
          AND dispatch.requirement_version_id = NEW.contract_version_id
          AND version.development_project_id = NEW.development_project_id
          AND dispatch.base_commit_sha IS NULL
          AND NEW.dispatch_base_commit_sha IS NOT NULL
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard work item must bind its pending dispatch and fixed base');
END;

-- An S02-bound work item may reach DONE only through the exact accepted
-- dispatch/candidate round. This is deliberately a database guard as well as
-- a store guard: generic transitions and direct SQL cannot skip the immutable
-- check, independent review, and integration evidence chain.
CREATE TRIGGER cleardev_standard_work_item_done_valid
BEFORE UPDATE OF state ON cleardev_work_items
WHEN NEW.state = 'DONE'
 AND OLD.state <> 'DONE'
 AND NEW.accepted_dispatch_id IS NOT NULL
 AND NOT EXISTS (
    SELECT 1
    FROM cleardev_standard_dispatches AS dispatch
    JOIN cleardev_contract_versions AS version ON version.id = NEW.contract_version_id
    JOIN cleardev_development_projects AS project ON project.id = NEW.development_project_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.work_item_id = NEW.id
    WHERE dispatch.id = NEW.accepted_dispatch_id
      AND dispatch.status = 'ACCEPTED'
      AND dispatch.preallocated_work_item_id = NEW.id
      AND dispatch.requirement_version_id = version.id
      AND dispatch.base_commit_sha = NEW.dispatch_base_commit_sha
      AND version.state = 'APPROVED'
      AND version.task_set_version = dispatch.expected_task_set_version + 1
      AND project.cancelled_at IS NULL
      AND candidate.id = (
          SELECT current_candidate.id
          FROM cleardev_candidate_commits AS current_candidate
          WHERE current_candidate.work_item_id = NEW.id
          ORDER BY current_candidate.sequence DESC, current_candidate.id DESC
          LIMIT 1
      )
      AND candidate.dispatch_id = dispatch.id
      AND candidate.base_commit_sha = dispatch.base_commit_sha
      AND EXISTS (
          SELECT 1
          FROM cleardev_candidate_check_runs AS scope
          WHERE scope.work_item_id = NEW.id
            AND scope.candidate_commit_id = candidate.id
            AND scope.dispatch_id = dispatch.id
            AND scope.base_commit_sha = dispatch.base_commit_sha
            AND scope.candidate_commit_sha = candidate.commit_sha
            AND scope.check_kind = 'SCOPE'
            AND scope.check_name = 'scope'
            AND scope.status = 'SETTLED'
            AND scope.result = 'PASS'
      )
      AND NOT EXISTS (
          SELECT 1
          FROM cleardev_required_checks AS required
          WHERE required.work_item_id = NEW.id
            AND NOT EXISTS (
                SELECT 1
                FROM cleardev_candidate_check_runs AS required_run
                WHERE required_run.work_item_id = NEW.id
                  AND required_run.candidate_commit_id = candidate.id
                  AND required_run.dispatch_id = dispatch.id
                  AND required_run.base_commit_sha = dispatch.base_commit_sha
                  AND required_run.candidate_commit_sha = candidate.commit_sha
                  AND required_run.check_kind = 'REQUIRED_CHECK'
                  AND required_run.check_name = required.name
                  AND required_run.status = 'SETTLED'
                  AND required_run.result = 'PASS'
            )
      )
      AND EXISTS (
          SELECT 1
          FROM cleardev_candidate_check_runs AS integration_run
          WHERE integration_run.work_item_id = NEW.id
            AND integration_run.candidate_commit_id = candidate.id
            AND integration_run.dispatch_id = dispatch.id
            AND integration_run.base_commit_sha = dispatch.base_commit_sha
            AND integration_run.candidate_commit_sha = candidate.commit_sha
            AND integration_run.check_kind = 'INTEGRATION'
            AND integration_run.status = 'SETTLED'
            AND integration_run.result = 'PASS'
            AND EXISTS (
                SELECT 1
                FROM cleardev_integration_candidates AS integration_candidate
                JOIN cleardev_evidence AS integration_evidence
                  ON integration_evidence.integration_candidate_id = integration_candidate.id
                WHERE integration_candidate.development_project_id = NEW.development_project_id
                  AND integration_candidate.requirement_version_id = version.id
                  AND integration_candidate.task_set_version = version.task_set_version
                  AND integration_candidate.dispatch_id = dispatch.id
                  AND integration_candidate.source_candidate_commit_id = candidate.id
                  AND integration_candidate.commit_sha = candidate.commit_sha
                  AND integration_evidence.development_project_id = NEW.development_project_id
                  AND integration_evidence.subject_type = 'DEVELOPMENT_PROJECT'
                  AND integration_evidence.subject_id = NEW.development_project_id
                  AND integration_evidence.evidence_kind = 'INTEGRATION'
                  AND integration_evidence.evidence_key = ''
                  AND integration_evidence.result = 'PASS'
                  AND integration_evidence.commit_sha = candidate.commit_sha
                  AND integration_evidence.candidate_check_run_id = integration_run.id
            )
      )
      AND EXISTS (
          SELECT 1
          FROM cleardev_standard_local_reviews AS local_review
          WHERE local_review.candidate_commit_id = candidate.id
            AND local_review.dispatch_id = dispatch.id
            AND local_review.status = 'SETTLED'
            AND local_review.verdict = 'PASS'
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard work item DONE requires its accepted candidate, checks, review, and integration evidence');
END;

CREATE TRIGGER cleardev_standard_candidate_dispatch_binding_valid
BEFORE INSERT ON cleardev_candidate_commits
WHEN (NEW.dispatch_id IS NULL) <> (NEW.base_commit_sha IS NULL)
 OR (
    NEW.dispatch_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM cleardev_standard_dispatches AS dispatch
        JOIN cleardev_standard_role_bindings AS builder ON builder.id = dispatch.builder_role_binding_id
        JOIN cleardev_work_items AS task ON task.id = NEW.work_item_id
        WHERE dispatch.id = NEW.dispatch_id
          AND dispatch.status = 'ACCEPTED'
          AND dispatch.preallocated_work_item_id = task.id
          AND task.accepted_dispatch_id = dispatch.id
          AND task.dispatch_base_commit_sha = NEW.base_commit_sha
          AND dispatch.base_commit_sha = NEW.base_commit_sha
          AND builder.status = 'BOUND'
          AND builder.ao_session_id = NEW.ao_session_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard candidate must use the accepted Builder and fixed base');
END;

CREATE TRIGGER cleardev_standard_integration_candidate_dispatch_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN (NEW.dispatch_id IS NULL) <> (NEW.source_candidate_commit_id IS NULL)
 OR (
    NEW.dispatch_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM cleardev_standard_dispatches AS dispatch
        JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.source_candidate_commit_id
        JOIN cleardev_work_items AS task ON task.id = candidate.work_item_id
        WHERE dispatch.id = NEW.dispatch_id
          AND dispatch.status = 'ACCEPTED'
          AND task.accepted_dispatch_id = dispatch.id
          AND candidate.dispatch_id = dispatch.id
          AND candidate.commit_sha = NEW.commit_sha
          AND NEW.development_project_id = task.development_project_id
          AND NEW.requirement_version_id = dispatch.requirement_version_id
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard integration candidate must reuse its accepted dispatch candidate');
END;

CREATE TRIGGER cleardev_standard_check_run_insert_valid
BEFORE INSERT ON cleardev_candidate_check_runs
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_work_items AS task
    JOIN cleardev_standard_dispatches AS dispatch ON dispatch.id = NEW.dispatch_id
    WHERE task.id = NEW.work_item_id
      AND task.accepted_dispatch_id = dispatch.id
      AND task.dispatch_base_commit_sha = NEW.base_commit_sha
      AND dispatch.status = 'ACCEPTED'
)
 OR (
    NEW.candidate_commit_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM cleardev_candidate_commits AS candidate
        WHERE candidate.id = NEW.candidate_commit_id
          AND candidate.work_item_id = NEW.work_item_id
          AND candidate.dispatch_id = NEW.dispatch_id
          AND candidate.base_commit_sha = NEW.base_commit_sha
          AND candidate.commit_sha = NEW.candidate_commit_sha
    )
 )
 OR (NEW.candidate_commit_id IS NULL AND NEW.candidate_commit_sha <> '')
 OR (
    NEW.check_kind = 'REQUIRED_CHECK'
    AND NOT EXISTS (
        SELECT 1 FROM cleardev_required_checks
        WHERE work_item_id = NEW.work_item_id AND name = NEW.check_name
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev candidate check run must match an accepted candidate and approved check');
END;
CREATE TRIGGER cleardev_candidate_check_runs_append_only_update
BEFORE UPDATE ON cleardev_candidate_check_runs
WHEN OLD.work_item_id IS NOT NEW.work_item_id
  OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
  OR OLD.dispatch_id IS NOT NEW.dispatch_id
  OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
  OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha
  OR OLD.check_kind IS NOT NEW.check_kind
  OR OLD.check_name IS NOT NEW.check_name
  OR OLD.check_spec_sha256 IS NOT NEW.check_spec_sha256
  OR OLD.argv_json IS NOT NEW.argv_json
  OR OLD.created_at IS NOT NEW.created_at
  OR OLD.status <> 'PENDING'
  OR NEW.status NOT IN ('SETTLED', 'FAILED')
BEGIN
    SELECT RAISE(ABORT, 'cleardev candidate check run is immutable or settles once');
END;
CREATE TRIGGER cleardev_candidate_check_runs_append_only_delete
BEFORE DELETE ON cleardev_candidate_check_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev candidate check runs are append only');
END;

CREATE TRIGGER cleardev_standard_local_review_insert_valid
BEFORE INSERT ON cleardev_standard_local_reviews
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_candidate_commits AS candidate
    JOIN cleardev_standard_dispatches AS dispatch ON dispatch.id = NEW.dispatch_id
    JOIN cleardev_standard_role_bindings AS builder ON builder.id = dispatch.builder_role_binding_id
    JOIN cleardev_standard_role_bindings AS reviewer ON reviewer.id = NEW.reviewer_role_binding_id
    JOIN cleardev_standard_agent_steps AS step ON step.id = NEW.agent_step_id
    WHERE candidate.id = NEW.candidate_commit_id
      AND candidate.dispatch_id = dispatch.id
      AND dispatch.status = 'ACCEPTED'
      AND reviewer.requirement_version_id = dispatch.requirement_version_id
      AND reviewer.role = 'REVIEWER'
      AND reviewer.candidate_commit_id = candidate.id
      AND reviewer.status = 'BOUND'
      AND reviewer.ao_session_id <> builder.ao_session_id
      AND step.role_binding_id = reviewer.id
      AND step.step_kind = 'LOCAL_REVIEW'
)
BEGIN
    SELECT RAISE(ABORT, 'cleardev local review must use an independent settled Reviewer');
END;
CREATE TRIGGER cleardev_standard_local_reviews_append_only_update
BEFORE UPDATE ON cleardev_standard_local_reviews
WHEN OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
  OR OLD.dispatch_id IS NOT NEW.dispatch_id
  OR OLD.review_packet_json IS NOT NEW.review_packet_json
  OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256
  OR OLD.reviewer_role_binding_id IS NOT NEW.reviewer_role_binding_id
  OR OLD.agent_step_id IS NOT NEW.agent_step_id
  OR OLD.created_at IS NOT NEW.created_at
  OR OLD.status <> 'PENDING'
  OR NEW.status NOT IN ('SETTLED', 'FAILED')
  OR (
      NEW.status = 'SETTLED'
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_standard_agent_steps AS step
          WHERE step.id = NEW.agent_step_id
            AND step.send_status = 'SETTLED'
            AND step.turn_id = NEW.turn_id
            AND step.final_message_id = NEW.final_message_id
      )
  )
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard local review is immutable or settles once');
END;
CREATE TRIGGER cleardev_standard_local_reviews_append_only_delete
BEFORE DELETE ON cleardev_standard_local_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev standard local reviews are append only');
END;

CREATE TRIGGER cleardev_standard_evidence_source_valid
BEFORE INSERT ON cleardev_evidence
WHEN (
    NEW.candidate_check_run_id IS NOT NULL
    AND NEW.local_review_id IS NOT NULL
)
 OR (
    NEW.candidate_check_run_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM cleardev_candidate_check_runs AS run
        WHERE run.id = NEW.candidate_check_run_id
          AND run.result = NEW.result
          AND (
              (NEW.candidate_commit_id IS NOT NULL
               AND run.candidate_commit_id = NEW.candidate_commit_id
               AND NEW.commit_sha = run.candidate_commit_sha
               AND (
                   (NEW.evidence_kind = 'SCOPE' AND run.check_kind = 'SCOPE')
                   OR (NEW.evidence_kind = 'REQUIRED_CHECK' AND run.check_kind = 'REQUIRED_CHECK' AND NEW.evidence_key = run.check_name)
                   OR (NEW.evidence_kind = 'INTEGRATION' AND run.check_kind = 'INTEGRATION')
               ))
              OR
              (NEW.integration_candidate_id IS NOT NULL
               AND run.check_kind = 'INTEGRATION'
               AND EXISTS (
                   SELECT 1 FROM cleardev_integration_candidates AS integration
                   WHERE integration.id = NEW.integration_candidate_id
                     AND integration.source_candidate_commit_id = run.candidate_commit_id
                     AND integration.commit_sha = run.candidate_commit_sha
               ))
          )
    )
 )
 OR (
    NEW.local_review_id IS NOT NULL
    AND NOT EXISTS (
        SELECT 1
        FROM cleardev_standard_local_reviews AS review
        JOIN cleardev_standard_role_bindings AS reviewer ON reviewer.id = review.reviewer_role_binding_id
        WHERE review.id = NEW.local_review_id
          AND NEW.evidence_kind = 'REVIEW'
          AND NEW.candidate_commit_id = review.candidate_commit_id
          AND NEW.result = CASE WHEN review.verdict = 'PASS' THEN 'PASS' ELSE 'FAIL' END
          AND NEW.source_type = 'REVIEW_ADAPTER'
          AND NEW.source_ao_session_id = reviewer.ao_session_id
    )
 )
 OR (
    NEW.candidate_check_run_id IS NULL
    AND NEW.local_review_id IS NULL
    AND (
        EXISTS (SELECT 1 FROM cleardev_candidate_commits AS candidate WHERE candidate.id = NEW.candidate_commit_id AND candidate.dispatch_id IS NOT NULL)
        OR EXISTS (SELECT 1 FROM cleardev_integration_candidates AS integration WHERE integration.id = NEW.integration_candidate_id AND integration.dispatch_id IS NOT NULL)
    )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev S02 evidence must derive from its check run or local review');
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS cleardev_standard_evidence_source_valid;
DROP TRIGGER IF EXISTS cleardev_standard_local_reviews_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_standard_local_reviews_append_only_update;
DROP TRIGGER IF EXISTS cleardev_standard_local_review_insert_valid;
DROP TRIGGER IF EXISTS cleardev_candidate_check_runs_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_candidate_check_runs_append_only_update;
DROP TRIGGER IF EXISTS cleardev_standard_check_run_insert_valid;
DROP TRIGGER IF EXISTS cleardev_standard_integration_candidate_dispatch_binding_valid;
DROP TRIGGER IF EXISTS cleardev_standard_candidate_dispatch_binding_valid;
DROP TRIGGER IF EXISTS cleardev_standard_work_item_done_valid;
DROP TRIGGER IF EXISTS cleardev_standard_work_item_dispatch_binding_valid;
DROP TRIGGER IF EXISTS cleardev_standard_dispatch_accept_valid;
DROP TRIGGER IF EXISTS cleardev_standard_dispatch_update_valid;
DROP TRIGGER IF EXISTS cleardev_standard_dispatch_insert_valid;
DROP TRIGGER IF EXISTS cleardev_standard_plan_reviews_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_standard_plan_reviews_append_only_update;
DROP TRIGGER IF EXISTS cleardev_standard_plan_review_insert_valid;
DROP TRIGGER IF EXISTS cleardev_standard_plan_append_only_delete;
DROP TRIGGER IF EXISTS cleardev_standard_plan_append_only_update;
DROP TRIGGER IF EXISTS cleardev_standard_plan_insert_valid;
DROP TRIGGER IF EXISTS cleardev_standard_agent_step_update_valid;
DROP TRIGGER IF EXISTS cleardev_standard_agent_step_insert_valid;
DROP TRIGGER IF EXISTS cleardev_standard_role_binding_update_valid;
DROP TRIGGER IF EXISTS cleardev_standard_role_binding_insert_valid;
DROP TABLE IF EXISTS cleardev_standard_local_reviews;
DROP TABLE IF EXISTS cleardev_candidate_check_runs;
DROP TABLE IF EXISTS cleardev_standard_dispatches;
DROP TABLE IF EXISTS cleardev_standard_plan_reviews;
DROP TABLE IF EXISTS cleardev_standard_engineering_plans;
DROP TABLE IF EXISTS cleardev_standard_agent_steps;
DROP TABLE IF EXISTS cleardev_standard_role_bindings;
DROP INDEX IF EXISTS idx_sessions_creation_idempotency_key;
ALTER TABLE cleardev_evidence DROP COLUMN local_review_id;
ALTER TABLE cleardev_evidence DROP COLUMN candidate_check_run_id;
ALTER TABLE cleardev_integration_candidates DROP COLUMN source_candidate_commit_id;
ALTER TABLE cleardev_integration_candidates DROP COLUMN dispatch_id;
ALTER TABLE cleardev_candidate_commits DROP COLUMN base_commit_sha;
ALTER TABLE cleardev_candidate_commits DROP COLUMN dispatch_id;
ALTER TABLE cleardev_work_items DROP COLUMN dispatch_base_commit_sha;
ALTER TABLE cleardev_work_items DROP COLUMN accepted_dispatch_id;
ALTER TABLE sessions DROP COLUMN permission_mode;
ALTER TABLE sessions DROP COLUMN creation_claim_expires_at;
ALTER TABLE sessions DROP COLUMN creation_claim_token;
ALTER TABLE sessions DROP COLUMN creation_request_fingerprint;
ALTER TABLE sessions DROP COLUMN creation_idempotency_key;
-- +goose StatementEnd
