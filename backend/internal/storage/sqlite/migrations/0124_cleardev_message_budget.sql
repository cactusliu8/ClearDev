-- +goose Up
CREATE TABLE cleardev_message_budget_versions (
 development_project_id TEXT PRIMARY KEY REFERENCES cleardev_development_projects(id),
 version TEXT NOT NULL CHECK(version IN ('LEGACY_UNMEASURED','MESSAGE_BUDGET_V1')),
 created_at TIMESTAMP NOT NULL
);
INSERT INTO cleardev_message_budget_versions SELECT id, 'LEGACY_UNMEASURED', created_at FROM cleardev_development_projects;
-- +goose StatementBegin
CREATE TRIGGER cleardev_message_budget_initialize AFTER INSERT ON cleardev_development_projects BEGIN
 INSERT INTO cleardev_message_budget_versions VALUES(NEW.id,'MESSAGE_BUDGET_V1',NEW.created_at);
END;
-- +goose StatementEnd
CREATE TABLE cleardev_agent_message_reservations (
 client_message_id TEXT PRIMARY KEY,
 development_project_id TEXT NOT NULL REFERENCES cleardev_message_budget_versions(development_project_id),
 budget_version TEXT NOT NULL CHECK(budget_version='MESSAGE_BUDGET_V1'),
 logical_step_id TEXT NOT NULL,
 attempt_id TEXT NOT NULL REFERENCES cleardev_agent_step_attempts(id),
 source TEXT NOT NULL CHECK(source IN ('ORIGINAL','RECOVERY_ORIGINAL','PARSE_CORRECTION')),
 ao_session_id TEXT NOT NULL,
 prompt_sha256 TEXT NOT NULL CHECK(length(prompt_sha256)=64),
 budget_id TEXT REFERENCES cleardev_complex_exception_budgets(id),
 reserved_at TIMESTAMP NOT NULL,
 UNIQUE(logical_step_id,source)
);
CREATE INDEX idx_cleardev_message_budget_role ON cleardev_agent_message_reservations(budget_id);
CREATE INDEX idx_cleardev_message_budget_requirement ON cleardev_agent_message_reservations(development_project_id);
CREATE TABLE cleardev_agent_message_confirmations (
 client_message_id TEXT PRIMARY KEY REFERENCES cleardev_agent_message_reservations(client_message_id),
 turn_id TEXT NOT NULL CHECK(length(trim(turn_id))>0),
 confirmed_at TIMESTAMP NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER cleardev_message_budget_versions_update_forbidden BEFORE UPDATE ON cleardev_message_budget_versions BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_message_budget_versions_delete_forbidden BEFORE DELETE ON cleardev_message_budget_versions BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_message_reservations_update_forbidden BEFORE UPDATE ON cleardev_agent_message_reservations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_message_reservations_delete_forbidden BEFORE DELETE ON cleardev_agent_message_reservations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_message_confirmations_update_forbidden BEFORE UPDATE ON cleardev_agent_message_confirmations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_message_confirmations_delete_forbidden BEFORE DELETE ON cleardev_agent_message_confirmations BEGIN
 SELECT RAISE(ABORT,'ClearDev message budget facts are append-only');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_message_budget_versions_cdc_insert AFTER INSERT ON cleardev_message_budget_versions BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id),NEW.created_at
 FROM cleardev_development_projects AS project  WHERE project.id=NEW.development_project_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_message_reservations_cdc_insert AFTER INSERT ON cleardev_agent_message_reservations BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',NEW.development_project_id),NEW.reserved_at
 FROM cleardev_development_projects AS project  WHERE project.id=NEW.development_project_id;
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER cleardev_agent_message_confirmations_cdc_insert AFTER INSERT ON cleardev_agent_message_confirmations BEGIN
 INSERT INTO change_log(project_id,session_id,event_type,payload,created_at)
 SELECT project.ao_project_id,NULL,'cleardev_project_updated',json_object('developmentProjectId',reservation.development_project_id),NEW.confirmed_at
 FROM cleardev_development_projects AS project JOIN cleardev_agent_message_reservations AS reservation ON reservation.client_message_id=NEW.client_message_id WHERE project.id=reservation.development_project_id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER cleardev_message_budget_initialize;
DROP TABLE cleardev_agent_message_confirmations;
DROP TABLE cleardev_agent_message_reservations;
DROP TABLE cleardev_message_budget_versions;
