-- The same immutable contract is stored twice: inside the run package and in
-- the admission mirror. The run package is encoded by the canonical encoder
-- (no HTML escaping); admissions written before the canonical encoder used
-- Go's default escaping, so the two texts differ only in "\u003c"/"\u003e"/
-- "\u0026" versus their characters while describing the same object. The
-- delivery view compares those texts to prove one exact binding, so it must
-- accept both spellings of the same contract. Nothing else changes: every
-- identity, candidate, review and check predicate stays byte-identical.
-- +goose NO TRANSACTION
-- +goose Up
DROP VIEW cleardev_completed_project_deliveries;
CREATE VIEW cleardev_completed_project_deliveries AS
SELECT s.product_id, s.id AS stage_id, r.id AS execution_run_id,
 r.development_project_id AS requirement_id, result.id AS result_id,
 candidate.id AS integration_candidate_id, candidate.commit_sha AS candidate_sha,
 admission.contract_json, review.id AS final_review_id
FROM cleardev_product_stages s
JOIN cleardev_complex_execution_runs r ON r.development_project_id=s.development_requirement_id
JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=r.id AND admission.stage_id=s.id AND admission.plan_id=r.plan_id
JOIN cleardev_complex_execution_results result ON result.execution_run_id=r.id
JOIN cleardev_integration_candidates candidate ON candidate.id=result.integration_candidate_id AND candidate.complex_execution_result_id=result.id
JOIN cleardev_requirement_final_reviews review ON review.execution_run_id=r.id
WHERE r.status IS 'COMPLETED' AND r.settled_at IS NOT NULL
 AND result.completion_status IS 'COMPLETED' AND result.completed_at IS NOT NULL
 AND json_extract(admission.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(admission.contract_json,'$.productId') IS s.product_id
 AND json_extract(admission.contract_json,'$.stageId') IS s.id
 AND json_extract(r.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND replace(replace(replace(json_extract(r.execution_package_json,'$.projectExecution'), '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
     IS replace(replace(replace(admission.contract_json, '\u003c', '<'), '\u003e', '>'), '\u0026', '&')
 AND candidate.requirement_version_id IS r.requirement_version_id
 AND candidate.development_project_id IS r.development_project_id
 AND review.rowid=(SELECT max(latest.rowid) FROM cleardev_requirement_final_reviews latest WHERE latest.execution_run_id=r.id)
 AND review.status IS 'SETTLED' AND review.verdict IS 'PASS' AND review.result_id IS NOT NULL AND review.settled_at IS NOT NULL
 AND review.development_project_id IS r.development_project_id
 AND review.requirement_version_id IS r.requirement_version_id AND review.requirement_sha256 IS r.requirement_sha256
 AND review.plan_id IS r.plan_id AND review.plan_sha256 IS r.plan_sha256
 AND review.candidate_commit_sha IS candidate.commit_sha
 AND review.base_commit_sha IS json_extract(admission.contract_json,'$.baseCommitSha')
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id
     AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected WHERE expected.value IS checks.check_run_id))
 AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected
     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id AND checks.check_run_id IS expected.value));

-- +goose Down
-- Refuse the downgrade as soon as any controlled execution or new evidence
-- exists (the same protection 0176/0177 apply), so a refused downgrade leaves
-- the migration version and schema untouched instead of half-reverting.
-- +goose StatementBegin
CREATE TEMP TABLE IF NOT EXISTS cleardev_delivery_view_downgrade_guard (ok INTEGER NOT NULL CHECK(ok = 1));
DELETE FROM cleardev_delivery_view_downgrade_guard;
INSERT INTO cleardev_delivery_view_downgrade_guard
SELECT CASE WHEN
    EXISTS (SELECT 1 FROM projects WHERE json_type(COALESCE(config, '{}'), '$.cleardev') = 'object')
    OR EXISTS (SELECT 1 FROM cleardev_complex_execution_runs)
    OR EXISTS (SELECT 1 FROM cleardev_workflow_recoveries)
    THEN 0 ELSE 1 END;
DROP TABLE cleardev_delivery_view_downgrade_guard;
-- +goose StatementEnd

-- Restore the exact 0154 predicate; no data or evidence is rewritten.
-- +goose NO TRANSACTION
DROP VIEW cleardev_completed_project_deliveries;
CREATE VIEW cleardev_completed_project_deliveries AS
SELECT s.product_id, s.id AS stage_id, r.id AS execution_run_id,
 r.development_project_id AS requirement_id, result.id AS result_id,
 candidate.id AS integration_candidate_id, candidate.commit_sha AS candidate_sha,
 admission.contract_json, review.id AS final_review_id
FROM cleardev_product_stages s
JOIN cleardev_complex_execution_runs r ON r.development_project_id=s.development_requirement_id
JOIN cleardev_project_execution_admissions admission ON admission.execution_run_id=r.id AND admission.stage_id=s.id AND admission.plan_id=r.plan_id
JOIN cleardev_complex_execution_results result ON result.execution_run_id=r.id
JOIN cleardev_integration_candidates candidate ON candidate.id=result.integration_candidate_id AND candidate.complex_execution_result_id=result.id
JOIN cleardev_requirement_final_reviews review ON review.execution_run_id=r.id
WHERE r.status IS 'COMPLETED' AND r.settled_at IS NOT NULL
 AND result.completion_status IS 'COMPLETED' AND result.completed_at IS NOT NULL
 AND json_extract(admission.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(admission.contract_json,'$.productId') IS s.product_id
 AND json_extract(admission.contract_json,'$.stageId') IS s.id
 AND json_extract(r.execution_package_json,'$.planValidationPolicy') IS 'PROJECT_EXECUTION_V1'
 AND json_extract(r.execution_package_json,'$.projectExecution') IS admission.contract_json
 AND candidate.requirement_version_id IS r.requirement_version_id
 AND candidate.development_project_id IS r.development_project_id
 AND review.rowid=(SELECT max(latest.rowid) FROM cleardev_requirement_final_reviews latest WHERE latest.execution_run_id=r.id)
 AND review.status IS 'SETTLED' AND review.verdict IS 'PASS' AND review.result_id IS NOT NULL AND review.settled_at IS NOT NULL
 AND review.development_project_id IS r.development_project_id
 AND review.requirement_version_id IS r.requirement_version_id AND review.requirement_sha256 IS r.requirement_sha256
 AND review.plan_id IS r.plan_id AND review.plan_sha256 IS r.plan_sha256
 AND review.candidate_commit_sha IS candidate.commit_sha
 AND review.base_commit_sha IS json_extract(admission.contract_json,'$.baseCommitSha')
 AND EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id
     AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected WHERE expected.value IS checks.check_run_id))
 AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected
     WHERE NOT EXISTS(SELECT 1 FROM cleardev_complex_execution_result_checks checks WHERE checks.result_id=result.id AND checks.check_run_id IS expected.value));
