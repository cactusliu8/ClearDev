-- 0190: a human-authorized engine change may move a controlled project's tool.
--
-- 0176 froze the cleardev execution tool the moment a project gained any
-- development project, and its model/effort after controlled work started. The
-- deliberate mid-project engine change the user asked for is exactly one
-- narrow human decision: an approved AUTHORIZE_CONTROLLED_ENGINE_CHANGE names
-- the project and the exact target engine and model. This migration lets that
-- exact recorded authorization bypass the freeze for that one config write;
-- without a matching settled authorization every existing freeze keeps firing.
-- Every other clause is byte-identical to the previous shipped definition; no
-- recorded row or history changes.

-- +goose Up
-- +goose StatementBegin
DROP TRIGGER cleardev_project_tool_frozen;
CREATE TRIGGER cleardev_project_tool_frozen
BEFORE UPDATE OF config ON projects
WHEN EXISTS (SELECT 1 FROM cleardev_development_projects WHERE ao_project_id = OLD.id)
 AND NOT EXISTS (
    SELECT 1 FROM cleardev_human_decision_requests grant
    WHERE grant.decision_kind='AUTHORIZE_CONTROLLED_ENGINE_CHANGE'
      AND grant.status='RESOLVED' AND grant.decision='APPROVE'
      AND json_extract(grant.binding_json,'$.aoProjectId')=OLD.id
      AND json_extract(grant.binding_json,'$.targetHarness')=json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.agent')
      AND json_extract(grant.binding_json,'$.model')=json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.model')
 )
 AND (
    json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.agent') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.agent')
 OR (
    (EXISTS (SELECT 1 FROM cleardev_controlled_preflights WHERE ao_project_id = OLD.id AND outcome = 'PASSED')
     OR EXISTS (SELECT 1 FROM cleardev_product_stages s JOIN cleardev_development_projects r ON r.id = s.development_requirement_id WHERE r.ao_project_id = OLD.id))
    AND (
       json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.model') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.model')
    OR json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.effort') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.effort')
    )
 )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev execution choice is frozen');
END;
-- +goose StatementEnd
-- +goose Down
-- Existing execution history cannot be downgraded past its recorded
-- human grants; refuse before touching any definition.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_engine_change_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_engine_change_down_guard_valid BEFORE INSERT ON cleardev_engine_change_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT,'downgrade refuses authorized engine changes'); END;
INSERT INTO cleardev_engine_change_down_guard SELECT CASE WHEN EXISTS(
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
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests WHERE decision_kind='AUTHORIZE_CONTROLLED_ENGINE_CHANGE')
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_engine_change_down_guard;
DROP TRIGGER cleardev_project_tool_frozen;
CREATE TRIGGER cleardev_project_tool_frozen
BEFORE UPDATE OF config ON projects
WHEN EXISTS (SELECT 1 FROM cleardev_development_projects WHERE ao_project_id = OLD.id)
 AND (
    json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.agent') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.agent')
 OR (
    (EXISTS (SELECT 1 FROM cleardev_controlled_preflights WHERE ao_project_id = OLD.id AND outcome = 'PASSED')
     OR EXISTS (SELECT 1 FROM cleardev_product_stages s JOIN cleardev_development_projects r ON r.id = s.development_requirement_id WHERE r.ao_project_id = OLD.id))
    AND (
       json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.model') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.model')
    OR json_extract(COALESCE(OLD.config, '{}'), '$.cleardev.effort') IS NOT json_extract(COALESCE(NEW.config, '{}'), '$.cleardev.effort')
    )
 )
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev execution choice is frozen');
END;
-- +goose StatementEnd
