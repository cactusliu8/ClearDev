package cleardev_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

func TestQuickExecutionCompletesFollowUpFromEmptyBody(t *testing.T) {
	_, harness, service, prep := newComplexParallelService(t, false)
	s07, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	assertComplexParallelCompleted(t, s07, harness)
	beforeBuilders, beforeReviewers := countComplexSpawns(harness)
	harness.mu.Lock()
	preflightsBefore := len(harness.preflightRequests)
	harness.mu.Unlock()

	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	assertQuickExecutionCompleted(t, view, s07)
	builders, reviewers := countComplexSpawns(harness)
	if builders != beforeBuilders+1 || reviewers != beforeReviewers {
		t.Fatalf("QUICK spawn delta builders=%d reviewers=%d, want +1 builder and no Reviewer", builders-beforeBuilders, reviewers-beforeReviewers)
	}
	if harness.prepareCalls == 0 {
		t.Fatal("QUICK Builder did not switch onto the S07 integration SHA")
	}
	harness.mu.Lock()
	if len(harness.preflightRequests) != preflightsBefore+1 {
		harness.mu.Unlock()
		t.Fatalf("QUICK check preflights = %d, want one new preflight", len(harness.preflightRequests)-preflightsBefore)
	}
	quickPreflight := harness.preflightRequests[len(harness.preflightRequests)-1]
	quickReleased := false
	for _, runID := range harness.checkReleaseIDs {
		quickReleased = quickReleased || runID == quickPreflight.RunID
	}
	harness.mu.Unlock()
	if quickPreflight.CandidateSHA != s07.ComplexExecution.Integration.CandidateCommitSHA || quickPreflight.RunID == "" {
		t.Fatalf("QUICK check preflight = %#v", quickPreflight)
	}
	if !quickReleased {
		t.Fatalf("completed QUICK flow did not release %q", quickPreflight.RunID)
	}
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-review-") && strings.Contains(cfg.CreationIdempotencyKey, "quick") {
			t.Fatalf("QUICK spawned a Reviewer: %#v", cfg)
		}
		if strings.Contains(cfg.CreationIdempotencyKey, "planner") && strings.Contains(cfg.CreationIdempotencyKey, "quick") {
			t.Fatalf("QUICK spawned a Planner: %#v", cfg)
		}
	}

	retried, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.QuickExecution == nil || retried.QuickExecution.Run.ID != view.QuickExecution.Run.ID || retried.QuickExecution.Task == nil || retried.QuickExecution.Task.DevelopmentTaskID != view.QuickExecution.Task.DevelopmentTaskID {
		t.Fatalf("idempotent QUICK retry changed the run: %#v", retried.QuickExecution)
	}
}

func TestQuickCheckPreflightStopsBeforeBuilderTask(t *testing.T) {
	_, harness, service, prep := newComplexParallelService(t, false)
	campaign, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	assertComplexParallelCompleted(t, campaign, harness)
	harness.preflightErr = errors.New("offline dependency environment is unavailable")

	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.QuickExecution == nil || view.QuickExecution.Phase != core.ComplexExecutionBlocked || view.QuickExecution.Task == nil || len(view.QuickExecution.Dispatches) != 0 || len(view.QuickExecution.CheckRuns) != 0 || view.QuickExecution.Integration != nil {
		t.Fatalf("QUICK preflight stop = %#v", view.QuickExecution)
	}
	failed := 0
	for _, binding := range view.QuickExecution.RoleBindings {
		if binding.Role == core.StandardRoleBuilder && binding.Status == core.RoleBindingStatusFailed && binding.ReasonCode == core.ReasonCode("CHECKER_UNAVAILABLE") {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed QUICK Builder bindings = %d, want one", failed)
	}
	harness.mu.Lock()
	defer harness.mu.Unlock()
	for _, relay := range harness.relays {
		if strings.Contains(relay.prompt, `"kind":"BUILDER_RESULT"`) && strings.Contains(relay.clientMessageID, "quick") {
			t.Fatalf("QUICK Builder received a task after failed preflight: %#v", relay)
		}
	}
}

func TestMailBenchV1CampaignThenQuickProductionFlow(t *testing.T) {
	_, harness, service, prep := newMailBenchQuickService(t)
	campaign, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if campaign.ComplexExecution == nil || campaign.ComplexExecution.Phase != core.ComplexExecutionCompleted || campaign.ComplexExecution.Run.RequirementVersionID != prep.V1ID {
		t.Fatalf("v1 Campaign completion = %#v", campaign.ComplexExecution)
	}
	if campaign.ComplexExecution.Run.Mode != core.WorkModeParallel || campaign.ComplexExecution.Run.TaskSetVersion != core.ComplexStandardTaskSetVersion {
		t.Fatalf("v1 Campaign run = %#v", campaign.ComplexExecution.Run)
	}
	wantSourceChecks := map[string]core.CandidateCheckKind{
		"demo-frontend":    core.CandidateCheckRequired,
		"demo-backend":     core.CandidateCheckRequired,
		"demo-database":    core.CandidateCheckRequired,
		"demo-api":         core.CandidateCheckRequired,
		"demo-integration": core.CandidateCheckIntegration,
	}
	for name, kind := range wantSourceChecks {
		found := false
		for _, spec := range campaign.ComplexExecution.CheckSpecs {
			if spec.CheckID == name && spec.Kind == kind {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("v1 Campaign missing source check %s", name)
		}
	}
	for _, run := range campaign.ComplexExecution.CheckRuns {
		if run.Kind != core.CandidateCheckScope && (run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass) {
			t.Fatalf("v1 Campaign check did not pass: %#v", run)
		}
	}
	beforeBuilders, beforeReviewers := countComplexSpawns(harness)
	quickView, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	quick := quickView.QuickExecution
	if quick == nil || quick.Phase != core.ComplexExecutionCompleted || quick.Run.CompletedAt == nil || quick.Integration == nil || quick.Task == nil {
		t.Fatalf("MailBench QUICK completion = %#v", quick)
	}
	if quick.Run.SourceExecutionRunID != campaign.ComplexExecution.Run.ID || quick.Run.IntegrationBaseSHA != campaign.ComplexExecution.Integration.CandidateCommitSHA ||
		quick.Run.ExpectedTaskSetVersion != campaign.ComplexExecution.Run.TaskSetVersion || quick.Run.TaskSetVersion != campaign.ComplexExecution.Run.TaskSetVersion+1 {
		t.Fatalf("MailBench QUICK source binding = %#v", quick.Run)
	}
	if quick.Task.TaskKey != "campaign-history" || quick.Task.Status != core.DevelopmentTaskStatusDone {
		t.Fatalf("MailBench QUICK task = %#v", quick.Task)
	}
	sourceByKey := map[string]core.ComplexExecutionCheckSpecFact{}
	var sourceTaskID string
	for _, task := range campaign.ComplexExecution.Tasks {
		if task.TaskKey == "campaign-history" {
			sourceTaskID = task.ID
		}
	}
	for _, spec := range campaign.ComplexExecution.CheckSpecs {
		if (spec.Kind == core.CandidateCheckRequired && spec.ComplexExecutionTaskID == sourceTaskID) || spec.Kind == core.CandidateCheckIntegration {
			sourceByKey[string(spec.Kind)+":"+spec.CheckID] = spec
		}
	}
	inherited := 0
	for _, spec := range quick.CheckSpecs {
		if spec.Kind == core.CandidateCheckScope {
			continue
		}
		source, ok := sourceByKey[string(spec.Kind)+":"+spec.CheckID]
		if !ok || source.CheckSpecSHA256 != spec.CheckSpecSHA256 || source.TimeoutSeconds != spec.TimeoutSeconds || strings.Join(source.Argv, "\x00") != strings.Join(spec.Argv, "\x00") {
			t.Fatalf("MailBench QUICK check was not inherited: quick=%#v source=%#v", spec, source)
		}
		inherited++
	}
	if inherited != 2 {
		t.Fatalf("MailBench QUICK inherited %d checks, want one required and one integration", inherited)
	}
	for _, run := range quick.CheckRuns {
		if run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass {
			t.Fatalf("MailBench QUICK check did not pass: %#v", run)
		}
	}
	builders, reviewers := countComplexSpawns(harness)
	if builders != beforeBuilders+1 || reviewers != beforeReviewers {
		t.Fatalf("MailBench QUICK spawn delta builders=%d reviewers=%d", builders-beforeBuilders, reviewers-beforeReviewers)
	}
	if quickView.OverallProgress.TaskSetVersion == nil || *quickView.OverallProgress.TaskSetVersion != campaign.ComplexExecution.Run.TaskSetVersion+1 {
		t.Fatalf("MailBench QUICK task set = %#v", quickView.OverallProgress.TaskSetVersion)
	}
}

func TestMailBenchV1IncompleteCampaignDoesNotStartQuick(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		setup func(*parallelAgentHarness)
	}{
		{
			name: "required-check-failed",
			setup: func(harness *parallelAgentHarness) {
				harness.checkFailure = true
			},
		},
		{
			name: "check-environment-unavailable",
			setup: func(harness *parallelAgentHarness) {
				harness.preflightErr = errors.New("offline check environment is incomplete")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, harness, service, prep := newMailBenchQuickService(t)
			testCase.setup(harness)
			first, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if first.ComplexExecution == nil || first.ComplexExecution.Phase == core.ComplexExecutionCompleted ||
				first.QuickExecution != nil || first.OverallProgress.Phase == core.OverallPhaseCompleted {
				t.Fatalf("incomplete Campaign was treated as complete: complex=%#v quick=%#v overall=%#v", first.ComplexExecution, first.QuickExecution, first.OverallProgress)
			}

			second, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if second.QuickExecution != nil || second.OverallProgress.Phase == core.OverallPhaseCompleted {
				t.Fatalf("incomplete Campaign started QUICK: quick=%#v overall=%#v", second.QuickExecution, second.OverallProgress)
			}
			harness.mu.Lock()
			defer harness.mu.Unlock()
			for _, relay := range harness.relays {
				if strings.Contains(relay.prompt, `"kind":"COMPLEX_QUICK_TASK_REQUEST"`) {
					t.Fatalf("incomplete Campaign sent QUICK request: %#v", relay)
				}
			}
		})
	}
}

func TestQuickExecutionRejectsUnsafeFollowUpWithoutBuilder(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"database", "migrations/001_init.sql"},
		{"dependency", "package.json"},
		{"config", "vite.config.ts"},
		{"permission", "CODEOWNERS"},
		{"auth", "src/oauth.js"},
		{"network", "Dockerfile"},
		{"ci", ".github/workflows/ci.yml"},
		{"repo", ".gitignore"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, harness, service, prep := newComplexParallelService(t, false)
			s07, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if s07.ComplexExecution == nil || s07.ComplexExecution.Phase != core.ComplexExecutionCompleted {
				t.Fatalf("S07 setup = %#v", s07.ComplexExecution)
			}
			before := len(harness.spawnConfigs)
			path := test.path
			harness.quickOverride = func(raw string) string {
				var payload map[string]any
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Fatal(err)
				}
				payload["writePaths"] = []any{path}
				payload["testPaths"] = []any{}
				encoded, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				return string(encoded)
			}
			view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if view.QuickExecution == nil || view.QuickExecution.Run.Mode != core.WorkModeQuick || view.QuickExecution.Phase != core.ComplexExecutionBlocked || view.QuickExecution.PhaseReason != core.ReasonQuickSensitivePath {
				t.Fatalf("%s QUICK phase = %#v", test.name, view.QuickExecution)
			}
			if view.QuickExecution.Task != nil {
				t.Fatalf("%s QUICK created a task: %#v", test.name, view.QuickExecution.Task)
			}
			if view.ComplexExecution == nil || view.ComplexExecution.Run.ID != s07.ComplexExecution.Run.ID || view.ComplexExecution.Run.Mode != core.WorkModeParallel {
				t.Fatalf("%s fell back to another mode: %#v", test.name, view.ComplexExecution)
			}
			if len(harness.spawnConfigs) != before {
				t.Fatalf("%s QUICK spawned extra sessions: before=%d after=%d", test.name, before, len(harness.spawnConfigs))
			}
			if view.OverallProgress.TaskSetVersion == nil || *view.OverallProgress.TaskSetVersion != core.ComplexStandardTaskSetVersion {
				t.Fatalf("%s QUICK changed the task set: %#v", test.name, view.OverallProgress.TaskSetVersion)
			}
		})
	}
}

func TestQuickExecutionNeedsHumanDoesNotCreateTask(t *testing.T) {
	_, harness, service, prep := newComplexParallelService(t, false)
	if _, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID); err != nil {
		t.Fatal(err)
	}
	harness.quickOverride = func(raw string) string {
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatal(err)
		}
		payload["decision"] = "NEEDS_HUMAN"
		payload["reasonCode"] = "HUMAN_DECISION_REQUIRED"
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	view, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	if view.QuickExecution == nil || view.QuickExecution.Phase != core.ComplexExecutionNeedsHuman || view.QuickExecution.Task != nil {
		t.Fatalf("NEEDS_HUMAN QUICK = %#v", view.QuickExecution)
	}
}

func TestQuickExecutionConcurrentSecondRequestStaysSingleTask(t *testing.T) {
	_, _, service, prep := newComplexParallelService(t, false)
	if _, err := service.StartComplexStandardExecution(context.Background(), prep.RequirementID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	views := make([]cleardevsvc.RequirementView, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			views[index], errs[index] = service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
		}(i)
	}
	wg.Wait()
	ids := map[string]struct{}{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent QUICK start %d: %v", i, err)
		}
		if views[i].QuickExecution == nil || views[i].QuickExecution.Task == nil {
			views[i], errs[i] = service.StartComplexStandardExecution(context.Background(), prep.RequirementID)
			err = errs[i]
		}
		if err != nil {
			t.Fatalf("concurrent QUICK start %d: %v", i, err)
		}
		if views[i].QuickExecution == nil || views[i].QuickExecution.Task == nil {
			t.Fatalf("concurrent QUICK %d missing task: %#v", i, views[i].QuickExecution)
		}
		ids[views[i].QuickExecution.Run.ID] = struct{}{}
		ids[views[i].QuickExecution.Task.DevelopmentTaskID] = struct{}{}
	}
	if len(ids) != 2 {
		t.Fatalf("concurrent QUICK created extra facts: %#v", ids)
	}
}

func assertQuickExecutionCompleted(t *testing.T, view, s07 cleardevsvc.RequirementView, extra ...any) {
	t.Helper()
	_ = extra
	quick := view.QuickExecution
	if quick == nil || quick.Phase != core.ComplexExecutionCompleted || quick.Run.CompletedAt == nil || quick.Integration == nil || len(quick.MissingEvidence) != 0 {
		t.Fatalf("QUICK completion = %#v", quick)
	}
	if quick.Run.Mode != core.WorkModeQuick || quick.Run.ModeReason != string(core.ReasonQuickFollowUpApproved) || quick.Run.SourceTaskKey != "deduplicate-email" {
		t.Fatalf("QUICK run = %#v", quick.Run)
	}
	if s07.ComplexExecution == nil || quick.Run.IntegrationBaseSHA != s07.ComplexExecution.Integration.CandidateCommitSHA {
		t.Fatalf("QUICK base = %s, want S07 integration %s", quick.Run.IntegrationBaseSHA, s07.ComplexExecution.Integration.CandidateCommitSHA)
	}
	if quick.Task == nil || quick.Task.TaskKey != "deduplicate-email" || quick.Task.Status != core.DevelopmentTaskStatusDone {
		t.Fatalf("QUICK task = %#v", quick.Task)
	}
	pkg, err := core.ParseComplexQuickExecutionPackage([]byte(quick.Task.ExecutionPackageJSON))
	if err != nil || pkg.Mode != string(core.WorkModeQuick) || pkg.TaskSetVersion != 2 {
		t.Fatalf("QUICK package = %#v err=%v", pkg, err)
	}
	if strings.Join(pkg.WritePaths, ",") != "src/deduplicate.js,test/deduplicate.test.js" {
		t.Fatalf("QUICK write paths = %#v", pkg.WritePaths)
	}
	builders, reviewers, planners := 0, 0, 0
	for _, binding := range quick.RoleBindings {
		switch binding.Role {
		case core.StandardRoleBuilder:
			builders++
		case core.StandardRoleReviewer:
			reviewers++
		case core.StandardRoleEngineeringPlanner:
			planners++
		}
	}
	if builders != 1 || reviewers != 0 || planners != 0 {
		t.Fatalf("QUICK roles builders=%d reviewers=%d planners=%d bindings=%#v", builders, reviewers, planners, quick.RoleBindings)
	}
	if view.OverallProgress.Phase != core.OverallPhaseCompleted || view.OverallProgress.TaskSetVersion == nil || *view.OverallProgress.TaskSetVersion != core.ComplexStandardTaskSetVersion+1 {
		t.Fatalf("QUICK overall = %#v", view.OverallProgress)
	}
	if s07.OverallProgress.TaskSetVersion == nil || *s07.OverallProgress.TaskSetVersion != core.ComplexStandardTaskSetVersion {
		t.Fatalf("S07 overall task set = %#v", s07.OverallProgress.TaskSetVersion)
	}
	if view.ComplexExecution == nil || view.ComplexExecution.Run.ID != s07.ComplexExecution.Run.ID {
		t.Fatalf("S07 execution changed after QUICK: %#v", view.ComplexExecution)
	}
}

func countComplexSpawns(harness *parallelAgentHarness) (builders, reviewers int) {
	for _, cfg := range harness.spawnConfigs {
		if strings.Contains(cfg.Branch, "cleardev-complex-builder-") {
			builders++
		}
		if strings.Contains(cfg.Branch, "cleardev-complex-review-") {
			reviewers++
		}
	}
	return builders, reviewers
}
