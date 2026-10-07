-- 0196: ordinary infrastructure retries of an admitted, native-approved candidate.
-- No retry cap, additional human decisions, authority tables or budget refunds.
-- +goose Up
-- +goose StatementBegin
CREATE VIEW cleardev_stopped_check_retry_chain AS
WITH RECURSIVE chain AS (
 SELECT offer.event_id AS event_id,offer.execution_run_id AS execution_run_id,original.id AS root_check_id,retry.id AS check_id,original.id AS predecessor_check_id,retry.retry_ordinal AS retry_ordinal
 FROM cleardev_stopped_check_requests offer
 JOIN cleardev_stopped_check_grants grant ON grant.event_id=offer.event_id AND grant.decision_request_id=offer.decision_request_id
 JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
 JOIN cleardev_complex_execution_check_runs original ON original.id=offer.original_check_id
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=offer.retry_check_id
 JOIN cleardev_workflow_recoveries recovery ON recovery.target_id=original.id AND recovery.successor_id=retry.id
 WHERE human.status='RESOLVED' AND human.decision='APPROVE' AND human.binding_json=offer.binding_json
 AND human.decision_kind='AUTHORIZE_STOPPED_CHECK_RECOVERY'
 AND original.retry_ordinal=0 AND original.status='FAILED' AND original.reason_code='CHECKER_UNAVAILABLE'
 AND original.result IS NULL AND original.exit_code IS NULL AND original.settled_at IS NOT NULL
 AND retry.retry_ordinal=1 AND retry.task_attempt_id=original.task_attempt_id AND retry.check_spec_id=original.check_spec_id
 AND retry.candidate_commit_id=original.candidate_commit_id AND retry.candidate_commit_sha=original.candidate_commit_sha
 AND recovery.action='RETRY_CHECK' AND recovery.dispatch_id=original.task_attempt_id AND recovery.candidate_sha=original.candidate_commit_sha
 UNION ALL
 SELECT chain.event_id,chain.execution_run_id,chain.root_check_id,retry.id,prior.id,retry.retry_ordinal
 FROM chain
 JOIN cleardev_complex_execution_check_runs prior ON prior.id=chain.check_id
 JOIN cleardev_workflow_recoveries recovery ON recovery.target_id=prior.id
 JOIN cleardev_complex_execution_check_runs retry ON retry.id=recovery.successor_id
 WHERE recovery.action='RETRY_CHECK' AND recovery.execution_run_id=chain.execution_run_id
 AND recovery.dispatch_id=prior.task_attempt_id AND recovery.candidate_sha=prior.candidate_commit_sha
 AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND prior.result IS NULL AND prior.exit_code IS NULL AND prior.settled_at IS NOT NULL
 AND retry.retry_ordinal=prior.retry_ordinal+1 AND retry.task_attempt_id=prior.task_attempt_id AND retry.check_spec_id=prior.check_spec_id
 AND retry.candidate_commit_id=prior.candidate_commit_id AND retry.candidate_commit_sha=prior.candidate_commit_sha
) SELECT * FROM chain;
DROP TRIGGER cleardev_stopped_check_retry_exact;
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

 AND NOT EXISTS(
 SELECT 1 FROM cleardev_stopped_check_retry_chain chain
 JOIN cleardev_complex_execution_check_runs prior ON prior.id=chain.check_id
 JOIN cleardev_workflow_recoveries recovery ON recovery.target_id=prior.id
 WHERE NEW.retry_ordinal>1 AND NEW.retry_ordinal=prior.retry_ordinal+1
 AND NEW.id=recovery.successor_id AND recovery.action='RETRY_CHECK'
 AND recovery.execution_run_id=chain.execution_run_id AND recovery.dispatch_id=prior.task_attempt_id
 AND recovery.candidate_sha=prior.candidate_commit_sha
 AND NEW.task_attempt_id=prior.task_attempt_id AND NEW.check_spec_id=prior.check_spec_id
 AND NEW.candidate_commit_id=prior.candidate_commit_id AND NEW.candidate_commit_sha=prior.candidate_commit_sha
 AND prior.status='FAILED' AND prior.reason_code='CHECKER_UNAVAILABLE' AND prior.result IS NULL AND prior.exit_code IS NULL AND prior.settled_at IS NOT NULL
 AND NEW.status='PENDING' AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_check_runs later WHERE later.task_attempt_id=prior.task_attempt_id AND later.check_spec_id=prior.check_spec_id AND later.retry_ordinal>prior.retry_ordinal))
BEGIN SELECT RAISE(ABORT,'stopped check retry must be unique and exactly human-authorized'); END;
DROP TRIGGER cleardev_stopped_check_start_current;
CREATE TRIGGER cleardev_stopped_check_start_current BEFORE UPDATE OF status ON cleardev_complex_execution_check_runs
WHEN NEW.status='STARTED' AND NEW.retry_ordinal>0 AND EXISTS(SELECT 1 FROM cleardev_stopped_check_requests offer JOIN cleardev_complex_execution_check_runs root ON root.id=offer.original_check_id WHERE root.task_attempt_id=NEW.task_attempt_id AND root.check_spec_id=NEW.check_spec_id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_stopped_check_requests offer
 JOIN cleardev_stopped_check_recovery_current current ON current.event_id=offer.event_id
 JOIN cleardev_stopped_check_retry_chain chain ON chain.event_id=offer.event_id
 JOIN sessions session ON session.id=json_extract(offer.binding_json,'$.aoSessionId')
 JOIN cleardev_complex_execution_runs run ON run.id=offer.execution_run_id
 JOIN cleardev_contract_versions version ON version.id=run.requirement_version_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 WHERE chain.check_id=NEW.id AND NEW.retry_ordinal=chain.retry_ordinal AND NEW.candidate_commit_sha=json_extract(offer.binding_json,'$.candidateSha')
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
CREATE TEMP TABLE stopped_technical_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO stopped_technical_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_stopped_check_retry_chain WHERE retry_ordinal>1) THEN 0 ELSE 1 END;
DROP TABLE stopped_technical_down_guard;
DROP TRIGGER cleardev_stopped_check_retry_exact;
DROP TRIGGER cleardev_stopped_check_start_current;
DROP VIEW cleardev_stopped_check_retry_chain;
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
