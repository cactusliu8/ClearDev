//go:build !windows

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

const directionE2EMessage = "只接受 example.com 域名，其他域名计入拒绝数量。"

// TestNativeMessageBoxClickDriver proves the X11 click driver can press Approve
// on the same native Electron dialog options the production host uses. It does
// not speak the human-authority protocol and does not replace the packaged
// Codex flow below.
func TestNativeMessageBoxClickDriver(t *testing.T) {
	requireElectronE2E(t)
	display, xvfbPID := startXvfbDisplay(t)
	evidenceDir := mustEvidenceDir(t)
	probe := filepath.Join(e2eRepoRoot(t), "backend", "e2e", "testdata", "human-dialog-probe.cjs")
	electronBin := unpackagedElectronBinary(t)
	cmd := exec.Command(electronBin, "--no-sandbox", probe, "Approve direction change", "v1 SHA-256 probe. Task count 2. Snapshot SHA-256 probe.")
	cmd.Env = electronX11Env(display, nil)
	cmd.Dir = filepath.Dir(electronBin)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	before := listedXWindows(t, display)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start dialog probe: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	clickNativeApprove(t, display, filepath.Join(evidenceDir, "probe-approve"), before, cmd.Process.Pid)
	buf := make([]byte, 64)
	n, readErr := stdout.Read(buf)
	if readErr != nil {
		t.Fatalf("dialog probe produced no result: %v", readErr)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("dialog probe exit: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "RESPONSE 0") {
		t.Fatalf("click driver did not select Approve: %q xvfb=%d", string(buf[:n]), xvfbPID)
	}
}

// TestDirectionChangeWithRealElectronAndCodex follows the frozen S05 handoff:
// finish the S04 preflow in an isolated data dir, stop that daemon, seed v1
// work through production adapters, then start a packaged Electron + production
// daemon and actually click native Approve and v2 Confirm.
func TestDirectionChangeWithRealElectronAndCodex(t *testing.T) {
	requireE2E(t)
	requireElectronE2E(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("S05 real demonstration needs git: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("S05 real demonstration needs node: %v", err)
	}
	if err := exec.Command(codexBinary(), "login", "status").Run(); err != nil { //nolint:gosec // explicit local binary selected by the gated harness.
		t.Fatalf("Codex Chat authentication preflight failed: %v", err)
	}

	evidenceDir := mustEvidenceDir(t)
	display, xvfbPID := startXvfbDisplay(t)
	homeDir := t.TempDir()
	fixturePath := prepareComplexE2EFixture(t)
	prdText := readComplexE2EPRD(t, fixturePath)
	dataDir := t.TempDir()
	runFile := filepath.Join(dataDir, "running.json")

	d := startDaemon(t, dataDir)
	projectID := registerStandardE2EProject(t, d, fixturePath)
	var created cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/complex", http.StatusCreated, map[string]any{
		"aoProjectId": projectID, "name": "复杂邮件名单", "prdText": prdText,
	}, &created)
	view := awaitComplexE2EPhase(t, d, created.Requirement.ID, core.ComplexPlanningAwaitingClarification, 12*time.Minute)
	d.mustCall("POST", "/cleardev/requirements/"+created.Requirement.ID+"/complex-clarifications", http.StatusOK, complexE2EAnswers(view), &view)
	view = awaitComplexE2EPhase(t, d, created.Requirement.ID, core.ComplexPlanningAwaitingConfirmation, 12*time.Minute)
	d.stop()
	d = startDaemonWithHumanAuthority(t, dataDir, core.HumanDecisionApprove)
	view = awaitComplexE2EPhase(t, d, created.Requirement.ID, core.ComplexPlanningApproved, 20*time.Minute)
	d.stop()

	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open isolated store for S05 preparer: %v", err)
	}
	prep := cleardevtest.SeedDirectionChangeV1Work(t, store, created.Requirement.ID, dataDir)
	dirtyBefore := gitEvidence(t, prep.DirtyRepo)
	if err := store.Close(); err != nil {
		t.Fatalf("close preparer store: %v", err)
	}

	electronBin := packagedElectronBinary(t)
	electronCmd := startPackagedElectron(t, display, homeDir, dataDir, runFile, freePort(t))
	d = attachDaemonFromRunFile(t, dataDir, runFile, 45*time.Second)
	daemonPID := d.pgid
	electronPID := electronCmd.Process.Pid
	writeJSONFile(t, filepath.Join(evidenceDir, "process.json"), map[string]any{
		"electronBinary":  electronBin,
		"electronVersion": electronVersion(t, electronBin),
		"electronPID":     electronPID,
		"daemonPID":       daemonPID,
		"xvfbPID":         xvfbPID,
		"display":         display,
		"dataDir":         dataDir,
	})

	beforeApprove := listedXWindows(t, display, electronPID)
	var proposed cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/"+created.Requirement.ID+"/direction-intents", http.StatusAccepted, map[string]any{
		"requestId":                "s05-e2e-direction",
		"developmentRequirementId": created.Requirement.ID,
		"message":                  directionE2EMessage,
	}, &proposed)
	view = awaitDirectionPhase(t, d, created.Requirement.ID, core.DirectionChangeAwaitingDecision, 12*time.Minute)
	if view.DirectionChange == nil || view.DirectionChange.Request == nil || view.DirectionChange.Gate == nil {
		t.Fatalf("direction decision facts missing: %#v", view.DirectionChange)
	}
	if view.DirectionChange.Intent == nil || !strings.Contains(view.DirectionChange.Intent.Message, "example.com") {
		t.Fatalf("direction message was not preserved: %#v", view.DirectionChange.Intent)
	}
	clickNativeApprove(t, display, filepath.Join(evidenceDir, "direction-approve"), beforeApprove, electronPID)

	view = awaitDirectionPhase(t, d, created.Requirement.ID, core.DirectionChangeAwaitingClarification, 12*time.Minute)
	beforeConfirm := listedXWindows(t, display, electronPID)
	d.mustCall("POST", "/cleardev/requirements/"+created.Requirement.ID+"/complex-clarifications", http.StatusOK, directionE2EAnswers(view), &view)
	view = awaitDirectionPhase(t, d, created.Requirement.ID, core.DirectionChangeAwaitingConfirmation, 12*time.Minute)
	clickNativeApprove(t, display, filepath.Join(evidenceDir, "v2-confirm"), beforeConfirm, electronPID)
	view = awaitDirectionPhase(t, d, created.Requirement.ID, core.DirectionChangeApproved, 20*time.Minute)

	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("v2 complex phase = %#v", view.ComplexPlanning)
	}
	var confirmed, superseded int
	var v2ID string
	for _, version := range view.RequirementVersions {
		switch version.Status {
		case core.RequirementVersionStatusConfirmed:
			confirmed++
			v2ID = version.ID
			if version.TaskSetVersion != 0 {
				t.Fatalf("v2 task-set version = %d", version.TaskSetVersion)
			}
		case core.RequirementVersionStatusSuperseded:
			superseded++
		}
	}
	if confirmed != 1 || superseded != 1 || v2ID == "" {
		t.Fatalf("versions confirmed=%d superseded=%d %#v", confirmed, superseded, view.RequirementVersions)
	}
	var v1SHA, v2SHA string
	for _, version := range view.RequirementVersions {
		switch version.Status {
		case core.RequirementVersionStatusConfirmed:
			v2SHA = version.SHA256
		case core.RequirementVersionStatusSuperseded:
			v1SHA = version.SHA256
		}
	}
	foundV2Plan := false
	for _, plan := range view.ComplexPlanning.Plans {
		if plan.RequirementVersionID != v2ID {
			continue
		}
		foundV2Plan = true
		if plan.CompilationSHA256 != v2SHA || plan.RequirementSHA256 != v2SHA {
			t.Fatalf("v2 plan bound compilation=%s requirement=%s, want confirmed %s", plan.CompilationSHA256, plan.RequirementSHA256, v2SHA)
		}
		if v1SHA != "" && plan.CompilationSHA256 == v1SHA {
			t.Fatal("v2 plan reused the superseded v1 compilation")
		}
	}
	if !foundV2Plan {
		t.Fatal("missing engineering plan bound to confirmed v2")
	}
	if len(view.DirectionChange.Checkpoints) == 0 {
		t.Fatal("direction checkpoints were not saved")
	}
	var sawDirty, sawUnstarted bool
	for _, checkpoint := range view.DirectionChange.Checkpoints {
		switch checkpoint.TaskID {
		case prep.PlannedTaskID:
			if checkpoint.Kind != core.DirectionCheckpointUnstarted {
				t.Fatalf("planned checkpoint = %#v", checkpoint)
			}
			sawUnstarted = true
		case prep.RunningTaskID:
			if checkpoint.Kind != core.DirectionCheckpointDirtyPreserved ||
				(!checkpoint.Dirty && !checkpoint.Untracked) ||
				checkpoint.WorktreePath != prep.DirtyRepo ||
				checkpoint.HeadSHA != prep.DirtyHeadSHA ||
				checkpoint.CandidateCommitID != "" ||
				checkpoint.SessionID != prep.BuilderSessionID {
				t.Fatalf("running checkpoint = %#v want DIRTY_PRESERVED path=%s head=%s session=%s",
					checkpoint, prep.DirtyRepo, prep.DirtyHeadSHA, prep.BuilderSessionID)
			}
			sawDirty = true
		}
	}
	if !sawDirty || !sawUnstarted {
		t.Fatalf("missing checkpoints dirty=%t unstarted=%t %#v", sawDirty, sawUnstarted, view.DirectionChange.Checkpoints)
	}
	v2Formal := 0
	for _, task := range view.DevelopmentTasks {
		if task.DevelopmentTask.ID == prep.PlannedTaskID || task.DevelopmentTask.ID == prep.RunningTaskID {
			if task.DevelopmentTask.Status != core.DevelopmentTaskStatusCancelled {
				t.Fatalf("v1 task %s status = %s", task.DevelopmentTask.ID, task.DevelopmentTask.Status)
			}
			continue
		}
		v2Formal++
	}
	if v2Formal != 0 {
		t.Fatalf("v2 formal tasks = %d", v2Formal)
	}
	dirtyAfter := gitEvidence(t, prep.DirtyRepo)
	if !strings.Contains(dirtyAfter["status"], "?? dirty.txt") {
		t.Fatalf("dirty worktree status = %q, want untracked dirty.txt", dirtyAfter["status"])
	}
	if dirtyAfter["head"] != dirtyBefore["head"] || dirtyAfter["head"] != prep.DirtyHeadSHA {
		t.Fatalf("dirty HEAD changed from %s to %s", dirtyBefore["head"], dirtyAfter["head"])
	}
	writeJSONFile(t, filepath.Join(evidenceDir, "direction-result.json"), map[string]any{
		"requirementId":      created.Requirement.ID,
		"directionRequestId": view.DirectionChange.Request.ID,
		"decisionRequestId":  view.DirectionChange.DecisionRequestID,
		"snapshotSha256":     view.DirectionChange.Gate.SnapshotSHA256,
		"phase":              view.DirectionChange.Phase,
		"checkpoints":        view.DirectionChange.Checkpoints,
		"events":             summarizeRequirementEvents(view),
		"dirtyRepo":          prep.DirtyRepo,
		"dirtyRepoBefore":    dirtyBefore,
		"dirtyRepoAfter":     dirtyAfter,
	})
	t.Logf("S05 direction change completed requirement=%s v2=%s display=%s evidence=%s electron=%d daemon=%d",
		created.Requirement.ID, v2ID, display, evidenceDir, electronPID, daemonPID)
}

func awaitDirectionPhase(t *testing.T, d *daemon, requirementID string, want core.DirectionChangePhase, timeout time.Duration) cleardevsvc.RequirementView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last cleardevsvc.RequirementView
	for time.Now().Before(deadline) {
		last = mustGetComplexE2E(t, d, requirementID)
		if last.DirectionChange != nil && last.DirectionChange.Phase == want {
			return last
		}
		if last.DirectionChange != nil {
			switch last.DirectionChange.Phase {
			case core.DirectionChangeNeedsHuman, core.DirectionChangeRejected:
				if last.DirectionChange.Phase != want {
					t.Fatalf("direction phase became %s while waiting for %s: %s\n%s", last.DirectionChange.Phase, want, complexE2EDebugJSON(last), d.tailLog())
				}
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out after %s waiting for direction phase %s: %s\n%s", timeout, want, complexE2EDebugJSON(last), d.tailLog())
	return last
}

func directionE2EAnswers(view cleardevsvc.RequirementView) map[string]any {
	if view.DirectionChange == nil || len(view.DirectionChange.CompilationRequests) == 0 {
		return map[string]any{}
	}
	request := view.DirectionChange.CompilationRequests[len(view.DirectionChange.CompilationRequests)-1]
	answers := make([]map[string]string, 0, len(view.DirectionChange.Questions))
	for _, question := range view.DirectionChange.Questions {
		if question.CompilationRequestID != request.ID {
			continue
		}
		text := "计入拒绝数量。"
		if strings.Contains(strings.ToLower(question.QuestionKey+question.Text), "duplicate") ||
			strings.Contains(question.Text, "重复") {
			text = "重复地址忽略不计，只保留第一次出现的记录。"
		}
		answers = append(answers, map[string]string{"questionKey": question.QuestionKey, "text": text})
	}
	return map[string]any{
		"compilationRequestId": request.ID,
		"clarificationRound":   request.ClarificationRound,
		"answers":              answers,
	}
}

func gitEvidence(t *testing.T, repo string) map[string]string {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return strings.TrimSpace(string(out)) + " " + err.Error()
		}
		return strings.TrimSpace(string(out))
	}
	return map[string]string{
		"head":      run("rev-parse", "HEAD"),
		"status":    run("status", "--porcelain=v1"),
		"worktrees": run("worktree", "list", "--porcelain"),
		"preserved": run("for-each-ref", "refs/ao/preserved/"),
	}
}

func summarizeRequirementEvents(view cleardevsvc.RequirementView) []map[string]any {
	out := make([]map[string]any, 0, len(view.Events))
	for _, event := range view.Events {
		out = append(out, map[string]any{
			"action":    event.Action,
			"subject":   event.SubjectType,
			"reason":    event.Reason,
			"createdAt": event.CreatedAt,
		})
	}
	return out
}

func mustEvidenceDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cleardev-s05-evidence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("S05 evidence directory %s", dir)
	return dir
}
