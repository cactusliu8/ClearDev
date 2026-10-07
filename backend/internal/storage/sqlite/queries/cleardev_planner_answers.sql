-- name: ListClearDevPlannerAnswers :many
SELECT * FROM cleardev_planner_answers WHERE development_project_id=? ORDER BY created_at,request_id;

-- name: GetClearDevPlannerAnswer :one
SELECT * FROM cleardev_planner_answers WHERE request_id=?;

-- name: InsertClearDevPlannerAnswer :exec
INSERT INTO cleardev_planner_answers(request_id,development_project_id,plan_id,plan_sha256,answers_json,answers_sha256,next_planning_request_id,created_at)
VALUES(?,?,?,?,?,?,?,?);

-- name: GetClearDevPlannerAnswerPlan :one
SELECT * FROM cleardev_complex_engineering_plans WHERE id=?;

-- name: GetClearDevPlannerQuestionSession :one
SELECT session.id AS ao_session_id, prompt.text AS question_prompt_text, final.text AS question_final_message_text
FROM cleardev_complex_engineering_plans plan
JOIN cleardev_complex_agent_steps step ON step.id=plan.agent_step_id
JOIN cleardev_complex_role_bindings role ON role.id=plan.planner_role_binding_id
JOIN sessions session ON session.id=role.ao_session_id
JOIN conversation_turns question ON question.id=step.turn_id
JOIN conversations conversation ON conversation.id=question.conversation_id
JOIN conversation_branches branch ON branch.id=question.branch_id AND branch.conversation_id=conversation.id
JOIN conversation_messages prompt ON prompt.conversation_id=conversation.id AND prompt.turn_id=question.id AND prompt.client_message_id=sqlc.arg(actual_client_message_id)
JOIN conversation_messages final ON final.id=step.final_message_id AND final.conversation_id=conversation.id AND final.turn_id=question.id
WHERE plan.id=sqlc.arg(plan_id)
AND question.handled_by_session_id=session.id AND question.state='completed' AND question.completed_at IS NOT NULL AND question.rolled_back_at IS NULL
AND conversation.current_session_id=session.id AND branch.session_id=session.id AND conversation.active_branch_id=question.branch_id
AND session.provider_conversation_id<>'' AND branch.provider_conversation_id=session.provider_conversation_id
AND prompt.branch_id=question.branch_id AND prompt.role='user' AND prompt.origin='automation'
AND final.branch_id=question.branch_id AND final.role='assistant' AND final.origin='provider' AND final.streaming=0
AND final.sequence=(SELECT max(message.sequence) FROM conversation_messages message WHERE message.turn_id=question.id AND message.role='assistant');
