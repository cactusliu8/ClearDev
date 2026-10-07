-- Durable supplementary context and bounded retries; historical results remain immutable.
-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
-- +goose StatementEnd
-- +goose StatementBegin
BEGIN IMMEDIATE;
CREATE TABLE cleardev_workflow_recoveries (
 id TEXT PRIMARY KEY NOT NULL,
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 action TEXT NOT NULL CHECK(action IN ('CONTINUE_BUILDER','RETRY_BUILDER_SESSION','RETRY_REVIEW','RETRY_CHECK','RETRY_STAGE')),
 target_id TEXT NOT NULL UNIQUE,
 dispatch_id TEXT NOT NULL DEFAULT '', task_id TEXT NOT NULL DEFAULT '',
 step_id TEXT NOT NULL DEFAULT '', binding_id TEXT NOT NULL DEFAULT '',
 successor_id TEXT NOT NULL UNIQUE, candidate_sha TEXT NOT NULL DEFAULT '',
 provider_conversation_id TEXT NOT NULL DEFAULT '', working_tree_sha256 TEXT NOT NULL DEFAULT '', original_stopped_at TIMESTAMP NOT NULL, original_status TEXT NOT NULL, original_reason TEXT NOT NULL, original_summary TEXT NOT NULL,
 supplement TEXT NOT NULL CHECK(length(trim(supplement)) BETWEEN 1 AND 16000),
 created_at TIMESTAMP NOT NULL
);
CREATE TRIGGER cleardev_workflow_recovery_immutable BEFORE UPDATE ON cleardev_workflow_recoveries BEGIN SELECT RAISE(ABORT,'workflow recovery is immutable'); END;
CREATE TRIGGER cleardev_workflow_recovery_keep_history BEFORE DELETE ON cleardev_workflow_recoveries BEGIN SELECT RAISE(ABORT,'workflow recovery history is immutable'); END;
CREATE TRIGGER cleardev_workflow_recovery_current BEFORE INSERT ON cleardev_workflow_recoveries WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_runs run
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED' AND run.reason_code='' AND run.settled_at IS NULL
 AND run.mode='STANDARD' AND json_extract(run.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
) BEGIN SELECT RAISE(ABORT,'workflow recovery requires a current approved project execution'); END;
CREATE TRIGGER cleardev_workflow_recovery_cdc AFTER INSERT ON cleardev_workflow_recoveries BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT p.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',p.id,'recoveryId',NEW.id),NEW.created_at
 FROM cleardev_development_projects p JOIN cleardev_complex_execution_runs r ON r.development_project_id=p.id WHERE r.id=NEW.execution_run_id;
END;
CREATE TEMP TABLE recovery_backup AS SELECT * FROM cleardev_complex_execution_reviews;
DROP TABLE cleardev_complex_execution_reviews;
CREATE TABLE cleardev_complex_execution_reviews (
    id                       TEXT PRIMARY KEY,
    task_attempt_id          TEXT NOT NULL REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id      TEXT NOT NULL REFERENCES cleardev_candidate_commits(id),
    reviewer_role_binding_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_role_bindings(id),
    agent_step_id            TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
    review_packet_json       TEXT NOT NULL CHECK (json_valid(review_packet_json)),
    review_packet_sha256     TEXT NOT NULL CHECK (length(review_packet_sha256) = 64 AND review_packet_sha256 NOT GLOB '*[^0-9a-f]*'),
    candidate_worktree_path  TEXT NOT NULL CHECK (length(trim(candidate_worktree_path)) > 0),
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
INSERT INTO cleardev_complex_execution_reviews SELECT * FROM recovery_backup;
DROP TABLE recovery_backup;
CREATE TRIGGER cleardev_complex_execution_review_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution reviews are append-only');
END;
CREATE TRIGGER cleardev_complex_execution_review_insert_valid
BEFORE INSERT ON cleardev_complex_execution_reviews
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_role_bindings AS reviewer ON reviewer.id = NEW.reviewer_role_binding_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_agent_steps AS step ON step.id = NEW.agent_step_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    WHERE attempt.id = NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id = attempt.id
      AND reviewer.execution_run_id = run.id AND reviewer.role = 'REVIEWER' AND reviewer.status = 'BOUND'
      AND reviewer.task_mapping_id = task.id AND reviewer.candidate_commit_id = candidate.id
      AND reviewer.workspace_path = NEW.candidate_worktree_path
      AND reviewer.workspace_path <> (SELECT workspace_path FROM cleardev_complex_execution_role_bindings WHERE id = attempt.builder_role_binding_id)
      AND reviewer.ao_session_id <> builder.ao_session_id
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS steward
          WHERE steward.id = run.steward_role_binding_id
            AND steward.ao_session_id = reviewer.ao_session_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS planner
          WHERE planner.development_project_id = run.development_project_id
            AND planner.role = 'ENGINEERING_PLANNER'
            AND planner.ao_session_id = reviewer.ao_session_id
      )
      AND step.role_binding_id = reviewer.id AND step.step_kind = 'LOCAL_REVIEW' AND step.request_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution review must use an independent Reviewer');
END;
CREATE TRIGGER cleardev_complex_execution_review_update_valid BEFORE UPDATE ON cleardev_complex_execution_reviews
WHEN OLD.task_attempt_id IS NOT NEW.task_attempt_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.reviewer_role_binding_id IS NOT NEW.reviewer_role_binding_id
 OR OLD.agent_step_id IS NOT NEW.agent_step_id
 OR OLD.review_packet_json IS NOT NEW.review_packet_json
 OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256
 OR OLD.candidate_worktree_path IS NOT NEW.candidate_worktree_path
 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status <> 'PENDING' OR NEW.status NOT IN ('SETTLED','FAILED')
 OR (NEW.status='SETTLED' AND NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_agent_steps AS step
 WHERE step.id=CASE WHEN EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=NEW.id) THEN NEW.id||':check-results' ELSE NEW.agent_step_id END
 AND step.role_binding_id=NEW.reviewer_role_binding_id AND step.send_status='SETTLED'
 AND step.turn_id=NEW.turn_id AND step.final_message_id=NEW.final_message_id
 ))
 OR (NEW.status='SETTLED' AND EXISTS (
 SELECT 1 FROM cleardev_complex_execution_agent_steps AS original
 WHERE original.id=NEW.agent_step_id AND json_valid(original.final_message_text)
 AND json_extract(original.final_message_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND NOT EXISTS (SELECT 1 FROM cleardev_review_check_requests WHERE review_id=NEW.id)
 ))
 OR (NEW.status='SETTLED' AND EXISTS (
 SELECT 1 FROM cleardev_review_check_requests AS request WHERE request.review_id=NEW.id
 AND ((SELECT count(*) FROM cleardev_review_check_results WHERE review_id=NEW.id)<>json_array_length(request.request_json,'$.checkIds')
 OR (NEW.verdict='PASS' AND EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=NEW.id AND json_extract(result_json,'$.outcome')<>'PASS')))
 ))
BEGIN SELECT RAISE(ABORT,'cleardev complex review requires its exact completed request and check results'); END;
CREATE TEMP TABLE recovery_backup AS SELECT * FROM cleardev_complex_execution_check_runs;
DROP TABLE cleardev_complex_execution_check_runs;
CREATE TABLE "cleardev_complex_execution_check_runs" (
    id                    TEXT PRIMARY KEY,
    check_spec_id         TEXT NOT NULL REFERENCES cleardev_complex_execution_check_specs(id),
    task_attempt_id       TEXT NOT NULL REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id   TEXT NOT NULL REFERENCES cleardev_candidate_commits(id),
    candidate_commit_sha  TEXT NOT NULL CHECK (length(candidate_commit_sha) = 40 AND candidate_commit_sha NOT GLOB '*[^0-9a-f]*'),
    container_image_id    TEXT,
    exit_code             INTEGER,
    status                TEXT NOT NULL CHECK (status IN ('PENDING', 'STARTED', 'SETTLED', 'FAILED')),
    timed_out             BOOLEAN,
    output_summary        TEXT,
    output_sha256         TEXT CHECK (output_sha256 IS NULL OR (length(output_sha256) = 64 AND output_sha256 NOT GLOB '*[^0-9a-f]*')),
    changed_paths_json    TEXT CHECK (changed_paths_json IS NULL OR (json_valid(changed_paths_json) AND json_type(changed_paths_json) = 'array')),
    result                TEXT CHECK (result IN ('PASS', 'FAIL')),
    created_at            TIMESTAMP NOT NULL,
    started_at            TIMESTAMP,
    settled_at            TIMESTAMP,
    reason_code           TEXT NOT NULL DEFAULT '',
    retry_ordinal         INTEGER NOT NULL DEFAULT 0 CHECK (retry_ordinal >= 0),
    UNIQUE (check_spec_id, task_attempt_id, candidate_commit_id, retry_ordinal),
    CHECK (
        (status = 'PENDING' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'STARTED' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'SETTLED' AND timed_out IS NOT NULL AND output_summary IS NOT NULL AND output_sha256 IS NOT NULL AND changed_paths_json IS NOT NULL AND result IS NOT NULL AND started_at IS NOT NULL AND settled_at IS NOT NULL AND ((result = 'PASS' AND reason_code = '') OR (result = 'FAIL' AND reason_code <> '')))
        OR (status = 'FAILED' AND started_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
INSERT INTO cleardev_complex_execution_check_runs SELECT * FROM recovery_backup;
DROP TABLE recovery_backup;
CREATE TRIGGER cleardev_complex_execution_check_run_update_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN OLD.check_spec_id IS NOT NEW.check_spec_id
 OR OLD.task_attempt_id IS NOT NEW.task_attempt_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha
 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.retry_ordinal IS NOT NEW.retry_ordinal
 OR OLD.status NOT IN ('PENDING', 'STARTED')
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('STARTED', 'FAILED'))
 OR (OLD.status = 'STARTED' AND NEW.status NOT IN ('SETTLED', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run is immutable or settles once');
END;
CREATE TRIGGER cleardev_complex_execution_check_run_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_check_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check runs are append-only');
END;
CREATE TRIGGER cleardev_project_check_receipt_settle_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN NEW.status='SETTLED' AND NEW.result IN ('PASS','FAIL') AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
   AND json_valid(NEW.output_summary)
   AND json_extract(NEW.output_summary,'$.schemaVersion') IS 1
   AND json_extract(NEW.output_summary,'$.policy') IS 'PROJECT_CHECK_V1'
   AND json_extract(NEW.output_summary,'$.executionRunId') IS spec.execution_run_id
   AND json_extract(NEW.output_summary,'$.contractSha256') IS admission.contract_sha256
   AND json_extract(NEW.output_summary,'$.checkRunId') IS NEW.id
   AND json_extract(NEW.output_summary,'$.checkId') IS spec.check_name
   AND json_extract(NEW.output_summary,'$.candidateSha') IS NEW.candidate_commit_sha
   AND json(json_extract(NEW.output_summary,'$.argv')) IS json(spec.argv_json)
   AND json_extract(NEW.output_summary,'$.timeoutSeconds') IS spec.timeout_seconds
   AND json_extract(NEW.output_summary,'$.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
   AND json_extract(NEW.output_summary,'$.imageId') IS NEW.container_image_id
   AND json_type(NEW.output_summary,'$.exitCode') IS 'integer'
   AND json_extract(NEW.output_summary,'$.exitCode') IS NEW.exit_code
   AND json_extract(NEW.output_summary,'$.timedOut') IS NEW.timed_out
   AND json_type(NEW.output_summary,'$.nodeVersion') IS 'text'
   AND length(json_extract(NEW.output_summary,'$.nodeVersion'))>0
   AND length(json_extract(NEW.output_summary,'$.sourceManifestId'))=64
   AND length(json_extract(NEW.output_summary,'$.sourceRootTreeOid'))=40
   AND length(json_extract(NEW.output_summary,'$.checkEnvironmentId'))=64
   AND length(json_extract(NEW.output_summary,'$.approvedArgvSha256'))=64
   AND length(json_extract(NEW.output_summary,'$.outputSha256'))=64
   AND json_type(NEW.output_summary,'$.outputSummary') IS 'text'
   AND ((NEW.result='PASS' AND NEW.exit_code=0 AND NEW.timed_out=0
         AND json_extract(NEW.output_summary,'$.outcome') IS 'PASS'
         AND json_type(NEW.output_summary,'$.outputTruncated') IS 'false')
     OR (NEW.result='FAIL' AND ((NEW.timed_out=1 AND json_extract(NEW.output_summary,'$.outcome') IS 'TIMED_OUT')
       OR (NEW.timed_out=0 AND json_extract(NEW.output_summary,'$.outcome') IS 'FAIL'
         AND (NEW.exit_code<>0 OR json_type(NEW.output_summary,'$.outputTruncated') IS 'true')))))
)
BEGIN SELECT RAISE(ABORT,'project checks require actual exact candidate/contract/command/environment receipts'); END;
CREATE TRIGGER cleardev_complex_execution_check_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = NEW.task_attempt_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = attempt.execution_run_id
    WHERE spec.id = NEW.check_spec_id AND spec.execution_run_id = run.id
      AND (spec.task_mapping_id IS NULL OR spec.task_mapping_id = task.id)
      AND (
        (NOT (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL')
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.commit_sha = NEW.candidate_commit_sha)
        OR (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL'
          AND task.plan_task_key = (
              SELECT last_key.value
              FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS last_key
              WHERE batch.execution_run_id = run.id
                AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
              ORDER BY last_key.key DESC LIMIT 1
          )
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.id = (
              SELECT verified.candidate_commit_id
              FROM cleardev_complex_execution_verified_candidates AS verified
              WHERE verified.task_mapping_id = task.id
          )
          AND NEW.candidate_commit_sha = (
              SELECT composition.output_commit_sha
              FROM cleardev_complex_execution_compositions AS composition
              JOIN cleardev_complex_execution_batches AS composition_batch ON composition_batch.id = composition.batch_id
              WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
                AND composition_batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
          ))
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
      AND (
        NEW.retry_ordinal = 0
        OR (
          NEW.retry_ordinal = 1
          AND EXISTS (
              SELECT 1 FROM cleardev_complex_execution_check_runs AS prior
              WHERE prior.check_spec_id = NEW.check_spec_id
                AND prior.task_attempt_id = NEW.task_attempt_id
                AND prior.candidate_commit_id = NEW.candidate_commit_id
                AND prior.retry_ordinal = 0
                AND prior.status = 'FAILED'
                AND prior.reason_code = 'CHECKER_UNAVAILABLE'
          )
        )
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run must match its candidate, attempt, fixed check, and gate');
END;
DROP INDEX idx_cleardev_complex_execution_reviewer_candidate;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_reviewer_candidate ON cleardev_complex_execution_role_bindings(execution_run_id,candidate_commit_id) WHERE role='REVIEWER' AND status IN('REQUESTED','BOUND');
CREATE UNIQUE INDEX cleardev_pending_review_candidate ON cleardev_complex_execution_reviews(candidate_commit_id) WHERE status='PENDING';
CREATE TRIGGER cleardev_review_recovery_required BEFORE INSERT ON cleardev_complex_execution_reviews WHEN EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews WHERE task_attempt_id=NEW.task_attempt_id) AND NOT EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.action='RETRY_REVIEW' AND recovery.successor_id=NEW.reviewer_role_binding_id AND recovery.dispatch_id=NEW.task_attempt_id AND recovery.id=json_extract(NEW.review_packet_json,'$.recovery.id')) BEGIN SELECT RAISE(ABORT,'repeat review requires exact recovery lineage'); END;
DROP INDEX cleardev_final_review_per_candidate;
CREATE UNIQUE INDEX cleardev_final_review_per_candidate ON cleardev_requirement_final_reviews(execution_run_id,candidate_commit_sha,COALESCE(json_extract(review_packet_json,'$.recovery.id'),json_extract(review_packet_json,'$.previousReview.reviewId'),''));

DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_insert_valid BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id
 JOIN cleardev_complex_execution_role_bindings b ON b.id=NEW.builder_role_binding_id JOIN cleardev_complex_execution_agent_steps s ON s.id=NEW.agent_step_id
 WHERE t.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.status='ACCEPTED' AND b.execution_run_id=r.id AND b.role='BUILDER' AND b.status='BOUND'
 AND s.role_binding_id=b.id AND s.step_kind='BUILDER_TASK' AND s.request_id=NEW.id
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.execution_run_id=NEW.execution_run_id AND s.task_id=NEW.task_mapping_id AND s.round=NEW.round))
 OR (NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NEW.round NOT IN(0,1,2,3,4,5,6))
 OR (NEW.batch_id IS NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND mode='STANDARD'))
 OR (NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b JOIN cleardev_complex_execution_runs r ON r.id=b.execution_run_id JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 WHERE b.id=NEW.batch_id AND r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND b.status='RUNNING' AND b.common_base_sha=NEW.base_commit_sha AND t.id=NEW.task_mapping_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key)))
 OR (NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_mapping_id AND a.round=NEW.round-1 AND a.base_commit_sha=NEW.base_commit_sha AND (NEW.batch_id IS NULL OR a.batch_id=NEW.batch_id)
 AND (a.status='REWORK' OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=a.id AND recovery.action='CONTINUE_BUILDER' AND recovery.execution_run_id=NEW.execution_run_id AND a.status IN('BLOCKED','NEEDS_HUMAN'))
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
  OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN')
  OR (a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='DEVELOPMENT')
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_mapping_id AND p.execution_run_id=NEW.execution_run_id)))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d LEFT JOIN cleardev_complex_execution_verified_candidates v ON v.task_mapping_id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND v.id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d JOIN cleardev_complex_execution_task_mappings t ON t.id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b WHERE b.execution_run_id=NEW.execution_run_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key) AND b.ordinal<(SELECT ordinal FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;

DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
     AND NOT(OLD.status IN('BLOCKED','NEEDS_HUMAN') AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action IN('RETRY_REVIEW','RETRY_CHECK') AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action='RETRY_BUILDER_SESSION' AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code='BUILDER_BUDGET_EXHAUSTED' AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at))
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;

DROP TRIGGER cleardev_complex_execution_check_run_insert_valid;
CREATE TRIGGER cleardev_complex_execution_check_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = NEW.task_attempt_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = attempt.execution_run_id
    WHERE spec.id = NEW.check_spec_id AND spec.execution_run_id = run.id
      AND (spec.task_mapping_id IS NULL OR spec.task_mapping_id = task.id)
      AND (
        (NOT (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL')
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.commit_sha = NEW.candidate_commit_sha)
        OR (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL'
          AND task.plan_task_key = (
              SELECT last_key.value
              FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS last_key
              WHERE batch.execution_run_id = run.id
                AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
              ORDER BY last_key.key DESC LIMIT 1
          )
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.id = (
              SELECT verified.candidate_commit_id
              FROM cleardev_complex_execution_verified_candidates AS verified
              WHERE verified.task_mapping_id = task.id
          )
          AND NEW.candidate_commit_sha = (
              SELECT composition.output_commit_sha
              FROM cleardev_complex_execution_compositions AS composition
              JOIN cleardev_complex_execution_batches AS composition_batch ON composition_batch.id = composition.batch_id
              WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
                AND composition_batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
          ))
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
      AND (
        NEW.retry_ordinal = 0 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery JOIN cleardev_complex_execution_check_runs prior ON prior.id=recovery.target_id WHERE recovery.action='RETRY_CHECK' AND recovery.successor_id=NEW.id AND prior.task_attempt_id=NEW.task_attempt_id AND prior.check_spec_id=NEW.check_spec_id AND prior.candidate_commit_id=NEW.candidate_commit_id AND prior.candidate_commit_sha=NEW.candidate_commit_sha AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND NEW.retry_ordinal=prior.retry_ordinal+1)
        OR (
          NEW.retry_ordinal = 1
          AND EXISTS (
              SELECT 1 FROM cleardev_complex_execution_check_runs AS prior
              WHERE prior.check_spec_id = NEW.check_spec_id
                AND prior.task_attempt_id = NEW.task_attempt_id
                AND prior.candidate_commit_id = NEW.candidate_commit_id
                AND prior.retry_ordinal = 0
                AND prior.status = 'FAILED'
                AND prior.reason_code = 'CHECKER_UNAVAILABLE'
          )
        )
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run must match its candidate, attempt, fixed check, and gate');
END;

DROP TRIGGER cleardev_final_review_recheck_insert_guard;
CREATE TRIGGER cleardev_final_review_recheck_insert_guard
BEFORE INSERT ON cleardev_requirement_final_reviews
WHEN NEW.id IS NULL OR (json_extract(NEW.review_packet_json,'$.previousReview') IS NOT NULL AND json_type(NEW.review_packet_json,'$.previousReview')<>'object') OR EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE rowid=NEW.rowid OR id=NEW.id OR (execution_run_id=NEW.execution_run_id AND candidate_commit_sha=NEW.candidate_commit_sha AND COALESCE(json_extract(review_packet_json,'$.recovery.id'),json_extract(review_packet_json,'$.previousReview.reviewId'),'')=COALESCE(json_extract(NEW.review_packet_json,'$.recovery.id'),json_extract(NEW.review_packet_json,'$.previousReview.reviewId'),'')))
 OR (json_extract(NEW.review_packet_json,'$.recovery') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery JOIN cleardev_requirement_final_reviews prior ON prior.id=recovery.target_id WHERE recovery.id=json_extract(NEW.review_packet_json,'$.recovery.id') AND recovery.action='RETRY_STAGE' AND recovery.successor_id=NEW.id AND recovery.execution_run_id=NEW.execution_run_id AND prior.candidate_commit_sha=NEW.candidate_commit_sha AND prior.check_run_ids_json=NEW.check_run_ids_json AND (prior.status='FAILED' OR prior.verdict IN('BLOCKED','NEEDS_HUMAN'))))
 OR (json_extract(NEW.review_packet_json,'$.previousReview') IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM cleardev_requirement_final_reviews old
  JOIN cleardev_human_decision_requests request ON request.id=json_extract(NEW.review_packet_json,'$.previousReview.authorityRequestId')
  JOIN cleardev_complex_execution_runs run ON run.id=old.execution_run_id
  WHERE old.id=json_extract(NEW.review_packet_json,'$.previousReview.reviewId') AND json_extract(old.review_packet_json,'$.previousReview') IS NULL
    AND old.status='SETTLED' AND old.verdict='NEEDS_HUMAN' AND old.result_id IS NOT NULL
    AND old.execution_run_id=NEW.execution_run_id AND old.candidate_commit_sha=NEW.candidate_commit_sha
    AND old.base_commit_sha=NEW.base_commit_sha AND old.source_workspace_path=NEW.source_workspace_path
    AND old.check_run_ids_json=NEW.check_run_ids_json
    AND NEW.id=old.id||':evidence-recheck' AND request.id='final-review-recheck:'||old.execution_run_id
    AND request.development_project_id=old.development_project_id
    AND request.decision_kind='AUTHORIZE_FINAL_REVIEW_EVIDENCE_RECHECK' AND request.status='RESOLVED' AND request.decision='APPROVE'
    AND json_extract(request.binding_json,'$.previousReviewId')=old.id
    AND json_extract(request.binding_json,'$.previousResultId')=old.result_id
    AND json_extract(request.binding_json,'$.previousPacketSha256')=old.review_packet_sha256
    AND json_extract(request.binding_json,'$.candidateSha')=old.candidate_commit_sha
    AND json_extract(request.binding_json,'$.executionRunId')=old.execution_run_id
    AND json_extract(request.binding_json,'$.developmentRequirementId')=old.development_project_id
    AND json_extract(old.review_packet_json,'$.attemptEvidence') IS NULL
    AND json_extract(NEW.review_packet_json,'$.previousReview.reviewId')=old.id
    AND json_extract(NEW.review_packet_json,'$.previousReview.resultId')=old.result_id
    AND json_extract(NEW.review_packet_json,'$.previousReview.packetSha256')=old.review_packet_sha256
    AND json_extract(NEW.review_packet_json,'$.previousReview.authorityRequestId')=request.id
    AND json_extract(NEW.review_packet_json,'$.attemptEvidence.policy')='MAIL_ATTEMPTS_V1'
    AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
    AND run.mode='STANDARD'
    AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=old.requirement_version_id)
    AND NOT EXISTS (SELECT 1 FROM cleardev_human_decision_effects WHERE request_id=request.id)
 ))
BEGIN SELECT RAISE(ABORT,'final review evidence recheck requires one bound native authorization'); END;

DROP TRIGGER cleardev_fixed_reviewer_authorization;
CREATE TRIGGER cleardev_fixed_reviewer_authorization BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NEW.role='REVIEWER' AND NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_fixed_recovery_claims AS claim ON claim.request_id=request.id
 WHERE request.execution_run_id=NEW.execution_run_id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
 AND NEW.id=claim.operation_id||':reviewer' AND NEW.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND NEW.continuation_of_role_binding_id=json_extract(request.request_json,'$.roleBindingId')
 AND NEW.task_mapping_id=json_extract(request.request_json,'$.taskId') AND NEW.candidate_commit_id=json_extract(request.request_json,'$.candidateId')
) AND NOT EXISTS (
 SELECT 1 FROM cleardev_mail_reviewer_rework_sources AS source
 WHERE NEW.status='REQUESTED' AND NEW.execution_run_id=source.execution_run_id
 AND NEW.continuation_of_role_binding_id=source.prior_binding_id AND NEW.task_mapping_id=source.task_mapping_id
 AND NEW.candidate_commit_id=source.candidate_id
 AND NEW.session_creation_idempotency_key='cleardev-mail-review-rework:'||source.candidate_id
) AND NOT EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.action='RETRY_REVIEW' AND recovery.successor_id=NEW.id AND recovery.binding_id=NEW.continuation_of_role_binding_id AND recovery.execution_run_id=NEW.execution_run_id AND recovery.task_id=NEW.task_mapping_id AND NEW.status='REQUESTED' AND NEW.session_creation_idempotency_key='cleardev-workflow-review:'||recovery.id) BEGIN SELECT RAISE(ABORT,'replacement Reviewer requires exact fixed recovery or mail rework authorization'); END;

COMMIT;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA foreign_keys=ON;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
PRAGMA foreign_keys=OFF;
-- +goose StatementEnd
-- +goose StatementBegin
BEGIN IMMEDIATE;
CREATE TEMP TABLE recovery_down_guard(ok INTEGER CHECK(ok=1)); INSERT INTO recovery_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_workflow_recoveries) OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs) THEN 0 ELSE 1 END; DROP TABLE recovery_down_guard;
DROP TRIGGER cleardev_review_recovery_required; DROP INDEX cleardev_pending_review_candidate;
CREATE TEMP TABLE recovery_backup AS SELECT * FROM cleardev_complex_execution_reviews;
DROP TABLE cleardev_complex_execution_reviews;
CREATE TABLE cleardev_complex_execution_reviews (
    id                       TEXT PRIMARY KEY,
    task_attempt_id          TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id      TEXT NOT NULL UNIQUE REFERENCES cleardev_candidate_commits(id),
    reviewer_role_binding_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_role_bindings(id),
    agent_step_id            TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
    review_packet_json       TEXT NOT NULL CHECK (json_valid(review_packet_json)),
    review_packet_sha256     TEXT NOT NULL CHECK (length(review_packet_sha256) = 64 AND review_packet_sha256 NOT GLOB '*[^0-9a-f]*'),
    candidate_worktree_path  TEXT NOT NULL CHECK (length(trim(candidate_worktree_path)) > 0),
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
INSERT INTO cleardev_complex_execution_reviews SELECT * FROM recovery_backup;
DROP TABLE recovery_backup;
CREATE TRIGGER cleardev_complex_execution_review_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_reviews
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution reviews are append-only');
END;
CREATE TRIGGER cleardev_complex_execution_review_insert_valid
BEFORE INSERT ON cleardev_complex_execution_reviews
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_role_bindings AS reviewer ON reviewer.id = NEW.reviewer_role_binding_id
    JOIN cleardev_complex_execution_role_bindings AS builder ON builder.id = attempt.builder_role_binding_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_complex_execution_agent_steps AS step ON step.id = NEW.agent_step_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    WHERE attempt.id = NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id = attempt.id
      AND reviewer.execution_run_id = run.id AND reviewer.role = 'REVIEWER' AND reviewer.status = 'BOUND'
      AND reviewer.task_mapping_id = task.id AND reviewer.candidate_commit_id = candidate.id
      AND reviewer.workspace_path = NEW.candidate_worktree_path
      AND reviewer.workspace_path <> (SELECT workspace_path FROM cleardev_complex_execution_role_bindings WHERE id = attempt.builder_role_binding_id)
      AND reviewer.ao_session_id <> builder.ao_session_id
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS steward
          WHERE steward.id = run.steward_role_binding_id
            AND steward.ao_session_id = reviewer.ao_session_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM cleardev_complex_role_bindings AS planner
          WHERE planner.development_project_id = run.development_project_id
            AND planner.role = 'ENGINEERING_PLANNER'
            AND planner.ao_session_id = reviewer.ao_session_id
      )
      AND step.role_binding_id = reviewer.id AND step.step_kind = 'LOCAL_REVIEW' AND step.request_id = NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution review must use an independent Reviewer');
END;
CREATE TRIGGER cleardev_complex_execution_review_update_valid BEFORE UPDATE ON cleardev_complex_execution_reviews
WHEN OLD.task_attempt_id IS NOT NEW.task_attempt_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.reviewer_role_binding_id IS NOT NEW.reviewer_role_binding_id
 OR OLD.agent_step_id IS NOT NEW.agent_step_id
 OR OLD.review_packet_json IS NOT NEW.review_packet_json
 OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256
 OR OLD.candidate_worktree_path IS NOT NEW.candidate_worktree_path
 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status <> 'PENDING' OR NEW.status NOT IN ('SETTLED','FAILED')
 OR (NEW.status='SETTLED' AND NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_agent_steps AS step
 WHERE step.id=CASE WHEN EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=NEW.id) THEN NEW.id||':check-results' ELSE NEW.agent_step_id END
 AND step.role_binding_id=NEW.reviewer_role_binding_id AND step.send_status='SETTLED'
 AND step.turn_id=NEW.turn_id AND step.final_message_id=NEW.final_message_id
 ))
 OR (NEW.status='SETTLED' AND EXISTS (
 SELECT 1 FROM cleardev_complex_execution_agent_steps AS original
 WHERE original.id=NEW.agent_step_id AND json_valid(original.final_message_text)
 AND json_extract(original.final_message_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND NOT EXISTS (SELECT 1 FROM cleardev_review_check_requests WHERE review_id=NEW.id)
 ))
 OR (NEW.status='SETTLED' AND EXISTS (
 SELECT 1 FROM cleardev_review_check_requests AS request WHERE request.review_id=NEW.id
 AND ((SELECT count(*) FROM cleardev_review_check_results WHERE review_id=NEW.id)<>json_array_length(request.request_json,'$.checkIds')
 OR (NEW.verdict='PASS' AND EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=NEW.id AND json_extract(result_json,'$.outcome')<>'PASS')))
 ))
BEGIN SELECT RAISE(ABORT,'cleardev complex review requires its exact completed request and check results'); END;
CREATE TEMP TABLE recovery_backup AS SELECT * FROM cleardev_complex_execution_check_runs;
DROP TABLE cleardev_complex_execution_check_runs;
CREATE TABLE "cleardev_complex_execution_check_runs" (
    id                    TEXT PRIMARY KEY,
    check_spec_id         TEXT NOT NULL REFERENCES cleardev_complex_execution_check_specs(id),
    task_attempt_id       TEXT NOT NULL REFERENCES cleardev_complex_execution_task_attempts(id),
    candidate_commit_id   TEXT NOT NULL REFERENCES cleardev_candidate_commits(id),
    candidate_commit_sha  TEXT NOT NULL CHECK (length(candidate_commit_sha) = 40 AND candidate_commit_sha NOT GLOB '*[^0-9a-f]*'),
    container_image_id    TEXT,
    exit_code             INTEGER,
    status                TEXT NOT NULL CHECK (status IN ('PENDING', 'STARTED', 'SETTLED', 'FAILED')),
    timed_out             BOOLEAN,
    output_summary        TEXT,
    output_sha256         TEXT CHECK (output_sha256 IS NULL OR (length(output_sha256) = 64 AND output_sha256 NOT GLOB '*[^0-9a-f]*')),
    changed_paths_json    TEXT CHECK (changed_paths_json IS NULL OR (json_valid(changed_paths_json) AND json_type(changed_paths_json) = 'array')),
    result                TEXT CHECK (result IN ('PASS', 'FAIL')),
    created_at            TIMESTAMP NOT NULL,
    started_at            TIMESTAMP,
    settled_at            TIMESTAMP,
    reason_code           TEXT NOT NULL DEFAULT '',
    retry_ordinal         INTEGER NOT NULL DEFAULT 0 CHECK (retry_ordinal IN (0, 1)),
    UNIQUE (check_spec_id, task_attempt_id, candidate_commit_id, retry_ordinal),
    CHECK (
        (status = 'PENDING' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'STARTED' AND container_image_id IS NULL AND exit_code IS NULL AND timed_out IS NULL AND output_summary IS NULL AND output_sha256 IS NULL AND changed_paths_json IS NULL AND result IS NULL AND started_at IS NOT NULL AND settled_at IS NULL AND reason_code = '')
        OR (status = 'SETTLED' AND timed_out IS NOT NULL AND output_summary IS NOT NULL AND output_sha256 IS NOT NULL AND changed_paths_json IS NOT NULL AND result IS NOT NULL AND started_at IS NOT NULL AND settled_at IS NOT NULL AND ((result = 'PASS' AND reason_code = '') OR (result = 'FAIL' AND reason_code <> '')))
        OR (status = 'FAILED' AND started_at IS NOT NULL AND settled_at IS NOT NULL AND reason_code <> '')
    )
);
INSERT INTO cleardev_complex_execution_check_runs SELECT * FROM recovery_backup;
DROP TABLE recovery_backup;
CREATE TRIGGER cleardev_complex_execution_check_run_update_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN OLD.check_spec_id IS NOT NEW.check_spec_id
 OR OLD.task_attempt_id IS NOT NEW.task_attempt_id
 OR OLD.candidate_commit_id IS NOT NEW.candidate_commit_id
 OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha
 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.retry_ordinal IS NOT NEW.retry_ordinal
 OR OLD.status NOT IN ('PENDING', 'STARTED')
 OR (OLD.status = 'PENDING' AND NEW.status NOT IN ('STARTED', 'FAILED'))
 OR (OLD.status = 'STARTED' AND NEW.status NOT IN ('SETTLED', 'FAILED'))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run is immutable or settles once');
END;
CREATE TRIGGER cleardev_complex_execution_check_run_append_only_delete
BEFORE DELETE ON cleardev_complex_execution_check_runs
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check runs are append-only');
END;
CREATE TRIGGER cleardev_project_check_receipt_settle_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN NEW.status='SETTLED' AND NEW.result IN ('PASS','FAIL') AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
   AND json_valid(NEW.output_summary)
   AND json_extract(NEW.output_summary,'$.schemaVersion') IS 1
   AND json_extract(NEW.output_summary,'$.policy') IS 'PROJECT_CHECK_V1'
   AND json_extract(NEW.output_summary,'$.executionRunId') IS spec.execution_run_id
   AND json_extract(NEW.output_summary,'$.contractSha256') IS admission.contract_sha256
   AND json_extract(NEW.output_summary,'$.checkRunId') IS NEW.id
   AND json_extract(NEW.output_summary,'$.checkId') IS spec.check_name
   AND json_extract(NEW.output_summary,'$.candidateSha') IS NEW.candidate_commit_sha
   AND json(json_extract(NEW.output_summary,'$.argv')) IS json(spec.argv_json)
   AND json_extract(NEW.output_summary,'$.timeoutSeconds') IS spec.timeout_seconds
   AND json_extract(NEW.output_summary,'$.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
   AND json_extract(NEW.output_summary,'$.imageId') IS NEW.container_image_id
   AND json_type(NEW.output_summary,'$.exitCode') IS 'integer'
   AND json_extract(NEW.output_summary,'$.exitCode') IS NEW.exit_code
   AND json_extract(NEW.output_summary,'$.timedOut') IS NEW.timed_out
   AND json_type(NEW.output_summary,'$.nodeVersion') IS 'text'
   AND length(json_extract(NEW.output_summary,'$.nodeVersion'))>0
   AND length(json_extract(NEW.output_summary,'$.sourceManifestId'))=64
   AND length(json_extract(NEW.output_summary,'$.sourceRootTreeOid'))=40
   AND length(json_extract(NEW.output_summary,'$.checkEnvironmentId'))=64
   AND length(json_extract(NEW.output_summary,'$.approvedArgvSha256'))=64
   AND length(json_extract(NEW.output_summary,'$.outputSha256'))=64
   AND json_type(NEW.output_summary,'$.outputSummary') IS 'text'
   AND ((NEW.result='PASS' AND NEW.exit_code=0 AND NEW.timed_out=0
         AND json_extract(NEW.output_summary,'$.outcome') IS 'PASS'
         AND json_type(NEW.output_summary,'$.outputTruncated') IS 'false')
     OR (NEW.result='FAIL' AND ((NEW.timed_out=1 AND json_extract(NEW.output_summary,'$.outcome') IS 'TIMED_OUT')
       OR (NEW.timed_out=0 AND json_extract(NEW.output_summary,'$.outcome') IS 'FAIL'
         AND (NEW.exit_code<>0 OR json_type(NEW.output_summary,'$.outputTruncated') IS 'true')))))
)
BEGIN SELECT RAISE(ABORT,'project checks require actual exact candidate/contract/command/environment receipts'); END;
CREATE TRIGGER cleardev_complex_execution_check_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = NEW.task_attempt_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = attempt.execution_run_id
    WHERE spec.id = NEW.check_spec_id AND spec.execution_run_id = run.id
      AND (spec.task_mapping_id IS NULL OR spec.task_mapping_id = task.id)
      AND (
        (NOT (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL')
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.commit_sha = NEW.candidate_commit_sha)
        OR (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL'
          AND task.plan_task_key = (
              SELECT last_key.value
              FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS last_key
              WHERE batch.execution_run_id = run.id
                AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
              ORDER BY last_key.key DESC LIMIT 1
          )
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.id = (
              SELECT verified.candidate_commit_id
              FROM cleardev_complex_execution_verified_candidates AS verified
              WHERE verified.task_mapping_id = task.id
          )
          AND NEW.candidate_commit_sha = (
              SELECT composition.output_commit_sha
              FROM cleardev_complex_execution_compositions AS composition
              JOIN cleardev_complex_execution_batches AS composition_batch ON composition_batch.id = composition.batch_id
              WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
                AND composition_batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
          ))
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
      AND (
        NEW.retry_ordinal = 0
        OR (
          NEW.retry_ordinal = 1
          AND EXISTS (
              SELECT 1 FROM cleardev_complex_execution_check_runs AS prior
              WHERE prior.check_spec_id = NEW.check_spec_id
                AND prior.task_attempt_id = NEW.task_attempt_id
                AND prior.candidate_commit_id = NEW.candidate_commit_id
                AND prior.retry_ordinal = 0
                AND prior.status = 'FAILED'
                AND prior.reason_code = 'CHECKER_UNAVAILABLE'
          )
        )
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run must match its candidate, attempt, fixed check, and gate');
END;

DROP TRIGGER cleardev_complex_execution_attempt_insert_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_insert_valid BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id
 JOIN cleardev_complex_execution_role_bindings b ON b.id=NEW.builder_role_binding_id JOIN cleardev_complex_execution_agent_steps s ON s.id=NEW.agent_step_id
 WHERE t.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.status='ACCEPTED' AND b.execution_run_id=r.id AND b.role='BUILDER' AND b.status='BOUND'
 AND s.role_binding_id=b.id AND s.step_kind='BUILDER_TASK' AND s.request_id=NEW.id
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=r.requirement_version_id AND status='ACTIVE'))
 OR (EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.execution_run_id=NEW.execution_run_id AND s.task_id=NEW.task_mapping_id AND s.round=NEW.round))
 OR (NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NEW.round NOT IN(0,1,2,3,4,5,6))
 OR (NEW.batch_id IS NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND mode='STANDARD'))
 OR (NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b JOIN cleardev_complex_execution_runs r ON r.id=b.execution_run_id JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 WHERE b.id=NEW.batch_id AND r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND b.status='RUNNING' AND b.common_base_sha=NEW.base_commit_sha AND t.id=NEW.task_mapping_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key)))
 OR (NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_mapping_id AND a.round=NEW.round-1 AND a.base_commit_sha=NEW.base_commit_sha AND (NEW.batch_id IS NULL OR a.batch_id=NEW.batch_id)
 AND (a.status='REWORK'
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
  OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN')
  OR (a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='DEVELOPMENT')
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_mapping_id AND p.execution_run_id=NEW.execution_run_id)))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d LEFT JOIN cleardev_complex_execution_verified_candidates v ON v.task_mapping_id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND v.id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d JOIN cleardev_complex_execution_task_mappings t ON t.id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b WHERE b.execution_run_id=NEW.execution_run_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key) AND b.ordinal<(SELECT ordinal FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;

DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
     AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code='BUILDER_BUDGET_EXHAUSTED' AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at))
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;

DROP TRIGGER cleardev_complex_execution_check_run_insert_valid;
CREATE TRIGGER cleardev_complex_execution_check_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NOT EXISTS (
    SELECT 1
    FROM cleardev_complex_execution_check_specs AS spec
    JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = NEW.task_attempt_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = attempt.execution_run_id
    WHERE spec.id = NEW.check_spec_id AND spec.execution_run_id = run.id
      AND (spec.task_mapping_id IS NULL OR spec.task_mapping_id = task.id)
      AND (
        (NOT (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL')
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.commit_sha = NEW.candidate_commit_sha)
        OR (spec.check_kind = 'INTEGRATION' AND run.mode = 'PARALLEL'
          AND task.plan_task_key = (
              SELECT last_key.value
              FROM cleardev_complex_execution_batches AS batch, json_each(batch.task_keys_json) AS last_key
              WHERE batch.execution_run_id = run.id
                AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
              ORDER BY last_key.key DESC LIMIT 1
          )
          AND candidate.work_item_id = task.work_item_id
          AND candidate.complex_execution_task_attempt_id = attempt.id
          AND candidate.id = (
              SELECT verified.candidate_commit_id
              FROM cleardev_complex_execution_verified_candidates AS verified
              WHERE verified.task_mapping_id = task.id
          )
          AND NEW.candidate_commit_sha = (
              SELECT composition.output_commit_sha
              FROM cleardev_complex_execution_compositions AS composition
              JOIN cleardev_complex_execution_batches AS composition_batch ON composition_batch.id = composition.batch_id
              WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
                AND composition_batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
          ))
      )
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
      AND (
        NEW.retry_ordinal = 0
        OR (
          NEW.retry_ordinal = 1
          AND EXISTS (
              SELECT 1 FROM cleardev_complex_execution_check_runs AS prior
              WHERE prior.check_spec_id = NEW.check_spec_id
                AND prior.task_attempt_id = NEW.task_attempt_id
                AND prior.candidate_commit_id = NEW.candidate_commit_id
                AND prior.retry_ordinal = 0
                AND prior.status = 'FAILED'
                AND prior.reason_code = 'CHECKER_UNAVAILABLE'
          )
        )
      )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution check run must match its candidate, attempt, fixed check, and gate');
END;

DROP TRIGGER cleardev_final_review_recheck_insert_guard;
CREATE TRIGGER cleardev_final_review_recheck_insert_guard
BEFORE INSERT ON cleardev_requirement_final_reviews
WHEN NEW.id IS NULL OR (json_extract(NEW.review_packet_json,'$.previousReview') IS NOT NULL AND json_type(NEW.review_packet_json,'$.previousReview')<>'object') OR EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE rowid=NEW.rowid OR id=NEW.id OR (execution_run_id=NEW.execution_run_id AND COALESCE(json_extract(review_packet_json,'$.previousReview.reviewId'),'')=COALESCE(json_extract(NEW.review_packet_json,'$.previousReview.reviewId'),'')))
 OR (json_extract(NEW.review_packet_json,'$.previousReview') IS NOT NULL AND NOT EXISTS (
  SELECT 1 FROM cleardev_requirement_final_reviews old
  JOIN cleardev_human_decision_requests request ON request.id=json_extract(NEW.review_packet_json,'$.previousReview.authorityRequestId')
  JOIN cleardev_complex_execution_runs run ON run.id=old.execution_run_id
  WHERE old.id=json_extract(NEW.review_packet_json,'$.previousReview.reviewId') AND json_extract(old.review_packet_json,'$.previousReview') IS NULL
    AND old.status='SETTLED' AND old.verdict='NEEDS_HUMAN' AND old.result_id IS NOT NULL
    AND old.execution_run_id=NEW.execution_run_id AND old.candidate_commit_sha=NEW.candidate_commit_sha
    AND old.base_commit_sha=NEW.base_commit_sha AND old.source_workspace_path=NEW.source_workspace_path
    AND old.check_run_ids_json=NEW.check_run_ids_json
    AND NEW.id=old.id||':evidence-recheck' AND request.id='final-review-recheck:'||old.execution_run_id
    AND request.development_project_id=old.development_project_id
    AND request.decision_kind='AUTHORIZE_FINAL_REVIEW_EVIDENCE_RECHECK' AND request.status='RESOLVED' AND request.decision='APPROVE'
    AND json_extract(request.binding_json,'$.previousReviewId')=old.id
    AND json_extract(request.binding_json,'$.previousResultId')=old.result_id
    AND json_extract(request.binding_json,'$.previousPacketSha256')=old.review_packet_sha256
    AND json_extract(request.binding_json,'$.candidateSha')=old.candidate_commit_sha
    AND json_extract(request.binding_json,'$.executionRunId')=old.execution_run_id
    AND json_extract(request.binding_json,'$.developmentRequirementId')=old.development_project_id
    AND json_extract(old.review_packet_json,'$.attemptEvidence') IS NULL
    AND json_extract(NEW.review_packet_json,'$.previousReview.reviewId')=old.id
    AND json_extract(NEW.review_packet_json,'$.previousReview.resultId')=old.result_id
    AND json_extract(NEW.review_packet_json,'$.previousReview.packetSha256')=old.review_packet_sha256
    AND json_extract(NEW.review_packet_json,'$.previousReview.authorityRequestId')=request.id
    AND json_extract(NEW.review_packet_json,'$.attemptEvidence.policy')='MAIL_ATTEMPTS_V1'
    AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
    AND run.mode='STANDARD'
    AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=old.requirement_version_id)
    AND NOT EXISTS (SELECT 1 FROM cleardev_human_decision_effects WHERE request_id=request.id)
 ))
BEGIN SELECT RAISE(ABORT,'final review evidence recheck requires one bound native authorization'); END;
DROP TRIGGER cleardev_fixed_reviewer_authorization;
CREATE TRIGGER cleardev_fixed_reviewer_authorization BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NEW.role='REVIEWER' AND NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_fixed_recovery_claims AS claim ON claim.request_id=request.id
 WHERE request.execution_run_id=NEW.execution_run_id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
 AND NEW.id=claim.operation_id||':reviewer' AND NEW.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND NEW.continuation_of_role_binding_id=json_extract(request.request_json,'$.roleBindingId')
 AND NEW.task_mapping_id=json_extract(request.request_json,'$.taskId') AND NEW.candidate_commit_id=json_extract(request.request_json,'$.candidateId')
) AND NOT EXISTS (
 SELECT 1 FROM cleardev_mail_reviewer_rework_sources AS source
 WHERE NEW.status='REQUESTED' AND NEW.execution_run_id=source.execution_run_id
 AND NEW.continuation_of_role_binding_id=source.prior_binding_id AND NEW.task_mapping_id=source.task_mapping_id
 AND NEW.candidate_commit_id=source.candidate_id
 AND NEW.session_creation_idempotency_key='cleardev-mail-review-rework:'||source.candidate_id
) BEGIN SELECT RAISE(ABORT,'replacement Reviewer requires exact fixed recovery or mail rework authorization'); END;
DROP INDEX idx_cleardev_complex_execution_reviewer_candidate;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_reviewer_candidate
 ON cleardev_complex_execution_role_bindings(execution_run_id,candidate_commit_id)
 WHERE role='REVIEWER' AND continuation_of_role_binding_id IS NULL;
DROP INDEX cleardev_final_review_per_candidate;
CREATE UNIQUE INDEX cleardev_final_review_per_candidate ON cleardev_requirement_final_reviews(execution_run_id, candidate_commit_sha, COALESCE(json_extract(review_packet_json,'$.previousReview.reviewId'),''));
DROP TABLE cleardev_workflow_recoveries;
COMMIT;
-- +goose StatementEnd
-- +goose StatementBegin
PRAGMA foreign_keys=ON;
-- +goose StatementEnd
