-- 0192: positive local no-send evidence, not a reinterpretation of unknown delivery.
-- Goose's transaction includes the single schema edit, both guards and the new
-- binding trigger. Existing rows, rowids, reservations and CDC triggers stay intact.
-- +goose Up
CREATE TEMP TABLE cleardev_presend_schema_guard(ok INTEGER NOT NULL CHECK(ok=1));
INSERT INTO cleardev_presend_schema_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_agent_attempt_events' AND instr(sql,'''DELIVERY_UNKNOWN'', ''RETIRED_BEFORE_SEND''')>0 AND instr(sql,'status NOT IN (''SENT'', ''CORRECTION_SENT'', ''COMPLETED'', ''RETIRED_BEFORE_SEND'')')>0 AND instr(sql,'OR (status=''RETIRED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=0 AND retry_at IS NULL AND provider_error_code='''')')>0) THEN 1 ELSE 0 END;
PRAGMA writable_schema=ON;
UPDATE sqlite_master SET sql = replace(replace(replace(sql,
  '''DELIVERY_UNKNOWN'', ''RETIRED_BEFORE_SEND''',
  '''DELIVERY_UNKNOWN'', ''RETIRED_BEFORE_SEND'', ''FAILED_BEFORE_SEND'''),
  'status NOT IN (''SENT'', ''CORRECTION_SENT'', ''COMPLETED'', ''RETIRED_BEFORE_SEND'')',
  'status NOT IN (''SENT'', ''CORRECTION_SENT'', ''COMPLETED'', ''RETIRED_BEFORE_SEND'', ''FAILED_BEFORE_SEND'')'),
  'OR (status=''RETIRED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=0 AND retry_at IS NULL AND provider_error_code='''')',
  'OR (status=''RETIRED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=0 AND retry_at IS NULL AND provider_error_code='''')
        OR (status=''FAILED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=1 AND retry_at IS NULL AND provider_error_code='''')')
WHERE type='table' AND name='cleardev_agent_attempt_events';
PRAGMA writable_schema=RESET;
INSERT INTO cleardev_presend_schema_guard SELECT CASE WHEN EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cleardev_agent_attempt_events' AND instr(sql,'''DELIVERY_UNKNOWN'', ''RETIRED_BEFORE_SEND'', ''FAILED_BEFORE_SEND''')>0 AND instr(sql,'status NOT IN (''SENT'', ''CORRECTION_SENT'', ''COMPLETED'', ''RETIRED_BEFORE_SEND'', ''FAILED_BEFORE_SEND'')')>0 AND instr(sql,'OR (status=''RETIRED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=0 AND retry_at IS NULL AND provider_error_code='''')
        OR (status=''FAILED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=1 AND retry_at IS NULL AND provider_error_code='''')')>0) THEN 1 ELSE 0 END;
DROP TABLE cleardev_presend_schema_guard;

-- +goose StatementBegin
CREATE TRIGGER cleardev_failed_before_send_binding
BEFORE INSERT ON cleardev_agent_attempt_events
WHEN NEW.status='FAILED_BEFORE_SEND' AND NOT EXISTS (
 SELECT 1 FROM cleardev_agent_step_attempts a
 JOIN cleardev_agent_message_reservations r ON r.attempt_id=a.id
 JOIN cleardev_message_budget_versions v ON v.development_project_id=a.development_project_id
 WHERE a.id=NEW.attempt_id AND a.attempt_number IN(1,2)
 AND a.client_message_id=NEW.client_message_id AND a.prompt_sha256=NEW.prompt_sha256
 AND r.client_message_id=a.client_message_id AND r.prompt_sha256=a.prompt_sha256
 AND r.ao_session_id=a.ao_session_id AND r.logical_step_id=a.logical_step_id
 AND r.development_project_id=a.development_project_id
 AND r.budget_version='MESSAGE_BUDGET_V1' AND v.version=r.budget_version
 AND ((a.attempt_number=1 AND r.source='ORIGINAL') OR (a.attempt_number=2 AND r.source='RECOVERY_ORIGINAL'))
 AND EXISTS(SELECT 1 FROM cleardev_agent_attempt_events e
   WHERE e.attempt_id=a.id AND e.status='DELIVERY_UNKNOWN'
   AND e.client_message_id=a.client_message_id AND e.prompt_sha256=a.prompt_sha256)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_attempt_events e WHERE e.attempt_id=a.id
   AND (e.status<>'DELIVERY_UNKNOWN' OR e.client_message_id<>a.client_message_id
     OR (e.prompt_sha256<>'' AND e.prompt_sha256<>a.prompt_sha256)
     OR e.turn_id<>'' OR e.turn_state<>'' OR e.failure_category<>'DELIVERY_UNKNOWN'))
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_step_results WHERE attempt_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM cleardev_agent_message_confirmations WHERE client_message_id=a.client_message_id)
 AND NOT EXISTS(SELECT 1 FROM conversation_messages WHERE client_message_id=a.client_message_id)
)
BEGIN SELECT RAISE(ABORT,'failed-before-send proof changed its reservation or contradicts delivery'); END;
-- +goose StatementEnd

-- +goose Down
CREATE TEMP TABLE cleardev_presend_down_guard(ok INTEGER NOT NULL CHECK(ok=1));
-- Preserve the earlier historical-downgrade boundary: a failed later Down must
-- not first remove this migration's version or its evidence protections.
INSERT INTO cleardev_presend_down_guard SELECT CASE WHEN
 EXISTS(SELECT 1 FROM cleardev_agent_attempt_events WHERE status='FAILED_BEFORE_SEND')
 OR EXISTS(SELECT 1 FROM cleardev_agent_step_attempts)
 OR EXISTS(SELECT 1 FROM cleardev_agent_message_reservations)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_requests)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_dispatches)
 OR EXISTS(SELECT 1 FROM cleardev_human_decision_reopens)
 OR EXISTS(SELECT 1 FROM cleardev_project_execution_admissions)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_runs)
 OR EXISTS(SELECT 1 FROM cleardev_planner_runtime_events)
 OR EXISTS(SELECT 1 FROM cleardev_complex_exception_budgets WHERE used_turns>max_turns OR authorized_extra_turns>0)
 OR EXISTS(SELECT 1 FROM cleardev_complex_execution_task_attempts WHERE round>6)
 OR EXISTS(SELECT 1 FROM projects WHERE json_type(COALESCE(config,'{}'),'$.cleardev')='object')
THEN 0 ELSE 1 END;
DROP TABLE cleardev_presend_down_guard;
DROP TRIGGER cleardev_failed_before_send_binding;
PRAGMA writable_schema=ON;
UPDATE sqlite_master SET sql = replace(replace(replace(sql,
  'OR (status=''RETIRED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=0 AND retry_at IS NULL AND provider_error_code='''')
        OR (status=''FAILED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=1 AND retry_at IS NULL AND provider_error_code='''')',
  'OR (status=''RETIRED_BEFORE_SEND'' AND turn_id='''' AND turn_state='''' AND failure_category='''' AND retryable=0 AND retry_at IS NULL AND provider_error_code='''')'),
  'status NOT IN (''SENT'', ''CORRECTION_SENT'', ''COMPLETED'', ''RETIRED_BEFORE_SEND'', ''FAILED_BEFORE_SEND'')',
  'status NOT IN (''SENT'', ''CORRECTION_SENT'', ''COMPLETED'', ''RETIRED_BEFORE_SEND'')'),
  '''DELIVERY_UNKNOWN'', ''RETIRED_BEFORE_SEND'', ''FAILED_BEFORE_SEND''',
  '''DELIVERY_UNKNOWN'', ''RETIRED_BEFORE_SEND''')
WHERE type='table' AND name='cleardev_agent_attempt_events';
PRAGMA writable_schema=RESET;
