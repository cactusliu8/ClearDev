-- +goose Up
-- +goose StatementBegin
CREATE TABLE cleardev_product_source_preparations (
 id TEXT PRIMARY KEY,
 product_id TEXT NOT NULL REFERENCES cleardev_product_goals(id),
 previous_id TEXT NOT NULL REFERENCES cleardev_product_discussions(id),
 input_json TEXT NOT NULL CHECK(json_valid(input_json)),
 selection_json TEXT NOT NULL CHECK(json_valid(selection_json)),
 branch TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('PENDING','READY','FAILED','APPLIED')),
 failure TEXT NOT NULL DEFAULT '',
 prepared_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(prepared_json)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX cleardev_product_source_active ON cleardev_product_source_preparations(product_id)
 WHERE status IN ('PENDING','READY');
CREATE TRIGGER cleardev_product_source_immutable BEFORE UPDATE ON cleardev_product_source_preparations
 WHEN NEW.rowid<>OLD.rowid OR NEW.id<>OLD.id OR NEW.product_id<>OLD.product_id OR NEW.previous_id<>OLD.previous_id
 OR NEW.input_json<>OLD.input_json OR NEW.selection_json<>OLD.selection_json OR NEW.branch<>OLD.branch OR NEW.created_at<>OLD.created_at
BEGIN SELECT RAISE(ABORT,'source preparation request is immutable'); END;
CREATE TRIGGER cleardev_product_source_keep_history BEFORE DELETE ON cleardev_product_source_preparations
BEGIN SELECT RAISE(ABORT,'source preparation history cannot be deleted'); END;
CREATE TRIGGER cleardev_product_source_no_replace BEFORE INSERT ON cleardev_product_source_preparations
 WHEN EXISTS(SELECT 1 FROM cleardev_product_source_preparations WHERE id=NEW.id)
BEGIN SELECT RAISE(ABORT,'source preparation cannot be replaced'); END;
CREATE TRIGGER cleardev_product_source_transition BEFORE UPDATE ON cleardev_product_source_preparations
 WHEN NOT ((OLD.status='PENDING' AND NEW.status IN ('READY','FAILED'))
 OR (OLD.status='READY' AND NEW.status IN ('APPLIED','FAILED'))
 OR (OLD.status='FAILED' AND NEW.status='PENDING'))
 OR substr(NEW.failure,1,length(OLD.failure))<>OLD.failure
 OR (OLD.prepared_json<>'null' AND OLD.prepared_json<>NEW.prepared_json)
BEGIN SELECT RAISE(ABORT,'invalid source preparation transition'); END;
CREATE TRIGGER cleardev_product_source_cdc_insert AFTER INSERT ON cleardev_product_source_preparations BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id),NEW.updated_at
 FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
CREATE TRIGGER cleardev_product_source_cdc_update AFTER UPDATE ON cleardev_product_source_preparations BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT ao_project_id,NULL,'cleardev_project_updated',json_object('productId',NEW.product_id),NEW.updated_at
 FROM cleardev_development_projects WHERE id=NEW.product_id;
END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER cleardev_product_source_transition;
DROP TRIGGER cleardev_product_source_no_replace;
DROP TRIGGER cleardev_product_source_keep_history;
DROP TRIGGER cleardev_product_source_cdc_update;
DROP TRIGGER cleardev_product_source_cdc_insert;
DROP TRIGGER cleardev_product_source_immutable;
DROP INDEX cleardev_product_source_active;
DROP TABLE cleardev_product_source_preparations;
