-- name: GetClearDevReviewCheckRequest :one
SELECT * FROM cleardev_review_check_requests WHERE review_id=?;

-- name: InsertClearDevReviewCheckRequest :exec
INSERT INTO cleardev_review_check_requests(review_id,request_json,request_sha256,created_at) VALUES(?,?,?,?);

-- name: ListClearDevReviewCheckResults :many
SELECT * FROM cleardev_review_check_results WHERE review_id=? ORDER BY check_id;

-- name: InsertClearDevReviewCheckResult :exec
INSERT INTO cleardev_review_check_results(review_id,check_id,run_id,result_json,result_sha256,created_at) VALUES(?,?,?,?,?,?);
