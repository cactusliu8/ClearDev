//go:build !windows

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// TestComplexStandardExecutionWithRealCodexChat is the deliberately expensive
// S06 demonstration. The test-only preparer reaches the exact S05 terminal
// state through production stores and services; the production daemon then
// owns every S06 write, Codex Chat turn, worktree, Git observation, check,
// review, and atomic integration transition.
func TestComplexStandardExecutionWithRealCodexChat(t *testing.T) {
	requireE2E(t)
	imageID := requireStandardE2EPrerequisites(t)
	fixturePath := prepareComplexE2EFixture(t)
	initialSHA := complexExecutionGit(t, fixturePath, "rev-parse", "--verify", "HEAD^{commit}")
	if !fullLowerGitSHA(initialSHA) {
		t.Fatalf("S06 fixture initial commit is not a full Git SHA: %q", initialSHA)
	}
	dataDir := t.TempDir()

	registrationDaemon := startDaemon(t, dataDir)
	projectID := registerStandardE2EProject(t, registrationDaemon, fixturePath)
	setPermissions(t, registrationDaemon, projectID, "auto")
	stewardSessionID := spawn(t, registrationDaemon, map[string]any{
		"projectId": projectID, "kind": "orchestrator", "harness": "codex", "mode": "chat", "prompt": "",
	}).Session.ID
	registrationDaemon.awaitLiveController(stewardSessionID, 90*time.Second)

	// Seed the completed S05 facts while the production-created, promptless
	// Steward controller is still live. Restarting that empty conversation before
	// its first turn would have no provider rollout to resume.
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open isolated store for S06 preparer: %v", err)
	}
	prep := cleardevtest.SeedComplexStandardV2WithSessions(t, store, projectID, fixturePath, dataDir, cleardevtest.ComplexStandardPrepSessions{
		StewardSessionID: stewardSessionID,
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close S06 preparer store: %v", err)
	}

	d := registrationDaemon
	var started cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &started)
	if started.ComplexExecution == nil || started.ComplexExecution.Run.RequirementVersionID != prep.V2ID || started.ComplexExecution.Run.PlanID != prep.V2PlanID {
		t.Fatalf("S06 start did not bind the prepared v2 and plan: %#v", started.ComplexExecution)
	}

	completed := awaitComplexExecutionPhase(t, d, prep.RequirementID, core.ComplexExecutionCompleted, 30*time.Minute)
	evidence := assertComplexExecutionCompletion(t, completed, prep, initialSHA, imageID, fixturePath)
	runID := completed.ComplexExecution.Run.ID

	// A completed run remains identical across daemon restart and a repeated
	// empty-body start request. This proves recovery reads the durable result
	// and does not create another role, task, turn, check, or event.
	before := complexExecutionFactCounts(completed.ComplexExecution)
	d.stop()
	d = startDaemon(t, dataDir)
	restarted := mustGetComplexE2E(t, d, prep.RequirementID)
	if restarted.ComplexExecution == nil || restarted.ComplexExecution.Run.ID != runID || restarted.ComplexExecution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("S06 restart read changed the completed run: %#v", restarted.ComplexExecution)
	}
	var retried cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &retried)
	if retried.ComplexExecution == nil || retried.ComplexExecution.Run.ID != runID || complexExecutionFactCounts(retried.ComplexExecution) != before {
		t.Fatalf("S06 idempotent retry changed facts: before=%+v after=%+v", before, complexExecutionFactCounts(retried.ComplexExecution))
	}

	evidence["daemonPID"] = d.pgid
	evidence["dataDir"] = dataDir
	evidence["events"] = summarizeRequirementEvents(retried)
	evidenceDir := mustComplexExecutionEvidenceDir(t)
	writeJSONFile(t, filepath.Join(evidenceDir, "complex-standard-execution.json"), evidence)
	t.Logf("S06 complex STANDARD completed requirement=%s run=%s tasks=%d evidence=%s", prep.RequirementID, runID, len(completed.ComplexExecution.Tasks), evidenceDir)
}

func awaitComplexExecutionPhase(t *testing.T, d *daemon, requirementID string, want core.ComplexExecutionPhase, timeout time.Duration) cleardevsvc.RequirementView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last cleardevsvc.RequirementView
	for time.Now().Before(deadline) {
		last = mustGetComplexE2E(t, d, requirementID)
		if last.ComplexExecution != nil {
			if last.ComplexExecution.Phase == want {
				return last
			}
			if last.ComplexExecution.Phase == core.ComplexExecutionBlocked || last.ComplexExecution.Phase == core.ComplexExecutionNeedsHuman {
				evidenceDir := mustComplexExecutionEvidenceDir(t)
				writeJSONFile(t, filepath.Join(evidenceDir, "complex-standard-execution-stopped.json"), last)
				t.Fatalf("S06 stopped in phase %s reason=%s missing=%v evidence=%s\n%s", last.ComplexExecution.Phase, last.ComplexExecution.PhaseReason, last.ComplexExecution.MissingEvidence, evidenceDir, d.tailLog())
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if last.ComplexExecution == nil {
		t.Fatalf("S06 did not expose execution facts before timeout\n%s", d.tailLog())
	}
	t.Fatalf("S06 phase timeout: got=%s want=%s missing=%v\n%s", last.ComplexExecution.Phase, want, last.ComplexExecution.MissingEvidence, d.tailLog())
	return cleardevsvc.RequirementView{}
}

func assertComplexExecutionCompletion(t *testing.T, view cleardevsvc.RequirementView, prep cleardevtest.ComplexStandardPrep, initialSHA, imageID, repo string) map[string]any {
	t.Helper()
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionCompleted || execution.Run.CompletedAt == nil || execution.Integration == nil || len(execution.MissingEvidence) != 0 {
		t.Fatalf("S06 completion read model = %#v", execution)
	}
	if view.OverallProgress.Phase != core.OverallPhaseCompleted || view.OverallProgress.CurrentRequirementVersionID != prep.V2ID || view.OverallProgress.TaskSetVersion == nil || *view.OverallProgress.TaskSetVersion != core.ComplexStandardTaskSetVersion {
		t.Fatalf("S06 overall progress = %#v", view.OverallProgress)
	}
	if execution.Run.Mode != core.WorkModeStandard || execution.Run.ModeReason != "ONE_BUILDER_REQUIRED" {
		t.Fatalf("S06 mode fact = mode:%s reason:%s", execution.Run.Mode, execution.Run.ModeReason)
	}

	tasks := append([]core.ComplexExecutionTask(nil), execution.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Ordinal < tasks[j].Ordinal })
	if len(tasks) != 2 || tasks[0].TaskKey != "normalize-and-deduplicate" || tasks[1].TaskKey != "build-summary" || len(tasks[0].DependencyTaskKeys) != 0 || strings.Join(tasks[1].DependencyTaskKeys, ",") != tasks[0].TaskKey {
		t.Fatalf("S06 task graph = %#v", tasks)
	}
	for _, task := range tasks {
		if task.Status != core.DevelopmentTaskStatusDone || task.ReworkCount != 0 || task.CurrentRound != 0 {
			t.Fatalf("S06 task %s terminal state = %#v", task.TaskKey, task)
		}
		pkg, err := core.ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
		if err != nil || pkg.Mode != string(core.WorkModeStandard) || len(pkg.GeneratedPaths) != 0 || len(pkg.SharedPathsRequireApproval) != 0 || pkg.MaxReworkCount != core.ComplexStandardMaxReworkCount {
			t.Fatalf("S06 task %s execution package is not the frozen STANDARD boundary: pkg=%#v err=%v", task.TaskKey, pkg, err)
		}
	}
	assertComplexExecutionTaskModes(t, view, prep.V2ID, tasks)

	dispatches := make(map[string]core.ComplexExecutionDispatch, len(tasks))
	for _, dispatch := range execution.Dispatches {
		if _, duplicate := dispatches[dispatch.ComplexExecutionTaskID]; duplicate {
			t.Fatalf("S06 produced multiple dispatches for task %s", dispatch.ComplexExecutionTaskID)
		}
		dispatches[dispatch.ComplexExecutionTaskID] = dispatch
	}
	if len(dispatches) != 2 {
		t.Fatalf("S06 dispatch count = %d, want 2", len(dispatches))
	}
	first, second := dispatches[tasks[0].ID], dispatches[tasks[1].ID]
	if first.ID == "" || second.ID == "" || first.Status != core.ComplexExecutionDispatchVerified || second.Status != core.ComplexExecutionDispatchVerified || first.BaseCommitSHA != initialSHA || second.BaseCommitSHA != first.CandidateCommitSHA || !fullLowerGitSHA(first.CandidateCommitSHA) || !fullLowerGitSHA(second.CandidateCommitSHA) {
		t.Fatalf("S06 dispatch chain = first:%#v second:%#v initial:%s", first, second, initialSHA)
	}
	if execution.Integration.CandidateCommitSHA != second.CandidateCommitSHA || execution.Integration.IntegrationCandidateID == "" || len(execution.Integration.CheckRunIDs) != 1 {
		t.Fatalf("S06 integration binding = %#v final=%s", execution.Integration, second.CandidateCommitSHA)
	}

	roles := map[core.StandardRole][]core.ComplexExecutionRoleBinding{}
	sessionOwner := map[string]core.StandardRole{}
	for _, role := range execution.RoleBindings {
		roles[role.Role] = append(roles[role.Role], role)
		if role.AOSessionID == "" {
			t.Fatalf("S06 role %s has no AO session: %#v", role.Role, role)
		}
		if prior, duplicate := sessionOwner[role.AOSessionID]; duplicate {
			t.Fatalf("S06 AO session %s is shared by %s and %s", role.AOSessionID, prior, role.Role)
		}
		sessionOwner[role.AOSessionID] = role.Role
	}
	if len(roles[core.StandardRoleSteward]) != 1 || len(roles[core.StandardRoleBuilder]) != 1 || len(roles[core.StandardRoleReviewer]) != 2 || len(roles) != 3 {
		t.Fatalf("S06 role counts = %#v", roles)
	}
	builder := roles[core.StandardRoleBuilder][0]
	if execution.Run.BuilderRoleBindingID != builder.ID || execution.Run.BuilderAOSessionID != builder.AOSessionID || execution.Run.InitialBaseCommitSHA != initialSHA || builder.WorkspacePath == "" || builder.BaseCommitSHA != initialSHA {
		t.Fatalf("S06 Builder binding = run:%#v builder:%#v", execution.Run, builder)
	}
	if _, duplicate := sessionOwner[prep.PlannerSessionID]; duplicate || prep.PlannerSessionID == prep.StewardSessionID {
		t.Fatalf("S06 reused the Engineering Planner session: planner=%s owners=%#v", prep.PlannerSessionID, sessionOwner)
	}
	for _, reviewer := range roles[core.StandardRoleReviewer] {
		if reviewer.Status != core.RoleBindingStatusEnded || reviewer.WorkspacePath == "" || reviewer.WorkspacePath == builder.WorkspacePath || !fullLowerGitSHA(reviewer.BaseCommitSHA) {
			t.Fatalf("S06 Reviewer binding is not independent: %#v builder=%#v", reviewer, builder)
		}
	}

	if len(execution.Reviews) != 2 || len(execution.Verifications) != 2 {
		t.Fatalf("S06 review/verification counts = %d/%d", len(execution.Reviews), len(execution.Verifications))
	}
	for _, review := range execution.Reviews {
		if review.Status != core.LocalReviewStatusSettled || review.Verdict != core.LocalReviewPass || review.ReviewPacketSHA256 == "" {
			t.Fatalf("S06 review did not PASS with bound Chat evidence: %#v", review)
		}
	}
	for _, verification := range execution.Verifications {
		if verification.ScopeEvidenceID == "" || len(verification.RequiredCheckRunIDs) == 0 || verification.LocalReviewID == "" || !fullLowerGitSHA(verification.CandidateCommitSHA) {
			t.Fatalf("S06 verification is incomplete: %#v", verification)
		}
	}

	if len(execution.CheckSpecs) != 6 || len(execution.CheckRuns) != 6 {
		t.Fatalf("S06 check catalog/run counts = %d/%d, want 6/6", len(execution.CheckSpecs), len(execution.CheckRuns))
	}
	assertComplexExecutionFixedChecks(t, execution)
	integrationRunID := execution.Integration.CheckRunIDs[0]
	for _, run := range execution.CheckRuns {
		if run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass || run.CheckSpecFactID == "" || !fullLowerGitSHA(run.CandidateCommitSHA) {
			t.Fatalf("S06 check did not settle PASS: %#v", run)
		}
		if run.Kind != core.CandidateCheckScope && run.ContainerImageID != imageID {
			t.Fatalf("S06 check %s image = %q, want %q", run.ID, run.ContainerImageID, imageID)
		}
	}
	if !complexExecutionHasIntegrationRun(execution.CheckRuns, integrationRunID, second.CandidateCommitSHA) {
		t.Fatalf("S06 integration check %s is not bound to final candidate %s", integrationRunID, second.CandidateCommitSHA)
	}
	integrationRun := complexExecutionCheckRunByID(t, execution.CheckRuns, integrationRunID)
	if integrationRun.OutputTruncated || !strings.Contains(integrationRun.OutputSummary, "ClearDev integration checks run in the frozen sandbox") || strings.Contains(integrationRun.OutputSummary, "# SKIP") {
		t.Fatalf("S06 integration check did not prove the live restricted sandbox: truncated=%t output=%q", integrationRun.OutputTruncated, integrationRun.OutputSummary)
	}

	for _, step := range execution.AgentSteps {
		if step.SendStatus != core.AgentStepSendStatusSettled || step.TurnID == "" || step.FinalMessageID == "" || step.MessageSHA256 == "" {
			t.Fatalf("S06 Agent step lacks completed Codex Chat evidence: %#v", step)
		}
	}
	if len(execution.AgentSteps) != 5 { // Steward + two Builder turns + two Reviewer turns.
		t.Fatalf("S06 Agent step count = %d, want 5", len(execution.AgentSteps))
	}

	gitChain := []map[string]any{
		{"taskKey": tasks[0].TaskKey, "base": first.BaseCommitSHA, "candidate": first.CandidateCommitSHA, "diff": complexExecutionGit(t, repo, "diff", "--name-status", "--find-renames", first.BaseCommitSHA, first.CandidateCommitSHA)},
		{"taskKey": tasks[1].TaskKey, "base": second.BaseCommitSHA, "candidate": second.CandidateCommitSHA, "diff": complexExecutionGit(t, repo, "diff", "--name-status", "--find-renames", second.BaseCommitSHA, second.CandidateCommitSHA)},
	}
	if status := complexExecutionGit(t, builder.WorkspacePath, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("S06 Builder worktree is dirty: %q", status)
	}
	assertNoS06HumanDecisionEvents(t, view.Events, execution.Run.ID)

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
		"dispatches":       execution.Dispatches,
		"checks":           execution.CheckRuns,
		"reviews":          execution.Reviews,
		"verifications":    execution.Verifications,
		"integration":      execution.Integration,
		"git":              gitChain,
		"containerImageId": imageID,
	}
}

func assertComplexExecutionTaskModes(t *testing.T, view cleardevsvc.RequirementView, versionID string, tasks []core.ComplexExecutionTask) {
	t.Helper()
	want := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		want[task.DevelopmentTaskID] = struct{}{}
	}
	seen := 0
	for _, task := range view.DevelopmentTasks {
		if task.DevelopmentTask.RequirementVersionID != versionID {
			continue
		}
		seen++
		if _, ok := want[task.DevelopmentTask.ID]; !ok || task.DevelopmentTask.Mode != core.WorkModeStandard || task.DevelopmentTask.Status != core.DevelopmentTaskStatusDone {
			t.Fatalf("S06 current task escaped the fixed STANDARD task set: %#v", task.DevelopmentTask)
		}
	}
	if seen != len(tasks) {
		t.Fatalf("S06 current v2 task count = %d, want %d", seen, len(tasks))
	}
}

func assertComplexExecutionFixedChecks(t *testing.T, execution *core.ComplexExecutionSnapshot) {
	t.Helper()
	wantCounts := map[string]int{"SCOPE": 2, "email-unit": 1, "deduplicate-unit": 1, "summary-unit": 1, "all-tests": 1}
	byID := make(map[string]core.ComplexExecutionCheckSpecFact, len(execution.CheckSpecs))
	for _, spec := range execution.CheckSpecs {
		byID[spec.ID] = spec
		wantCounts[spec.CheckID]--
		if spec.CheckID == "SCOPE" {
			if spec.Kind != core.CandidateCheckScope || len(spec.Argv) != 0 || spec.TimeoutSeconds != 0 {
				t.Fatalf("S06 scope check spec changed: %#v", spec)
			}
			continue
		}
		frozen, ok := core.FrozenComplexCheckByID(spec.CheckID)
		if !ok || !slices.Equal(spec.Argv, frozen.Argv) || spec.TimeoutSeconds != frozen.TimeoutSeconds {
			t.Fatalf("S06 executable check is not frozen: spec=%#v frozen=%#v found=%t", spec, frozen, ok)
		}
		expectedKind := core.CandidateCheckRequired
		if spec.CheckID == "all-tests" {
			expectedKind = core.CandidateCheckIntegration
		}
		if spec.Kind != expectedKind {
			t.Fatalf("S06 check kind changed for %s: %s", spec.CheckID, spec.Kind)
		}
	}
	for checkID, remaining := range wantCounts {
		if remaining != 0 {
			t.Fatalf("S06 fixed check %s count delta = %d", checkID, remaining)
		}
	}
	for _, run := range execution.CheckRuns {
		spec, ok := byID[run.CheckSpecFactID]
		if !ok || !slices.Equal(run.Argv, spec.Argv) || run.Kind != spec.Kind {
			t.Fatalf("S06 check run escaped its persisted fixed spec: run=%#v spec=%#v found=%t", run, spec, ok)
		}
	}
}

func assertNoS06HumanDecisionEvents(t *testing.T, events []core.RequirementEvent, runID string) {
	t.Helper()
	startSequence := int64(0)
	for _, event := range events {
		if event.SubjectType == core.SubjectComplexExecutionRun && event.SubjectID == runID && event.Action == core.ActionRequestComplexExecution {
			startSequence = event.Sequence
			break
		}
	}
	if startSequence == 0 {
		t.Fatalf("S06 request event for run %s is missing", runID)
	}
	for _, event := range events {
		if event.Sequence < startSequence {
			continue
		}
		if event.Source == core.EventSourceHumanDecision || event.SubjectType == core.SubjectHumanDecisionRequest || event.SubjectType == core.SubjectHumanDecisionDispatch || event.SubjectType == core.SubjectHumanDecisionEffect {
			t.Fatalf("S06 created a forbidden human/desktop decision event: %#v", event)
		}
	}
}

func complexExecutionCheckRunByID(t *testing.T, runs []core.ComplexExecutionCheckRun, id string) core.ComplexExecutionCheckRun {
	t.Helper()
	for _, run := range runs {
		if run.ID == id {
			return run
		}
	}
	t.Fatalf("S06 check run %s is missing", id)
	return core.ComplexExecutionCheckRun{}
}

type complexExecutionCounts struct {
	Roles, Steps, Tasks, Dispatches, Checks, Reviews, Verifications int
}

func complexExecutionFactCounts(execution *core.ComplexExecutionSnapshot) complexExecutionCounts {
	if execution == nil {
		return complexExecutionCounts{}
	}
	return complexExecutionCounts{len(execution.RoleBindings), len(execution.AgentSteps), len(execution.Tasks), len(execution.Dispatches), len(execution.CheckRuns), len(execution.Reviews), len(execution.Verifications)}
}

func complexExecutionHasIntegrationRun(runs []core.ComplexExecutionCheckRun, id, candidateSHA string) bool {
	for _, run := range runs {
		if run.ID == id && run.Kind == core.CandidateCheckIntegration && run.CandidateCommitSHA == candidateSHA && run.Result == core.EvidenceResultPass {
			return true
		}
	}
	return false
}

func complexExecutionGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, boundedOutput(out))
	}
	return strings.TrimSpace(string(out))
}

func mustComplexExecutionEvidenceDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "cleardev-s06-evidence-")
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
