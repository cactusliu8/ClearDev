-- +goose Up
-- New purposes retain all previous immutable binding data and uniqueness.
DROP INDEX idx_cleardev_benchmark_bindings_project;
ALTER TABLE cleardev_benchmark_bindings RENAME TO cleardev_benchmark_bindings_previous;
CREATE TABLE cleardev_benchmark_bindings (
    development_requirement_id TEXT PRIMARY KEY
        REFERENCES cleardev_development_projects(id) ON DELETE CASCADE,
    ao_project_id TEXT NOT NULL REFERENCES projects(id),
    project_root TEXT NOT NULL CHECK (length(trim(project_root)) > 0),
    manifest_path TEXT NOT NULL CHECK (length(trim(manifest_path)) > 0),
    manifest_sha256 TEXT NOT NULL UNIQUE CHECK (
        length(manifest_sha256) = 64 AND manifest_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    facility_version TEXT NOT NULL CHECK (facility_version IN ('s12b-v1', 's12b-v17')),
    purpose TEXT NOT NULL CHECK (purpose IN ('OFFLINE_FIXTURE', 'DEV_LIVE', 'FORMAL')),
    plan_unit_id TEXT NOT NULL CHECK (length(trim(plan_unit_id)) > 0),
    scene INTEGER NOT NULL CHECK (scene >= 1),
    group_id TEXT NOT NULL CHECK (group_id IN ('G3', 'G4')),
    mode_policy TEXT NOT NULL CHECK (mode_policy IN ('STANDARD_ONLY', 'DYNAMIC')),
    check_profile TEXT NOT NULL CHECK (check_profile = 'node-ts-http-v1'),
    check_prefix TEXT NOT NULL CHECK (check_prefix IN ('CHK-LIB', 'CHK-INV', 'CHK-TKT', 'CHK-DEV')),
    check_profile_sha256 TEXT NOT NULL CHECK (
        length(check_profile_sha256) = 64 AND check_profile_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    public_input_sha256 TEXT NOT NULL CHECK (
        length(public_input_sha256) = 64 AND public_input_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    material_sha256 TEXT NOT NULL CHECK (
        length(material_sha256) = 64 AND material_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    created_at TIMESTAMP NOT NULL,
    CHECK (
        (purpose = 'OFFLINE_FIXTURE' AND facility_version = 's12b-v1' AND scene IN (1,2)) OR
        (purpose = 'DEV_LIVE' AND facility_version = 's12b-v17' AND check_prefix = 'CHK-DEV'
          AND plan_unit_id IN ('DEV-SENSOR-' || group_id, 'DEV-SENSOR-' || group_id || '-F')) OR
        (purpose = 'FORMAL' AND facility_version = 's12b-v17' AND scene IN (1,2)
          AND ((plan_unit_id GLOB 'R[1-4]-T1-' || group_id AND check_prefix = 'CHK-LIB') OR
               (plan_unit_id GLOB 'R[1-4]-T3-' || group_id AND check_prefix = 'CHK-TKT') OR
               ((plan_unit_id GLOB 'R[1-4]-T2-' || group_id OR plan_unit_id GLOB 'R[1-4]-T2F-' || group_id OR plan_unit_id GLOB 'R[1-4]-Q-' || group_id) AND check_prefix = 'CHK-INV')))
    ),
    CHECK (
        (group_id = 'G3' AND mode_policy = 'STANDARD_ONLY') OR
        (group_id = 'G4' AND mode_policy = 'DYNAMIC')
    )
);
CREATE INDEX idx_cleardev_benchmark_bindings_project
    ON cleardev_benchmark_bindings (ao_project_id, created_at);

INSERT INTO cleardev_benchmark_bindings SELECT * FROM cleardev_benchmark_bindings_previous;
DROP TABLE cleardev_benchmark_bindings_previous;

-- +goose Down
-- Refuse downgrade with live rows rather than deleting or relabeling them.
DROP INDEX idx_cleardev_benchmark_bindings_project;
ALTER TABLE cleardev_benchmark_bindings RENAME TO cleardev_benchmark_bindings_previous;
CREATE TABLE cleardev_benchmark_bindings (
    development_requirement_id TEXT PRIMARY KEY
        REFERENCES cleardev_development_projects(id) ON DELETE CASCADE,
    ao_project_id TEXT NOT NULL REFERENCES projects(id),
    project_root TEXT NOT NULL CHECK (length(trim(project_root)) > 0),
    manifest_path TEXT NOT NULL CHECK (length(trim(manifest_path)) > 0),
    manifest_sha256 TEXT NOT NULL UNIQUE CHECK (
        length(manifest_sha256) = 64 AND manifest_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    facility_version TEXT NOT NULL CHECK (facility_version = 's12b-v1'),
    purpose TEXT NOT NULL CHECK (purpose = 'OFFLINE_FIXTURE'),
    plan_unit_id TEXT NOT NULL CHECK (length(trim(plan_unit_id)) > 0),
    scene INTEGER NOT NULL CHECK (scene IN (1, 2)),
    group_id TEXT NOT NULL CHECK (group_id IN ('G3', 'G4')),
    mode_policy TEXT NOT NULL CHECK (mode_policy IN ('STANDARD_ONLY', 'DYNAMIC')),
    check_profile TEXT NOT NULL CHECK (check_profile = 'node-ts-http-v1'),
    check_prefix TEXT NOT NULL CHECK (check_prefix IN ('CHK-LIB', 'CHK-INV', 'CHK-TKT', 'CHK-DEV')),
    check_profile_sha256 TEXT NOT NULL CHECK (
        length(check_profile_sha256) = 64 AND check_profile_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    public_input_sha256 TEXT NOT NULL CHECK (
        length(public_input_sha256) = 64 AND public_input_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    material_sha256 TEXT NOT NULL CHECK (
        length(material_sha256) = 64 AND material_sha256 NOT GLOB '*[^0-9a-f]*'
    ),
    created_at TIMESTAMP NOT NULL,
    CHECK (
        (group_id = 'G3' AND mode_policy = 'STANDARD_ONLY') OR
        (group_id = 'G4' AND mode_policy = 'DYNAMIC')
    )
);
CREATE INDEX idx_cleardev_benchmark_bindings_project
    ON cleardev_benchmark_bindings (ao_project_id, created_at);

INSERT INTO cleardev_benchmark_bindings SELECT * FROM cleardev_benchmark_bindings_previous;
DROP TABLE cleardev_benchmark_bindings_previous;
