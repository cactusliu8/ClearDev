package cleardev

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
)

type resultPreviewDouble struct {
	starts, stops      int
	session            domain.SessionID
	project, workspace string
	status             previewserver.Status
	err                error
	afterStart         func()
}

func (p *resultPreviewDouble) StartMail(_ context.Context, session domain.SessionID, workspace, project, _ string) (previewserver.Status, error) {
	p.starts++
	p.session, p.workspace, p.project = session, workspace, project
	p.status = previewserver.Status{SessionID: session, State: previewserver.StateReady, URL: "http://127.0.0.1:4567/"}
	if p.afterStart != nil {
		p.afterStart()
	}
	return p.status, p.err
}
func (p *resultPreviewDouble) Stop(_ context.Context, session domain.SessionID) (previewserver.Status, error) {
	p.stops++
	p.status = previewserver.Status{SessionID: session, State: previewserver.StateStopped}
	return p.status, nil
}
func (p *resultPreviewDouble) MailStatus(domain.SessionID, string) previewserver.Status {
	return p.status
}

type resultSourceDouble struct {
	ports.ClearDevCandidateInspector
	mode string
}

func (p *resultSourceDouble) InspectCandidate(_ context.Context, _ string, sha string) (ports.ClearDevCandidateInspection, error) {
	result := ports.ClearDevCandidateInspection{BaseSHA: sha, CandidateSHA: sha}
	switch p.mode {
	case "dirty":
		return result, ports.ErrClearDevWorkspaceDirty
	case "sha":
		result.CandidateSHA = forty("f")
	}
	return result, nil
}

func (p *resultSourceDouble) InspectDeliveryBranch(_ context.Context, _, _, _ string) error {
	if p.mode == "branch" {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

// Completion facts, review binding and all reads use real SQLite. Only the
// already-tested preview process and post-completion source are explicit doubles.
func TestResultPreviewUsesCompletedBindingWithoutChangingFacts(t *testing.T) {
	f, h := newMailFlowFixture(t)
	f.confirm(t)
	execution := f.completed(t)
	record, found, err := f.store.GetSession(context.Background(), domain.SessionID(execution.Run.BuilderAOSessionID))
	if err != nil || !found {
		t.Fatal(err)
	}
	record.Activity.State = domain.ActivityIdle
	if err := f.store.UpdateSession(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	preview := &resultPreviewDouble{}
	f.service.resultPreview = preview
	f.service.inspector = &resultSourceDouble{ClearDevCandidateInspector: h}
	before := mustGetComplex(t, f.service, f.view.Requirement.ID)
	result, err := f.service.StartResultPreview(context.Background(), f.view.Requirement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateSHA != execution.Integration.CandidateCommitSHA || preview.session != record.ID || preview.project != string(record.ProjectID) || preview.workspace != record.Metadata.WorkspacePath {
		t.Fatalf("incorrect completed source: %+v %+v", result, preview)
	}
	if _, err := f.service.GetResultPreview(context.Background(), f.view.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.StopResultPreview(context.Background(), f.view.Requirement.ID); err != nil {
		t.Fatal(err)
	}
	after := mustGetComplex(t, f.service, f.view.Requirement.ID)
	if !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(before.IntegrationCandidates, after.IntegrationCandidates) || before.TrustedProgress.FactSummarySHA256 != after.TrustedProgress.FactSummarySHA256 {
		t.Fatal("viewing changed workflow completion facts")
	}
	if preview.starts != 1 || preview.stops != 1 {
		t.Fatal("unexpected lifecycle calls")
	}
}

func TestResultPreviewRejectsUnfinishedDirtyStaleOrBusySource(t *testing.T) {
	for _, mode := range []string{"unfinished", "dirty", "sha", "branch", "busy", "terminated", "startup", "changed-during-start"} {
		t.Run(mode, func(t *testing.T) {
			f, h := newMailFlowFixture(t)
			preview := &resultPreviewDouble{}
			f.service.resultPreview = preview
			if mode != "unfinished" {
				f.confirm(t)
				execution := f.completed(t)
				record, _, err := f.store.GetSession(context.Background(), domain.SessionID(execution.Run.BuilderAOSessionID))
				if err != nil {
					t.Fatal(err)
				}
				record.Activity.State = domain.ActivityIdle
				if mode == "busy" {
					record.Activity.State = domain.ActivityActive
				}
				if mode == "terminated" {
					record.IsTerminated = true
				}
				if err := f.store.UpdateSession(context.Background(), record); err != nil {
					t.Fatal(err)
				}
			}
			source := &resultSourceDouble{ClearDevCandidateInspector: h, mode: mode}
			f.service.inspector = source
			if mode == "startup" {
				preview.err = errors.New("explicit startup failure")
			}
			if mode == "changed-during-start" {
				preview.afterStart = func() { source.mode = "dirty" }
			}
			if _, err := f.service.StartResultPreview(context.Background(), f.view.Requirement.ID); err == nil {
				t.Fatal("unsafe source was opened")
			}
			if mode == "changed-during-start" && preview.stops != 1 {
				t.Fatal("changed source not stopped")
			}
			if mode != "startup" && mode != "changed-during-start" && preview.starts != 0 {
				t.Fatal("rejected source reached launcher")
			}
		})
	}
}
