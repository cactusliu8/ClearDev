-- 0188: human-granted repair rounds extend the bounded attempt horizon.
--
-- A coordination repair grant recorded through the native Human Authority
-- channel (AUTHORIZE_COORDINATION_REPAIR) authorizes one repair round pair
-- beyond the default six rounds of one task. Three shipped guards kept that
-- horizon fixed regardless of any grant: the attempt-insert round bound, the
-- blocked-attempt recovery family and the coordination barrier view. This
-- migration lets those exact guards honor the already-recorded human
-- authorization (the task builder budget's authorized_extra_turns and the
-- settled repair decision) for rounds 7-12. Every other clause of the triggers
-- and the view is byte-identical to the previous shipped definitions; no
-- recorded attempt, failure or history changes.

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
 OR (NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=NEW.execution_run_id) AND NEW.round NOT IN(0,1,2,3,4,5,6) AND NOT (NEW.round BETWEEN 7 AND 12 AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets grant WHERE grant.execution_run_id=NEW.execution_run_id AND grant.complex_execution_task_id=NEW.task_mapping_id AND grant.role_kind='BUILDER' AND grant.authorized_extra_turns>0)))
 OR (NEW.batch_id IS NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE id=NEW.execution_run_id AND mode='STANDARD'))
 OR (NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b JOIN cleardev_complex_execution_runs r ON r.id=b.execution_run_id JOIN cleardev_complex_execution_task_mappings t ON t.execution_run_id=r.id
 WHERE b.id=NEW.batch_id AND r.id=NEW.execution_run_id AND r.mode='PARALLEL' AND b.status='RUNNING' AND b.common_base_sha=NEW.base_commit_sha AND t.id=NEW.task_mapping_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key)))

 OR CASE WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING' ) THEN NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
 AND EXISTS(SELECT 1 FROM cleardev_work_items item WHERE item.complex_execution_task_id=effective.id
   AND item.rework_count=NEW.round AND ((NEW.round=0 AND item.state='PLANNED') OR
    (NEW.round>0 AND item.state='RUNNING' AND EXISTS(SELECT 1 FROM cleardev_project_events event
      WHERE event.subject_id=item.id AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED'
      AND event.reason_text='planner-engineering:'||revision.event_id))))
 AND (NEW.round=0 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts previous
   WHERE previous.task_mapping_id=NEW.task_mapping_id AND previous.round=NEW.round-1 AND previous.settled_at IS NOT NULL))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies dependency
   JOIN cleardev_planner_runtime_effective_tasks prerequisite ON prerequisite.id=dependency.depends_on_mapping_id
   WHERE dependency.task_mapping_id=NEW.task_mapping_id AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_verified_candidates verified
    JOIN cleardev_complex_execution_task_attempts checked ON checked.id=verified.task_attempt_id
    WHERE verified.task_mapping_id=prerequisite.id
     AND checked.round>=COALESCE(json_extract(prerequisite.task_packet_json,'$.runtimeRevision.firstRound'),0)
     AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts later
      WHERE later.task_mapping_id=prerequisite.id AND later.round>checked.round)))
 AND NEW.base_commit_sha=COALESCE((SELECT candidate.commit_sha
   FROM cleardev_complex_execution_task_attempts prior JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=prior.id
   WHERE prior.execution_run_id=NEW.execution_run_id AND prior.builder_role_binding_id=NEW.builder_role_binding_id
    AND prior.batch_id IS NULL AND prior.settled_at IS NOT NULL
   ORDER BY prior.dispatched_at DESC, candidate.created_at DESC LIMIT 1),
   (SELECT base_commit_sha FROM cleardev_complex_execution_role_bindings WHERE id=NEW.builder_role_binding_id))) ELSE ((NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_mapping_id AND a.round=NEW.round-1 AND a.base_commit_sha=NEW.base_commit_sha AND (NEW.batch_id IS NULL OR a.batch_id=NEW.batch_id)
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
  OR (a.status='BLOCKED' AND a.reason_code='BUILDER_BLOCKED' AND a.settled_at IS NOT NULL AND EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_task_amendments revision
    JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
    JOIN cleardev_work_items item ON item.complex_execution_task_id=revision.task_mapping_id
    JOIN cleardev_project_events event ON event.subject_id=item.id
    WHERE revision.task_mapping_id=NEW.task_mapping_id AND revision.execution_run_id=NEW.execution_run_id
    AND json_extract(revision.task_packet_json,'$.runtimeRevision.firstRound')=NEW.round
    AND json_extract(revision.task_packet_json,'$.schemaVersion')=4
    AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
    AND item.state='RUNNING' AND item.rework_count=NEW.round
    AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='planner-engineering:'||revision.event_id))
  OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=a.id AND recovery.action='CONTINUE_BUILDER' AND recovery.execution_run_id=NEW.execution_run_id AND a.status IN('BLOCKED','NEEDS_HUMAN'))
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
  OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN')
  OR (a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='DEVELOPMENT')
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_mapping_id AND p.execution_run_id=NEW.execution_run_id)))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))) END
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d LEFT JOIN cleardev_complex_execution_verified_candidates v ON v.task_mapping_id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND v.id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies d JOIN cleardev_complex_execution_task_mappings t ON t.id=d.depends_on_mapping_id WHERE d.task_mapping_id=NEW.task_mapping_id AND NEW.batch_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_batches b WHERE b.execution_run_id=NEW.execution_run_id AND EXISTS(SELECT 1 FROM json_each(b.task_keys_json) WHERE value=t.plan_task_key) AND b.ordinal<(SELECT ordinal FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id)))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
     AND NOT(OLD.status IN('BLOCKED','NEEDS_HUMAN') AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action IN('RETRY_REVIEW','RETRY_CHECK') AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=OLD.id AND recovery.action='RETRY_BUILDER_SESSION' AND recovery.original_stopped_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND NEW.status='REWORK' AND OLD.reason_code IN('BUILDER_BUDGET_EXHAUSTED','BUILDER_BLOCKED') AND OLD.settled_at IS NOT NULL
       AND EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE execution_run_id=OLD.execution_run_id AND complex_execution_task_id=OLD.task_mapping_id AND role_kind='BUILDER' AND authorized_extra_turns>0)) AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='CHECKER_UNAVAILABLE'
      AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
        SELECT 1 FROM cleardev_project_check_recoveries recovery
        JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.retry_check_run_id
        WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1
          AND recovery.original_attempt_settled_at=OLD.settled_at))
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id WHERE json_extract(r.binding_json,'$.dispatchId')=OLD.id AND h.logical_step_id=OLD.agent_step_id AND r.original_stopped_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP VIEW cleardev_planner_runtime_barriers;
CREATE VIEW cleardev_planner_runtime_barriers AS
SELECT event.id AS event_id,event.execution_run_id,
       COALESCE(decision.reason_code,'PLANNER_COORDINATION_PENDING') AS reason_code
FROM cleardev_planner_runtime_events event
LEFT JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=event.id
WHERE (decision.event_id IS NULL OR decision.outcome NOT IN('CONTINUE','AMEND_REMAINING')
 OR (decision.outcome='AMEND_REMAINING' AND
     (SELECT count(*) FROM cleardev_planner_runtime_task_amendments amendment WHERE amendment.event_id=event.id)
       <>json_array_length(decision.result_json,'$.amendments')))
AND NOT (decision.source='CONTROL_PLANE' AND decision.outcome='STOP'
 AND EXISTS(SELECT 1 FROM cleardev_human_decision_requests grant
   WHERE grant.decision_kind='AUTHORIZE_COORDINATION_REPAIR' AND grant.status='RESOLVED' AND grant.decision='APPROVE'
     AND json_extract(grant.binding_json,'$.eventId')=event.id))

-- +goose StatementEnd
-- +goose Down
-- Existing execution history cannot be downgraded past its recorded
-- human grants; refuse before touching any definition.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_human_repair_rounds_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_human_repair_rounds_down_guard_valid BEFORE INSERT ON cleardev_human_repair_rounds_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT,'downgrade refuses human-granted repair rounds'); END;
INSERT INTO cleardev_human_repair_rounds_down_guard SELECT CASE WHEN EXISTS(
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
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE round>6)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests WHERE decision_kind='AUTHORIZE_COORDINATION_REPAIR')
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_human_repair_rounds_down_guard;
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

 OR CASE WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING' ) THEN NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
 AND EXISTS(SELECT 1 FROM cleardev_work_items item WHERE item.complex_execution_task_id=effective.id
   AND item.rework_count=NEW.round AND ((NEW.round=0 AND item.state='PLANNED') OR
    (NEW.round>0 AND item.state='RUNNING' AND EXISTS(SELECT 1 FROM cleardev_project_events event
      WHERE event.subject_id=item.id AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED'
      AND event.reason_text='planner-engineering:'||revision.event_id))))
 AND (NEW.round=0 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts previous
   WHERE previous.task_mapping_id=NEW.task_mapping_id AND previous.round=NEW.round-1 AND previous.settled_at IS NOT NULL))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_dependencies dependency
   JOIN cleardev_planner_runtime_effective_tasks prerequisite ON prerequisite.id=dependency.depends_on_mapping_id
   WHERE dependency.task_mapping_id=NEW.task_mapping_id AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_verified_candidates verified
    JOIN cleardev_complex_execution_task_attempts checked ON checked.id=verified.task_attempt_id
    WHERE verified.task_mapping_id=prerequisite.id
     AND checked.round>=COALESCE(json_extract(prerequisite.task_packet_json,'$.runtimeRevision.firstRound'),0)
     AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts later
      WHERE later.task_mapping_id=prerequisite.id AND later.round>checked.round)))
 AND NEW.base_commit_sha=COALESCE((SELECT candidate.commit_sha
   FROM cleardev_complex_execution_task_attempts prior JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=prior.id
   WHERE prior.execution_run_id=NEW.execution_run_id AND prior.builder_role_binding_id=NEW.builder_role_binding_id
    AND prior.batch_id IS NULL AND prior.settled_at IS NOT NULL
   ORDER BY prior.dispatched_at DESC, candidate.created_at DESC LIMIT 1),
   (SELECT base_commit_sha FROM cleardev_complex_execution_role_bindings WHERE id=NEW.builder_role_binding_id))) ELSE ((NEW.round>0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a WHERE a.task_mapping_id=NEW.task_mapping_id AND a.round=NEW.round-1 AND a.base_commit_sha=NEW.base_commit_sha AND (NEW.batch_id IS NULL OR a.batch_id=NEW.batch_id)
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
  OR (a.status='BLOCKED' AND a.reason_code='BUILDER_BLOCKED' AND a.settled_at IS NOT NULL AND EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_task_amendments revision
    JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=revision.event_id
    JOIN cleardev_work_items item ON item.complex_execution_task_id=revision.task_mapping_id
    JOIN cleardev_project_events event ON event.subject_id=item.id
    WHERE revision.task_mapping_id=NEW.task_mapping_id AND revision.execution_run_id=NEW.execution_run_id
    AND json_extract(revision.task_packet_json,'$.runtimeRevision.firstRound')=NEW.round
    AND json_extract(revision.task_packet_json,'$.schemaVersion')=4
    AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING'
    AND item.state='RUNNING' AND item.rework_count=NEW.round
    AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='planner-engineering:'||revision.event_id))
  OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.dispatch_id=a.id AND recovery.action='CONTINUE_BUILDER' AND recovery.execution_run_id=NEW.execution_run_id AND a.status IN('BLOCKED','NEEDS_HUMAN'))
  OR (a.status='VERIFIED' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings tm JOIN cleardev_work_items w ON w.id=tm.work_item_id WHERE tm.id=NEW.task_mapping_id AND w.rework_count>=NEW.round))
  OR EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='HUMAN_EXTRA' AND a.status='NEEDS_HUMAN')
  OR (a.status='NEEDS_HUMAN' AND a.reason_code='CANDIDATE_INVALID'
   AND EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots s WHERE s.dispatch_id=NEW.id AND s.attempt_kind='DEVELOPMENT')
   AND EXISTS(SELECT 1 FROM cleardev_candidate_commits c JOIN cleardev_complex_execution_task_attempts p ON p.id=c.complex_execution_task_attempt_id WHERE p.task_mapping_id=NEW.task_mapping_id AND p.execution_run_id=NEW.execution_run_id)))))
 OR (NEW.round=0 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id WHERE t.id=NEW.task_mapping_id
 AND ((r.mode='STANDARD' AND EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b WHERE b.id=NEW.builder_role_binding_id AND ((t.ordinal=0 AND b.base_commit_sha=NEW.base_commit_sha) OR (t.ordinal>0 AND NEW.base_commit_sha=(SELECT c.commit_sha FROM cleardev_complex_execution_verified_candidates v JOIN cleardev_complex_execution_task_mappings p ON p.id=v.task_mapping_id JOIN cleardev_candidate_commits c ON c.id=v.candidate_commit_id WHERE p.execution_run_id=t.execution_run_id AND p.ordinal<t.ordinal ORDER BY p.ordinal DESC LIMIT 1)))))
 OR (r.mode='PARALLEL' AND NEW.base_commit_sha=(SELECT common_base_sha FROM cleardev_complex_execution_batches WHERE id=NEW.batch_id AND status='RUNNING')))))) END
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
 AND NOT(OLD.status='BLOCKED' AND NEW.status='OBSERVED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_project_check_continuations continuation JOIN cleardev_complex_execution_check_runs retry ON retry.id=continuation.retry_check_run_id WHERE retry.task_attempt_id=OLD.id AND retry.status='PENDING' AND retry.retry_ordinal=1 AND continuation.blocked_attempt_settled_at=OLD.settled_at)) AND NOT(OLD.status='BLOCKED' AND OLD.reason_code='BUILDER_SPAWN_FAILED' AND NEW.status='RUNNING' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(SELECT 1 FROM cleardev_builder_replacement_handoffs h JOIN cleardev_builder_replacement_requests r ON r.id=h.request_id WHERE json_extract(r.binding_json,'$.dispatchId')=OLD.id AND h.logical_step_id=OLD.agent_step_id AND r.original_stopped_at=OLD.settled_at)))
 OR (OLD.status='PENDING' AND NEW.status NOT IN('RUNNING','FAILED','BLOCKED','NEEDS_HUMAN'))
 OR (OLD.status='RUNNING' AND NEW.status NOT IN('OBSERVED','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='OBSERVED' AND NEW.status NOT IN('REVIEWING','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
 OR (OLD.status='REVIEWING' AND NEW.status NOT IN('VERIFIED','REWORK','BLOCKED','NEEDS_HUMAN','FAILED'))
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt is immutable or invalid'); END;
DROP VIEW cleardev_planner_runtime_barriers;
CREATE VIEW cleardev_planner_runtime_barriers AS
SELECT event.id AS event_id,event.execution_run_id,
       COALESCE(decision.reason_code,'PLANNER_COORDINATION_PENDING') AS reason_code
FROM cleardev_planner_runtime_events event
LEFT JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=event.id
WHERE decision.event_id IS NULL OR decision.outcome NOT IN('CONTINUE','AMEND_REMAINING')
 OR (decision.outcome='AMEND_REMAINING' AND
     (SELECT count(*) FROM cleardev_planner_runtime_task_amendments amendment WHERE amendment.event_id=event.id)
       <>json_array_length(decision.result_json,'$.amendments'))

-- +goose StatementEnd
