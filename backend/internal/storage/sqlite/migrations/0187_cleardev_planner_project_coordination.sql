-- Bounded engineering revisions reuse the existing Planner history. Original admissions remain immutable.
-- +goose Up
-- +goose StatementBegin
CREATE VIEW cleardev_planner_project_contracts AS
SELECT admission.execution_run_id,admission.stage_id,admission.plan_id,
 COALESCE(json_extract(amend.task_packet_json,'$.projectExecution'),admission.contract_json) AS contract_json,
 COALESCE(json_extract(amend.task_packet_json,'$.runtimeRevision.projectExecutionSha256'),admission.contract_sha256) AS contract_sha256,
 admission.created_at
FROM cleardev_project_execution_admissions admission
LEFT JOIN cleardev_planner_runtime_task_amendments amend ON amend.id=(
 SELECT a.id FROM cleardev_planner_runtime_task_amendments a
 WHERE a.execution_run_id=admission.execution_run_id AND json_extract(a.task_packet_json,'$.schemaVersion')=4
 ORDER BY a.created_at DESC,a.ordinal DESC,a.id DESC LIMIT 1)
;
DROP TRIGGER cleardev_planner_runtime_event_insert;
CREATE TRIGGER cleardev_planner_runtime_event_insert
BEFORE INSERT ON cleardev_planner_runtime_events
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_events old
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
      AND (json_type(step.final_message_text,'$.coordination')='object' OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND json_extract(step.final_message_text,'$.outcome')='BLOCKED'))
      AND json_extract(NEW.report_json,'$.category') IN('ENGINEERING','PRODUCT')
      AND (json_extract(NEW.report_json,'$.category')=json_extract(step.final_message_text,'$.coordination.category') OR (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1' AND EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews fr WHERE fr.execution_run_id=run.id AND fr.status='SETTLED' AND fr.verdict IN('BLOCKED','REWORK')) AND json_type(step.final_message_text,'$.coordination') IS NULL AND json_extract(step.final_message_text,'$.outcome')='BLOCKED' AND json_extract(NEW.report_json,'$.category')='ENGINEERING' AND json_extract(NEW.report_json,'$.evidence[0]')=json_extract(step.final_message_text,'$.summary')))
      AND length(trim(COALESCE(json_extract(NEW.report_json,'$.summary'),'')))>0
      AND json_array_length(NEW.report_json,'$.evidence') BETWEEN 1 AND 6
      AND json_array_length(NEW.report_json,'$.affectedTaskKeys') BETWEEN 1 AND 3
      AND NOT EXISTS(SELECT 1 FROM json_each(NEW.report_json,'$.affectedTaskKeys') affected
                     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                                      WHERE task.execution_run_id=run.id AND task.plan_task_key=affected.value))
 )
BEGIN SELECT RAISE(ABORT,'runtime event requires the exact settled Builder report and cannot replace history'); END;
DROP TRIGGER cleardev_planner_runtime_request_insert;
CREATE TRIGGER cleardev_planner_runtime_request_insert
BEFORE INSERT ON cleardev_planner_runtime_requests
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests old WHERE old.event_id=NEW.event_id
             OR old.agent_step_id=NEW.agent_step_id OR (old.execution_run_id=NEW.execution_run_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=NEW.execution_run_id)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests prior
           LEFT JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=prior.event_id
           WHERE prior.execution_run_id=NEW.execution_run_id AND decision.event_id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_events event
    JOIN cleardev_complex_execution_runs run ON run.id=event.execution_run_id
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    JOIN cleardev_complex_engineering_plans plan ON plan.id=run.plan_id
    JOIN cleardev_complex_role_bindings planner ON planner.id=plan.planner_role_binding_id
    WHERE event.id=NEW.event_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND (json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1' OR json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1')
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND version.task_set_version=run.accepted_task_set_version AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND planner.id=NEW.planner_role_binding_id AND planner.role='ENGINEERING_PLANNER'
      AND planner.status='BOUND' AND planner.ao_session_id=NEW.ao_session_id
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id AND (NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1') OR status<>'SETTLED' OR verdict NOT IN('BLOCKED','REWORK')))
 )
BEGIN SELECT RAISE(ABORT,'runtime request requires quiescent current facts, the original Planner and remaining durable quota'); END;
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
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
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
     OR (SELECT count(*) FROM cleardev_planner_runtime_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
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
DROP TRIGGER cleardev_project_check_recovery_valid;
CREATE TRIGGER cleardev_project_check_recovery_valid BEFORE INSERT ON cleardev_project_check_recoveries
WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_runs prior
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=NEW.retry_check_run_id
 JOIN cleardev_complex_execution_check_specs spec ON spec.id=prior.check_spec_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=prior.task_attempt_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=run.id
 JOIN cleardev_product_stages stage ON stage.id=admission.stage_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_development_projects parent ON parent.id=stage.product_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=prior.candidate_commit_id
 WHERE prior.id=NEW.original_check_run_id AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND prior.retry_ordinal=0
   AND spec.check_kind='REQUIRED_CHECK' AND spec.task_mapping_id=task.id
   AND retry.check_spec_id=prior.check_spec_id AND retry.task_attempt_id=prior.task_attempt_id
   AND retry.candidate_commit_id=prior.candidate_commit_id AND retry.candidate_commit_sha=prior.candidate_commit_sha
   AND retry.status='PENDING' AND retry.retry_ordinal=1
   AND candidate.complex_execution_task_attempt_id=attempt.id AND candidate.work_item_id=item.id AND candidate.commit_sha=prior.candidate_commit_sha
   AND attempt.status='BLOCKED' AND attempt.reason_code='CHECKER_UNAVAILABLE' AND attempt.settled_at=NEW.original_attempt_settled_at
   AND attempt.round=(SELECT max(round) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
   AND item.state='BLOCKED' AND item.paused_from_state='RUNNING'
   AND run.status='ACCEPTED' AND run.mode='STANDARD' AND run.reason_code='' AND run.settled_at IS NULL
   AND json_extract(run.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
   AND project.cancelled_at IS NULL AND parent.cancelled_at IS NULL AND project.state<>'PAUSED' AND parent.state<>'PAUSED'
   AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
   AND version.task_set_version=run.accepted_task_set_version
   AND stage.definition_sha256=json_extract(admission.contract_json,'$.stageDefinitionSha256')
   AND stage.discussion_id=(SELECT id FROM cleardev_product_discussions WHERE product_id=stage.product_id ORDER BY ordinal DESC LIMIT 1)
   AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews WHERE task_attempt_id=attempt.id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_project_check_recoveries recovered
      JOIN cleardev_complex_execution_check_runs recovered_check ON recovered_check.id=recovered.original_check_run_id
      WHERE recovered_check.task_attempt_id=attempt.id AND recovered_check.candidate_commit_id=prior.candidate_commit_id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE task_mapping_id=task.id AND candidate_commit_id=candidate.id AND role='REVIEWER')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs scope
      JOIN cleardev_complex_execution_check_specs scope_spec ON scope_spec.id=scope.check_spec_id
      WHERE scope.task_attempt_id=attempt.id AND scope.candidate_commit_id=candidate.id
        AND scope_spec.check_kind='SCOPE' AND scope.status='SETTLED' AND scope.result='PASS')
)
BEGIN SELECT RAISE(ABORT,'project dependency check recovery requires the same current candidate and settled infrastructure failure'); END;
DROP TRIGGER cleardev_project_check_continuation_valid;
CREATE TRIGGER cleardev_project_check_continuation_valid BEFORE INSERT ON cleardev_project_check_continuations WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_runs prior
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=NEW.retry_check_run_id
 JOIN cleardev_complex_execution_check_specs spec ON spec.id=prior.check_spec_id
 JOIN cleardev_project_check_recoveries recovery ON recovery.original_check_run_id=prior.id AND recovery.retry_check_run_id=retry.id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=(SELECT builder_role_binding_id FROM cleardev_complex_execution_task_attempts WHERE id=prior.task_attempt_id)
 JOIN sessions builder ON builder.id=binding.ao_session_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=prior.task_attempt_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=run.id
 JOIN cleardev_product_stages stage ON stage.id=admission.stage_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_development_projects parent ON parent.id=stage.product_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=prior.candidate_commit_id
 WHERE retry.id=NEW.retry_check_run_id AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND prior.retry_ordinal=0
   AND spec.check_kind='REQUIRED_CHECK' AND spec.task_mapping_id=task.id
   AND retry.check_spec_id=prior.check_spec_id AND retry.task_attempt_id=prior.task_attempt_id
   AND retry.candidate_commit_id=prior.candidate_commit_id AND retry.candidate_commit_sha=prior.candidate_commit_sha
   AND retry.status='PENDING' AND retry.retry_ordinal=1
   AND candidate.complex_execution_task_attempt_id=attempt.id AND candidate.work_item_id=item.id AND candidate.commit_sha=prior.candidate_commit_sha
   AND attempt.status='BLOCKED' AND attempt.reason_code='BUILDER_SPAWN_FAILED' AND attempt.settled_at=NEW.blocked_attempt_settled_at
   AND attempt.round=(SELECT max(round) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
   AND item.state='BLOCKED' AND item.paused_from_state='RUNNING'
   AND run.status='ACCEPTED' AND run.mode='STANDARD' AND run.reason_code='' AND run.settled_at IS NULL
   AND json_extract(run.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
   AND project.cancelled_at IS NULL AND parent.cancelled_at IS NULL AND project.state<>'PAUSED' AND parent.state<>'PAUSED'
   AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
   AND version.task_set_version=run.accepted_task_set_version
   AND stage.definition_sha256=json_extract(admission.contract_json,'$.stageDefinitionSha256')
   AND stage.discussion_id=(SELECT id FROM cleardev_product_discussions WHERE product_id=stage.product_id ORDER BY ordinal DESC LIMIT 1)
   AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews WHERE task_attempt_id=attempt.id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE task_mapping_id=task.id AND candidate_commit_id=candidate.id AND role='REVIEWER')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs scope
      JOIN cleardev_complex_execution_check_specs scope_spec ON scope_spec.id=scope.check_spec_id
      WHERE scope.task_attempt_id=attempt.id AND scope.candidate_commit_id=candidate.id
        AND scope_spec.check_kind='SCOPE' AND scope.status='SETTLED' AND scope.result='PASS')
   AND binding.status='BOUND' AND builder.activity_state='exited' AND builder.is_terminated=0
   AND recovery.created_at<=attempt.settled_at
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_agent_steps step WHERE step.id=attempt.agent_step_id AND step.send_status='SETTLED')
) BEGIN SELECT RAISE(ABORT,'project check continuation requires the same never-started retry and exited-Builder barrier'); END;
DROP TRIGGER cleardev_project_check_receipt_settle_valid;
CREATE TRIGGER cleardev_project_check_receipt_settle_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN NEW.status='SETTLED' AND NEW.result IN ('PASS','FAIL') AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
   AND json_valid(NEW.output_summary)
   AND json_extract(NEW.output_summary,'$.schemaVersion') IS 1
   AND json_extract(NEW.output_summary,'$.policy') IS 'PROJECT_CHECK_V1'
   AND json_extract(NEW.output_summary,'$.executionRunId') IS spec.execution_run_id
   AND json_extract(NEW.output_summary,'$.contractSha256') IS admission.contract_sha256
   AND json_extract(NEW.output_summary,'$.checkRunId') IS NEW.id
   AND json_extract(NEW.output_summary,'$.checkId') IS spec.check_name
   AND json_extract(NEW.output_summary,'$.candidateSha') IS NEW.candidate_commit_sha
   AND json(json_extract(NEW.output_summary,'$.argv')) IS json(spec.argv_json)
   AND json_extract(NEW.output_summary,'$.timeoutSeconds') IS spec.timeout_seconds
   AND json_extract(NEW.output_summary,'$.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
   AND json_extract(NEW.output_summary,'$.imageId') IS NEW.container_image_id
   AND json_type(NEW.output_summary,'$.exitCode') IS 'integer'
   AND json_extract(NEW.output_summary,'$.exitCode') IS NEW.exit_code
   AND json_extract(NEW.output_summary,'$.timedOut') IS NEW.timed_out
   AND json_type(NEW.output_summary,'$.nodeVersion') IS 'text'
   AND length(json_extract(NEW.output_summary,'$.nodeVersion'))>0
   AND length(json_extract(NEW.output_summary,'$.sourceManifestId'))=64
   AND length(json_extract(NEW.output_summary,'$.sourceRootTreeOid'))=40
   AND length(json_extract(NEW.output_summary,'$.checkEnvironmentId'))=64
   AND length(json_extract(NEW.output_summary,'$.approvedArgvSha256'))=64
   AND length(json_extract(NEW.output_summary,'$.outputSha256'))=64
   AND json_type(NEW.output_summary,'$.outputSummary') IS 'text'
   AND ((NEW.result='PASS' AND NEW.exit_code=0 AND NEW.timed_out=0
         AND json_extract(NEW.output_summary,'$.outcome') IS 'PASS'
         AND json_type(NEW.output_summary,'$.outputTruncated') IS 'false')
     OR (NEW.result='FAIL' AND ((NEW.timed_out=1 AND json_extract(NEW.output_summary,'$.outcome') IS 'TIMED_OUT')
       OR (NEW.timed_out=0 AND json_extract(NEW.output_summary,'$.outcome') IS 'FAIL'
         AND (NEW.exit_code<>0 OR json_type(NEW.output_summary,'$.outputTruncated') IS 'true')))))
)
BEGIN SELECT RAISE(ABORT,'project checks require actual exact candidate/contract/command/environment receipts'); END;
DROP TRIGGER cleardev_review_check_request_valid;
CREATE TRIGGER cleardev_review_check_request_valid BEFORE INSERT ON cleardev_review_check_requests
WHEN EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=NEW.review_id)
 OR NOT EXISTS (
 SELECT 1 FROM (
   SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text
   FROM cleardev_mail_review_check_sources
   UNION ALL
   SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text
   FROM cleardev_project_replacement_review_check_sources
 ) source
 JOIN cleardev_complex_execution_reviews review ON review.id=source.review_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=source.binding_id
 WHERE source.review_id=NEW.review_id AND review.status='PENDING' AND binding.status='BOUND'
 AND json_valid(source.reply_text) AND json_extract(source.reply_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND (
   (json_type(NEW.request_json,'$.projectExecution') IS NULL
    AND ((json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
          AND run.mode='STANDARD' AND run.fixed_builder_count=1)
      OR (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
          AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
          AND ((run.mode='STANDARD' AND run.fixed_builder_count=1) OR (run.mode='PARALLEL' AND run.fixed_builder_count=2))))
    AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN('demo-backend','demo-api','demo-frontend','demo-integration')))
   OR
   (run.mode='STANDARD' AND run.fixed_builder_count=1
    AND json_extract(source.reply_text,'$.schemaVersion') IS 2
    AND json_type(NEW.request_json,'$.projectExecution') IS 'object'
    AND EXISTS(
      SELECT 1 FROM cleardev_planner_project_contracts admission
      WHERE admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
        AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
        AND replace(replace(replace(json_extract(run.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
        AND replace(replace(replace(json_extract(NEW.request_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
        AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') selected
          WHERE selected.type IS NOT 'text' OR NOT EXISTS(
            SELECT 1 FROM json_each(admission.contract_json,'$.basis.checks') approved
            WHERE json_extract(approved.value,'$.id') IS selected.value))))
 )
 AND run.status='ACCEPTED'
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_replacement_review_results WHERE original_review_id=review.id)
 AND json_extract(NEW.request_json,'$.reviewId')=review.id
 AND json_extract(NEW.request_json,'$.requestStepId')=source.request_step_id
 AND COALESCE(json_extract(NEW.request_json,'$.replacementRecoveryId'),'')=source.recovery_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestAttemptId'),'')=source.source_attempt_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestResultId'),'')=source.source_result_id
 AND json_extract(NEW.request_json,'$.candidateId')=candidate.id
 AND json_extract(NEW.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(NEW.request_json,'$.packetSha256')=review.review_packet_sha256
 AND json_type(NEW.request_json,'$.checkIds') IS 'array'
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(source.reply_text,'$.checkIds')
 AND NOT EXISTS(SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(source.reply_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review checks require exact original or authorized replacement reply and its versioned catalog'); END;
DROP TRIGGER cleardev_review_check_result_valid;
CREATE TRIGGER cleardev_review_check_result_valid BEFORE INSERT ON cleardev_review_check_results
WHEN EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=NEW.review_id AND check_id=NEW.check_id OR run_id=NEW.run_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_review_check_requests request
 JOIN cleardev_complex_execution_reviews review ON review.id=request.review_id
 WHERE request.review_id=NEW.review_id AND review.status='PENDING'
 AND NEW.run_id=NEW.review_id||':requested-check:'||NEW.check_id
 AND EXISTS(SELECT 1 FROM json_each(request.request_json,'$.checkIds') WHERE value=NEW.check_id)
 AND json_extract(NEW.result_json,'$.reviewId') IS NEW.review_id
 AND json_extract(NEW.result_json,'$.checkId') IS NEW.check_id
 AND json_extract(NEW.result_json,'$.proof.runId') IS NEW.run_id
 AND json_extract(NEW.result_json,'$.proof.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
 AND json_extract(NEW.result_json,'$.outcome') IN ('PASS','FAIL','TIMED_OUT','INFRA_ERROR')
 AND (
   (json_type(request.request_json,'$.projectExecution') IS NULL
    AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
    AND NEW.check_id IN('demo-backend','demo-api','demo-frontend','demo-integration'))
   OR EXISTS(
    SELECT 1 FROM cleardev_planner_project_contracts admission
    JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id AND attempt.execution_run_id=admission.execution_run_id
    JOIN json_each(admission.contract_json,'$.basis.checks') approved
    WHERE replace(replace(replace(json_extract(request.request_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
      AND json_extract(approved.value,'$.id') IS NEW.check_id
      AND json(json_extract(NEW.result_json,'$.proof.argv')) IS json(json_extract(approved.value,'$.argv'))
      AND (
       (json_extract(NEW.result_json,'$.outcome') IS 'INFRA_ERROR'
        AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
        AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
        AND json_extract(NEW.result_json,'$.proof.exitCode') IS -1)
       OR
       (json_type(NEW.result_json,'$.projectReceipt') IS 'object'
        AND json_extract(NEW.result_json,'$.projectReceipt.schemaVersion') IS 1
        AND json_extract(NEW.result_json,'$.projectReceipt.policy') IS 'PROJECT_CHECK_V1'
        AND json_extract(NEW.result_json,'$.projectReceipt.executionRunId') IS admission.execution_run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.contractSha256') IS admission.contract_sha256
        AND json_extract(NEW.result_json,'$.projectReceipt.checkRunId') IS NEW.run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.checkId') IS NEW.check_id
        AND json_extract(NEW.result_json,'$.projectReceipt.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
        AND json(json_extract(NEW.result_json,'$.projectReceipt.argv')) IS json(json_extract(approved.value,'$.argv'))
        AND json_extract(NEW.result_json,'$.projectReceipt.timeoutSeconds') IS json_extract(approved.value,'$.timeoutSeconds')
        AND json_extract(NEW.result_json,'$.projectReceipt.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') IS json_extract(NEW.result_json,'$.proof.imageId')
        AND length(json_extract(NEW.result_json,'$.projectReceipt.imageId'))=71
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') LIKE 'sha256:%'
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid'))=40
        AND length(json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.approvedArgvSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.outputSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.nodeVersion'))>0
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId') IS json_extract(NEW.result_json,'$.proof.sourceManifestId')
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid') IS json_extract(NEW.result_json,'$.proof.sourceTreeOid')
        AND json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId') IS json_extract(NEW.result_json,'$.proof.environmentId')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSha256') IS json_extract(NEW.result_json,'$.proof.outputSha256')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSummary') IS json_extract(NEW.result_json,'$.output')
        AND json_extract(NEW.result_json,'$.projectReceipt.outcome') IS json_extract(NEW.result_json,'$.outcome')
        AND json_extract(NEW.result_json,'$.projectReceipt.exitCode') IS json_extract(NEW.result_json,'$.proof.exitCode')
        AND json_extract(NEW.result_json,'$.projectReceipt.timedOut') IS json_extract(NEW.result_json,'$.proof.timedOut')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputTruncated') IS json_extract(NEW.result_json,'$.proof.truncated')
        AND (json_extract(approved.value,'$.argv[0]') IS NOT 'npm' OR (
          length(json_extract(NEW.result_json,'$.projectReceipt.npmVersion'))>0
          AND length(json_extract(NEW.result_json,'$.projectReceipt.packageJsonSha256'))=64
          AND ((length(json_extract(NEW.result_json,'$.projectReceipt.packageLockSha256'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyCacheKey'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyEnvironment'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyTreeSha256'))=64)
           OR (json_type(NEW.result_json,'$.projectReceipt.packageLockSha256') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyCacheKey') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyEnvironment') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyTreeSha256') IS NULL))))
        AND (
         (json_extract(NEW.result_json,'$.outcome') IS 'PASS'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'true'
          AND json_extract(NEW.result_json,'$.proof.exitCode') IS 0
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND json_type(NEW.result_json,'$.proof.truncated') IS 'false')
         OR (json_extract(NEW.result_json,'$.outcome') IS 'FAIL'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND (json_extract(NEW.result_json,'$.proof.exitCode')<>0 OR json_type(NEW.result_json,'$.proof.truncated') IS 'true'))
         OR (json_extract(NEW.result_json,'$.outcome') IS 'TIMED_OUT'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'true')))
      )
   )
 )
) BEGIN SELECT RAISE(ABORT,'review check result must bind its exact versioned request and actual runtime receipt'); END;
DROP VIEW cleardev_project_replacement_authorities;
CREATE VIEW cleardev_project_replacement_authorities AS
SELECT review.id AS review_id,run.id AS execution_run_id,attempt.task_mapping_id,attempt.id AS task_attempt_id,
 candidate.id AS candidate_id,candidate.commit_sha AS candidate_sha,review.review_packet_sha256 AS packet_sha,
 review.agent_step_id AS request_step_id,request.id AS recovery_id,binding.id AS binding_id,
 binding.ao_session_id AS session_id,binding.workspace_path,request.failure_event_id,
 json_extract(request.request_json,'$.promptSha256') AS prompt_sha
FROM cleardev_fixed_recovery_requests request
JOIN cleardev_fixed_recovery_claims claim ON claim.request_id=request.id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
JOIN cleardev_fixed_recovery_results outcome ON outcome.request_id=request.id AND outcome.outcome='PASS'
JOIN cleardev_complex_execution_reviews review ON review.id=json_extract(request.request_json,'$.reviewId')
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id AND run.id=request.execution_run_id
JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id AND candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_role_bindings binding ON binding.id=claim.operation_id||':reviewer'
WHERE run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED'
 AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND replace(replace(replace(json_extract(run.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
 AND json_extract(request.request_json,'$.logicalStepId')=review.agent_step_id
 AND json_extract(request.request_json,'$.dispatchId')=attempt.id
 AND json_extract(request.request_json,'$.taskId')=attempt.task_mapping_id
 AND json_extract(request.request_json,'$.roleBindingId')=review.reviewer_role_binding_id
 AND json_extract(request.request_json,'$.candidateId')=candidate.id
 AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND binding.execution_run_id=run.id AND binding.role='REVIEWER'
 AND binding.task_mapping_id=attempt.task_mapping_id AND binding.candidate_commit_id=candidate.id
 AND binding.continuation_of_role_binding_id=review.reviewer_role_binding_id
 AND binding.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND binding.base_commit_sha=candidate.commit_sha
 AND json_extract(outcome.result_json,'$.outcome')='PASS'
 AND json_extract(outcome.result_json,'$.roleBindingId')=binding.id
 AND json_extract(outcome.result_json,'$.sessionId')=binding.ao_session_id
 AND json_extract(outcome.result_json,'$.workspacePath')=binding.workspace_path
 AND binding.ao_session_id<>json_extract(request.request_json,'$.sessionId');
DROP TRIGGER cleardev_project_replacement_result_exact;
CREATE TRIGGER cleardev_project_replacement_result_exact BEFORE INSERT ON cleardev_replacement_review_results
WHEN EXISTS(
 SELECT 1 FROM cleardev_fixed_recovery_requests request
 JOIN cleardev_complex_execution_runs run ON run.id=request.execution_run_id
 JOIN cleardev_planner_project_contracts admission ON admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
 WHERE request.id=NEW.recovery_request_id AND run.mode='STANDARD' AND run.fixed_builder_count=1
   AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
   AND replace(replace(replace(json_extract(run.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
)
AND NOT EXISTS(
 SELECT 1 FROM cleardev_project_replacement_final_sources source
 WHERE source.recovery_id=NEW.recovery_request_id AND source.review_id=NEW.original_review_id
   AND source.attempt_id=NEW.attempt_id AND source.result_id=NEW.result_id
   AND json_extract(source.reply_text,'$.verdict')=NEW.verdict
   AND json_extract(NEW.result_json,'$.recoveryRequestId')=NEW.recovery_request_id
   AND json_extract(NEW.result_json,'$.originalReviewId')=NEW.original_review_id
   AND json_extract(NEW.result_json,'$.attemptId')=NEW.attempt_id
   AND json_extract(NEW.result_json,'$.resultId')=NEW.result_id
   AND json_extract(NEW.result_json,'$.verdict')=NEW.verdict
   AND json_type(NEW.result_json,'$.summary')='text'
   AND json_extract(NEW.result_json,'$.reasonCode')=json_extract(source.reply_text,'$.reasonCode')
)
BEGIN SELECT RAISE(ABORT,'project replacement result must reference its exact authorized final reply'); END;
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
DROP VIEW cleardev_completed_project_deliveries;
CREATE VIEW cleardev_completed_project_deliveries AS
SELECT s.product_id, s.id AS stage_id, r.id AS execution_run_id,
 r.development_project_id AS requirement_id, result.id AS result_id,
 candidate.id AS integration_candidate_id, candidate.commit_sha AS candidate_sha,
 effective.contract_json, review.id AS final_review_id
FROM cleardev_product_stages s
JOIN cleardev_complex_execution_runs r ON r.development_project_id=s.development_requirement_id
JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=r.id AND admission.stage_id=s.id AND admission.plan_id=r.plan_id
JOIN cleardev_planner_project_contracts effective ON effective.execution_run_id=r.id
JOIN cleardev_complex_execution_results result ON result.execution_run_id=r.id
JOIN cleardev_integration_candidates candidate ON candidate.id=result.integration_candidate_id AND candidate.complex_execution_result_id=result.id
JOIN cleardev_requirement_final_reviews review ON review.execution_run_id=r.id
WHERE r.status IS 'COMPLETED' AND r.settled_at IS NOT NULL
 AND result.completion_status IS 'COMPLETED' AND result.completed_at IS NOT NULL
 AND json_extract(admission.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(admission.contract_json,'$.productId') IS s.product_id
 AND json_extract(admission.contract_json,'$.stageId') IS s.id
 AND json_extract(r.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND replace(replace(replace(json_extract(r.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
     IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
 AND candidate.requirement_version_id IS r.requirement_version_id
 AND candidate.development_project_id IS r.development_project_id
 AND review.rowid=(SELECT max(latest.rowid) FROM cleardev_requirement_final_reviews latest WHERE latest.execution_run_id=r.id)
 AND review.status IS 'SETTLED' AND review.verdict IS 'PASS' AND review.result_id IS NOT NULL AND review.settled_at IS NOT NULL
 AND review.development_project_id IS r.development_project_id
 AND review.requirement_version_id IS r.requirement_version_id AND review.requirement_sha256 IS r.requirement_sha256
 AND review.plan_id IS r.plan_id AND review.plan_sha256 IS r.plan_sha256
 AND review.candidate_commit_sha IS candidate.commit_sha
 AND review.base_commit_sha IS json_extract(admission.contract_json,'$.baseCommitSha')
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id
     AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected WHERE expected.value IS checks.check_run_id))
 AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected
     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id AND checks.check_run_id IS expected.value));
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_planner_project_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_planner_project_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs) OR EXISTS(SELECT 1 FROM cleardev_builder_replacement_requests)
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
DROP TABLE cleardev_planner_project_down_guard;
DROP VIEW cleardev_completed_project_deliveries;
CREATE VIEW cleardev_completed_project_deliveries AS
SELECT s.product_id, s.id AS stage_id, r.id AS execution_run_id,
 r.development_project_id AS requirement_id, result.id AS result_id,
 candidate.id AS integration_candidate_id, candidate.commit_sha AS candidate_sha,
 admission.contract_json, review.id AS final_review_id
FROM cleardev_product_stages s
JOIN cleardev_complex_execution_runs r ON r.development_project_id=s.development_requirement_id
JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=r.id AND admission.stage_id=s.id AND admission.plan_id=r.plan_id
JOIN cleardev_complex_execution_results result ON result.execution_run_id=r.id
JOIN cleardev_integration_candidates candidate ON candidate.id=result.integration_candidate_id AND candidate.complex_execution_result_id=result.id
JOIN cleardev_requirement_final_reviews review ON review.execution_run_id=r.id
WHERE r.status IS 'COMPLETED' AND r.settled_at IS NOT NULL
 AND result.completion_status IS 'COMPLETED' AND result.completed_at IS NOT NULL
 AND json_extract(admission.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(admission.contract_json,'$.productId') IS s.product_id
 AND json_extract(admission.contract_json,'$.stageId') IS s.id
 AND json_extract(r.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND replace(replace(replace(json_extract(r.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
     IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
 AND candidate.requirement_version_id IS r.requirement_version_id
 AND candidate.development_project_id IS r.development_project_id
 AND review.rowid=(SELECT max(latest.rowid) FROM cleardev_requirement_final_reviews latest WHERE latest.execution_run_id=r.id)
 AND review.status IS 'SETTLED' AND review.verdict IS 'PASS' AND review.result_id IS NOT NULL AND review.settled_at IS NOT NULL
 AND review.development_project_id IS r.development_project_id
 AND review.requirement_version_id IS r.requirement_version_id AND review.requirement_sha256 IS r.requirement_sha256
 AND review.plan_id IS r.plan_id AND review.plan_sha256 IS r.plan_sha256
 AND review.candidate_commit_sha IS candidate.commit_sha
 AND review.base_commit_sha IS json_extract(admission.contract_json,'$.baseCommitSha')
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id
     AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected WHERE expected.value IS checks.check_run_id))
 AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected
     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id AND checks.check_run_id IS expected.value));
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
DROP TRIGGER cleardev_project_replacement_result_exact;
CREATE TRIGGER cleardev_project_replacement_result_exact BEFORE INSERT ON cleardev_replacement_review_results
WHEN EXISTS(
 SELECT 1 FROM cleardev_fixed_recovery_requests request
 JOIN cleardev_complex_execution_runs run ON run.id=request.execution_run_id
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
 WHERE request.id=NEW.recovery_request_id AND run.mode='STANDARD' AND run.fixed_builder_count=1
   AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
   AND replace(replace(replace(json_extract(run.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
)
AND NOT EXISTS(
 SELECT 1 FROM cleardev_project_replacement_final_sources source
 WHERE source.recovery_id=NEW.recovery_request_id AND source.review_id=NEW.original_review_id
   AND source.attempt_id=NEW.attempt_id AND source.result_id=NEW.result_id
   AND json_extract(source.reply_text,'$.verdict')=NEW.verdict
   AND json_extract(NEW.result_json,'$.recoveryRequestId')=NEW.recovery_request_id
   AND json_extract(NEW.result_json,'$.originalReviewId')=NEW.original_review_id
   AND json_extract(NEW.result_json,'$.attemptId')=NEW.attempt_id
   AND json_extract(NEW.result_json,'$.resultId')=NEW.result_id
   AND json_extract(NEW.result_json,'$.verdict')=NEW.verdict
   AND json_type(NEW.result_json,'$.summary')='text'
   AND json_extract(NEW.result_json,'$.reasonCode')=json_extract(source.reply_text,'$.reasonCode')
)
BEGIN SELECT RAISE(ABORT,'project replacement result must reference its exact authorized final reply'); END;
DROP VIEW cleardev_project_replacement_authorities;
CREATE VIEW cleardev_project_replacement_authorities AS
SELECT review.id AS review_id,run.id AS execution_run_id,attempt.task_mapping_id,attempt.id AS task_attempt_id,
 candidate.id AS candidate_id,candidate.commit_sha AS candidate_sha,review.review_packet_sha256 AS packet_sha,
 review.agent_step_id AS request_step_id,request.id AS recovery_id,binding.id AS binding_id,
 binding.ao_session_id AS session_id,binding.workspace_path,request.failure_event_id,
 json_extract(request.request_json,'$.promptSha256') AS prompt_sha
FROM cleardev_fixed_recovery_requests request
JOIN cleardev_fixed_recovery_claims claim ON claim.request_id=request.id AND claim.action='REBUILD_INDEPENDENT_REVIEWER'
JOIN cleardev_fixed_recovery_results outcome ON outcome.request_id=request.id AND outcome.outcome='PASS'
JOIN cleardev_complex_execution_reviews review ON review.id=json_extract(request.request_json,'$.reviewId')
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id AND run.id=request.execution_run_id
JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id AND candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_role_bindings binding ON binding.id=claim.operation_id||':reviewer'
WHERE run.mode='STANDARD' AND run.fixed_builder_count=1 AND run.status='ACCEPTED'
 AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND replace(replace(replace(json_extract(run.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
 AND json_extract(request.request_json,'$.logicalStepId')=review.agent_step_id
 AND json_extract(request.request_json,'$.dispatchId')=attempt.id
 AND json_extract(request.request_json,'$.taskId')=attempt.task_mapping_id
 AND json_extract(request.request_json,'$.roleBindingId')=review.reviewer_role_binding_id
 AND json_extract(request.request_json,'$.candidateId')=candidate.id
 AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND binding.execution_run_id=run.id AND binding.role='REVIEWER'
 AND binding.task_mapping_id=attempt.task_mapping_id AND binding.candidate_commit_id=candidate.id
 AND binding.continuation_of_role_binding_id=review.reviewer_role_binding_id
 AND binding.session_creation_idempotency_key=claim.operation_id||':reviewer-session'
 AND binding.base_commit_sha=candidate.commit_sha
 AND json_extract(outcome.result_json,'$.outcome')='PASS'
 AND json_extract(outcome.result_json,'$.roleBindingId')=binding.id
 AND json_extract(outcome.result_json,'$.sessionId')=binding.ao_session_id
 AND json_extract(outcome.result_json,'$.workspacePath')=binding.workspace_path
 AND binding.ao_session_id<>json_extract(request.request_json,'$.sessionId');
DROP TRIGGER cleardev_review_check_result_valid;
CREATE TRIGGER cleardev_review_check_result_valid BEFORE INSERT ON cleardev_review_check_results
WHEN EXISTS(SELECT 1 FROM cleardev_review_check_results WHERE review_id=NEW.review_id AND check_id=NEW.check_id OR run_id=NEW.run_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_review_check_requests request
 JOIN cleardev_complex_execution_reviews review ON review.id=request.review_id
 WHERE request.review_id=NEW.review_id AND review.status='PENDING'
 AND NEW.run_id=NEW.review_id||':requested-check:'||NEW.check_id
 AND EXISTS(SELECT 1 FROM json_each(request.request_json,'$.checkIds') WHERE value=NEW.check_id)
 AND json_extract(NEW.result_json,'$.reviewId') IS NEW.review_id
 AND json_extract(NEW.result_json,'$.checkId') IS NEW.check_id
 AND json_extract(NEW.result_json,'$.proof.runId') IS NEW.run_id
 AND json_extract(NEW.result_json,'$.proof.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
 AND json_extract(NEW.result_json,'$.outcome') IN ('PASS','FAIL','TIMED_OUT','INFRA_ERROR')
 AND (
   (json_type(request.request_json,'$.projectExecution') IS NULL
    AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
    AND NEW.check_id IN('demo-backend','demo-api','demo-frontend','demo-integration'))
   OR EXISTS(
    SELECT 1 FROM cleardev_project_execution_admissions admission
    JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id AND attempt.execution_run_id=admission.execution_run_id
    JOIN json_each(admission.contract_json,'$.basis.checks') approved
    WHERE replace(replace(replace(json_extract(request.request_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
      AND json_extract(approved.value,'$.id') IS NEW.check_id
      AND json(json_extract(NEW.result_json,'$.proof.argv')) IS json(json_extract(approved.value,'$.argv'))
      AND (
       (json_extract(NEW.result_json,'$.outcome') IS 'INFRA_ERROR'
        AND json_type(NEW.result_json,'$.projectReceipt') IS NULL
        AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
        AND json_extract(NEW.result_json,'$.proof.exitCode') IS -1)
       OR
       (json_type(NEW.result_json,'$.projectReceipt') IS 'object'
        AND json_extract(NEW.result_json,'$.projectReceipt.schemaVersion') IS 1
        AND json_extract(NEW.result_json,'$.projectReceipt.policy') IS 'PROJECT_CHECK_V1'
        AND json_extract(NEW.result_json,'$.projectReceipt.executionRunId') IS admission.execution_run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.contractSha256') IS admission.contract_sha256
        AND json_extract(NEW.result_json,'$.projectReceipt.checkRunId') IS NEW.run_id
        AND json_extract(NEW.result_json,'$.projectReceipt.checkId') IS NEW.check_id
        AND json_extract(NEW.result_json,'$.projectReceipt.candidateSha') IS json_extract(request.request_json,'$.candidateSha')
        AND json(json_extract(NEW.result_json,'$.projectReceipt.argv')) IS json(json_extract(approved.value,'$.argv'))
        AND json_extract(NEW.result_json,'$.projectReceipt.timeoutSeconds') IS json_extract(approved.value,'$.timeoutSeconds')
        AND json_extract(NEW.result_json,'$.projectReceipt.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') IS json_extract(NEW.result_json,'$.proof.imageId')
        AND length(json_extract(NEW.result_json,'$.projectReceipt.imageId'))=71
        AND json_extract(NEW.result_json,'$.projectReceipt.imageId') LIKE 'sha256:%'
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid'))=40
        AND length(json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.approvedArgvSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.outputSha256'))=64
        AND length(json_extract(NEW.result_json,'$.projectReceipt.nodeVersion'))>0
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceManifestId') IS json_extract(NEW.result_json,'$.proof.sourceManifestId')
        AND json_extract(NEW.result_json,'$.projectReceipt.sourceRootTreeOid') IS json_extract(NEW.result_json,'$.proof.sourceTreeOid')
        AND json_extract(NEW.result_json,'$.projectReceipt.checkEnvironmentId') IS json_extract(NEW.result_json,'$.proof.environmentId')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSha256') IS json_extract(NEW.result_json,'$.proof.outputSha256')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputSummary') IS json_extract(NEW.result_json,'$.output')
        AND json_extract(NEW.result_json,'$.projectReceipt.outcome') IS json_extract(NEW.result_json,'$.outcome')
        AND json_extract(NEW.result_json,'$.projectReceipt.exitCode') IS json_extract(NEW.result_json,'$.proof.exitCode')
        AND json_extract(NEW.result_json,'$.projectReceipt.timedOut') IS json_extract(NEW.result_json,'$.proof.timedOut')
        AND json_extract(NEW.result_json,'$.projectReceipt.outputTruncated') IS json_extract(NEW.result_json,'$.proof.truncated')
        AND (json_extract(approved.value,'$.argv[0]') IS NOT 'npm' OR (
          length(json_extract(NEW.result_json,'$.projectReceipt.npmVersion'))>0
          AND length(json_extract(NEW.result_json,'$.projectReceipt.packageJsonSha256'))=64
          AND ((length(json_extract(NEW.result_json,'$.projectReceipt.packageLockSha256'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyCacheKey'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyEnvironment'))=64
            AND length(json_extract(NEW.result_json,'$.projectReceipt.dependencyTreeSha256'))=64)
           OR (json_type(NEW.result_json,'$.projectReceipt.packageLockSha256') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyCacheKey') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyEnvironment') IS NULL
            AND json_type(NEW.result_json,'$.projectReceipt.dependencyTreeSha256') IS NULL))))
        AND (
         (json_extract(NEW.result_json,'$.outcome') IS 'PASS'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'true'
          AND json_extract(NEW.result_json,'$.proof.exitCode') IS 0
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND json_type(NEW.result_json,'$.proof.truncated') IS 'false')
         OR (json_extract(NEW.result_json,'$.outcome') IS 'FAIL'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'false'
          AND (json_extract(NEW.result_json,'$.proof.exitCode')<>0 OR json_type(NEW.result_json,'$.proof.truncated') IS 'true'))
         OR (json_extract(NEW.result_json,'$.outcome') IS 'TIMED_OUT'
          AND json_type(NEW.result_json,'$.proof.passed') IS 'false'
          AND json_type(NEW.result_json,'$.proof.timedOut') IS 'true')))
      )
   )
 )
) BEGIN SELECT RAISE(ABORT,'review check result must bind its exact versioned request and actual runtime receipt'); END;
DROP TRIGGER cleardev_review_check_request_valid;
CREATE TRIGGER cleardev_review_check_request_valid BEFORE INSERT ON cleardev_review_check_requests
WHEN EXISTS(SELECT 1 FROM cleardev_review_check_requests WHERE review_id=NEW.review_id)
 OR NOT EXISTS (
 SELECT 1 FROM (
   SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text
   FROM cleardev_mail_review_check_sources
   UNION ALL
   SELECT review_id,request_step_id,binding_id,recovery_id,source_attempt_id,source_result_id,reply_text
   FROM cleardev_project_replacement_review_check_sources
 ) source
 JOIN cleardev_complex_execution_reviews review ON review.id=source.review_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=review.candidate_commit_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=review.task_attempt_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=source.binding_id
 WHERE source.review_id=NEW.review_id AND review.status='PENDING' AND binding.status='BOUND'
 AND json_valid(source.reply_text) AND json_extract(source.reply_text,'$.kind')='REVIEW_CHECK_REQUEST'
 AND (
   (json_type(NEW.request_json,'$.projectExecution') IS NULL
    AND ((json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V1'
          AND run.mode='STANDARD' AND run.fixed_builder_count=1)
      OR (json_extract(run.execution_package_json,'$.deliveryPolicy')='MAIL_INCREMENT_V2'
          AND json_extract(run.execution_package_json,'$.attemptPolicy')='MAIL_ATTEMPTS_V1'
          AND ((run.mode='STANDARD' AND run.fixed_builder_count=1) OR (run.mode='PARALLEL' AND run.fixed_builder_count=2))))
    AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') WHERE value NOT IN('demo-backend','demo-api','demo-frontend','demo-integration')))
   OR
   (run.mode='STANDARD' AND run.fixed_builder_count=1
    AND json_extract(source.reply_text,'$.schemaVersion') IS 2
    AND json_type(NEW.request_json,'$.projectExecution') IS 'object'
    AND EXISTS(
      SELECT 1 FROM cleardev_project_execution_admissions admission
      WHERE admission.execution_run_id=run.id AND admission.plan_id=run.plan_id
        AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
        AND replace(replace(replace(json_extract(run.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
        AND replace(replace(replace(json_extract(NEW.request_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&') IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
        AND NOT EXISTS(SELECT 1 FROM json_each(NEW.request_json,'$.checkIds') selected
          WHERE selected.type IS NOT 'text' OR NOT EXISTS(
            SELECT 1 FROM json_each(admission.contract_json,'$.basis.checks') approved
            WHERE json_extract(approved.value,'$.id') IS selected.value))))
 )
 AND run.status='ACCEPTED'
 AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_replacement_review_results WHERE original_review_id=review.id)
 AND json_extract(NEW.request_json,'$.reviewId')=review.id
 AND json_extract(NEW.request_json,'$.requestStepId')=source.request_step_id
 AND COALESCE(json_extract(NEW.request_json,'$.replacementRecoveryId'),'')=source.recovery_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestAttemptId'),'')=source.source_attempt_id
 AND COALESCE(json_extract(NEW.request_json,'$.requestResultId'),'')=source.source_result_id
 AND json_extract(NEW.request_json,'$.candidateId')=candidate.id
 AND json_extract(NEW.request_json,'$.candidateSha')=candidate.commit_sha
 AND json_extract(NEW.request_json,'$.packetSha256')=review.review_packet_sha256
 AND json_type(NEW.request_json,'$.checkIds') IS 'array'
 AND json_array_length(NEW.request_json,'$.checkIds') BETWEEN 1 AND 4
 AND (SELECT count(DISTINCT value) FROM json_each(NEW.request_json,'$.checkIds'))=json_array_length(NEW.request_json,'$.checkIds')
 AND json_array_length(NEW.request_json,'$.checkIds')=json_array_length(source.reply_text,'$.checkIds')
 AND NOT EXISTS(SELECT value FROM json_each(NEW.request_json,'$.checkIds') EXCEPT SELECT value FROM json_each(source.reply_text,'$.checkIds'))
) BEGIN SELECT RAISE(ABORT,'review checks require exact original or authorized replacement reply and its versioned catalog'); END;
DROP TRIGGER cleardev_project_check_receipt_settle_valid;
CREATE TRIGGER cleardev_project_check_receipt_settle_valid
BEFORE UPDATE ON cleardev_complex_execution_check_runs
WHEN NEW.status='SETTLED' AND NEW.result IN ('PASS','FAIL') AND EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
) AND NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs spec
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=spec.execution_run_id
 WHERE spec.id=NEW.check_spec_id AND spec.check_kind<>'SCOPE'
   AND json_valid(NEW.output_summary)
   AND json_extract(NEW.output_summary,'$.schemaVersion') IS 1
   AND json_extract(NEW.output_summary,'$.policy') IS 'PROJECT_CHECK_V1'
   AND json_extract(NEW.output_summary,'$.executionRunId') IS spec.execution_run_id
   AND json_extract(NEW.output_summary,'$.contractSha256') IS admission.contract_sha256
   AND json_extract(NEW.output_summary,'$.checkRunId') IS NEW.id
   AND json_extract(NEW.output_summary,'$.checkId') IS spec.check_name
   AND json_extract(NEW.output_summary,'$.candidateSha') IS NEW.candidate_commit_sha
   AND json(json_extract(NEW.output_summary,'$.argv')) IS json(spec.argv_json)
   AND json_extract(NEW.output_summary,'$.timeoutSeconds') IS spec.timeout_seconds
   AND json_extract(NEW.output_summary,'$.image') IS 'node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436'
   AND json_extract(NEW.output_summary,'$.imageId') IS NEW.container_image_id
   AND json_type(NEW.output_summary,'$.exitCode') IS 'integer'
   AND json_extract(NEW.output_summary,'$.exitCode') IS NEW.exit_code
   AND json_extract(NEW.output_summary,'$.timedOut') IS NEW.timed_out
   AND json_type(NEW.output_summary,'$.nodeVersion') IS 'text'
   AND length(json_extract(NEW.output_summary,'$.nodeVersion'))>0
   AND length(json_extract(NEW.output_summary,'$.sourceManifestId'))=64
   AND length(json_extract(NEW.output_summary,'$.sourceRootTreeOid'))=40
   AND length(json_extract(NEW.output_summary,'$.checkEnvironmentId'))=64
   AND length(json_extract(NEW.output_summary,'$.approvedArgvSha256'))=64
   AND length(json_extract(NEW.output_summary,'$.outputSha256'))=64
   AND json_type(NEW.output_summary,'$.outputSummary') IS 'text'
   AND ((NEW.result='PASS' AND NEW.exit_code=0 AND NEW.timed_out=0
         AND json_extract(NEW.output_summary,'$.outcome') IS 'PASS'
         AND json_type(NEW.output_summary,'$.outputTruncated') IS 'false')
     OR (NEW.result='FAIL' AND ((NEW.timed_out=1 AND json_extract(NEW.output_summary,'$.outcome') IS 'TIMED_OUT')
       OR (NEW.timed_out=0 AND json_extract(NEW.output_summary,'$.outcome') IS 'FAIL'
         AND (NEW.exit_code<>0 OR json_type(NEW.output_summary,'$.outputTruncated') IS 'true')))))
)
BEGIN SELECT RAISE(ABORT,'project checks require actual exact candidate/contract/command/environment receipts'); END;
DROP TRIGGER cleardev_project_check_continuation_valid;
CREATE TRIGGER cleardev_project_check_continuation_valid BEFORE INSERT ON cleardev_project_check_continuations WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_runs prior
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=NEW.retry_check_run_id
 JOIN cleardev_complex_execution_check_specs spec ON spec.id=prior.check_spec_id
 JOIN cleardev_project_check_recoveries recovery ON recovery.original_check_run_id=prior.id AND recovery.retry_check_run_id=retry.id
 JOIN cleardev_complex_execution_role_bindings binding ON binding.id=(SELECT builder_role_binding_id FROM cleardev_complex_execution_task_attempts WHERE id=prior.task_attempt_id)
 JOIN sessions builder ON builder.id=binding.ao_session_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=prior.task_attempt_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id
 JOIN cleardev_product_stages stage ON stage.id=admission.stage_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_development_projects parent ON parent.id=stage.product_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=prior.candidate_commit_id
 WHERE retry.id=NEW.retry_check_run_id AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND prior.retry_ordinal=0
   AND spec.check_kind='REQUIRED_CHECK' AND spec.task_mapping_id=task.id
   AND retry.check_spec_id=prior.check_spec_id AND retry.task_attempt_id=prior.task_attempt_id
   AND retry.candidate_commit_id=prior.candidate_commit_id AND retry.candidate_commit_sha=prior.candidate_commit_sha
   AND retry.status='PENDING' AND retry.retry_ordinal=1
   AND candidate.complex_execution_task_attempt_id=attempt.id AND candidate.work_item_id=item.id AND candidate.commit_sha=prior.candidate_commit_sha
   AND attempt.status='BLOCKED' AND attempt.reason_code='BUILDER_SPAWN_FAILED' AND attempt.settled_at=NEW.blocked_attempt_settled_at
   AND attempt.round=(SELECT max(round) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
   AND item.state='BLOCKED' AND item.paused_from_state='RUNNING'
   AND run.status='ACCEPTED' AND run.mode='STANDARD' AND run.reason_code='' AND run.settled_at IS NULL
   AND json_extract(run.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
   AND project.cancelled_at IS NULL AND parent.cancelled_at IS NULL AND project.state<>'PAUSED' AND parent.state<>'PAUSED'
   AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
   AND version.task_set_version=run.accepted_task_set_version
   AND stage.definition_sha256=json_extract(admission.contract_json,'$.stageDefinitionSha256')
   AND stage.discussion_id=(SELECT id FROM cleardev_product_discussions WHERE product_id=stage.product_id ORDER BY ordinal DESC LIMIT 1)
   AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews WHERE task_attempt_id=attempt.id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE task_mapping_id=task.id AND candidate_commit_id=candidate.id AND role='REVIEWER')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs scope
      JOIN cleardev_complex_execution_check_specs scope_spec ON scope_spec.id=scope.check_spec_id
      WHERE scope.task_attempt_id=attempt.id AND scope.candidate_commit_id=candidate.id
        AND scope_spec.check_kind='SCOPE' AND scope.status='SETTLED' AND scope.result='PASS')
   AND binding.status='BOUND' AND builder.activity_state='exited' AND builder.is_terminated=0
   AND recovery.created_at<=attempt.settled_at
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_agent_steps step WHERE step.id=attempt.agent_step_id AND step.send_status='SETTLED')
) BEGIN SELECT RAISE(ABORT,'project check continuation requires the same never-started retry and exited-Builder barrier'); END;
DROP TRIGGER cleardev_project_check_recovery_valid;
CREATE TRIGGER cleardev_project_check_recovery_valid BEFORE INSERT ON cleardev_project_check_recoveries
WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_runs prior
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=NEW.retry_check_run_id
 JOIN cleardev_complex_execution_check_specs spec ON spec.id=prior.check_spec_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=prior.task_attempt_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=run.id
 JOIN cleardev_product_stages stage ON stage.id=admission.stage_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_development_projects parent ON parent.id=stage.product_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=prior.candidate_commit_id
 WHERE prior.id=NEW.original_check_run_id AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND prior.retry_ordinal=0
   AND spec.check_kind='REQUIRED_CHECK' AND spec.task_mapping_id=task.id
   AND retry.check_spec_id=prior.check_spec_id AND retry.task_attempt_id=prior.task_attempt_id
   AND retry.candidate_commit_id=prior.candidate_commit_id AND retry.candidate_commit_sha=prior.candidate_commit_sha
   AND retry.status='PENDING' AND retry.retry_ordinal=1
   AND candidate.complex_execution_task_attempt_id=attempt.id AND candidate.work_item_id=item.id AND candidate.commit_sha=prior.candidate_commit_sha
   AND attempt.status='BLOCKED' AND attempt.reason_code='CHECKER_UNAVAILABLE' AND attempt.settled_at=NEW.original_attempt_settled_at
   AND attempt.round=(SELECT max(round) FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
   AND item.state='BLOCKED' AND item.paused_from_state='RUNNING'
   AND run.status='ACCEPTED' AND run.mode='STANDARD' AND run.reason_code='' AND run.settled_at IS NULL
   AND json_extract(run.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1'
   AND project.cancelled_at IS NULL AND parent.cancelled_at IS NULL AND project.state<>'PAUSED' AND parent.state<>'PAUSED'
   AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
   AND version.task_set_version=run.accepted_task_set_version
   AND stage.definition_sha256=json_extract(admission.contract_json,'$.stageDefinitionSha256')
   AND stage.discussion_id=(SELECT id FROM cleardev_product_discussions WHERE product_id=stage.product_id ORDER BY ordinal DESC LIMIT 1)
   AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_reviews WHERE task_attempt_id=attempt.id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_project_check_recoveries recovered
      JOIN cleardev_complex_execution_check_runs recovered_check ON recovered_check.id=recovered.original_check_run_id
      WHERE recovered_check.task_attempt_id=attempt.id AND recovered_check.candidate_commit_id=prior.candidate_commit_id)
   AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings WHERE task_mapping_id=task.id AND candidate_commit_id=candidate.id AND role='REVIEWER')
   AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs scope
      JOIN cleardev_complex_execution_check_specs scope_spec ON scope_spec.id=scope.check_spec_id
      WHERE scope.task_attempt_id=attempt.id AND scope.candidate_commit_id=candidate.id
        AND scope_spec.check_kind='SCOPE' AND scope.status='SETTLED' AND scope.result='PASS')
)
BEGIN SELECT RAISE(ABORT,'project dependency check recovery requires the same current candidate and settled infrastructure failure'); END;
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
      AND item.state='PLANNED' AND item.rework_count=0
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE task_mapping_id=task.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_mail_attempt_slots WHERE task_id=task.id)
      AND task.task_packet_sha256=NEW.previous_package_sha256 AND NEW.task_packet_sha256<>NEW.previous_package_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.eventId')=NEW.event_id
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.decisionSha256')=decision.result_sha256
      AND json_extract(NEW.task_packet_json,'$.runtimeRevision.previousPackageSha256')=NEW.previous_package_sha256
      AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision')
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria') BETWEEN 1 AND 12
      AND json_array_length(amendment.value,'$.additionalReviewCriteria') BETWEEN 1 AND 6
      AND json_array_length(NEW.task_packet_json,'$.reviewCriteria')=json_array_length(task.task_packet_json,'$.reviewCriteria')+json_array_length(amendment.value,'$.additionalReviewCriteria')
      AND NOT EXISTS(SELECT 1 FROM json_each(task.task_packet_json,'$.reviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||criterion.key||']') IS NOT criterion.value)
      AND NOT EXISTS(SELECT 1 FROM json_each(amendment.value,'$.additionalReviewCriteria') criterion
                     WHERE json_extract(NEW.task_packet_json,'$.reviewCriteria['||(json_array_length(task.task_packet_json,'$.reviewCriteria')+criterion.key)||']') IS NOT criterion.value)
 )
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;
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
      AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                     JOIN cleardev_work_items item ON item.id=task.work_item_id
                     WHERE task.execution_run_id=run.id AND item.state IN('BLOCKED','NEEDS_HUMAN'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id)
 ))
 OR (NEW.outcome='AMEND_REMAINING' AND (
     json_array_length(NEW.result_json,'$.amendments') NOT BETWEEN 1 AND 2
     OR (SELECT count(*) FROM cleardev_planner_runtime_decisions WHERE execution_run_id=NEW.execution_run_id AND outcome='AMEND_REMAINING')>=2))
BEGIN SELECT RAISE(ABORT,'runtime decision requires exact Planner settlement and cannot clear stale or failed execution facts'); END;
DROP TRIGGER cleardev_planner_runtime_request_insert;
CREATE TRIGGER cleardev_planner_runtime_request_insert
BEFORE INSERT ON cleardev_planner_runtime_requests
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests old WHERE old.event_id=NEW.event_id
             OR old.agent_step_id=NEW.agent_step_id OR (old.execution_run_id=NEW.execution_run_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=NEW.execution_run_id)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests prior
           LEFT JOIN cleardev_planner_runtime_decisions decision ON decision.event_id=prior.event_id
           WHERE prior.execution_run_id=NEW.execution_run_id AND decision.event_id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_decisions WHERE event_id=NEW.event_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_planner_runtime_events event
    JOIN cleardev_complex_execution_runs run ON run.id=event.execution_run_id
    JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
    JOIN cleardev_development_projects project ON project.id=run.development_project_id
    JOIN cleardev_complex_engineering_plans plan ON plan.id=run.plan_id
    JOIN cleardev_complex_role_bindings planner ON planner.id=plan.planner_role_binding_id
    WHERE event.id=NEW.event_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1'
      AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256
      AND version.task_set_version=run.accepted_task_set_version AND project.cancelled_at IS NULL AND project.state<>'PAUSED'
      AND planner.id=NEW.planner_role_binding_id AND planner.role='ENGINEERING_PLANNER'
      AND planner.status='BOUND' AND planner.ao_session_id=NEW.ao_session_id
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
      AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
      AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active
                     WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews WHERE execution_run_id=run.id)
 )
BEGIN SELECT RAISE(ABORT,'runtime request requires quiescent current facts, the original Planner and remaining durable quota'); END;
DROP TRIGGER cleardev_planner_runtime_event_insert;
CREATE TRIGGER cleardev_planner_runtime_event_insert
BEFORE INSERT ON cleardev_planner_runtime_events
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_events old
            WHERE old.id=NEW.id OR old.dispatch_id=NEW.dispatch_id OR old.source_step_id=NEW.source_step_id)
 OR NOT EXISTS(
    SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
    JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
    JOIN cleardev_complex_execution_agent_steps step ON step.id=attempt.agent_step_id
    WHERE attempt.id=NEW.dispatch_id AND run.id=NEW.execution_run_id AND run.status='ACCEPTED'
      AND json_extract(run.execution_package_json,'$.plannerCoordinationPolicy')='PLANNER_RUNTIME_COORDINATION_V1'
      AND step.id=NEW.source_step_id AND step.step_kind='BUILDER_TASK' AND step.send_status='SETTLED'
      AND step.request_id=attempt.id AND step.role_binding_id=attempt.builder_role_binding_id
      AND step.message_sha256=NEW.source_message_sha256 AND json_valid(step.final_message_text)
      AND json_extract(step.final_message_text,'$.kind')='BUILDER_RESULT'
      AND json_type(step.final_message_text,'$.coordination')='object'
      AND json_extract(NEW.report_json,'$.category') IN('ENGINEERING','PRODUCT')
      AND json_extract(NEW.report_json,'$.category')=json_extract(step.final_message_text,'$.coordination.category')
      AND length(trim(COALESCE(json_extract(NEW.report_json,'$.summary'),'')))>0
      AND json_array_length(NEW.report_json,'$.evidence') BETWEEN 1 AND 6
      AND json_array_length(NEW.report_json,'$.affectedTaskKeys') BETWEEN 1 AND 3
      AND NOT EXISTS(SELECT 1 FROM json_each(NEW.report_json,'$.affectedTaskKeys') affected
                     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings task
                                      WHERE task.execution_run_id=run.id AND task.plan_task_key=affected.value))
 )
BEGIN SELECT RAISE(ABORT,'runtime event requires the exact settled Builder report and cannot replace history'); END;
DROP VIEW cleardev_planner_project_contracts;
-- +goose StatementEnd
