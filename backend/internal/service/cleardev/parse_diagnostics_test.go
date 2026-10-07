package cleardev

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func (m *memoryAgentAttempts) ReadClearDevAgentStepResultParse(_ context.Context, id string) (core.AgentStepResultParse, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, found := m.parses[id]
	return item, found, nil
}

func TestParseDiagnosticsPreserveHistoricalConclusionAndError(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"memory", "sqlite"} {
		t.Run(name, func(t *testing.T) {
			var attempts AgentAttemptStore
			requirementID := "requirement"
			if name == "memory" {
				attempts = newMemoryAgentAttempts()
			} else {
				store := sqlitetest.MustOpen(t)
				requirementID = seedStandardRequirement(t, store, &standardTestIDs{}, func() time.Time { return now }, false)
				attempts = store
			}
			a := core.AgentStepAttempt{ID: "diagnostic-attempt", DevelopmentRequirementID: requirementID, LogicalStepID: "diagnostic-step", StepCategory: core.AgentStepCategoryStandard, StepKind: core.AgentStepBuilderResult, AttemptNumber: 1, RoleBindingID: "binding", AOSessionID: "session", ClientMessageID: "message", PromptSHA256: strings.Repeat("a", 64), RequestedAt: now, CreatedAt: &now, RequestedAtSemantics: core.AttemptTimeActualCreation}
			if _, _, err := attempts.EnsureClearDevAgentStepAttempt(ctx, a); err != nil {
				t.Fatal(err)
			}
			r := core.AgentStepResult{ID: "diagnostic-result", AttemptID: a.ID, ResultIndex: 1, Source: core.AgentResultOriginal, ClientMessageID: a.ClientMessageID, TurnID: "turn", FinalMessageID: "final", RawMessageText: "old raw reply", RawMessageSHA256: coreDigest([]byte("old raw reply")), ObservedAt: now}
			if err := attempts.RecordClearDevAgentStepResult(ctx, r); err != nil {
				t.Fatal(err)
			}
			original := core.AgentStepResultParse{ResultID: r.ID, Conclusion: core.AgentResultParseInvalid, ErrorSummary: "builder result does not match the active execution round", ParsedAt: now}
			if err := attempts.RecordClearDevAgentStepResultParse(ctx, original); err != nil {
				t.Fatal(err)
			}
			before, err := attempts.ListClearDevAgentStepAttempts(ctx, requirementID)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				service := &Service{attempts: attempts, now: func() time.Time { return now.Add(time.Hour) }}
				diagnostic := &core.AgentResultValidationError{Code: "RESULT_TEXT_TOO_LONG", Field: "summary", Actual: 10001, Limit: 10000}
				if err := service.recordAgentParse(ctx, r.ID, diagnostic); err != nil {
					t.Fatalf("new diagnosis stranded historical parse replay: %v", err)
				}
				if err := service.recordAgentParse(ctx, r.ID, nil); err == nil {
					t.Fatal("historical INVALID was silently upgraded to VALID")
				}
			}
			changed := original
			changed.ErrorSummary = "new detail"
			if err := attempts.RecordClearDevAgentStepResultParse(ctx, changed); err == nil {
				t.Fatal("direct storage write changed immutable history")
			}
			after, err := attempts.ListClearDevAgentStepAttempts(ctx, requirementID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("historical evidence changed: %v", err)
			}
		})
	}
}

func TestParseDiagnosticsNewRecordsAndCorrectionUseAccurateError(t *testing.T) {
	attempts := newMemoryAgentAttempts()
	service := &Service{attempts: attempts, now: time.Now}
	diagnostic := &core.AgentResultValidationError{Code: "RESULT_TEXT_TOO_LONG", Field: "summary", Actual: 10001, Limit: 10000}
	if err := service.recordAgentParse(context.Background(), "new-result", diagnostic); err != nil {
		t.Fatal(err)
	}
	stored, found, err := attempts.ReadClearDevAgentStepResultParse(context.Background(), "new-result")
	if err != nil || !found || stored.ErrorSummary != diagnostic.Error() || stored.Conclusion != core.AgentResultParseInvalid {
		t.Fatalf("new diagnostic was not preserved: %+v %v", stored, err)
	}
	prompt := mechanicalParseCorrectionPrompt(diagnostic)
	if !strings.Contains(prompt, "summary") || !strings.Contains(prompt, "10001") || !strings.Contains(prompt, "10000") || !strings.Contains(prompt, "Do not modify files") {
		t.Fatalf("correction does not identify the error and keep scope: %s", prompt)
	}
}

type unavailableParseReader struct{ *memoryAgentAttempts }

func (m unavailableParseReader) ReadClearDevAgentStepResultParse(context.Context, string) (core.AgentStepResultParse, bool, error) {
	return core.AgentStepResultParse{}, false, errors.New("read unavailable")
}

func TestParseDiagnosticsReadFailureDoesNotWrite(t *testing.T) {
	attempts := newMemoryAgentAttempts()
	service := &Service{attempts: unavailableParseReader{attempts}, now: time.Now}
	if err := service.recordAgentParse(context.Background(), "new-result", errors.New("invalid")); err == nil {
		t.Fatal("read failure was ignored")
	}
	if len(attempts.parses) != 0 {
		t.Fatal("unreadable prior evidence was overwritten")
	}
}

func TestBoundedDiagnosticSummaryKeepsCompleteUTF8(t *testing.T) {
	got := boundedDiagnosticSummary(strings.Repeat("中文", 300))
	if len(got) > 1000 || !utf8.ValidString(got) || got == "" {
		t.Fatalf("bounded diagnostic is invalid UTF-8 or exceeds its limit: %d", len(got))
	}
}
