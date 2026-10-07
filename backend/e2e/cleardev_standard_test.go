//go:build !windows

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

const standardE2ERequirementText = "实现 `normalizeEmail(input)`。输入必须是字符串；先去除首尾空白，再要求恰好一个 `@`，且本地部分和域名都非空，否则抛出 `TypeError(\"invalid email\")`。输出保留本地部分大小写，把域名转为小写。至少测试空白处理、域名大小写、非字符串、缺少或多个 `@`、空本地部分和空域名。不实现登录、网络、数据库、国际化域名或完整 Request for Comments（意见征求稿，RFC）邮箱校验。"

// TestClearDevStandardSingleTaskWithRealCodexChat is the one deliberately
// expensive S02 demonstration. It crosses every real boundary: daemon HTTP,
// persisted confirmation, four native Codex Chat roles, managed worktrees,
// Git-derived scope, restricted Docker checks, and the independently reviewed
// integration candidate. Unit tests cover the same decisions with fakes; this
// test proves that the assembled local product can perform them.
func TestClearDevStandardSingleTaskWithRealCodexChat(t *testing.T) {
	requireE2E(t)
	imageID := requireStandardE2EPrerequisites(t)
	fixturePath, initialSHA := prepareStandardE2EFixture(t)
	dataDir := t.TempDir()

	initialDaemon := startDaemon(t, dataDir)
	projectID := registerStandardE2EProject(t, initialDaemon, fixturePath)
	initialDaemon.stop()

	requirementID, versionID := seedConfirmedStandardE2ERequirement(t, dataDir, projectID)
	d := startDaemon(t, dataDir)

	var started cleardevsvc.RequirementView
	d.mustCall(
		"POST",
		"/cleardev/requirements/"+requirementID+"/standard-runs",
		http.StatusAccepted,
		nil,
		&started,
	)
	if started.Requirement.ID != requirementID {
		t.Fatalf("start response requirement = %q, want %q", started.Requirement.ID, requirementID)
	}

	finalView := awaitStandardE2ETerminal(t, d, requirementID, 25*time.Minute)
	assertStandardE2ECompletionReadModel(t, finalView)
	assertStandardE2EChatTurns(t, d, finalView.StandardFlow)
	result := assertStandardE2EDurableFacts(t, dataDir, fixturePath, initialSHA, imageID, requirementID, versionID)

	t.Logf("S02 STANDARD completed requirement=%s version=%s plan=%s dispatch=%s task=%s",
		requirementID, versionID, result.planID, result.dispatchID, result.taskID)
	t.Logf("roles steward=%s planner=%s builder=%s reviewer=%s",
		result.sessions[core.StandardRoleSteward], result.sessions[core.StandardRoleEngineeringPlanner],
		result.sessions[core.StandardRoleBuilder], result.sessions[core.StandardRoleReviewer])
	t.Logf("candidate base=%s head=%s checks=%d reviewer=PASS overall=COMPLETED",
		result.baseSHA, result.candidateSHA, result.checkCount)
}

func assertStandardE2ECompletionReadModel(t *testing.T, view cleardevsvc.RequirementView) {
	t.Helper()
	if view.OverallProgress.Phase != core.OverallPhaseCompleted || view.OverallProgress.Attention != core.OverallAttentionNone {
		t.Fatalf("completed STANDARD read model = phase:%s attention:%s, want COMPLETED/NONE", view.OverallProgress.Phase, view.OverallProgress.Attention)
	}
	if len(view.DevelopmentTasks) != 1 {
		t.Fatalf("completed STANDARD read model has %d tasks, want 1", len(view.DevelopmentTasks))
	}
	task := view.DevelopmentTasks[0]
	if task.DevelopmentTask.Status != core.DevelopmentTaskStatusDone || len(task.MissingEvidence) != 0 {
		t.Fatalf("completed STANDARD task = status:%s missing:%v, want DONE with no missing evidence", task.DevelopmentTask.Status, task.MissingEvidence)
	}
	if len(task.RequiredChecks) != 1 || task.RequiredChecks[0].Name != "email-unit" {
		t.Fatalf("completed STANDARD task required checks = %#v, want only email-unit", task.RequiredChecks)
	}
}

func requireStandardE2EPrerequisites(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("S02 real demonstration needs git: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatalf("S02 real demonstration needs node for the initial fixture check: %v", err)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("S02 real demonstration needs Docker: %v", err)
	}
	// Do not capture or print auth output. A zero status is the only fact this
	// test needs, and protects credentials/account metadata from test logs.
	if err := exec.Command(codexBinary(), "login", "status").Run(); err != nil { //nolint:gosec // explicit local binary selected by the gated harness.
		t.Fatalf("Codex Chat authentication preflight failed: %v", err)
	}
	imageOutput, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", core.StandardCandidateCheckImage).Output()
	if err != nil {
		t.Fatalf("fixed S02 image %s is unavailable locally: %v", core.StandardCandidateCheckImage, err)
	}
	imageID := strings.TrimSpace(string(imageOutput))
	if !strings.HasPrefix(imageID, "sha256:") || len(imageID) <= len("sha256:") {
		t.Fatalf("Docker returned an invalid image id for %s", core.StandardCandidateCheckImage)
	}
	return imageID
}

func prepareStandardE2EFixture(t *testing.T) (string, string) {
	t.Helper()
	source := standardE2EFixtureSource(t)
	target := filepath.Join(t.TempDir(), fmt.Sprintf("cleardev-s02-%d", uniqueSuffix()))
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
		t.Fatalf("copy fixed S02 fixture: %v", err)
	}

	node := exec.Command("node", "--test")
	node.Dir = target
	if output, err := node.CombinedOutput(); err != nil {
		t.Fatalf("initial fixed fixture node --test failed: %v: %s", err, boundedOutput(output))
	}
	standardE2EGit(t, target, "init", "-b", "main")
	// Persist a fixture-local identity so the real Builder can commit without
	// depending on, or reading, the host user's Git configuration.
	standardE2EGit(t, target, "config", "user.name", "ClearDev E2E")
	standardE2EGit(t, target, "config", "user.email", "cleardev-e2e@example.invalid")
	standardE2EGit(t, target, "add", "-A")
	standardE2EGit(t, target, "commit", "-m", "test: freeze S02 fixture")
	// AO worktrees use the registered remote default as their immutable diff
	// base. Point origin back at this isolated repository so refresh remains
	// entirely local while still exercising the production remote-ref path.
	standardE2EGit(t, target, "remote", "add", "origin", ".")
	standardE2EGit(t, target, "update-ref", "refs/remotes/origin/main", "HEAD")
	standardE2EGit(t, target, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	initialSHA := strings.TrimSpace(standardE2EGit(t, target, "rev-parse", "--verify", "HEAD^{commit}"))
	if !fullLowerGitSHA(initialSHA) {
		t.Fatalf("fixture initial commit is not a full Git SHA")
	}
	return target, initialSHA
}

func standardE2EFixtureSource(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve e2e working directory: %v", err)
	}
	for directory := filepath.Clean(workingDir); ; directory = filepath.Dir(directory) {
		for _, relative := range []string{
			filepath.Join("testdata", "cleardev_s02_fixture"),
			filepath.Join("e2e", "testdata", "cleardev_s02_fixture"),
			filepath.Join("backend", "e2e", "testdata", "cleardev_s02_fixture"),
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
	t.Fatal("could not locate backend/e2e/testdata/cleardev_s02_fixture from the test working directory")
	return ""
}

func registerStandardE2EProject(t *testing.T, d *daemon, path string) string {
	t.Helper()
	var out struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	d.mustCall("POST", "/projects", http.StatusCreated, map[string]any{"path": path}, &out)
	if out.Project.ID == "" {
		t.Fatal("S02 fixture registration returned no AO project id")
	}
	// Pin the fixture's known branch so the demonstration does not depend on the
	// host Git default while its local origin supplies the immutable base ref.
	d.mustCall("PUT", "/projects/"+out.Project.ID+"/config", http.StatusOK, map[string]any{
		"config": map[string]any{"defaultBranch": "main"},
	}, nil)
	return out.Project.ID
}

type allowStandardE2EHuman struct{}

func (allowStandardE2EHuman) Authorize(context.Context, cleardevsvc.HumanDecision) error { return nil }

func seedConfirmedStandardE2ERequirement(t *testing.T, dataDir, projectID string) (string, string) {
	t.Helper()
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("open isolated AO store for S01 preparation: %v", err)
	}
	service := cleardevsvc.New(cleardevsvc.Deps{
		Facts: store,
		AO:    store,
		Human: allowStandardE2EHuman{},
	})
	ctx := context.Background()
	view, err := service.CreateRequirement(ctx, cleardevsvc.CreateRequirementInput{
		AOProjectID:     projectID,
		Name:            "实现邮箱规范化",
		RequirementText: standardE2ERequirementText,
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("create S02 demonstration requirement through S01 service: %v", err)
	}
	if len(view.RequirementVersions) != 1 {
		_ = store.Close()
		t.Fatalf("new S02 demonstration requirement has %d versions, want 1", len(view.RequirementVersions))
	}
	versionID := view.RequirementVersions[0].ID
	if err := service.SubmitRequirementForConfirmation(ctx, versionID); err != nil {
		_ = store.Close()
		t.Fatalf("submit S02 requirement for confirmation: %v", err)
	}
	if err := service.ConfirmRequirementVersion(ctx, versionID); err != nil {
		_ = store.Close()
		t.Fatalf("confirm S02 requirement through trusted test capability: %v", err)
	}
	confirmed, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		_ = store.Close()
		t.Fatalf("read confirmed S02 requirement: %v", err)
	}
	if len(confirmed.RequirementVersions) != 1 || confirmed.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed {
		_ = store.Close()
		t.Fatal("S02 demonstration preparation did not leave one confirmed requirement version")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close isolated AO store after S01 preparation: %v", err)
	}
	return view.Requirement.ID, versionID
}

func awaitStandardE2ETerminal(t *testing.T, d *daemon, requirementID string, timeout time.Duration) cleardevsvc.RequirementView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last cleardevsvc.RequirementView
	for time.Now().Before(deadline) {
		status, err := d.call("GET", "/cleardev/requirements/"+requirementID, nil, &last)
		if err != nil {
			t.Fatalf("query S02 STANDARD flow: %v\n%s", err, d.tailLog())
		}
		if status != http.StatusOK {
			t.Fatalf("query S02 STANDARD flow returned status %d\n%s", status, d.tailLog())
		}
		if last.OverallProgress.Phase == core.OverallPhaseCompleted {
			if step, ok := standardE2EStep(last.StandardFlow, core.AgentStepStatusReport); ok {
				switch step.SendStatus {
				case core.AgentStepSendStatusSettled:
					return last
				case core.AgentStepSendStatusFailed:
					t.Fatalf("task completed but final Steward report failed: reason=%s\n%s", step.ReasonCode, d.tailLog())
				}
			}
		} else if summary, failed := standardE2EStableFailure(last); failed {
			t.Fatalf("S02 STANDARD flow stopped before completion: %s\n%s\n%s", summary, standardE2EDebugJSON(last), d.debugSessions())
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out after %s waiting for S02 STANDARD completion: %s\n%s",
		timeout, standardE2EViewSummary(last), d.tailLog())
	return last
}

func standardE2EDebugJSON(view cleardevsvc.RequirementView) string {
	raw, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return fmt.Sprintf("(could not encode STANDARD view: %v)", err)
	}
	return "--- STANDARD view ---\n" + string(raw)
}

func (d *daemon) debugSessions() string {
	var sessions any
	status, err := d.call("GET", "/sessions", nil, &sessions)
	if err != nil {
		return fmt.Sprintf("(could not read sessions: %v)\n%s", err, d.tailLog())
	}
	raw, marshalErr := json.MarshalIndent(sessions, "", "  ")
	if marshalErr != nil {
		return fmt.Sprintf("(could not encode sessions: %v)\n%s", marshalErr, d.tailLog())
	}
	return fmt.Sprintf("--- sessions (status %d) ---\n%s\n%s", status, raw, d.tailLog())
}

func standardE2EStableFailure(view cleardevsvc.RequirementView) (string, bool) {
	if view.TrustedProgress.Phase == core.TrustedPhaseBlocked ||
		view.TrustedProgress.Attention == core.OverallAttentionBlocked {
		return fmt.Sprintf("trustedPhase=%s attention=%s reason=%s",
			view.TrustedProgress.Phase, view.TrustedProgress.Attention, view.TrustedProgress.ReasonCode), true
	}
	if view.OverallProgress.Attention == core.OverallAttentionNeedsHuman ||
		view.OverallProgress.Attention == core.OverallAttentionBlocked ||
		view.OverallProgress.Attention == core.OverallAttentionIntegrationFailed {
		return fmt.Sprintf("phase=%s attention=%s", view.OverallProgress.Phase, view.OverallProgress.Attention), true
	}
	if view.StandardFlow == nil {
		return "", false
	}
	for _, binding := range view.StandardFlow.RoleBindings {
		// An ended Reviewer binding can be valid history after the one allowed
		// rework. FAILED is terminal; ENDED is not, by itself, a failed flow.
		if binding.Status == core.RoleBindingStatusFailed {
			return fmt.Sprintf("role=%s status=%s reason=%s", binding.Role, binding.Status, binding.ReasonCode), true
		}
	}
	for _, step := range view.StandardFlow.AgentSteps {
		if step.SendStatus == core.AgentStepSendStatusFailed {
			return fmt.Sprintf("step=%s status=%s reason=%s", step.Kind, step.SendStatus, step.ReasonCode), true
		}
	}
	for _, review := range view.StandardFlow.PlanReviews {
		if review.Verdict != core.PlanReviewApproved {
			return fmt.Sprintf("planReview=%s reason=%s", review.Verdict, review.ReasonCode), true
		}
	}
	for _, dispatch := range view.StandardFlow.Dispatches {
		if dispatch.Status == core.DispatchStatusRejected || dispatch.Status == core.DispatchStatusFailed {
			return fmt.Sprintf("dispatch=%s reason=%s", dispatch.Status, dispatch.ReasonCode), true
		}
	}
	for _, review := range view.StandardFlow.LocalReviews {
		if review.Status == core.LocalReviewStatusSettled &&
			(review.Verdict == core.LocalReviewBlocked || review.Verdict == core.LocalReviewNeedsHuman) {
			return fmt.Sprintf("localReview=%s reason=%s", review.Verdict, review.ReasonCode), true
		}
		if review.Status == core.LocalReviewStatusFailed {
			return fmt.Sprintf("localReview=%s reason=%s", review.Status, review.ReasonCode), true
		}
	}
	return "", false
}

func standardE2EViewSummary(view cleardevsvc.RequirementView) string {
	pieces := []string{
		fmt.Sprintf("phase=%s", view.OverallProgress.Phase),
		fmt.Sprintf("attention=%s", view.OverallProgress.Attention),
	}
	if view.StandardFlow != nil {
		pieces = append(pieces,
			fmt.Sprintf("roles=%d", len(view.StandardFlow.RoleBindings)),
			fmt.Sprintf("steps=%d", len(view.StandardFlow.AgentSteps)),
			fmt.Sprintf("plans=%d", len(view.StandardFlow.EngineeringPlans)),
			fmt.Sprintf("dispatches=%d", len(view.StandardFlow.Dispatches)),
			fmt.Sprintf("checks=%d", len(view.StandardFlow.CandidateCheckRuns)),
			fmt.Sprintf("reviews=%d", len(view.StandardFlow.LocalReviews)),
		)
	}
	return strings.Join(pieces, " ")
}

func standardE2EStep(flow *core.StandardFlowSnapshot, kind core.AgentStepKind) (core.AgentStep, bool) {
	if flow == nil {
		return core.AgentStep{}, false
	}
	for index := len(flow.AgentSteps) - 1; index >= 0; index-- {
		if flow.AgentSteps[index].Kind == kind {
			return flow.AgentSteps[index], true
		}
	}
	return core.AgentStep{}, false
}

func assertStandardE2EChatTurns(t *testing.T, d *daemon, flow *core.StandardFlowSnapshot) {
	t.Helper()
	if flow == nil {
		t.Fatal("completed query omitted standardFlow facts")
	}
	bindings := make(map[string]core.RoleSessionBinding, len(flow.RoleBindings))
	for _, binding := range flow.RoleBindings {
		bindings[binding.ID] = binding
	}
	for _, step := range flow.AgentSteps {
		if step.SendStatus != core.AgentStepSendStatusSettled {
			continue
		}
		if step.RequestID == "" || step.ClientMessageID == "" || !fullLowerSHA256(step.PromptSHA256) ||
			!fullLowerSHA256(step.MessageSHA256) {
			t.Fatalf("settled step %s lacks stable request/message identities", step.Kind)
		}
		binding, ok := bindings[step.RoleBindingID]
		if !ok || binding.AOSessionID == "" {
			t.Fatalf("settled step %s has no bound role session", step.Kind)
		}
		snap := d.conversation(binding.AOSessionID)
		matchedTurn, ok := snap.turnByID(step.TurnID)
		if !ok || matchedTurn.State != "completed" || matchedTurn.RolledBack {
			t.Fatalf("step %s does not bind an exact completed, non-rolled-back turn", step.Kind)
		}
		var final message
		var lastAssistantSequence int64 = -1
		for _, candidate := range snap.Messages {
			if candidate.TurnID != step.TurnID || candidate.Role != "assistant" {
				continue
			}
			if candidate.Sequence > lastAssistantSequence {
				lastAssistantSequence = candidate.Sequence
			}
			if candidate.ID == step.FinalMessageID {
				final = candidate
			}
		}
		if final.ID == "" || final.Stream || final.Sequence != lastAssistantSequence {
			t.Fatalf("step %s does not bind the final non-streaming assistant message", step.Kind)
		}
		if sha256Text(final.Text) != step.MessageSHA256 {
			t.Fatalf("step %s final message hash does not match its persisted bytes", step.Kind)
		}
	}
}

type standardE2EResult struct {
	planID       string
	dispatchID   string
	taskID       string
	baseSHA      string
	candidateSHA string
	checkCount   int
	sessions     map[core.StandardRole]string
}

func assertStandardE2EDurableFacts(
	t *testing.T,
	dataDir, fixturePath, initialSHA, expectedImageID, requirementID, versionID string,
) standardE2EResult {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.OpenReadOnly(ctx, dataDir)
	if err != nil {
		t.Fatalf("open completed S02 database read-only: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("close completed S02 database: %v", err)
		}
	}()

	requirement, ok, err := store.GetClearDevRequirement(ctx, requirementID)
	if err != nil || !ok {
		t.Fatalf("read completed requirement facts: found=%t err=%v", ok, err)
	}
	flow, ok, err := store.GetClearDevStandardFlow(ctx, requirementID)
	if err != nil || !ok {
		t.Fatalf("read completed STANDARD facts: found=%t err=%v", ok, err)
	}
	if flow.RequirementVersionID != versionID || len(requirement.RequirementVersions) != 1 ||
		requirement.RequirementVersions[0].Status != core.RequirementVersionStatusConfirmed ||
		requirement.RequirementVersions[0].RequirementText != standardE2ERequirementText {
		t.Fatal("completed flow is not bound to the one frozen confirmed requirement version")
	}

	roleBindings := make(map[core.StandardRole][]core.RoleSessionBinding)
	seenSessions := make(map[string]core.StandardRole)
	for _, binding := range flow.RoleBindings {
		if binding.Status != core.RoleBindingStatusBound && binding.Status != core.RoleBindingStatusEnded {
			t.Fatalf("role %s ended in binding status %s", binding.Role, binding.Status)
		}
		if binding.RequirementVersionID != versionID || binding.AOSessionID == "" || binding.SessionCreationIdempotencyKey == "" {
			t.Fatalf("role %s has an incomplete durable binding", binding.Role)
		}
		if previous, duplicate := seenSessions[binding.AOSessionID]; duplicate {
			t.Fatalf("roles %s and %s reused AO session %s", previous, binding.Role, binding.AOSessionID)
		}
		seenSessions[binding.AOSessionID] = binding.Role
		roleBindings[binding.Role] = append(roleBindings[binding.Role], binding)
	}
	for _, role := range []core.StandardRole{
		core.StandardRoleSteward, core.StandardRoleEngineeringPlanner, core.StandardRoleBuilder, core.StandardRoleReviewer,
	} {
		if len(roleBindings[role]) == 0 {
			t.Fatalf("completed flow has no %s session", role)
		}
	}
	if len(roleBindings[core.StandardRoleSteward]) != 1 || len(roleBindings[core.StandardRoleEngineeringPlanner]) != 1 || len(roleBindings[core.StandardRoleBuilder]) != 1 {
		t.Fatal("Steward, Engineering Planner, and Builder must each have exactly one durable session")
	}

	roleSessions := make(map[core.StandardRole]string)
	for role, bindings := range roleBindings {
		for _, binding := range bindings {
			record, found, readErr := store.GetSession(ctx, domain.SessionID(binding.AOSessionID))
			if readErr != nil || !found {
				t.Fatalf("read %s AO session: found=%t err=%v", role, found, readErr)
			}
			wantKind := domain.KindWorker
			if role == core.StandardRoleSteward {
				wantKind = domain.KindOrchestrator
			}
			if record.Kind != wantKind || record.Harness != domain.HarnessCodex || record.Mode != domain.SessionModeChat ||
				record.PermissionMode != domain.PermissionModeAuto {
				t.Fatalf("role %s session kind/harness/mode/permission = %s/%s/%s/%s", role,
					record.Kind, record.Harness, record.Mode, record.PermissionMode)
			}
			if record.CreationIdempotencyKey != binding.SessionCreationIdempotencyKey {
				t.Fatalf("role %s session does not preserve its spawn idempotency key", role)
			}
			if record.CreationRequestFingerprint == "" || record.Metadata.Prompt != "" {
				t.Fatalf("role %s session lacks a spawn fingerprint or was started with a task prompt", role)
			}
			roleSessions[role] = binding.AOSessionID
		}
	}

	if len(flow.EngineeringPlans) != 1 || len(flow.PlanReviews) != 1 || len(flow.Dispatches) != 1 {
		t.Fatalf("immutable planning facts = plans:%d reviews:%d dispatches:%d, want 1/1/1",
			len(flow.EngineeringPlans), len(flow.PlanReviews), len(flow.Dispatches))
	}
	plan := flow.EngineeringPlans[0]
	if plan.RequirementVersionID != versionID || plan.RequirementSHA256 != requirement.RequirementVersions[0].SHA256 ||
		plan.Version != 1 || sha256Text(plan.PlanJSON) != plan.PlanSHA256 {
		t.Fatal("engineering plan version, requirement binding, or content hash is invalid")
	}
	var parsedPlan core.EngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsedPlan); err != nil {
		t.Fatalf("decode persisted normalized plan: %v", err)
	}
	if parsedPlan.Mode != string(core.WorkModeStandard) || !reflect.DeepEqual(parsedPlan.Task, core.FrozenStandardTaskPlan()) {
		t.Fatal("persisted engineering plan differs from the frozen S02 task")
	}

	planReview := flow.PlanReviews[0]
	if planReview.EngineeringPlanID != plan.ID || planReview.Verdict != core.PlanReviewApproved ||
		planReview.ReasonCode != core.ReasonCode("PLAN_ACCEPTABLE") {
		t.Fatal("exact engineering plan lacks the frozen APPROVED Steward review")
	}
	dispatch := flow.Dispatches[0]
	if dispatch.EngineeringPlanID != plan.ID || dispatch.PlanReviewID != planReview.ID ||
		dispatch.Status != core.DispatchStatusAccepted || dispatch.Mode != core.WorkModeStandard ||
		dispatch.BaseCommitSHA != initialSHA || !fullLowerGitSHA(dispatch.BaseCommitSHA) ||
		sha256Text(dispatch.ExecutionPackageJSON) != dispatch.ExecutionPackageSHA256 {
		t.Fatal("accepted dispatch does not bind the exact approved plan, execution package, and frozen base")
	}

	if len(requirement.DevelopmentTasks) != 1 {
		t.Fatalf("completed single-task flow saved %d development tasks", len(requirement.DevelopmentTasks))
	}
	task := requirement.DevelopmentTasks[0]
	if task.ID != dispatch.PreallocatedDevelopmentTaskID || task.RequirementVersionID != versionID ||
		task.Mode != core.WorkModeStandard || task.Status != core.DevelopmentTaskStatusDone || task.MaxReworkCount != 1 {
		t.Fatalf("completed development task has unexpected binding/state: id=%s status=%s", task.ID, task.Status)
	}
	assertStandardE2EExecutionPackage(t, dispatch, plan, task, requirement.RequirementVersions[0])

	currentCandidate := currentStandardE2ECandidate(t, requirement, task.ID)
	if !fullLowerGitSHA(currentCandidate.CommitSHA) || currentCandidate.AOSessionID != roleBindings[core.StandardRoleBuilder][0].AOSessionID {
		t.Fatal("current candidate is not the full commit observed from the bound Builder")
	}
	builderSession, found, err := store.GetSession(ctx, domain.SessionID(currentCandidate.AOSessionID))
	if err != nil || !found {
		t.Fatalf("read bound Builder workspace: found=%t err=%v", found, err)
	}
	assertStandardE2EBuilderWorkspace(t, builderSession, fixturePath, dispatch.BaseCommitSHA, currentCandidate.CommitSHA)
	if len(requirement.IntegrationCandidates) != 1 {
		t.Fatalf("completed single-task flow saved %d integration candidates", len(requirement.IntegrationCandidates))
	}
	integration := requirement.IntegrationCandidates[0]
	if integration.CommitSHA != currentCandidate.CommitSHA || integration.RequirementVersionID != versionID ||
		integration.TaskSetVersion == nil || *integration.TaskSetVersion != requirement.RequirementVersions[0].TaskSetVersion {
		t.Fatal("integration candidate is not the same current candidate tree and task set")
	}
	assertStandardE2EEvidence(t, requirement, task, currentCandidate, integration)
	assertStandardE2EGitCandidate(t, fixturePath, dispatch.BaseCommitSHA, currentCandidate.CommitSHA)

	currentRuns := make([]core.CandidateCheckRun, 0, 3)
	for _, run := range flow.CandidateCheckRuns {
		if run.CandidateCommitID == currentCandidate.ID {
			currentRuns = append(currentRuns, run)
		}
	}
	assertStandardE2EChecks(t, currentRuns, dispatch, currentCandidate, expectedImageID)
	currentReview := currentStandardE2EReview(t, flow.LocalReviews, currentCandidate.ID)
	if currentReview.Verdict != core.LocalReviewPass || currentReview.Status != core.LocalReviewStatusSettled ||
		sha256Text(currentReview.ReviewPacketJSON) != currentReview.ReviewPacketSHA256 {
		t.Fatal("current candidate lacks an immutable Reviewer PASS packet")
	}
	assertStandardE2EReviewPacket(t, currentReview, plan, dispatch, currentCandidate, currentRuns)
	reviewerBinding := bindingByID(t, flow.RoleBindings, currentReview.ReviewerRoleBindingID)
	if reviewerBinding.Role != core.StandardRoleReviewer || reviewerBinding.CandidateCommitID != currentCandidate.ID ||
		reviewerBinding.AOSessionID == currentCandidate.AOSessionID {
		t.Fatal("local review was not performed by an independent candidate-bound Reviewer")
	}
	reviewerSession, found, err := store.GetSession(ctx, domain.SessionID(reviewerBinding.AOSessionID))
	if err != nil || !found {
		t.Fatalf("read bound Reviewer workspace: found=%t err=%v", found, err)
	}
	if reviewerSession.Metadata.WorkspacePath == "" || reviewerSession.Metadata.WorkspacePath == builderSession.Metadata.WorkspacePath ||
		filepath.Clean(reviewerSession.Metadata.WorkspaceRepoPath) != filepath.Clean(fixturePath) {
		t.Fatal("Reviewer did not receive a distinct managed worktree for the fixture repository")
	}
	if status := standardE2EGit(t, reviewerSession.Metadata.WorkspacePath, "status", "--porcelain=v1", "-z", "--untracked-files=all"); status != "" {
		t.Fatal("Reviewer changed its independent worktree")
	}
	reviewerHead := strings.TrimSpace(standardE2EGit(t, reviewerSession.Metadata.WorkspacePath, "rev-parse", "--verify", "HEAD^{commit}"))
	if reviewerHead != currentCandidate.CommitSHA {
		t.Fatal("Reviewer worktree HEAD is not the exact Builder candidate")
	}
	roleSessions[core.StandardRoleReviewer] = reviewerBinding.AOSessionID
	assertStandardE2ESourceBindings(t, dataDir, requirement, task, currentCandidate, integration, dispatch, currentRuns, currentReview)

	assertStandardE2EStepLinks(t, flow, plan, planReview, dispatch, currentReview)
	return standardE2EResult{
		planID: plan.ID, dispatchID: dispatch.ID, taskID: task.ID,
		baseSHA: dispatch.BaseCommitSHA, candidateSHA: currentCandidate.CommitSHA,
		checkCount: len(currentRuns), sessions: roleSessions,
	}
}

type standardE2EExecutionPackage struct {
	SchemaVersion        int                   `json:"schemaVersion"`
	Mode                 string                `json:"mode"`
	DispatchID           string                `json:"dispatchId"`
	TaskID               string                `json:"taskId"`
	RequirementVersionID string                `json:"requirementVersionId"`
	RequirementSHA256    string                `json:"requirementSha256"`
	RequirementText      string                `json:"requirementText"`
	PlanID               string                `json:"planId"`
	PlanSHA256           string                `json:"planSha256"`
	Task                 core.StandardTaskPlan `json:"task"`
}

func assertStandardE2EExecutionPackage(t *testing.T, dispatch core.Dispatch, plan core.EngineeringPlan, task core.DevelopmentTask, version core.RequirementVersion) {
	t.Helper()
	var packet standardE2EExecutionPackage
	if err := json.Unmarshal([]byte(dispatch.ExecutionPackageJSON), &packet); err != nil {
		t.Fatalf("decode persisted execution package: %v", err)
	}
	if packet.SchemaVersion != 1 || packet.Mode != string(core.WorkModeStandard) || packet.DispatchID != dispatch.ID ||
		packet.TaskID != task.ID || packet.RequirementVersionID != version.ID || packet.RequirementSHA256 != version.SHA256 ||
		packet.RequirementText != standardE2ERequirementText || packet.PlanID != dispatch.EngineeringPlanID ||
		packet.PlanSHA256 != plan.PlanSHA256 ||
		!reflect.DeepEqual(packet.Task, core.FrozenStandardTaskPlan()) {
		t.Fatal("accepted execution package differs from the frozen requirement and task")
	}
}

func currentStandardE2ECandidate(t *testing.T, snapshot core.RequirementSnapshot, taskID string) core.CandidateCommit {
	t.Helper()
	var current core.CandidateCommit
	for _, candidate := range snapshot.Candidates {
		if candidate.DevelopmentTaskID == taskID && candidate.Sequence > current.Sequence {
			current = candidate
		}
	}
	if current.ID == "" {
		t.Fatal("completed task has no candidate commit")
	}
	return current
}

func currentStandardE2EReview(t *testing.T, reviews []core.LocalReview, candidateID string) core.LocalReview {
	t.Helper()
	for _, review := range reviews {
		if review.CandidateCommitID == candidateID {
			return review
		}
	}
	t.Fatal("current candidate has no local review")
	return core.LocalReview{}
}

func assertStandardE2EEvidence(
	t *testing.T,
	snapshot core.RequirementSnapshot,
	task core.DevelopmentTask,
	candidate core.CandidateCommit,
	integration core.IntegrationCandidate,
) {
	t.Helper()
	required := map[string]bool{
		string(core.EvidenceKindScope):                         false,
		string(core.EvidenceKindRequiredCheck) + ":email-unit": false,
		string(core.EvidenceKindIntegration):                   false,
		string(core.EvidenceKindReview):                        false,
	}
	integrationEvidence := false
	for _, evidence := range snapshot.Evidence {
		if evidence.Result != core.EvidenceResultPass || evidence.CommitSHA != candidate.CommitSHA {
			continue
		}
		if evidence.SubjectType == core.SubjectDevelopmentTask && evidence.SubjectID == task.ID &&
			evidence.CandidateCommitID == candidate.ID {
			key := string(evidence.Kind)
			if evidence.Key != "" {
				key += ":" + evidence.Key
			}
			if _, expected := required[key]; expected {
				required[key] = true
			}
		}
		if evidence.SubjectType == core.SubjectDevelopmentRequirement && evidence.SubjectID == snapshot.Requirement.ID &&
			evidence.Kind == core.EvidenceKindIntegration && evidence.IntegrationCandidateID == integration.ID {
			integrationEvidence = true
		}
	}
	for key, present := range required {
		if !present {
			t.Fatalf("current candidate lacks passing %s evidence", key)
		}
	}
	if !integrationEvidence {
		t.Fatal("same-tree integration candidate lacks its passing integration evidence")
	}
}

func assertStandardE2EChecks(
	t *testing.T,
	runs []core.CandidateCheckRun,
	dispatch core.Dispatch,
	candidate core.CandidateCommit,
	expectedImageID string,
) {
	t.Helper()
	if len(runs) != 3 {
		t.Fatalf("current candidate has %d checker runs, want scope, required, and integration", len(runs))
	}
	frozen := core.FrozenStandardTaskPlan()
	scopeSpec, _ := json.Marshal(struct {
		WritePaths                 []string `json:"writePaths"`
		GeneratedPaths             []string `json:"generatedPaths"`
		SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
		ForbiddenPaths             []string `json:"forbiddenPaths"`
	}{frozen.WritePaths, frozen.GeneratedPaths, frozen.SharedPathsRequireApproval, frozen.ForbiddenPaths})
	requiredSpec, _ := json.Marshal(frozen.RequiredChecks[0])
	integrationSpec, _ := json.Marshal(frozen.IntegrationCheck)
	wantNames := map[core.CandidateCheckKind]string{
		core.CandidateCheckScope: "scope", core.CandidateCheckRequired: "email-unit", core.CandidateCheckIntegration: "node-all",
	}
	wantSpecHashes := map[core.CandidateCheckKind]string{
		core.CandidateCheckScope: sha256Text(string(scopeSpec)), core.CandidateCheckRequired: sha256Text(string(requiredSpec)),
		core.CandidateCheckIntegration: sha256Text(string(integrationSpec)),
	}
	wantArgv := map[core.CandidateCheckKind][]string{
		core.CandidateCheckRequired:    {"node", "--test", "test/email.test.js"},
		core.CandidateCheckIntegration: {"node", "--test"},
	}
	seen := make(map[core.CandidateCheckKind]bool, 3)
	for _, run := range runs {
		if seen[run.Kind] {
			t.Fatalf("candidate has duplicate %s checker runs", run.Kind)
		}
		seen[run.Kind] = true
		if run.DispatchID != dispatch.ID || run.BaseCommitSHA != dispatch.BaseCommitSHA ||
			run.CandidateCommitSHA != candidate.CommitSHA || run.Status != core.CandidateCheckRunStatusSettled ||
			run.Result != core.EvidenceResultPass || run.TimedOut || run.Name != wantNames[run.Kind] ||
			run.CheckSpecSHA256 != wantSpecHashes[run.Kind] || !fullLowerSHA256(run.CheckSpecSHA256) ||
			!fullLowerSHA256(run.OutputSHA256) || run.SettledAt == nil {
			t.Fatalf("%s checker run is not a settled PASS for the exact base/candidate", run.Kind)
		}
		if run.Kind == core.CandidateCheckScope {
			assertStandardE2ESavedDiff(t, run.ChangedPathsJSON)
			var argv []string
			if err := json.Unmarshal([]byte(run.ArgvJSON), &argv); err != nil || len(argv) != 0 ||
				run.ContainerImageID != "" || run.ExitCode == nil || *run.ExitCode != 0 {
				t.Fatal("scope checker did not save its exact non-container observation")
			}
		}
		if argv, isContainerCheck := wantArgv[run.Kind]; isContainerCheck {
			var gotArgv []string
			if err := json.Unmarshal([]byte(run.ArgvJSON), &gotArgv); err != nil || !reflect.DeepEqual(gotArgv, argv) {
				t.Fatalf("%s checker argv differs from the approved execution package", run.Kind)
			}
			if run.ContainerImageID != expectedImageID || run.ExitCode == nil || *run.ExitCode != 0 {
				t.Fatalf("%s checker did not pass in the preflighted immutable image", run.Kind)
			}
			if strings.TrimSpace(run.ChangedPathsJSON) != "[]" {
				t.Fatalf("%s checker accepted caller-supplied changed paths", run.Kind)
			}
		}
	}
	for _, kind := range []core.CandidateCheckKind{core.CandidateCheckScope, core.CandidateCheckRequired, core.CandidateCheckIntegration} {
		if !seen[kind] {
			t.Fatalf("current candidate lacks %s checker run", kind)
		}
	}
}

type standardE2EReviewPacket struct {
	SchemaVersion int                     `json:"schemaVersion"`
	AssignmentID  string                  `json:"reviewAssignmentId"`
	Requirement   string                  `json:"requirement"`
	PlanJSON      json.RawMessage         `json:"plan"`
	ExecutionPack json.RawMessage         `json:"executionPackage"`
	BaseSHA       string                  `json:"baseSha"`
	CandidateID   string                  `json:"candidateId"`
	CandidateSHA  string                  `json:"candidateSha"`
	Diff          []standardE2EReviewPath `json:"diff"`
	Checks        []struct {
		Kind         string `json:"kind"`
		Name         string `json:"name"`
		Result       string `json:"result"`
		OutputSHA256 string `json:"outputSha256"`
	} `json:"checks"`
}

type standardE2EReviewPath struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"oldPath"`
}

func assertStandardE2EReviewPacket(
	t *testing.T,
	review core.LocalReview,
	plan core.EngineeringPlan,
	dispatch core.Dispatch,
	candidate core.CandidateCommit,
	runs []core.CandidateCheckRun,
) {
	t.Helper()
	var packet standardE2EReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		t.Fatalf("decode immutable local review packet: %v", err)
	}
	if packet.SchemaVersion != 1 || packet.AssignmentID != review.ID || packet.Requirement != standardE2ERequirementText ||
		packet.BaseSHA != dispatch.BaseCommitSHA || packet.CandidateID != candidate.ID || packet.CandidateSHA != candidate.CommitSHA ||
		string(packet.PlanJSON) != plan.PlanJSON || string(packet.ExecutionPack) != dispatch.ExecutionPackageJSON {
		t.Fatal("local review packet does not bind the exact requirement, plan, dispatch, base, and candidate")
	}
	assertStandardE2EReviewPaths(t, packet.Diff)
	if len(packet.Checks) != len(runs) {
		t.Fatalf("local review packet has %d checks, want %d", len(packet.Checks), len(runs))
	}
	for _, check := range packet.Checks {
		matched := false
		for _, run := range runs {
			if check.Kind == string(run.Kind) && check.Name == run.Name && check.Result == string(core.EvidenceResultPass) &&
				check.OutputSHA256 == run.OutputSHA256 {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("local review packet check %s/%s is not an exact passing checker run", check.Kind, check.Name)
		}
	}
}

func assertStandardE2ESavedDiff(t *testing.T, encoded string) {
	t.Helper()
	var paths []standardE2EReviewPath
	if err := json.Unmarshal([]byte(encoded), &paths); err != nil {
		t.Fatalf("decode persisted Git scope paths: %v", err)
	}
	assertStandardE2EReviewPaths(t, paths)
}

func assertStandardE2EReviewPaths(t *testing.T, paths []standardE2EReviewPath) {
	t.Helper()
	changed := make([]string, 0, len(paths))
	statuses := make(map[string]string, len(paths))
	for _, path := range paths {
		if path.OldPath != "" {
			changed = append(changed, path.OldPath)
			statuses[path.OldPath] = path.Status
		}
		changed = append(changed, path.Path)
		statuses[path.Path] = path.Status
	}
	sort.Strings(changed)
	if !reflect.DeepEqual(changed, []string{"src/email.js", "test/email.test.js"}) ||
		statuses["src/email.js"] != "M" || statuses["test/email.test.js"] != "A" {
		t.Fatalf("persisted candidate diff paths %v, want only the frozen S02 paths", changed)
	}
}

func assertStandardE2ESourceBindings(
	t *testing.T,
	dataDir string,
	snapshot core.RequirementSnapshot,
	task core.DevelopmentTask,
	candidate core.CandidateCommit,
	integration core.IntegrationCandidate,
	dispatch core.Dispatch,
	runs []core.CandidateCheckRun,
	review core.LocalReview,
) {
	t.Helper()
	dsn := "file:" + filepath.Join(dataDir, "ao.db") + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open S02 source bindings read-only: %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Errorf("close S02 source-binding reader: %v", err)
		}
	}()

	var candidateDispatch, candidateBase sql.NullString
	if err := database.QueryRow(
		"SELECT dispatch_id, base_commit_sha FROM cleardev_candidate_commits WHERE id = ?", candidate.ID,
	).Scan(&candidateDispatch, &candidateBase); err != nil {
		t.Fatalf("read candidate dispatch/base binding: %v", err)
	}
	if candidateDispatch.String != dispatch.ID || candidateBase.String != dispatch.BaseCommitSHA {
		t.Fatal("current candidate does not bind the accepted dispatch and frozen base")
	}

	var integrationDispatch, sourceCandidate sql.NullString
	if err := database.QueryRow(
		"SELECT dispatch_id, source_candidate_commit_id FROM cleardev_integration_candidates WHERE id = ?", integration.ID,
	).Scan(&integrationDispatch, &sourceCandidate); err != nil {
		t.Fatalf("read integration candidate source binding: %v", err)
	}
	if integrationDispatch.String != dispatch.ID || sourceCandidate.String != candidate.ID {
		t.Fatal("integration candidate is not linked to the same accepted task candidate")
	}

	runByKind := make(map[core.CandidateCheckKind]core.CandidateCheckRun, len(runs))
	for _, run := range runs {
		runByKind[run.Kind] = run
	}
	integrationEvidenceID := ""
	for _, evidence := range snapshot.Evidence {
		if evidence.SubjectType == core.SubjectDevelopmentRequirement && evidence.SubjectID == snapshot.Requirement.ID &&
			evidence.IntegrationCandidateID == integration.ID && evidence.Kind == core.EvidenceKindIntegration &&
			evidence.Result == core.EvidenceResultPass {
			integrationEvidenceID = evidence.ID
			break
		}
	}
	assertStandardE2EEvidenceSource(t, database, integrationEvidenceID, runByKind[core.CandidateCheckIntegration].ID, "")

	matchedTaskEvidence := 0
	for _, evidence := range snapshot.Evidence {
		if evidence.SubjectType != core.SubjectDevelopmentTask || evidence.SubjectID != task.ID ||
			evidence.CandidateCommitID != candidate.ID || evidence.Result != core.EvidenceResultPass {
			continue
		}
		expectedRun, expectedReview := "", ""
		switch evidence.Kind {
		case core.EvidenceKindScope:
			expectedRun = runByKind[core.CandidateCheckScope].ID
		case core.EvidenceKindRequiredCheck:
			if evidence.Key != "email-unit" {
				continue
			}
			expectedRun = runByKind[core.CandidateCheckRequired].ID
		case core.EvidenceKindIntegration:
			expectedRun = runByKind[core.CandidateCheckIntegration].ID
		case core.EvidenceKindReview:
			expectedReview = review.ID
		default:
			continue
		}
		assertStandardE2EEvidenceSource(t, database, evidence.ID, expectedRun, expectedReview)
		matchedTaskEvidence++
	}
	if matchedTaskEvidence != 4 {
		t.Fatalf("verified %d task evidence source bindings, want scope/required/integration/review", matchedTaskEvidence)
	}
}

func assertStandardE2EEvidenceSource(t *testing.T, database *sql.DB, evidenceID, wantRunID, wantReviewID string) {
	t.Helper()
	if evidenceID == "" {
		t.Fatal("passing evidence has no durable id")
	}
	var runID, reviewID sql.NullString
	if err := database.QueryRow(
		"SELECT candidate_check_run_id, local_review_id FROM cleardev_evidence WHERE id = ?", evidenceID,
	).Scan(&runID, &reviewID); err != nil {
		t.Fatalf("read evidence source binding for %s: %v", evidenceID, err)
	}
	if runID.String != wantRunID || reviewID.String != wantReviewID || (runID.Valid == reviewID.Valid) {
		t.Fatalf("evidence %s does not bind exactly one expected checker/reviewer source", evidenceID)
	}
}

func assertStandardE2EStepLinks(
	t *testing.T,
	flow core.StandardFlowSnapshot,
	plan core.EngineeringPlan,
	review core.PlanReview,
	dispatch core.Dispatch,
	localReview core.LocalReview,
) {
	t.Helper()
	steps := make(map[string]core.AgentStep, len(flow.AgentSteps))
	for _, step := range flow.AgentSteps {
		steps[step.ID] = step
	}
	assertLink := func(name, stepID, bindingID, turnID, messageID string) {
		t.Helper()
		step, ok := steps[stepID]
		if !ok || step.SendStatus != core.AgentStepSendStatusSettled || step.RoleBindingID != bindingID ||
			step.TurnID != turnID || step.FinalMessageID != messageID || step.MessageSHA256 == "" {
			t.Fatalf("%s does not bind its exact settled Agent step", name)
		}
	}
	assertLink("engineering plan", plan.AgentStepID, plan.PlannerRoleBindingID, plan.TurnID, plan.FinalMessageID)
	assertLink("plan review", review.AgentStepID, review.StewardRoleBindingID, review.TurnID, review.FinalMessageID)
	assertLink("dispatch", dispatch.AgentStepID, dispatch.StewardRoleBindingID, steps[dispatch.AgentStepID].TurnID, steps[dispatch.AgentStepID].FinalMessageID)
	assertLink("local review", localReview.AgentStepID, localReview.ReviewerRoleBindingID, localReview.TurnID, localReview.FinalMessageID)
	for _, required := range []core.AgentStepKind{
		core.AgentStepRequestPlanning, core.AgentStepEngineeringPlan, core.AgentStepPlanReview,
		core.AgentStepDispatchRequest, core.AgentStepBuilderResult, core.AgentStepLocalReview, core.AgentStepStatusReport,
	} {
		step, ok := standardE2EStep(&flow, required)
		if !ok || step.SendStatus != core.AgentStepSendStatusSettled {
			t.Fatalf("completed flow lacks settled %s step", required)
		}
	}
}

func bindingByID(t *testing.T, bindings []core.RoleSessionBinding, id string) core.RoleSessionBinding {
	t.Helper()
	for _, binding := range bindings {
		if binding.ID == id {
			return binding
		}
	}
	t.Fatalf("role binding %s was not found", id)
	return core.RoleSessionBinding{}
}

func assertStandardE2EGitCandidate(t *testing.T, repo, baseSHA, candidateSHA string) {
	t.Helper()
	standardE2EGit(t, repo, "cat-file", "-e", candidateSHA+"^{commit}")
	standardE2EGit(t, repo, "merge-base", "--is-ancestor", baseSHA, candidateSHA)
	raw := []byte(standardE2EGit(t, repo, "diff", "--name-status", "-z", "--find-renames", baseSHA, candidateSHA))
	parts := bytes.Split(raw, []byte{0})
	if len(parts) == 0 || len(parts[len(parts)-1]) != 0 {
		t.Fatal("candidate name-status diff is not NUL terminated")
	}
	parts = parts[:len(parts)-1]
	paths := make([]standardE2EReviewPath, 0, 2)
	for index := 0; index < len(parts); {
		status := string(parts[index])
		index++
		if status == "" || index >= len(parts) {
			t.Fatal("candidate name-status diff is incomplete")
		}
		if status[0] == 'R' || status[0] == 'C' {
			if index+1 >= len(parts) {
				t.Fatal("candidate rename diff is incomplete")
			}
			paths = append(paths, standardE2EReviewPath{Status: status, OldPath: string(parts[index]), Path: string(parts[index+1])})
			index += 2
			continue
		}
		paths = append(paths, standardE2EReviewPath{Status: status, Path: string(parts[index])})
		index++
	}
	assertStandardE2EReviewPaths(t, paths)
}

func assertStandardE2EBuilderWorkspace(t *testing.T, session domain.SessionRecord, repo, baseSHA, candidateSHA string) {
	t.Helper()
	if session.Metadata.WorkspacePath == "" || filepath.Clean(session.Metadata.WorkspaceRepoPath) != filepath.Clean(repo) ||
		session.Metadata.DiffBaseSHA != baseSHA {
		t.Fatal("Builder session is not bound to the managed fixture worktree and frozen base")
	}
	status := standardE2EGit(t, session.Metadata.WorkspacePath, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if status != "" {
		t.Fatal("Builder candidate worktree is not clean")
	}
	head := strings.TrimSpace(standardE2EGit(t, session.Metadata.WorkspacePath, "rev-parse", "--verify", "HEAD^{commit}"))
	if head != candidateSHA {
		t.Fatal("Builder managed worktree HEAD differs from the persisted candidate")
	}
}

func standardE2EGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=cleardev-e2e", "GIT_AUTHOR_EMAIL=cleardev-e2e@example.com",
		"GIT_COMMITTER_NAME=cleardev-e2e", "GIT_COMMITTER_EMAIL=cleardev-e2e@example.com",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, boundedOutput(output))
	}
	return string(output)
}

func fullLowerGitSHA(value string) bool {
	return fullLowerHex(value, 40)
}

func fullLowerSHA256(value string) bool {
	return fullLowerHex(value, 64)
}

func fullLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}

func boundedOutput(value []byte) string {
	const limit = 4096
	value = bytes.TrimSpace(value)
	if len(value) > limit {
		value = append(append([]byte(nil), value[:limit]...), []byte("...<truncated>")...)
	}
	return string(value)
}
