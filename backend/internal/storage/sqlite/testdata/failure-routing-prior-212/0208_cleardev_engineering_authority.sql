-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_engineering_revision_decisions (
 event_id TEXT PRIMARY KEY REFERENCES cleardev_planner_runtime_events(id),
 execution_run_id TEXT NOT NULL REFERENCES cleardev_complex_execution_runs(id),
 source TEXT NOT NULL CHECK(source='PLANNER'), outcome TEXT NOT NULL CHECK(outcome='AMEND_REMAINING'),
 reason_code TEXT NOT NULL CHECK(reason_code=''),result_json TEXT NOT NULL CHECK(json_valid(result_json)),
 result_sha256 TEXT NOT NULL CHECK(length(result_sha256)=64),summary TEXT NOT NULL,created_at TIMESTAMP NOT NULL,
 request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id)
);
CREATE TRIGGER cleardev_engineering_revision_insert BEFORE INSERT ON cleardev_engineering_revision_decisions
WHEN NOT EXISTS(SELECT 1 FROM cleardev_human_decision_requests human
 JOIN cleardev_human_decision_effects effect ON effect.request_id=human.id
 JOIN cleardev_planner_runtime_prior_decisions prior ON prior.event_id=NEW.event_id
 JOIN cleardev_planner_runtime_requests request ON request.event_id=NEW.event_id
 JOIN cleardev_complex_agent_steps step ON step.id=request.agent_step_id
 JOIN cleardev_complex_execution_runs run ON run.id=NEW.execution_run_id
 WHERE human.id=NEW.request_id AND human.decision_kind='AUTHORIZE_ENGINEERING_REVISION'
 AND human.status='RESOLVED' AND human.decision='APPROVE' AND effect.decision='APPROVE'
 AND human.development_project_id=run.development_project_id
 AND json_extract(human.binding_json,'$.executionRunId')=run.id
 AND json_extract(human.binding_json,'$.eventId')=NEW.event_id
 AND json_extract(human.binding_json,'$.resultSha256')=NEW.result_sha256
 AND json_extract(human.binding_json,'$.contextSha256')=request.context_sha256
 AND prior.execution_run_id=run.id AND prior.source='CONTROL_PLANE' AND prior.outcome='STOP'
 AND prior.result_json=NEW.result_json AND prior.result_sha256=NEW.result_sha256
 AND json_extract(NEW.result_json,'$.decision')='AMEND_REMAINING'
 AND json_extract(NEW.result_json,'$.eventId')=NEW.event_id AND json_extract(NEW.result_json,'$.contextSha256')=request.context_sha256
 AND step.send_status='SETTLED' AND step.request_id=NEW.event_id AND step.role_binding_id=request.planner_role_binding_id
 AND json_extract(step.final_message_text,'$.decision')='AMEND_REMAINING'
 AND run.status='ACCEPTED' AND run.settled_at IS NULL
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts attempt WHERE attempt.execution_run_id=run.id AND attempt.status IN('PENDING','RUNNING','OBSERVED','REVIEWING')))
BEGIN SELECT RAISE(ABORT,'engineering revision requires an exact native human approval'); END;
CREATE TRIGGER cleardev_engineering_revision_update BEFORE UPDATE ON cleardev_engineering_revision_decisions BEGIN SELECT RAISE(ABORT,'engineering authority history is immutable'); END;
CREATE TRIGGER cleardev_engineering_revision_delete BEFORE DELETE ON cleardev_engineering_revision_decisions BEGIN SELECT RAISE(ABORT,'engineering authority history is immutable'); END;
CREATE TRIGGER cleardev_engineering_revision_cdc AFTER INSERT ON cleardev_engineering_revision_decisions BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
DROP VIEW cleardev_planner_runtime_current_decisions;
CREATE VIEW cleardev_planner_runtime_current_decisions AS
 SELECT prior.* FROM cleardev_planner_runtime_prior_decisions prior
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_continue_reapplications fixed WHERE fixed.event_id=prior.event_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions approved WHERE approved.event_id=prior.event_id)
 UNION ALL SELECT * FROM cleardev_planner_continue_reapplications
 UNION ALL SELECT event_id,execution_run_id,source,outcome,reason_code,result_json,result_sha256,summary,created_at FROM cleardev_engineering_revision_decisions;
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
    JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=NEW.event_id
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
       AND json_remove(NEW.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution','$.writePaths','$.requiredChecks')=json_remove(task.task_packet_json,'$.reviewCriteria','$.runtimeRevision','$.projectExecution','$.writePaths','$.requiredChecks')
       AND json_remove(NEW.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision','$.writePaths','$.requiredChecks')=json_remove(task.task_packet_json,'$.projectExecution.basis','$.projectExecution.runtime','$.reviewCriteria','$.runtimeRevision','$.writePaths','$.requiredChecks')
       AND (
         (json_extract(NEW.task_packet_json,'$.projectExecution.basis.writePaths')=json_extract(task.task_packet_json,'$.projectExecution.basis.writePaths')
          AND json_extract(NEW.task_packet_json,'$.projectExecution.basis.checks')=json_extract(task.task_packet_json,'$.projectExecution.basis.checks')
          AND json_extract(NEW.task_packet_json,'$.writePaths')=json_extract(task.task_packet_json,'$.writePaths')
          AND json_extract(NEW.task_packet_json,'$.requiredChecks')=json_extract(task.task_packet_json,'$.requiredChecks'))
         OR EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions approved
           WHERE approved.event_id=NEW.event_id AND approved.execution_run_id=NEW.execution_run_id
           AND approved.result_sha256=decision.result_sha256
           AND approved.request_id=json_extract(NEW.task_packet_json,'$.runtimeRevision.humanDecisionRequestId')
           AND json_array_length(NEW.task_packet_json,'$.requiredChecks')=json_array_length(task.task_packet_json,'$.requiredChecks')
           AND NOT EXISTS(SELECT 1 FROM json_each(task.task_packet_json,'$.requiredChecks') original_check
             WHERE json_extract(original_check.value,'$.id') IS NOT json_extract(NEW.task_packet_json,'$.requiredChecks['||original_check.key||'].id'))
           AND NOT EXISTS(SELECT 1 FROM json_each(NEW.task_packet_json,'$.requiredChecks') new_check
             WHERE NOT EXISTS(SELECT 1 FROM json_each(NEW.task_packet_json,'$.projectExecution.basis.checks') basis_check
               WHERE json_extract(new_check.value,'$.id')=json_extract(basis_check.value,'$.id')
               AND json_extract(new_check.value,'$.argv')=json_extract(basis_check.value,'$.argv')
               AND json_extract(new_check.value,'$.timeoutSeconds')=json_extract(basis_check.value,'$.timeoutSeconds')))
           AND json_extract(NEW.task_packet_json,'$.writePaths')=COALESCE(json_extract(amendment.value,'$.writePaths'),json_extract(task.task_packet_json,'$.writePaths')))
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
BEGIN SELECT RAISE(ABORT,'runtime amendment may only append criteria to an intact never-attempted task; history and authority cannot change'); END;


ALTER TABLE cleardev_complex_execution_check_specs ADD COLUMN authority_event_id TEXT REFERENCES cleardev_engineering_revision_decisions(event_id);
DROP INDEX idx_cleardev_complex_execution_task_check_spec;
DROP INDEX idx_cleardev_complex_execution_integration_check_spec;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_check_spec ON cleardev_complex_execution_check_specs(task_mapping_id,check_kind,check_name) WHERE task_mapping_id IS NOT NULL AND authority_event_id IS NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_integration_check_spec ON cleardev_complex_execution_check_specs(execution_run_id,check_kind,check_name) WHERE task_mapping_id IS NULL AND authority_event_id IS NULL;
CREATE UNIQUE INDEX idx_cleardev_authorized_task_check_spec ON cleardev_complex_execution_check_specs(task_mapping_id,check_kind,check_name,authority_event_id) WHERE task_mapping_id IS NOT NULL AND authority_event_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_authorized_integration_check_spec ON cleardev_complex_execution_check_specs(execution_run_id,check_kind,check_name,authority_event_id) WHERE task_mapping_id IS NULL AND authority_event_id IS NOT NULL;
DROP TRIGGER cleardev_complex_execution_check_spec_insert_valid;
CREATE TRIGGER cleardev_complex_execution_check_spec_insert_valid BEFORE INSERT ON cleardev_complex_execution_check_specs
WHEN (NEW.authority_event_id IS NOT NULL AND NOT (EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions decision JOIN cleardev_planner_runtime_effective_tasks task ON task.execution_run_id=decision.execution_run_id
 WHERE decision.event_id=NEW.authority_event_id AND decision.execution_run_id=NEW.execution_run_id
 AND task.id=NEW.task_mapping_id AND json_extract(task.task_packet_json,'$.runtimeRevision.humanDecisionRequestId')=decision.request_id
 AND NEW.check_kind='SCOPE' AND NEW.check_name='' AND json(NEW.argv_json)=json('[]') AND NEW.timeout_seconds=0) OR EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions decision
 JOIN cleardev_planner_runtime_effective_tasks task ON task.execution_run_id=decision.execution_run_id
 JOIN json_each(task.task_packet_json,'$.projectExecution.basis.checks') approved
 WHERE decision.event_id=NEW.authority_event_id AND decision.execution_run_id=NEW.execution_run_id
 AND json_extract(task.task_packet_json,'$.runtimeRevision.humanDecisionRequestId')=decision.request_id
 AND json_extract(approved.value,'$.id')=NEW.check_name
 AND json_extract(approved.value,'$.timeoutSeconds')=NEW.timeout_seconds
 AND json(json_extract(approved.value,'$.argv'))=json(NEW.argv_json)
 AND (NEW.check_kind='INTEGRATION' OR (task.id=NEW.task_mapping_id AND EXISTS(
 SELECT 1 FROM json_each(task.task_packet_json,'$.requiredChecks') required
 WHERE json_extract(required.value,'$.id')=NEW.check_name
 AND json_extract(required.value,'$.timeoutSeconds')=NEW.timeout_seconds
 AND json(json_extract(required.value,'$.argv'))=json(NEW.argv_json))))))) OR NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_runs run
 LEFT JOIN cleardev_complex_execution_task_mappings task ON task.id=NEW.task_mapping_id
 WHERE run.id=NEW.execution_run_id AND
 ((NEW.check_kind='INTEGRATION' AND NEW.task_mapping_id IS NULL) OR (NEW.check_kind<>'INTEGRATION' AND task.execution_run_id=run.id))
) OR (
 NEW.check_kind<>'SCOPE' AND NOT (EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions decision
 JOIN cleardev_planner_runtime_effective_tasks task ON task.execution_run_id=decision.execution_run_id
 JOIN json_each(task.task_packet_json,'$.projectExecution.basis.checks') approved
 WHERE decision.event_id=NEW.authority_event_id AND decision.execution_run_id=NEW.execution_run_id
 AND json_extract(task.task_packet_json,'$.runtimeRevision.humanDecisionRequestId')=decision.request_id
 AND json_extract(approved.value,'$.id')=NEW.check_name
 AND json_extract(approved.value,'$.timeoutSeconds')=NEW.timeout_seconds
 AND json(json_extract(approved.value,'$.argv'))=json(NEW.argv_json)
 AND (NEW.check_kind='INTEGRATION' OR (task.id=NEW.task_mapping_id AND EXISTS(
 SELECT 1 FROM json_each(task.task_packet_json,'$.requiredChecks') required
 WHERE json_extract(required.value,'$.id')=NEW.check_name
 AND json_extract(required.value,'$.timeoutSeconds')=NEW.timeout_seconds
 AND json(json_extract(required.value,'$.argv'))=json(NEW.argv_json)))))) AND NOT EXISTS(
 SELECT 1 FROM cleardev_project_execution_admissions admission
 JOIN cleardev_complex_execution_runs run ON run.id=admission.execution_run_id
 JOIN json_each(admission.contract_json,'$.basis.checks') approved
 WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED'
   AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
   AND json_extract(approved.value,'$.id') IS NEW.check_name
   AND json_extract(approved.value,'$.timeoutSeconds') IS NEW.timeout_seconds
   AND json(json_extract(approved.value,'$.argv')) IS json(NEW.argv_json)
   AND (NEW.check_kind='INTEGRATION' OR EXISTS(
     SELECT 1 FROM cleardev_complex_execution_task_mappings task
     JOIN json_each(task.task_packet_json,'$.requiredChecks') task_check
     WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=run.id
       AND json_extract(task.task_packet_json,'$.schemaVersion') IS 4
       AND json_extract(task_check.value,'$.id') IS NEW.check_name
       AND json_extract(task_check.value,'$.timeoutSeconds') IS NEW.timeout_seconds
       AND json(json_extract(task_check.value,'$.argv')) IS json(NEW.argv_json)))
 ) AND NOT (
 NOT EXISTS(SELECT 1 FROM cleardev_project_execution_admissions WHERE execution_run_id=NEW.execution_run_id)
 AND NEW.timeout_seconds=60 AND (
 (NEW.check_name='email-unit' AND json(NEW.argv_json)=json('["node","--test","test/email.test.js"]') AND NEW.check_spec_sha256='feafc89ad5e5314058f839a1f280aa8cff20503583f40379c01f4d97dd031466') OR
 (NEW.check_name='deduplicate-unit' AND json(NEW.argv_json)=json('["node","--test","test/deduplicate.test.js"]') AND NEW.check_spec_sha256='04f123a6661183016601642f5aea4db485ef025a371e884a52b6ed4a11cee37e') OR
 (NEW.check_name='summary-unit' AND json(NEW.argv_json)=json('["node","--test","test/summary.test.js"]') AND NEW.check_spec_sha256='7de135c41f08e1eb60ace8d93642a74645118f3dfb54d515e0099a39059a372f') OR
 (NEW.check_name='all-tests' AND json(NEW.argv_json)=json('["node","--test"]') AND NEW.check_spec_sha256='82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586') OR
 (NEW.check_name IN ('demo-backend','CHK-LIB-BACKEND','CHK-INV-BACKEND','CHK-TKT-BACKEND','CHK-DEV-BACKEND') AND json(NEW.argv_json)=json('["npm","run","test:backend"]') AND NEW.check_spec_sha256='002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22') OR
 (NEW.check_name IN ('demo-database','CHK-LIB-DATABASE','CHK-INV-DATABASE','CHK-TKT-DATABASE','CHK-DEV-DATABASE') AND json(NEW.argv_json)=json('["npm","run","test:database"]') AND NEW.check_spec_sha256='7545c63f42eca5bb8b9db44014a4850045b83c27375beaeb6fc5424cd2d3f787') OR
 (NEW.check_name IN ('demo-api','CHK-LIB-API','CHK-INV-API','CHK-TKT-API','CHK-DEV-API') AND json(NEW.argv_json)=json('["npm","run","test:api"]') AND NEW.check_spec_sha256='c6c74e01bd47039cc69b2f71fa8948be8bcb1af25fcd4b2d467f9bd3d81b6be7') OR
 (NEW.check_name='demo-frontend' AND json(NEW.argv_json)=json('["npm","run","test:frontend"]') AND NEW.check_spec_sha256='153f7220bfe994393806417c9b0259ecf1148c573f963a3e39ccfe3abc5196f1') OR
 (NEW.check_name IN ('demo-integration','CHK-LIB-ALL','CHK-INV-ALL','CHK-TKT-ALL','CHK-DEV-ALL') AND json(NEW.argv_json)=json('["npm","test"]') AND NEW.check_spec_sha256='527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4') OR
 (NEW.check_name IN ('CHK-LIB-TYPE','CHK-INV-TYPE','CHK-TKT-TYPE','CHK-DEV-TYPE') AND json(NEW.argv_json)=json('["npm","run","typecheck"]') AND NEW.check_spec_sha256='e7f1a6143601f8eaeebf1837c7854c14f118864f0568ad75956bdba45af9e92a')
 ))
)
BEGIN SELECT RAISE(ABORT,'check specification must match its immutable admitted project or historical catalogue'); END;


CREATE VIEW cleardev_current_execution_check_specs AS
 SELECT spec.* FROM cleardev_complex_execution_check_specs spec WHERE NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_check_specs newer WHERE newer.execution_run_id=spec.execution_run_id AND newer.task_mapping_id IS spec.task_mapping_id AND newer.check_kind=spec.check_kind AND newer.check_name=spec.check_name AND newer.rowid>spec.rowid);
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_complex_execution_runs run ON run.id=task.execution_run_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=NEW.candidate_commit_id
 JOIN cleardev_complex_execution_reviews review ON review.task_attempt_id=attempt.id AND review.candidate_commit_id=candidate.id
 WHERE task.id=NEW.task_mapping_id AND attempt.id=NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id=attempt.id AND attempt.status='REVIEWING'
 AND ((NEW.replacement_recovery_id IS NULL AND NEW.replacement_attempt_id IS NULL AND NEW.replacement_result_id IS NULL AND review.status='SETTLED' AND review.verdict='PASS')
 OR EXISTS(
 SELECT 1 FROM cleardev_replacement_review_results replacement
 JOIN cleardev_fixed_recovery_requests request ON request.id=replacement.recovery_request_id
 JOIN cleardev_fixed_recovery_results result ON result.request_id=request.id AND result.outcome='PASS'
 JOIN cleardev_agent_step_attempts agent_attempt ON agent_attempt.id=replacement.attempt_id
 JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=replacement.result_id AND parsed.conclusion='VALID'
 WHERE replacement.recovery_request_id=NEW.replacement_recovery_id AND replacement.original_review_id=review.id AND replacement.attempt_id=NEW.replacement_attempt_id AND replacement.result_id=NEW.replacement_result_id AND replacement.verdict='PASS'
 AND request.execution_run_id=run.id AND json_extract(request.request_json,'$.candidateId')=candidate.id AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND agent_attempt.ao_session_id=json_extract(result.result_json,'$.sessionId')
 AND ((agent_attempt.attempt_number=2 AND agent_attempt.logical_step_id=review.agent_step_id AND NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id))
 OR EXISTS(SELECT 1 FROM cleardev_mail_replacement_final_sources source WHERE source.recovery_id=replacement.recovery_request_id AND source.review_id=review.id AND source.attempt_id=replacement.attempt_id AND source.result_id=replacement.result_id))
 ))
 AND EXISTS(SELECT 1 FROM cleardev_current_execution_check_specs scope JOIN cleardev_complex_execution_check_runs scope_run ON scope_run.check_spec_id=scope.id WHERE scope_run.id=NEW.scope_check_run_id AND scope.task_mapping_id=task.id AND scope.check_kind='SCOPE' AND scope_run.task_attempt_id=attempt.id AND scope_run.candidate_commit_id=candidate.id AND scope_run.status='SETTLED' AND scope_run.result='PASS')
 AND review.id=NEW.review_id
 AND json_array_length(NEW.required_check_runs_json)=(SELECT count(*) FROM cleardev_current_execution_check_specs WHERE task_mapping_id=task.id AND check_kind='REQUIRED_CHECK')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.required_check_runs_json) required_id WHERE typeof(required_id.value)<>'text' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN cleardev_current_execution_check_specs required_spec ON required_spec.id=required_run.check_spec_id WHERE required_run.id=required_id.value AND required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_current_execution_check_specs required_spec WHERE required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN json_each(NEW.required_check_runs_json) required_id ON required_id.value=required_run.id WHERE required_run.check_spec_id=required_spec.id AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_current_execution_check_specs spec WHERE spec.task_mapping_id=task.id AND spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs check_run WHERE check_run.check_spec_id=spec.id AND check_run.task_attempt_id=attempt.id AND check_run.candidate_commit_id=candidate.id AND check_run.status='SETTLED' AND check_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
) BEGIN SELECT RAISE(ABORT,'cleardev complex execution verified candidate lacks current scope, checks, review, or gate'); END;
DROP TRIGGER cleardev_complex_execution_result_update_valid;
CREATE TRIGGER cleardev_complex_execution_result_update_valid
BEFORE UPDATE ON cleardev_complex_execution_results
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.integration_candidate_id IS NOT NEW.integration_candidate_id
 OR OLD.created_at IS NOT NEW.created_at
 OR (OLD.completion_status = 'PENDING' AND NEW.completion_status <> 'COMMITTING')
 OR (OLD.completion_status = 'COMMITTING' AND NEW.completion_status <> 'COMPLETED')
 OR OLD.completion_status = 'COMPLETED'
 OR (NEW.completion_status IN ('COMMITTING', 'COMPLETED') AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND run.status = 'ACCEPTED'
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 ))
 OR (NEW.completion_status = 'COMMITTING' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_integration_candidates AS integration ON integration.id = NEW.integration_candidate_id
    WHERE run.id = NEW.execution_run_id
      AND integration.complex_execution_result_id = NEW.id
      AND NOT EXISTS (
          SELECT 1
          FROM cleardev_current_execution_check_specs AS spec
          WHERE spec.execution_run_id = run.id AND spec.check_kind = 'INTEGRATION'
            AND NOT EXISTS (
                SELECT 1
                FROM cleardev_complex_execution_result_checks AS result_check
                JOIN cleardev_complex_execution_check_runs AS check_run ON check_run.id = result_check.check_run_id
                WHERE result_check.result_id = NEW.id
                  AND check_run.check_spec_id = spec.id
                  AND check_run.candidate_commit_id = integration.complex_source_candidate_commit_id
                  AND check_run.status = 'SETTLED' AND check_run.result = 'PASS'
            )
      )
 ))
 OR (NEW.completion_status = 'COMPLETED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
                      WHERE task.execution_run_id = run.id AND work_item.state <> 'DONE')
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result is immutable or cannot complete');
END;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE engineering_authority_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO engineering_authority_down_guard SELECT NOT (EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions) OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests) OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs) OR EXISTS(SELECT 1 FROM cleardev_product_goals));
DROP TABLE engineering_authority_down_guard;
DROP VIEW cleardev_planner_runtime_current_decisions;
CREATE VIEW cleardev_planner_runtime_current_decisions AS
 SELECT prior.* FROM cleardev_planner_runtime_prior_decisions prior
 WHERE NOT EXISTS(SELECT 1 FROM cleardev_planner_continue_reapplications fixed WHERE fixed.event_id=prior.event_id)
 UNION ALL SELECT * FROM cleardev_planner_continue_reapplications;
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
    JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=NEW.event_id
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


DROP TRIGGER cleardev_complex_execution_check_spec_insert_valid;
DROP INDEX idx_cleardev_authorized_task_check_spec;
DROP INDEX idx_cleardev_authorized_integration_check_spec;
DROP INDEX idx_cleardev_complex_execution_task_check_spec;
DROP INDEX idx_cleardev_complex_execution_integration_check_spec;
DROP TRIGGER cleardev_complex_execution_verified_candidate_insert_valid;
CREATE TRIGGER cleardev_complex_execution_verified_candidate_insert_valid BEFORE INSERT ON cleardev_complex_execution_verified_candidates
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_complex_execution_runs run ON run.id=task.execution_run_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=NEW.candidate_commit_id
 JOIN cleardev_complex_execution_reviews review ON review.task_attempt_id=attempt.id AND review.candidate_commit_id=candidate.id
 WHERE task.id=NEW.task_mapping_id AND attempt.id=NEW.task_attempt_id AND candidate.complex_execution_task_attempt_id=attempt.id AND attempt.status='REVIEWING'
 AND ((NEW.replacement_recovery_id IS NULL AND NEW.replacement_attempt_id IS NULL AND NEW.replacement_result_id IS NULL AND review.status='SETTLED' AND review.verdict='PASS')
 OR EXISTS(
 SELECT 1 FROM cleardev_replacement_review_results replacement
 JOIN cleardev_fixed_recovery_requests request ON request.id=replacement.recovery_request_id
 JOIN cleardev_fixed_recovery_results result ON result.request_id=request.id AND result.outcome='PASS'
 JOIN cleardev_agent_step_attempts agent_attempt ON agent_attempt.id=replacement.attempt_id
 JOIN cleardev_agent_step_result_parses parsed ON parsed.result_id=replacement.result_id AND parsed.conclusion='VALID'
 WHERE replacement.recovery_request_id=NEW.replacement_recovery_id AND replacement.original_review_id=review.id AND replacement.attempt_id=NEW.replacement_attempt_id AND replacement.result_id=NEW.replacement_result_id AND replacement.verdict='PASS'
 AND request.execution_run_id=run.id AND json_extract(request.request_json,'$.candidateId')=candidate.id AND json_extract(request.request_json,'$.candidateSha')=candidate.commit_sha AND json_extract(request.request_json,'$.reviewPacketSha256')=review.review_packet_sha256
 AND agent_attempt.ao_session_id=json_extract(result.result_json,'$.sessionId')
 AND ((agent_attempt.attempt_number=2 AND agent_attempt.logical_step_id=review.agent_step_id AND NOT EXISTS(SELECT 1 FROM cleardev_bounded_mail_runs WHERE id=run.id))
 OR EXISTS(SELECT 1 FROM cleardev_mail_replacement_final_sources source WHERE source.recovery_id=replacement.recovery_request_id AND source.review_id=review.id AND source.attempt_id=replacement.attempt_id AND source.result_id=replacement.result_id))
 ))
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs scope JOIN cleardev_complex_execution_check_runs scope_run ON scope_run.check_spec_id=scope.id WHERE scope_run.id=NEW.scope_check_run_id AND scope.task_mapping_id=task.id AND scope.check_kind='SCOPE' AND scope_run.task_attempt_id=attempt.id AND scope_run.candidate_commit_id=candidate.id AND scope_run.status='SETTLED' AND scope_run.result='PASS')
 AND review.id=NEW.review_id
 AND json_array_length(NEW.required_check_runs_json)=(SELECT count(*) FROM cleardev_complex_execution_check_specs WHERE task_mapping_id=task.id AND check_kind='REQUIRED_CHECK')
 AND NOT EXISTS(SELECT 1 FROM json_each(NEW.required_check_runs_json) required_id WHERE typeof(required_id.value)<>'text' OR NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN cleardev_complex_execution_check_specs required_spec ON required_spec.id=required_run.check_spec_id WHERE required_run.id=required_id.value AND required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs required_spec WHERE required_spec.task_mapping_id=task.id AND required_spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs required_run JOIN json_each(NEW.required_check_runs_json) required_id ON required_id.value=required_run.id WHERE required_run.check_spec_id=required_spec.id AND required_run.task_attempt_id=attempt.id AND required_run.candidate_commit_id=candidate.id AND required_run.status='SETTLED' AND required_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs spec WHERE spec.task_mapping_id=task.id AND spec.check_kind='REQUIRED_CHECK' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs check_run WHERE check_run.check_spec_id=spec.id AND check_run.task_attempt_id=attempt.id AND check_run.candidate_commit_id=candidate.id AND check_run.status='SETTLED' AND check_run.result='PASS'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=run.requirement_version_id AND status='ACTIVE')
) BEGIN SELECT RAISE(ABORT,'cleardev complex execution verified candidate lacks current scope, checks, review, or gate'); END;
DROP TRIGGER cleardev_complex_execution_result_update_valid;
CREATE TRIGGER cleardev_complex_execution_result_update_valid
BEFORE UPDATE ON cleardev_complex_execution_results
WHEN OLD.execution_run_id IS NOT NEW.execution_run_id
 OR OLD.integration_candidate_id IS NOT NEW.integration_candidate_id
 OR OLD.created_at IS NOT NEW.created_at
 OR (OLD.completion_status = 'PENDING' AND NEW.completion_status <> 'COMMITTING')
 OR (OLD.completion_status = 'COMMITTING' AND NEW.completion_status <> 'COMPLETED')
 OR OLD.completion_status = 'COMPLETED'
 OR (NEW.completion_status IN ('COMMITTING', 'COMPLETED') AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND run.status = 'ACCEPTED'
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 ))
 OR (NEW.completion_status = 'COMMITTING' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_integration_candidates AS integration ON integration.id = NEW.integration_candidate_id
    WHERE run.id = NEW.execution_run_id
      AND integration.complex_execution_result_id = NEW.id
      AND NOT EXISTS (
          SELECT 1
          FROM cleardev_complex_execution_check_specs AS spec
          WHERE spec.execution_run_id = run.id AND spec.check_kind = 'INTEGRATION'
            AND NOT EXISTS (
                SELECT 1
                FROM cleardev_complex_execution_result_checks AS result_check
                JOIN cleardev_complex_execution_check_runs AS check_run ON check_run.id = result_check.check_run_id
                WHERE result_check.result_id = NEW.id
                  AND check_run.check_spec_id = spec.id
                  AND check_run.candidate_commit_id = integration.complex_source_candidate_commit_id
                  AND check_run.status = 'SETTLED' AND check_run.result = 'PASS'
            )
      )
 ))
 OR (NEW.completion_status = 'COMPLETED' AND NOT EXISTS (
    SELECT 1 FROM cleardev_complex_execution_runs AS run
    JOIN cleardev_contract_versions AS version ON version.id = run.requirement_version_id
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    WHERE run.id = NEW.execution_run_id
      AND project.id = run.development_project_id
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = run.requirement_sha256
      AND version.task_set_version = run.accepted_task_set_version
      AND NOT EXISTS (SELECT 1 FROM cleardev_complex_execution_task_mappings AS task
                      JOIN cleardev_work_items AS work_item ON work_item.id = task.work_item_id
                      WHERE task.execution_run_id = run.id AND work_item.state <> 'DONE')
      AND NOT EXISTS (SELECT 1 FROM cleardev_direction_stop_gates AS gate WHERE gate.requirement_version_id = run.requirement_version_id AND gate.status = 'ACTIVE')
 ))
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution result is immutable or cannot complete');
END;
DROP VIEW cleardev_current_execution_check_specs;
ALTER TABLE cleardev_complex_execution_check_specs DROP COLUMN authority_event_id;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_task_check_spec ON cleardev_complex_execution_check_specs(task_mapping_id,check_kind,check_name) WHERE task_mapping_id IS NOT NULL;
CREATE UNIQUE INDEX idx_cleardev_complex_execution_integration_check_spec ON cleardev_complex_execution_check_specs(execution_run_id,check_kind,check_name) WHERE task_mapping_id IS NULL;
CREATE TRIGGER cleardev_complex_execution_check_spec_insert_valid BEFORE INSERT ON cleardev_complex_execution_check_specs
WHEN NOT EXISTS(
 SELECT 1 FROM cleardev_complex_execution_runs run
 LEFT JOIN cleardev_complex_execution_task_mappings task ON task.id=NEW.task_mapping_id
 WHERE run.id=NEW.execution_run_id AND
 ((NEW.check_kind='INTEGRATION' AND NEW.task_mapping_id IS NULL) OR (NEW.check_kind<>'INTEGRATION' AND task.execution_run_id=run.id))
) OR (
 NEW.check_kind<>'SCOPE' AND NOT EXISTS(
 SELECT 1 FROM cleardev_project_execution_admissions admission
 JOIN cleardev_complex_execution_runs run ON run.id=admission.execution_run_id
 JOIN json_each(admission.contract_json,'$.basis.checks') approved
 WHERE run.id=NEW.execution_run_id AND run.status='ACCEPTED'
   AND json_extract(run.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
   AND json_extract(approved.value,'$.id') IS NEW.check_name
   AND json_extract(approved.value,'$.timeoutSeconds') IS NEW.timeout_seconds
   AND json(json_extract(approved.value,'$.argv')) IS json(NEW.argv_json)
   AND (NEW.check_kind='INTEGRATION' OR EXISTS(
     SELECT 1 FROM cleardev_complex_execution_task_mappings task
     JOIN json_each(task.task_packet_json,'$.requiredChecks') task_check
     WHERE task.id=NEW.task_mapping_id AND task.execution_run_id=run.id
       AND json_extract(task.task_packet_json,'$.schemaVersion') IS 4
       AND json_extract(task_check.value,'$.id') IS NEW.check_name
       AND json_extract(task_check.value,'$.timeoutSeconds') IS NEW.timeout_seconds
       AND json(json_extract(task_check.value,'$.argv')) IS json(NEW.argv_json)))
 ) AND NOT (
 NOT EXISTS(SELECT 1 FROM cleardev_project_execution_admissions WHERE execution_run_id=NEW.execution_run_id)
 AND NEW.timeout_seconds=60 AND (
 (NEW.check_name='email-unit' AND json(NEW.argv_json)=json('["node","--test","test/email.test.js"]') AND NEW.check_spec_sha256='feafc89ad5e5314058f839a1f280aa8cff20503583f40379c01f4d97dd031466') OR
 (NEW.check_name='deduplicate-unit' AND json(NEW.argv_json)=json('["node","--test","test/deduplicate.test.js"]') AND NEW.check_spec_sha256='04f123a6661183016601642f5aea4db485ef025a371e884a52b6ed4a11cee37e') OR
 (NEW.check_name='summary-unit' AND json(NEW.argv_json)=json('["node","--test","test/summary.test.js"]') AND NEW.check_spec_sha256='7de135c41f08e1eb60ace8d93642a74645118f3dfb54d515e0099a39059a372f') OR
 (NEW.check_name='all-tests' AND json(NEW.argv_json)=json('["node","--test"]') AND NEW.check_spec_sha256='82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586') OR
 (NEW.check_name IN ('demo-backend','CHK-LIB-BACKEND','CHK-INV-BACKEND','CHK-TKT-BACKEND','CHK-DEV-BACKEND') AND json(NEW.argv_json)=json('["npm","run","test:backend"]') AND NEW.check_spec_sha256='002e3eeb106f74ca3bdd9e4ea429b3842579702f26a50361d791725ec4303d22') OR
 (NEW.check_name IN ('demo-database','CHK-LIB-DATABASE','CHK-INV-DATABASE','CHK-TKT-DATABASE','CHK-DEV-DATABASE') AND json(NEW.argv_json)=json('["npm","run","test:database"]') AND NEW.check_spec_sha256='7545c63f42eca5bb8b9db44014a4850045b83c27375beaeb6fc5424cd2d3f787') OR
 (NEW.check_name IN ('demo-api','CHK-LIB-API','CHK-INV-API','CHK-TKT-API','CHK-DEV-API') AND json(NEW.argv_json)=json('["npm","run","test:api"]') AND NEW.check_spec_sha256='c6c74e01bd47039cc69b2f71fa8948be8bcb1af25fcd4b2d467f9bd3d81b6be7') OR
 (NEW.check_name='demo-frontend' AND json(NEW.argv_json)=json('["npm","run","test:frontend"]') AND NEW.check_spec_sha256='153f7220bfe994393806417c9b0259ecf1148c573f963a3e39ccfe3abc5196f1') OR
 (NEW.check_name IN ('demo-integration','CHK-LIB-ALL','CHK-INV-ALL','CHK-TKT-ALL','CHK-DEV-ALL') AND json(NEW.argv_json)=json('["npm","test"]') AND NEW.check_spec_sha256='527c484bcc3bb219e92ed61f99ff968f31143f89e53fda93d09b74c0ce3177d4') OR
 (NEW.check_name IN ('CHK-LIB-TYPE','CHK-INV-TYPE','CHK-TKT-TYPE','CHK-DEV-TYPE') AND json(NEW.argv_json)=json('["npm","run","typecheck"]') AND NEW.check_spec_sha256='e7f1a6143601f8eaeebf1837c7854c14f118864f0568ad75956bdba45af9e92a')
 ))
)
BEGIN SELECT RAISE(ABORT,'check specification must match its immutable admitted project or historical catalogue'); END;

DROP TABLE cleardev_engineering_revision_decisions;
-- +goose StatementEnd
