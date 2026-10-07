-- name: InsertClearDevBenchmarkBinding :exec
INSERT INTO cleardev_benchmark_bindings (
    development_requirement_id, ao_project_id, project_root, manifest_path,
    manifest_sha256, facility_version, purpose, plan_unit_id, scene, group_id,
    mode_policy, check_profile, check_prefix, check_profile_sha256,
    public_input_sha256, material_sha256, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetClearDevBenchmarkBinding :one
SELECT * FROM cleardev_benchmark_bindings
WHERE development_requirement_id = ?;

-- name: GetClearDevBenchmarkBindingByManifest :one
SELECT * FROM cleardev_benchmark_bindings
WHERE manifest_sha256 = ?;
