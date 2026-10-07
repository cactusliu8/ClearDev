package cleardevdemo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

const (
	demoName            = "完整演示邮件名单"
	planningTimeout     = 20 * time.Minute
	planningLongTimeout = 25 * time.Minute
	executionTimeout    = 60 * time.Minute
	explanationTimeout  = 12 * time.Minute
)

// Result is the isolated demonstration tree plus the offline evidence pack.
type Result struct {
	Layout   Layout
	Evidence Evidence
}

// Run executes one isolated full demonstration. Callers cannot choose mode,
// tasks, paths, checks, sessions, candidates, or completion state.
func Run(ctx context.Context, opts Options) (Result, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Minute)
		defer cancel()
	}
	started := opts.now()
	if err := Preflight(ctx, opts); err != nil {
		return Result{}, err
	}
	layout, err := allocateLayout(opts)
	if err != nil {
		return Result{}, err
	}
	group := &ProcessGroup{}
	defer group.Kill()

	result, runErr := runDemo(ctx, opts, layout, group, started)
	if runErr != nil {
		preserveStartupRecord(layout)
		_, cleanupErr := finalizeDemoCleanup(layout, group)
		failureText := runErr.Error()
		if cleanupErr != nil {
			failureText += "\ncleanup: " + cleanupErr.Error()
		}
		_ = os.WriteFile(filepath.Join(layout.EvidenceDir, "error.txt"), []byte(failureText+"\n"), 0o600)
		opts.logErr("demonstration failed; evidence kept at %s", layout.EvidenceDir)
		if cleanupErr != nil {
			return Result{Layout: layout}, errors.Join(runErr, fmt.Errorf("cleanup failed: %w", cleanupErr))
		}
		return Result{Layout: layout}, runErr
	}
	_, err = finalizeDemoCleanup(layout, group)
	if err != nil {
		_ = os.WriteFile(filepath.Join(layout.EvidenceDir, "error.txt"), []byte(err.Error()+"\n"), 0o600)
		return Result{Layout: layout}, err
	}
	cleanupHash, err := sha256File(filepath.Join(layout.EvidenceDir, "cleanup.json"))
	if err != nil {
		return Result{Layout: layout}, err
	}
	result.Evidence.CleanupSHA256 = cleanupHash
	if err := writeJSON(filepath.Join(layout.EvidenceDir, "evidence.json"), result.Evidence); err != nil {
		return Result{Layout: layout}, err
	}
	if _, err := VerifyEvidenceDir(layout.EvidenceDir); err != nil {
		return Result{Layout: layout}, err
	}
	opts.log("evidence %s", layout.EvidenceDir)
	return result, nil
}

func preserveStartupRecord(layout Layout) {
	raw, err := os.ReadFile(layout.StartupFile) //nolint:gosec // isolated startup record copied into retained evidence
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(layout.EvidenceDir, evidenceStartupFile), raw, 0o600)
}

func finalizeDemoCleanup(layout Layout, group *ProcessGroup) (CleanupEvidence, error) {
	group.Kill()
	cleanup := CleanupEvidence{
		SchemaVersion:    1,
		ProcessesStopped: !group.anyAlive(),
		CheckedPorts:     []int{layout.Port, layout.DebugPort, layout.AppPort, layout.VNCPort},
	}
	cleanup.PortsClosed = waitPortsClosed(cleanup.CheckedPorts, 5*time.Second)
	removeErr := removeDemoWork(layout)
	cleanup.WorkDirectoriesGone = demoWorkDirectoriesGone(layout)
	if removeErr != nil {
		cleanup.FailureReason = removeErr.Error()
	} else if !cleanup.ProcessesStopped || !cleanup.PortsClosed || !cleanup.WorkDirectoriesGone {
		cleanup.FailureReason = fmt.Sprintf(
			"processesStopped=%t portsClosed=%t workDirectoriesGone=%t",
			cleanup.ProcessesStopped,
			cleanup.PortsClosed,
			cleanup.WorkDirectoriesGone,
		)
	}
	if err := writeJSON(filepath.Join(layout.EvidenceDir, evidenceCleanupFile), cleanup); err != nil {
		return cleanup, err
	}
	if cleanup.FailureReason != "" {
		return cleanup, fmt.Errorf("demonstration cleanup failed: %s", cleanup.FailureReason)
	}
	return cleanup, nil
}

func (o Options) log(format string, args ...any) {
	_, _ = fmt.Fprintf(o.stdout(), format+"\n", args...)
}

func (o Options) logErr(format string, args ...any) {
	_, _ = fmt.Fprintf(o.stderr(), format+"\n", args...)
}

func runDemo(ctx context.Context, opts Options, layout Layout, group *ProcessGroup, started time.Time) (Result, error) {
	opts.log("isolated root %s", layout.Root)
	packaged, err := inspectPackagedRuntime(opts)
	if err != nil {
		return Result{}, err
	}
	buildSourceRaw, err := os.ReadFile(packaged.BuildSourcePath) //nolint:gosec // packaged local build metadata
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(layout.EvidenceDir, buildSourceFileName), buildSourceRaw, 0o600); err != nil {
		return Result{}, err
	}
	prdText, err := core.FrozenDemoPRD()
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(layout.EvidenceDir, "prd.md"), []byte(prdText+"\n"), 0o600); err != nil {
		return Result{}, err
	}
	templateCommit, err := initGitRepo(layout.RepoDir)
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(layout.EvidenceDir, "template-commit.txt"), []byte(templateCommit+"\n"), 0o600); err != nil {
		return Result{}, err
	}
	imageID, err := frozenCheckImageID(ctx, opts)
	if err != nil {
		return Result{}, err
	}

	display, xvfb, err := startXvfb(layout.EvidenceDir)
	if err != nil {
		return Result{}, err
	}
	group.add(xvfb.Process.Pid)
	vnc, err := startVNC(layout, display)
	if err != nil {
		return Result{}, err
	}
	group.add(vnc.Process.Pid)
	hostDisplay := strings.TrimSpace(opts.HostDisplay)
	if hostDisplay == "" {
		hostDisplay = strings.TrimSpace(os.Getenv("DISPLAY"))
	}
	viewer, viewerWindow, viewerExecutable, err := startVisibleViewer(opts, layout, hostDisplay)
	if err != nil {
		return Result{}, err
	}
	group.add(viewer.Process.Pid)
	electronCmd, err := startPackagedElectron(opts, layout, display, layout.EvidenceDir, layout.DebugPort)
	if err != nil {
		return Result{}, err
	}
	group.add(electronCmd.Process.Pid)
	daemonPID, daemonPort, err := waitDesktopStartup(layout, electronCmd, 45*time.Second)
	if err != nil {
		return Result{}, err
	}
	group.add(daemonPID)
	if daemonPort != layout.Port {
		opts.log("daemon bound 127.0.0.1:%d (requested %d)", daemonPort, layout.Port)
	}
	if err := waitDaemonHTTP(ctx, daemonPort, daemonPID, 30*time.Second); err != nil {
		return Result{}, err
	}
	if err := waitLargeWindow(display, electronCmd.Process.Pid, 15*time.Second); err != nil {
		return Result{}, err
	}
	_, _ = xdotool(hostDisplay, "windowactivate", "--sync", viewerWindow.ID)
	time.Sleep(500 * time.Millisecond)
	visibleScreenshot := filepath.Join(layout.EvidenceDir, "visible-desktop")
	if err := screenshotDesktop(display, visibleScreenshot); err != nil {
		return Result{}, fmt.Errorf("capture desktop streamed to visible VNC viewer: %w", err)
	}
	visibleScreenshotHash, err := sha256File(visibleScreenshot + ".xwd")
	if err != nil {
		return Result{}, err
	}
	visibility := VisibilityEvidence{
		SchemaVersion:      1,
		HostDisplay:        hostDisplay,
		VNCAddress:         fmt.Sprintf("127.0.0.1:%d", layout.VNCPort),
		PasswordProtected:  true,
		ViewerExecutable:   viewerExecutable,
		ViewerWindowWidth:  viewerWindow.Width,
		ViewerWindowHeight: viewerWindow.Height,
		ScreenshotSource:   "vnc-streamed-xvfb-root",
		ScreenshotSHA256:   visibleScreenshotHash,
	}
	if err := writeJSON(filepath.Join(layout.EvidenceDir, "visibility.json"), visibility); err != nil {
		return Result{}, err
	}
	visibilityHash, err := sha256File(filepath.Join(layout.EvidenceDir, "visibility.json"))
	if err != nil {
		return Result{}, err
	}
	startupRaw, err := os.ReadFile(layout.StartupFile) //nolint:gosec // isolated startup record
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(layout.EvidenceDir, "desktop-startup.json"), startupRaw, 0o600); err != nil {
		return Result{}, err
	}
	startupHash, err := sha256File(filepath.Join(layout.EvidenceDir, "desktop-startup.json"))
	if err != nil {
		return Result{}, err
	}
	client := newAPIClient(daemonPort)
	beforeWindows := listedWindows(display, electronCmd.Process.Pid)

	projectID, err := registerDemoProject(ctx, client, layout.RepoDir)
	if err != nil {
		return Result{}, err
	}
	var created cleardevsvc.RequirementView
	if err := client.post(ctx, "/cleardev/requirements/complex", map[string]any{
		"aoProjectId": projectID, "name": demoName, "prdText": prdText,
	}, &created, 201); err != nil {
		return Result{}, fmt.Errorf("submit frozen PRD: %w", err)
	}
	requirementID := created.Requirement.ID
	if requirementID == "" {
		return Result{}, fmt.Errorf("complex create returned no requirement id")
	}
	opts.log("requirement %s", requirementID)

	view, err := waitView(ctx, client, requirementID, planningTimeout, func(view cleardevsvc.RequirementView) (bool, error) {
		if err := planningStopped(view); err != nil {
			return false, err
		}
		if view.ComplexPlanning == nil {
			return false, nil
		}
		switch view.ComplexPlanning.Phase {
		case core.ComplexPlanningAwaitingClarification, core.ComplexPlanningAwaitingConfirmation:
			return true, nil
		default:
			return false, nil
		}
	})
	if err != nil {
		return Result{}, err
	}
	if view.ComplexPlanning.Phase == core.ComplexPlanningAwaitingClarification {
		answers, answerErr := clarificationAnswers(view)
		if answerErr != nil {
			return Result{}, answerErr
		}
		if err := client.post(ctx, "/cleardev/requirements/"+url.PathEscape(requirementID)+"/complex-clarifications", answers, &view, 200); err != nil {
			return Result{}, fmt.Errorf("submit frozen answers: %w", err)
		}
		view, err = waitPlanning(ctx, client, requirementID, core.ComplexPlanningAwaitingConfirmation, planningTimeout)
		if err != nil {
			return Result{}, err
		}
	}
	opts.log("waiting for native v1 confirm dialog")
	if err := clickNativeApprove(
		display,
		filepath.Join(layout.EvidenceDir, "confirm-dialog"),
		beforeWindows,
		"Confirm requirement version v1",
		electronCmd.Process.Pid,
	); err != nil {
		return Result{}, err
	}
	view, err = waitPlanning(ctx, client, requirementID, core.ComplexPlanningApproved, planningLongTimeout)
	if err != nil {
		return Result{}, err
	}
	if err := assertApprovedV1(view, prdText); err != nil {
		return Result{}, err
	}
	opts.log("starting execution with an empty body")
	if err := client.post(ctx, "/cleardev/requirements/"+url.PathEscape(requirementID)+"/execution-runs", nil, &view, 202); err != nil {
		return Result{}, fmt.Errorf("start execution: %w", err)
	}
	view, err = waitCompleted(ctx, client, requirementID, executionTimeout)
	if err != nil {
		return Result{}, err
	}
	if err := assertParallelCompletion(view); err != nil {
		return Result{}, err
	}
	integrationSHA := view.ComplexExecution.Integration.CandidateCommitSHA
	opts.log("combination commit %s", integrationSHA)
	sample, err := verifyFinalApp(ctx, layout, integrationSHA, group, client, projectID, browserPreviewSessionID(compactSessions(view)))
	if err != nil {
		return Result{}, err
	}
	if err := inspectProgressPage(ctx, layout, display, electronCmd.Process.Pid, projectID, requirementID); err != nil {
		return Result{}, err
	}
	if err := client.post(ctx, "/cleardev/requirements/"+url.PathEscape(requirementID)+"/progress-explanations", nil, &view, 202); err != nil {
		return Result{}, fmt.Errorf("request progress explanation: %w", err)
	}
	view, err = waitExplanation(ctx, client, requirementID, explanationTimeout)
	if err != nil {
		return Result{}, err
	}

	finished := opts.now()
	evidence, err := writeEvidence(layout, view, Evidence{
		SchemaVersion:        EvidenceSchemaV2,
		StartedAt:            started.Format(time.RFC3339),
		FinishedAt:           finished.Format(time.RFC3339),
		DurationMS:           finished.Sub(started).Milliseconds(),
		TemplateCommit:       templateCommit,
		CheckImageID:         strings.TrimSpace(imageID),
		Sample:               sample,
		BuildCandidateCommit: packaged.BuildSource.CandidateCommit,
		BuildSourceClean:     packaged.BuildSource.SourceClean,
		BuildSourceSHA256:    packaged.BuildSourceSHA256,
		RuntimeSHA256:        packaged.RuntimeSHA256,
		StartupSHA256:        startupHash,
		VisibilitySHA256:     visibilityHash,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Layout: layout, Evidence: evidence}, nil
}

func frozenCheckImageID(ctx context.Context, opts Options) (string, error) {
	out, err := opts.commandOutput(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", core.StandardCandidateCheckImage)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func registerDemoProject(ctx context.Context, client *apiClient, repo string) (string, error) {
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := client.post(ctx, "/projects", map[string]any{"path": repo}, &created, 201); err != nil {
		return "", fmt.Errorf("register project: %w", err)
	}
	if created.Project.ID == "" {
		return "", fmt.Errorf("project registration returned no id")
	}
	if err := client.put(ctx, "/projects/"+url.PathEscape(created.Project.ID)+"/config", map[string]any{
		"config": map[string]any{
			"defaultBranch": "main",
			"agentConfig":   map[string]any{"permissions": "auto"},
		},
	}, nil); err != nil {
		return "", fmt.Errorf("set project config: %w", err)
	}
	return created.Project.ID, nil
}

func waitPlanning(ctx context.Context, client *apiClient, id string, want core.ComplexPlanningPhase, timeout time.Duration) (cleardevsvc.RequirementView, error) {
	return waitView(ctx, client, id, timeout, func(view cleardevsvc.RequirementView) (bool, error) {
		if err := planningStopped(view); err != nil {
			return false, err
		}
		return view.ComplexPlanning != nil && view.ComplexPlanning.Phase == want, nil
	})
}

func waitCompleted(ctx context.Context, client *apiClient, id string, timeout time.Duration) (cleardevsvc.RequirementView, error) {
	return waitView(ctx, client, id, timeout, func(view cleardevsvc.RequirementView) (bool, error) {
		if err := executionStopped(view); err != nil {
			return false, err
		}
		if view.ComplexExecution == nil || view.ComplexExecution.Integration == nil {
			return false, nil
		}
		return view.TrustedProgress.Phase == core.TrustedPhaseCompleted &&
			view.ComplexExecution.Phase == core.ComplexExecutionCompleted, nil
	})
}

func waitExplanation(ctx context.Context, client *apiClient, id string, timeout time.Duration) (cleardevsvc.RequirementView, error) {
	return waitView(ctx, client, id, timeout, func(view cleardevsvc.RequirementView) (bool, error) {
		note := view.TrustedProgress.Explanation
		if note == nil {
			return false, nil
		}
		if note.Status == core.ProgressExplanationFailed {
			return false, fmt.Errorf("progress explanation failed: %s", note.ReasonCode)
		}
		return note.Status == core.ProgressExplanationSettled && !note.Stale, nil
	})
}

func waitView(ctx context.Context, client *apiClient, id string, timeout time.Duration, ready func(cleardevsvc.RequirementView) (bool, error)) (cleardevsvc.RequirementView, error) {
	deadline := time.Now().Add(timeout)
	var last cleardevsvc.RequirementView
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		var view cleardevsvc.RequirementView
		if err := client.get(ctx, "/cleardev/requirements/"+url.PathEscape(id), &view); err != nil {
			last = view
			time.Sleep(2 * time.Second)
			continue
		}
		last = view
		ok, err := ready(view)
		if err != nil {
			return view, err
		}
		if ok {
			return view, nil
		}
		time.Sleep(2 * time.Second)
	}
	return last, fmt.Errorf("timed out after %s; last phase=%s trusted=%s", timeout, planningPhase(last), last.TrustedProgress.Phase)
}

func planningPhase(view cleardevsvc.RequirementView) string {
	if view.ComplexPlanning == nil {
		return ""
	}
	return string(view.ComplexPlanning.Phase)
}

func planningStopped(view cleardevsvc.RequirementView) error {
	if view.ComplexPlanning == nil {
		return nil
	}
	switch view.ComplexPlanning.Phase {
	case core.ComplexPlanningNeedsHuman, core.ComplexPlanningRejected:
		return fmt.Errorf("planning stopped at %s (%s)", view.ComplexPlanning.Phase, view.ComplexPlanning.ReasonCode)
	}
	return nil
}

func executionStopped(view cleardevsvc.RequirementView) error {
	if err := planningStopped(view); err != nil {
		return err
	}
	if view.ComplexExecution == nil {
		return nil
	}
	switch view.ComplexExecution.Phase {
	case core.ComplexExecutionNeedsHuman, core.ComplexExecutionBlocked:
		return fmt.Errorf("execution stopped at %s (%s)", view.ComplexExecution.Phase, view.ComplexExecution.PhaseReason)
	}
	return nil
}

func assertApprovedV1(view cleardevsvc.RequirementView, prdText string) error {
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		return fmt.Errorf("expected APPROVED planning, got %s", planningPhase(view))
	}
	if len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed || view.RequirementVersions[0].Version != 1 {
		return fmt.Errorf("expected confirmed v1, got %#v", view.RequirementVersions)
	}
	if strings.TrimSpace(view.RequirementVersions[0].RequirementText) == strings.TrimSpace(prdText) {
		return fmt.Errorf("confirmed v1 reused the original PRD text")
	}
	if len(view.ComplexPlanning.Plans) == 0 || len(view.ComplexPlanning.Reviews) == 0 {
		return fmt.Errorf("approved plan or review is missing")
	}
	return nil
}

func assertParallelCompletion(view cleardevsvc.RequirementView) error {
	exec := view.ComplexExecution
	if exec == nil || exec.Integration == nil {
		return fmt.Errorf("completed execution is missing integration")
	}
	if exec.Run.Mode != core.WorkModeParallel || exec.Run.FixedBuilderCount != 2 {
		return fmt.Errorf("mode=%s builders=%d, want PARALLEL with two builders", exec.Run.Mode, exec.Run.FixedBuilderCount)
	}
	if len(exec.Integration.CandidateCommitSHA) != 40 {
		return fmt.Errorf("integration commit %q is not a full SHA", exec.Integration.CandidateCommitSHA)
	}
	overlapped := false
	for _, batch := range exec.Batches {
		if len(batch.TaskKeys) >= 2 {
			overlapped = true
			break
		}
	}
	if !overlapped {
		overlapped = dispatchesOverlap(exec.Dispatches)
	}
	if !overlapped {
		return fmt.Errorf("parallel run did not record overlapping independent tasks")
	}
	if !hasPassingSpecialist(exec, "sqlite-migration-specialist") {
		return fmt.Errorf("missing passing sqlite-migration-specialist result")
	}
	return nil
}

func dispatchesOverlap(dispatches []core.ComplexExecutionDispatch) bool {
	for i, a := range dispatches {
		if a.Round != 0 || a.SettledAt == nil {
			continue
		}
		for _, b := range dispatches[i+1:] {
			if b.Round != 0 || b.SettledAt == nil || a.ComplexExecutionTaskID == b.ComplexExecutionTaskID {
				continue
			}
			if a.CreatedAt.Before(*b.SettledAt) && b.CreatedAt.Before(*a.SettledAt) {
				return true
			}
		}
	}
	return false
}

func hasPassingSpecialist(exec *core.ComplexExecutionSnapshot, checkID string) bool {
	if exec.Exception == nil {
		return false
	}
	for _, check := range exec.Exception.SpecialistChecks {
		if check.CheckID == checkID && check.Result == core.EvidenceResultPass {
			return true
		}
	}
	return false
}

func inspectProgressPage(ctx context.Context, layout Layout, display string, electronPID int, projectID, requirementID string) error {
	cdp, err := dialCDP(ctx, layout.DebugPort, 45*time.Second)
	if err != nil {
		return err
	}
	defer cdp.close()
	expr := fmt.Sprintf("window.focus(); location.hash = '#/projects/%s'; true", projectID)
	if _, err := cdp.evaluate(ctx, expr); err != nil {
		return fmt.Errorf("open project board: %w", err)
	}
	if err := cdp.waitText(ctx, `document.querySelector('[data-testid="cleardev-progress-open"]') ? 'yes' : ''`, "yes", 30*time.Second); err != nil {
		return fmt.Errorf("progress entry: %w", err)
	}
	if _, err := cdp.evaluate(ctx, `(function(){ var el = document.querySelector('[data-testid="cleardev-progress-open"]'); if (el) { el.click(); } return 'ok'; })()`); err != nil {
		return err
	}
	if err := cdp.waitText(ctx, `document.querySelector('[data-testid="cleardev-progress-page"]') ? 'yes' : ''`, "yes", 30*time.Second); err != nil {
		return fmt.Errorf("progress page: %w", err)
	}
	phaseExpr := fmt.Sprintf(`document.querySelector('[data-testid="cleardev-progress-requirement-%s"]') ? document.querySelector('[data-testid="cleardev-progress-requirement-%s"]').getAttribute('data-phase') : ''`, requirementID, requirementID)
	if err := cdp.waitText(ctx, phaseExpr, string(core.TrustedPhaseCompleted), 30*time.Second); err != nil {
		return fmt.Errorf("progress page COMPLETED: %w", err)
	}
	windows := inspectWindows(display, electronPID)
	for _, win := range windows {
		if win.Width >= 800 && win.Height >= 500 {
			_ = screenshotWindow(display, win.ID, filepath.Join(layout.EvidenceDir, "progress-page"))
			break
		}
	}
	return nil
}

func writeEvidence(layout Layout, view cleardevsvc.RequirementView, evidence Evidence) (Evidence, error) {
	prdPath := filepath.Join(layout.EvidenceDir, "prd.md")
	prdHash, err := sha256File(prdPath)
	if err != nil {
		return Evidence{}, err
	}
	evidence.InputSHA256 = map[string]string{"prd.md": prdHash}
	evidence.SchemaVersion = EvidenceSchemaV2
	evidence.RequirementID = view.Requirement.ID
	version, err := confirmedDemoVersion(view)
	if err != nil {
		return Evidence{}, err
	}
	if err := writeExactFile(filepath.Join(layout.EvidenceDir, evidenceRequirementFile), version.RequirementText); err != nil {
		return Evidence{}, err
	}
	evidence.RequirementVersionID = version.ID
	evidence.RequirementSHA256 = sha256String(version.RequirementText)
	evidence.TaskSetVersion = version.TaskSetVersion
	if view.ComplexPlanning != nil && len(view.ComplexPlanning.Plans) > 0 {
		plan := view.ComplexPlanning.Plans[len(view.ComplexPlanning.Plans)-1]
		if err := writeExactFile(filepath.Join(layout.EvidenceDir, evidencePlanFile), plan.PlanJSON); err != nil {
			return Evidence{}, err
		}
		evidence.PlanID = plan.ID
		evidence.PlanSHA256 = sha256String(plan.PlanJSON)
	}
	if view.ComplexPlanning != nil && len(view.ComplexPlanning.Reviews) > 0 {
		review := view.ComplexPlanning.Reviews[len(view.ComplexPlanning.Reviews)-1]
		evidence.PlanReviewID = review.ID
		evidence.PlanReviewVerdict = string(review.Verdict)
		if err := writeJSON(filepath.Join(layout.EvidenceDir, evidenceReviewFile), boundReviewIdentifiers{
			ID:         review.ID,
			PlanID:     evidence.PlanID,
			PlanSHA256: evidence.PlanSHA256,
			Verdict:    string(review.Verdict),
		}); err != nil {
			return Evidence{}, err
		}
	}
	if view.ComplexExecution != nil {
		evidence.Mode = string(view.ComplexExecution.Run.Mode)
		evidence.ModeReason = view.ComplexExecution.Run.ModeReason
		evidence.FixedBuilderCount = view.ComplexExecution.Run.FixedBuilderCount
		if view.ComplexExecution.Integration != nil {
			evidence.IntegrationSHA = view.ComplexExecution.Integration.CandidateCommitSHA
		}
		evidence.IntegrationCheckIDs = catalogCheckIDs(view.ComplexExecution, core.CandidateCheckIntegration, "")
		evidence.Tasks = compactTasks(view.ComplexExecution)
		evidence.CheckRuns = compactCheckRuns(view.ComplexExecution)
		evidence.Reviews = compactReviews(view.ComplexExecution)
		evidence.SpecialistChecks = compactSpecialists(view.ComplexExecution)
	}
	evidence.ProgressPhase = string(view.TrustedProgress.Phase)
	evidence.MaxEventSequence = view.TrustedProgress.LatestFactSequence
	if view.TrustedProgress.Explanation != nil {
		evidence.ExplanationSHA256 = view.TrustedProgress.Explanation.FactSummarySHA256
	}
	evidence.RoleSessions = compactSessions(view)
	evidence.Usage = map[string]any{
		"agentSteps": usageStepCount(view),
		"checkRuns":  len(evidence.CheckRuns),
		"reviews":    len(evidence.Reviews),
	}
	if err := writeJSON(filepath.Join(layout.EvidenceDir, "evidence.json"), evidence); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

func compactTasks(exec *core.ComplexExecutionSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(exec.Tasks))
	for _, task := range exec.Tasks {
		out = append(out, map[string]any{
			"id": task.ID, "taskKey": task.TaskKey, "status": task.Status, "round": task.CurrentRound,
			"currentCandidateCommitSha": currentVerifiedCandidateSHA(exec, task.ID),
			"requiredCheckIds":          catalogCheckIDs(exec, core.CandidateCheckRequired, task.ID),
		})
	}
	return out
}

func catalogCheckIDs(exec *core.ComplexExecutionSnapshot, kind core.CandidateCheckKind, taskID string) []string {
	ids := make([]string, 0)
	seen := map[string]bool{}
	for _, spec := range exec.CheckSpecs {
		if spec.Kind != kind {
			continue
		}
		if taskID != "" && spec.ComplexExecutionTaskID != taskID {
			continue
		}
		id := strings.TrimSpace(spec.CheckID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func specCheckID(exec *core.ComplexExecutionSnapshot, specFactID string) string {
	for _, spec := range exec.CheckSpecs {
		if spec.ID == specFactID {
			return spec.CheckID
		}
	}
	return ""
}

func currentVerifiedCandidateSHA(exec *core.ComplexExecutionSnapshot, taskID string) string {
	sha := ""
	round := -1
	for _, verification := range exec.Verifications {
		if verification.ComplexExecutionTaskID != taskID {
			continue
		}
		if verification.Round < round {
			continue
		}
		if err := verifyCommit("verified candidate", verification.CandidateCommitSHA); err != nil {
			continue
		}
		round = verification.Round
		sha = verification.CandidateCommitSHA
	}
	return sha
}

func compactCheckRuns(exec *core.ComplexExecutionSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(exec.CheckRuns))
	for _, run := range exec.CheckRuns {
		out = append(out, map[string]any{
			"id": run.ID, "kind": run.Kind, "status": run.Status, "result": run.Result,
			"checkId": specCheckID(exec, run.CheckSpecFactID), "checkSpecFactId": run.CheckSpecFactID,
			"complexExecutionTaskId": run.ComplexExecutionTaskID,
			"candidateCommitSha":     run.CandidateCommitSHA,
		})
	}
	return out
}

func compactReviews(exec *core.ComplexExecutionSnapshot) []map[string]any {
	out := make([]map[string]any, 0, len(exec.Reviews))
	for _, review := range exec.Reviews {
		out = append(out, map[string]any{
			"id": review.ID, "status": review.Status, "verdict": review.Verdict,
			"complexExecutionTaskId": review.ComplexExecutionTaskID,
			"candidateCommitSha":     review.CandidateCommitSHA,
		})
	}
	return out
}

func compactSpecialists(exec *core.ComplexExecutionSnapshot) []map[string]any {
	if exec.Exception == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(exec.Exception.SpecialistChecks))
	for _, check := range exec.Exception.SpecialistChecks {
		out = append(out, map[string]any{
			"id": check.ID, "checkId": check.CheckID, "result": check.Result, "status": check.Status,
		})
	}
	return out
}

func confirmedDemoVersion(view cleardevsvc.RequirementView) (core.RequirementVersion, error) {
	wantID := ""
	if view.ComplexExecution != nil {
		wantID = view.ComplexExecution.Run.RequirementVersionID
	}
	for _, version := range view.RequirementVersions {
		if version.Status != core.RequirementVersionStatusConfirmed {
			continue
		}
		if wantID == "" || version.ID == wantID {
			return version, nil
		}
	}
	return core.RequirementVersion{}, fmt.Errorf("evidence is missing a confirmed requirement version")
}

func compactSessions(view cleardevsvc.RequirementView) map[string]string {
	sessions := map[string]string{}
	if view.ComplexPlanning != nil {
		for _, binding := range view.ComplexPlanning.RoleBindings {
			if binding.AOSessionID != "" {
				sessions[string(binding.Role)] = binding.AOSessionID
			}
		}
	}
	if view.ComplexExecution != nil {
		for _, binding := range view.ComplexExecution.RoleBindings {
			if binding.AOSessionID != "" {
				key := string(binding.Role)
				if binding.BuilderSlot > 0 {
					key = fmt.Sprintf("%s-%d", binding.Role, binding.BuilderSlot)
				}
				sessions[key] = binding.AOSessionID
			}
		}
	}
	return sessions
}

func usageStepCount(view cleardevsvc.RequirementView) int {
	n := 0
	if view.ComplexPlanning != nil {
		n += len(view.ComplexPlanning.AgentSteps)
	}
	if view.ComplexExecution != nil {
		n += len(view.ComplexExecution.AgentSteps)
		if view.ComplexExecution.Exception != nil {
			n += len(view.ComplexExecution.Exception.OnDemandSteps)
		}
	}
	return n
}

func removeDemoWork(layout Layout) error {
	for _, dir := range []string{layout.HomeDir, layout.DataDir, layout.RepoDir, layout.AppDir} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
