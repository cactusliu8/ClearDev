-- A failure bridge requests bounded engineering judgment; it does not grant
-- paths, change a result, reopen a task or authorize a provider message.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_failure_coordination_sources (
 event_id TEXT PRIMARY KEY REFERENCES cleardev_planner_runtime_events(id) DEFERRABLE INITIALLY DEFERRED,
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 dispatch_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_task_attempts(id),
 source_step_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_agent_steps(id),
 source_message_sha256 TEXT NOT NULL CHECK(length(source_message_sha256)=64),
 working_tree_sha256 TEXT NOT NULL CHECK(length(working_tree_sha256)=64 AND working_tree_sha256 NOT GLOB '*[^0-9a-f]*'),
 failure_receipt_id TEXT REFERENCES cleardev_workflow_failure_receipts(id),
 bridge_kind TEXT NOT NULL CHECK(bridge_kind IN ('BUILDER_DIAGNOSIS','REPEATED_HANDOFF')),
 created_at TIMESTAMP NOT NULL
);
CREATE TRIGGER cleardev_failure_coordination_immutable BEFORE UPDATE ON cleardev_failure_coordination_sources
BEGIN SELECT RAISE(ABORT,'failure coordination source is immutable'); END;
CREATE TRIGGER cleardev_failure_coordination_keep BEFORE DELETE ON cleardev_failure_coordination_sources
BEGIN SELECT RAISE(ABORT,'failure coordination source history is immutable'); END;
CREATE TRIGGER cleardev_failure_coordination_no_replace BEFORE INSERT ON cleardev_failure_coordination_sources
WHEN EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources WHERE event_id=NEW.event_id OR dispatch_id=NEW.dispatch_id OR source_step_id=NEW.source_step_id)
BEGIN SELECT RAISE(ABORT,'failure coordination cannot replace its source'); END;
CREATE TRIGGER cleardev_failure_coordination_source_valid BEFORE INSERT ON cleardev_failure_coordination_sources
WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_complex_execution_agent_steps step ON step.id=attempt.agent_step_id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=attempt.builder_role_binding_id
 JOIN sessions session ON session.id=binding.ao_session_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_development_projects requirement ON requirement.id=run.development_project_id
 JOIN cleardev_work_items item ON item.complex_execution_task_id=attempt.task_mapping_id
 WHERE run.id=NEW.execution_run_id AND attempt.id=NEW.dispatch_id AND step.id=NEW.source_step_id
 AND NEW.event_id=attempt.id||':planner-coordination'
 AND run.mode='STANDARD' AND run.status='ACCEPTED' AND run.settled_at IS NULL AND run.reason_code=''
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
 AND requirement.cancelled_at IS NULL AND requirement.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND attempt.status IN ('BLOCKED','NEEDS_HUMAN') AND attempt.settled_at IS NOT NULL
 AND item.state IN ('BLOCKED','NEEDS_HUMAN') AND item.rework_count=attempt.round
 AND attempt.round=(SELECT max(round) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=attempt.task_mapping_id)
 AND step.send_status='SETTLED' AND step.step_kind='BUILDER_TASK' AND step.request_id=attempt.id
 AND step.role_binding_id=binding.id AND binding.status='BOUND' AND binding.role='BUILDER'
 AND session.is_terminated=0 AND session.activity_state='idle' AND session.project_id=requirement.ao_project_id
 AND NOT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=session.id AND state NOT IN ('completed','failed','interrupted'))
 AND step.message_sha256=NEW.source_message_sha256 AND json_extract(step.final_message_text,'$.kind')='BUILDER_RESULT'
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active WHERE active.execution_run_id=run.id AND active.status IN ('PENDING','RUNNING','OBSERVED','REVIEWING'))
 AND ((NEW.bridge_kind='BUILDER_DIAGNOSIS' AND attempt.reason_code='BUILDER_BLOCKED' AND json_extract(step.final_message_text,'$.outcome')='BLOCKED' AND NEW.failure_receipt_id IS NULL)
 OR (NEW.bridge_kind='REPEATED_HANDOFF' AND attempt.reason_code='CANDIDATE_INVALID'
   AND json_extract(step.final_message_text,'$.outcome')='CANDIDATE_READY'
   AND NOT EXISTS(SELECT 1 FROM cleardev_candidate_commits WHERE complex_execution_task_attempt_id=attempt.id)
   AND EXISTS(SELECT 1 FROM cleardev_workflow_failure_receipts receipt
      WHERE receipt.id=NEW.failure_receipt_id AND receipt.execution_run_id=run.id AND receipt.source_id=attempt.id
      AND receipt.source_kind='CANDIDATE_HANDOFF' AND json_extract(receipt.receipt_json,'$.beforePublish')=1
      AND json_extract(receipt.receipt_json,'$.problemCode') IN ('NO_IMPLEMENTATION_CHANGE','SOURCE_LIMIT','HANDOFF_CONTENT'))
   AND EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery
      JOIN cleardev_complex_execution_task_attempts previous ON previous.id=recovery.dispatch_id
      WHERE recovery.execution_run_id=run.id AND recovery.action='CONTINUE_BUILDER'
      AND previous.task_mapping_id=attempt.task_mapping_id AND previous.round<attempt.round AND recovery.original_reason='CANDIDATE_INVALID')))
) BEGIN SELECT RAISE(ABORT,'failure coordination requires the exact settled safe original source'); END;


-- Keep installed historical guards, including already deployed 0203-0212 human
-- authority and Reviewer sources. A valid new bridge chooses the stricter new
-- branch; ordinary sources still execute exactly the saved historical predicate.
CREATE TABLE cleardev_failure_prior_guards (
 name TEXT PRIMARY KEY NOT NULL,
 original_sql TEXT NOT NULL,
 bridge_predicate TEXT NOT NULL
);
INSERT INTO cleardev_failure_prior_guards SELECT name,sql,'EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b WHERE b.event_id=NEW.id AND b.execution_run_id=NEW.execution_run_id)' FROM sqlite_schema WHERE type='trigger' AND name='cleardev_planner_runtime_event_insert';
INSERT INTO cleardev_failure_prior_guards SELECT name,sql,'EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b WHERE b.event_id=NEW.event_id AND b.execution_run_id=NEW.execution_run_id)' FROM sqlite_schema WHERE type='trigger' AND name='cleardev_planner_runtime_amendment_insert';
INSERT INTO cleardev_failure_prior_guards SELECT name,sql,'EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b WHERE b.event_id=NEW.event_id AND b.execution_run_id=NEW.execution_run_id)' FROM sqlite_schema WHERE type='trigger' AND name='cleardev_planner_runtime_decision_insert';
INSERT INTO cleardev_failure_prior_guards SELECT name,sql,'EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b JOIN cleardev_planner_runtime_task_amendments a ON a.event_id=b.event_id WHERE b.execution_run_id=NEW.execution_run_id AND a.execution_run_id=b.execution_run_id AND a.task_mapping_id=NEW.task_mapping_id AND json_extract(a.task_packet_json,''$.runtimeRevision.firstRound'')=NEW.round)' FROM sqlite_schema WHERE type='trigger' AND name='cleardev_complex_execution_attempt_insert_valid';
CREATE TEMP TABLE failure_guard_shape(ok INTEGER CHECK(ok=1));
INSERT INTO failure_guard_shape SELECT CASE WHEN count(*)=4 AND min(
 instr(original_sql,'WHEN ')>0 AND instr(original_sql,'BEGIN SELECT RAISE(ABORT,')>instr(original_sql,'WHEN ')
 AND instr(substr(original_sql,instr(original_sql,'BEGIN SELECT RAISE(ABORT,')+1),'BEGIN SELECT RAISE(ABORT,')=0
) THEN 1 ELSE 0 END FROM cleardev_failure_prior_guards;
DROP TABLE failure_guard_shape;
CREATE TRIGGER cleardev_failure_prior_guard_immutable BEFORE UPDATE ON cleardev_failure_prior_guards
BEGIN SELECT RAISE(ABORT,'original failure-routing guard is immutable'); END;
CREATE TRIGGER cleardev_failure_prior_guard_keep BEFORE DELETE ON cleardev_failure_prior_guards
BEGIN SELECT RAISE(ABORT,'original failure-routing guard must be retained'); END;
CREATE TRIGGER cleardev_failure_prior_guard_no_insert BEFORE INSERT ON cleardev_failure_prior_guards
BEGIN SELECT RAISE(ABORT,'original failure-routing guards are sealed'); END;

PRAGMA writable_schema=ON;
UPDATE sqlite_schema SET sql=(
 SELECT substr(original_sql,1,instr(original_sql,'WHEN ')-1)
   ||'WHEN NOT ('||bridge_predicate||') AND ('
   ||substr(original_sql,instr(original_sql,'WHEN ')+5,instr(original_sql,'BEGIN SELECT RAISE(ABORT,')-instr(original_sql,'WHEN ')-5)
   ||') '||substr(original_sql,instr(original_sql,'BEGIN SELECT RAISE(ABORT,'))
 FROM cleardev_failure_prior_guards p WHERE p.name=sqlite_schema.name
) WHERE type='trigger' AND name IN (SELECT name FROM cleardev_failure_prior_guards);
PRAGMA writable_schema=RESET;

CREATE TRIGGER cleardev_unified_planner_runtime_event_insert
BEFORE INSERT ON cleardev_planner_runtime_events
WHEN (EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b WHERE b.event_id=NEW.id AND b.execution_run_id=NEW.execution_run_id)) AND (EXISTS(SELECT 1 FROM cleardev_planner_runtime_events old
            WHERE old.id=NEW.id OR old.dispatch_id=NEW.dispatch_id OR old.source_step_id=NEW.source_step_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
    JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
    JOIN cleardev_complex_execution_agent_steps step ON step.id=attempt.agent_step_id
    WHERE attempt.id=NEW.dispatch_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND (json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1' OR json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1')
      AND step.id=NEW.source_step_id AND step.step_kind='BUILDER_TASK' AND step.send_status='SETTLED'
      AND step.request_id=attempt.id AND step.role_binding_id=attempt.builder_role_binding_id
      AND step.message_sha256=NEW.source_message_sha256 AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='BUILDER_RESULT'
      AND (json_type(step.final_message_text,'$.coordination')='object' OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND json_extract(step.final_message_text,'$.outcome')='BLOCKED') OR EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources bridge WHERE bridge.event_id=NEW.id AND bridge.execution_run_id=run.id AND bridge.dispatch_id=attempt.id AND bridge.source_step_id=step.id AND bridge.source_message_sha256=step.message_sha256))
      AND json_extract(NEW.report_json,'$.category') IN('ENGINEERING','PRODUCT')
      AND (json_extract(NEW.report_json,'$.category')=json_extract(step.final_message_text,'$.coordination.category') OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews fr WHERE fr.execution_run_id=run.id AND fr.status='SETTLED' AND fr.verdict IN('BLOCKED','REWORK')) AND json_type(step.final_message_text,'$.coordination') IS NULL AND json_extract(step.final_message_text,'$.outcome')='BLOCKED' AND json_extract(NEW.report_json,'$.category')='ENGINEERING' AND json_extract(NEW.report_json,'$.evidence[0]')=json_extract(step.final_message_text,'$.summary')) OR (EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources bridge WHERE bridge.event_id=NEW.id AND bridge.execution_run_id=run.id AND bridge.dispatch_id=attempt.id AND bridge.source_step_id=step.id AND bridge.source_message_sha256=step.message_sha256) AND json_extract(NEW.report_json,'$.category')='ENGINEERING' AND json_extract(NEW.report_json,'$.evidence[0]')=json_extract(step.final_message_text,'$.summary')))
      AND length(trim(COALESCE(json_extract(NEW.report_json,'$.summary'),'')))>0
      AND json_array_length(NEW.report_json,'$.evidence') BETWEEN 1 AND 6
      AND json_array_length(NEW.report_json,'$.affectedTaskKeys') BETWEEN 1 AND 3
      AND NOT EXISTS(SELECT 1 FROM json_each(NEW.report_json,'$.affectedTaskKeys') affected
                     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                                      WHERE task.execution_run_id=run.id AND task.plan_task_key=affected.value))
 )
)
BEGIN SELECT RAISE(ABORT,'runtime event requires the exact settled Builder report and cannot replace history'); END;
CREATE TRIGGER cleardev_unified_planner_runtime_amendment_insert
BEFORE INSERT ON cleardev_planner_runtime_task_amendments
WHEN (EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b WHERE b.event_id=NEW.event_id AND b.execution_run_id=NEW.execution_run_id)) AND (EXISTS(SELECT 1 FROM cleardev_planner_runtime_task_amendments old WHERE old.id=NEW.id
             OR (old.event_id=NEW.event_id AND old.task_mapping_id=NEW.task_mapping_id)
             OR (old.task_mapping_id=NEW.task_mapping_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_task_amendments WHERE task_mapping_id=NEW.task_mapping_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_effective_tasks task
    JOIN cleardev_work_items item ON item.id=task.work_item_id
    JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=NEW.event_id
    JOIN json_each(decision.result_json,'$.amendments') amendment ON json_extract(amendment.value,'$.taskKey')=task.plan_task_key
    WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=NEW.execution_run_id
      AND decision.execution_run_id=NEW.execution_run_id AND decision.outcome='AMEND_REMAINING' AND decision.source='PLANNER'
      AND ((item.state='PLANNED' AND item.rework_count=0
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=task.id))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4 AND (item.state IN('BLOCKED','REVIEW','REWORK') OR (item.state='NEEDS_HUMAN' AND EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources bridge JOIN cleardev_complex_execution_task_attempts source ON source.id=bridge.dispatch_id WHERE bridge.event_id=NEW.event_id AND bridge.execution_run_id=task.execution_run_id AND source.task_mapping_id=task.id AND source.status='NEEDS_HUMAN' AND source.reason_code='CANDIDATE_INVALID' AND NOT EXISTS(SELECT 1 FROM cleardev_candidate_commits WHERE complex_execution_task_attempt_id=source.id))))
       AND json_extract(NEW.task_packet_json,'$.runtimeRevision.firstRound')=item.rework_count+1
       AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE execution_run_id=task.execution_run_id AND status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))))
      AND task.task_packet_sha256=NEW.previous_package_sha256 AND NEW.task_packet_sha256<>NEW.previous_package_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.eventId')=NEW.event_id
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.decisionSha256')=decision.result_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.previousPackageSha256')=NEW.previous_package_sha256
      AND ((json_extract(task.task_packet_json,'$.schemaVersion')<>4 AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision'))
      OR (json_extract(task.task_packet_json,'$.schemaVersion')=4
       AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution','$.writePaths')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution','$.writePaths')
       AND json_remove(NEW.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision','$.writePaths')=json_remove(task.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision','$.writePaths')
       AND (
        (json_extract(NEW.task_packet_json,'$.projectExecution.basis.writePaths')=json_extract(task.task_packet_json,'$.projectExecution.basis.writePaths')
         AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.checks')=json_extract(task.task_packet_json,'$.projectExecution.basis.checks')
         AND json_extract(NEW.task_packet_json,'$.writePaths')=json_extract(task.task_packet_json,'$.writePaths'))
        )
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
)
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;
CREATE TRIGGER cleardev_unified_planner_runtime_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_decisions
WHEN (EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b WHERE b.event_id=NEW.event_id AND b.execution_run_id=NEW.execution_run_id)) AND (EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_events WHERE id=NEW.event_id AND execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_requests request
    JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
    JOIN cleardev_planner_runtime_events event ON event.id=request.event_id
    WHERE request.event_id=NEW.event_id AND request.execution_run_id=NEW.execution_run_id
      AND step.role_binding_id=request.planner_role_binding_id AND step.request_id=event.id
      AND step.step_kind='COMPLEX_ENGINEERING_PLAN' AND step.send_status='SETTLED'
      AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='PLANNER_RUNTIME_COORDINATION'
      AND json_extract(NEW.result_json,'$.eventId')=event.id
      AND json_extract(NEW.result_json,'$.contextSha256')=request.context_sha256
      AND json_extract(NEW.result_json,'$.decision')=NEW.outcome
      AND json_extract(step.final_message_text,'$.decision')=NEW.outcome
      AND (json_extract(event.report_json,'$.category')<>'PRODUCT' OR NEW.outcome IN('PRODUCT_CLARIFICATION_REQUIRED','STOP'))
 ))
 OR (NEW.outcome IN('CONTINUE','AMEND_REMAINING') AND NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_runs run
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED' AND version.state='APPROVED'
      AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED' AND project.paused_from_state IS NULL
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome='AMEND_REMAINING'
       OR (NEW.outcome='CONTINUE' AND EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources bridge
           JOIN cleardev_complex_execution_task_attempts original ON original.id=bridge.dispatch_id
           WHERE bridge.event_id=NEW.event_id AND bridge.execution_run_id=run.id
           AND original.status IN('BLOCKED','NEEDS_HUMAN') AND original.settled_at IS NOT NULL
           AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings other
             JOIN cleardev_work_items item ON item.id=other.work_item_id
             WHERE other.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN') AND other.id<>original.task_mapping_id)))
       OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
)
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
CREATE TRIGGER cleardev_unified_complex_execution_attempt_insert_valid BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN (EXISTS(SELECT 1 FROM cleardev_failure_coordination_sources b JOIN cleardev_planner_runtime_task_amendments a ON a.event_id=b.event_id WHERE b.execution_run_id=NEW.execution_run_id AND a.execution_run_id=b.execution_run_id AND a.task_mapping_id=NEW.task_mapping_id AND json_extract(a.task_packet_json,'$.runtimeRevision.firstRound')=NEW.round)) AND (NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings t JOIN cleardev_complex_execution_runs r ON r.id=t.execution_run_id
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
    ((NEW.round>0 AND item.state='RUNNING' AND EXISTS(SELECT 1 FROM cleardev_project_events event
      WHERE event.subject_id=item.id AND event.action='RESTART_DEVELOPMENT_TASK' AND event.outcome='ACCEPTED'
      AND event.reason_text='planner-engineering:'||revision.event_id))
    OR (NEW.round>0 AND item.state='REWORK' AND EXISTS(
      SELECT 1 FROM cleardev_failure_coordination_sources bridge
      JOIN cleardev_complex_execution_task_attempts original ON original.id=bridge.dispatch_id
      JOIN cleardev_workflow_recoveries recovery ON recovery.dispatch_id=original.id
      WHERE bridge.event_id=revision.event_id AND bridge.execution_run_id=run.id AND original.task_mapping_id=effective.id
      AND original.round=NEW.round-1 AND original.status IN('BLOCKED','NEEDS_HUMAN') AND original.settled_at IS NOT NULL
      AND recovery.action='CONTINUE_BUILDER' AND recovery.execution_run_id=run.id AND recovery.task_id=effective.id
      AND recovery.working_tree_sha256=bridge.working_tree_sha256 AND recovery.candidate_sha=NEW.base_commit_sha
      AND NOT EXISTS(SELECT 1 FROM cleardev_candidate_commits WHERE complex_execution_task_attempt_id=original.id))))))
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
)
BEGIN SELECT RAISE(ABORT,'cleardev complex execution attempt has an invalid Builder, gate, or bounded round'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE failure_coordination_down(value INTEGER CHECK(value=0));
INSERT INTO failure_coordination_down SELECT count(*) FROM cleardev_failure_coordination_sources;
DROP TABLE failure_coordination_down;
DROP TRIGGER cleardev_unified_planner_runtime_event_insert;
DROP TRIGGER cleardev_unified_planner_runtime_amendment_insert;
DROP TRIGGER cleardev_unified_planner_runtime_decision_insert;
DROP TRIGGER cleardev_unified_complex_execution_attempt_insert_valid;
PRAGMA writable_schema=ON;
UPDATE sqlite_schema SET sql=(SELECT original_sql FROM cleardev_failure_prior_guards p WHERE p.name=sqlite_schema.name)
WHERE type='trigger' AND name IN (SELECT name FROM cleardev_failure_prior_guards);
PRAGMA writable_schema=RESET;
DROP TABLE cleardev_failure_coordination_sources;
DROP TABLE cleardev_failure_prior_guards;
-- +goose StatementEnd
