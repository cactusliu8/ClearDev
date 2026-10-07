package cleardev

import (
	"context"
	"errors"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
)

type projectPreviewServiceHarness struct {
	*projectExecutionFlowHarness
	branchErr          error
	branchWorkspace    string
	branch             string
	branchCandidate    string
	preparedWorkspace  string
	preparedCandidate  string
	preparedContract   core.ProjectExecutionContract
	preparationRelease int
}

func (h *projectPreviewServiceHarness) InspectDeliveryBranch(_ context.Context, workspace, branch, candidate string) error {
	h.branchWorkspace, h.branch, h.branchCandidate = workspace, branch, candidate
	return h.branchErr
}

func (h *projectPreviewServiceHarness) PrepareProjectResult(_ context.Context, workspace, candidate string, contract core.ProjectExecutionContract) (ports.ClearDevProjectResultSource, error) {
	h.preparedWorkspace, h.preparedCandidate, h.preparedContract = workspace, candidate, contract
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		return ports.ClearDevProjectResultSource{}, err
	}
	return ports.ClearDevProjectResultSource{
		WorkspacePath:  "/managed/project-result-runtime",
		CandidateSHA:   candidate,
		ContractSHA256: digest,
		Environment: ports.ClearDevCheckEnvironment{
			CandidateSHA:           candidate,
			SourceManifestID:       strings64("1"),
			ProjectExecutionSHA256: digest,
		},
		Release: func(context.Context) error {
			h.preparationRelease++
			return nil
		},
	}, nil
}

type projectResultPreviewDouble struct {
	resultPreviewDouble
	projectStarts int
	dataReady     bool
	source        ports.ClearDevProjectResultSource
	sourceErr     error
	workspace     string
	candidate     string
	contract      core.ProjectExecutionContract
	contractSHA   string
	trial         *stageTrialFixtureManager
}

func (p *projectResultPreviewDouble) StartProject(
	ctx context.Context,
	session domain.SessionID,
	workspace, candidate string,
	contract core.ProjectExecutionContract,
	prepare ports.ClearDevProjectResultPrepare,
) (previewserver.Status, error) {
	p.projectStarts++
	p.session, p.workspace, p.candidate, p.contract = session, workspace, candidate, contract
	digest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		return previewserver.Status{}, err
	}
	p.contractSHA = digest
	p.source, p.sourceErr = prepare(ctx)
	if p.sourceErr != nil {
		return previewserver.Status{}, p.sourceErr
	}
	p.status = previewserver.Status{SessionID: session, State: previewserver.StateReady, URL: "http://127.0.0.1:4568/"}
	p.dataReady = true
	return p.status, nil
}

func (p *projectResultPreviewDouble) ProjectDataReady(_ context.Context, _ core.ProjectExecutionContract, candidate string) (bool, error) {
	return p.dataReady && candidate == p.candidate, nil
}

func (p *projectResultPreviewDouble) ProjectDataBaselineCurrent(_ context.Context, _ core.ProjectExecutionContract, candidate string) (bool, error) {
	return p.candidate == "" || p.candidate == candidate, nil
}

func (p *projectResultPreviewDouble) ProjectStatus(session domain.SessionID, candidate, contractSHA string) previewserver.Status {
	if session != p.session || candidate != p.candidate || contractSHA != p.contractSHA {
		return previewserver.Status{SessionID: session, State: previewserver.StateStopped}
	}
	return p.status
}

func strings64(value string) string {
	out := ""
	for len(out) < 64 {
		out += value
	}
	return out[:64]
}

// The execution model/AO/Git/checker remain explicit test doubles. Service,
// SQLite completion facts, exact final-review binding and preview routing are real.
func TestProjectResultPreviewUsesExactFinalDeliveryWithoutChangingCompletion(t *testing.T) {
	ctx := context.Background()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	base := attachProjectFlow(f, preparer)
	h := &projectPreviewServiceHarness{projectExecutionFlowHarness: base}
	f.s.sessions, f.s.chat, f.s.inspector, f.s.checks = h, h, h, h

	if _, err := f.s.StartProjectExecution(ctx, child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	completed := driveProjectFlow(t, f, child.Requirement.ID, false)
	contract, project, err := core.ProjectContractFromRun(completed.Run)
	if err != nil || !project {
		t.Fatalf("completed run has no project contract: %v", err)
	}
	builder, found := complexExecutionBindingByID(completed, completed.Run.BuilderRoleBindingID)
	if !found {
		t.Fatal("completed project lost its Builder binding")
	}
	record, found, err := f.store.GetSession(ctx, domain.SessionID(builder.AOSessionID))
	if err != nil || !found {
		t.Fatalf("read completed Builder: %v", err)
	}
	record.Activity.State = domain.ActivityIdle
	if err := f.store.UpdateSession(ctx, record); err != nil {
		t.Fatal(err)
	}

	manager := &projectResultPreviewDouble{}
	f.s.resultPreview = manager
	before := mustGetComplex(t, f.s, child.Requirement.ID)

	started, err := f.s.StartResultPreview(ctx, child.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if manager.starts != 0 || manager.projectStarts != 1 {
		t.Fatalf("project result used the wrong preview lane: mail=%d project=%d", manager.starts, manager.projectStarts)
	}
	if started.CandidateSHA != completed.Integration.CandidateCommitSHA || manager.candidate != completed.Integration.CandidateCommitSHA ||
		manager.workspace != record.Metadata.WorkspacePath || h.preparedWorkspace != record.Metadata.WorkspacePath ||
		h.preparedCandidate != completed.Integration.CandidateCommitSHA {
		t.Fatalf("project preview lost the final source binding: started=%+v manager=%+v prepared=%s/%s", started, manager.status, h.preparedWorkspace, h.preparedCandidate)
	}
	expectedDigest, err := core.ProjectExecutionContractDigest(contract)
	if err != nil {
		t.Fatal(err)
	}
	actualDigest, err := core.ProjectExecutionContractDigest(h.preparedContract)
	if err != nil {
		t.Fatal(err)
	}
	if manager.contractSHA != expectedDigest || actualDigest != expectedDigest || manager.source.ContractSHA256 != expectedDigest ||
		manager.source.CandidateSHA != completed.Integration.CandidateCommitSHA {
		t.Fatal("project preview did not carry the exact admitted execution contract")
	}
	if h.branchCandidate != completed.Integration.CandidateCommitSHA || h.branchWorkspace != record.Metadata.WorkspacePath || h.branch != record.Metadata.Branch {
		t.Fatalf("delivery branch inspection did not use the completed Builder source: %s %s %s", h.branchWorkspace, h.branch, h.branchCandidate)
	}

	current, err := f.s.GetResultPreview(ctx, child.Requirement.ID)
	if err != nil || current.Preview.State != previewserver.StateReady || current.CandidateSHA != completed.Integration.CandidateCommitSHA {
		t.Fatalf("project preview status: %+v %v", current, err)
	}
	stopped, err := f.s.StopResultPreview(ctx, child.Requirement.ID)
	if err != nil || stopped.Preview.State != previewserver.StateStopped {
		t.Fatalf("stop project preview: %+v %v", stopped, err)
	}
	after := mustGetComplex(t, f.s, child.Requirement.ID)
	if !reflect.DeepEqual(before.Events, after.Events) ||
		!reflect.DeepEqual(before.IntegrationCandidates, after.IntegrationCandidates) ||
		before.TrustedProgress.FactSummarySHA256 != after.TrustedProgress.FactSummarySHA256 ||
		after.ComplexExecution == nil || after.ComplexExecution.Run.CompletedAt == nil {
		t.Fatal("opening or stopping a project result changed completion facts")
	}

	starts := manager.projectStarts
	h.branchErr = errors.New("explicit delivered branch drift")
	if _, err := f.s.StartResultPreview(ctx, child.Requirement.ID); err == nil {
		t.Fatal("project preview opened after the delivered source branch drifted")
	}
	if manager.projectStarts != starts {
		t.Fatal("branch drift reached the project launcher")
	}
}
