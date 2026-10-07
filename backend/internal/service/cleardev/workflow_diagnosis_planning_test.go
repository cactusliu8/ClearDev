package cleardev

import (
	"context"
	"fmt"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestWorkflowBlockerDiagnosisKeepsStaleExecutionHistorical(t *testing.T) {
	view, recovery := blockerProjectionFixture()
	recovery.Options[0].UnavailableReason = "EXECUTION_NOT_CURRENT"
	view.TrustedProgress.Blockers = []core.TrustedIssue{{Kind: "EXECUTION_BLOCKED", SubjectID: "run", ReasonCode: "BUILDER_SPAWN_FAILED"}}
	diagnosis := workflowDiagnosisFromFacts(view, recovery, time.Now())
	if len(diagnosis.Issues) < 2 || diagnosis.Issues[0].ReasonCode != "EXECUTION_NOT_CURRENT" {
		t.Fatalf("current source problem hidden by an old failure: %+v", diagnosis.Issues)
	}
	for _, issue := range diagnosis.Issues {
		if issue.ReasonCode == "BUILDER_SPAWN_FAILED" && issue.Relationship != "HISTORICAL" {
			t.Fatal("failure of an invalidated run was called a current blocker")
		}
		if issue.Relationship == "PRECEDING_FAILURE" {
			t.Fatal("outdated run's rework context must not be treated as current rework")
		}
	}
}

func TestWorkflowBlockerDiagnosisClarificationIsNotNativeApproval(t *testing.T) {
	view := RequirementView{
		TrustedProgress: core.TrustedProgressSummary{Phase: core.TrustedPhaseAwaitingClarification, PendingDecisions: []core.TrustedIssue{{Kind: "CLARIFICATION_ANSWERS"}}},
		ComplexPlanning: &ComplexPlanningView{
			Phase:               core.ComplexPlanningAwaitingClarification,
			CompilationRequests: []core.ComplexCompilationRequest{{ID: "old"}, {ID: "current"}},
			Questions: []core.ComplexClarificationQuestion{
				{CompilationRequestID: "old", QuestionKey: "old", Text: "obsolete question"},
				{CompilationRequestID: "current", QuestionKey: "answered", Text: "already answered"},
				{CompilationRequestID: "current", QuestionKey: "storage", Text: "Which local data must remain?", Reason: "Avoid losing the user's existing records"},
			},
			Answers: []core.ComplexClarificationAnswer{{CompilationRequestID: "current", QuestionKey: "answered", Text: "saved answer"}},
		},
	}
	diagnosis := workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
	if len(diagnosis.Issues) != 1 || diagnosis.Issues[0].Category != "INPUT" {
		t.Fatalf("clarification was mislabeled as an approval: %+v", diagnosis.Issues)
	}
	questions := 0
	for _, fact := range diagnosis.Issues[0].Evidence {
		if fact.Kind == "QUESTION" {
			questions++
			if fact.Value != "Which local data must remain?" {
				t.Fatal("wrong question selected")
			}
		}
	}
	if questions != 1 {
		t.Fatal("the actual unanswered question is not visible")
	}
}

func TestWorkflowBlockerDiagnosisDetectsMixedAttemptSnapshot(t *testing.T) {
	view, recovery := blockerProjectionFixture()
	number := int64(1)
	view.TrustedProgress.ControlledWork = []core.ControlledWork{{LogicalStepID: "pending-step", RoleBindingID: "builder", AOSessionID: "original", AttemptID: "first", AttemptNumber: &number, EvidenceID: "first-event"}}
	view.AgentStepAttempts = []core.AgentStepAttemptView{
		{ID: "first", LogicalStepID: "pending-step", RoleBindingID: "builder", AOSessionID: "original", AttemptNumber: 1, LastEventID: "first-event"},
		{ID: "second", LogicalStepID: "pending-step", RoleBindingID: "builder", AOSessionID: "original", AttemptNumber: 2, LastEventID: "second-event"},
	}
	diagnosis := workflowDiagnosisFromFacts(view, recovery, time.Now())
	if diagnosis.Current || diagnosis.ReadError != "CURRENT_BINDINGS_CHANGED" || len(diagnosis.Issues) != 0 {
		t.Fatal("first failure heading mixed with second attempt evidence")
	}
}

func TestWorkflowBlockerDiagnosisPreflightRolesDoNotCollapse(t *testing.T) {
	view := RequirementView{TrustedProgress: core.TrustedProgressSummary{Phase: core.TrustedPhaseBlocked,
		ControlledWork: []core.ControlledWork{
			{Role: "BUILDER", RoleBindingID: "builder", PreflightID: "builder-preflight", ReasonCode: "LOGIN_REQUIRED"},
			{Role: "REVIEWER", RoleBindingID: "reviewer", PreflightID: "reviewer-preflight", ReasonCode: "LOGIN_REQUIRED"},
		}}}
	diagnosis := workflowDiagnosisFromFacts(view, core.WorkflowRecoveryView{}, time.Now())
	if len(diagnosis.Issues) != 2 || diagnosis.Issues[0].SubjectID == diagnosis.Issues[1].SubjectID {
		t.Fatal("roles without logical steps were deduplicated as the same failure")
	}
	store := &diagnosisPreflightStore{records: map[string]core.ControlledPreflight{
		"builder":  {ID: "builder-preflight", RoleBindingID: "builder", ErrorSummary: "builder account detail", Outcome: "FAILED"},
		"reviewer": {ID: "reviewer-preflight", RoleBindingID: "reviewer", ErrorSummary: "reviewer account detail", Outcome: "FAILED"},
	}}
	s := &Service{preflights: store}
	s.addWorkflowDiagnosisPreflights(context.Background(), diagnosis)
	for _, issue := range diagnosis.Issues {
		found := false
		for _, fact := range issue.Evidence {
			if fact.Kind == "PREFLIGHT_SUMMARY" {
				found = fact.Value == issue.SubjectID+" account detail"
			}
		}
		if !found {
			t.Fatal("precise role preflight summary missing or cross-bound")
		}
	}
	if store.writes != 0 {
		t.Fatal("diagnosis performed a preflight")
	}
}

type diagnosisPreflightStore struct {
	records map[string]core.ControlledPreflight
	writes  int
}

func (s *diagnosisPreflightStore) RecordClearDevControlledPreflight(context.Context, core.ControlledPreflight) error {
	s.writes++
	return nil
}
func (s *diagnosisPreflightStore) GetLatestClearDevControlledPreflight(context.Context, string) (core.ControlledPreflight, bool, error) {
	return core.ControlledPreflight{}, false, nil
}
func (s *diagnosisPreflightStore) GetLatestClearDevControlledPreflightForBinding(_ context.Context, id string) (core.ControlledPreflight, bool, error) {
	record, found := s.records[id]
	return record, found, nil
}

func TestWorkflowBlockerDiagnosisShowsProductDiscussionLimit(t *testing.T) {
	replies := make([]string, core.ProductMaxDiscussions)
	for i := range replies {
		replies[i] = productDiscussReply()
	}
	s, h, _ := newProductTestService(t, replies...)
	view := createTestProduct(t, s)
	for i := 1; i < core.ProductMaxDiscussions; i++ {
		var err error
		view, err = s.SubmitProductDiscussion(context.Background(), view.Goal.ID, ProductDiscussionInput{RequestID: fmt.Sprintf("diagnosis-round-%d", i), ExpectedPreviousID: view.Discussions[len(view.Discussions)-1].ID, Message: "Continue discussing the original goal."})
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := len(h.relays)
	recovery, err := s.GetWorkflowRecovery(context.Background(), view.Goal.ID)
	if err != nil || recovery.Diagnosis == nil || !recovery.Diagnosis.Current {
		t.Fatalf("diagnosis unavailable: %+v %v", recovery.Diagnosis, err)
	}
	found := false
	for _, issue := range recovery.Diagnosis.Issues {
		found = found || issue.ReasonCode == "PRODUCT_DISCUSSION_LIMIT_REACHED" && issue.Category == "BUDGET"
	}
	if !found || len(h.relays) != calls {
		t.Fatal("product-only limit lost or diagnosis sent a model message")
	}
}
