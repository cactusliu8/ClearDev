package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/browser"
)

// Runtime and browser behavior are explicit doubles; Service/SQLite ownership,
// candidate binding and PASS settlement checks are real.
type stageTrialFixtureManager struct {
	resultPreviewDouble
	candidate, contractSHA string
	disabled               bool
	afterStart             func()
}

func (m *stageTrialFixtureManager) StartStageTrial(ctx context.Context, owner domain.SessionID, workspace, candidate string, contract core.ProjectExecutionContract, prepare ports.ClearDevProjectResultPrepare) (previewserver.Status, error) {
	if prepare != nil {
		source, err := prepare(ctx)
		if err != nil {
			return previewserver.Status{}, err
		}
		if source.CandidateSHA != candidate {
			return previewserver.Status{}, errors.New("fixture prepared wrong candidate")
		}
	}
	m.starts++
	m.session, m.workspace, m.candidate = owner, workspace, candidate
	m.contractSHA, _ = core.ProjectExecutionContractDigest(contract)
	m.status = previewserver.Status{SessionID: owner, State: previewserver.StateReady, Configuration: "cleardev-stage-trial", URL: "http://127.0.0.1:4569/"}
	if m.afterStart != nil {
		m.afterStart()
	}
	return m.status, nil
}

func (m *stageTrialFixtureManager) StageTrialStatus(owner domain.SessionID, candidate, contractSHA string) previewserver.Status {
	if owner != m.session || candidate != m.candidate || contractSHA != m.contractSHA {
		return previewserver.Status{State: previewserver.StateStopped}
	}
	return m.status
}

func (h *projectExecutionFlowHarness) simulateStageTrial(ctx context.Context, owner domain.SessionID, prompt string) error {
	parts := strings.SplitN(prompt, "--- REQUIREMENT FINAL REVIEW PACKET ---\n", 2)
	var packet core.RequirementFinalReviewPacket
	if len(parts) != 2 || json.Unmarshal([]byte(parts[1]), &packet) != nil {
		return errors.New("missing fixture final packet")
	}
	if !packet.FunctionalTrial || h.trial.disabled {
		return nil
	}
	if !packet.TrialHostExecution || packet.TrialCLIExecutable == "" {
		return errors.New("new project review omitted frozen host trial access")
	}
	contract, _, err := core.ProjectContractFromRun(packet.Run)
	if err != nil {
		return err
	}
	manager, ok := h.service.resultPreview.(StageReviewTrialManager)
	if !ok {
		return errors.New("fixture is missing controlled stage trial runtime")
	}
	_, err = manager.StartStageTrial(ctx, owner, h.builderWorkspace, packet.CandidateCommitSHA, contract, nil)
	return err
}

func (p *projectResultPreviewDouble) StartStageTrial(ctx context.Context, owner domain.SessionID, workspace, candidate string, contract core.ProjectExecutionContract, prepare ports.ClearDevProjectResultPrepare) (previewserver.Status, error) {
	if p.trial == nil {
		p.trial = &stageTrialFixtureManager{}
	}
	return p.trial.StartStageTrial(ctx, owner, workspace, candidate, contract, prepare)
}

func (p *projectResultPreviewDouble) StageTrialStatus(owner domain.SessionID, candidate, digest string) previewserver.Status {
	if p.trial == nil {
		return previewserver.Status{State: previewserver.StateStopped}
	}
	return p.trial.StageTrialStatus(owner, candidate, digest)
}

func (p *projectResultPreviewDouble) Stop(ctx context.Context, owner domain.SessionID) (previewserver.Status, error) {
	if p.trial != nil && p.trial.session == owner {
		return p.trial.Stop(ctx, owner)
	}
	return p.resultPreviewDouble.Stop(ctx, owner)
}

func (h *projectExecutionFlowHarness) PrepareProjectResult(_ context.Context, _, candidate string, contract core.ProjectExecutionContract) (ports.ClearDevProjectResultSource, error) {
	digest, err := core.ProjectExecutionContractDigest(contract)
	return ports.ClearDevProjectResultSource{WorkspacePath: "/managed/stage-trial-fixture", CandidateSHA: candidate, ContractSHA256: digest,
		Environment: ports.ClearDevCheckEnvironment{CandidateSHA: candidate, ProjectExecutionSHA256: digest, SourceManifestID: strings.Repeat("1", 64)},
		Release:     func(context.Context) error { return nil }}, err
}

func pendingStageTrialFixture(t *testing.T) (*projectPlanningFixture, string, *projectExecutionFlowHarness, core.ComplexExecutionSnapshot, string) {
	t.Helper()
	f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
	h := attachProjectFlow(f, preparer)
	if _, err := f.s.StartProjectExecution(context.Background(), child.Requirement.ID, admission); err != nil {
		t.Fatal(err)
	}
	e := driveProjectFlow(t, f, child.Requirement.ID, true)
	for e.FinalReview.Status != "SENT" {
		if _, stopped, err := f.s.advanceComplexStandardExecution(context.Background(), child.Requirement.ID); err != nil || stopped {
			t.Fatalf("send final review: %v", err)
		}
		e, _, _ = f.store.GetClearDevComplexExecution(context.Background(), child.Requirement.ID)
	}
	token, verifier, err := browser.NewAuthority().Issue(domain.SessionID(e.FinalReview.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := f.store.GetSession(context.Background(), domain.SessionID(e.FinalReview.AOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	record.Metadata.BrowserCapabilityVerifier = verifier
	if err := f.store.UpdateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return f, child.Requirement.ID, h, e, token
}

func TestStageTrialRequiresCurrentReviewerCapabilityAndExactCandidate(t *testing.T) {
	ctx := context.Background()
	f, id, h, e, token := pendingStageTrialFixture(t)
	owner := e.FinalReview.AOSessionID
	// A valid capability for another role still cannot authorize this review.
	builderToken, builderVerifier, err := browser.NewAuthority().Issue(domain.SessionID(e.Run.BuilderAOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	builder, _, err := f.store.GetSession(ctx, domain.SessionID(e.Run.BuilderAOSessionID))
	if err != nil {
		t.Fatal(err)
	}
	builder.Metadata.BrowserCapabilityVerifier = builderVerifier
	if err := f.store.UpdateSession(ctx, builder); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.StageReviewTrial(ctx, id, e.Run.BuilderAOSessionID, builderToken, "start"); err == nil {
		t.Fatal("Builder received Stage Reviewer trial authority")
	}
	for _, input := range []struct{ session, cap, action string }{{owner, "bad", "start"}, {e.Run.BuilderAOSessionID, token, "start"}, {owner, token, "run-arbitrary-command"}} {
		if _, err := f.s.StageReviewTrial(ctx, id, input.session, input.cap, input.action); err == nil {
			t.Fatal("unauthorized stage trial accepted")
		}
	}
	before := h.trial.starts
	if _, err := f.s.StageReviewTrial(ctx, id, owner, token, "start"); err != nil {
		t.Fatal(err)
	}
	if h.trial.starts != before+1 || h.trial.candidate != e.FinalReview.CandidateCommitSHA || h.trial.workspace != e.FinalReview.SourceWorkspacePath {
		t.Fatal("trial was not bound to final source")
	}
	if _, err := f.s.StageReviewTrial(ctx, id, owner, token, "stop"); err != nil {
		t.Fatal(err)
	}
	if f.s.stageReviewTrialReady(context.Background(), *e.FinalReview, e.Run) {
		t.Fatal("stopped trial remained ready")
	}
	if _, err := f.s.StageReviewTrial(ctx, id, owner, token, "start"); err != nil {
		t.Fatal(err)
	}
	done := driveProjectFlow(t, f, id, false)
	if done.Run.CompletedAt == nil || h.trial.stops != 2 {
		t.Fatal("final PASS did not complete and stop trial")
	}
	if _, err := f.s.StageReviewTrial(ctx, id, owner, token, "start"); err == nil {
		t.Fatal("terminal review reopened")
	}
}

func TestStageTrialCandidateDriftDuringPreparationStopsOwnedProcess(t *testing.T) {
	f, id, h, e, token := pendingStageTrialFixture(t)
	h.trial.afterStart = func() { h.currentCandidate = strings.Repeat("e", 40) }
	before := h.trial.stops
	if _, err := f.s.StageReviewTrial(context.Background(), id, e.FinalReview.AOSessionID, token, "start"); err == nil {
		t.Fatal("source drift during start accepted")
	}
	if h.trial.stops != before+1 {
		t.Fatal("changed candidate trial was not stopped")
	}
}

func TestStageReviewRejectsPASSWithoutReadyTrial(t *testing.T) {
	f, id, h, e, _ := pendingStageTrialFixture(t)
	h.trial.disabled = true
	if _, err := h.trial.Stop(context.Background(), domain.SessionID(e.FinalReview.AOSessionID)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		_, _, _ = f.s.advanceComplexStandardExecution(context.Background(), id)
		current, _, _ := f.store.GetClearDevComplexExecution(context.Background(), id)
		if current.Run.CompletedAt != nil || current.FinalReview.Verdict == "PASS" {
			t.Fatal("model PASS bypassed trial")
		}
		if current.FinalReview.Status == "FAILED" {
			return
		}
	}
}
