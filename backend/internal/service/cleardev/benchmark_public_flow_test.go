package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestBenchmarkFacilityG3PublicInputUsesProductionPlanningBeforeExecution(t *testing.T) {
	store, harness, ids, clock, _ := newComplexFixture(t)
	harness.benchmarkSequentialCandidates = true
	harness.inspectionPaths = []ports.ClearDevDiffPath{{Status: "M", Path: "src/email.js"}}
	harness.complexPlanMutator = func(plan *core.ComplexEngineeringPlanResult) {
		for index := range plan.Tasks {
			plan.Tasks[index].WritePaths = []string{"src/email.js"}
			plan.Tasks[index].RequiredCheckIDs = []string{"CHK-DEV-ALL"}
		}
		plan.IntegrationCheckIDs = []string{"CHK-DEV-ALL"}
	}
	manifest := benchmarkBackendManifestForTest("/tmp/s04-project", core.BenchmarkGroupG3, core.BenchmarkModeStandardOnly)
	service := New(Deps{
		ParseCorrections:           store,
		AgentAttempts:              store,
		ControlledPreflights:       store,
		ControlledPreflightChecker: alwaysPassControlledPreflight{},
		ProgressExplanations:       store,
		Workspace:                  gitWorkspaceObserver{},
		RecoverAgentSession:        func(context.Context, domain.SessionID) error { return errors.New("unexpected session recovery") },
		Facts:                      store, BenchmarkFacts: store, BenchmarkManifest: &manifest,
		StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store, DirectionFacts: store, HumanDecisions: store,
		AO: store, Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		Human: allowStandardHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Second, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})

	view := createComplexUntilClarification(t, service)
	if binding, found, err := store.GetClearDevBenchmarkBinding(context.Background(), view.Requirement.ID); err != nil || !found || binding.Group != core.BenchmarkGroupG3 {
		t.Fatalf("public create did not atomically bind G3: found=%v err=%v binding=%+v", found, err, binding)
	}
	view = answerComplexQuestions(t, service, view)
	if view.ComplexPlanning == nil || len(view.ComplexPlanning.Plans) != 0 || view.ComplexPlanning.Phase != core.ComplexPlanningAwaitingConfirmation {
		t.Fatalf("planning advanced before human confirmation: %#v", view.ComplexPlanning)
	}
	if err := service.ConfirmRequirementVersion(context.Background(), view.RequirementVersions[0].ID); err != nil {
		t.Fatal(err)
	}
	view = mustGetComplex(t, service, view.Requirement.ID)
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved || len(view.ComplexPlanning.Plans) != 1 || len(view.ComplexPlanning.Reviews) != 1 {
		t.Fatalf("production planning did not reach one approved plan: %#v", view.ComplexPlanning)
	}
	var plan core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(view.ComplexPlanning.Plans[0].PlanJSON), &plan); err != nil {
		t.Fatal(err)
	}
	for _, task := range plan.Tasks {
		if len(task.RequiredCheckIDs) != 1 || task.RequiredCheckIDs[0] != "CHK-DEV-ALL" {
			t.Fatalf("public production planner lost benchmark check binding: %#v", task.RequiredCheckIDs)
		}
	}
	if len(plan.IntegrationCheckIDs) != 1 || plan.IntegrationCheckIDs[0] != "CHK-DEV-ALL" {
		t.Fatalf("production plan integration checks = %#v", plan.IntegrationCheckIDs)
	}

	completed, err := service.StartComplexStandardExecution(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	runID := ""
	for attempt := 0; attempt < 8 && (completed.ComplexExecution == nil || completed.ComplexExecution.Phase != core.ComplexExecutionCompleted); attempt++ {
		if completed.ComplexExecution != nil {
			if runID == "" {
				runID = completed.ComplexExecution.Run.ID
			} else if completed.ComplexExecution.Run.ID != runID {
				t.Fatalf("resume changed execution run %s -> %s", runID, completed.ComplexExecution.Run.ID)
			}
		}
		if err := service.ResumeComplexStandardExecutions(context.Background()); err != nil {
			t.Fatal(err)
		}
		completed = mustGetComplex(t, service, view.Requirement.ID)
	}
	if completed.ComplexExecution == nil || completed.ComplexExecution.Phase != core.ComplexExecutionCompleted || completed.ComplexExecution.Run.Mode != core.WorkModeStandard || completed.ComplexExecution.Run.FixedBuilderCount != 1 {
		t.Fatalf("G3 public-input execution did not complete STANDARD/1: %#v", completed.ComplexExecution)
	}
	for _, spec := range completed.ComplexExecution.CheckSpecs {
		if spec.Kind != core.CandidateCheckScope && !strings.HasPrefix(spec.CheckID, "CHK-DEV-") {
			t.Fatalf("public-input G3 execution carried an old check spec: %#v", spec)
		}
	}
	if completed.ComplexExecution.Integration == nil || len(completed.ComplexExecution.Verifications) != len(plan.Tasks) {
		t.Fatalf("G3 public-input candidate evidence incomplete: integration=%#v verifications=%d tasks=%d", completed.ComplexExecution.Integration, len(completed.ComplexExecution.Verifications), len(plan.Tasks))
	}
}

func benchmarkBackendManifestForTest(projectRoot, group string, policy core.BenchmarkModePolicy) core.BenchmarkBackendManifest {
	return core.BenchmarkBackendManifest{
		SchemaVersion: 1, Kind: core.BenchmarkBackendManifestKind, Purpose: core.BenchmarkPurposeOffline, FacilityVersion: core.BenchmarkFacilityVersionV1,
		RepositoryCommit: strings.Repeat("a", 40), FacilityCommit: strings.Repeat("b", 40), ProtocolCommit: strings.Repeat("c", 40), MaterialCommit: strings.Repeat("d", 40),
		MaterialSHA256: strings.Repeat("e", 64), PublicInputSHA256: strings.Repeat("f", 64), PlanUnitID: "DEV-R1-T3-" + group, Scene: 1,
		Group: group, Policy: policy, CheckProfile: core.BenchmarkCheckProfileNodeTS, CheckPrefix: "CHK-DEV", ProjectRoot: projectRoot, IsolationRoot: "/tmp",
		Path: "/tmp/cleardev-benchmark-" + strings.ToLower(group) + ".json", ManifestSHA256: strings.Repeat("1", 64),
	}
}
