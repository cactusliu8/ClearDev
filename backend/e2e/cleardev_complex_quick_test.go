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

func TestQuickExecutionWithRealCodexChat(t *testing.T) {
	requireE2E(t)
	imageID := requireStandardE2EPrerequisites(t)
	fixturePath := prepareComplexE2EFixture(t)
	initialSHA := complexExecutionGit(t, fixturePath, "rev-parse", "--verify", "HEAD^{commit}")
	if !fullLowerGitSHA(initialSHA) {
		t.Fatalf("S08 fixture initial commit is not a full Git SHA: %q", initialSHA)
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
		t.Fatalf("open isolated store for S08 preparer: %v", err)
	}
	prep := cleardevtest.SeedComplexParallelV2WithSessions(t, store, projectID, fixturePath, dataDir, cleardevtest.ComplexStandardPrepSessions{
		StewardSessionID: stewardSessionID,
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close S08 preparer store: %v", err)
	}

	d := registrationDaemon
	var started cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &started)
	s07 := awaitComplexExecutionPhase(t, d, prep.RequirementID, core.ComplexExecutionCompleted, 30*time.Minute)
	if s07.ComplexExecution == nil || s07.ComplexExecution.Integration == nil {
		t.Fatalf("S08 requires a completed S07 integration: %#v", s07.ComplexExecution)
	}

	var followUp cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &followUp)
	if followUp.QuickExecution == nil {
		t.Fatalf("empty-body follow-up did not start QUICK: %#v", followUp)
	}
	completed := awaitQuickExecutionPhase(t, d, prep.RequirementID, core.ComplexExecutionCompleted, 30*time.Minute)
	evidence := assertQuickExecutionCompletion(t, completed, s07, prep, imageID)

	d.stop()
	d = startDaemon(t, dataDir)
	restarted := mustGetComplexE2E(t, d, prep.RequirementID)
	if restarted.QuickExecution == nil || restarted.QuickExecution.Run.ID != completed.QuickExecution.Run.ID || restarted.QuickExecution.Phase != core.ComplexExecutionCompleted {
		t.Fatalf("S08 restart changed the completed QUICK run: %#v", restarted.QuickExecution)
	}
	var retried cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+prep.RequirementID+"/execution-runs", http.StatusAccepted, nil, &retried)
	if retried.QuickExecution == nil || retried.QuickExecution.Run.ID != completed.QuickExecution.Run.ID || retried.QuickExecution.Task == nil || retried.QuickExecution.Task.DevelopmentTaskID != completed.QuickExecution.Task.DevelopmentTaskID {
		t.Fatalf("S08 idempotent retry changed the QUICK task: before=%#v after=%#v", completed.QuickExecution, retried.QuickExecution)
	}

	evidence["daemonPID"] = d.pgid
	evidence["dataDir"] = dataDir
	evidenceDir := mustQuickExecutionEvidenceDir(t)
	writeJSONFile(t, filepath.Join(evidenceDir, "complex-quick-execution.json"), evidence)
	t.Logf("S08 QUICK completed requirement=%s run=%s taskSet=2 evidence=%s", prep.RequirementID, completed.QuickExecution.Run.ID, evidenceDir)
}

func awaitQuickExecutionPhase(t *testing.T, d *daemon, requirementID string, want core.ComplexExecutionPhase, timeout time.Duration) cleardevsvc.RequirementView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last cleardevsvc.RequirementView
	for time.Now().Before(deadline) {
		last = mustGetComplexE2E(t, d, requirementID)
		if last.QuickExecution != nil {
			if last.QuickExecution.Phase == want {
				return last
			}
			if last.QuickExecution.Phase == core.ComplexExecutionBlocked || last.QuickExecution.Phase == core.ComplexExecutionNeedsHuman {
				evidenceDir := mustQuickExecutionEvidenceDir(t)
				writeJSONFile(t, filepath.Join(evidenceDir, "complex-quick-execution-stopped.json"), last)
				t.Fatalf("S08 stopped in phase %s reason=%s missing=%v evidence=%s\n%s", last.QuickExecution.Phase, last.QuickExecution.PhaseReason, last.QuickExecution.MissingEvidence, evidenceDir, d.tailLog())
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if last.QuickExecution == nil {
		t.Fatalf("S08 did not expose QUICK facts before timeout\n%s", d.tailLog())
	}
	t.Fatalf("S08 phase timeout: got=%s want=%s missing=%v\n%s", last.QuickExecution.Phase, want, last.QuickExecution.MissingEvidence, d.tailLog())
	return cleardevsvc.RequirementView{}
}

func assertQuickExecutionCompletion(t *testing.T, view, s07 cleardevsvc.RequirementView, prep cleardevtest.ComplexParallelPrep, imageID string) map[string]any {
	t.Helper()
	quick := view.QuickExecution
	if quick == nil || quick.Phase != core.ComplexExecutionCompleted || quick.Run.CompletedAt == nil || quick.Integration == nil || len(quick.MissingEvidence) != 0 {
		t.Fatalf("S08 completion read model = %#v", quick)
	}
	if view.OverallProgress.Phase != core.OverallPhaseCompleted || view.OverallProgress.TaskSetVersion == nil || *view.OverallProgress.TaskSetVersion != core.ComplexStandardTaskSetVersion+1 {
		t.Fatalf("S08 overall progress = %#v", view.OverallProgress)
	}
	if quick.Run.Mode != core.WorkModeQuick || quick.Run.SourceTaskKey != "deduplicate-email" || quick.Run.IntegrationBaseSHA != s07.ComplexExecution.Integration.CandidateCommitSHA {
		t.Fatalf("S08 run = %#v s07=%#v", quick.Run, s07.ComplexExecution.Integration)
	}
	if quick.Task == nil || quick.Task.TaskKey != "deduplicate-email" || quick.Task.Status != core.DevelopmentTaskStatusDone {
		t.Fatalf("S08 task = %#v", quick.Task)
	}
	pkg, err := core.ParseComplexQuickExecutionPackage([]byte(quick.Task.ExecutionPackageJSON))
	if err != nil || pkg.Mode != string(core.WorkModeQuick) || strings.Join(pkg.WritePaths, ",") != "src/deduplicate.js,test/deduplicate.test.js" {
		t.Fatalf("S08 package = %#v err=%v", pkg, err)
	}

	builders, reviewers, planners := 0, 0, 0
	var builder core.ComplexExecutionRoleBinding
	for _, binding := range quick.RoleBindings {
		switch binding.Role {
		case core.StandardRoleBuilder:
			builders++
			builder = binding
		case core.StandardRoleReviewer:
			reviewers++
		case core.StandardRoleEngineeringPlanner:
			planners++
		}
	}
	if builders != 1 || reviewers != 0 || planners != 0 || builder.AOSessionID == "" || builder.WorkspacePath == "" {
		t.Fatalf("S08 roles builders=%d reviewers=%d planners=%d builder=%#v", builders, reviewers, planners, builder)
	}
	if !strings.Contains(builder.SessionCreationIdempotencyKey, "builder-quick") {
		t.Fatalf("S08 Builder idempotency key = %q", builder.SessionCreationIdempotencyKey)
	}

	dispatch := core.ComplexExecutionDispatch{}
	for _, item := range quick.Dispatches {
		if item.CandidateCommitSHA == quick.Integration.CandidateCommitSHA {
			dispatch = item
		}
	}
	if dispatch.ID == "" || dispatch.BaseCommitSHA != quick.Run.IntegrationBaseSHA || !fullLowerGitSHA(dispatch.CandidateCommitSHA) {
		t.Fatalf("S08 dispatch = %#v", dispatch)
	}
	changed := []string{}
	for _, path := range strings.Split(strings.TrimSpace(complexExecutionGit(t, builder.WorkspacePath, "diff", "--name-only", dispatch.BaseCommitSHA, dispatch.CandidateCommitSHA)), "\n") {
		if strings.TrimSpace(path) != "" {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	if strings.Join(changed, ",") != "src/deduplicate.js,test/deduplicate.test.js" {
		t.Fatalf("S08 git paths = %#v", changed)
	}

	scope, required, integration := 0, 0, 0
	for _, run := range quick.CheckRuns {
		if run.Status != core.ComplexExecutionCheckRunSettled || run.Result != core.EvidenceResultPass {
			t.Fatalf("S08 check = %#v", run)
		}
		if run.ContainerImageID != "" && !strings.Contains(run.ContainerImageID, imageID) && run.Kind != core.CandidateCheckScope {
			t.Fatalf("S08 check image = %s want %s", run.ContainerImageID, imageID)
		}
		switch run.Kind {
		case core.CandidateCheckScope:
			scope++
		case core.CandidateCheckRequired:
			required++
		case core.CandidateCheckIntegration:
			integration++
		}
	}
	if scope == 0 || required == 0 || integration == 0 {
		t.Fatalf("S08 checks scope=%d required=%d integration=%d", scope, required, integration)
	}

	if view.ComplexExecution == nil || view.ComplexExecution.Run.ID != s07.ComplexExecution.Run.ID {
		t.Fatalf("S08 changed the S07 execution: %#v", view.ComplexExecution)
	}
	for _, event := range view.Events {
		if strings.Contains(string(event.SubjectType), "RECOVERY") || strings.Contains(string(event.SubjectType), "SPECIALIST") {
			t.Fatalf("S08 created a forbidden fact: %#v", event)
		}
	}

	return map[string]any{
		"requirementId":    prep.RequirementID,
		"versionId":        prep.V2ID,
		"runId":            quick.Run.ID,
		"phase":            quick.Phase,
		"s07Integration":   s07.ComplexExecution.Integration.CandidateCommitSHA,
		"quickIntegration": quick.Integration.CandidateCommitSHA,
		"roles":            quick.RoleBindings,
		"task":             quick.Task,
		"dispatches":       quick.Dispatches,
		"checks":           quick.CheckRuns,
		"changedPaths":     changed,
		"containerImageId": imageID,
	}
}

func mustQuickExecutionEvidenceDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "cleardev-s08-evidence-")
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
