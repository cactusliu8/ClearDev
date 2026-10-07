-- +goose Up
CREATE TABLE cleardev_fixed_recovery_requests (
 id TEXT PRIMARY KEY,
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 logical_step_id TEXT UNIQUE,
 check_run_id TEXT UNIQUE REFERENCES cleardev_complex_execution_check_runs(id),
 failure_event_id TEXT REFERENCES cleardev_agent_attempt_events(id),
 request_json TEXT NOT NULL CHECK(json_valid(request_json)),
 created_at TIMESTAMP NOT NULL,
 CHECK((logical_step_id IS NOT NULL AND failure_event_id IS NOT NULL AND check_run_id IS NULL) OR (logical_step_id IS NULL AND failure_event_id IS NULL AND check_run_id IS NOT NULL))
);
CREATE TABLE cleardev_fixed_recovery_refusals (
 request_id TEXT PRIMARY KEY REFERENCES cleardev_fixed_recovery_requests(id),
 reason_code TEXT NOT NULL CHECK(length(trim(reason_code))>0),
 recorded_at TIMESTAMP NOT NULL
);
-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_refusals_update_forbidden BEFORE UPDATE ON cleardev_fixed_recovery_refusals BEGIN SELECT RAISE(ABORT,'fixed recovery refusal is immutable'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_refusals_delete_forbidden BEFORE DELETE ON cleardev_fixed_recovery_refusals BEGIN SELECT RAISE(ABORT,'fixed recovery refusal is immutable'); END;
-- +goose StatementEnd
CREATE TABLE cleardev_fixed_recovery_claims (
 request_id TEXT PRIMARY KEY REFERENCES cleardev_fixed_recovery_requests(id),
 proposal_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_exception_recovery_actions(id),
 action TEXT NOT NULL CHECK(action IN ('RESTORE_ORIGINAL_SESSION','REBUILD_INDEPENDENT_REVIEWER','RETRY_SETTLED_INFRA_CHECK')),
 operation_id TEXT NOT NULL UNIQUE,
 claimed_at TIMESTAMP NOT NULL
);
CREATE TABLE cleardev_fixed_recovery_results (
 request_id TEXT PRIMARY KEY REFERENCES cleardev_fixed_recovery_claims(request_id),
 outcome TEXT NOT NULL CHECK(outcome IN ('PASS','FAILED','UNKNOWN')),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 recorded_at TIMESTAMP NOT NULL
);
CREATE TABLE cleardev_replacement_review_results (
 recovery_request_id TEXT PRIMARY KEY REFERENCES cleardev_fixed_recovery_results(request_id),
 original_review_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_reviews(id),
 attempt_id TEXT NOT NULL UNIQUE REFERENCES cleardev_agent_step_attempts(id),
 result_id TEXT NOT NULL UNIQUE REFERENCES cleardev_agent_step_results(id),
 verdict TEXT NOT NULL CHECK(verdict IN ('PASS','REWORK','BLOCKED','NEEDS_HUMAN')),
 result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 recorded_at TIMESTAMP NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_requests_update_forbidden BEFORE UPDATE ON cleardev_fixed_recovery_requests BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_requests_delete_forbidden BEFORE DELETE ON cleardev_fixed_recovery_requests BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_claims_update_forbidden BEFORE UPDATE ON cleardev_fixed_recovery_claims BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_claims_delete_forbidden BEFORE DELETE ON cleardev_fixed_recovery_claims BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_results_update_forbidden BEFORE UPDATE ON cleardev_fixed_recovery_results BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_results_delete_forbidden BEFORE DELETE ON cleardev_fixed_recovery_results BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_replacement_review_results_update_forbidden BEFORE UPDATE ON cleardev_replacement_review_results BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_replacement_review_results_delete_forbidden BEFORE DELETE ON cleardev_replacement_review_results BEGIN
 SELECT RAISE(ABORT,'ClearDev fixed recovery facts are append-only');
END;
-- +goose StatementEnd


DROP INDEX idx_cleardev_complex_execution_reviewer_candidate;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_reviewer_candidate
 ON cleardev_complex_execution_role_bindings(execution_run_id,candidate_commit_id)
 WHERE role='REVIEWER' AND continuation_of_role_binding_id IS NULL;
CREATE UNIQUE INDEX idx_cleardev_fixed_reviewer_once ON cleardev_complex_execution_role_bindings(continuation_of_role_binding_id) WHERE role='REVIEWER';
-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_reviewer_authorization BEFORE INSERT ON cleardev_complex_execution_role_bindings
WHEN NEW.role='REVIEWER' AND NEW.continuation_of_role_binding_id IS NOT NULL AND NOT EXISTS (
 SELECT 1 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_fixed_recovery_claims AS claim ON claim.request_id=request.id
 WHERE request.execution_run_id=NEW.execution_run_id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
 AND NEW.id=claim.operation_id||':reviewer' AND NEW.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND NEW.continuation_of_role_binding_id=json_extract(request.request_json,'$.roleBindingId')
 AND NEW.task_mapping_id=json_extract(request.request_json,'$.taskId') AND NEW.candidate_commit_id=json_extract(request.request_json,'$.candidateId')
) BEGIN SELECT RAISE(ABORT,'replacement Reviewer requires exact fixed recovery authorization'); END;
-- +goose StatementEnd
ALTER TABLE cleardev_complex_execution_verified_candidates ADD COLUMN replacement_recovery_id TEXT REFERENCES cleardev_replacement_review_results(recovery_request_id);
ALTER TABLE cleardev_complex_execution_verified_candidates ADD COLUMN replacement_attempt_id TEXT REFERENCES cleardev_agent_step_attempts(id);
ALTER TABLE cleardev_complex_execution_verified_candidates ADD COLUMN replacement_result_id TEXT REFERENCES cleardev_agent_step_results(id);
-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_verification_immutable BEFORE UPDATE ON cleardev_complex_execution_verified_candidates
WHEN OLD.replacement_recovery_id IS NOT NEW.replacement_recovery_id OR OLD.replacement_attempt_id IS NOT NEW.replacement_attempt_id OR OLD.replacement_result_id IS NOT NEW.replacement_result_id
BEGIN SELECT RAISE(ABORT,'replacement verification evidence is immutable'); END;
-- +goose StatementEnd

DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid
BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_reviews AS review ON review.task_attempt_id = attempt.id AND review.candidate_commit_id = candidate.id
    WHERE task.id = NEW.task_mapping_id AND attempt.id = NEW.task_attempt_id
      AND candidate.complex_execution_task_attempt_id = attempt.id
      AND attempt.status = 'REVIEWING' AND (
 (NEW.replacement_recovery_id IS NULL AND NEW.replacement_attempt_id IS NULL AND NEW.replacement_result_id IS NULL AND review.status='SETTLED' AND review.verdict='PASS')
 OR EXISTS (
 SELECT 1 FROM cleardev_replacement_review_results AS replacement
 JOIN cleardev_fixed_recovery_requests AS request ON request.id=replacement.recovery_request_id
 JOIN cleardev_fixed_recovery_results AS result ON result.request_id=request.id AND result.outcome='PASS'
 JOIN cleardev_agent_step_attempts AS agent_attempt ON agent_attempt.id=replacement.attempt_id AND agent_attempt.attempt_number=2
 JOIN cleardev_agent_step_result_parses AS parsed ON parsed.result_id=replacement.result_id AND parsed.conclusion='VALID'
 WHERE replacement.recovery_request_id=NEW.replacement_recovery_id AND replacement.original_review_id=review.id
 AND replacement.attempt_id=NEW.replacement_attempt_id AND replacement.result_id=NEW.replacement_result_id AND replacement.verdict='PASS'
 AND request.execution_run_id=run.id AND json_extract(request.request_json,'$.candidateId')=candidate.id
 AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND agent_attempt.logical_step_id=review.agent_step_id AND agent_attempt.ao_session_id=json_extract(result.result_json,'$.sessionId')
 ))
      AND EXISTS (SELECT 1 FROM cleardev_complex_execution_check_specs AS scope
                  JOIN cleardev_complex_execution_check_runs AS scope_run ON scope_run.check_spec_id = scope.id
                  WHERE scope_run.id = NEW.scope_check_run_id AND scope.task_mapping_id = task.id AND scope.check_kind = 'SCOPE'
                    AND scope_run.task_attempt_id = attempt.id AND scope_run.candidate_commit_id = candidate.id
                    AND scope_run.status = 'SETTLED' AND scope_run.result = 'PASS')
      AND review.id = NEW.review_id
	  AND json_array_length(NEW.required_check_runs_json) = (
	      SELECT count(*) FROM cleardev_complex_execution_check_specs AS required_spec
	      WHERE required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM json_each(NEW.required_check_runs_json) AS required_id
	      WHERE typeof(required_id.value) <> 'text'
	         OR NOT EXISTS (
	             SELECT 1
	             FROM cleardev_complex_execution_check_runs AS required_run
	             JOIN cleardev_complex_execution_check_specs AS required_spec ON required_spec.id = required_run.check_spec_id
	             WHERE required_run.id = required_id.value
	               AND required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	               AND required_run.task_attempt_id = attempt.id AND required_run.candidate_commit_id = candidate.id
	               AND required_run.status = 'SETTLED' AND required_run.result = 'PASS'
	         )
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM cleardev_complex_execution_check_specs AS required_spec
	      WHERE required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	        AND NOT EXISTS (
	            SELECT 1
	            FROM cleardev_complex_execution_check_runs AS required_run
	            JOIN json_each(NEW.required_check_runs_json) AS required_id ON required_id.value = required_run.id
	            WHERE required_run.check_spec_id = required_spec.id
	              AND required_run.task_attempt_id = attempt.id AND required_run.candidate_commit_id = candidate.id
	              AND required_run.status = 'SETTLED' AND required_run.result = 'PASS'
	        )
	  )
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_check_specs AS spec
                      WHERE spec.task_mapping_id = task.id AND spec.check_kind = 'REQUIRED_CHECK'
                        AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_check_runs AS check_run
                                        WHERE check_run.check_spec_id = spec.id AND check_run.task_attempt_id = attempt.id
                                          AND check_run.candidate_commit_id = candidate.id AND check_run.status = 'SETTLED' AND check_run.result = 'PASS'))
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidate lacks current scope, checks, review, or gate');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_requests_cdc AFTER INSERT ON cleardev_fixed_recovery_requests BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'fixedRecoveryRequestId',request.id),request.created_at
 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_complex_execution_runs AS run ON run.id=request.execution_run_id JOIN cleardev_development_projects AS project ON project.id=run.development_project_id
 WHERE request.id=NEW.id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_claims_cdc AFTER INSERT ON cleardev_fixed_recovery_claims BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'fixedRecoveryRequestId',request.id),request.created_at
 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_complex_execution_runs AS run ON run.id=request.execution_run_id JOIN cleardev_development_projects AS project ON project.id=run.development_project_id
 WHERE request.id=NEW.request_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_results_cdc AFTER INSERT ON cleardev_fixed_recovery_results BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'fixedRecoveryRequestId',request.id),request.created_at
 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_complex_execution_runs AS run ON run.id=request.execution_run_id JOIN cleardev_development_projects AS project ON project.id=run.development_project_id
 WHERE request.id=NEW.request_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_replacement_review_results_cdc AFTER INSERT ON cleardev_replacement_review_results BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'fixedRecoveryRequestId',request.id),request.created_at
 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_complex_execution_runs AS run ON run.id=request.execution_run_id JOIN cleardev_development_projects AS project ON project.id=run.development_project_id
 WHERE request.id=NEW.recovery_request_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_fixed_recovery_refusals_cdc AFTER INSERT ON cleardev_fixed_recovery_refusals BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'fixedRecoveryRequestId',request.id),NEW.recorded_at
 FROM cleardev_fixed_recovery_requests AS request JOIN cleardev_complex_execution_runs AS run ON run.id=request.execution_run_id JOIN cleardev_development_projects AS project ON project.id=run.development_project_id WHERE request.id=NEW.request_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
DROP TRIGGER cleardev_fixed_verification_immutable;
ALTER TABLE cleardev_complex_execution_verified_candidates DROP COLUMN replacement_result_id;
ALTER TABLE cleardev_complex_execution_verified_candidates DROP COLUMN replacement_attempt_id;
ALTER TABLE cleardev_complex_execution_verified_candidates DROP COLUMN replacement_recovery_id;
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid
BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_task_attempts AS attempt
    JOIN cleardev_complex_execution_task_mappings AS task ON task.id = attempt.task_mapping_id
    JOIN cleardev_complex_execution_runs AS run ON run.id = task.execution_run_id
    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.candidate_commit_id
    JOIN cleardev_complex_execution_reviews AS review ON review.task_attempt_id = attempt.id AND review.candidate_commit_id = candidate.id
    WHERE task.id = NEW.task_mapping_id AND attempt.id = NEW.task_attempt_id
      AND candidate.complex_execution_task_attempt_id = attempt.id
      AND attempt.status = 'REVIEWING' AND review.status = 'SETTLED' AND review.verdict = 'PASS'
      AND EXISTS (SELECT 1 FROM cleardev_complex_execution_check_specs AS scope
                  JOIN cleardev_complex_execution_check_runs AS scope_run ON scope_run.check_spec_id = scope.id
                  WHERE scope_run.id = NEW.scope_check_run_id AND scope.task_mapping_id = task.id AND scope.check_kind = 'SCOPE'
                    AND scope_run.task_attempt_id = attempt.id AND scope_run.candidate_commit_id = candidate.id
                    AND scope_run.status = 'SETTLED' AND scope_run.result = 'PASS')
      AND review.id = NEW.review_id
	  AND json_array_length(NEW.required_check_runs_json) = (
	      SELECT count(*) FROM cleardev_complex_execution_check_specs AS required_spec
	      WHERE required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM json_each(NEW.required_check_runs_json) AS required_id
	      WHERE typeof(required_id.value) <> 'text'
	         OR NOT EXISTS (
	             SELECT 1
	             FROM cleardev_complex_execution_check_runs AS required_run
	             JOIN cleardev_complex_execution_check_specs AS required_spec ON required_spec.id = required_run.check_spec_id
	             WHERE required_run.id = required_id.value
	               AND required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	               AND required_run.task_attempt_id = attempt.id AND required_run.candidate_commit_id = candidate.id
	               AND required_run.status = 'SETTLED' AND required_run.result = 'PASS'
	         )
	  )
	  AND NOT EXISTS (
	      SELECT 1 FROM cleardev_complex_execution_check_specs AS required_spec
	      WHERE required_spec.task_mapping_id = task.id AND required_spec.check_kind = 'REQUIRED_CHECK'
	        AND NOT EXISTS (
	            SELECT 1
	            FROM cleardev_complex_execution_check_runs AS required_run
	            JOIN json_each(NEW.required_check_runs_json) AS required_id ON required_id.value = required_run.id
	            WHERE required_run.check_spec_id = required_spec.id
	              AND required_run.task_attempt_id = attempt.id AND required_run.candidate_commit_id = candidate.id
	              AND required_run.status = 'SETTLED' AND required_run.result = 'PASS'
	        )
	  )
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_check_specs AS spec
                      WHERE spec.task_mapping_id = task.id AND spec.check_kind = 'REQUIRED_CHECK'
                        AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_check_runs AS check_run
                                        WHERE check_run.check_spec_id = spec.id AND check_run.task_attempt_id = attempt.id
                                          AND check_run.candidate_commit_id = candidate.id AND check_run.status = 'SETTLED' AND check_run.result = 'PASS'))
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution verified candidate lacks current scope, checks, review, or gate');
END;
-- +goose StatementEnd
DROP TRIGGER cleardev_fixed_reviewer_authorization;
DROP INDEX idx_cleardev_fixed_reviewer_once;
DROP INDEX idx_cleardev_complex_execution_reviewer_candidate;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_reviewer_candidate ON cleardev_complex_execution_role_bindings(execution_run_id,candidate_commit_id) WHERE role='REVIEWER';
DROP TABLE cleardev_replacement_review_results;
DROP TABLE cleardev_fixed_recovery_results;
DROP TABLE cleardev_fixed_recovery_claims;
DROP TABLE cleardev_fixed_recovery_refusals;
DROP TABLE cleardev_fixed_recovery_requests;
