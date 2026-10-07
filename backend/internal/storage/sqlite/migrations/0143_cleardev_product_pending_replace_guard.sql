-- The partial unique index cleardev_product_one_pending_discussion is another
-- occupied key that INSERT OR REPLACE can silently delete through: inserting a
-- different id for a product that already has a pending round deletes that
-- protected round. Reject the replacement INSERT while the pending slot is
-- taken; the legitimate append path always settles the previous round first.
-- +goose Up
-- +goose StatementBegin
CREATE TRIGGER cleardev_product_pending_discussion_insert_replace_guard BEFORE INSERT ON cleardev_product_discussions
WHEN NEW.settled_at IS NULL AND EXISTS (
 SELECT 1 FROM cleardev_product_discussions WHERE product_id=NEW.product_id AND settled_at IS NULL)
BEGIN SELECT RAISE(ABORT,'pending product discussion already exists'); END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER cleardev_product_pending_discussion_insert_replace_guard;
-- +goose StatementEnd
