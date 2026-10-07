-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_planner_runtime_decision_insert;
CREATE TRIGGER cleardev_planner_runtime_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
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
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome IN('CONTINUE','AMEND_REMAINING') OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
DROP TRIGGER cleardev_planner_runtime_recovery_decision_insert;
CREATE TRIGGER cleardev_planner_runtime_recovery_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_recovery_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_recovery_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r WHERE r.event_id=NEW.event_id AND r.execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r JOIN cleardev_agent_step_results result ON result.attempt_id=r.second_attempt_id JOIN cleardev_complex_agent_steps step ON step.id=r.logical_step_id WHERE r.event_id=NEW.event_id AND result.final_message_id=step.final_message_id AND result.turn_id=step.turn_id AND result.raw_message_sha256=step.message_sha256))
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
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome IN('CONTINUE','AMEND_REMAINING') OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
DROP TRIGGER cleardev_extra_coordination_decision_insert;
CREATE TRIGGER cleardev_extra_coordination_decision_insert
BEFORE INSERT ON cleardev_extra_coordination_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_decisions WHERE event_id=NEW.event_id)
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
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome IN('CONTINUE','AMEND_REMAINING') OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
 OR NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=NEW.event_id AND grant.execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(
 SELECT 1 FROM cleardev_planner_runtime_requests request
 JOIN cleardev_agent_step_attempts attempt ON attempt.logical_step_id=request.agent_step_id
 JOIN cleardev_agent_step_results result ON result.attempt_id=attempt.id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 WHERE request.event_id=NEW.event_id AND request.ordinal=3 AND attempt.ao_session_id=request.ao_session_id
 AND attempt.role_binding_id=request.planner_role_binding_id AND attempt.prompt_sha256=step.prompt_sha256
 AND result.turn_id=step.turn_id AND result.final_message_id=step.final_message_id AND result.raw_message_sha256=step.message_sha256))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER cleardev_planner_runtime_decision_insert;
CREATE TRIGGER cleardev_planner_runtime_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
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
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome='AMEND_REMAINING' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
DROP TRIGGER cleardev_planner_runtime_recovery_decision_insert;
CREATE TRIGGER cleardev_planner_runtime_recovery_decision_insert
BEFORE INSERT ON cleardev_planner_runtime_recovery_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_recovery_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r WHERE r.event_id=NEW.event_id AND r.execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_recoveries r JOIN cleardev_agent_step_results result ON result.attempt_id=r.second_attempt_id JOIN cleardev_complex_agent_steps step ON step.id=r.logical_step_id WHERE r.event_id=NEW.event_id AND result.final_message_id=step.final_message_id AND result.turn_id=step.turn_id AND result.raw_message_sha256=step.message_sha256))
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
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome='AMEND_REMAINING' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
DROP TRIGGER cleardev_extra_coordination_decision_insert;
CREATE TRIGGER cleardev_extra_coordination_decision_insert
BEFORE INSERT ON cleardev_extra_coordination_decisions
WHEN EXISTS(SELECT 1 FROM cleardev_extra_coordination_decisions WHERE event_id=NEW.event_id)
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
      AND (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND NEW.outcome='AMEND_REMAINING' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN')))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 3
     OR (SELECT count(*) FROM cleardev_planner_runtime_current_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
 OR NOT EXISTS(SELECT 1 FROM cleardev_extra_coordination_grants grant WHERE grant.event_id=NEW.event_id AND grant.execution_run_id=NEW.execution_run_id)
 OR (NEW.source='PLANNER' AND NOT EXISTS(
 SELECT 1 FROM cleardev_planner_runtime_requests request
 JOIN cleardev_agent_step_attempts attempt ON attempt.logical_step_id=request.agent_step_id
 JOIN cleardev_agent_step_results result ON result.attempt_id=attempt.id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 WHERE request.event_id=NEW.event_id AND request.ordinal=3 AND attempt.ao_session_id=request.ao_session_id
 AND attempt.role_binding_id=request.planner_role_binding_id AND attempt.prompt_sha256=step.prompt_sha256
 AND result.turn_id=step.turn_id AND result.final_message_id=step.final_message_id AND result.raw_message_sha256=step.message_sha256))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
-- +goose StatementEnd
