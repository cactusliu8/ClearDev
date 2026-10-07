-- The per-round final review index allowed one review without a previousReview
-- per execution run, which blocks the review of a rework round's new
-- candidate. The index keys on the exact candidate instead, keeping the same
-- guarantees: one review per run, candidate and recheck source.

-- +goose NO TRANSACTION
-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS cleardev_final_review_per_round;
CREATE UNIQUE INDEX cleardev_final_review_per_candidate ON cleardev_requirement_final_reviews(execution_run_id, candidate_commit_sha, COALESCE(json_extract(review_packet_json,'$.previousReview.reviewId'),''));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS temp.cleardev_final_review_index_down_guard;
CREATE TEMP TABLE cleardev_final_review_index_down_guard(ok INTEGER NOT NULL);
CREATE TEMP TRIGGER cleardev_final_review_index_down_guard_valid BEFORE INSERT ON cleardev_final_review_index_down_guard
WHEN NEW.ok<>1 BEGIN SELECT RAISE(ABORT, 'downgrade refuses downstream-guarded project history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
INSERT INTO cleardev_final_review_index_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM (SELECT execution_run_id FROM cleardev_requirement_final_reviews GROUP BY execution_run_id HAVING count(DISTINCT candidate_commit_sha)>1))
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussions WHERE failure_reason='PRODUCT_DISCOVERY_INVALID')
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_check_specs)
 OR EXISTS(SELECT 1 FROM cleardev_product_discussion_contexts)
 OR EXISTS(SELECT 1 FROM cleardev_complex_plan_validations)
 OR EXISTS(SELECT 1 FROM cleardev_complex_engineering_plans WHERE json_extract(plan_json,'$.schemaVersion') IN (2,3))
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
THEN 0 ELSE 1 END;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE cleardev_final_review_index_down_guard;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX IF EXISTS cleardev_final_review_per_candidate;
CREATE UNIQUE INDEX cleardev_final_review_per_round ON cleardev_requirement_final_reviews(execution_run_id,COALESCE(json_extract(review_packet_json,'$.previousReview.reviewId'),''));
-- +goose StatementEnd
