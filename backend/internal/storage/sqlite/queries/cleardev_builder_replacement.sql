-- name: InsertClearDevBuilderReplacementRequest :exec
INSERT INTO cleardev_builder_replacement_requests(id,requirement_id,execution_run_id,logical_step_id,decision_request_id,target_id,binding_json,source_json,old_dispatch_json,old_step_json,original_stopped_at,original_status,original_reason,original_summary,supplement,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevBuilderReplacementRequest :one
SELECT * FROM cleardev_builder_replacement_requests WHERE id=?;

-- name: InsertClearDevBuilderReplacementGrant :exec
INSERT INTO cleardev_builder_replacement_grants(decision_request_id,request_id,logical_step_id,created_at) VALUES(?,?,?,?);

-- name: GetClearDevBuilderReplacementGrant :one
SELECT * FROM cleardev_builder_replacement_grants WHERE decision_request_id=?;

-- name: InsertClearDevBuilderReplacementHandoff :exec
INSERT INTO cleardev_builder_replacement_handoffs(id,request_id,decision_request_id,logical_step_id,new_role_binding_id,session_creation_key,retirement_event_id,second_attempt_id,supplement,created_at) VALUES(?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevBuilderReplacementHandoff :one
SELECT * FROM cleardev_builder_replacement_handoffs WHERE id=?;

-- name: InsertClearDevBuilderReplacementAlias :exec
INSERT INTO cleardev_builder_replacement_aliases(handoff_id,logical_step_id,new_role_binding_id,new_ao_session_id,workspace_path,launch_sha256,snapshot_sha256,second_attempt_id,created_at) VALUES(?,?,?,?,?,?,?,?,?);

-- name: GetClearDevBuilderReplacementAlias :one
SELECT * FROM cleardev_builder_replacement_aliases WHERE handoff_id=?;

-- name: InsertClearDevBuilderReplacementObservation :exec
INSERT INTO cleardev_builder_replacement_observations(id,handoff_id,stage,operation_key,outcome,reason_code,ao_session_id,workspace_path,launch_sha256,snapshot_sha256,observed_at) VALUES(?,?,?,?,?,?,?,?,?,?,?);

-- name: GetClearDevBuilderReplacementObservation :one
SELECT * FROM cleardev_builder_replacement_observations WHERE id=?;

-- name: InsertClearDevBuilderSessionFence :exec
INSERT INTO cleardev_builder_session_fences(ao_session_id,old_role_binding_id,handoff_id,created_at) VALUES(?,?,?,?);

-- name: GetClearDevBuilderSessionFence :one
SELECT * FROM cleardev_builder_session_fences WHERE ao_session_id=?;

-- name: InsertClearDevBuilderSessionOperation :exec
INSERT INTO cleardev_builder_session_operations(id,ao_session_id,kind,created_at) VALUES(?,?,?,?);

-- name: GetClearDevBuilderSessionOperation :one
SELECT * FROM cleardev_builder_session_operations WHERE id=?;

-- name: InsertClearDevBuilderSessionOperationEnd :exec
INSERT INTO cleardev_builder_session_operation_ends(operation_id,outcome,created_at) VALUES(?,?,?);

-- name: GetClearDevBuilderSessionOperationEnd :one
SELECT * FROM cleardev_builder_session_operation_ends WHERE operation_id=?;

-- name: InsertClearDevBuilderHandoffContext :exec
INSERT INTO cleardev_builder_handoff_contexts(ao_session_id,context_json,reference_path,context_sha256,snapshot_sha256,system_prompt,launch_fingerprint,created_at) VALUES(?,?,?,?,?,?,?,?);

-- name: GetClearDevBuilderHandoffContext :one
SELECT * FROM cleardev_builder_handoff_contexts WHERE ao_session_id=?;

-- name: GetClearDevBuilderReplacementRequestForStep :one
SELECT * FROM cleardev_builder_replacement_requests WHERE logical_step_id=?;

-- name: GetClearDevBuilderReplacementRequestForDecision :one
SELECT * FROM cleardev_builder_replacement_requests WHERE decision_request_id=?;

-- name: GetClearDevBuilderReplacementHandoffForStep :one
SELECT * FROM cleardev_builder_replacement_handoffs WHERE logical_step_id=?;

-- name: GetClearDevBuilderReplacementAliasForStep :one
SELECT * FROM cleardev_builder_replacement_aliases WHERE logical_step_id=?;

-- name: ListClearDevBuilderReplacementRequests :many
SELECT * FROM cleardev_builder_replacement_requests WHERE requirement_id=? ORDER BY created_at,id;

-- name: ListClearDevBuilderReplacementObservations :many
SELECT * FROM cleardev_builder_replacement_observations WHERE handoff_id=? ORDER BY observed_at,id;

-- name: CountOpenClearDevBuilderSessionOperations :one
SELECT count(*) FROM cleardev_builder_session_operations op WHERE op.ao_session_id=? AND NOT EXISTS(SELECT 1 FROM cleardev_builder_session_operation_ends done WHERE done.operation_id=op.id);

-- name: IsClearDevManagedBuilderSession :one
SELECT EXISTS(SELECT 1 FROM cleardev_complex_execution_role_bindings b JOIN cleardev_complex_execution_runs r ON r.id=b.execution_run_id WHERE b.ao_session_id=sqlc.narg(ao_session_id) AND b.role='BUILDER' AND r.mode='STANDARD' AND json_extract(r.execution_package_json,'$.projectExecution.policy')='PROJECT_EXECUTION_V1' UNION ALL SELECT 1 FROM sessions s JOIN cleardev_builder_replacement_handoffs h ON h.session_creation_key=s.creation_idempotency_key JOIN cleardev_builder_replacement_requests request ON request.id=h.request_id JOIN cleardev_development_projects project ON project.id=request.requirement_id WHERE s.id=sqlc.narg(ao_session_id) AND s.project_id=project.ao_project_id);

-- name: HasOpenClearDevBuilderConversationTurn :one
SELECT EXISTS(SELECT 1 FROM conversation_turns WHERE handled_by_session_id=? AND state IN ('queued','running'));
