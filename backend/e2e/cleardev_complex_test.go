//go:build !windows

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// TestClearDevComplexPlanningWithRealCodexChat is the S04 demonstration. It
// uses a real daemon, real Project Steward and Engineering Planner Codex Chat
// sessions, and the production human-authority private channel. The channel is
// driven by the same protocol Electron uses; a windowed Electron binary is a
// separate host-environment check.
func TestClearDevComplexPlanningWithRealCodexChat(t *testing.T) {
	requireE2E(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("S04 real demonstration needs git: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("S04 real demonstration needs node: %v", err)
	}
	if err := exec.Command(codexBinary(), "login", "status").Run(); err != nil { //nolint:gosec // explicit local binary selected by the gated harness.
		t.Fatalf("Codex Chat authentication preflight failed: %v", err)
	}

	fixturePath := prepareComplexE2EFixture(t)
	prdText := readComplexE2EPRD(t, fixturePath)
	dataDir := t.TempDir()

	d := startDaemon(t, dataDir)
	projectID := registerStandardE2EProject(t, d, fixturePath)

	var created cleardevsvc.RequirementView
	d.mustCall("POST", "/cleardev/requirements/complex", http.StatusCreated, map[string]any{
		"aoProjectId": projectID, "name": "复杂邮件名单", "prdText": prdText,
	}, &created)
	if created.Requirement.ID == "" || len(created.RequirementVersions) != 0 || len(created.DevelopmentTasks) != 0 {
		t.Fatalf("complex create left versions or tasks: %#v", created)
	}
	assertComplexStandardGate(t, d, created.Requirement.ID)

	view := awaitComplexE2EPhase(t, d, created.Requirement.ID, core.ComplexPlanningAwaitingClarification, 12*time.Minute)
	questionCount := len(view.ComplexPlanning.Questions)
	if questionCount == 0 {
		t.Fatal("round 0 did not persist blocking questions")
	}

	d.stop()
	d = startDaemon(t, dataDir)
	resumed := mustGetComplexE2E(t, d, created.Requirement.ID)
	if resumed.ComplexPlanning == nil || resumed.ComplexPlanning.Phase != core.ComplexPlanningAwaitingClarification ||
		len(resumed.ComplexPlanning.Questions) != questionCount {
		t.Fatalf("resume at clarification changed questions: before=%d after=%#v", questionCount, resumed.ComplexPlanning)
	}

	answers := complexE2EAnswers(resumed)
	d.mustCall(
		"POST",
		"/cleardev/requirements/"+created.Requirement.ID+"/complex-clarifications",
		http.StatusOK,
		answers,
		&view,
	)
	view = awaitComplexE2EPhase(t, d, created.Requirement.ID, core.ComplexPlanningAwaitingConfirmation, 12*time.Minute)
	if len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusPendingConfirmation {
		t.Fatalf("pending v1 = %#v", view.RequirementVersions)
	}

	d.stop()
	d = startDaemon(t, dataDir)
	pending := mustGetComplexE2E(t, d, created.Requirement.ID)
	if pending.ComplexPlanning == nil || pending.ComplexPlanning.Phase != core.ComplexPlanningAwaitingConfirmation {
		t.Fatalf("resume at pending confirmation = %#v", pending.ComplexPlanning)
	}
	if _, hasPlanner := complexE2ERole(pending, core.StandardRoleEngineeringPlanner); hasPlanner {
		t.Fatal("resume at pending confirmation started the Engineering Planner")
	}

	d.stop()
	d = startDaemonWithHumanAuthority(t, dataDir, core.HumanDecisionApprove)
	view = awaitComplexE2EPhase(t, d, created.Requirement.ID, core.ComplexPlanningApproved, 20*time.Minute)
	assertComplexE2EApprovedFacts(t, dataDir, created.Requirement.ID, prdText, view)
	assertComplexStandardGate(t, d, created.Requirement.ID)

	t.Logf("S04 complex planning completed requirement=%s version=%s plan=%s review=%s steward=%s planner=%s",
		view.Requirement.ID, view.RequirementVersions[0].ID, view.ComplexPlanning.Plans[0].ID,
		view.ComplexPlanning.Reviews[0].ID, complexE2ESession(view, core.StandardRoleSteward),
		complexE2ESession(view, core.StandardRoleEngineeringPlanner))
}

func prepareComplexE2EFixture(t *testing.T) string {
	t.Helper()
	source := complexE2EFixtureSource(t)
	target := filepath.Join(t.TempDir(), fmt.Sprintf("cleardev-s04-%d", uniqueSuffix()))
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fixture contains unsupported symlink %s", relative)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(destination, contents, info.Mode().Perm())
	}); err != nil {
		t.Fatalf("copy fixed S04 fixture: %v", err)
	}
	node := exec.Command("node", "--test")
	node.Dir = target
	if output, err := node.CombinedOutput(); err != nil {
		t.Fatalf("initial fixed fixture node --test failed: %v: %s", err, boundedOutput(output))
	}
	standardE2EGit(t, target, "init", "-b", "main")
	standardE2EGit(t, target, "config", "user.name", "ClearDev E2E")
	standardE2EGit(t, target, "config", "user.email", "cleardev-e2e@example.invalid")
	standardE2EGit(t, target, "add", "-A")
	standardE2EGit(t, target, "commit", "-m", "test: freeze S04 fixture")
	standardE2EGit(t, target, "remote", "add", "origin", ".")
	standardE2EGit(t, target, "update-ref", "refs/remotes/origin/main", "HEAD")
	standardE2EGit(t, target, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return target
}

func complexE2EFixtureSource(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve e2e working directory: %v", err)
	}
	for directory := filepath.Clean(workingDir); ; directory = filepath.Dir(directory) {
		for _, relative := range []string{
			filepath.Join("internal", "cleardev", "testdata", "complex-email-list"),
			filepath.Join("backend", "internal", "cleardev", "testdata", "complex-email-list"),
		} {
			candidate := filepath.Join(directory, relative)
			if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
				return candidate
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	t.Fatal("could not locate backend/internal/cleardev/testdata/complex-email-list")
	return ""
}

func readComplexE2EPRD(t *testing.T, fixturePath string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixturePath, "PRD.md"))
	if err != nil {
		t.Fatalf("read fixture PRD: %v", err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		t.Fatal("fixture PRD is empty")
	}
	return text
}

func startDaemonWithHumanAuthority(t *testing.T, dataDir string, choice core.HumanDecisionChoice) *daemon {
	t.Helper()
	browserToken, err := humanauthority.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	humanToken, err := humanauthority.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	desktopRunID, err := humanauthority.NewDesktopRunID()
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := humanauthority.NewEndpoint(desktopRunID)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := humanauthority.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(endpoint)
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = humanauthority.ServeFakeDesktop(ctx, listener, humanToken, desktopRunID, func(core.HumanDecisionOffer) core.HumanDecisionChoice {
			return choice
		})
		_ = listener.Close()
	}()

	bin := buildDaemon(t)
	port := freePort(t)
	logPath := filepath.Join(t.TempDir(), fmt.Sprintf("daemon-%d.log", port))
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create daemon log: %v", err)
	}
	cmd := exec.Command(bin, "daemon")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(),
		"AO_DATA_DIR="+dataDir,
		"AO_RUN_FILE="+filepath.Join(dataDir, "running.json"),
		"AO_PORT="+fmt.Sprintf("%d", port),
		browserruntime.RuntimeTokenStdinEnv+"=1",
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon with human authority: %v", err)
	}
	payload, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "browserRuntimeToken": browserToken, "humanAuthorityToken": humanToken,
		"humanAuthorityEndpoint": endpoint, "desktopRunId": desktopRunID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(payload, '\n')); err != nil {
		t.Fatalf("write desktop bootstrap: %v", err)
	}
	_ = stdin.Close()

	next := &daemon{
		t: t, dataDir: dataDir, port: port,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d/api/v1", port),
		logPath: logPath, cmd: cmd, pgid: cmd.Process.Pid,
	}
	t.Cleanup(next.kill)
	next.waitReady()
	t.Cleanup(next.killAllSessions)
	return next
}

func awaitComplexE2EPhase(t *testing.T, d *daemon, requirementID string, want core.ComplexPlanningPhase, timeout time.Duration) cleardevsvc.RequirementView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last cleardevsvc.RequirementView
	for time.Now().Before(deadline) {
		last = mustGetComplexE2E(t, d, requirementID)
		if last.ComplexPlanning != nil && last.ComplexPlanning.Phase == want {
			return last
		}
		if summary, failed := complexE2EStableFailure(last); failed {
			t.Fatalf("S04 complex flow stopped before %s: %s\n%s\n%s", want, summary, complexE2EDebugJSON(last), d.debugSessions())
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out after %s waiting for complex phase %s: %s\n%s", timeout, want, complexE2EDebugJSON(last), d.tailLog())
	return last
}

func mustGetComplexE2E(t *testing.T, d *daemon, requirementID string) cleardevsvc.RequirementView {
	t.Helper()
	var view cleardevsvc.RequirementView
	status, err := d.call("GET", "/cleardev/requirements/"+requirementID, nil, &view)
	if err != nil {
		t.Fatalf("query complex requirement: %v\n%s", err, d.tailLog())
	}
	if status != http.StatusOK {
		t.Fatalf("query complex requirement returned status %d\n%s", status, d.tailLog())
	}
	return view
}

func complexE2EAnswers(view cleardevsvc.RequirementView) map[string]any {
	request := view.ComplexPlanning.CompilationRequests[len(view.ComplexPlanning.CompilationRequests)-1]
	answers := make([]map[string]string, 0, len(view.ComplexPlanning.Questions))
	for _, question := range view.ComplexPlanning.Questions {
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

func assertComplexStandardGate(t *testing.T, d *daemon, requirementID string) {
	t.Helper()
	status, apiErr := d.callExpectingError("POST", "/cleardev/requirements/"+requirementID+"/standard-runs", nil)
	if status != http.StatusConflict || apiErr.Code != string(core.ReasonComplexPlanRequired) {
		t.Fatalf("standard-runs gate status=%d code=%s, want 409 COMPLEX_PLAN_REQUIRED", status, apiErr.Code)
	}
}

func assertComplexE2EApprovedFacts(t *testing.T, dataDir, requirementID, prdText string, view cleardevsvc.RequirementView) {
	t.Helper()
	if view.ComplexPlanning == nil || view.ComplexPlanning.Phase != core.ComplexPlanningApproved {
		t.Fatalf("approved read model = %#v", view.ComplexPlanning)
	}
	if len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed {
		t.Fatalf("confirmed v1 = %#v", view.RequirementVersions)
	}
	if view.RequirementVersions[0].RequirementText == prdText {
		t.Fatal("confirmed v1 reused the original PRD text")
	}
	if len(view.DevelopmentTasks) != 0 || len(view.ComplexPlanning.Plans) == 0 || len(view.ComplexPlanning.Reviews) == 0 {
		t.Fatalf("approved facts plans=%d reviews=%d tasks=%d", len(view.ComplexPlanning.Plans), len(view.ComplexPlanning.Reviews), len(view.DevelopmentTasks))
	}
	if view.ComplexPlanning.Reviews[len(view.ComplexPlanning.Reviews)-1].Verdict != core.PlanReviewApproved {
		t.Fatalf("latest review = %#v", view.ComplexPlanning.Reviews)
	}
	steward, hasSteward := complexE2ERole(view, core.StandardRoleSteward)
	planner, hasPlanner := complexE2ERole(view, core.StandardRoleEngineeringPlanner)
	if !hasSteward || !hasPlanner || steward.AOSessionID == "" || planner.AOSessionID == "" || steward.AOSessionID == planner.AOSessionID {
		t.Fatalf("role sessions steward=%#v planner=%#v", steward, planner)
	}

	ctx := context.Background()
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if err != nil {
		t.Fatalf("open completed S04 database read-only: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close completed S04 database: %v", err)
		}
	}()
	snapshot, ok, err := store.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !ok {
		t.Fatalf("read completed requirement facts: found=%t err=%v", ok, err)
	}
	if len(snapshot.DevelopmentTasks) != 0 || len(snapshot.Candidates) != 0 || len(snapshot.IntegrationCandidates) != 0 {
		t.Fatal("S04 left development tasks, candidates, or integration facts")
	}
	flow, exists, err := store.GetClearDevStandardFlow(ctx, requirementID)
	if err != nil {
		t.Fatal(err)
	}
	if exists && (len(flow.Dispatches) != 0 || len(flow.RoleBindings) != 0 || len(flow.LocalReviews) != 0 || len(flow.CandidateCheckRuns) != 0) {
		t.Fatalf("S04 leaked STANDARD work: %#v", flow)
	}
	planning, ok, err := store.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil || !ok {
		t.Fatalf("read completed complex facts: found=%t err=%v", ok, err)
	}
	if planning.Requirement.OriginalPRDText != prdText {
		t.Fatal("original PRD text was rewritten")
	}
	if !core.ComplexPlanIsDispatchable(planning, planning.Plans[len(planning.Plans)-1].ID) {
		t.Fatal("approved plan was not dispatchable")
	}
	for _, binding := range planning.RoleBindings {
		record, found, readErr := store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
		if readErr != nil || !found {
			t.Fatalf("read %s AO session: found=%t err=%v", binding.Role, found, readErr)
		}
		wantKind := domain.KindWorker
		if binding.Role == core.StandardRoleSteward {
			wantKind = domain.KindOrchestrator
		}
		if record.Kind != wantKind || record.Harness != domain.HarnessCodex ||
			domain.NormalizeSessionMode(record.Mode) != domain.SessionModeChat ||
			record.PermissionMode != domain.PermissionModeAuto || record.Metadata.Prompt != "" {
			t.Fatalf("role %s session kind/harness/mode/permission/prompt = %s/%s/%s/%s/%q", binding.Role,
				record.Kind, record.Harness, record.Mode, record.PermissionMode, record.Metadata.Prompt)
		}
	}
}

func complexE2ERole(view cleardevsvc.RequirementView, role core.StandardRole) (core.ComplexRoleBinding, bool) {
	if view.ComplexPlanning == nil {
		return core.ComplexRoleBinding{}, false
	}
	return core.ComplexRoleBindingByRole(core.ComplexPlanningSnapshot{
		RoleBindings: view.ComplexPlanning.RoleBindings,
	}, role)
}

func complexE2ESession(view cleardevsvc.RequirementView, role core.StandardRole) string {
	binding, ok := complexE2ERole(view, role)
	if !ok {
		return ""
	}
	return binding.AOSessionID
}

func complexE2EStableFailure(view cleardevsvc.RequirementView) (string, bool) {
	if view.ComplexPlanning == nil {
		return "", false
	}
	switch view.ComplexPlanning.Phase {
	case core.ComplexPlanningNeedsHuman, core.ComplexPlanningRejected:
		return fmt.Sprintf("phase=%s reason=%s", view.ComplexPlanning.Phase, view.ComplexPlanning.ReasonCode), true
	}
	for _, binding := range view.ComplexPlanning.RoleBindings {
		if binding.Status == core.RoleBindingStatusFailed {
			return fmt.Sprintf("role=%s status=%s reason=%s", binding.Role, binding.Status, binding.ReasonCode), true
		}
	}
	for _, step := range view.ComplexPlanning.AgentSteps {
		if step.SendStatus == core.AgentStepSendStatusFailed {
			return fmt.Sprintf("step=%s status=%s reason=%s", step.Kind, step.SendStatus, step.ReasonCode), true
		}
	}
	return "", false
}

func complexE2EDebugJSON(view cleardevsvc.RequirementView) string {
	raw, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return fmt.Sprintf("(could not encode complex view: %v)", err)
	}
	return "--- complex view ---\n" + string(raw)
}
