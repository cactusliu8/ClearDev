package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// The provider, source observer and environment preparation are test doubles.
// These tests use the real service and migrated SQLite for authorization,
// idempotency and immutable materialization; they are not live delivery proof.
type projectExecutionPreparer struct {
	*projectPlanningAgent
	failure error
	calls   int
}

func (p *projectExecutionPreparer) PrepareProjectExecution(_ context.Context, contract core.ProjectExecutionContract) error {
	p.calls++
	if p.failure != nil {
		return p.failure
	}
	return core.ValidateProjectExecutionContract(contract)
}

func plannedProjectExecutionFixture(t *testing.T, origin string) (*projectPlanningFixture, RequirementView, core.ProjectExecutionAdmission, *projectExecutionPreparer) {
	t.Helper()
	f := newProjectPlanningFixture(t, origin)
	selected := f.choose(t, f.create(t), strings.ToLower(origin))
	_, child := f.prepare(t, selected)
	f.h.replies = append(f.h.replies, genericEngineeringReply(t, child.RequirementVersions[0]))
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if child.ComplexPlanning == nil || len(child.ComplexPlanning.Plans) != 1 || child.ComplexExecution != nil {
		t.Fatal("the fixture did not stop at a saved planning-only plan")
	}
	plan := child.ComplexPlanning.Plans[0]
	admission := core.ProjectExecutionAdmission{
		RequestID: "start-project", PlanID: plan.ID, PlanSHA256: plan.PlanSHA256,
		RequirementSHA256: child.RequirementVersions[0].SHA256, BaseCommitSHA: selected.Selection.BaseCommitSHA,
	}
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	f.s.checks, f.s.finalReviews = preparer, f.store
	// Drive one persisted transition at a time without a fake Builder delivery.
	f.s.runBackground = func(func()) {}
	return f, child, admission, preparer
}

func TestProjectExecutionAdmissionAndMaterialization(t *testing.T) {
	for _, origin := range []string{"EMPTY", "EXISTING", "DISCOVERED"} {
		t.Run(origin, func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, origin)
			ctx := context.Background()
			before := len(f.h.relays)
			if _, err := f.s.StartComplexStandardExecution(ctx, child.Requirement.ID); err == nil {
				t.Fatal("historical start bypassed explicit project admission")
			}
			if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
				t.Fatal(err)
			}
			run, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
			if err != nil || !found || len(run.Tasks) != 0 || preparer.calls != 1 {
				t.Fatalf("admission: %+v %v", run, err)
			}
			contract, project, err := core.ProjectContractFromRun(run.Run)
			if err != nil || !project || contract.BaseCommitSHA != admission.BaseCommitSHA || run.Run.Mode != core.WorkModeStandard || run.Run.FixedBuilderCount != 1 {
				t.Fatalf("contract: %+v %v", contract, err)
			}
			if phase, _ := core.DeriveComplexExecutionPhase(run); phase != core.ComplexExecutionPreparingTasks {
				t.Fatalf("admitted plan must materialize without fictitious Steward approval: %s", phase)
			}
			for _, requestID := range []string{admission.RequestID, "second-click"} {
				retry := admission
				retry.RequestID = requestID
				if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, retry); err != nil {
					t.Fatal(err)
				}
			}
			replayed, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
			if err != nil || replayed.Run.ID != run.Run.ID || preparer.calls != 1 || len(f.h.relays) != before {
				t.Fatal("replay changed the run, repeated preparation or sent a model task")
			}
			progressed, stopped, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID)
			if err != nil || !progressed || stopped {
				t.Fatalf("materialize: changed=%v stopped=%v err=%v", progressed, stopped, err)
			}
			materialized, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
			if err != nil || len(materialized.Tasks) != 1 || len(materialized.CheckSpecs) != 3 {
				t.Fatalf("materialized tasks/checks: %+v %v", materialized, err)
			}
			pkg, err := core.ParseComplexStandardExecutionPackage([]byte(materialized.Tasks[0].ExecutionPackageJSON))
			if err != nil || pkg.SchemaVersion != core.ProjectExecutionProtocolVersion || core.ProjectTaskMatchesRun(pkg, materialized.Run) != nil {
				t.Fatalf("V4 task binding: %+v %v", pkg, err)
			}
			if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
				t.Fatalf("replay after materialization: %v", err)
			}
			if f.h.mailCalls != 0 || len(f.h.checkRequests) != 0 || len(f.h.relays) != before {
				t.Fatal("admission/materialization invoked the mail template or invented a check/development result")
			}
			assertProjectAdmissionHistory(t, f, materialized.Run.ID)
		})
	}
}

func TestProjectGeneratedDependencyLockDoesNotAcquireMailGenerator(t *testing.T) {
	f := newProjectPlanningFixture(t, "EMPTY")
	initial := f.create(t)
	var proposal core.ProductDiscoveryResult
	if err := json.Unmarshal([]byte(genericProjectReply("empty")), &proposal); err != nil {
		t.Fatal(err)
	}
	basis := proposal.Stages[0].ExecutionBasis
	// The shared fixture already includes the declared dependency lock.
	proposal.Stages[0].ExecutionBasis = basis
	raw, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, string(raw))
	selected, err := f.s.SubmitProductDiscussion(context.Background(), initial.Goal.ID, projectChoice(initial, "empty"))
	if err != nil {
		t.Fatal(err)
	}
	_, child := f.prepare(t, selected)
	var plan map[string]any
	if err := json.Unmarshal([]byte(genericEngineeringReply(t, child.RequirementVersions[0])), &plan); err != nil {
		t.Fatal(err)
	}
	task := plan["tasks"].([]any)[0].(map[string]any)
	paths := []any{}
	for _, name := range task["writePaths"].([]any) {
		if name != "package-lock.json" {
			paths = append(paths, name)
		}
	}
	task["writePaths"] = paths
	task["generatedPaths"] = []string{"package-lock.json"}
	task["reviewCriteria"] = append(task["reviewCriteria"].([]any), "The declared dependencies and actual lock entries stay consistent.")
	raw, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	f.h.replies = append(f.h.replies, string(raw))
	applyFakeDesktopDecision(t, f.store, f.s, time.Now, child.Requirement.ID, core.HumanDecisionKindConfirmVersion, core.HumanDecisionApprove)
	child = mustGetComplex(t, f.s, child.Requirement.ID)
	if child.ComplexPlanning == nil || len(child.ComplexPlanning.Plans) != 1 {
		t.Fatalf("generated dependency lock plan was not saved: phase=%s reason=%s", child.ComplexPlanning.Phase, child.TrustedProgress.ReasonCode)
	}
	stored := child.ComplexPlanning.Plans[0]
	admission := core.ProjectExecutionAdmission{RequestID: "start-generated-lock", PlanID: stored.ID, PlanSHA256: stored.PlanSHA256,
		RequirementSHA256: child.RequirementVersions[0].SHA256, BaseCommitSHA: selected.Selection.BaseCommitSHA}
	preparer := &projectExecutionPreparer{projectPlanningAgent: f.h}
	f.s.checks, f.s.finalReviews = preparer, f.store
	f.s.runBackground = func(func()) {}
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	if progressed, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil || !progressed || stopped {
		t.Fatalf("materialize generated lock plan: changed=%v stopped=%v err=%v", progressed, stopped, err)
	}
	execution, _, err := f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	if err != nil || execution.Exception == nil || len(execution.Tasks) != 1 || len(execution.Exception.GeneratedCommands) != 0 {
		t.Fatalf("generic project inherited the mail lock generator: %+v %v", execution.Exception, err)
	}
}

func assertProjectAdmissionHistory(t *testing.T, f *projectPlanningFixture, runID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM cleardev_project_execution_admissions WHERE execution_run_id=?", runID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("admission count=%d err=%v", count, err)
	}
	for _, statement := range []string{
		"DELETE FROM cleardev_project_execution_admissions",
		"UPDATE cleardev_project_execution_admissions SET contract_json='{}'",
		"INSERT OR REPLACE INTO cleardev_project_execution_admissions SELECT * FROM cleardev_project_execution_admissions",
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("admission history accepted %s", statement)
		}
	}
}

func TestProjectExecutionRejectsDriftAndMissingRuntime(t *testing.T) {
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	ctx := context.Background()
	for _, mutate := range []func(*core.ProjectExecutionAdmission){
		func(a *core.ProjectExecutionAdmission) { a.RequestID = "" },
		func(a *core.ProjectExecutionAdmission) { a.PlanID = "another-plan" },
		func(a *core.ProjectExecutionAdmission) { a.PlanSHA256 = strings.Repeat("e", 64) },
		func(a *core.ProjectExecutionAdmission) { a.RequirementSHA256 = strings.Repeat("e", 64) },
		func(a *core.ProjectExecutionAdmission) { a.BaseCommitSHA = forty("b") },
	} {
		request := admission
		mutate(&request)
		if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, request); err == nil {
			t.Fatal("changed explicit binding was admitted")
		}
	}
	f.h.source.BaseCommitSHA = forty("b")
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err == nil {
		t.Fatal("source drift was admitted")
	}
	f.h.source.BaseCommitSHA = forty("a")
	preparer.failure = errors.New("Node/npm checker image is not installed")
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err == nil {
		t.Fatal("missing environment was admitted")
	}
	if _, found, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID); err != nil || found {
		t.Fatal("failed admission left an execution")
	}
	preparer.failure = nil
	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	f.h.source.BaseCommitSHA = forty("b")
	if progressed, _, err := f.s.advanceComplexStandardExecution(ctx, child.Requirement.ID); err == nil || progressed {
		t.Fatal("source drift after admission was materialized")
	}
	run, _, err := f.store.GetClearDevComplexExecution(ctx, child.Requirement.ID)
	if err != nil || len(run.Tasks) != 0 {
		t.Fatal("source drift lost admission history or created tasks")
	}
	assertProjectAdmissionHistory(t, f, run.Run.ID)
}
