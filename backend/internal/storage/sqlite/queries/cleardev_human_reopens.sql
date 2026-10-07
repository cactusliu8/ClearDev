-- name: ListClearDevHumanDecisionRequestsForRequirement :many
SELECT * FROM cleardev_human_decision_requests WHERE development_project_id=? ORDER BY created_at,id;
-- name: ListClearDevHumanDecisionDispatchHistory :many
SELECT * FROM cleardev_human_decision_dispatches WHERE request_id=? ORDER BY rowid;
-- name: GetClearDevHumanDecisionReopen :one
SELECT * FROM cleardev_human_decision_reopens WHERE request_id=?;
-- name: ListClearDevHumanDecisionReopens :many
SELECT * FROM cleardev_human_decision_reopens WHERE decision_request_id=? ORDER BY created_at,request_id;
-- name: ListRunnableClearDevHumanDecisionReopens :many
SELECT r.* FROM cleardev_human_decision_reopens r
WHERE r.desktop_run_id=? AND NOT EXISTS(SELECT 1 FROM cleardev_human_decision_dispatches d WHERE d.id=r.next_dispatch_id)
ORDER BY r.created_at,r.request_id;
-- name: InsertClearDevHumanDecisionReopen :exec
INSERT INTO cleardev_human_decision_reopens(request_id,decision_request_id,content_sha256,previous_dispatch_id,desktop_run_id,next_dispatch_id,created_at) VALUES(?,?,?,?,?,?,?);
