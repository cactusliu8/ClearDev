-- +goose Up
-- +goose StatementBegin
CREATE TRIGGER cleardev_planner_repair_task_binding BEFORE INSERT ON cleardev_planner_runtime_task_amendments
WHEN NOT EXISTS (
 SELECT 1 FROM cleardev_planner_runtime_current_decisions decision
 JOIN json_each(decision.result_json,'$.amendments') proposal
 JOIN cleardev_planner_runtime_effective_tasks task ON task.id=NEW.task_mapping_id
 WHERE decision.event_id=NEW.event_id AND decision.execution_run_id=NEW.execution_run_id
 AND json_extract(proposal.value,'$.taskKey')=task.plan_task_key
 AND json_extract(NEW.task_packet_json,'$.runtimeRevision.repairTask') IS json_extract(proposal.value,'$.repairTask')
 AND (json_type(proposal.value,'$.repairTask') IS NULL OR (
  json_extract(NEW.task_packet_json,'$.schemaVersion')=4
  AND json_type(proposal.value,'$.repairTask')='object'
  AND json_type(proposal.value,'$.repairTask.title')='text'
  AND length(trim(json_extract(proposal.value,'$.repairTask.title'))) BETWEEN 1 AND 240
  AND json_type(proposal.value,'$.repairTask.instructions')='text'
  AND length(trim(json_extract(proposal.value,'$.repairTask.instructions'))) BETWEEN 1 AND 4000
  AND json_array_length(proposal.value,'$.additionalReviewCriteria') BETWEEN 1 AND 6)))
BEGIN SELECT RAISE(ABORT,'repair task must match the exact Planner proposal'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_repair_downgrade_guard (ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_repair_downgrade_guard SELECT NOT (EXISTS(SELECT 1 FROM cleardev_engineering_revision_decisions)
 OR EXISTS(SELECT 1 FROM cleardev_product_goals)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs));
DROP TABLE cleardev_repair_downgrade_guard;
DROP TRIGGER cleardev_planner_repair_task_binding;
-- +goose StatementEnd
