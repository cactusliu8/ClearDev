package cleardev_test

import (
	"context"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

type parallelResultPreview struct {
	workspace, project, sha string
	starts, stops           int
	status                  previewserver.Status
	afterStart              func()
}

func (p *parallelResultPreview) StartMail(_ context.Context, session domain.SessionID, workspace, project, sha string) (previewserver.Status, error) {
	p.workspace, p.project, p.sha = workspace, project, sha
	p.starts++
	p.status = previewserver.Status{SessionID: session, State: previewserver.StateReady}
	if p.afterStart != nil {
		p.afterStart()
	}
	return p.status, nil
}

func (p *parallelResultPreview) Stop(_ context.Context, session domain.SessionID) (previewserver.Status, error) {
	p.stops++
	p.status = previewserver.Status{SessionID: session, State: previewserver.StateStopped}
	return p.status, nil
}

func (p *parallelResultPreview) MailStatus(domain.SessionID, string) previewserver.Status {
	return p.status
}

func TestMailV2ResultPreviewTargetsFinalCompositionAndPreservesFacts(t *testing.T) {
	ctx := context.Background()
	for _, overlapping := range []bool{false, true} {
		name := "parallel"
		if overlapping {
			name = "serial-overlap"
		}
		t.Run(name, func(t *testing.T) {
			f := newMailParallelBoundaryFixtureForPaths(t, overlapping)
			execution := f.advance(t, true, nil)
			preview := &parallelResultPreview{}
			service := parallelTestServiceWithOptions(f.store, f.harness, func(deps *cleardevsvc.Deps) { deps.ResultPreview = preview })
			before, err := service.GetRequirement(ctx, f.prep.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.StartResultPreview(ctx, f.prep.RequirementID)
			if err != nil {
				t.Fatal(err)
			}
			if preview.sha != execution.Integration.CandidateCommitSHA || result.CandidateSHA != preview.sha || preview.project != before.Requirement.AOProjectID {
				t.Fatalf("preview lost completed F or project data identity: %+v %+v", result, preview)
			}
			if !overlapping {
				composition, ok := core.ComplexExecutionFinalComposition(execution)
				if !ok || preview.workspace != composition.WorkspacePath || result.SourceBranch != core.ComplexCompositionBranch(composition.RequestID) {
					t.Fatalf("preview opened a partial Builder instead of F: %+v %+v", result, composition)
				}
				for _, binding := range execution.RoleBindings {
					if binding.Role == core.StandardRoleBuilder && preview.workspace == binding.WorkspacePath {
						t.Fatal("composition preview used a Builder-local source")
					}
				}
			}
			if _, err := service.GetResultPreview(ctx, f.prep.RequirementID); err != nil {
				t.Fatal(err)
			}
			if _, err := service.StopResultPreview(ctx, f.prep.RequirementID); err != nil {
				t.Fatal(err)
			}
			after, err := service.GetRequirement(ctx, f.prep.RequirementID)
			if err != nil || !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(before.IntegrationCandidates, after.IntegrationCandidates) || before.TrustedProgress.FactSummarySHA256 != after.TrustedProgress.FactSummarySHA256 {
				t.Fatal("viewing changed completion evidence", err)
			}
		})
	}
}

func TestMailV2ResultPreviewRejectsFinalSourceOrBranchDrift(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"dirty", "sha", "branch", "during-start"} {
		t.Run(mode, func(t *testing.T) {
			f := newMailParallelBoundaryFixture(t)
			execution := f.advance(t, true, nil)
			composition, ok := core.ComplexExecutionFinalComposition(execution)
			if !ok {
				t.Fatal("completed composition missing")
			}
			preview := &parallelResultPreview{}
			service := parallelTestServiceWithOptions(f.store, f.harness, func(deps *cleardevsvc.Deps) { deps.ResultPreview = preview })
			state := f.harness.workspaces[composition.WorkspacePath]
			switch mode {
			case "dirty":
				state.dirty = true
			case "sha":
				state.headSHA = forty("f")
			case "branch":
				state.branch = "main"
			case "during-start":
				preview.afterStart = func() { state.branch = "main" }
			}
			if _, err := service.StartResultPreview(ctx, f.prep.RequirementID); err == nil {
				t.Fatal("stale final source was accepted")
			}
			if mode == "during-start" && (preview.starts != 1 || preview.stops != 1) {
				t.Fatal("concurrent source change did not stop startup")
			}
			if mode != "during-start" && preview.starts != 0 {
				t.Fatal("stale source reached preview launcher")
			}
			if _, err := service.StopResultPreview(ctx, f.prep.RequirementID); err != nil {
				t.Fatal("source drift prevented stopping the existing preview", err)
			}
		})
	}
}
