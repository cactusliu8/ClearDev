package cleardev

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSourcePreparationDurableReplayAndRestart(t *testing.T) {
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "DISCOVERED")
	initial := f.create(t)
	f.s.runBackground = func(func()) {}
	input := projectChoice(initial, "discovered")
	view, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input)
	if err != nil || view.Phase != "PREPARING_SOURCE" || view.CanDiscuss || len(f.h.relays) != 1 {
		t.Fatalf("prepare: %+v %v", view, err)
	}
	if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err != nil {
		t.Fatal(err)
	}
	changed := input
	changed.Message = "different"
	_, changedErr := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, changed)
	assertAPICode(t, changedErr, "PRODUCT_REQUEST_CHANGED")
	other := input
	other.RequestID = "another-choice"
	if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, other); err == nil {
		t.Fatal("accepted concurrent preparation")
	}
	// New service instance resumes the saved request without another user click.
	f.h.replies = append(f.h.replies, genericProjectReply("discovered"))
	f.service()
	if err := f.s.ResumeComplexFlows(ctx); err != nil {
		t.Fatal(err)
	}
	view, err = f.s.GetProductGoal(ctx, initial.Goal.ID)
	if err != nil || view.Phase != "READY" || len(view.Discussions) != 2 || view.Selection == nil || view.SourcePreparation != nil || len(f.h.relays) != 2 {
		t.Fatalf("resume: %+v %v", view, err)
	}
	if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err != nil || len(f.h.relays) != 2 {
		t.Fatalf("applied replay: %v", err)
	}
	p, err := f.store.GetClearDevProductSourcePreparation(ctx, input.RequestID)
	if err != nil || p == nil || p.Status != "APPLIED" {
		t.Fatalf("not settled: %+v %v", p, err)
	}
}

func TestSourcePreparationFailureRetainedAndExplicitRetry(t *testing.T) {
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "DISCOVERED")
	initial := f.create(t)
	input := projectChoice(initial, "discovered")
	f.h.prepareErr = errors.New("git fetch failed: connection refused")
	view, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input)
	if err != nil || view.Phase != "SOURCE_PREPARATION_FAILED" || view.SourcePreparation == nil || !strings.Contains(view.SourcePreparation.Failure, "connection refused") || len(f.h.relays) != 1 {
		t.Fatalf("failure: %+v %v", view, err)
	}
	// Boot observes a known failure rather than repeatedly invoking preparation.
	f.service()
	if err := f.s.ResumeComplexFlows(ctx); err != nil {
		t.Fatal(err)
	}
	f.h.prepareErr = nil
	f.h.replies = append(f.h.replies, genericProjectReply("discovered"))
	view, err = f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input)
	if err != nil || view.Phase != "READY" || len(f.h.relays) != 2 {
		t.Fatalf("retry: %+v %v", view, err)
	}
	p, err := f.store.GetClearDevProductSourcePreparation(ctx, input.RequestID)
	if err != nil || !strings.Contains(p.Failure, "connection refused") {
		t.Fatal("lost failed preparation evidence")
	}
}

func TestSourcePreparationCancellationPreventsResume(t *testing.T) {
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "DISCOVERED")
	initial := f.create(t)
	f.s.runBackground = func(func()) {}
	input := projectChoice(initial, "discovered")
	if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CancelRequirement(ctx, initial.Goal.ID, "test cancellation"); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CheckClearDevProductSourcePreparation(ctx, input.RequestID); err == nil {
		t.Fatal("cancelled product still current")
	}
	f.service()
	if err := f.s.ResumeComplexFlows(ctx); err != nil {
		t.Fatal(err)
	}
	view, err := f.s.GetProductGoal(ctx, initial.Goal.ID)
	if err != nil || view.Phase != "CANCELLED" || len(f.h.relays) != 1 {
		t.Fatalf("cancel: %+v %v", view, err)
	}
}

func TestSourcePreparationMalformedInputIsNotDurable(t *testing.T) {
	f := newProjectPlanningFixture(t, "DISCOVERED")
	initial := f.create(t)
	input := projectChoice(initial, "discovered")
	input.Message = " "
	if _, err := f.s.SubmitProductDiscussion(context.Background(), initial.Goal.ID, input); err == nil {
		t.Fatal("accepted empty message")
	}
	p, err := f.store.GetClearDevProductSourcePreparation(context.Background(), input.RequestID)
	if err != nil || p != nil {
		t.Fatal("malformed request was saved")
	}
}

func TestSourcePreparationCannotInventReadySelection(t *testing.T) {
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "DISCOVERED")
	initial := f.create(t)
	f.s.runBackground = func(func()) {}
	input := projectChoice(initial, "discovered")
	if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err != nil {
		t.Fatal(err)
	}
	p, err := f.store.GetClearDevProductSourcePreparation(ctx, input.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	forged := p.Selection
	forged.RepositoryPath = "/another/project"
	if err := f.store.SetClearDevProductSourcePreparation(ctx, p.ID, "PENDING", "READY", "", &forged, time.Now()); err == nil {
		t.Fatal("accepted substituted prepared source")
	}
}

func TestProjectDirectoryPromptInspectsRegisteredFolder(t *testing.T) {
	prompt := projectDirectoryDiscoveryPrompt("本地文件读取和搜索只允许位于上述调查工作区，以及 selection 非 null 时的 selection.repositoryPath", "/tmp/actual-project")
	for _, fragment := range []string{"/tmp/actual-project", "第一步先只读检查", "不能代替登记目录的现状", "不能当成空目录"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("missing directory rule %q", fragment)
		}
	}
}
