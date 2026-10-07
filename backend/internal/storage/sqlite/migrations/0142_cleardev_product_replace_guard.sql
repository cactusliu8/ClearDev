-- INSERT OR REPLACE deletes conflicting rows without firing delete triggers
-- (recursive triggers are off by default), which bypassed the product history
-- guards from 0141. The replacement INSERT must therefore be rejected while
-- the occupied rows are still visible: any existing identity or unique key
-- blocks the insert before the implicit delete can run.
-- +goose Up
-- +goose StatementBegin
CREATE TRIGGER cleardev_product_goal_insert_replace_guard BEFORE INSERT ON cleardev_product_goals
WHEN EXISTS(SELECT 1 FROM cleardev_product_goals WHERE id=NEW.id)
 OR EXISTS(SELECT 1 FROM cleardev_product_goals WHERE request_id=NEW.request_id)
BEGIN SELECT RAISE(ABORT,'product goal already exists'); END;

CREATE TRIGGER cleardev_product_discussion_insert_replace_guard BEFORE INSERT ON cleardev_product_discussions
WHEN EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE id=NEW.id)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE product_id=NEW.product_id AND ordinal=NEW.ordinal)
 OR (NEW.agent_step_id IS NOT NULL AND EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE agent_step_id=NEW.agent_step_id))
BEGIN SELECT RAISE(ABORT,'product discussion already exists'); END;

CREATE TRIGGER cleardev_product_stage_insert_replace_guard BEFORE INSERT ON cleardev_product_stages
WHEN EXISTS(SELECT 1 FROM cleardev_product_stages WHERE id=NEW.id)
 OR EXISTS(SELECT 1 FROM cleardev_product_stages WHERE discussion_id=NEW.discussion_id AND ordinal=NEW.ordinal)
 OR (NEW.development_requirement_id IS NOT NULL AND EXISTS(SELECT 1 FROM cleardev_product_stages WHERE development_requirement_id=NEW.development_requirement_id))
BEGIN SELECT RAISE(ABORT,'product stage already exists'); END;

-- UPDATE OR REPLACE drops a different conflicting row while applying a legal
-- bind that steals an occupied requirement link; refuse the occupied value
-- before that implicit delete can run.
CREATE TRIGGER cleardev_product_stage_rebind_replace_guard BEFORE UPDATE ON cleardev_product_stages
WHEN NEW.development_requirement_id IS NOT NULL AND EXISTS (
 SELECT 1 FROM cleardev_product_stages WHERE id<>OLD.id AND development_requirement_id=NEW.development_requirement_id)
BEGIN SELECT RAISE(ABORT,'stage requirement link is already bound'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER cleardev_product_goal_insert_replace_guard;
DROP TRIGGER cleardev_product_discussion_insert_replace_guard;
DROP TRIGGER cleardev_product_stage_insert_replace_guard;
DROP TRIGGER cleardev_product_stage_rebind_replace_guard;
-- +goose StatementEnd
