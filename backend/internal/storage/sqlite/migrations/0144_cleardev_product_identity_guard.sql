-- TEXT PRIMARY KEY columns accept NULL identities, and these rowid tables
-- accept explicit rowid writes; INSERT OR REPLACE resolves conflicts on the
-- implicit rowid as well, and id<>OLD.id comparisons go NULL when an identity
-- is NULL. Reject NULL identities and rowid values that collide with an
-- occupied row (the collision case of replacement), refuse rowid changes on
-- updates, and compare identities null-safely so replacement can no longer
-- delete or steal protected rows through them.
-- +goose Up
-- +goose StatementBegin
CREATE TRIGGER cleardev_product_goal_identity_guard BEFORE INSERT ON cleardev_product_goals
WHEN NEW.id IS NULL OR EXISTS(SELECT 1 FROM cleardev_product_goals WHERE rowid=NEW.rowid)
BEGIN SELECT RAISE(ABORT,'product goal identity is null or occupied'); END;

CREATE TRIGGER cleardev_product_discussion_identity_guard BEFORE INSERT ON cleardev_product_discussions
WHEN NEW.id IS NULL OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE rowid=NEW.rowid)
BEGIN SELECT RAISE(ABORT,'product discussion identity is null or occupied'); END;

CREATE TRIGGER cleardev_product_stage_identity_guard BEFORE INSERT ON cleardev_product_stages
WHEN NEW.id IS NULL OR EXISTS(SELECT 1 FROM cleardev_product_stages WHERE rowid=NEW.rowid)
BEGIN SELECT RAISE(ABORT,'product stage identity is null or occupied'); END;

CREATE TRIGGER cleardev_product_discussion_rowid_guard BEFORE UPDATE ON cleardev_product_discussions
WHEN NEW.rowid IS NOT OLD.rowid
BEGIN SELECT RAISE(ABORT,'product discussion rowid is immutable'); END;

CREATE TRIGGER cleardev_product_stage_rowid_guard BEFORE UPDATE ON cleardev_product_stages
WHEN NEW.rowid IS NOT OLD.rowid
BEGIN SELECT RAISE(ABORT,'product stage rowid is immutable'); END;

CREATE TRIGGER cleardev_product_stage_rebind_nullsafe_guard BEFORE UPDATE ON cleardev_product_stages
WHEN NEW.development_requirement_id IS NOT NULL AND EXISTS (
 SELECT 1 FROM cleardev_product_stages WHERE id IS NOT OLD.id AND development_requirement_id=NEW.development_requirement_id)
BEGIN SELECT RAISE(ABORT,'stage requirement link is already bound'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER cleardev_product_goal_identity_guard;
DROP TRIGGER cleardev_product_discussion_identity_guard;
DROP TRIGGER cleardev_product_stage_identity_guard;
DROP TRIGGER cleardev_product_discussion_rowid_guard;
DROP TRIGGER cleardev_product_stage_rowid_guard;
DROP TRIGGER cleardev_product_stage_rebind_nullsafe_guard;
-- +goose StatementEnd
