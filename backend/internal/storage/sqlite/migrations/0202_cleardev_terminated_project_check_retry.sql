-- Explicit retry of a settled terminated project check retains its failure.
-- Exit 137 alone does not establish OOM. Existing stopped-Planner gates remain.
-- +goose Up
-- +goose StatementBegin
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
        NEW.retry_ordinal = 0 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery JOIN cleardev_complex_execution_check_runs prior ON prior.id=recovery.target_id WHERE recovery.action='RETRY_CHECK' AND recovery.successor_id=NEW.id AND prior.task_attempt_id=NEW.task_attempt_id AND prior.check_spec_id=NEW.check_spec_id AND prior.candidate_commit_id=NEW.candidate_commit_id AND prior.candidate_commit_sha=NEW.candidate_commit_sha AND ((prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE') OR (prior.status='SETTLED' AND prior.reason_code='CHECK_FAILED' AND prior.result='FAIL' AND prior.exit_code=137 AND prior.timed_out=0 AND prior.settled_at IS NOT NULL AND json_valid(prior.output_summary) AND json_extract(prior.output_summary,'$.policy')='PROJECT_CHECK_FRESH_INSTALL_V2' AND json_extract(prior.output_summary,'$.image')='node@sha256:fbe64f0a038c6117d58d6a160c52994ee586cab9e36812802b564ac2d464bb24' AND json_extract(prior.output_summary,'$.outputTruncated')=0)) AND NEW.retry_ordinal=prior.retry_ordinal+1)
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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
-- +goose StatementEnd
