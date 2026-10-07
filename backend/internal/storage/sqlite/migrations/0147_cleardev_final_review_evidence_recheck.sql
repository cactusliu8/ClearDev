-- One native-authorized evidence recheck; preserve every original review row and result.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_requirement_final_reviews_0147 (
    id TEXT PRIMARY KEY NOT NULL,
    execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256 TEXT NOT NULL CHECK(length(requirement_sha256)=64),
    plan_id TEXT NOT NULL,
    plan_sha256 TEXT NOT NULL CHECK(length(plan_sha256)=64),
    candidate_commit_sha TEXT NOT NULL CHECK(length(candidate_commit_sha)=40 AND candidate_commit_sha NOT GLOB '*[^0-9a-f]*'),
    base_commit_sha TEXT NOT NULL CHECK(length(base_commit_sha)=40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'),
    source_workspace_path TEXT NOT NULL CHECK(length(trim(source_workspace_path))>0),
    review_packet_json TEXT NOT NULL CHECK(json_valid(review_packet_json)),
    review_packet_sha256 TEXT NOT NULL CHECK(length(review_packet_sha256)=64),
    prompt_sha256 TEXT NOT NULL CHECK(length(prompt_sha256)=64),
    check_run_ids_json TEXT NOT NULL CHECK(json_valid(check_run_ids_json) AND json_type(check_run_ids_json)='array' AND json_array_length(check_run_ids_json)>0),
    status TEXT NOT NULL CHECK(status IN ('REQUESTED','PENDING','SENT','SETTLED','FAILED')),
    ao_session_id TEXT,
    workspace_path TEXT,
    result_id TEXT REFERENCES cleardev_agent_step_results(id),
    verdict TEXT CHECK(verdict IN ('PASS','REWORK','BLOCKED','NEEDS_HUMAN')),
    reason_code TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    bound_at TIMESTAMP,
    sent_at TIMESTAMP,
    settled_at TIMESTAMP,
    CHECK(status='FAILED' OR
        (status='REQUESTED' AND ao_session_id IS NULL AND workspace_path IS NULL AND bound_at IS NULL AND sent_at IS NULL AND settled_at IS NULL AND result_id IS NULL AND verdict IS NULL) OR
        (status='PENDING' AND ao_session_id IS NOT NULL AND workspace_path IS NOT NULL AND bound_at IS NOT NULL AND sent_at IS NULL AND settled_at IS NULL AND result_id IS NULL AND verdict IS NULL) OR
        (status='SENT' AND ao_session_id IS NOT NULL AND workspace_path IS NOT NULL AND bound_at IS NOT NULL AND sent_at IS NOT NULL AND settled_at IS NULL AND result_id IS NULL AND verdict IS NULL) OR
        (status='SETTLED' AND ao_session_id IS NOT NULL AND workspace_path IS NOT NULL AND bound_at IS NOT NULL AND sent_at IS NOT NULL AND settled_at IS NOT NULL AND result_id IS NOT NULL AND verdict IS NOT NULL)),
    CHECK(status<>'FAILED' OR (settled_at IS NOT NULL AND verdict IS NULL AND result_id IS NULL AND reason_code<>'')),
    CHECK(workspace_path IS NULL OR (length(trim(workspace_path))>0 AND workspace_path<>source_workspace_path))
);
INSERT INTO cleardev_requirement_final_reviews_0147 (rowid,id,execution_run_id,development_project_id,requirement_version_id,requirement_sha256,plan_id,plan_sha256,candidate_commit_sha,base_commit_sha,source_workspace_path,review_packet_json,review_packet_sha256,prompt_sha256,check_run_ids_json,status,ao_session_id,workspace_path,result_id,verdict,reason_code,summary,created_at,bound_at,sent_at,settled_at) SELECT rowid,id,execution_run_id,development_project_id,requirement_version_id,requirement_sha256,plan_id,plan_sha256,candidate_commit_sha,base_commit_sha,source_workspace_path,review_packet_json,review_packet_sha256,prompt_sha256,check_run_ids_json,status,ao_session_id,workspace_path,result_id,verdict,reason_code,summary,created_at,bound_at,sent_at,settled_at FROM cleardev_requirement_final_reviews;
DROP TRIGGER cleardev_final_review_cdc_update;
DROP TRIGGER cleardev_final_review_cdc_insert;
DROP TRIGGER cleardev_execution_requires_final_review;
DROP TRIGGER cleardev_final_review_delete_guard;
DROP TRIGGER cleardev_final_review_update_guard;
DROP TRIGGER cleardev_final_review_insert_guard;
DROP TABLE cleardev_requirement_final_reviews;
ALTER TABLE cleardev_requirement_final_reviews_0147 RENAME TO cleardev_requirement_final_reviews;
CREATE UNIQUE INDEX cleardev_final_review_per_round ON cleardev_requirement_final_reviews(execution_run_id,COALESCE(json_extract(review_packet_json,'$.previousReview.reviewId'),''));
CREATE TRIGGER cleardev_final_review_insert_guard
BEFORE INSERT ON cleardev_requirement_final_reviews
WHEN NEW.status<>'REQUESTED' OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id=run.development_project_id
    WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND run.development_project_id=NEW.development_project_id
      AND run.requirement_version_id=NEW.requirement_version_id AND run.requirement_sha256=NEW.requirement_sha256
      AND run.plan_id=NEW.plan_id AND run.plan_sha256=NEW.plan_sha256
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL
      AND json_extract(run.execution_package_json,'$.finalReviewPolicy')='REQUIREMENT_FINAL_REVIEW_V1'
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
)
BEGIN SELECT RAISE(ABORT,'requirement final review has no current frozen execution'); END;

CREATE TRIGGER cleardev_final_review_update_guard
BEFORE UPDATE ON cleardev_requirement_final_reviews
WHEN OLD.rowid IS NOT NEW.rowid OR OLD.id IS NOT NEW.id OR OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.development_project_id IS NOT NEW.development_project_id OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256 OR OLD.plan_id IS NOT NEW.plan_id OR OLD.plan_sha256 IS NOT NEW.plan_sha256
 OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
 OR OLD.source_workspace_path IS NOT NEW.source_workspace_path OR OLD.review_packet_json IS NOT NEW.review_packet_json
 OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256 OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
 OR OLD.check_run_ids_json IS NOT NEW.check_run_ids_json OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status IN ('SETTLED','FAILED')
 OR NOT ((OLD.status='REQUESTED' AND NEW.status IN ('PENDING','FAILED'))
      OR (OLD.status='PENDING' AND NEW.status IN ('SENT','FAILED'))
      OR (OLD.status='SENT' AND NEW.status IN ('SETTLED','FAILED')))
 OR (NEW.status='FAILED' AND (
      OLD.ao_session_id IS NOT NEW.ao_session_id
   OR OLD.workspace_path IS NOT NEW.workspace_path
   OR OLD.bound_at IS NOT NEW.bound_at
   OR OLD.sent_at IS NOT NEW.sent_at))
 OR (NEW.status<>'FAILED' AND OLD.status<>'REQUESTED' AND (
      OLD.ao_session_id IS NOT NEW.ao_session_id
   OR OLD.workspace_path IS NOT NEW.workspace_path
   OR OLD.bound_at IS NOT NEW.bound_at))
 OR (NEW.status<>'FAILED' AND OLD.status='SENT' AND OLD.sent_at IS NOT NEW.sent_at)
 OR (NEW.status<>'FAILED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_development_projects AS project
    WHERE project.id=NEW.development_project_id AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 ))
 OR (NEW.status<>'FAILED' AND NEW.ao_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions AS session
    JOIN cleardev_development_projects AS project ON project.ao_project_id=session.project_id
    WHERE project.id=NEW.development_project_id AND session.id=NEW.ao_session_id
      AND session.kind='worker' AND session.harness='codex'
      AND session.session_mode='chat' AND session.permission_mode='auto'
      AND session.creation_idempotency_key='cleardev-requirement-final-review:'||COALESCE(json_extract(NEW.review_packet_json,'$.previousReview.reviewId'),NEW.id)
      AND session.workspace_path=NEW.workspace_path
 ))
 OR (NEW.status<>'FAILED' AND NEW.ao_session_id IS NOT NULL AND (
      EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_exception_ondemand_bindings WHERE ao_session_id=NEW.ao_session_id)))
 OR (NEW.status='SENT' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_agent_message_reservations AS message ON message.attempt_id=attempt.id AND message.client_message_id=attempt.client_message_id
    JOIN cleardev_agent_attempt_events AS event ON event.attempt_id=attempt.id AND event.client_message_id=message.client_message_id
    WHERE attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND message.ao_session_id=NEW.ao_session_id AND message.prompt_sha256=NEW.prompt_sha256
      AND message.source IN ('ORIGINAL','RECOVERY_ORIGINAL') AND event.status='SENT' AND length(event.turn_id)>0
 ))
 OR (NEW.status='SETTLED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_results AS result
    JOIN cleardev_agent_step_attempts AS attempt ON attempt.id=result.attempt_id
    JOIN cleardev_agent_step_result_parses AS parsed ON parsed.result_id=result.id
    WHERE result.id=NEW.result_id AND parsed.conclusion='VALID'
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND json_extract(result.raw_message_text,'$.kind')='REQUIREMENT_FINAL_REVIEW_RESULT'
      AND json_extract(result.raw_message_text,'$.verdict')=NEW.verdict
 ))
BEGIN SELECT RAISE(ABORT,'requirement final review is immutable, not independent, or has no bound provider result'); END;

CREATE TRIGGER cleardev_final_review_delete_guard
BEFORE DELETE ON cleardev_requirement_final_reviews
BEGIN SELECT RAISE(ABORT,'requirement final reviews are append-only'); END;

-- The final PASS is checked again inside the existing atomic completion statement.
CREATE TRIGGER cleardev_execution_requires_final_review
BEFORE UPDATE OF completion_status ON cleardev_complex_execution_results
WHEN NEW.completion_status='COMMITTING'
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND json_extract(execution_package_json,'$.finalReviewPolicy') IS NOT NULL)
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_requirement_final_reviews AS review
    JOIN cleardev_complex_execution_runs AS run ON run.id=review.execution_run_id
    JOIN cleardev_integration_candidates AS candidate ON candidate.id=NEW.integration_candidate_id
    WHERE run.id=NEW.execution_run_id
      AND review.rowid=(SELECT max(rowid) FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id)
      AND json_extract(run.execution_package_json,'$.finalReviewPolicy')='REQUIREMENT_FINAL_REVIEW_V1'
      AND review.status='SETTLED' AND review.verdict='PASS' AND review.result_id IS NOT NULL
      AND review.development_project_id=run.development_project_id
      AND review.requirement_version_id=run.requirement_version_id AND review.requirement_sha256=run.requirement_sha256
      AND review.plan_id=run.plan_id AND review.plan_sha256=run.plan_sha256
      AND candidate.commit_sha=review.candidate_commit_sha
      AND (SELECT count(*) FROM cleardev_complex_execution_result_checks WHERE result_id=NEW.id)=json_array_length(review.check_run_ids_json)
      AND NOT EXISTS (SELECT 1 FROM json_each(review.check_run_ids_json) AS expected
          WHERE NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_result_checks AS actual WHERE actual.result_id=NEW.id AND actual.check_run_id=expected.value))
 )
BEGIN SELECT RAISE(ABORT,'completion requires exact requirement final reviewer PASS'); END;

CREATE TRIGGER cleardev_final_review_cdc_insert
AFTER INSERT ON cleardev_requirement_final_reviews
BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id,'finalReviewId',NEW.id),NEW.created_at
 FROM cleardev_development_projects WHERE id=NEW.development_project_id;
END;
CREATE TRIGGER cleardev_final_review_cdc_update
AFTER UPDATE ON cleardev_requirement_final_reviews
BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id,'finalReviewId',NEW.id,'status',NEW.status),COALESCE(NEW.settled_at,NEW.sent_at,NEW.bound_at,NEW.created_at)
 FROM cleardev_development_projects WHERE id=NEW.development_project_id;
END;

-- Reject every REPLACE conflict before its implicit deletion, including rowid.
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

CREATE TRIGGER cleardev_final_review_recheck_session_guard
BEFORE UPDATE ON cleardev_requirement_final_reviews
WHEN NEW.status<>'FAILED' AND NEW.ao_session_id IS NOT NULL AND (
 (json_extract(NEW.review_packet_json,'$.previousReview') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews old WHERE old.id=json_extract(NEW.review_packet_json,'$.previousReview.reviewId') AND old.ao_session_id=NEW.ao_session_id AND old.workspace_path=NEW.workspace_path))
 OR EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews other WHERE other.id<>NEW.id AND other.ao_session_id=NEW.ao_session_id AND other.id IS NOT json_extract(NEW.review_packet_json,'$.previousReview.reviewId'))
)
BEGIN SELECT RAISE(ABORT,'final evidence recheck must reuse its original independent reviewer'); END;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_final_recheck_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_final_recheck_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE json_extract(review_packet_json,'$.previousReview') IS NOT NULL) OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests WHERE decision_kind='AUTHORIZE_FINAL_REVIEW_EVIDENCE_RECHECK') THEN 0 ELSE 1 END;
DROP TABLE cleardev_final_recheck_down_guard;
DROP TRIGGER cleardev_final_review_cdc_update;
DROP TRIGGER cleardev_final_review_cdc_insert;
DROP TRIGGER cleardev_execution_requires_final_review;
DROP TRIGGER cleardev_final_review_delete_guard;
DROP TRIGGER cleardev_final_review_update_guard;
DROP TRIGGER cleardev_final_review_insert_guard;
DROP TRIGGER cleardev_final_review_recheck_insert_guard;
DROP TRIGGER cleardev_final_review_recheck_session_guard;
CREATE TEMP TABLE cleardev_final_review_saved_0147 AS SELECT rowid AS saved_rowid,* FROM cleardev_requirement_final_reviews;
DROP TABLE cleardev_requirement_final_reviews;
CREATE TABLE cleardev_requirement_final_reviews (
    id TEXT PRIMARY KEY,
    execution_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_runs(id),
    development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
    requirement_version_id TEXT NOT NULL REFERENCES cleardev_contract_versions(id),
    requirement_sha256 TEXT NOT NULL CHECK(length(requirement_sha256)=64),
    plan_id TEXT NOT NULL,
    plan_sha256 TEXT NOT NULL CHECK(length(plan_sha256)=64),
    candidate_commit_sha TEXT NOT NULL CHECK(length(candidate_commit_sha)=40 AND candidate_commit_sha NOT GLOB '*[^0-9a-f]*'),
    base_commit_sha TEXT NOT NULL CHECK(length(base_commit_sha)=40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*'),
    source_workspace_path TEXT NOT NULL CHECK(length(trim(source_workspace_path))>0),
    review_packet_json TEXT NOT NULL CHECK(json_valid(review_packet_json)),
    review_packet_sha256 TEXT NOT NULL CHECK(length(review_packet_sha256)=64),
    prompt_sha256 TEXT NOT NULL CHECK(length(prompt_sha256)=64),
    check_run_ids_json TEXT NOT NULL CHECK(json_valid(check_run_ids_json) AND json_type(check_run_ids_json)='array' AND json_array_length(check_run_ids_json)>0),
    status TEXT NOT NULL CHECK(status IN ('REQUESTED','PENDING','SENT','SETTLED','FAILED')),
    ao_session_id TEXT UNIQUE,
    workspace_path TEXT,
    result_id TEXT REFERENCES cleardev_agent_step_results(id),
    verdict TEXT CHECK(verdict IN ('PASS','REWORK','BLOCKED','NEEDS_HUMAN')),
    reason_code TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL,
    bound_at TIMESTAMP,
    sent_at TIMESTAMP,
    settled_at TIMESTAMP,
    CHECK(status='FAILED' OR
        (status='REQUESTED' AND ao_session_id IS NULL AND workspace_path IS NULL AND bound_at IS NULL AND sent_at IS NULL AND settled_at IS NULL AND result_id IS NULL AND verdict IS NULL) OR
        (status='PENDING' AND ao_session_id IS NOT NULL AND workspace_path IS NOT NULL AND bound_at IS NOT NULL AND sent_at IS NULL AND settled_at IS NULL AND result_id IS NULL AND verdict IS NULL) OR
        (status='SENT' AND ao_session_id IS NOT NULL AND workspace_path IS NOT NULL AND bound_at IS NOT NULL AND sent_at IS NOT NULL AND settled_at IS NULL AND result_id IS NULL AND verdict IS NULL) OR
        (status='SETTLED' AND ao_session_id IS NOT NULL AND workspace_path IS NOT NULL AND bound_at IS NOT NULL AND sent_at IS NOT NULL AND settled_at IS NOT NULL AND result_id IS NOT NULL AND verdict IS NOT NULL)),
    CHECK(status<>'FAILED' OR (settled_at IS NOT NULL AND verdict IS NULL AND result_id IS NULL AND reason_code<>'')),
    CHECK(workspace_path IS NULL OR (length(trim(workspace_path))>0 AND workspace_path<>source_workspace_path))
);
INSERT INTO cleardev_requirement_final_reviews (rowid,id,execution_run_id,development_project_id,requirement_version_id,requirement_sha256,plan_id,plan_sha256,candidate_commit_sha,base_commit_sha,source_workspace_path,review_packet_json,review_packet_sha256,prompt_sha256,check_run_ids_json,status,ao_session_id,workspace_path,result_id,verdict,reason_code,summary,created_at,bound_at,sent_at,settled_at) SELECT saved_rowid,id,execution_run_id,development_project_id,requirement_version_id,requirement_sha256,plan_id,plan_sha256,candidate_commit_sha,base_commit_sha,source_workspace_path,review_packet_json,review_packet_sha256,prompt_sha256,check_run_ids_json,status,ao_session_id,workspace_path,result_id,verdict,reason_code,summary,created_at,bound_at,sent_at,settled_at FROM cleardev_final_review_saved_0147;
DROP TABLE cleardev_final_review_saved_0147;
CREATE TRIGGER cleardev_final_review_insert_guard
BEFORE INSERT ON cleardev_requirement_final_reviews
WHEN NEW.status<>'REQUESTED' OR NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id=run.development_project_id
    WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND run.development_project_id=NEW.development_project_id
      AND run.requirement_version_id=NEW.requirement_version_id AND run.requirement_sha256=NEW.requirement_sha256
      AND run.plan_id=NEW.plan_id AND run.plan_sha256=NEW.plan_sha256
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL
      AND json_extract(run.execution_package_json,'$.finalReviewPolicy')='REQUIREMENT_FINAL_REVIEW_V1'
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
)
BEGIN SELECT RAISE(ABORT,'requirement final review has no current frozen execution'); END;

CREATE TRIGGER cleardev_final_review_update_guard
BEFORE UPDATE ON cleardev_requirement_final_reviews
WHEN OLD.id IS NOT NEW.id OR OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.development_project_id IS NOT NEW.development_project_id OR OLD.requirement_version_id IS NOT NEW.requirement_version_id
 OR OLD.requirement_sha256 IS NOT NEW.requirement_sha256 OR OLD.plan_id IS NOT NEW.plan_id OR OLD.plan_sha256 IS NOT NEW.plan_sha256
 OR OLD.candidate_commit_sha IS NOT NEW.candidate_commit_sha OR OLD.base_commit_sha IS NOT NEW.base_commit_sha
 OR OLD.source_workspace_path IS NOT NEW.source_workspace_path OR OLD.review_packet_json IS NOT NEW.review_packet_json
 OR OLD.review_packet_sha256 IS NOT NEW.review_packet_sha256 OR OLD.prompt_sha256 IS NOT NEW.prompt_sha256
 OR OLD.check_run_ids_json IS NOT NEW.check_run_ids_json OR OLD.created_at IS NOT NEW.created_at
 OR OLD.status IN ('SETTLED','FAILED')
 OR NOT ((OLD.status='REQUESTED' AND NEW.status IN ('PENDING','FAILED'))
      OR (OLD.status='PENDING' AND NEW.status IN ('SENT','FAILED'))
      OR (OLD.status='SENT' AND NEW.status IN ('SETTLED','FAILED')))
 OR (NEW.status='FAILED' AND (
      OLD.ao_session_id IS NOT NEW.ao_session_id
   OR OLD.workspace_path IS NOT NEW.workspace_path
   OR OLD.bound_at IS NOT NEW.bound_at
   OR OLD.sent_at IS NOT NEW.sent_at))
 OR (NEW.status<>'FAILED' AND OLD.status<>'REQUESTED' AND (
      OLD.ao_session_id IS NOT NEW.ao_session_id
   OR OLD.workspace_path IS NOT NEW.workspace_path
   OR OLD.bound_at IS NOT NEW.bound_at))
 OR (NEW.status<>'FAILED' AND OLD.status='SENT' AND OLD.sent_at IS NOT NEW.sent_at)
 OR (NEW.status<>'FAILED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_development_projects AS project
    WHERE project.id=NEW.development_project_id AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 ))
 OR (NEW.status<>'FAILED' AND NEW.ao_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions AS session
    JOIN cleardev_development_projects AS project ON project.ao_project_id=session.project_id
    WHERE project.id=NEW.development_project_id AND session.id=NEW.ao_session_id
      AND session.kind='worker' AND session.harness='codex'
      AND session.session_mode='chat' AND session.permission_mode='auto'
      AND session.creation_idempotency_key='cleardev-requirement-final-review:'||NEW.id
      AND session.workspace_path=NEW.workspace_path
 ))
 OR (NEW.status<>'FAILED' AND NEW.ao_session_id IS NOT NULL AND (
      EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_role_bindings WHERE ao_session_id=NEW.ao_session_id)
   OR EXISTS(SELECT 1 FROM cleardev_complex_exception_ondemand_bindings WHERE ao_session_id=NEW.ao_session_id)))
 OR (NEW.status='SENT' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_attempts AS attempt
    JOIN cleardev_agent_message_reservations AS message ON message.attempt_id=attempt.id AND message.client_message_id=attempt.client_message_id
    JOIN cleardev_agent_attempt_events AS event ON event.attempt_id=attempt.id AND event.client_message_id=message.client_message_id
    WHERE attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND message.ao_session_id=NEW.ao_session_id AND message.prompt_sha256=NEW.prompt_sha256
      AND message.source IN ('ORIGINAL','RECOVERY_ORIGINAL') AND event.status='SENT' AND length(event.turn_id)>0
 ))
 OR (NEW.status='SETTLED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_agent_step_results AS result
    JOIN cleardev_agent_step_attempts AS attempt ON attempt.id=result.attempt_id
    JOIN cleardev_agent_step_result_parses AS parsed ON parsed.result_id=result.id
    WHERE result.id=NEW.result_id AND parsed.conclusion='VALID'
      AND attempt.development_project_id=NEW.development_project_id
      AND attempt.logical_step_id=NEW.id||':step' AND attempt.role_binding_id=NEW.id
      AND attempt.step_category='COMPLEX_EXECUTION' AND attempt.step_kind='REQUIREMENT_FINAL_REVIEW'
      AND attempt.ao_session_id=NEW.ao_session_id AND attempt.prompt_sha256=NEW.prompt_sha256
      AND json_extract(result.raw_message_text,'$.kind')='REQUIREMENT_FINAL_REVIEW_RESULT'
      AND json_extract(result.raw_message_text,'$.verdict')=NEW.verdict
 ))
BEGIN SELECT RAISE(ABORT,'requirement final review is immutable, not independent, or has no bound provider result'); END;

CREATE TRIGGER cleardev_final_review_delete_guard
BEFORE DELETE ON cleardev_requirement_final_reviews
BEGIN SELECT RAISE(ABORT,'requirement final reviews are append-only'); END;

-- The final PASS is checked again inside the existing atomic completion statement.
CREATE TRIGGER cleardev_execution_requires_final_review
BEFORE UPDATE OF completion_status ON cleardev_complex_execution_results
WHEN NEW.completion_status='COMMITTING'
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND json_extract(execution_package_json,'$.finalReviewPolicy') IS NOT NULL)
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_requirement_final_reviews AS review
    JOIN cleardev_complex_execution_runs AS run ON run.id=review.execution_run_id
    JOIN cleardev_integration_candidates AS candidate ON candidate.id=NEW.integration_candidate_id
    WHERE run.id=NEW.execution_run_id
      AND json_extract(run.execution_package_json,'$.finalReviewPolicy')='REQUIREMENT_FINAL_REVIEW_V1'
      AND review.status='SETTLED' AND review.verdict='PASS' AND review.result_id IS NOT NULL
      AND review.development_project_id=run.development_project_id
      AND review.requirement_version_id=run.requirement_version_id AND review.requirement_sha256=run.requirement_sha256
      AND review.plan_id=run.plan_id AND review.plan_sha256=run.plan_sha256
      AND candidate.commit_sha=review.candidate_commit_sha
      AND (SELECT count(*) FROM cleardev_complex_execution_result_checks WHERE result_id=NEW.id)=json_array_length(review.check_run_ids_json)
      AND NOT EXISTS (SELECT 1 FROM json_each(review.check_run_ids_json) AS expected
          WHERE NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_result_checks AS actual WHERE actual.result_id=NEW.id AND actual.check_run_id=expected.value))
 )
BEGIN SELECT RAISE(ABORT,'completion requires exact requirement final reviewer PASS'); END;

CREATE TRIGGER cleardev_final_review_cdc_insert
AFTER INSERT ON cleardev_requirement_final_reviews
BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id,'finalReviewId',NEW.id),NEW.created_at
 FROM cleardev_development_projects WHERE id=NEW.development_project_id;
END;
CREATE TRIGGER cleardev_final_review_cdc_update
AFTER UPDATE ON cleardev_requirement_final_reviews
BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id,'finalReviewId',NEW.id,'status',NEW.status),COALESCE(NEW.settled_at,NEW.sent_at,NEW.bound_at,NEW.created_at)
 FROM cleardev_development_projects WHERE id=NEW.development_project_id;
END;

-- +goose StatementEnd
