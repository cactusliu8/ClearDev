-- Project choices and proposed execution bases are planning facts, not runtime grants.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_product_discussion_contexts (
 discussion_id TEXT NOT NULL PRIMARY KEY REFERENCES cleardev_product_discussions(id),
 protocol_version INTEGER NOT NULL CHECK(protocol_version=2),
 selection_json TEXT CHECK(selection_json IS NULL OR json_valid(selection_json)),
 selection_sha256 TEXT,
 CHECK((selection_json IS NULL AND selection_sha256 IS NULL) OR
       (selection_json IS NOT NULL AND selection_sha256 IS NOT NULL AND length(selection_sha256)=64))
) WITHOUT ROWID;
CREATE TRIGGER cleardev_product_context_insert_guard BEFORE INSERT ON cleardev_product_discussion_contexts
WHEN EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts WHERE discussion_id=NEW.discussion_id)
 OR NOT EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE id=NEW.discussion_id AND settled_at IS NULL)
 OR (NEW.selection_json IS NOT NULL AND (
   json_type(NEW.selection_json,'$.baseCommitSha') IS NOT 'text'
   OR length(json_extract(NEW.selection_json,'$.baseCommitSha'))<>40
   OR json_extract(NEW.selection_json,'$.baseCommitSha') GLOB '*[^0-9a-f]*'
   OR json_type(NEW.selection_json,'$.aoProjectId') IS NOT 'text'
   OR json_type(NEW.selection_json,'$.repositoryPath') IS NOT 'text'
   OR json_type(NEW.selection_json,'$.reason') IS NOT 'text'
   OR NOT EXISTS (
     SELECT 1 FROM cleardev_product_discussions source
     JOIN cleardev_product_discussions current ON current.id=NEW.discussion_id AND current.product_id=source.product_id
     JOIN json_each(source.result_json,'$.options') option
     WHERE source.id=json_extract(NEW.selection_json,'$.sourceDiscussionId')
       AND source.ordinal<current.ordinal AND source.settled_at IS NOT NULL
       AND (json_type(NEW.selection_json,'$.choiceDiscussionId') IS NULL
            OR (json_type(NEW.selection_json,'$.choiceDiscussionId') IS 'text' AND EXISTS (
              SELECT 1 FROM cleardev_product_discussions choice
              WHERE choice.id=json_extract(NEW.selection_json,'$.choiceDiscussionId')
                AND choice.product_id=current.product_id AND choice.ordinal>source.ordinal AND choice.ordinal<=current.ordinal)))
       AND json_extract(option.value,'$.key')=json_extract(NEW.selection_json,'$.option.key')
       AND json_extract(option.value,'$.title') IS json_extract(NEW.selection_json,'$.option.title')
       AND json_extract(option.value,'$.origin') IS json_extract(NEW.selection_json,'$.option.origin')
       AND COALESCE(json_extract(option.value,'$.repositoryUrl'),'')=COALESCE(json_extract(NEW.selection_json,'$.option.repositoryUrl'),'')
       AND json_extract(option.value,'$.description') IS json_extract(NEW.selection_json,'$.option.description')
       AND json_extract(option.value,'$.tradeoffs') IS json_extract(NEW.selection_json,'$.option.tradeoffs')
   )
 ))
BEGIN SELECT RAISE(ABORT,'project context needs a pending discussion and an exact prior option'); END;
CREATE TRIGGER cleardev_product_context_immutable BEFORE UPDATE ON cleardev_product_discussion_contexts
BEGIN SELECT RAISE(ABORT,'project discussion context is immutable'); END;
CREATE TRIGGER cleardev_product_context_keep_history BEFORE DELETE ON cleardev_product_discussion_contexts
BEGIN SELECT RAISE(ABORT,'project choices cannot be deleted'); END;
CREATE TRIGGER cleardev_product_context_cdc AFTER INSERT ON cleardev_product_discussion_contexts BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT p.ao_project_id,NULL,'cleardev_project_updated',json_object('productId',d.product_id),d.created_at
 FROM cleardev_product_discussions d JOIN cleardev_development_projects p ON p.id=d.product_id WHERE d.id=NEW.discussion_id;
END;

DROP TRIGGER cleardev_product_discussion_insert_guard;
CREATE TRIGGER cleardev_product_discussion_insert_guard BEFORE INSERT ON cleardev_product_discussions
WHEN NEW.settled_at IS NOT NULL OR NEW.ordinal<>(SELECT count(*) FROM cleardev_product_discussions WHERE product_id=NEW.product_id)
 OR EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.product_id=NEW.product_id AND s.development_requirement_id IS NOT NULL
   AND (json_type(s.definition_json,'$.executionBasis') IS NOT 'object'
        OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs r WHERE r.development_project_id=s.development_requirement_id)))
 OR EXISTS(SELECT 1 FROM cleardev_development_projects WHERE id=NEW.product_id AND cancelled_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'discussion is stale, executing, legacy-frozen or not pending'); END;

DROP TRIGGER cleardev_product_stage_bind_guard;
CREATE TRIGGER cleardev_product_stage_bind_guard BEFORE UPDATE ON cleardev_product_stages
WHEN OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.discussion_id IS NOT NEW.discussion_id
 OR OLD.ordinal IS NOT NEW.ordinal OR OLD.definition_json IS NOT NEW.definition_json
 OR OLD.definition_sha256 IS NOT NEW.definition_sha256 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.development_requirement_id IS NOT NULL OR NEW.development_requirement_id IS NULL OR NEW.base_commit_sha IS NULL
 OR NEW.development_requirement_id=NEW.product_id
 OR NEW.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=NEW.product_id ORDER BY ordinal DESC LIMIT 1)
 OR (json_type(NEW.definition_json,'$.executionBasis') IS NOT 'object' AND json_extract(NEW.definition_json,'$.feasibility')<>'SUPPORTED')
 OR NOT EXISTS (
   SELECT 1 FROM cleardev_development_projects child, cleardev_development_projects parent
   WHERE child.id=NEW.development_requirement_id AND parent.id=NEW.product_id
     AND child.cancelled_at IS NULL AND parent.cancelled_at IS NULL
     AND (
       (json_type(NEW.definition_json,'$.executionBasis') IS NOT 'object' AND child.ao_project_id=parent.ao_project_id)
       OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts c JOIN cleardev_product_discussions d ON d.id=c.discussion_id
          WHERE c.discussion_id=NEW.discussion_id AND c.selection_json IS NOT NULL
            AND child.ao_project_id=json_extract(c.selection_json,'$.aoProjectId')
            AND NEW.base_commit_sha=json_extract(c.selection_json,'$.baseCommitSha')
            AND json_extract(d.result_json,'$.selectedOptionKey')=json_extract(c.selection_json,'$.option.key'))
     )
 )
BEGIN SELECT RAISE(ABORT,'stage link is stale, unselected, changed or cross-project'); END;

CREATE TRIGGER cleardev_project_basis_contract_guard BEFORE INSERT ON cleardev_contract_versions
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.development_requirement_id=NEW.development_project_id
 AND json_type(s.definition_json,'$.executionBasis')='object'
 AND (s.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=s.product_id ORDER BY ordinal DESC LIMIT 1)
      OR NOT EXISTS(SELECT 1 FROM json_each(NEW.contract_text,'$.constraints') c
                    WHERE c.value='项目执行依据（仅规划，不授权运行命令）：'||json_extract(s.definition_json,'$.executionBasis'))))
BEGIN SELECT RAISE(ABORT,'project specification lost its basis or has been superseded'); END;
CREATE TRIGGER cleardev_project_stale_confirmation_guard BEFORE UPDATE ON cleardev_contract_versions
WHEN NEW.state='APPROVED' AND EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.development_requirement_id=NEW.development_project_id
 AND json_type(s.definition_json,'$.executionBasis')='object'
 AND s.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=s.product_id ORDER BY ordinal DESC LIMIT 1))
BEGIN SELECT RAISE(ABORT,'superseded project specification cannot be confirmed'); END;
CREATE TRIGGER cleardev_project_plan_source_guard BEFORE INSERT ON cleardev_complex_engineering_plans
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.development_requirement_id=NEW.development_project_id
 AND json_type(s.definition_json,'$.executionBasis')='object'
 AND (s.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=s.product_id ORDER BY ordinal DESC LIMIT 1)
      OR json_type(NEW.plan_json,'$.schemaVersion') IS NOT 'integer'
      OR NOT ((json_extract(NEW.plan_json,'$.schemaVersion') IS 3 AND json_extract(NEW.plan_json,'$.kind') IS 'COMPLEX_ENGINEERING_PLAN')
           OR (json_extract(NEW.plan_json,'$.schemaVersion') IS 2 AND json_extract(NEW.plan_json,'$.kind') IS 'PRODUCT_CLARIFICATION_REQUIRED'))))
BEGIN SELECT RAISE(ABORT,'project plan is superseded or uses an executable protocol'); END;
CREATE TRIGGER cleardev_project_planning_no_tasks BEFORE INSERT ON cleardev_work_items
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.development_requirement_id=NEW.development_project_id AND json_type(s.definition_json,'$.executionBasis')='object')
BEGIN SELECT RAISE(ABORT,'generic project planning does not authorize development tasks'); END;
CREATE TRIGGER cleardev_project_planning_no_execution BEFORE INSERT ON cleardev_complex_execution_runs
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages s WHERE s.development_requirement_id=NEW.development_project_id AND json_type(s.definition_json,'$.executionBasis')='object')
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans p WHERE p.id=NEW.plan_id AND json_extract(p.plan_json,'$.schemaVersion')=3)
BEGIN SELECT RAISE(ABORT,'generic project planning does not authorize execution'); END;
-- +goose StatementEnd

-- +goose Down
-- Refuse at the current version before changing any schema, including the
-- historical execution/admission/runtime facts already protected by 0149.
-- A failed multi-migration downgrade must not partially move those runs to 0149.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_project_planning_down_guard (ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_project_planning_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events) THEN 0 ELSE 1 END;
DROP TABLE cleardev_project_planning_down_guard;
DROP TRIGGER cleardev_project_planning_no_execution;
DROP TRIGGER cleardev_project_planning_no_tasks;
DROP TRIGGER cleardev_project_plan_source_guard;
DROP TRIGGER cleardev_project_stale_confirmation_guard;
DROP TRIGGER cleardev_project_basis_contract_guard;
DROP TABLE cleardev_product_discussion_contexts;
DROP TRIGGER cleardev_product_discussion_insert_guard;
CREATE TRIGGER cleardev_product_discussion_insert_guard BEFORE INSERT ON cleardev_product_discussions
WHEN NEW.settled_at IS NOT NULL OR NEW.ordinal<>(SELECT count(*) FROM cleardev_product_discussions WHERE product_id=NEW.product_id)
 OR EXISTS(SELECT 1 FROM cleardev_product_stages WHERE product_id=NEW.product_id AND development_requirement_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM cleardev_development_projects WHERE id=NEW.product_id AND cancelled_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'discussion is stale, frozen or not pending'); END;
DROP TRIGGER cleardev_product_stage_bind_guard;
CREATE TRIGGER cleardev_product_stage_bind_guard BEFORE UPDATE ON cleardev_product_stages
WHEN OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.discussion_id IS NOT NEW.discussion_id
 OR OLD.ordinal IS NOT NEW.ordinal OR OLD.definition_json IS NOT NEW.definition_json
 OR OLD.definition_sha256 IS NOT NEW.definition_sha256 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.development_requirement_id IS NOT NULL OR NEW.development_requirement_id IS NULL OR NEW.base_commit_sha IS NULL
 OR NEW.development_requirement_id=NEW.product_id
 OR NEW.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=NEW.product_id ORDER BY ordinal DESC LIMIT 1)
 OR json_extract(NEW.definition_json,'$.feasibility')<>'SUPPORTED'
 OR NOT EXISTS (
   SELECT 1 FROM cleardev_development_projects child JOIN cleardev_development_projects parent ON child.ao_project_id=parent.ao_project_id
   WHERE child.id=NEW.development_requirement_id AND parent.id=NEW.product_id AND child.cancelled_at IS NULL AND parent.cancelled_at IS NULL
 )
BEGIN SELECT RAISE(ABORT,'stage link is stale, unsupported, changed or cross-project'); END;
-- +goose StatementEnd
