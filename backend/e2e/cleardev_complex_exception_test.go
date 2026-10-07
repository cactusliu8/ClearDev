//go:build !windows

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

func TestControlledExceptionScopeExpansionGeneratedProofSpecialistRecoveryWithRealCodexChat(t *testing.T) {
	requireE2E(t)
	imageID := requireStandardE2EPrerequisites(t)
	fixturePath := prepareComplexE2EFixture(t)
	initialSHA := complexExecutionGit(t, fixturePath, "rev-parse", "--verify", "HEAD^{commit}")
	if !fullLowerGitSHA(initialSHA) {
		t.Fatalf("S09 fixture initial commit is not a full Git SHA: %q", initialSHA)
	}
	injectFile := filepath.Join(t.TempDir(), "injected-infra")
	t.Setenv("AO_CLEARDEV_TEST_INJECT_INFRA_FILE", injectFile)
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
		t.Fatalf("open isolated store for S09 preparer: %v", err)
	}
	prep := cleardevtest.SeedComplexExceptionV2WithSessions(t, store, projectID, fixturePath, dataDir, cleardevtest.ComplexStandardPrepSessions{
		StewardSessionID: stewardSessionID,
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close S09 preparer store: %v", err)
	}

	d := registrationDaemon
	var started cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &started)
	if started.ComplexExecution == nil || started.ComplexExecution.Run.RequirementVersionID != prep.V2ID || started.ComplexExecution.Run.PlanID != prep.V2PlanID {
		t.Fatalf("S09 start did not bind the prepared v2 and plan: %#v", started.ComplexExecution)
	}

	completed := awaitComplexExecutionPhase(t, d, prep.RequirementID, core.ComplexExecutionCompleted, 40*time.Minute)
	assertControlledExceptionCompletion(t, completed, prep, initialSHA, imageID, fixturePath, injectFile)
	runID := completed.ComplexExecution.Run.ID

	before := complexExecutionFactCounts(completed.ComplexExecution)
	d.stop()
	d = startDaemon(t, dataDir)
	restarted := mustGetComplexE2E(t, d, prep.RequirementID)
	if restarted.ComplexExecution == nil || restarted.ComplexExecution.Run.ID != runID || restarted.ComplexExecution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("S09 restart read changed the completed run: %#v", restarted.ComplexExecution)
	}
	var retried cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &retried)
	if retried.ComplexExecution == nil || retried.ComplexExecution.Run.ID != runID || complexExecutionFactCounts(retried.ComplexExecution) != before {
		t.Fatalf("S09 idempotent retry changed facts: before=%+v after=%+v", before, complexExecutionFactCounts(retried.ComplexExecution))
	}

	evidenceDir := mustComplexExecutionEvidenceDir(t)
	writeJSONFile(t, filepath.Join(evidenceDir, "complex-controlled-exception.json"), map[string]any{
		"requirementId": prep.RequirementID,
		"runId":         runID,
		"phase":         retried.ComplexExecution.Phase,
		"exception":     retried.ComplexExecution.Exception,
		"injectedFile":  injectFile,
		"imageId":       imageID,
	})
	t.Logf("S09 controlled exception completed requirement=%s run=%s evidence=%s", prep.RequirementID, runID, evidenceDir)
}

func assertControlledExceptionCompletion(t *testing.T, view cleardevsvc.RequirementView, prep cleardevtest.ComplexStandardPrep, initialSHA, imageID, repo, injectFile string) {
	t.Helper()
	execution := view.ComplexExecution
	if execution == nil || execution.Phase != core.ComplexExecutionCompleted || execution.Exception == nil || execution.Integration == nil {
		t.Fatalf("S09 completion = %#v", execution)
	}
	if execution.Run.Mode != core.WorkModeStandard {
		t.Fatalf("S09 mode = %s", execution.Run.Mode)
	}
	specialists, recoveries, live := 0, 0, 0
	for _, binding := range execution.Exception.OnDemandBindings {
		switch binding.Mode {
		case core.ComplexOnDemandModeSpecialist:
			specialists++
		case core.ComplexOnDemandModeRecovery:
			recoveries++
		}
		if binding.Status == core.RoleBindingStatusRequested || binding.Status == core.RoleBindingStatusBound {
			live++
		}
	}
	if specialists != 1 || recoveries != 1 || live != 0 {
		t.Fatalf("S09 on-demand specialists=%d recoveries=%d live=%d", specialists, recoveries, live)
	}
	if len(execution.Exception.SpecialistResults) != 1 || execution.Exception.SpecialistResults[0].Outcome != "PASS" {
		t.Fatalf("S09 specialist results = %#v", execution.Exception.SpecialistResults)
	}
	if len(execution.Exception.ScopeDecisions) != 1 || !execution.Exception.ScopeDecisions[0].ControlAccepted {
		t.Fatalf("S09 scope decisions = %#v", execution.Exception.ScopeDecisions)
	}
	maxPermission := int64(0)
	for _, permission := range execution.Exception.PermissionVersions {
		if permission.Version > maxPermission {
			maxPermission = permission.Version
		}
	}
	if maxPermission < 2 {
		t.Fatalf("S09 permission version = %d", maxPermission)
	}
	if len(execution.Exception.GeneratedProofs) != 1 || execution.Exception.GeneratedProofs[0].Result != core.EvidenceResultPass {
		t.Fatalf("S09 generated proofs = %#v", execution.Exception.GeneratedProofs)
	}
	injected, retried := 0, 0
	for _, run := range execution.CheckRuns {
		if run.Kind != core.CandidateCheckIntegration {
			continue
		}
		if run.RetryOrdinal == 0 && strings.Contains(run.OutputSummary, "INJECTED_INFRASTRUCTURE_FAILURE") {
			injected++
		}
		if run.RetryOrdinal == 1 && run.Result == core.EvidenceResultPass {
			if strings.Contains(run.OutputSummary, "INJECTED_INFRASTRUCTURE_FAILURE") {
				t.Fatal("S09 retry reused the injected failure")
			}
			retried++
		}
	}
	if injected != 1 || retried != 1 {
		t.Fatalf("S09 integration injected=%d retried=%d", injected, retried)
	}
	if _, err := os.Stat(injectFile); err != nil {
		t.Fatalf("S09 injected sentinel missing: %v", err)
	}
	if len(execution.Exception.RecoveryActions) != 1 || execution.Exception.RecoveryActions[0].Action != core.ComplexRecoveryActionRetryInfraCheck {
		t.Fatalf("S09 recovery = %#v", execution.Exception.RecoveryActions)
	}
	firstDiff := ""
	for _, task := range execution.Tasks {
		if task.TaskKey != "normalize-and-deduplicate" {
			continue
		}
		for _, dispatch := range execution.Dispatches {
			if dispatch.ComplexExecutionTaskID == task.ID {
				firstDiff = complexExecutionGit(t, repo, "diff", "--name-only", dispatch.BaseCommitSHA, dispatch.CandidateCommitSHA)
			}
		}
	}
	if !strings.Contains(firstDiff, "package.json") || !strings.Contains(firstDiff, "package-lock.json") {
		t.Fatalf("S09 first-task diff missing shared/generated paths: %s", firstDiff)
	}
	_ = initialSHA
	_ = imageID
}
