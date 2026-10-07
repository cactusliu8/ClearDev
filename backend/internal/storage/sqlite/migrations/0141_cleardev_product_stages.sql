-- Product discovery and stage definitions are not another execution state
-- machine. Linked requirements retain all existing approval/completion gates.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_product_goals (
    id TEXT PRIMARY KEY REFERENCES cleardev_development_projects(id),
    request_id TEXT NOT NULL UNIQUE CHECK(length(trim(request_id))>0),
    policy_version INTEGER NOT NULL DEFAULT 1 CHECK(policy_version=1),
    created_at TIMESTAMP NOT NULL
);

CREATE TABLE cleardev_product_discussions (
    id TEXT PRIMARY KEY CHECK(length(trim(id))>0),
    product_id TEXT NOT NULL REFERENCES cleardev_product_goals(id),
    ordinal INTEGER NOT NULL CHECK(ordinal>=0 AND ordinal<12),
    user_message TEXT NOT NULL CHECK(length(trim(user_message))>0 AND length(user_message)<=16000),
    agent_step_id TEXT UNIQUE REFERENCES cleardev_complex_agent_steps(id),
    result_json TEXT,
    result_sha256 TEXT,
    failure_reason TEXT,
    created_at TIMESTAMP NOT NULL,
    settled_at TIMESTAMP,
    UNIQUE(product_id,ordinal),
    CHECK((settled_at IS NULL AND agent_step_id IS NULL AND result_json IS NULL AND result_sha256 IS NULL AND failure_reason IS NULL)
       OR (settled_at IS NOT NULL AND agent_step_id IS NOT NULL AND result_json IS NOT NULL AND json_valid(result_json)
           AND result_sha256 IS NOT NULL AND length(result_sha256)=64 AND failure_reason IS NULL)
       OR (settled_at IS NOT NULL AND agent_step_id IS NULL AND result_json IS NULL AND result_sha256 IS NULL
           AND failure_reason IS NOT NULL AND failure_reason='PRODUCT_STEWARD_WORKSPACE_CHANGED'))
);
CREATE UNIQUE INDEX cleardev_product_one_pending_discussion ON cleardev_product_discussions(product_id) WHERE settled_at IS NULL;

CREATE TABLE cleardev_product_stages (
    id TEXT PRIMARY KEY,
    product_id TEXT NOT NULL REFERENCES cleardev_product_goals(id),
    discussion_id TEXT NOT NULL REFERENCES cleardev_product_discussions(id),
    ordinal INTEGER NOT NULL CHECK(ordinal>=0 AND ordinal<8),
    definition_json TEXT NOT NULL CHECK(json_valid(definition_json)),
    definition_sha256 TEXT NOT NULL CHECK(length(definition_sha256)=64),
    development_requirement_id TEXT UNIQUE REFERENCES cleardev_development_projects(id),
    base_commit_sha TEXT,
    created_at TIMESTAMP NOT NULL,
    CHECK((development_requirement_id IS NULL AND base_commit_sha IS NULL) OR
          (development_requirement_id IS NOT NULL AND base_commit_sha IS NOT NULL AND length(base_commit_sha)=40 AND base_commit_sha NOT GLOB '*[^0-9a-f]*')),
    UNIQUE(discussion_id,ordinal)
);

CREATE TRIGGER cleardev_product_goal_immutable BEFORE UPDATE ON cleardev_product_goals
BEGIN SELECT RAISE(ABORT,'product identity is immutable'); END;
CREATE TRIGGER cleardev_product_goal_keep_history BEFORE DELETE ON cleardev_product_goals
BEGIN SELECT RAISE(ABORT,'product history cannot be deleted'); END;
CREATE TRIGGER cleardev_product_not_executable BEFORE INSERT ON cleardev_contract_versions
WHEN EXISTS(SELECT 1 FROM cleardev_product_goals WHERE id=NEW.development_project_id)
BEGIN SELECT RAISE(ABORT,'product goal is not an executable stage'); END;

CREATE TRIGGER cleardev_product_discussion_insert_guard BEFORE INSERT ON cleardev_product_discussions
WHEN NEW.settled_at IS NOT NULL OR NEW.ordinal<>(SELECT count(*) FROM cleardev_product_discussions WHERE product_id=NEW.product_id)
 OR EXISTS(SELECT 1 FROM cleardev_product_stages WHERE product_id=NEW.product_id AND development_requirement_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM cleardev_development_projects WHERE id=NEW.product_id AND cancelled_at IS NOT NULL)
BEGIN SELECT RAISE(ABORT,'discussion is stale, frozen or not pending'); END;
CREATE TRIGGER cleardev_product_discussion_settle_guard BEFORE UPDATE ON cleardev_product_discussions
WHEN OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.ordinal IS NOT NEW.ordinal
 OR OLD.user_message IS NOT NEW.user_message OR OLD.created_at IS NOT NEW.created_at OR OLD.settled_at IS NOT NULL
 OR NEW.settled_at IS NULL
 OR (NEW.failure_reason IS NULL AND NOT EXISTS (
   SELECT 1 FROM cleardev_complex_agent_steps AS step
   JOIN cleardev_complex_role_bindings AS binding ON binding.id=step.role_binding_id
   WHERE step.id=NEW.agent_step_id AND step.request_id=NEW.id
     AND binding.development_project_id=NEW.product_id AND binding.role='STEWARD'
     AND step.step_kind='REQUIREMENT_COMPILATION' AND step.send_status='SETTLED'
     AND step.final_message_text=NEW.result_json
     AND json_extract(NEW.result_json,'$.kind')='PRODUCT_DISCOVERY'
 ))
BEGIN SELECT RAISE(ABORT,'product response needs its exact settled Steward step'); END;
CREATE TRIGGER cleardev_product_discussion_keep_history BEFORE DELETE ON cleardev_product_discussions
BEGIN SELECT RAISE(ABORT,'product discussion history cannot be deleted'); END;

CREATE TRIGGER cleardev_product_stage_insert_guard BEFORE INSERT ON cleardev_product_stages
WHEN NEW.development_requirement_id IS NOT NULL OR NOT EXISTS (
 SELECT 1 FROM cleardev_product_discussions AS d
 WHERE d.id=NEW.discussion_id AND d.product_id=NEW.product_id AND d.settled_at IS NOT NULL
   AND json_extract(d.result_json,'$.outcome')='READY'
   AND NEW.ordinal<json_array_length(d.result_json,'$.stages')
   AND json_extract(NEW.definition_json,'$.key')=json_extract(d.result_json,'$.stages['||NEW.ordinal||'].key')
)
BEGIN SELECT RAISE(ABORT,'stage has no ready product proposal'); END;
CREATE TRIGGER cleardev_product_stage_bind_guard BEFORE UPDATE ON cleardev_product_stages
WHEN OLD.id IS NOT NEW.id OR OLD.product_id IS NOT NEW.product_id OR OLD.discussion_id IS NOT NEW.discussion_id
 OR OLD.ordinal IS NOT NEW.ordinal OR OLD.definition_json IS NOT NEW.definition_json
 OR OLD.definition_sha256 IS NOT NEW.definition_sha256 OR OLD.created_at IS NOT NEW.created_at
 OR OLD.development_requirement_id IS NOT NULL OR NEW.development_requirement_id IS NULL OR NEW.base_commit_sha IS NULL
 OR NEW.development_requirement_id=NEW.product_id
 OR NEW.discussion_id IS NOT (SELECT id FROM cleardev_product_discussions WHERE product_id=NEW.product_id ORDER BY ordinal DESC LIMIT 1)
 OR json_extract(NEW.definition_json,'$.feasibility')<>'SUPPORTED'
 OR NOT EXISTS (
   SELECT 1 FROM cleardev_development_projects AS child
   JOIN cleardev_development_projects AS parent ON child.ao_project_id=parent.ao_project_id
   WHERE child.id=NEW.development_requirement_id AND parent.id=NEW.product_id
     AND child.cancelled_at IS NULL AND parent.cancelled_at IS NULL
 )
BEGIN SELECT RAISE(ABORT,'stage link is stale, unsupported, changed or cross-project'); END;
CREATE TRIGGER cleardev_product_stage_keep_history BEFORE DELETE ON cleardev_product_stages
BEGIN SELECT RAISE(ABORT,'product stage history cannot be deleted'); END;

-- This first product-stage policy does not expose runtime roadmap revisions.
-- Legacy standalone requirements retain their existing direction-change path.
CREATE TRIGGER cleardev_product_stage_no_direction BEFORE INSERT ON cleardev_direction_intents
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages WHERE development_requirement_id=NEW.development_project_id)
BEGIN SELECT RAISE(ABORT,'runtime product stage revision is not supported'); END;

-- Stage outcomes survive the conversion into an executable specification.
-- Every original criterion must be present and owned by a MUST requirement.
CREATE TRIGGER cleardev_product_stage_contract_guard BEFORE INSERT ON cleardev_contract_versions
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages WHERE development_requirement_id=NEW.development_project_id)
 AND (json_valid(NEW.contract_text)<>1 OR NEW.version<>1
 OR EXISTS (
   SELECT 1 FROM cleardev_product_stages AS stage, json_each(stage.definition_json,'$.acceptanceCriteria') AS criterion
   WHERE stage.development_requirement_id=NEW.development_project_id
     AND NOT EXISTS (
       SELECT 1 FROM json_each(NEW.contract_text,'$.acceptanceScenarios') AS scenario
       WHERE json_extract(scenario.value,'$.text')=criterion.value
         AND EXISTS (
           SELECT 1 FROM json_each(NEW.contract_text,'$.requirements') AS requirement,
                         json_each(requirement.value,'$.acceptanceIds') AS acceptance
           WHERE json_extract(requirement.value,'$.priority')='MUST'
             AND acceptance.value=json_extract(scenario.value,'$.id')
         )
     )
 ) OR EXISTS (
   SELECT 1 FROM cleardev_product_stages AS stage, json_each(stage.definition_json,'$.nonGoals') AS non_goal
   WHERE stage.development_requirement_id=NEW.development_project_id
     AND NOT EXISTS(SELECT 1 FROM json_each(NEW.contract_text,'$.nonGoals') AS actual WHERE actual.value=non_goal.value)
 ))
BEGIN SELECT RAISE(ABORT,'compiled stage omitted a frozen functional outcome or non-goal'); END;

CREATE TRIGGER cleardev_product_goal_cdc AFTER INSERT ON cleardev_product_goals BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.id),NEW.created_at FROM cleardev_development_projects WHERE id=NEW.id;
END;
CREATE TRIGGER cleardev_product_discussion_cdc_insert AFTER INSERT ON cleardev_product_discussions BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id,'discussionId',NEW.id),NEW.created_at FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
CREATE TRIGGER cleardev_product_discussion_cdc_update AFTER UPDATE ON cleardev_product_discussions BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id,'discussionId',NEW.id),NEW.settled_at FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
CREATE TRIGGER cleardev_product_stage_cdc_insert AFTER INSERT ON cleardev_product_stages BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id,'stageId',NEW.id),NEW.created_at FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
CREATE TRIGGER cleardev_product_stage_cdc_update AFTER UPDATE ON cleardev_product_stages BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id,'stageId',NEW.id),strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
-- +goose StatementEnd

-- +goose Down
-- Never erase a recorded product proposal, discussion or stage history. An
-- empty installation may downgrade; any recorded product safely refuses it.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_product_down_guard(ok INTEGER CHECK(ok=1));
INSERT INTO cleardev_product_down_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM cleardev_product_goals) THEN 0 ELSE 1 END;
DROP TABLE cleardev_product_down_guard;
DROP TRIGGER cleardev_product_not_executable;
DROP TRIGGER cleardev_product_stage_no_direction;
DROP TRIGGER cleardev_product_stage_contract_guard;
DROP TABLE cleardev_product_stages;
DROP TABLE cleardev_product_discussions;
DROP TABLE cleardev_product_goals;
-- +goose StatementEnd
