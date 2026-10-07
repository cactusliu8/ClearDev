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

 OR CASE WHEN EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews review
 JOIN cleardev_agent_step_results result ON result.id=review.result_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=NEW.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN json_each(review.review_packet_json,'$.tasks') frozen_task
 JOIN cleardev_project_events event ON event.subject_id=item.id
 WHERE review.execution_run_id=NEW.execution_run_id AND review.status='SETTLED' AND review.verdict IN('REWORK','BLOCKED')
 AND json_extract(review.review_packet_json,'$.stageRepairRouting')=1
 AND json_extract(frozen_task.value,'$.id')=task.id
 AND (json_extract(result.raw_message_text,'$.repairTaskKey')=task.plan_task_key OR
   (json_type(result.raw_message_text,'$.repairTaskKey') IS NULL AND json_array_length(review.review_packet_json,'$.tasks')=1))
 AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='builder-first-final:'||review.id
 AND item.state='RUNNING' AND item.rework_count=NEW.round
 AND NEW.round=json_extract(frozen_task.value,'$.reworkCount')+1
 AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews newer WHERE newer.execution_run_id=review.execution_run_id AND newer.rowid>review.rowid)
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts prior WHERE prior.task_mapping_id=task.id AND prior.round=NEW.round-1 AND prior.status='VERIFIED' AND prior.settled_at IS NOT NULL)
) THEN NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews review
 JOIN cleardev_agent_step_results result ON result.id=review.result_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=NEW.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN json_each(review.review_packet_json,'$.tasks') frozen_task
 JOIN cleardev_project_events event ON event.subject_id=item.id
 WHERE review.execution_run_id=NEW.execution_run_id AND review.status='SETTLED' AND review.verdict IN('REWORK','BLOCKED')
 AND json_extract(review.review_packet_json,'$.stageRepairRouting')=1
 AND json_extract(frozen_task.value,'$.id')=task.id
 AND (json_extract(result.raw_message_text,'$.repairTaskKey')=task.plan_task_key OR
   (json_type(result.raw_message_text,'$.repairTaskKey') IS NULL AND json_array_length(review.review_packet_json,'$.tasks')=1))
 AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED' AND event.reason_text='builder-first-final:'||review.id
 AND item.state='RUNNING' AND item.rework_count=NEW.round
 AND NEW.round=json_extract(frozen_task.value,'$.reworkCount')+1
 AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews newer WHERE newer.execution_run_id=review.execution_run_id AND newer.rowid>review.rowid)
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts prior WHERE prior.task_mapping_id=task.id AND prior.round=NEW.round-1 AND prior.status='VERIFIED' AND prior.settled_at IS NOT NULL)
 AND NEW.base_commit_sha=review.candidate_commit_sha) WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING' ) THEN NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
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
    JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
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
DROP TRIGGER cleardev_complex_execution_integration_candidate_binding_valid;
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
	           ORDER BY CASE WHEN json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' THEN attempt.dispatched_at END DESC, task.ordinal DESC, attempt.round DESC
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
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_stage_repair_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_stage_repair_down_guard SELECT NOT (EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions)
 OR EXISTS(SELECT 1 FROM cleardev_product_goals)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs));
DROP TABLE cleardev_stage_repair_down_guard;
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
 JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
 JOIN cleardev_complex_execution_runs run ON run.id=revision.execution_run_id
 WHERE effective.id=NEW.task_mapping_id AND run.id=NEW.execution_run_id AND run.mode='STANDARD'
 AND json_extract(effective.task_packet_json,'$.schemaVersion')=4
 AND COALESCE(json_extract(effective.task_packet_json,'$.runtimeRevision.firstRound'),0)=NEW.round
 AND decision.source='PLANNER' AND decision.outcome='AMEND_REMAINING' ) THEN NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_effective_tasks effective
 JOIN cleardev_planner_runtime_task_amendments revision ON revision.task_mapping_id=effective.id AND revision.task_packet_sha256=effective.task_packet_sha256
 JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
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
    JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=revision.event_id
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
DROP TRIGGER cleardev_complex_execution_integration_candidate_binding_valid;
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
