-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_planner_answers (
 request_id TEXT PRIMARY KEY CHECK(length(request_id) BETWEEN 1 AND 200),
 development_project_id TEXT NOT NULL REFERENCES cleardev_development_projects(id),
 plan_id TEXT NOT NULL UNIQUE REFERENCES cleardev_complex_engineering_plans(id),
 plan_sha256 TEXT NOT NULL CHECK(length(plan_sha256)=64),
 answers_json TEXT NOT NULL CHECK(json_valid(answers_json) AND json_type(answers_json)='array' AND json_array_length(answers_json) BETWEEN 1 AND 8),
 answers_sha256 TEXT NOT NULL CHECK(length(answers_sha256)=64),
 next_planning_request_id TEXT NOT NULL UNIQUE,
 created_at TIMESTAMP NOT NULL
) WITHOUT ROWID;
CREATE TRIGGER cleardev_planner_answers_immutable BEFORE UPDATE ON cleardev_planner_answers
BEGIN SELECT RAISE(ABORT,'Planner answers are immutable'); END;
CREATE TRIGGER cleardev_planner_answers_keep BEFORE DELETE ON cleardev_planner_answers
BEGIN SELECT RAISE(ABORT,'Planner answers must be retained'); END;
CREATE TRIGGER cleardev_planner_answers_bound BEFORE INSERT ON cleardev_planner_answers
WHEN EXISTS(SELECT 1 FROM cleardev_planner_answers WHERE request_id=NEW.request_id OR plan_id=NEW.plan_id OR next_planning_request_id=NEW.next_planning_request_id)
 OR NOT EXISTS (
 SELECT 1 FROM cleardev_complex_engineering_plans plan
 JOIN cleardev_development_projects project ON project.id=plan.development_project_id
 JOIN cleardev_contract_versions version ON version.id=plan.requirement_version_id
 JOIN cleardev_complex_agent_steps step ON step.id=plan.agent_step_id
 JOIN cleardev_complex_role_bindings binding ON binding.id=plan.planner_role_binding_id
 WHERE plan.id=NEW.plan_id AND plan.development_project_id=NEW.development_project_id AND plan.plan_sha256=NEW.plan_sha256
 AND project.cancelled_at IS NULL AND version.state='APPROVED' AND version.superseded_by_id IS NULL AND version.task_set_version=0
 AND json_extract(plan.plan_json,'$.kind')='PRODUCT_CLARIFICATION_REQUIRED'
 AND step.send_status='SETTLED' AND step.role_binding_id=binding.id AND step.request_id=plan.planning_request_id
 AND binding.status='BOUND' AND binding.role='ENGINEERING_PLANNER'
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_runs run WHERE run.development_project_id=project.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans later WHERE later.development_project_id=project.id AND later.version>plan.version)
 AND (SELECT count(*) FROM cleardev_planner_answers answer JOIN cleardev_complex_engineering_plans previous ON previous.id=answer.plan_id WHERE previous.requirement_version_id=plan.requirement_version_id)<2
 AND NEW.next_planning_request_id='cleardev-complex-plan-'||plan.requirement_version_id||'-'||(1+(SELECT count(*) FROM cleardev_complex_engineering_plans previous WHERE previous.development_project_id=project.id AND previous.requirement_version_id=plan.requirement_version_id))
 AND json_array_length(NEW.answers_json)=json_array_length(json_extract(plan.plan_json,'$.questions'))
 ) OR EXISTS (SELECT 1 FROM json_each(NEW.answers_json) WHERE type<>'text' OR length(trim(value))=0)
BEGIN SELECT RAISE(ABORT,'Planner answer is not bound to the current clarification'); END;
CREATE TRIGGER cleardev_planner_answers_cdc AFTER INSERT ON cleardev_planner_answers BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',id,'plannerAnswerRequestId',NEW.request_id),NEW.created_at
 FROM cleardev_development_projects WHERE id=NEW.development_project_id;
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_planner_answers_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_planner_answers_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_planner_answers)
 OR EXISTS(SELECT 1 FROM cleardev_builder_session_checks)
 OR EXISTS(SELECT 1 FROM cleardev_planning_step_recoveries)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config,'{}'),'$.cleardev')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_workflow_recoveries)
 THEN 0 ELSE 1 END;
DROP TABLE cleardev_planner_answers_down_guard;
DROP TABLE cleardev_planner_answers;
-- +goose StatementEnd
