-- Only an interrupted FIRST review of a confirmed product stage is eligible.
-- No result, unknown delivery, execution, direction change or later plan may
-- be silently converted into a retry. Session recovery remains control-plane work.
-- name: ListClearDevPlanningRecoveryCandidates :many
SELECT sqlc.embed(attempt), sqlc.embed(failure), sqlc.embed(plan), sqlc.embed(step)
FROM cleardev_agent_step_attempts AS attempt
JOIN cleardev_agent_attempt_events AS failure ON failure.attempt_id=attempt.id
JOIN cleardev_complex_agent_steps AS step ON step.id=attempt.logical_step_id
JOIN cleardev_complex_role_bindings AS role ON role.id=step.role_binding_id
JOIN sessions AS session ON session.id=attempt.ao_session_id
JOIN cleardev_development_projects AS requirement ON requirement.id=attempt.development_project_id
JOIN cleardev_product_stages AS stage ON stage.development_requirement_id=requirement.id
JOIN cleardev_contract_versions AS version ON version.development_project_id=requirement.id AND version.state='APPROVED'
JOIN cleardev_complex_engineering_plans AS plan ON plan.requirement_version_id=version.id AND plan.requirement_sha256=version.sha256
WHERE attempt.attempt_number=1 AND attempt.step_category='COMPLEX_PLANNING' AND attempt.step_kind='COMPLEX_PLAN_REVIEW'
 AND step.step_kind=attempt.step_kind AND step.role_binding_id=attempt.role_binding_id AND step.prompt_sha256=attempt.prompt_sha256
 AND step.client_message_id=attempt.client_message_id AND step.send_status='FAILED' AND step.reason_code='STEWARD_UNAVAILABLE'
 AND role.role='STEWARD' AND role.status='BOUND' AND role.ao_session_id=attempt.ao_session_id AND role.development_project_id=requirement.id
 AND session.project_id=requirement.ao_project_id AND session.is_terminated=0 AND session.id IN (SELECT session_id FROM cleardev_project_tool_sessions) AND session.session_mode='chat'
 AND requirement.cancelled_at IS NULL AND requirement.state<>'PAUSED'
 AND failure.client_message_id=attempt.client_message_id AND failure.prompt_sha256 IN ('',attempt.prompt_sha256)
 AND EXISTS(SELECT 1 FROM cleardev_agent_attempt_events sent WHERE sent.attempt_id=attempt.id AND sent.status='SENT' AND sent.client_message_id=attempt.client_message_id AND sent.prompt_sha256=attempt.prompt_sha256 AND sent.turn_id=failure.turn_id)
 AND failure.turn_id IS NOT NULL AND failure.turn_id<>''
 AND failure.status='INTERRUPTED' AND failure.failure_category='TURN_INTERRUPTED' AND failure.turn_state IN ('failed','interrupted')
 AND failure.id=(SELECT id FROM cleardev_agent_attempt_events WHERE attempt_id=attempt.id ORDER BY rowid DESC LIMIT 1)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_attempts WHERE logical_step_id=step.id AND attempt_number=2)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_results WHERE attempt_id=attempt.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_plan_reviews WHERE plan_id=plan.id)
 AND plan.development_project_id=requirement.id AND plan.created_at<=step.requested_at
 AND plan.version=(SELECT max(version) FROM cleardev_complex_engineering_plans WHERE development_project_id=requirement.id)
 AND version.version=(SELECT max(version) FROM cleardev_contract_versions WHERE development_project_id=requirement.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs WHERE development_project_id=requirement.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_direction_intents WHERE development_project_id=requirement.id);

-- name: ReopenClearDevInterruptedPlanningReview :execrows
UPDATE cleardev_complex_agent_steps SET send_status='PENDING',sent_at=NULL,turn_id=NULL,final_message_id=NULL,final_message_text=NULL,message_sha256=NULL,completed_at=NULL,failed_at=NULL,reason_code=''
WHERE id=? AND send_status='FAILED' AND reason_code='STEWARD_UNAVAILABLE';
