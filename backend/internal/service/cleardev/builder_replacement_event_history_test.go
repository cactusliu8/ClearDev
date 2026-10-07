package cleardev

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestBuilderReplacementStorageDeliveryEventsRejectReplaceIgnoreAndRowID(t *testing.T) {
	f, _, id := registeredBuilderReplacementStorageFixture(t)
	ctx := context.Background()
	advanceReplacementToCandidateReview(t, f, id)
	state, err := f.store.ReadClearDevBuilderReplacement(ctx, id, time.Now())
	if err != nil || state.Handoff == nil {
		t.Fatal("read delivered handoff", err)
	}
	db := builderReplacementStorageDB(t, f)
	db.SetMaxOpenConns(1)
	var recursive, foreignKeys int
	if err := db.QueryRow(`PRAGMA recursive_triggers`).Scan(&recursive); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || recursive != 0 || foreignKeys != 1 {
		t.Fatal("test must exercise the ordinary SQLite trigger configuration", recursive, foreignKeys, err)
	}
	events := map[string]string{}
	for _, status := range []string{"SENT", "COMPLETED"} {
		var eventID string
		if err := db.QueryRow(`SELECT id FROM cleardev_agent_attempt_events WHERE attempt_id=? AND status=? LIMIT 1`, state.Handoff.AttemptID, status).Scan(&eventID); err != nil {
			t.Fatal("normal Service did not append delivery evidence", status, err)
		}
		events[status] = eventID
	}
	// These additional valid failure/correction facts are explicit storage test
	// inputs, not claims about additional provider sends or business failures.
	for _, status := range []core.AgentAttemptSendStatus{core.AgentAttemptCorrectionSent, core.AgentAttemptFailed, core.AgentAttemptInterrupted, core.AgentAttemptObservationTimedOut, core.AgentAttemptDeliveryUnknown} {
		event := core.AgentAttemptEvent{ID: "history-guard-" + string(status), AttemptID: state.Handoff.AttemptID, Status: status, RecordedAt: time.Now().UTC()}
		if status != core.AgentAttemptCorrectionSent {
			event.FailureCategory = domain.AgentFailureProvider
		}
		if created, err := f.store.EnsureClearDevAgentAttemptEvent(ctx, event); err != nil || !created {
			t.Fatal("legitimate new event was not appended", status, created, err)
		}
		beforeReplay := builderReplacementStorageRows(t, db)
		event.RecordedAt = event.RecordedAt.Add(time.Hour)
		if created, err := f.store.EnsureClearDevAgentAttemptEvent(ctx, event); err != nil || created {
			t.Fatal("exact replay must keep the original fact and time", status, created, err)
		}
		if !reflect.DeepEqual(beforeReplay, builderReplacementStorageRows(t, db)) {
			t.Fatal("idempotent event replay changed facts or CDC", status)
		}
		events[string(status)] = event.ID
	}
	const columns = `id,attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at`
	for status, eventID := range events {
		t.Run(status, func(t *testing.T) {
			before := builderReplacementStorageRows(t, db)
			statements := []string{
				`INSERT OR REPLACE INTO cleardev_agent_attempt_events(` + columns + `) SELECT ` + columns + ` FROM cleardev_agent_attempt_events WHERE id=?`,
				`INSERT OR IGNORE INTO cleardev_agent_attempt_events(` + columns + `) SELECT ` + columns + ` FROM cleardev_agent_attempt_events WHERE id=?`,
				`INSERT OR REPLACE INTO cleardev_agent_attempt_events(rowid,` + columns + `) SELECT rowid,id,attempt_id,'FAILED',client_message_id,prompt_sha256,turn_id,'failed','PROVIDER_FAILURE',1,NULL,'forged-code','forged-history',recorded_at FROM cleardev_agent_attempt_events WHERE id=?`,
				`INSERT OR REPLACE INTO cleardev_agent_attempt_events(rowid,` + columns + `) SELECT rowid,id||':foreign-rowid',attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at FROM cleardev_agent_attempt_events WHERE id=?`,
				`INSERT OR IGNORE INTO cleardev_agent_attempt_events(rowid,` + columns + `) SELECT rowid,id||':foreign-rowid',attempt_id,status,client_message_id,prompt_sha256,turn_id,turn_state,failure_category,retryable,retry_at,provider_error_code,error_summary,recorded_at FROM cleardev_agent_attempt_events WHERE id=?`,
			}
			for _, statement := range statements {
				if _, err := db.Exec(statement, eventID); err == nil || !strings.Contains(err.Error(), "agent attempt event identity already exists") {
					t.Fatal("history identity conflict was not rejected before replacement or ignore", statement, err)
				}
				if !reflect.DeepEqual(before, builderReplacementStorageRows(t, db)) {
					t.Fatal("rejected insert changed event fields, rowid, time, budget or CDC")
				}
			}
		})
	}
	var brokenFK int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&brokenFK); err != nil || brokenFK != 0 {
		t.Fatal("foreign keys", brokenFK, err)
	}
}
