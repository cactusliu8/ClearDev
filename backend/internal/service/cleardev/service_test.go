package cleardev_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type workspaceObserver struct {
	mu          sync.Mutex
	observation ports.WorkspaceObservation
	err         error
	last        ports.WorkspaceInfo
}

func (w *workspaceObserver) ObserveWorkspace(_ context.Context, info ports.WorkspaceInfo) (ports.WorkspaceObservation, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.last = info
	return w.observation, w.err
}

func (w *workspaceObserver) set(observation ports.WorkspaceObservation, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.observation, w.err = observation, err
}

type allowHuman struct{}

func (allowHuman) Authorize(context.Context, cleardevsvc.HumanDecision) error { return nil }

func seedServiceProject(t *testing.T, store *sqlite.Store, id string) (domain.SessionRecord, domain.SessionRecord) {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID: id, Path: "/tmp/" + id, Kind: domain.ProjectKindSingleRepo, RegisteredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	newSession := func(path string) domain.SessionRecord {
		now := time.Now().UTC().Truncate(time.Second)
		record, err := store.CreateSession(ctx, domain.SessionRecord{
			ProjectID: domain.ProjectID(id), Kind: domain.KindWorker, Harness: domain.HarnessFake,
			Activity: domain.Activity{State: domain.ActivityActive, LastActivityAt: now},
			Metadata: domain.SessionMetadata{
				Branch: "codex/work", WorkspacePath: path, WorkspaceRepoPath: "/tmp/" + id,
				DiffBaseRef: "refs/heads/main",
			},
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	return newSession("/managed/" + id + "/builder"), newSession("/managed/" + id + "/reviewer")
}

func newService(t *testing.T, store *sqlite.Store, observer *workspaceObserver, human cleardevsvc.HumanDecisionAuthorizer) *cleardevsvc.Service {
	t.Helper()
	var mu sync.Mutex
	nextID, clockTick := 0, 0
	base := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	return cleardevsvc.New(cleardevsvc.Deps{
		Facts: store, AO: store, Workspace: observer, Human: human,
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			nextID++
			return fmt.Sprintf("cleardev-id-%03d", nextID)
		},
		Clock: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			clockTick++
			return base.Add(time.Duration(clockTick) * time.Second)
		},
	})
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	var got *apierr.Error
	if !errors.As(err, &got) {
		t.Fatalf("error %v is not apierr", err)
	}
	return got.Code
}

func requirementInput() cleardevsvc.CreateRequirementInput {
	return cleardevsvc.CreateRequirementInput{
		AOProjectID: "ao-service", Name: "service requirement", RequirementText: "first requirement text",
	}
}

func taskInput(mode core.WorkMode, maxRework int) cleardevsvc.CreateDevelopmentTaskInput {
	return cleardevsvc.CreateDevelopmentTaskInput{
		Title: "implement core path", Mode: mode, MaxReworkCount: maxRework,
		Permissions: cleardevsvc.CreatePathPermissionsInput{
			WritePaths: []string{"backend/**"}, ForbiddenPaths: []string{"backend/secret/**"},
			SharedPathsRequireApproval: []string{"backend/shared/**"}, GeneratedPaths: []string{"backend/gen/**"},
		},
		RequiredChecks: []cleardevsvc.CreateRequiredCheckInput{{Name: "build", Kind: "command"}},
	}
}

func createRequirement(t *testing.T, service *cleardevsvc.Service) cleardevsvc.RequirementView {
	t.Helper()
	view, err := service.CreateRequirement(context.Background(), requirementInput())
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func confirmVersion(t *testing.T, service *cleardevsvc.Service, id string) {
	t.Helper()
	ctx := context.Background()
	if err := service.SubmitRequirementForConfirmation(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmRequirementVersion(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func createConfirmedRequirement(t *testing.T, service *cleardevsvc.Service) cleardevsvc.RequirementView {
	t.Helper()
	view := createRequirement(t, service)
	confirmVersion(t, service, view.RequirementVersions[0].ID)
	confirmed, err := service.GetRequirement(context.Background(), view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	return confirmed
}

func startWithCandidate(t *testing.T, service *cleardevsvc.Service, observer *workspaceObserver, taskID, sessionID, sha string) core.CandidateCommit {
	t.Helper()
	ctx := context.Background()
	if err := service.StartDevelopmentTask(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	observer.set(ports.WorkspaceObservation{Path: "/managed/ao-service/builder", HeadSHA: sha}, nil)
	candidate, err := service.RegisterCandidate(ctx, taskID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func submitPassingQuickEvidence(t *testing.T, service *cleardevsvc.Service, taskID string, candidate core.CandidateCommit) {
	t.Helper()
	ctx := context.Background()
	if err := service.SubmitDevelopmentTaskReview(ctx, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateScope(ctx, taskID, candidate.ID, candidate.CommitSHA, []string{"backend/main.go"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordRequiredCheckResult(ctx, taskID, candidate.ID, candidate.CommitSHA, "build", core.EvidenceResultPass, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordDevelopmentTaskIntegrationResult(ctx, taskID, candidate.ID, candidate.CommitSHA, core.EvidenceResultPass, nil); err != nil {
		t.Fatal(err)
	}
}

func completeQuickTask(t *testing.T, service *cleardevsvc.Service, observer *workspaceObserver, requirementID, sessionID, sha string) core.DevelopmentTask {
	t.Helper()
	task, err := service.CreateDevelopmentTask(context.Background(), requirementID, taskInput(core.WorkModeQuick, 1))
	if err != nil {
		t.Fatal(err)
	}
	candidate := startWithCandidate(t, service, observer, task.ID, sessionID, sha)
	submitPassingQuickEvidence(t, service, task.ID, candidate)
	if err := service.CompleteDevelopmentTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCreateRequirementOnlyCreatesDraftV1WithoutTasks(t *testing.T) {
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	service := newService(t, store, &workspaceObserver{}, allowHuman{})

	view := createRequirement(t, service)
	if view.Requirement.AOProjectID != "ao-service" || view.Requirement.Name != "service requirement" {
		t.Fatalf("requirement = %+v", view.Requirement)
	}
	if len(view.RequirementVersions) != 1 || view.RequirementVersions[0].Version != 1 || view.RequirementVersions[0].Status != core.RequirementVersionStatusDraft {
		t.Fatalf("versions = %+v", view.RequirementVersions)
	}
	if len(view.DevelopmentTasks) != 0 {
		t.Fatalf("public create unexpectedly made development tasks: %+v", view.DevelopmentTasks)
	}
	if view.OverallProgress.Phase != core.OverallPhaseDefiningRequirement || view.OverallProgress.TaskSetVersion != nil {
		t.Fatalf("progress = %+v", view.OverallProgress)
	}
}

func TestCreateRequirementValidatesInputAndRejectsUnsupportedAOProjects(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	for _, project := range []domain.ProjectRecord{
		{ID: "ao-workspace", Path: "/tmp/workspace", Kind: domain.ProjectKindWorkspace, RegisteredAt: time.Now().UTC()},
		{ID: "ao-scratch", Path: "/tmp/scratch", Kind: domain.ProjectKindScratch, RegisteredAt: time.Now().UTC()},
	} {
		if err := store.UpsertProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	service := newService(t, store, &workspaceObserver{}, allowHuman{})
	for _, tc := range []struct {
		name  string
		input cleardevsvc.CreateRequirementInput
		code  string
	}{
		{name: "AO project id required", input: cleardevsvc.CreateRequirementInput{Name: "n", RequirementText: "r"}, code: "AO_PROJECT_ID_REQUIRED"},
		{name: "name required", input: cleardevsvc.CreateRequirementInput{AOProjectID: "ao-workspace", RequirementText: "r"}, code: "NAME_REQUIRED"},
		{name: "requirement text required", input: cleardevsvc.CreateRequirementInput{AOProjectID: "ao-workspace", Name: "n"}, code: "REQUIREMENT_TEXT_REQUIRED"},
		{name: "missing AO project", input: cleardevsvc.CreateRequirementInput{AOProjectID: "missing", Name: "n", RequirementText: "r"}, code: "AO_PROJECT_NOT_FOUND"},
		{name: "workspace unsupported", input: cleardevsvc.CreateRequirementInput{AOProjectID: "ao-workspace", Name: "n", RequirementText: "r"}, code: "AO_PROJECT_KIND_UNSUPPORTED"},
		{name: "scratch unsupported", input: cleardevsvc.CreateRequirementInput{AOProjectID: "ao-scratch", Name: "n", RequirementText: "r"}, code: "AO_PROJECT_KIND_UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := service.CreateRequirement(ctx, tc.input); apiCode(t, err) != tc.code {
				t.Fatalf("CreateRequirement error = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestRequirementVersionRejectThenCreateAndConfirmV2(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	service := newService(t, store, &workspaceObserver{}, allowHuman{})
	created := createRequirement(t, service)
	v1 := created.RequirementVersions[0]
	if err := service.SubmitRequirementForConfirmation(ctx, v1.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.RejectRequirementVersion(ctx, v1.ID, "needs revision"); err != nil {
		t.Fatal(err)
	}
	v2, err := service.CreateRequirementVersion(ctx, created.Requirement.ID, "second requirement text")
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 || v2.Status != core.RequirementVersionStatusDraft || v2.TaskSetVersion != 0 {
		t.Fatalf("v2 = %+v", v2)
	}
	confirmVersion(t, service, v2.ID)
	view, err := service.GetRequirement(ctx, created.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.RequirementVersions[0].Status != core.RequirementVersionStatusRejected || view.RequirementVersions[1].Status != core.RequirementVersionStatusConfirmed {
		t.Fatalf("versions after v2 confirmation = %+v", view.RequirementVersions)
	}
	if view.OverallProgress.CurrentRequirementVersionID != v2.ID || view.OverallProgress.Phase != core.OverallPhasePlanningTasks {
		t.Fatalf("progress = %+v", view.OverallProgress)
	}
}

func TestCreateDevelopmentTaskAtomicallyBindsCurrentVersionAndTaskSet(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seedServiceProject(t, store, "ao-service")
	service := newService(t, store, &workspaceObserver{}, allowHuman{})
	view := createConfirmedRequirement(t, service)
	currentID := view.OverallProgress.CurrentRequirementVersionID
	task, err := service.CreateDevelopmentTask(ctx, view.Requirement.ID, taskInput(core.WorkModeQuick, 1))
	if err != nil {
		t.Fatal(err)
	}
	if task.RequirementVersionID != currentID || task.Status != core.DevelopmentTaskStatusPlanned {
		t.Fatalf("task = %+v", task)
	}
	updated, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.DevelopmentTasks) != 1 || len(updated.DevelopmentTasks[0].PermissionVersions) != 1 || len(updated.DevelopmentTasks[0].RequiredChecks) != 1 {
		t.Fatalf("atomic task facts = %+v", updated.DevelopmentTasks)
	}
	if updated.OverallProgress.TaskSetVersion == nil || *updated.OverallProgress.TaskSetVersion != 1 {
		t.Fatalf("task-set version = %+v", updated.OverallProgress.TaskSetVersion)
	}
}

func TestManagedCandidateEvidenceQuickAndStandardReviewerRules(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	builder, reviewer := seedServiceProject(t, store, "ao-service")
	observer := &workspaceObserver{}
	service := newService(t, store, observer, allowHuman{})
	view := createConfirmedRequirement(t, service)
	quick, err := service.CreateDevelopmentTask(ctx, view.Requirement.ID, taskInput(core.WorkModeQuick, 1))
	if err != nil {
		t.Fatal(err)
	}
	standard, err := service.CreateDevelopmentTask(ctx, view.Requirement.ID, taskInput(core.WorkModeStandard, 1))
	if err != nil {
		t.Fatal(err)
	}
	shaQuick := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	observer.set(ports.WorkspaceObservation{Path: builder.Metadata.WorkspacePath, HeadSHA: shaQuick, Dirty: true}, nil)
	if _, err := service.RegisterCandidate(ctx, quick.ID, string(builder.ID)); apiCode(t, err) != string(core.ReasonCandidateMismatch) {
		t.Fatalf("dirty worktree accepted: %v", err)
	}
	observer.set(ports.WorkspaceObservation{Path: builder.Metadata.WorkspacePath, HeadSHA: "short"}, nil)
	if _, err := service.RegisterCandidate(ctx, quick.ID, string(builder.ID)); apiCode(t, err) != string(core.ReasonCandidateMismatch) {
		t.Fatalf("short SHA accepted: %v", err)
	}
	quickCandidate := startWithCandidate(t, service, observer, quick.ID, string(builder.ID), shaQuick)
	submitPassingQuickEvidence(t, service, quick.ID, quickCandidate)
	if err := service.CompleteDevelopmentTask(ctx, quick.ID); err != nil {
		t.Fatal(err)
	}
	standardCandidate := startWithCandidate(t, service, observer, standard.ID, string(builder.ID), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	submitPassingQuickEvidence(t, service, standard.ID, standardCandidate)
	if _, err := service.RecordReviewResult(ctx, standard.ID, standardCandidate.ID, standardCandidate.CommitSHA, string(builder.ID), core.EvidenceResultPass, nil); apiCode(t, err) != string(core.ReasonCandidateMismatch) {
		t.Fatalf("builder self-review accepted: %v", err)
	}
	if err := service.CompleteDevelopmentTask(ctx, standard.ID); apiCode(t, err) != string(core.ReasonEvidenceIncomplete) {
		t.Fatalf("STANDARD completed without independent reviewer: %v", err)
	}
	if _, err := service.RecordReviewResult(ctx, standard.ID, standardCandidate.ID, standardCandidate.CommitSHA, string(reviewer.ID), core.EvidenceResultPass, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteDevelopmentTask(ctx, standard.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReworkLimitAndRequirementCancellationAreAuditableAndClosed(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	builder, _ := seedServiceProject(t, store, "ao-service")
	observer := &workspaceObserver{}
	service := newService(t, store, observer, allowHuman{})
	view := createConfirmedRequirement(t, service)
	task, err := service.CreateDevelopmentTask(ctx, view.Requirement.ID, taskInput(core.WorkModeQuick, 0))
	if err != nil {
		t.Fatal(err)
	}
	candidate := startWithCandidate(t, service, observer, task.ID, string(builder.ID), "cccccccccccccccccccccccccccccccccccccccc")
	if err := service.SubmitDevelopmentTaskReview(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateScope(ctx, task.ID, candidate.ID, candidate.CommitSHA, []string{"backend/secret/key.go"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.RequestDevelopmentTaskRework(ctx, view.Requirement.ID, task.ID); apiCode(t, err) != string(core.ReasonReworkLimitReached) {
		t.Fatalf("exhausted task entered REWORK: %v", err)
	}
	if err := service.EscalateDevelopmentTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	afterRework, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRework.DevelopmentTasks[0].DevelopmentTask.Status != core.DevelopmentTaskStatusNeedsHuman {
		t.Fatalf("exhausted task = %+v", afterRework.DevelopmentTasks[0].DevelopmentTask)
	}
	if afterRework.OverallProgress.Attention != core.OverallAttentionNeedsHuman {
		t.Fatalf("attention = %+v", afterRework.OverallProgress)
	}
	foundLimitReason := false
	for _, event := range afterRework.Events {
		if event.SubjectID == task.ID && event.Outcome == core.EventAccepted && event.Reason == core.ReasonReworkLimitReached {
			foundLimitReason = true
		}
	}
	if !foundLimitReason {
		t.Fatalf("missing accepted REWORK_LIMIT_REACHED event: %+v", afterRework.Events)
	}
	if err := service.CancelRequirement(ctx, view.Requirement.ID, "human cancellation"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateRequirementVersion(ctx, view.Requirement.ID, "must not be created"); apiCode(t, err) != string(core.ReasonRequirementCancelled) {
		t.Fatalf("cancelled requirement accepted version write: %v", err)
	}
	if _, err := service.CreateDevelopmentTask(ctx, view.Requirement.ID, taskInput(core.WorkModeQuick, 1)); apiCode(t, err) != string(core.ReasonRequirementCancelled) {
		t.Fatalf("cancelled requirement accepted task write: %v", err)
	}
	if _, err := service.RegisterCandidate(ctx, task.ID, string(builder.ID)); apiCode(t, err) != string(core.ReasonRequirementCancelled) {
		t.Fatalf("cancelled requirement accepted candidate write: %v", err)
	}
	final, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.OverallProgress.Phase != core.OverallPhaseCancelled || final.OverallProgress.Attention != core.OverallAttentionNone {
		t.Fatalf("cancelled progress = %+v", final.OverallProgress)
	}
}

func TestPauseDevelopmentTaskForHumanPersistsReworkLimitReason(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	builder, _ := seedServiceProject(t, store, "ao-service")
	observer := &workspaceObserver{}
	service := newService(t, store, observer, allowHuman{})
	view := createConfirmedRequirement(t, service)
	task, err := service.CreateDevelopmentTask(ctx, view.Requirement.ID, taskInput(core.WorkModeQuick, 0))
	if err != nil {
		t.Fatal(err)
	}
	candidate := startWithCandidate(t, service, observer, task.ID, string(builder.ID), "abababababababababababababababababababab")
	if err := service.SubmitDevelopmentTaskReview(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateScope(ctx, task.ID, candidate.ID, candidate.CommitSHA, []string{"backend/secret/key.go"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.PauseDevelopmentTaskForHuman(ctx, task.ID, "operator review"); err != nil {
		t.Fatal(err)
	}
	after, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := after.DevelopmentTasks[0].DevelopmentTask.Status; got != core.DevelopmentTaskStatusNeedsHuman {
		t.Fatalf("paused task status = %s", got)
	}
	last := after.Events[len(after.Events)-1]
	if last.Action != core.ActionPauseDevelopmentTaskNeedsHuman || last.Outcome != core.EventAccepted || last.Reason != core.ReasonReworkLimitReached {
		t.Fatalf("pause event = %+v", last)
	}
}

func TestIntegrationCandidateBindsVersionTaskSetAndHistoricalCancellationDoesNotInvalidateCurrent(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	builder, _ := seedServiceProject(t, store, "ao-service")
	observer := &workspaceObserver{}
	service := newService(t, store, observer, allowHuman{})
	view := createConfirmedRequirement(t, service)
	v1Task := completeQuickTask(t, service, observer, view.Requirement.ID, string(builder.ID), "dddddddddddddddddddddddddddddddddddddddd")
	oldCandidate, err := service.RegisterIntegrationCandidate(ctx, view.Requirement.ID, string(builder.ID))
	if err != nil {
		t.Fatal(err)
	}
	if oldCandidate.RequirementVersionID != view.OverallProgress.CurrentRequirementVersionID || oldCandidate.TaskSetVersion == nil || *oldCandidate.TaskSetVersion != 1 {
		t.Fatalf("old integration binding = %+v", oldCandidate)
	}
	if _, err := service.RecordRequirementIntegrationResult(ctx, view.Requirement.ID, oldCandidate.ID, oldCandidate.CommitSHA, core.EvidenceResultPass, nil); err != nil {
		t.Fatal(err)
	}
	historicalTask, err := service.CreateDevelopmentTask(ctx, view.Requirement.ID, taskInput(core.WorkModeQuick, 1))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := service.CreateRequirementVersion(ctx, view.Requirement.ID, "replacement requirement")
	if err != nil {
		t.Fatal(err)
	}
	confirmVersion(t, service, v2.ID)
	v2Task := completeQuickTask(t, service, observer, view.Requirement.ID, string(builder.ID), "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	currentCandidate, err := service.RegisterIntegrationCandidate(ctx, view.Requirement.ID, string(builder.ID))
	if err != nil {
		t.Fatal(err)
	}
	if currentCandidate.RequirementVersionID != v2.ID || currentCandidate.TaskSetVersion == nil || *currentCandidate.TaskSetVersion != 1 {
		t.Fatalf("current integration binding = %+v", currentCandidate)
	}
	if _, err := service.RecordRequirementIntegrationResult(ctx, view.Requirement.ID, currentCandidate.ID, currentCandidate.CommitSHA, core.EvidenceResultPass, nil); err != nil {
		t.Fatal(err)
	}
	beforeCancel, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeCancel.OverallProgress.Phase != core.OverallPhaseCompleted {
		t.Fatalf("current completed progress = %+v", beforeCancel.OverallProgress)
	}
	if err := service.CancelDevelopmentTask(ctx, v1Task.ID); apiCode(t, err) != string(core.ReasonInvalidTransition) {
		t.Fatalf("historical DONE task was cancelled: %v", err)
	}
	afterRejectedCancel, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := afterRejectedCancel.Events[len(afterRejectedCancel.Events)-1]
	if last.Action != core.ActionCancelDevelopmentTask || last.SubjectID != v1Task.ID || last.Outcome != core.EventRejected || last.Reason != core.ReasonInvalidTransition {
		t.Fatalf("historical DONE cancellation event = %+v", last)
	}
	var rejectedTaskStatus core.DevelopmentTaskStatus
	for _, taskView := range afterRejectedCancel.DevelopmentTasks {
		if taskView.DevelopmentTask.ID == v1Task.ID {
			rejectedTaskStatus = taskView.DevelopmentTask.Status
		}
	}
	var historicalTaskSetAfterRejection int64
	for _, version := range afterRejectedCancel.RequirementVersions {
		if version.ID == view.OverallProgress.CurrentRequirementVersionID {
			historicalTaskSetAfterRejection = version.TaskSetVersion
		}
	}
	if rejectedTaskStatus != core.DevelopmentTaskStatusDone || historicalTaskSetAfterRejection != 2 {
		t.Fatalf("rejected DONE cancellation changed facts: status=%s taskSet=%d", rejectedTaskStatus, historicalTaskSetAfterRejection)
	}
	if err := service.CancelDevelopmentTask(ctx, historicalTask.ID); err != nil {
		t.Fatal(err)
	}
	afterCancel, err := service.GetRequirement(ctx, view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterCancel.OverallProgress.Phase != core.OverallPhaseCompleted || afterCancel.OverallProgress.TaskSetVersion == nil || *afterCancel.OverallProgress.TaskSetVersion != 1 {
		t.Fatalf("historical cancellation invalidated current evidence: %+v", afterCancel.OverallProgress)
	}
	statuses := make(map[string]core.DevelopmentTaskStatus, len(afterCancel.DevelopmentTasks))
	for _, taskView := range afterCancel.DevelopmentTasks {
		statuses[taskView.DevelopmentTask.ID] = taskView.DevelopmentTask.Status
	}
	if len(statuses) != 3 || statuses[v1Task.ID] != core.DevelopmentTaskStatusDone || statuses[historicalTask.ID] != core.DevelopmentTaskStatusCancelled || statuses[v2Task.ID] != core.DevelopmentTaskStatusDone {
		t.Fatalf("task history lost after cancellation: %+v", afterCancel.DevelopmentTasks)
	}
}
