-- Historical terminal project checks can register an existing task repair.
-- Keep the stopped attempt immutable and admit only the exact settled check,
-- original settled Builder message and persisted next-round restart event.
-- No new table, approval or default budget is introduced.

-- +goose Up
-- +goose StatementBegin
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
  OR (a.status='BLOCKED' AND a.settled_at IS NOT NULL AND a.reason_code IN('CHECKER_UNAVAILABLE','CHECK_FAILED','CHECK_TIMEOUT')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm
    JOIN cleardev_work_items w ON w.id=tm.work_item_id
    JOIN cleardev_complex_execution_runs r ON r.id=tm.execution_run_id
    JOIN cleardev_complex_execution_agent_steps original ON original.id=a.agent_step_id
    JOIN cleardev_candidate_commits c ON c.complex_execution_task_attempt_id=a.id
    JOIN cleardev_complex_execution_check_runs failed ON failed.task_attempt_id=a.id AND failed.candidate_commit_id=c.id AND failed.candidate_commit_sha=c.commit_sha
    JOIN cleardev_complex_execution_check_specs spec ON spec.id=failed.check_spec_id
    JOIN cleardev_project_events event ON event.subject_id=w.id
    WHERE tm.id=NEW.task_mapping_id AND r.id=NEW.execution_run_id AND r.mode='STANDARD' AND r.status='ACCEPTED' AND r.settled_at IS NULL
     AND json_extract(r.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
     AND w.state='RUNNING' AND w.rework_count=NEW.round
     AND original.send_status='SETTLED' AND original.turn_id IS NOT NULL
     AND spec.execution_run_id=r.id AND spec.check_kind='REQUIRED_CHECK'
     AND failed.settled_at IS NOT NULL AND failed.reason_code=a.reason_code AND (failed.status='FAILED' OR (failed.status='SETTLED' AND failed.result='FAIL'))
     AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='builder-first-check:'||failed.id))
  OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=a.id AND recovery.action='CONTINUE_BUILDER' AND recovery.execution_run_id=NEW.execution_run_id AND a.status IN('BLOCKED','NEEDS_HUMAN'))
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
-- +goose StatementEnd

-- +goose Down
-- Refuse to remove the rule while a saved next round relies on it.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_builder_check_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_builder_check_down_guard_valid BEFORE INSERT ON cleardev_builder_check_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT,'downgrade refuses saved Builder check repair rounds'); END;
INSERT INTO cleardev_builder_check_down_guard SELECT CASE WHEN EXISTS(
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
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_builder_check_down_guard;
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
-- +goose StatementEnd
