package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// Opt-in bridge for test/cleardev-recovery-desktop.mjs. This code is linked only
// into the test binary, never the daemon. Each case has an isolated real SQLite
// store and real recovery service. Provider/Git/checker responses are doubles;
// the native Electron renderer calls the actual GET/POST recovery methods.
type recoveryDesktopCase struct {
	Name            string `json:"name"`
	ProjectID       string `json:"projectId"`
	RequirementID   string `json:"requirementId"`
	ProductID       string `json:"productId,omitempty"`
	ReadOnly        bool   `json:"readOnly,omitempty"`
	ExpectedHistory int    `json:"expectedHistory,omitempty"`
	service         *Service
	calls           func() int
	beforeCalls     int
	advance         func() error
	verify          func() error
	plannerRequests []SubmitPlannerClarificationsInput
	requests        []WorkflowRecoveryInput
}

// This extra native case models the user's empty-summary stop using a real
// recovery transaction and an explicitly unavailable test-native session. The
// desktop is allowed to inspect it only, never to assert that it is repaired.
func unsentBuilderDiagnosisDesktopCase(t *testing.T) *recoveryDesktopCase {
	t.Helper()
	ctx := context.Background()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EMPTY")
	h := &workflowBlockedBuilderHarness{projectExecutionFlowHarness: attachProjectFlow(f, preparer), block: true}
	f.s.chat = h
	id := child.Requirement.ID
	if _, err := f.s.StartProjectExecution(ctx, id, admission); err != nil {
		t.Fatal(err)
	}
	before := stoppedWorkflow(t, f, id)
	binding, _ := complexExecutionBindingByID(before, before.Run.BuilderRoleBindingID)
	record, _, err := f.store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Activity.State = domain.ActivityExited
	record.Metadata.ProviderConversationID = "explicit-native-diagnosis-fixture"
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}
	restores := 0
	f.s.restoreOriginalAgentSession = func(context.Context, domain.SessionID) (string, error) {
		restores++
		return "", errors.New("explicit test session restore unavailable")
	}
	if _, err := f.s.RequestWorkflowRecovery(ctx, id, WorkflowRecoveryInput{RequestID: "diagnosis-unsent-fixture", ExecutionRunID: before.Run.ID, Action: core.RecoveryContinueBuilder, TargetID: before.Dispatches[0].ID, Supplement: "Keep the original work."}); err != nil {
		t.Fatal(err)
	}
	stopped := stoppedWorkflow(t, f, id)
	initialRestores := restores
	return &recoveryDesktopCase{Name: "unsent", ProjectID: "notes-project", RequirementID: id, service: f.s, ReadOnly: true,
		calls: func() int { return len(h.relays) }, beforeCalls: len(h.relays),
		verify: func() error {
			after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stopped, after) || restores != initialRestores {
				return errors.New("native diagnosis changed original work or restored the session")
			}
			return nil
		},
	}
}

func plannerAnswerDesktopCase(t *testing.T, registered bool) *recoveryDesktopCase {
	t.Helper()
	ctx := context.Background()
	f, before := plannerAnswerFixture(t)
	name := "planner"
	calls := len(f.h.relays)
	if registered {
		name = "plannerRegistered"
		f.s.runBackground = func(func()) {}
		input := plannerAnswerInput(before)
		input.RequestID = "native-registered-planner-answer"
		if _, err := f.s.SubmitPlannerClarifications(ctx, before.Requirement.ID, input); err != nil {
			t.Fatal(err)
		}
		if err := f.store.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		f.store, err = sqlite.Open(f.dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.store.Close() })
		f.service()
	}
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, before.RequirementVersions[0]))
	return &recoveryDesktopCase{Name: name, ProjectID: "notes-project", RequirementID: before.Requirement.ID, service: f.s, calls: func() int { return len(f.h.relays) }, beforeCalls: calls, verify: func() error {
		after, err := f.s.GetRequirement(ctx, before.Requirement.ID)
		if err != nil {
			return err
		}
		if after.ComplexPlanning.Phase != core.ComplexPlanningProjectPlanned || after.ComplexExecution != nil || len(after.ComplexPlanning.PlannerAnswerHistory) != 1 || len(f.h.relays) != calls+1 || !reflect.DeepEqual(before.RequirementVersions, after.RequirementVersions) || !reflect.DeepEqual(before.ComplexPlanning.Plans[0], after.ComplexPlanning.Plans[0]) {
			return errors.New("native Planner answer changed original facts, repeated sends or authorized execution")
		}
		return nil
	}}
}

func TestRecoveryPanelDesktopBridge(t *testing.T) {
	root, token := os.Getenv("CLEARDEV_RECOVERY_DESKTOP_DIR"), os.Getenv("CLEARDEV_RECOVERY_DESKTOP_TOKEN")
	if root == "" {
		t.Skip("opt-in real Electron recovery check")
	}
	if !filepath.IsAbs(root) || len(token) < 32 {
		t.Fatal("isolated absolute directory and test-only capability required")
	}
	ctx := context.Background()
	cases := make([]*recoveryDesktopCase, 0, 4)
	for _, name := range []string{"discussion", "compilation", "registered"} {
		f := newPlanningContinuationFixture(t, name != "discussion")
		before, _, err := f.store.GetClearDevProduct(ctx, f.product)
		if err != nil {
			t.Fatal(err)
		}
		attempts, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
		if err != nil {
			t.Fatal(err)
		}
		if name == "registered" {
			f.s.runBackground = func(func()) {}
			if _, err := f.s.RequestWorkflowRecovery(ctx, f.id, f.input()); err != nil {
				t.Fatal(err)
			}
			// A new service instance models resuming an existing registration,
			// without changing its persisted identity or manufacturing a new one.
			f.s = planningContinuationService(f.store, f.h, f.s.newID, f.s.now)
		}
		f.h.fail = false
		f.h.replies = append(f.h.replies, f.reply)
		item := &recoveryDesktopCase{Name: name, ProjectID: "s04-project", RequirementID: f.id, ProductID: f.product, service: f.s,
			calls: func() int { return len(f.h.relays) }, beforeCalls: len(f.h.relays)}
		item.verify = func() error {
			after, _, err := f.store.GetClearDevProduct(ctx, f.product)
			if err != nil {
				return err
			}
			all, err := f.store.ListClearDevAgentStepAttempts(ctx, f.id)
			if err != nil {
				return err
			}
			if len(all) != 2 || !reflect.DeepEqual(attempts[0], all[0]) || len(before.Discussions) != len(after.Discussions) || before.Discussions[0].UserMessage != after.Discussions[0].UserMessage || before.Discussions[0].ID != after.Discussions[0].ID {
				return fmt.Errorf("%s lost its original input or attempt", name)
			}
			if _, exists, err := f.store.GetClearDevComplexExecution(ctx, f.id); err != nil {
				return err
			} else if exists {
				return errors.New("planning recovery must not start execution")
			}
			if item.calls() != item.beforeCalls+1 {
				return fmt.Errorf("%s made %d sends, want exactly one", name, item.calls()-item.beforeCalls)
			}
			return nil
		}
		cases = append(cases, item)
	}
	f, h, id, before := invalidBuilderWorkflowFixture(t)
	builder := &recoveryDesktopCase{Name: "builder", ProjectID: "notes-project", RequirementID: id, service: f.s,
		calls: func() int { return len(h.relays) }, beforeCalls: len(h.relays)}
	builder.advance = func() error {
		for range 120 {
			state, _, err := f.store.GetClearDevComplexExecution(ctx, id)
			if err != nil {
				return err
			}
			if state.Run.CompletedAt != nil {
				return nil
			}
			_, _, err = f.s.advanceComplexStandardExecution(ctx, id)
			if err != nil && !errors.Is(err, errComplexExecutionStopped) {
				return err
			}
		}
		return errors.New("test Builder continuation did not finish")
	}
	builder.verify = func() error {
		after, _, err := f.store.GetClearDevComplexExecution(ctx, id)
		if err != nil {
			return err
		}
		if len(after.Dispatches) != 2 || !reflect.DeepEqual(before.Dispatches[0], after.Dispatches[0]) || after.Run.CompletedAt == nil || len(after.WorkflowRecoveries) != 1 {
			return errors.New("Builder recovery did not preserve old work and finish its test flow")
		}
		return nil
	}
	cases = append(cases, builder)
	cases = append(cases, unsentBuilderDiagnosisDesktopCase(t), builderSessionRecheckDesktopCase(t), newBuilderSessionRecheckDesktopCase(t, true))
	active := cases[0]
	cases = append(cases, plannerAnswerDesktopCase(t, false), plannerAnswerDesktopCase(t, true))
	var mu sync.Mutex
	finished := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-ClearDev-Desktop-Test") != token {
			http.Error(w, "test capability required", http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		var value any
		var err error
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/fixture/select/"):
			name := strings.TrimPrefix(r.URL.Path, "/fixture/select/")
			found := false
			for _, item := range cases {
				if item.Name == name {
					active, found = item, true
				}
			}
			if !found {
				http.NotFound(w, r)
				return
			}
			value = active
		case r.Method == http.MethodPost && r.URL.Path == "/fixture/finish":
			value = map[string]bool{"finished": true}
			defer once.Do(func() { close(finished) })
		case r.Method == http.MethodGet && r.URL.Path == "/fixture/evidence":
			receipt, readErr := active.service.GetWorkflowRecovery(ctx, active.RequirementID)
			err = readErr
			requirement, reqErr := active.service.GetRequirement(ctx, active.RequirementID)
			if reqErr != nil {
				err = reqErr
			}
			value = map[string]any{"case": active.Name, "recovery": receipt, "requirement": requirement, "plannerRequests": active.plannerRequests, "requests": active.requests, "newTestProviderSends": active.calls() - active.beforeCalls}

		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/planner-clarifications") && strings.HasPrefix(active.Name, "planner"):
			var input SubmitPlannerClarificationsInput
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
			decoder.DisallowUnknownFields()
			err = decoder.Decode(&input)
			if err == nil && decoder.Decode(new(any)) != io.EOF {
				err = errors.New("trailing Planner request")
			}
			if err == nil {
				active.plannerRequests = append(active.plannerRequests, input)
				value, err = active.service.SubmitPlannerClarifications(ctx, active.RequirementID, input)
			}
		case strings.HasSuffix(r.URL.Path, "/recoveries"):
			rid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/cleardev/requirements/"), "/recoveries")
			if r.Method == http.MethodGet {
				value, err = active.service.GetWorkflowRecovery(ctx, rid)
			} else if r.Method == http.MethodPost && rid == active.RequirementID && !active.ReadOnly {
				var input WorkflowRecoveryInput
				decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
				decoder.DisallowUnknownFields()
				err = decoder.Decode(&input)
				if err == nil && decoder.Decode(new(any)) != io.EOF {
					err = errors.New("trailing test request data")
				}
				if err == nil {
					active.requests = append(active.requests, input)
					value, err = active.service.RequestWorkflowRecovery(ctx, rid, input)
				}
				if err == nil && active.advance != nil {
					err = active.advance()
					if err == nil {
						value, err = active.service.GetWorkflowRecovery(ctx, rid)
					}
				}
			} else {
				http.Error(w, "unregistered test target or method", http.StatusForbidden)
				return
			}
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/products"):
			project := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/cleardev/projects/"), "/products")
			value, err = active.service.ListProductGoals(ctx, project)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/progress"):
			project := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/cleardev/projects/"), "/progress")
			value, err = active.service.ListProjectProgress(ctx, project)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/cleardev/requirements/"):
			rid := strings.TrimPrefix(r.URL.Path, "/api/v1/cleardev/requirements/")
			value, err = active.service.GetRequirement(ctx, rid)
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusConflict)
			value = map[string]any{"error": map[string]string{"code": "DESKTOP_FIXTURE_REFUSED", "message": err.Error()}}
		}
		if encodeErr := json.NewEncoder(w).Encode(value); encodeErr != nil {
			t.Logf("test response disconnected: %v", encodeErr)
		}
	}))
	defer server.Close()
	manifest, err := json.Marshal(map[string]any{"endpoint": server.URL, "cases": cases, "realSQLite": true, "realProvider": false})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bridge.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Minute):
		t.Fatal("native desktop check did not finish within its bounded window")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, item := range cases {
		if err := item.verify(); err != nil {
			t.Error(err)
		}
		if strings.HasPrefix(item.Name, "planner") {
			if len(item.plannerRequests) < 1 {
				t.Error("native Planner did not submit an answer")
			}
			view, err := item.service.GetRequirement(ctx, item.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.ComplexPlanning.PlannerAnswerHistory) != 1 {
				t.Fatal("native Planner answer missing")
			}
			answer := view.ComplexPlanning.PlannerAnswerHistory[0]
			for _, request := range item.plannerRequests {
				if request.RequestID != answer.RequestID || request.PlanID != answer.PlanID || request.PlanSHA256 != answer.PlanSHA256 || !reflect.DeepEqual(request.Answers, answer.Answers) {
					t.Error("native Planner request lost immutable registration")
				}
			}
			continue
		}
		if item.ReadOnly {
			if len(item.requests) != 0 || item.calls() != item.beforeCalls {
				t.Errorf("%s diagnosis read unexpectedly caused an action", item.Name)
			}
			continue
		}
		view, err := item.service.GetWorkflowRecovery(ctx, item.RequirementID)
		expected := item.ExpectedHistory
		if expected == 0 {
			expected = 1
		}
		if err != nil || len(view.History) != expected || len(item.requests) < 1 {
			t.Errorf("%s has no exact saved UI recovery: %v", item.Name, err)
		}
		for _, request := range item.requests {
			matched := false
			for _, history := range view.History {
				matched = matched || request.RequestID == history.ID && request.TargetID == history.TargetID && request.Supplement == history.Supplement
			}
			if !matched {
				t.Errorf("%s UI changed a persisted recovery request", item.Name)
			}
		}
	}
}
