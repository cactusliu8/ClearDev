-- The integration-candidate guard still picked a task's oldest verification
-- (ordering by task ordinal alone), so after a rework round it demanded the
-- pre-rework candidate and blocked completion. The selection now also orders
-- by the attempt round, choosing the newest verification of the last task.
-- Every other clause, including the whole PARALLEL branch, is unchanged.

-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_integration_candidate_binding_valid;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN (NEW.complex_execution_result_id IS NOT NULL AND NEW.complex_quick_result_id IS NOT NULL)
 OR (NEW.complex_quick_result_id IS NULL) <> (NEW.complex_quick_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NULL) <> (NEW.complex_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NOT NULL AND NOT EXISTS (
	 SELECT 1 FROM cleardev_complex_execution_results AS result
	 JOIN cleardev_complex_execution_runs AS run ON run.id = result.execution_run_id
	 JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_source_candidate_commit_id
	 JOIN cleardev_complex_execution_role_bindings AS builder ON builder.execution_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND' AND builder.builder_slot = 1
	 WHERE result.id = NEW.complex_execution_result_id AND result.integration_candidate_id = NEW.id
	   AND run.development_project_id = NEW.development_project_id
	   AND NEW.requirement_version_id = run.requirement_version_id
	   AND NEW.task_set_version = run.accepted_task_set_version
	   AND NEW.ao_session_id = builder.ao_session_id
	   AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
	   AND (
	     (run.mode = 'STANDARD'
	       AND candidate.commit_sha = NEW.commit_sha
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = verified.task_attempt_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC, attempt.round DESC
	           LIMIT 1
	       ))
	     OR (run.mode = 'PARALLEL'
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           JOIN cleardev_complex_execution_task_attempts AS attempt ON attempt.id = verified.task_attempt_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC, attempt.round DESC
	           LIMIT 1
	       )
	       AND NEW.commit_sha = (
	           SELECT composition.output_commit_sha
	           FROM cleardev_complex_execution_compositions AS composition
	           JOIN cleardev_complex_execution_batches AS batch ON batch.id = composition.batch_id
	           WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
	             AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
	       ))
	   )
 ))
 OR (NEW.complex_quick_result_id IS NOT NULL AND NOT EXISTS (
	    SELECT 1 FROM cleardev_complex_quick_results AS result
	    JOIN cleardev_complex_quick_runs AS run ON run.id = result.quick_run_id
	    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_quick_source_candidate_commit_id
	    JOIN cleardev_complex_quick_role_bindings AS builder ON builder.quick_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND'
	    WHERE result.id = NEW.complex_quick_result_id AND result.integration_candidate_id = NEW.id
	      AND run.development_project_id = NEW.development_project_id
	      AND NEW.requirement_version_id = run.requirement_version_id
	      AND NEW.task_set_version = run.accepted_task_set_version
	      AND NEW.ao_session_id = builder.ao_session_id
	      AND candidate.commit_sha = NEW.commit_sha
	      AND candidate.complex_quick_task_attempt_id IS NOT NULL
	      AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution integration candidate must use its final result and candidate');
END;
-- +goose StatementEnd

-- +goose Down
-- The guard repeats the downstream history checks because goose commits each
-- Down separately.
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_integration_trigger_down_guard;
CREATE TEMP TABLE cleardev_integration_trigger_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_integration_trigger_down_guard_valid BEFORE INSERT ON cleardev_integration_trigger_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_integration_trigger_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM (SELECT task_mapping_id FROM cleardev_complex_execution_verified_candidates GROUP BY task_mapping_id HAVING count(*)>1))
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_integration_trigger_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_integration_candidate_binding_valid;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_integration_candidate_binding_valid
BEFORE INSERT ON cleardev_integration_candidates
WHEN (NEW.complex_execution_result_id IS NOT NULL AND NEW.complex_quick_result_id IS NOT NULL)
 OR (NEW.complex_quick_result_id IS NULL) <> (NEW.complex_quick_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NULL) <> (NEW.complex_source_candidate_commit_id IS NULL)
 OR (NEW.complex_execution_result_id IS NOT NULL AND NOT EXISTS (
	 SELECT 1 FROM cleardev_complex_execution_results AS result
	 JOIN cleardev_complex_execution_runs AS run ON run.id = result.execution_run_id
	 JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_source_candidate_commit_id
	 JOIN cleardev_complex_execution_role_bindings AS builder ON builder.execution_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND' AND builder.builder_slot = 1
	 WHERE result.id = NEW.complex_execution_result_id AND result.integration_candidate_id = NEW.id
	   AND run.development_project_id = NEW.development_project_id
	   AND NEW.requirement_version_id = run.requirement_version_id
	   AND NEW.task_set_version = run.accepted_task_set_version
	   AND NEW.ao_session_id = builder.ao_session_id
	   AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
	   AND (
	     (run.mode = 'STANDARD'
	       AND candidate.commit_sha = NEW.commit_sha
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC
	           LIMIT 1
	       ))
	     OR (run.mode = 'PARALLEL'
	       AND candidate.id = (
	           SELECT verified.candidate_commit_id
	           FROM cleardev_complex_execution_verified_candidates AS verified
	           JOIN cleardev_complex_execution_task_mappings AS task ON task.id = verified.task_mapping_id
	           WHERE task.execution_run_id = run.id
	           ORDER BY task.ordinal DESC
	           LIMIT 1
	       )
	       AND NEW.commit_sha = (
	           SELECT composition.output_commit_sha
	           FROM cleardev_complex_execution_compositions AS composition
	           JOIN cleardev_complex_execution_batches AS batch ON batch.id = composition.batch_id
	           WHERE composition.execution_run_id = run.id AND composition.status = 'COMPOSED'
	             AND batch.ordinal = (SELECT max(ordinal) FROM cleardev_complex_execution_batches WHERE execution_run_id = run.id)
	       ))
	   )
 ))
 OR (NEW.complex_quick_result_id IS NOT NULL AND NOT EXISTS (
	    SELECT 1 FROM cleardev_complex_quick_results AS result
	    JOIN cleardev_complex_quick_runs AS run ON run.id = result.quick_run_id
	    JOIN cleardev_candidate_commits AS candidate ON candidate.id = NEW.complex_quick_source_candidate_commit_id
	    JOIN cleardev_complex_quick_role_bindings AS builder ON builder.quick_run_id = run.id AND builder.role = 'BUILDER' AND builder.status = 'BOUND'
	    WHERE result.id = NEW.complex_quick_result_id AND result.integration_candidate_id = NEW.id
	      AND run.development_project_id = NEW.development_project_id
	      AND NEW.requirement_version_id = run.requirement_version_id
	      AND NEW.task_set_version = run.accepted_task_set_version
	      AND NEW.ao_session_id = builder.ao_session_id
	      AND candidate.commit_sha = NEW.commit_sha
	      AND candidate.complex_quick_task_attempt_id IS NOT NULL
	      AND NEW.dispatch_id IS NULL AND NEW.source_candidate_commit_id IS NULL
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution integration candidate must use its final result and candidate');
END;
-- +goose StatementEnd
