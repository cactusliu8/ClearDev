package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

type attemptQueryTrace struct {
	gen.DBTX
	queries []string
}

func (t *attemptQueryTrace) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	t.queries = append(t.queries, query)
	return t.DBTX.QueryContext(ctx, query, args...)
}
func (t *attemptQueryTrace) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	t.queries = append(t.queries, query)
	return t.DBTX.QueryRowContext(ctx, query, args...)
}

func TestAgentAttemptStateQueriesExcludeRawResults(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	goose.SetBaseFS(os.DirFS(".."))
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO projects(id,path,registered_at,config,kind) VALUES('project','/repo',?,'{}','single_repo');
 INSERT INTO cleardev_development_projects(id,ao_project_id,name,state,created_at,updated_at) VALUES('requirement','project','query test','INTAKE',?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db, db)
	for _, id := range []string{"target", "unrelated"} {
		a := core.AgentStepAttempt{ID: id, DevelopmentRequirementID: "requirement", LogicalStepID: id, StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "binding", AOSessionID: "session", ClientMessageID: id + ":message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
		if _, _, err := store.EnsureClearDevAgentStepAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
		raw := strings.Repeat("x", 2<<20)
		if err := store.RecordClearDevAgentStepResult(ctx, core.AgentStepResult{ID: id + ":result", AttemptID: id, ResultIndex: 1, Source: core.AgentResultOriginal, ClientMessageID: a.ClientMessageID, TurnID: "turn", FinalMessageID: "final", RawMessageText: raw, RawMessageSHA256: strings.Repeat("b", 64), ObservedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	// Insert events with reversed clock order: insertion order is authoritative.
	for i, msg := range []string{"target:message", "target:message:parse-correction", "target:message"} {
		if err := store.RecordClearDevAgentAttemptEvent(ctx, core.AgentAttemptEvent{ID: msg + string(rune('a'+i)), AttemptID: "target", Status: core.AgentAttemptSent, ClientMessageID: msg, RecordedAt: now.Add(-time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	trace := &attemptQueryTrace{DBTX: db}
	store.qr = gen.New(trace)
	full, err := store.ListClearDevAgentStepAttempts(ctx, "requirement")
	if err != nil || len(full) != 2 {
		t.Fatalf("full=%d err=%v", len(full), err)
	}
	t.Logf("before: full evidence queries=%d raw bytes=%d", len(trace.queries), len(full[0].Results[0].RawMessageText)+len(full[1].Results[0].RawMessageText))
	trace.queries = nil
	step, err := store.ListClearDevAgentStepAttemptStates(ctx, "requirement", "target")
	if err != nil || len(step) != 1 || len(step[0].Results) != 0 {
		t.Fatalf("step=%#v err=%v", step, err)
	}
	one, err := store.GetClearDevAgentAttemptState(ctx, "requirement", "target")
	if err != nil || one.LastEventID != "target:messagec" {
		t.Fatalf("one=%#v err=%v", one, err)
	}
	correction, err := store.GetClearDevAgentDeliveryState(ctx, "requirement", "target", "target:message:parse-correction")
	if err != nil || correction.LastEventID != "target:message:parse-correctionb" {
		t.Fatalf("correction=%#v err=%v", correction, err)
	}
	latest, err := store.ListLatestClearDevAgentAttemptStates(ctx, "requirement")
	if err != nil || len(latest) != 2 {
		t.Fatalf("latest=%#v err=%v", latest, err)
	}
	for _, query := range trace.queries {
		if strings.Contains(query, "raw_message") || strings.Contains(query, "cleardev_agent_step_results") || strings.Contains(query, "cleardev_agent_step_result_parses") {
			t.Fatalf("state query selected raw evidence: %s", query)
		}
	}
	t.Logf("after: four state operations queries=%d raw bytes=0; unrelated 2 MiB result excluded", len(trace.queries))
	if _, err := store.GetClearDevAgentAttemptState(ctx, "other", "target"); err == nil {
		t.Fatal("cross-requirement attempt read")
	}
}
