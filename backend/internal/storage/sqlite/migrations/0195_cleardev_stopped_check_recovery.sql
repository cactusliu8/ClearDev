-- 0195: a native-human-authorized same-candidate check after an engineering STOP.
-- Original STOP, failed check, spent counters and all role/message budgets survive.
-- +goose Up

-- +goose StatementBegin
CREATE TABLE cleardev_stopped_check_requests (
 id TEXT PRIMARY KEY NOT NULL,
 execution_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_runs(id),
 event_id TEXT NOT NULL UNIQUE REFERENCES cleardev_planner_runtime_events(id),
 original_check_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_check_runs(id),
 retry_check_id TEXT NOT NULL UNIQUE,
 decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id),
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json)),
 context_json TEXT NOT NULL CHECK(json_valid(context_json)),
 supplement TEXT NOT NULL CHECK(length(trim(supplement)) BETWEEN 1 AND 16000),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TABLE cleardev_stopped_check_grants (
 event_id TEXT PRIMARY KEY NOT NULL REFERENCES cleardev_stopped_check_requests(event_id),
 execution_run_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_execution_runs(id),
 decision_request_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_requests(id),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_stopped_check_request_exact BEFORE INSERT ON cleardev_stopped_check_requests
WHEN EXISTS(SELECT 1 FROM cleardev_stopped_check_requests old WHERE old.id=NEW.id OR old.execution_run_id=NEW.execution_run_id OR old.event_id=NEW.event_id OR old.original_check_id=NEW.original_check_id OR old.retry_check_id=NEW.retry_check_id OR old.decision_request_id=NEW.decision_request_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_human_decision_requests human
 JOIN cleardev_planner_runtime_events event ON event.id=NEW.event_id
 JOIN cleardev_planner_runtime_current_decisions stop ON stop.event_id=event.id
 JOIN cleardev_complex_execution_runs run ON run.id=event.execution_run_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=event.dispatch_id
 JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
 JOIN cleardev_work_items item ON item.id=task.work_item_id
 JOIN cleardev_planner_runtime_effective_tasks effective ON effective.id=task.id
 JOIN cleardev_complex_execution_check_runs original ON original.id=NEW.original_check_id
 JOIN cleardev_complex_execution_check_specs spec ON spec.id=original.check_spec_id
 JOIN cleardev_candidate_commits candidate ON candidate.id=original.candidate_commit_id
 JOIN cleardev_complex_execution_role_bindings builder ON builder.id=attempt.builder_role_binding_id
 JOIN sessions session ON session.id=builder.ao_session_id
 WHERE human.id=NEW.decision_request_id AND human.decision_kind='AUTHORIZE_STOPPED_CHECK_RECOVERY'
 AND human.status='PENDING' AND human.binding_schema_version=1 AND human.binding_json=NEW.binding_json AND human.development_project_id=project.id
 AND run.id=NEW.execution_run_id AND run.mode='STANDARD' AND run.status='ACCEPTED' AND run.settled_at IS NULL
 AND json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1'
 AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.sha256=run.requirement_sha256 AND version.task_set_version=run.accepted_task_set_version
 AND project.cancelled_at IS NULL AND project.paused_from_state IS NULL AND project.state<>'PAUSED'
 AND stop.source='PLANNER' AND stop.outcome='STOP' AND stop.result_sha256=json_extract(NEW.binding_json,'$.stopSha256')
 AND json_extract(event.report_json,'$.category')='ENGINEERING'
 AND json_array_length(stop.result_json,'$.questions')=0 AND json_array_length(stop.result_json,'$.amendments')=0
 AND original.task_attempt_id=attempt.id AND original.status='FAILED' AND original.reason_code='CHECKER_UNAVAILABLE'
 AND original.settled_at IS NOT NULL AND original.result IS NULL AND original.retry_ordinal=0 AND original.exit_code IS NULL
 AND spec.check_kind='REQUIRED_CHECK' AND spec.task_mapping_id=task.id
 AND candidate.complex_execution_task_attempt_id=attempt.id AND candidate.commit_sha=original.candidate_commit_sha
 AND attempt.status IN('REWORK','BLOCKED') AND attempt.reason_code='CHECKER_UNAVAILABLE' AND attempt.settled_at IS NOT NULL
 AND item.state IN('REWORK','BLOCKED') AND item.rework_count=json_extract(NEW.binding_json,'$.reworkCount')
 AND json_extract(NEW.binding_json,'$.developmentRequirementId')=project.id AND json_extract(NEW.binding_json,'$.executionRunId')=run.id
 AND json_extract(NEW.binding_json,'$.eventId')=event.id AND json_extract(NEW.binding_json,'$.dispatchId')=attempt.id
 AND json_extract(NEW.binding_json,'$.taskId')=task.id AND json_extract(NEW.binding_json,'$.checkRunId')=original.id
 AND json_extract(NEW.binding_json,'$.candidateSha')=candidate.commit_sha AND json_extract(NEW.binding_json,'$.taskPacketSha256')=effective.task_packet_sha256
 AND NEW.retry_check_id=original.id||':stopped-check-retry' AND json_extract(NEW.binding_json,'$.retryCheckRunId')=NEW.retry_check_id
 AND builder.role='BUILDER' AND builder.status='BOUND' AND builder.execution_run_id=run.id
 AND json_extract(NEW.binding_json,'$.builderRoleBindingId')=builder.id AND json_extract(NEW.binding_json,'$.aoSessionId')=session.id
 AND session.kind='worker' AND session.session_mode='chat' AND session.permission_mode='auto' AND session.is_terminated=0
 AND session.project_id=project.ao_project_id AND session.creation_idempotency_key=builder.session_creation_idempotency_key
 AND session.provider_conversation_id=json_extract(NEW.binding_json,'$.providerConversationId') AND session.workspace_path=json_extract(NEW.binding_json,'$.workspacePath')
 AND session.creation_idempotency_key=json_extract(NEW.binding_json,'$.sessionCreationKey') AND session.creation_request_fingerprint=json_extract(NEW.binding_json,'$.creationFingerprint')
 AND session.harness=json_extract(NEW.binding_json,'$.harness') AND session.model=json_extract(NEW.binding_json,'$.model')
 AND session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts later WHERE later.task_mapping_id=task.id AND later.round>attempt.round)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs later WHERE later.task_attempt_id=attempt.id AND later.check_spec_id=spec.id AND later.retry_ordinal>0)
 AND NOT EXISTS(SELECT 1 FROM cleardev_workflow_recoveries recovery WHERE recovery.target_id=original.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_verified_candidates verified WHERE verified.task_attempt_id=attempt.id)
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs scopecheck JOIN cleardev_complex_execution_check_specs scopespec ON scopespec.id=scopecheck.check_spec_id
   WHERE scopecheck.task_attempt_id=attempt.id AND scopecheck.candidate_commit_id=candidate.id AND scopespec.check_kind='SCOPE' AND scopecheck.status='SETTLED' AND scopecheck.result='PASS')
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_mappings other JOIN cleardev_work_items otheritem ON otheritem.id=other.work_item_id
   WHERE other.execution_run_id=run.id AND other.id<>task.id AND NOT EXISTS(
     SELECT 1 FROM cleardev_complex_execution_task_attempts finished WHERE finished.task_mapping_id=other.id AND finished.status='VERIFIED' AND otheritem.rework_count<=finished.round
     AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts later WHERE later.task_mapping_id=other.id AND later.round>finished.round)))

 AND NOT EXISTS(SELECT 1 FROM cleardev_planner_runtime_barriers barrier WHERE barrier.execution_run_id=run.id AND barrier.event_id<>event.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE')
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts active WHERE active.execution_run_id=run.id AND active.status IN('PENDING','RUNNING','OBSERVED','REVIEWING'))
 AND NOT EXISTS(SELECT 1 FROM conversation_turns turn WHERE turn.handled_by_session_id=session.id AND turn.state NOT IN('completed','failed','interrupted'))
 )
BEGIN SELECT RAISE(ABORT,'stopped check recovery requires exact settled engineering STOP, candidate, original identity and failed checker'); END;
CREATE TRIGGER cleardev_stopped_check_grant_exact BEFORE INSERT ON cleardev_stopped_check_grants
WHEN EXISTS(SELECT 1 FROM cleardev_stopped_check_grants old WHERE old.event_id=NEW.event_id OR old.execution_run_id=NEW.execution_run_id OR old.decision_request_id=NEW.decision_request_id)
 OR NOT EXISTS(
 SELECT 1 FROM cleardev_stopped_check_requests offer JOIN cleardev_human_decision_requests human ON human.id=offer.decision_request_id
 WHERE offer.event_id=NEW.event_id AND offer.execution_run_id=NEW.execution_run_id AND human.id=NEW.decision_request_id
 AND human.decision_kind='AUTHORIZE_STOPPED_CHECK_RECOVERY' AND human.status='RESOLVED' AND human.decision='APPROVE'
 AND human.binding_json=offer.binding_json AND human.resolved_at=NEW.created_at
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs WHERE id=offer.retry_check_id)
 )
BEGIN SELECT RAISE(ABORT,'same-candidate check recovery needs its exact native approval'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_stopped_check_requests_update BEFORE UPDATE ON cleardev_stopped_check_requests BEGIN SELECT RAISE(ABORT,'stopped check authority is immutable'); END;
CREATE TRIGGER cleardev_stopped_check_requests_delete BEFORE DELETE ON cleardev_stopped_check_requests BEGIN SELECT RAISE(ABORT,'stopped check authority history is immutable'); END;
CREATE TRIGGER cleardev_stopped_check_requests_cdc AFTER INSERT ON cleardev_stopped_check_requests BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_stopped_check_grants_update BEFORE UPDATE ON cleardev_stopped_check_grants BEGIN SELECT RAISE(ABORT,'stopped check authority is immutable'); END;
CREATE TRIGGER cleardev_stopped_check_grants_delete BEFORE DELETE ON cleardev_stopped_check_grants BEGIN SELECT RAISE(ABORT,'stopped check authority history is immutable'); END;
CREATE TRIGGER cleardev_stopped_check_grants_cdc AFTER INSERT ON cleardev_stopped_check_grants BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'eventId',NEW.event_id),NEW.created_at
 FROM cleardev_complex_execution_runs run JOIN cleardev_development_projects project ON project.id=run.development_project_id WHERE run.id=NEW.execution_run_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE VIEW cleardev_stopped_check_recovery_current AS
SELECT offer.event_id,offer.execution_run_id,offer.original_check_id,offer.retry_check_id
FROM cleardev_stopped_check_requests offer
JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id
JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
JOIN cleardev_planner_runtime_current_decisions stop ON stop.event_id=offer.event_id
JOIN cleardev_complex_execution_task_attempts attempt ON attempt.id=json_extract(offer.binding_json,'$.dispatchId')
JOIN cleardev_candidate_commits candidate ON candidate.complex_execution_task_attempt_id=attempt.id
JOIN cleardev_complex_execution_task_mappings task ON task.id=attempt.task_mapping_id
JOIN cleardev_work_items item ON item.id=task.work_item_id
JOIN cleardev_planner_runtime_effective_tasks effective ON effective.id=task.id
WHERE human.status='RESOLVED' AND human.decision='APPROVE' AND human.decision_kind='AUTHORIZE_STOPPED_CHECK_RECOVERY'
 AND human.binding_json=offer.binding_json AND human.id=offer.decision_request_id
 AND stop.source='PLANNER' AND stop.outcome='STOP' AND stop.result_sha256=json_extract(offer.binding_json,'$.stopSha256')
 AND attempt.execution_run_id=offer.execution_run_id AND attempt.status IN('OBSERVED','REVIEWING','VERIFIED')
 AND task.id=json_extract(offer.binding_json,'$.taskId') AND item.rework_count=json_extract(offer.binding_json,'$.reworkCount')
 AND item.state IN('RUNNING','REVIEW','DONE') AND candidate.commit_sha=json_extract(offer.binding_json,'$.candidateSha')
 AND effective.task_packet_sha256=json_extract(offer.binding_json,'$.taskPacketSha256')
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts later WHERE later.task_mapping_id=task.id AND later.round>attempt.round);
-- +goose StatementEnd

-- +goose StatementBegin
DROP VIEW cleardev_planner_runtime_barriers;
CREATE VIEW cleardev_planner_runtime_barriers AS
SELECT event.id AS event_id,event.execution_run_id,
       COALESCE(decision.reason_code,'PLANNER_COORDINATION_PENDING') AS reason_code
FROM cleardev_planner_runtime_events event
LEFT JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=event.id
WHERE (decision.event_id IS NULL OR decision.outcome NOT IN('CONTINUE','AMEND_REMAINING')
 OR (decision.outcome='AMEND_REMAINING' AND
     (SELECT count(*) FROM cleardev_planner_runtime_task_amendments amendment WHERE amendment.event_id=event.id)
       <>json_array_length(decision.result_json,'$.amendments')))
AND NOT (decision.source='CONTROL_PLANE' AND decision.outcome='STOP'
 AND EXISTS(SELECT 1 FROM cleardev_human_decision_requests grant
   WHERE grant.decision_kind='AUTHORIZE_COORDINATION_REPAIR' AND grant.status='RESOLVED' AND grant.decision='APPROVE'
     AND json_extract(grant.binding_json,'$.eventId')=event.id))
AND NOT EXISTS(SELECT 1 FROM cleardev_stopped_check_recovery_current recovery WHERE recovery.event_id=event.id);
-- +goose StatementEnd

-- +goose StatementBegin
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
CREATE TRIGGER cleardev_complex_execution_attempt_update_valid BEFORE UPDATE ON cleardev_complex_execution_task_attempts
WHEN OLD.task_mapping_id IS NOT NEW.task_mapping_id OR OLD.execution_run_id IS NOT NEW.execution_run_id OR OLD.builder_role_binding_id IS NOT NEW.builder_role_binding_id OR OLD.agent_step_id IS NOT NEW.agent_step_id OR OLD.round IS NOT NEW.round OR OLD.base_commit_sha IS NOT NEW.base_commit_sha OR OLD.batch_id IS NOT NEW.batch_id
 OR (OLD.status NOT IN('PENDING','RUNNING','OBSERVED','REVIEWING')
 AND NOT(OLD.status IN('REWORK','BLOCKED') AND OLD.reason_code='CHECKER_UNAVAILABLE' AND OLD.settled_at IS NOT NULL
 AND NEW.status='OBSERVED' AND NEW.reason_code='' AND NEW.settled_at IS NULL AND EXISTS(
 SELECT 1 FROM cleardev_stopped_check_requests offer JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id
 JOIN cleardev_workflow_recoveries recovery ON recovery.target_id=offer.original_check_id
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=offer.retry_check_id
 WHERE json_extract(offer.binding_json,'$.dispatchId')=OLD.id AND offer.execution_run_id=OLD.execution_run_id
 AND recovery.action='RETRY_CHECK' AND recovery.dispatch_id=OLD.id AND recovery.successor_id=retry.id
 AND recovery.original_status=OLD.status AND recovery.original_reason=OLD.reason_code AND recovery.original_stopped_at=OLD.settled_at
 AND retry.status='PENDING' AND retry.task_attempt_id=OLD.id AND retry.retry_ordinal=1))
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
CREATE TRIGGER cleardev_stopped_check_retry_exact BEFORE INSERT ON cleardev_complex_execution_check_runs
WHEN NEW.retry_ordinal>0 AND EXISTS(
 SELECT 1 FROM cleardev_stopped_check_requests offer JOIN cleardev_complex_execution_check_runs original ON original.id=offer.original_check_id
 WHERE original.task_attempt_id=NEW.task_attempt_id AND original.check_spec_id=NEW.check_spec_id)
 AND NOT EXISTS(
 SELECT 1 FROM cleardev_stopped_check_requests offer
 JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id
 JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
 JOIN cleardev_complex_execution_check_runs original ON original.id=offer.original_check_id
 JOIN cleardev_workflow_recoveries recovery ON recovery.target_id=original.id
 WHERE NEW.id=offer.retry_check_id AND NEW.task_attempt_id=original.task_attempt_id AND NEW.check_spec_id=original.check_spec_id
 AND NEW.candidate_commit_id=original.candidate_commit_id AND NEW.candidate_commit_sha=original.candidate_commit_sha
 AND NEW.status='PENDING' AND NEW.retry_ordinal=1 AND original.retry_ordinal=0 AND original.status='FAILED' AND original.reason_code='CHECKER_UNAVAILABLE'
 AND human.status='RESOLVED' AND human.decision='APPROVE' AND human.binding_json=offer.binding_json
 AND recovery.action='RETRY_CHECK' AND recovery.successor_id=NEW.id AND recovery.candidate_sha=NEW.candidate_commit_sha)
BEGIN SELECT RAISE(ABORT,'stopped check retry must be unique and exactly human-authorized'); END;
CREATE TRIGGER cleardev_stopped_check_no_counter_refund BEFORE UPDATE ON cleardev_work_items
WHEN NEW.rework_count<OLD.rework_count AND EXISTS(
 SELECT 1 FROM cleardev_stopped_check_requests offer JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id
 WHERE json_extract(offer.binding_json,'$.taskId')=OLD.complex_execution_task_id)
BEGIN SELECT RAISE(ABORT,'stopped check recovery cannot refund spent task counters'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_stopped_check_no_new_dispatch BEFORE INSERT ON cleardev_complex_execution_task_attempts
WHEN EXISTS(SELECT 1 FROM cleardev_stopped_check_requests offer
 JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id
 JOIN cleardev_planner_runtime_current_decisions stop ON stop.event_id=offer.event_id
 WHERE offer.execution_run_id=NEW.execution_run_id AND stop.source='PLANNER' AND stop.outcome='STOP'
 AND stop.result_sha256=json_extract(offer.binding_json,'$.stopSha256'))
BEGIN SELECT RAISE(ABORT,'same-candidate check recovery cannot authorize a new Builder dispatch'); END;
CREATE TRIGGER cleardev_stopped_check_start_current BEFORE UPDATE OF status ON cleardev_complex_execution_check_runs
WHEN NEW.status='STARTED' AND EXISTS(SELECT 1 FROM cleardev_stopped_check_requests WHERE retry_check_id=NEW.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_stopped_check_requests offer
 JOIN cleardev_stopped_check_recovery_current current ON current.event_id=offer.event_id
 JOIN sessions session ON session.id=json_extract(offer.binding_json,'$.aoSessionId')
 JOIN cleardev_complex_execution_runs run ON run.id=offer.execution_run_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 WHERE offer.retry_check_id=NEW.id AND NEW.retry_ordinal=1 AND NEW.candidate_commit_sha=json_extract(offer.binding_json,'$.candidateSha')
 AND session.permission_mode='auto' AND session.session_mode='chat' AND session.is_terminated=0 AND session.kind='worker'
 AND session.creation_idempotency_key=json_extract(offer.binding_json,'$.sessionCreationKey')
 AND session.creation_request_fingerprint=json_extract(offer.binding_json,'$.creationFingerprint')
 AND session.provider_conversation_id=json_extract(offer.binding_json,'$.providerConversationId')
 AND session.workspace_path=json_extract(offer.binding_json,'$.workspacePath') AND session.harness=json_extract(offer.binding_json,'$.harness')
 AND session.model=json_extract(offer.binding_json,'$.model') AND session.id IN(SELECT session_id FROM cleardev_project_tool_sessions)
 AND run.status='ACCEPTED' AND run.settled_at IS NULL AND version.state='APPROVED' AND version.superseded_by_id IS NULL
 AND version.sha256=run.requirement_sha256 AND version.task_set_version=run.accepted_task_set_version
 AND project.cancelled_at IS NULL AND project.paused_from_state IS NULL AND project.state<>'PAUSED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE requirement_version_id=version.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_stop_gates WHERE requirement_version_id=version.id AND status='ACTIVE'))
BEGIN SELECT RAISE(ABORT,'stopped check may start only against the exact current authorized candidate'); END;
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
CREATE TEMP TABLE cleardev_stopped_check_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_stopped_check_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_stopped_check_requests) OR EXISTS(SELECT 1 FROM cleardev_stopped_check_grants)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests WHERE decision_kind='AUTHORIZE_STOPPED_CHECK_RECOVERY')
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_stopped_check_down_guard;
DROP TRIGGER cleardev_stopped_check_no_new_dispatch;
DROP TRIGGER cleardev_stopped_check_start_current;
DROP TRIGGER cleardev_stopped_check_no_counter_refund;
DROP TRIGGER cleardev_stopped_check_retry_exact;
DROP TRIGGER cleardev_complex_execution_attempt_update_valid;
DROP VIEW cleardev_planner_runtime_barriers;
-- +goose StatementEnd

-- +goose StatementBegin
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
CREATE VIEW cleardev_planner_runtime_barriers AS
SELECT event.id AS event_id,event.execution_run_id,
       COALESCE(decision.reason_code,'PLANNER_COORDINATION_PENDING') AS reason_code
FROM cleardev_planner_runtime_events event
LEFT JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=event.id
WHERE (decision.event_id IS NULL OR decision.outcome NOT IN('CONTINUE','AMEND_REMAINING')
 OR (decision.outcome='AMEND_REMAINING' AND
     (SELECT count(*) FROM cleardev_planner_runtime_task_amendments amendment WHERE amendment.event_id=event.id)
       <>json_array_length(decision.result_json,'$.amendments')))
AND NOT (decision.source='CONTROL_PLANE' AND decision.outcome='STOP'
 AND EXISTS(SELECT 1 FROM cleardev_human_decision_requests grant
   WHERE grant.decision_kind='AUTHORIZE_COORDINATION_REPAIR' AND grant.status='RESOLVED' AND grant.decision='APPROVE'
     AND json_extract(grant.binding_json,'$.eventId')=event.id));
-- +goose StatementEnd

-- +goose StatementBegin
DROP VIEW cleardev_stopped_check_recovery_current;
DROP TABLE cleardev_stopped_check_grants;
DROP TABLE cleardev_stopped_check_requests;
-- +goose StatementEnd
