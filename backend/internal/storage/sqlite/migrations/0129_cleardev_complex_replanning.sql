-- +goose Up
-- S12B v20: allow natural one-task complex plans and keep a single-task plan
-- on STANDARD/1. The previous effective trigger (0117) required 2-6 tasks and
-- let a one-task plan claim a degraded or parallel mode. Redefine the current
-- trigger only; 0110-0128 stay untouched.
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid;
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_runs
WHEN NEW.status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    JOIN cleardev_complex_engineering_plans AS plan ON plan.id = NEW.plan_id
    JOIN cleardev_complex_plan_reviews AS review ON review.id = NEW.plan_review_id
    JOIN cleardev_complex_role_bindings AS steward ON steward.id = NEW.steward_role_binding_id
    WHERE version.id = NEW.requirement_version_id
      AND project.id = NEW.development_project_id
      AND project.cancelled_at IS NULL
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.plan_sha256 = NEW.plan_sha256
      AND review.plan_id = plan.id
      AND review.plan_sha256 = plan.plan_sha256
      AND review.verdict = 'APPROVED'
      AND (
        (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'ONE_BUILDER_REQUIRED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') = 1)
        OR (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'PARALLEL_UNSAFE_DEGRADED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
           AND json_array_length(plan.plan_json, '$.tasks') > 1)
        OR (NEW.mode = 'PARALLEL' AND NEW.selection_reason_code = 'PARALLEL_PLAN_APPROVED'
           AND NEW.fixed_builder_count BETWEEN 2 AND 3
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
           AND NEW.fixed_builder_count <= json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount')
           AND json_array_length(plan.plan_json, '$.tasks') > 1)
      )
      AND json_array_length(plan.plan_json, '$.tasks') BETWEEN 1 AND 6
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
 )
 OR EXISTS (
    SELECT 1 FROM cleardev_direction_stop_gates AS gate
    WHERE gate.requirement_version_id = NEW.requirement_version_id AND gate.status = 'ACTIVE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run lacks the exact approved current plan or is stopped');
END;
-- +goose StatementEnd

-- +goose Down
-- A one-task execution cannot legally coexist with the old 2-6 task trigger.
-- Refuse and roll back instead of deleting history.
-- +goose StatementBegin
CREATE TEMP TABLE cleardev_complex_replanning_down_guard (
    single_task INTEGER NOT NULL CHECK (single_task = 0)
);
INSERT INTO cleardev_complex_replanning_down_guard (single_task)
SELECT 1
FROM cleardev_complex_execution_runs AS run
JOIN cleardev_complex_engineering_plans AS plan ON plan.id = run.plan_id
WHERE json_array_length(plan.plan_json, '$.tasks') = 1
LIMIT 1;
DROP TABLE cleardev_complex_replanning_down_guard;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS cleardev_complex_execution_run_insert_valid;
-- +goose StatementBegin
CREATE TRIGGER cleardev_complex_execution_run_insert_valid
BEFORE INSERT ON cleardev_complex_execution_runs
WHEN NEW.status <> 'PENDING'
 OR NOT EXISTS (
    SELECT 1
    FROM cleardev_contract_versions AS version
    JOIN cleardev_development_projects AS project ON project.id = version.development_project_id
    JOIN cleardev_complex_engineering_plans AS plan ON plan.id = NEW.plan_id
    JOIN cleardev_complex_plan_reviews AS review ON review.id = NEW.plan_review_id
    JOIN cleardev_complex_role_bindings AS steward ON steward.id = NEW.steward_role_binding_id
    WHERE version.id = NEW.requirement_version_id
      AND project.id = NEW.development_project_id
      AND project.cancelled_at IS NULL
      AND version.state = 'APPROVED'
      AND version.superseded_by_id IS NULL
      AND version.sha256 = NEW.requirement_sha256
      AND version.task_set_version = NEW.expected_task_set_version
      AND plan.development_project_id = project.id
      AND plan.requirement_version_id = version.id
      AND plan.plan_sha256 = NEW.plan_sha256
      AND review.plan_id = plan.id
      AND review.plan_sha256 = plan.plan_sha256
      AND review.verdict = 'APPROVED'
      AND (
        (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'ONE_BUILDER_REQUIRED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') = 1)
        OR (NEW.mode = 'STANDARD' AND NEW.selection_reason_code = 'PARALLEL_UNSAFE_DEGRADED' AND NEW.fixed_builder_count = 1
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3)
        OR (NEW.mode = 'PARALLEL' AND NEW.selection_reason_code = 'PARALLEL_PLAN_APPROVED'
           AND NEW.fixed_builder_count BETWEEN 2 AND 3
           AND json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount') BETWEEN 2 AND 3
           AND NEW.fixed_builder_count <= json_extract(plan.plan_json, '$.parallelSuggestion.recommendedBuilderCount'))
      )
      AND json_array_length(plan.plan_json, '$.tasks') BETWEEN 2 AND 6
      AND steward.development_project_id = project.id
      AND steward.role = 'STEWARD'
      AND steward.status = 'BOUND'
 )
 OR EXISTS (
    SELECT 1 FROM cleardev_direction_stop_gates AS gate
    WHERE gate.requirement_version_id = NEW.requirement_version_id AND gate.status = 'ACTIVE'
 )
BEGIN
    SELECT RAISE(ABORT, 'cleardev complex execution run lacks the exact approved current plan or is stopped');
END;
-- +goose StatementEnd
