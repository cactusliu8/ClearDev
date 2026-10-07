//go:build !windows

package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"

	_ "modernc.org/sqlite"
)

type progressWorkspace struct {
	observation ports.WorkspaceObservation
}

func (w *progressWorkspace) ObserveWorkspace(context.Context, ports.WorkspaceInfo) (ports.WorkspaceObservation, error) {
	return w.observation, nil
}

type progressE2EPrep struct {
	PendingID   string
	CompletedID string
	StewardID   string
	BindingID   string
	ProjectID   string
	RepoPath    string
}

func TestTrustedProgressWithRealElectronAndCodex(t *testing.T) {
	requireE2E(t)
	requireElectronE2E(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("S10 real demonstration needs git: %v", err)
	}
	if err := exec.Command(codexBinary(), "login", "status").Run(); err != nil { //nolint:gosec // gated local Codex preflight
		t.Fatalf("Codex Chat authentication preflight failed: %v", err)
	}

	evidenceDir := mustProgressEvidenceDir(t)
	display, xvfbPID := startXvfbDisplay(t)
	homeDir := t.TempDir()
	repoPath := prepareProgressE2ERepo(t)
	dataDir := t.TempDir()
	runFile := filepath.Join(dataDir, "running.json")

	d := startDaemon(t, dataDir)
	projectID := registerStandardE2EProject(t, d, repoPath)
	setPermissions(t, d, projectID, "auto")
	d.stop()

	pendingID, completedID := seedProgressE2ERequirements(t, dataDir, projectID)

	d = startDaemon(t, dataDir)
	stewardID := spawn(t, d, map[string]any{
		"projectId": projectID, "kind": "orchestrator", "harness": "codex", "mode": "chat", "prompt": "",
	}).Session.ID
	d.awaitLiveController(stewardID, 90*time.Second)
	d.stop()

	bindingID := bindEndedProgressSteward(t, dataDir, completedID, stewardID)

	debugPort := freePort(t)
	electronCmd := startPackagedElectronWithDebug(t, display, homeDir, dataDir, runFile, evidenceDir, freePort(t), debugPort)
	d = attachDaemonFromRunFile(t, dataDir, runFile, 45*time.Second)
	waitSessionTerminated(t, d, stewardID, 30*time.Second)

	var progress cleardevsvc.ProjectProgressView
	d.mustCall("GET", "/cleardev/projects/"+projectID+"/progress", http.StatusOK, nil, &progress)
	if progress.AOProjectID != projectID || len(progress.Requirements) != 2 {
		t.Fatalf("project progress = %#v", progress)
	}
	if progress.Requirements[0].DevelopmentRequirementID != pendingID || progress.Requirements[0].Phase != core.TrustedPhaseAwaitingConfirmation {
		t.Fatalf("needs-human requirement was not first: %#v", progress.Requirements[0])
	}
	if progress.Requirements[1].DevelopmentRequirementID != completedID || progress.Requirements[1].Phase != core.TrustedPhaseCompleted {
		t.Fatalf("completed requirement was not second: %#v", progress.Requirements[1])
	}
	completed := progress.Requirements[1]
	if !completed.CanRequestExplanation {
		t.Fatalf("completed requirement cannot request an explanation: %#v", completed)
	}

	cdp := awaitProgressCDP(t, debugPort, 45*time.Second)
	defer cdp.close()
	mainWin := waitMainElectronWindow(t, display, electronCmd.Process.Pid, 30*time.Second)
	if err := cdp.evaluate(t, fmt.Sprintf("window.focus(); window.__s10ProgressMarker = 'keep'; location.hash = '#/projects/%s'; true", projectID)); err != nil {
		t.Fatalf("open project board: %v", err)
	}
	waitCDPText(t, cdp, `document.querySelector('[data-testid="cleardev-progress-open"]') ? 'yes' : ''`, "yes", 30*time.Second)
	if !tabUntil(t, cdp, `document.activeElement && document.activeElement.getAttribute('data-testid') === 'cleardev-progress-open'`, 80) {
		t.Fatalf("keyboard did not reach the ClearDev progress entry; debug=%s", cdpDebug(t, cdp))
	}
	activateFocusedControl(t, cdp, "cleardev-progress-open")
	waitCDPText(t, cdp, `document.querySelector('[data-testid="cleardev-progress-page"]') ? 'yes' : ''`, "yes", 30*time.Second)
	waitCDPText(t, cdp, fmt.Sprintf(`document.querySelector('[data-testid="cleardev-progress-requirement-%s"]') ? document.querySelector('[data-testid="cleardev-progress-requirement-%s"]').getAttribute('data-phase') : ''`, pendingID, pendingID), string(core.TrustedPhaseAwaitingConfirmation), 30*time.Second)
	waitCDPText(t, cdp, fmt.Sprintf(`document.querySelector('[data-testid="cleardev-progress-requirement-%s"]') ? document.querySelector('[data-testid="cleardev-progress-requirement-%s"]').getAttribute('data-phase') : ''`, completedID, completedID), string(core.TrustedPhaseCompleted), 30*time.Second)

	screenshotXWindow(t, display, mainWin.ID, filepath.Join(evidenceDir, "progress-before-explanation"))
	if mainWin.Width < 1000 || mainWin.Height < 600 {
		t.Fatalf("progress window %dx%d is smaller than a common desktop size", mainWin.Width, mainWin.Height)
	}

	waitCDPText(t, cdp, fmt.Sprintf(`document.querySelector('[data-testid="cleardev-progress-requirement-%s"] [data-testid="cleardev-progress-explain"]:not([disabled])') ? 'yes' : ''`, completedID), "yes", 15*time.Second)
	if err := cdp.evaluate(t, "window.__s10ProgressMarker = 'keep'"); err != nil {
		t.Fatalf("set CDC marker: %v", err)
	}
	if !tabUntil(t, cdp, fmt.Sprintf(`document.activeElement && document.activeElement.getAttribute('data-testid') === 'cleardev-progress-explain' && !document.activeElement.disabled && document.activeElement.closest('[data-testid="cleardev-progress-requirement-%s"]')`, completedID), 80) {
		t.Fatalf("keyboard did not reach the completed requirement explanation button; debug=%s", cdpDebug(t, cdp))
	}
	activateFocusedControl(t, cdp, "cleardev-progress-explain")

	explained := waitProgressExplanation(t, d, completedID, 8*time.Minute)
	if explained.TrustedProgress.Explanation == nil || explained.TrustedProgress.Explanation.Status != core.ProgressExplanationSettled {
		t.Fatalf("explanation = %#v", explained.TrustedProgress.Explanation)
	}
	note := explained.TrustedProgress.Explanation
	if note.Stale || note.Phase != string(explained.TrustedProgress.Phase) || note.Attention != string(explained.TrustedProgress.Attention) {
		t.Fatalf("settled explanation drifted from derived progress: note=%#v summary=%#v", note, explained.TrustedProgress)
	}
	if note.NextOwnerRole != explained.TrustedProgress.NextOwner.Role {
		t.Fatalf("explanation next owner %q != derived %q", note.NextOwnerRole, explained.TrustedProgress.NextOwner.Role)
	}
	wantPending := len(explained.TrustedProgress.PendingDecisions) > 0
	if note.PendingDecision != wantPending {
		t.Fatalf("explanation pendingDecision=%v want %v", note.PendingDecision, wantPending)
	}
	continuationID := findProgressContinuationSession(t, d, stewardID)
	if continuationID == "" || continuationID == stewardID {
		t.Fatalf("continuation session missing: original=%s sessions=%s", stewardID, d.debugSessions())
	}
	waitCDPText(t, cdp, fmt.Sprintf(`document.querySelector('[data-testid="cleardev-progress-requirement-%s"] [data-testid="cleardev-progress-explanation"]') ? document.querySelector('[data-testid="cleardev-progress-requirement-%s"] [data-testid="cleardev-progress-explanation"]').getAttribute('data-explanation-status') : ''`, completedID, completedID), "SETTLED", 30*time.Second)
	marker := cdp.mustEvaluate(t, "window.__s10ProgressMarker || ''")
	if marker != "keep" {
		t.Fatalf("progress page reloaded instead of CDC refresh; marker=%q", marker)
	}
	screenshotXWindow(t, display, mainWin.ID, filepath.Join(evidenceDir, "progress-after-explanation"))

	prep := progressE2EPrep{
		PendingID: pendingID, CompletedID: completedID, StewardID: stewardID,
		BindingID: bindingID, ProjectID: projectID, RepoPath: repoPath,
	}
	writeJSONFile(t, filepath.Join(evidenceDir, "process.json"), map[string]any{
		"electronBinary":  packagedElectronBinary(t),
		"electronVersion": electronVersion(t, packagedElectronBinary(t)),
		"electronPID":     electronCmd.Process.Pid,
		"daemonPID":       d.pgid,
		"xvfbPID":         xvfbPID,
		"display":         display,
		"dataDir":         dataDir,
		"debugPort":       debugPort,
	})
	writeJSONFile(t, filepath.Join(evidenceDir, "trusted-progress.json"), map[string]any{
		"prep":                prep,
		"before":              progress,
		"explained":           explained.TrustedProgress,
		"continuationSession": continuationID,
		"window":              mainWin,
	})
	t.Logf("S10 trusted progress completed pending=%s completed=%s continuation=%s evidence=%s",
		pendingID, completedID, continuationID, evidenceDir)
}

func seedProgressE2ERequirements(t *testing.T, dataDir, projectID string) (pendingID, completedID string) {
	t.Helper()
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open isolated store for S10 seed: %v", err)
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("close S10 seed store: %v", closeErr)
		}
	}()
	ctx := context.Background()
	now := time.Now().UTC()
	observer := &progressWorkspace{observation: ports.WorkspaceObservation{
		Path: "/managed/s10-complete", HeadSHA: "0123456789abcdef0123456789abcdef01234567",
	}}
	service := cleardevsvc.New(cleardevsvc.Deps{
		Facts: store, StandardFacts: store, HumanDecisions: store, AO: store,
		Workspace: observer, Human: allowStandardE2EHuman{},
	})

	pending, err := service.CreateRequirement(ctx, cleardevsvc.CreateRequirementInput{
		AOProjectID: projectID, Name: "待确认邮箱规则", RequirementText: "确认是否规范化邮箱域名。",
	})
	if err != nil {
		t.Fatalf("create pending requirement: %v", err)
	}
	if err := service.SubmitRequirementForConfirmation(ctx, pending.RequirementVersions[0].ID); err != nil {
		t.Fatalf("submit pending requirement: %v", err)
	}

	completed, err := service.CreateRequirement(ctx, cleardevsvc.CreateRequirementInput{
		AOProjectID: projectID, Name: "已完成快速任务", RequirementText: "实现核心路径并完成检查。",
	})
	if err != nil {
		t.Fatalf("create completed requirement: %v", err)
	}
	if err := service.SubmitRequirementForConfirmation(ctx, completed.RequirementVersions[0].ID); err != nil {
		t.Fatalf("submit completed requirement: %v", err)
	}
	if err := service.ConfirmRequirementVersion(ctx, completed.RequirementVersions[0].ID); err != nil {
		t.Fatalf("confirm completed requirement: %v", err)
	}
	builder, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: domain.ProjectID(projectID), Kind: domain.KindWorker, Harness: domain.HarnessCodex,
		Mode: domain.SessionModeChat, PermissionMode: domain.PermissionModeAuto,
		Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
		Metadata: domain.SessionMetadata{
			Branch: "main", WorkspacePath: "/managed/s10-complete", WorkspaceRepoPath: "/managed/s10-complete",
		},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create builder session for completed seed: %v", err)
	}
	task, err := service.CreateDevelopmentTask(ctx, completed.Requirement.ID, cleardevsvc.CreateDevelopmentTaskInput{
		Title: "implement core path", Mode: core.WorkModeQuick, MaxReworkCount: 1,
		Permissions: cleardevsvc.CreatePathPermissionsInput{
			WritePaths: []string{"backend/**"}, ForbiddenPaths: []string{"backend/secret/**"},
			SharedPathsRequireApproval: []string{"backend/shared/**"}, GeneratedPaths: []string{"backend/gen/**"},
		},
		RequiredChecks: []cleardevsvc.CreateRequiredCheckInput{{Name: "build", Kind: "command"}},
	})
	if err != nil {
		t.Fatalf("create completed task: %v", err)
	}
	if err := service.StartDevelopmentTask(ctx, task.ID); err != nil {
		t.Fatalf("start completed task: %v", err)
	}
	candidate, err := service.RegisterCandidate(ctx, task.ID, string(builder.ID))
	if err != nil {
		t.Fatalf("register completed candidate: %v", err)
	}
	if err := service.SubmitDevelopmentTaskReview(ctx, task.ID); err != nil {
		t.Fatalf("submit completed review: %v", err)
	}
	if _, err := service.EvaluateScope(ctx, task.ID, candidate.ID, candidate.CommitSHA, []string{"backend/main.go"}, nil); err != nil {
		t.Fatalf("scope evidence: %v", err)
	}
	if _, err := service.RecordRequiredCheckResult(ctx, task.ID, candidate.ID, candidate.CommitSHA, "build", core.EvidenceResultPass, nil); err != nil {
		t.Fatalf("required check evidence: %v", err)
	}
	if _, err := service.RecordDevelopmentTaskIntegrationResult(ctx, task.ID, candidate.ID, candidate.CommitSHA, core.EvidenceResultPass, nil); err != nil {
		t.Fatalf("task integration evidence: %v", err)
	}
	if err := service.CompleteDevelopmentTask(ctx, task.ID); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	integration, err := service.RegisterIntegrationCandidate(ctx, completed.Requirement.ID, string(builder.ID))
	if err != nil {
		t.Fatalf("register integration candidate: %v", err)
	}
	if _, err := service.RecordRequirementIntegrationResult(ctx, completed.Requirement.ID, integration.ID, integration.CommitSHA, core.EvidenceResultPass, nil); err != nil {
		t.Fatalf("requirement integration evidence: %v", err)
	}
	view, err := service.GetRequirement(ctx, completed.Requirement.ID)
	if err != nil || view.TrustedProgress.Phase != core.TrustedPhaseCompleted {
		t.Fatalf("seeded completed phase = %#v err=%v", view.TrustedProgress, err)
	}
	builder.IsTerminated = true
	builder.Activity.State = domain.ActivityExited
	if err := store.UpdateSession(ctx, builder); err != nil {
		t.Fatalf("terminate S10 seed builder session: %v", err)
	}
	return pending.Requirement.ID, completed.Requirement.ID
}

func bindEndedProgressSteward(t *testing.T, dataDir, requirementID, stewardSessionID string) string {
	t.Helper()
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open store to bind S10 steward: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	binding, created, err := store.StartClearDevStandardFlow(ctx, core.StartStandardFlowCommand{
		DevelopmentRequirementID: requirementID, StewardRoleBindingID: "s10-steward-binding",
		StewardSessionIdempotencyKey: "s10-steward-key", At: now,
	})
	if err != nil || !created {
		_ = store.Close()
		t.Fatalf("start S10 steward binding = %+v created=%v err=%v", binding, created, err)
	}
	ok, err := store.BindClearDevRoleBinding(ctx, binding.ID, stewardSessionID, now.Add(time.Second))
	if err != nil || !ok {
		_ = store.Close()
		t.Fatalf("bind S10 steward = %v err=%v", ok, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store after S10 steward bind: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("reopen sqlite to end S10 steward: %v", err)
	}
	defer func() { _ = db.Close() }()
	queries := gen.New(db)
	rows, err := queries.EndClearDevStandardRoleBindingCAS(ctx, gen.EndClearDevStandardRoleBindingCASParams{
		ReasonCode: "STEWARD_SESSION_ENDED",
		EndedAt:    sql.NullTime{Time: now.Add(2 * time.Second), Valid: true},
		ID:         binding.ID,
	})
	if err != nil || rows != 1 {
		t.Fatalf("end S10 steward binding rows=%d err=%v", rows, err)
	}
	return binding.ID
}

func killSessionIfLive(t *testing.T, d *daemon, sessionID string) {
	t.Helper()
	var read struct {
		Session struct {
			IsTerminated bool   `json:"isTerminated"`
			Status       string `json:"status"`
			Activity     struct {
				State string `json:"state"`
			} `json:"activity"`
		} `json:"session"`
	}
	status, err := d.call("GET", "/sessions/"+sessionID, nil, &read)
	if err != nil || status != http.StatusOK {
		return
	}
	if progressSessionEnded(read.Session.IsTerminated, read.Session.Status, read.Session.Activity.State) {
		return
	}
	d.mustCall("POST", "/sessions/"+sessionID+"/kill", http.StatusOK, nil, nil)
}

func waitSessionTerminated(t *testing.T, d *daemon, sessionID string, timeout time.Duration) {
	t.Helper()
	killSessionIfLive(t, d, sessionID)
	deadline := time.Now().Add(timeout)
	var last struct {
		IsTerminated bool
		Status       string
		Activity     string
	}
	for time.Now().Before(deadline) {
		var read struct {
			Session struct {
				IsTerminated bool   `json:"isTerminated"`
				Status       string `json:"status"`
				Activity     struct {
					State string `json:"state"`
				} `json:"activity"`
			} `json:"session"`
		}
		status, err := d.call("GET", "/sessions/"+sessionID, nil, &read)
		if err == nil && status == http.StatusOK {
			last.IsTerminated = read.Session.IsTerminated
			last.Status = read.Session.Status
			last.Activity = read.Session.Activity.State
			if progressSessionEnded(last.IsTerminated, last.Status, last.Activity) {
				return
			}
		}
		killSessionIfLive(t, d, sessionID)
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("original steward %s was not clearly ended: terminated=%v status=%q activity=%q", sessionID, last.IsTerminated, last.Status, last.Activity)
}

func progressSessionEnded(terminated bool, status, activity string) bool {
	return terminated || status == "terminated" || status == "exited" || activity == "exited"
}

func findProgressContinuationSession(t *testing.T, d *daemon, original string) string {
	t.Helper()
	var list struct {
		Sessions []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"sessions"`
	}
	d.mustCall("GET", "/sessions", http.StatusOK, nil, &list)
	for _, session := range list.Sessions {
		if session.Kind == "orchestrator" && session.ID != original {
			return session.ID
		}
	}
	return ""
}

func waitProgressExplanation(t *testing.T, d *daemon, requirementID string, timeout time.Duration) cleardevsvc.RequirementView {
	t.Helper()
	startedAt := time.Now()
	deadline := startedAt.Add(timeout)
	var last cleardevsvc.RequirementView
	for time.Now().Before(deadline) {
		d.mustCall("GET", "/cleardev/requirements/"+requirementID, http.StatusOK, nil, &last)
		if last.TrustedProgress.Explanation != nil {
			switch last.TrustedProgress.Explanation.Status {
			case core.ProgressExplanationSettled:
				return last
			case core.ProgressExplanationFailed:
				t.Fatalf("progress explanation failed: %#v\n%s", last.TrustedProgress.Explanation, d.tailLog())
			}
		} else if time.Since(startedAt) > 45*time.Second {
			t.Fatalf("progress page did not start an explanation request\n%s", d.tailLog())
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for progress explanation: %#v\n%s", last.TrustedProgress.Explanation, d.tailLog())
	return last
}

func prepareProgressE2ERepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("S10 trusted progress fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=s10", "GIT_AUTHOR_EMAIL=s10@example.com",
			"GIT_COMMITTER_NAME=s10", "GIT_COMMITTER_EMAIL=s10@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.name", "ClearDev E2E")
	run("config", "user.email", "cleardev-e2e@example.invalid")
	run("add", "README.md")
	run("commit", "-m", "init")
	run("remote", "add", "origin", ".")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return dir
}

func mustProgressEvidenceDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cleardev-s10-evidence-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("S10 evidence directory %s", dir)
	return dir
}

func startPackagedElectronWithDebug(t *testing.T, display, home, dataDir, runFile, evidenceDir string, port, debugPort int) *exec.Cmd {
	t.Helper()
	binary := packagedElectronBinary(t)
	codexHome := seedIsolatedCodexHome(t, home)
	logPath := filepath.Join(evidenceDir, "electron.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--no-sandbox", "--remote-debugging-port="+strconv.Itoa(debugPort), "--remote-allow-origins=*")
	cmd.Dir = filepath.Dir(binary)
	cmd.Env = electronX11Env(display, map[string]string{
		"HOME":        home,
		"AO_DATA_DIR": dataDir,
		"AO_RUN_FILE": runFile,
		"AO_PORT":     strconv.Itoa(port),
		"CODEX_HOME":  codexHome,
	})
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start packaged Electron: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	})
	t.Logf("packaged Electron binary=%s version=%s pid=%d display=%s debug=%d log=%s",
		binary, electronVersion(t, binary), cmd.Process.Pid, display, debugPort, logPath)
	return cmd
}

func waitMainElectronWindow(t *testing.T, display string, pid int, timeout time.Duration) xWindow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []xWindow
	for time.Now().Before(deadline) {
		last = inspectXWindows(t, display, pid)
		var best xWindow
		bestArea := 0
		for _, win := range last {
			area := win.Width * win.Height
			if win.Width >= 800 && win.Height >= 500 && area > bestArea {
				best = win
				bestArea = area
			}
		}
		if bestArea > 0 {
			return best
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("main Electron window did not appear on DISPLAY=%s; windows=%s", display, mustJSON(last))
	return xWindow{}
}

type progressCDP struct {
	conn *websocket.Conn
	mu   sync.Mutex
	next int64
}

func awaitProgressCDP(t *testing.T, port int, timeout time.Duration) *progressCDP {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		targets, err := listCDPTargets(port)
		if err == nil {
			for _, target := range preferredCDPTargets(targets) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				conn, _, dialErr := websocket.Dial(ctx, target.WebSocketDebuggerURL, nil)
				cancel()
				if dialErr != nil {
					lastErr = dialErr
					continue
				}
				client := &progressCDP{conn: conn}
				if _, evalErr := client.evaluateValue(t, "document.readyState"); evalErr == nil {
					_, _ = client.call("Page.bringToFront", map[string]any{})
					return client
				}
				_ = conn.Close(websocket.StatusNormalClosure, "")
			}
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("Electron CDP was not ready on port %d: %v", port, lastErr)
	return nil
}

type cdpTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func preferredCDPTargets(targets []cdpTarget) []cdpTarget {
	var preferred, rest []cdpTarget
	for _, target := range targets {
		if target.WebSocketDebuggerURL == "" || target.Type != "page" {
			continue
		}
		if strings.HasPrefix(target.URL, "devtools://") || strings.HasPrefix(target.URL, "chrome-error://") {
			continue
		}
		if strings.Contains(target.URL, "index.html") || strings.HasPrefix(target.URL, "file:") || strings.HasPrefix(target.URL, "app:") {
			preferred = append(preferred, target)
			continue
		}
		if target.URL != "about:blank" {
			rest = append(rest, target)
		}
	}
	return append(preferred, rest...)
}

func listCDPTargets(port int) ([]cdpTarget, error) {
	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cdp list status %d", res.StatusCode)
	}
	var targets []cdpTarget
	if err := json.NewDecoder(res.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func (c *progressCDP) close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

func (c *progressCDP) call(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := c.next
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, c.conn, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		var msg struct {
			ID     int64           `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := wsjson.Read(ctx, c.conn, &msg); err != nil {
			return nil, err
		}
		if msg.ID != id {
			continue
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, fmt.Errorf("cdp %s: %s", method, msg.Error)
		}
		return msg.Result, nil
	}
}

func (c *progressCDP) evaluate(t *testing.T, expression string) error {
	t.Helper()
	_, err := c.evaluateValue(t, expression)
	return err
}

func (c *progressCDP) evaluateValue(t *testing.T, expression string) (string, error) {
	t.Helper()
	raw, err := c.call("Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  false,
	})
	if err != nil {
		return "", err
	}
	var result struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if len(result.ExceptionDetails) > 0 && string(result.ExceptionDetails) != "null" {
		return "", fmt.Errorf("evaluate %s: %s", expression, result.ExceptionDetails)
	}
	switch value := result.Result.Value.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

func (c *progressCDP) mustEvaluate(t *testing.T, expression string) string {
	t.Helper()
	value, err := c.evaluateValue(t, expression)
	if err != nil {
		t.Fatalf("evaluate %s: %v", expression, err)
	}
	return value
}

func waitCDPText(t *testing.T, cdp *progressCDP, expression, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		value, err := cdp.evaluateValue(t, expression)
		if err == nil && value == want {
			return
		}
		if err == nil {
			last = value
		} else {
			last = err.Error()
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("cdp wait %s: got %q want %q debug=%s", expression, last, want, cdpDebug(t, cdp))
}

func tabUntil(t *testing.T, cdp *progressCDP, predicate string, maxTabs int) bool {
	t.Helper()
	for i := 0; i < maxTabs; i++ {
		ok, err := cdp.evaluateValue(t, "!!("+predicate+")")
		if err == nil && ok == "true" {
			return true
		}
		pressKey(t, cdp, "Tab", "Tab", 9)
		time.Sleep(80 * time.Millisecond)
	}
	ok, _ := cdp.evaluateValue(t, "!!("+predicate+")")
	return ok == "true"
}

func pressKey(t *testing.T, cdp *progressCDP, key, code string, windowsCode int) {
	t.Helper()
	params := map[string]any{
		"type":                  "rawKeyDown",
		"key":                   key,
		"code":                  code,
		"windowsVirtualKeyCode": windowsCode,
		"nativeVirtualKeyCode":  windowsCode,
	}
	if _, err := cdp.call("Input.dispatchKeyEvent", params); err != nil {
		t.Fatalf("keyDown %s: %v", key, err)
	}
	params["type"] = "keyUp"
	if _, err := cdp.call("Input.dispatchKeyEvent", params); err != nil {
		t.Fatalf("keyUp %s: %v", key, err)
	}
}

func activateFocusedControl(t *testing.T, cdp *progressCDP, testID string) {
	t.Helper()
	clicked, err := cdp.evaluateValue(t, fmt.Sprintf(`(function() {
		var el = document.activeElement;
		if (!el || el.getAttribute("data-testid") !== %q || el.disabled) {
			return "not-focused:" + (el && (el.getAttribute("data-testid") || el.tagName) || "none");
		}
		el.click();
		return "clicked";
	})()`, testID))
	if err != nil {
		t.Fatalf("activate %s: %v debug=%s", testID, err, cdpDebug(t, cdp))
	}
	if clicked != "clicked" {
		t.Fatalf("activate %s: %s debug=%s", testID, clicked, cdpDebug(t, cdp))
	}
}

func cdpDebug(t *testing.T, cdp *progressCDP) string {
	t.Helper()
	value, err := cdp.evaluateValue(t, `(function() {
		var active = document.activeElement;
		var buttons = Array.from(document.querySelectorAll("[data-testid]")).map(function(el) { return el.getAttribute("data-testid"); }).slice(0, 24);
		return JSON.stringify({
			hash: location.hash,
			title: document.title,
			ready: document.readyState,
			active: active && (active.getAttribute("data-testid") || active.tagName),
			testids: buttons
		});
	})()`)
	if err != nil {
		return err.Error()
	}
	return value
}
