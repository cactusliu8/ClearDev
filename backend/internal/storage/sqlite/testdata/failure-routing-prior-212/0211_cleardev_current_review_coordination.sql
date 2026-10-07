-- +goose Up
-- Use the same latest review as GetClearDevRequirementFinalReview.
-- Any unfinished review still prevents a new coordination request.
-- +goose StatementBegin
DROP TRIGGER cleardev_planner_runtime_request_insert;
CREATE TRIGGER cleardev_planner_runtime_request_insert
BEFORE INSERT ON cleardev_planner_runtime_requests
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests old WHERE old.event_id=NEW.event_id
             OR old.agent_step_id=NEW.agent_step_id OR (old.execution_run_id=NEW.execution_run_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=NEW.execution_run_id)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests prior
           LEFT JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=prior.event_id
           WHERE prior.execution_run_id=NEW.execution_run_id AND decision.event_id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_current_decisions WHERE event_id=NEW.event_id)
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
      AND NOT EXISTS(SELECT 1 FROM cleardev_requirement_final_reviews review WHERE review.execution_run_id=run.id AND (
          NOT (json_extract(run.execution_package_json,'$.planValidationPolicy')='PROJECT_EXECUTION_V1')
          OR review.status NOT IN('SETTLED','FAILED')
          OR (review.rowid=(SELECT max(latest.rowid) FROM cleardev_requirement_final_reviews latest WHERE latest.execution_run_id=run.id)
              AND (review.status<>'SETTLED' OR review.verdict NOT IN('BLOCKED','REWORK')))))
 )
 OR (NEW.ordinal=3 AND NOT EXISTS(
 SELECT 1 FROM cleardev_extra_coordination_grants grant
 JOIN cleardev_extra_coordination_requests offer ON offer.event_id=grant.event_id
 JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
 JOIN sessions session ON session.id=NEW.ao_session_id
 WHERE grant.event_id=NEW.event_id AND grant.execution_run_id=NEW.execution_run_id
 AND human.status='RESOLVED' AND human.decision='APPROVE' AND human.decision_kind='AUTHORIZE_EXTRA_PLANNER_COORDINATION'
 AND human.binding_json=offer.binding_json AND NEW.context_json=offer.context_json AND NEW.prompt=offer.prompt
 AND NEW.context_sha256=json_extract(offer.binding_json,'$.contextSha256')
 AND NEW.planner_role_binding_id=json_extract(offer.binding_json,'$.plannerRoleBindingId')
 AND NEW.ao_session_id=json_extract(offer.binding_json,'$.aoSessionId')
 AND session.provider_conversation_id=json_extract(offer.binding_json,'$.providerConversationId')
 AND session.creation_request_fingerprint=json_extract(offer.binding_json,'$.creationFingerprint')
 AND session.workspace_path=json_extract(offer.binding_json,'$.workspacePath')
 AND session.harness=json_extract(offer.binding_json,'$.harness') AND session.model=json_extract(offer.binding_json,'$.model')
 AND session.permission_mode='auto' AND session.session_mode='chat' AND session.is_terminated=0
 ))
BEGIN SELECT RAISE(ABORT,'runtime request requires quiescent current facts, the original Planner and remaining durable quota'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE current_review_downgrade_guard(ok INTEGER CHECK(ok=1));
INSERT INTO current_review_downgrade_guard SELECT NOT (EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions)
 OR EXISTS(SELECT 1 FROM cleardev_product_goals)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs));
DROP TABLE current_review_downgrade_guard;
DROP TRIGGER cleardev_planner_runtime_request_insert;
CREATE TRIGGER cleardev_planner_runtime_request_insert
BEFORE INSERT ON cleardev_planner_runtime_requests
WHEN EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests old WHERE old.event_id=NEW.event_id
             OR old.agent_step_id=NEW.agent_step_id OR (old.execution_run_id=NEW.execution_run_id AND old.ordinal=NEW.ordinal))
 OR NEW.ordinal<>1+(SELECT count(*) FROM cleardev_planner_runtime_requests WHERE execution_run_id=NEW.execution_run_id)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_requests prior
           LEFT JOIN cleardev_planner_runtime_current_decisions decision ON decision.event_id=prior.event_id
           WHERE prior.execution_run_id=NEW.execution_run_id AND decision.event_id IS NULL)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_current_decisions WHERE event_id=NEW.event_id)
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
 OR (NEW.ordinal=3 AND NOT EXISTS(
 SELECT 1 FROM cleardev_extra_coordination_grants grant
 JOIN cleardev_extra_coordination_requests offer ON offer.event_id=grant.event_id
 JOIN cleardev_human_decision_requests human ON human.id=grant.decision_request_id
 JOIN sessions session ON session.id=NEW.ao_session_id
 WHERE grant.event_id=NEW.event_id AND grant.execution_run_id=NEW.execution_run_id
 AND human.status='RESOLVED' AND human.decision='APPROVE' AND human.decision_kind='AUTHORIZE_EXTRA_PLANNER_COORDINATION'
 AND human.binding_json=offer.binding_json AND NEW.context_json=offer.context_json AND NEW.prompt=offer.prompt
 AND NEW.context_sha256=json_extract(offer.binding_json,'$.contextSha256')
 AND NEW.planner_role_binding_id=json_extract(offer.binding_json,'$.plannerRoleBindingId')
 AND NEW.ao_session_id=json_extract(offer.binding_json,'$.aoSessionId')
 AND session.provider_conversation_id=json_extract(offer.binding_json,'$.providerConversationId')
 AND session.creation_request_fingerprint=json_extract(offer.binding_json,'$.creationFingerprint')
 AND session.workspace_path=json_extract(offer.binding_json,'$.workspacePath')
 AND session.harness=json_extract(offer.binding_json,'$.harness') AND session.model=json_extract(offer.binding_json,'$.model')
 AND session.permission_mode='auto' AND session.session_mode='chat' AND session.is_terminated=0
 ))
BEGIN SELECT RAISE(ABORT,'runtime request requires quiescent current facts, the original Planner and remaining durable quota'); END;
-- +goose StatementEnd
