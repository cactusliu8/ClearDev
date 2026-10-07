-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_human_decision_reopens (
 request_id TEXT PRIMARY KEY CHECK(length(request_id) BETWEEN 1 AND 200),
 decision_request_id TEXT NOT NULL REFERENCES cleardev_human_decision_requests(id),
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64),
 previous_dispatch_id TEXT NOT NULL UNIQUE REFERENCES cleardev_human_decision_dispatches(id),
 desktop_run_id TEXT NOT NULL CHECK(length(desktop_run_id)>0),
 next_dispatch_id TEXT NOT NULL UNIQUE CHECK(length(next_dispatch_id)>0),
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TRIGGER cleardev_human_decision_reopens_immutable BEFORE UPDATE ON cleardev_human_decision_reopens
BEGIN SELECT RAISE(ABORT,'Decision reopen requests are immutable'); END;
CREATE TRIGGER cleardev_human_decision_reopens_keep BEFORE DELETE ON cleardev_human_decision_reopens
BEGIN SELECT RAISE(ABORT,'Decision reopen requests must be retained'); END;
CREATE TRIGGER cleardev_human_decision_reopens_bound BEFORE INSERT ON cleardev_human_decision_reopens
WHEN EXISTS(SELECT 1 FROM cleardev_human_decision_reopens WHERE request_id=NEW.request_id OR previous_dispatch_id=NEW.previous_dispatch_id OR next_dispatch_id=NEW.next_dispatch_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_human_decision_requests req JOIN cleardev_human_decision_dispatches old ON old.request_id=req.id
 WHERE req.id=NEW.decision_request_id AND req.status='PENDING' AND req.content_sha256=NEW.content_sha256 AND old.id=NEW.previous_dispatch_id
 AND old.consumed_at IS NOT NULL AND old.outcome IN ('EXPIRED','DISCONNECTED','LATER')
 AND NOT EXISTS(SELECT 1 FROM cleardev_human_decision_dispatches later WHERE later.request_id=req.id AND later.rowid>old.rowid))
BEGIN SELECT RAISE(ABORT,'Decision reopen request is not current'); END;
CREATE TRIGGER cleardev_human_decision_reopens_cdc AFTER INSERT ON cleardev_human_decision_reopens BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',project.id,'decisionReopenRequestId',NEW.request_id),NEW.created_at
 FROM cleardev_development_projects project JOIN cleardev_human_decision_requests req ON req.development_project_id=project.id WHERE req.id=NEW.decision_request_id;
END;
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_human_reopens_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_human_reopens_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_human_decision_reopens)
 OR EXISTS(SELECT 1 FROM cleardev_planner_answers)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_checks)
 OR EXISTS(SELECT 1 FROM cleardev_planning_step_recoveries)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config,'{}'),'$.cleardev')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_human_reopens_down_guard;
DROP TABLE cleardev_human_decision_reopens;
-- +goose StatementEnd
