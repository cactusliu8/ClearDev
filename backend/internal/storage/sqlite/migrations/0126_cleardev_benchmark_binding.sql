-- +goose Up
-- S12B benchmark bindings are created atomically with one ClearDev requirement.
-- They are intentionally startup-manifest facts, not a caller-selectable mode.
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

-- +goose Down
DROP INDEX IF EXISTS idx_cleardev_benchmark_bindings_project;
DROP TABLE IF EXISTS cleardev_benchmark_bindings;
