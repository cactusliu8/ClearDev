-- 0189: human-authorized repair rounds widen the review-criteria ceiling.
--
-- The amendment-insert trigger bounded an amended package's review criteria to
-- twelve. A human-authorized repair of an already full task (like the live
-- test2 stock-foundation task at exactly twelve criteria) must still be able to
-- land its additive revision, which adds at most one amendment's worth (six)
-- of criteria. This migration lets that exact guard honor the same human grant
-- signal the round/window guards use (the task builder budget's
-- authorized_extra_turns): the ceiling becomes 18 with a grant and stays 12
-- without one. Every other clause is byte-identical to the previous shipped
-- definition; no recorded row, attempt or history changes.

-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_planner_runtime_amendment_insert;
CREATE TRIGGER cleardev_planner_runtime_amendment_insert
BEFORE INSERT ON cleardev_planner_runtime_task_amendments
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_task_amendments old WHERE old.id=NEW.id
             OR (old.event_id=NEW.event_id AND old.task_mapping_id=NEW.task_mapping_id)
             OR (old.task_mapping_id=NEW.task_mapping_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_task_amendments WHERE task_mapping_id=NEW.task_mapping_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_effective_tasks task
    JOIN cleardev_work_items item ON item.id=task.work_item_id
    JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=NEW.event_id
    JOIN json_each(decision.result_json,'$.amendments') amendment ON json_extract(amendment.value,'$.taskKey')=task.plan_task_key
    WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=NEW.execution_run_id
      AND decision.execution_run_id=NEW.execution_run_id AND decision.outcome='AMEND_REMAINING' AND decision.source='PLANNER'
      AND ((item.state='PLANNED' AND item.rework_count=0
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=task.id))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4 AND item.state IN('BLOCKED','REVIEW','REWORK')
       AND json_extract(NEW.task_packet_json,'$.runtimeRevision.firstRound')=item.rework_count+1
       AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE execution_run_id=task.execution_run_id AND status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))))
      AND task.task_packet_sha256=NEW.previous_package_sha256 AND NEW.task_packet_sha256<>NEW.previous_package_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.eventId')=NEW.event_id
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.decisionSha256')=decision.result_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.previousPackageSha256')=NEW.previous_package_sha256
      AND ((json_extract(task.task_packet_json,'$.schemaVersion')<>4 AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision'))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4
       AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')
       AND json_remove(NEW.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.writePaths')=json_extract(task.task_packet_json,'$.projectExecution.basis.writePaths')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.checks')=json_extract(task.task_packet_json,'$.projectExecution.basis.checks')
       AND (json_type(amendment.value,'$.executionBasis') IS NULL AND json_extract(NEW.task_packet_json,'$.projectExecution')=json_extract(task.task_packet_json,'$.projectExecution')
        OR json_extract(NEW.task_packet_json,'$.projectExecution.basis')=json_extract(amendment.value,'$.executionBasis'))))
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria') BETWEEN 1 AND (12 + CASE WHEN EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets grant WHERE grant.execution_run_id=NEW.execution_run_id AND grant.complex_execution_task_id=NEW.task_mapping_id AND grant.role_kind='BUILDER' AND grant.authorized_extra_turns>0) THEN 6 ELSE 0 END)
      AND json_array_length(amendment.value,'$.additionalReviewCriteria') BETWEEN 0 AND 6
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria')=json_array_length(task.task_packet_json,'$.reviewCriteria')+json_array_length(amendment.value,'$.additionalReviewCriteria')
      AND NOT EXISTS(SELECT 1 FROM json_each(task.task_packet_json,'$.reviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||criterion.key||']') IS NOT criterion.value)
      AND NOT EXISTS(SELECT 1 FROM json_each(amendment.value,'$.additionalReviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||(json_array_length(task.task_packet_json,'$.reviewCriteria')+criterion.key)||']') IS NOT criterion.value)
 )
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;
-- +goose StatementEnd
-- +goose Down
-- Existing execution history cannot be downgraded past its recorded
-- human grants; refuse before touching any definition.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_review_criteria_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_review_criteria_down_guard_valid BEFORE INSERT ON cleardev_review_criteria_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT,'downgrade refuses human-authorized review criteria'); END;
INSERT INTO cleardev_review_criteria_down_guard SELECT CASE WHEN EXISTS(
 SELECT 1 FROM cleardev_complex_execution_task_attempts next JOIN cleardev_complex_execution_task_attempts prior
 ON prior.task_mapping_id=next.task_mapping_id AND prior.round=next.round-1
 WHERE prior.status='BLOCKED' AND prior.reason_code IN('CHECKER_UNAVAILABLE','CHECK_FAILED','CHECK_TIMEOUT')
)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_requests)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_grants)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_aliases)
 OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_observations)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_fences)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_operations)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_operation_ends)
 OR EXISTS(SELECT 1 FROM cleardev_builder_handoff_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE status='RETIRED_BEFORE_SEND')
 OR EXISTS(SELECT 1 FROM cleardev_planning_extra_requests)
 OR EXISTS(SELECT 1 FROM cleardev_planning_extra_grants)
 OR EXISTS(SELECT 1 FROM cleardev_planning_extra_continuations)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_planning_step_recoveries)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_reopens)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_checks)
 OR EXISTS(SELECT 1 FROM cleardev_planner_answers)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config,'{}'),'$.cleardev')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budget_occupancies)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests WHERE decision_kind='AUTHORIZE_COORDINATION_REPAIR')
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_review_criteria_down_guard;
DROP TRIGGER cleardev_planner_runtime_amendment_insert;
CREATE TRIGGER cleardev_planner_runtime_amendment_insert
BEFORE INSERT ON cleardev_planner_runtime_task_amendments
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_task_amendments old WHERE old.id=NEW.id
             OR (old.event_id=NEW.event_id AND old.task_mapping_id=NEW.task_mapping_id)
             OR (old.task_mapping_id=NEW.task_mapping_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_task_amendments WHERE task_mapping_id=NEW.task_mapping_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_effective_tasks task
    JOIN cleardev_work_items item ON item.id=task.work_item_id
    JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=NEW.event_id
    JOIN json_each(decision.result_json,'$.amendments') amendment ON json_extract(amendment.value,'$.taskKey')=task.plan_task_key
    WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=NEW.execution_run_id
      AND decision.execution_run_id=NEW.execution_run_id AND decision.outcome='AMEND_REMAINING' AND decision.source='PLANNER'
      AND ((item.state='PLANNED' AND item.rework_count=0
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=task.id))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4 AND item.state IN('BLOCKED','REVIEW','REWORK')
       AND json_extract(NEW.task_packet_json,'$.runtimeRevision.firstRound')=item.rework_count+1
       AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE execution_run_id=task.execution_run_id AND status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))))
      AND task.task_packet_sha256=NEW.previous_package_sha256 AND NEW.task_packet_sha256<>NEW.previous_package_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.eventId')=NEW.event_id
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.decisionSha256')=decision.result_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.previousPackageSha256')=NEW.previous_package_sha256
      AND ((json_extract(task.task_packet_json,'$.schemaVersion')<>4 AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision'))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4
       AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution')
       AND json_remove(NEW.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.writePaths')=json_extract(task.task_packet_json,'$.projectExecution.basis.writePaths')
       AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.checks')=json_extract(task.task_packet_json,'$.projectExecution.basis.checks')
       AND (json_type(amendment.value,'$.executionBasis') IS NULL AND json_extract(NEW.task_packet_json,'$.projectExecution')=json_extract(task.task_packet_json,'$.projectExecution')
        OR json_extract(NEW.task_packet_json,'$.projectExecution.basis')=json_extract(amendment.value,'$.executionBasis'))))
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria') BETWEEN 1 AND 12
      AND json_array_length(amendment.value,'$.additionalReviewCriteria') BETWEEN 0 AND 6
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria')=json_array_length(task.task_packet_json,'$.reviewCriteria')+json_array_length(amendment.value,'$.additionalReviewCriteria')
      AND NOT EXISTS(SELECT 1 FROM json_each(task.task_packet_json,'$.reviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||criterion.key||']') IS NOT criterion.value)
      AND NOT EXISTS(SELECT 1 FROM json_each(amendment.value,'$.additionalReviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||(json_array_length(task.task_packet_json,'$.reviewCriteria')+criterion.key)||']') IS NOT criterion.value)
 )
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;
-- +goose StatementEnd
