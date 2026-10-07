-- +goose Up
-- A bounded request is not a verdict. Original reviews and steps stay immutable.
CREATE TABLE cleardev_review_check_requests (
 review_id TEXT PRIMARY KEY REFERENCES cleardev_complex_execution_reviews(id),
 request_json TEXT NOT NULL CHECK(json_valid(request_json)),
 request_sha256 TEXT NOT NULL CHECK(length(request_sha256)=64),
 created_at TIMESTAMP NOT NULL
);
CREATE TABLE cleardev_review_check_results (
 review_id TEXT NOT NULL REFERENCES cleardev_review_check_requests(review_id),
 check_id TEXT NOT NULL CHECK(check_id IN ('demo-backend','demo-api','demo-frontend','demo-integration')),
 run_id TEXT NOT NULL UNIQUE,
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64),
 created_at TIMESTAMP NOT NULL,
 PRIMARY KEY(review_id,check_id)
);
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_request_valid BEFORE INSERT ON cleardev_review_check_requests
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_reviews AS review
 JOIN cleardev_complex_execution_agent_steps AS step ON step.id=review.agent_step_id
 JOIN cleardev_candidate_commits AS candidate ON candidate.id=review.candidate_commit_id
 JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id=review.task_attempt_id
 JOIN cleardev_complex_execution_runs AS run ON run.id=attempt.execution_run_id
 JOIN cleardev_development_projects AS project ON project.id=run.development_project_id
 JOIN cleardev_complex_execution_role_bindings AS binding ON binding.id=review.reviewer_role_binding_id
 WHERE review.id=NEW.review_id AND review.status='PENDING' AND binding.status='BOUND'
 AND step.send_status='SETTLED' AND json_valid(step.final_message_text)
 AND json_extract(step.final_message_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
 AND run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED'
 AND project.cancelled_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND json_extract(NEW.request_json,'$.reviewId')=review.id
 AND json_extract(NEW.request_json,'$.requestStepId')=step.id
 AND json_extract(NEW.request_json,'$.candidateId')=candidate.id
 AND json_extract(NEW.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(NEW.request_json,'$.packetSha256')=review.review_packet_sha256
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND NOT EXISTS (SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN ('demo-backend','demo-api','demo-frontend','demo-integration'))
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(step.final_message_text,'$.checkIds')
 AND NOT EXISTS (SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(step.final_message_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review check request must bind an exact pending mail review'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_result_valid BEFORE INSERT ON cleardev_review_check_results
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_review_check_requests AS request
 JOIN cleardev_complex_execution_reviews AS review ON review.id=request.review_id
 WHERE request.review_id=NEW.review_id AND review.status='PENDING'
 AND NEW.run_id=NEW.review_id||':requested-check:'||NEW.check_id
 AND EXISTS (SELECT 1 FROM json_each(request.request_json,'$.checkIds') WHERE value=NEW.check_id)
 AND json_extract(NEW.result_json,'$.reviewId')=NEW.review_id
 AND json_extract(NEW.result_json,'$.checkId')=NEW.check_id
 AND json_extract(NEW.result_json,'$.proof.runId')=NEW.run_id
 AND json_extract(NEW.result_json,'$.proof.candidateSha')=json_extract(request.request_json,'$.candidateSha')
 AND json_extract(NEW.result_json,'$.outcome') IN ('PASS','FAIL','TIMED_OUT','INFRA_ERROR')
) BEGIN SELECT RAISE(ABORT,'review check result must bind its approved request'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_request_no_update BEFORE UPDATE ON cleardev_review_check_requests BEGIN SELECT RAISE(ABORT,'review check requests are immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_request_no_delete BEFORE DELETE ON cleardev_review_check_requests BEGIN SELECT RAISE(ABORT,'review check requests are append-only'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_result_no_update BEFORE UPDATE ON cleardev_review_check_results BEGIN SELECT RAISE(ABORT,'review check results are immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_result_no_delete BEFORE DELETE ON cleardev_review_check_results BEGIN SELECT RAISE(ABORT,'review check results are append-only'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_request_cdc AFTER INSERT ON cleardev_review_check_requests BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'reviewCheckRequestId',NEW.review_id),NEW.created_at
 FROM cleardev_complex_execution_reviews AS review JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id=review.task_attempt_id JOIN cleardev_complex_execution_runs AS run ON run.id=attempt.execution_run_id JOIN cleardev_development_projects AS project ON project.id=run.development_project_id WHERE review.id=NEW.review_id;
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_check_result_cdc AFTER INSERT ON cleardev_review_check_results BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'reviewCheckRequestId',NEW.review_id),NEW.created_at
 FROM cleardev_complex_execution_reviews AS review JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id=review.task_attempt_id JOIN cleardev_complex_execution_runs AS run ON run.id=attempt.execution_run_id JOIN cleardev_development_projects AS project ON project.id=run.development_project_id WHERE review.id=NEW.review_id;
END;
-- +goose StatementEnd
DROP TRIGGER cleardev_complex_execution_review_update_valid;
-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose Down
-- Never remove request history to make the old one-turn protocol fit.
-- +goose StatementBegin
CREATE TRIGGER cleardev_review_checks_down_guard BEFORE DELETE ON cleardev_review_check_requests BEGIN SELECT RAISE(ABORT,'cannot downgrade with Reviewer check history'); END;
-- +goose StatementEnd
DELETE FROM cleardev_review_check_requests;
DROP TRIGGER cleardev_review_checks_down_guard;
DROP TRIGGER cleardev_complex_execution_review_update_valid;
-- +goose StatementBegin
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
 SELECT 1 FROM cleardev_complex_execution_agent_steps AS step WHERE step.id=NEW.agent_step_id AND step.send_status='SETTLED' AND step.turn_id=NEW.turn_id AND step.final_message_id=NEW.final_message_id
 )) BEGIN SELECT RAISE(ABORT,'cleardev complex execution review is immutable or settles once'); END;
-- +goose StatementEnd
DROP TABLE cleardev_review_check_results;
DROP TABLE cleardev_review_check_requests;
