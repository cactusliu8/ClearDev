//go:build !windows

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestComplexParallelExecutionWithRealCodexChat(t *testing.T) {
	requireE2E(t)
	imageID := requireStandardE2EPrerequisites(t)
	fixturePath := prepareComplexE2EFixture(t)
	initialSHA := complexExecutionGit(t, fixturePath, "rev-parse", "--verify", "HEAD^{commit}")
	if !fullLowerGitSHA(initialSHA) {
		t.Fatalf("S07 fixture initial commit is not a full Git SHA: %q", initialSHA)
	}
	dataDir := t.TempDir()

	registrationDaemon := startDaemon(t, dataDir)
	projectID := registerStandardE2EProject(t, registrationDaemon, fixturePath)
	setPermissions(t, registrationDaemon, projectID, "auto")
	stewardSessionID := spawn(t, registrationDaemon, map[string]any{
		"projectId": projectID, "kind": "orchestrator", "harness": "codex", "mode": "chat", "prompt": "",
	}).Session.ID
	registrationDaemon.awaitLiveController(stewardSessionID, 90*time.Second)

	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open isolated store for S07 preparer: %v", err)
	}
	prep := cleardevtest.SeedComplexParallelV2WithSessions(t, store, projectID, fixturePath, dataDir, cleardevtest.ComplexStandardPrepSessions{
		StewardSessionID: stewardSessionID,
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close S07 preparer store: %v", err)
	}

	d := registrationDaemon
	var started cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &started)
	if started.ComplexExecution == nil || started.ComplexExecution.Run.RequirementVersionID != prep.V2ID || started.ComplexExecution.Run.PlanID != prep.V2PlanID {
		t.Fatalf("S07 start did not bind the prepared v2 and plan: %#v", started.ComplexExecution)
	}

	completed := awaitComplexExecutionPhase(t, d, prep.RequirementID, core.ComplexExecutionCompleted, 30*time.Minute)
	evidence := assertComplexParallelExecutionCompletion(t, completed, prep, initialSHA, imageID)
	runID := completed.ComplexExecution.Run.ID

	before := complexExecutionFactCounts(completed.ComplexExecution)
	d.stop()
	d = startDaemon(t, dataDir)
	restarted := mustGetComplexE2E(t, d, prep.RequirementID)
	if restarted.ComplexExecution == nil || restarted.ComplexExecution.Run.ID != runID || restarted.ComplexExecution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("S07 restart read changed the completed run: %#v", restarted.ComplexExecution)
	}
	var retried cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &retried)
	if retried.ComplexExecution == nil || retried.ComplexExecution.Run.ID != runID || complexExecutionFactCounts(retried.ComplexExecution) != before {
		t.Fatalf("S07 idempotent retry changed facts: before=%+v after=%+v", before, complexExecutionFactCounts(retried.ComplexExecution))
	}

	evidence["daemonPID"] = d.pgid
	evidence["dataDir"] = dataDir
	evidence["events"] = summarizeRequirementEvents(retried)
	evidenceDir := mustComplexParallelEvidenceDir(t)
	writeJSONFile(t, filepath.Join(evidenceDir, "complex-parallel-execution.json"), evidence)
	t.Logf("S07 complex PARALLEL completed requirement=%s run=%s tasks=%d evidence=%s", prep.RequirementID, runID, len(completed.ComplexExecution.Tasks), evidenceDir)
}

func assertComplexParallelExecutionCompletion(t *testing.T, view cleardevsvc.RequirementView, prep cleardevtest.ComplexParallelPrep, initialSHA, imageID string) map[string]any {
	t.Helper()
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionCompleted || execution.Run.CompletedAt == nil || execution.Integration == nil || len(execution.MissingEvidence) != 0 {
		t.Fatalf("S07 completion read model = %#v", execution)
	}
	if view.OverallProgress.Phase != core.OverallPhaseCompleted || view.OverallProgress.CurrentRequirementVersionID != prep.V2ID {
		t.Fatalf("S07 overall progress = %#v", view.OverallProgress)
	}
	if execution.Run.Mode != core.WorkModeParallel || execution.Run.FixedBuilderCount != 2 || execution.Run.ModeReason != string(core.ReasonParallelPlanApproved) {
		t.Fatalf("S07 mode fact = %#v", execution.Run)
	}

	tasks := append([]core.ComplexExecutionTask(nil), execution.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Ordinal < tasks[j].Ordinal })
	if len(tasks) != 3 || tasks[0].TaskKey != "normalize-email" || tasks[1].TaskKey != "deduplicate-email" || tasks[2].TaskKey != "build-summary" {
		t.Fatalf("S07 task graph = %#v", tasks)
	}
	for _, task := range tasks {
		if task.Status != core.DevelopmentTaskStatusDone {
			t.Fatalf("S07 task %s terminal state = %#v", task.TaskKey, task)
		}
		pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
		if err != nil || pkg.Mode != string(core.WorkModeParallel) {
			t.Fatalf("S07 task %s package mode = %#v err=%v", task.TaskKey, pkg, err)
		}
	}
	if len(execution.Batches) != 2 || len(execution.Compositions) != 2 {
		t.Fatalf("S07 batches/compositions = %d/%d", len(execution.Batches), len(execution.Compositions))
	}

	dispatches := map[string]core.ComplexExecutionDispatch{}
	for _, dispatch := range execution.Dispatches {
		dispatches[dispatch.ComplexExecutionTaskID] = dispatch
	}
	wave0 := execution.Compositions[0]
	summary := dispatches[tasks[2].ID]
	if wave0.OutputCommitSHA == "" || summary.BaseCommitSHA != wave0.OutputCommitSHA {
		t.Fatalf("S07 wave-1 base = %#v composed=%#v", summary, wave0)
	}
	final := execution.Compositions[len(execution.Compositions)-1]
	if execution.Integration.CandidateCommitSHA != final.OutputCommitSHA || !fullLowerGitSHA(final.OutputCommitSHA) {
		t.Fatalf("S07 integration SHA = %#v final=%#v", execution.Integration, final)
	}
	if !complexExecutionHasIntegrationRun(execution.CheckRuns, execution.Integration.CheckRunIDs[0], final.OutputCommitSHA) {
		t.Fatalf("S07 integration check is not bound to composed commit %s", final.OutputCommitSHA)
	}

	roles := map[core.StandardRole][]core.ComplexExecutionRoleBinding{}
	for _, role := range execution.RoleBindings {
		roles[role.Role] = append(roles[role.Role], role)
	}
	if len(roles[core.StandardRoleBuilder]) != 2 || len(roles[core.StandardRoleReviewer]) != 3 {
		t.Fatalf("S07 role counts = %#v", roles)
	}
	assertComplexParallelBuilderOverlap(t, execution.AgentSteps)
	assertNoS06HumanDecisionEvents(t, view.Events, execution.Run.ID)
	for _, event := range view.Events {
		if strings.Contains(string(event.Action), "QUICK") || strings.Contains(string(event.SubjectType), "RECOVERY") || strings.Contains(string(event.SubjectType), "SPECIALIST") {
			t.Fatalf("S07 created a forbidden fact: %#v", event)
		}
	}

	return map[string]any{
		"requirementId":    prep.RequirementID,
		"versionId":        prep.V2ID,
		"planId":           prep.V2PlanID,
		"reviewId":         prep.V2ReviewID,
		"runId":            execution.Run.ID,
		"phase":            execution.Phase,
		"initialBase":      initialSHA,
		"roles":            execution.RoleBindings,
		"agentSteps":       execution.AgentSteps,
		"tasks":            tasks,
		"batches":          execution.Batches,
		"compositions":     execution.Compositions,
		"dispatches":       execution.Dispatches,
		"checks":           execution.CheckRuns,
		"reviews":          execution.Reviews,
		"verifications":    execution.Verifications,
		"integration":      execution.Integration,
		"containerImageId": imageID,
	}
}

func assertComplexParallelBuilderOverlap(t *testing.T, steps []core.AgentStep) {
	t.Helper()
	type interval struct{ start, end time.Time }
	var builder []interval
	for _, step := range steps {
		if step.Kind != core.ComplexExecutionAgentStepBuilderTask || step.SentAt == nil || step.CompletedAt == nil {
			continue
		}
		builder = append(builder, interval{start: *step.SentAt, end: *step.CompletedAt})
	}
	if len(builder) < 2 {
		t.Fatalf("S07 Builder turns = %d, want at least two overlapping wave-0 turns", len(builder))
	}
	overlapped := false
	for i := 0; i < len(builder); i++ {
		for j := i + 1; j < len(builder); j++ {
			if builder[i].start.Before(builder[j].end) && builder[j].start.Before(builder[i].end) {
				overlapped = true
			}
		}
	}
	if !overlapped {
		t.Fatalf("S07 Builder turns did not overlap: %#v", builder)
	}
}

func mustComplexParallelEvidenceDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "cleardev-s07-evidence-")
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
