-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_builder_handoff_feedback (
 dispatch_id TEXT PRIMARY KEY REFERENCES cleardev_complex_execution_task_attempts(id),
 summary TEXT NOT NULL CHECK(length(summary) BETWEEN 1 AND 16000),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TRIGGER cleardev_builder_handoff_feedback_insert BEFORE INSERT ON cleardev_builder_handoff_feedback
WHEN NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts a
 JOIN cleardev_complex_execution_agent_steps step ON step.id=a.agent_step_id
 WHERE a.id=NEW.dispatch_id AND a.status='RUNNING' AND step.send_status='SETTLED'
 AND NOT EXISTS(SELECT 1 FROM cleardev_candidate_commits c WHERE c.complex_execution_task_attempt_id=a.id))
BEGIN SELECT RAISE(ABORT,'handoff feedback requires a settled unpublished Builder result'); END;
CREATE TRIGGER cleardev_builder_handoff_feedback_update BEFORE UPDATE ON cleardev_builder_handoff_feedback
BEGIN SELECT RAISE(ABORT,'handoff feedback is immutable'); END;
CREATE TRIGGER cleardev_builder_handoff_feedback_delete BEFORE DELETE ON cleardev_builder_handoff_feedback
BEGIN SELECT RAISE(ABORT,'handoff feedback is immutable'); END;
CREATE TRIGGER cleardev_builder_handoff_feedback_cdc AFTER INSERT ON cleardev_builder_handoff_feedback BEGIN
 INSERT INTO change_log(project_id,event_type,payload,created_at)
 SELECT project.ao_project_id,'cleardev_project_updated',json_object('developmentProjectId',project.id,'dispatchId',NEW.dispatch_id),NEW.created_at
 FROM cleardev_complex_execution_task_attempts attempt
 JOIN cleardev_complex_execution_runs run ON run.id=attempt.execution_run_id
 JOIN cleardev_development_projects project ON project.id=run.development_project_id
 WHERE attempt.id=NEW.dispatch_id;
END;
-- +goose StatementEnd
-- +goose Down
CREATE TEMP TABLE cleardev_handoff_down_guard (ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_handoff_down_guard SELECT NOT (
 EXISTS(SELECT 1 FROM cleardev_builder_handoff_feedback)
 OR EXISTS(SELECT 1 FROM cleardev_product_goals)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
);
DROP TABLE cleardev_handoff_down_guard;
DROP TABLE cleardev_builder_handoff_feedback;
