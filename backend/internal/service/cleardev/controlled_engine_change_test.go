package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestControlledEngineChangeApprovalAlignsProjectConfig(t *testing.T) {
	f, _, before := settledProjectFailure(t, "BLOCKED")
	ctx := context.Background()
	contract, project, err := core.ProjectContractFromRun(before.Run)
	if err != nil || !project {
		t.Fatal(err)
	}
	aoProjectID := contract.Selection.AOProjectID
	requirementID := before.Run.DevelopmentRequirementID
	// A historical request whose binding predates the current schema is
	// inserted FIRST, reproducing the live sequence: it can never be offered
	// or settled and must not absorb the live request.
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	staleBinding := `{"developmentProjectId":"` + requirementID + `","aoProjectId":"` + aoProjectID + `","targetHarness":"opencode","model":"command-goat/deepseek/deepseek-v4.1-flash"}`
	if _, err := raw.Exec(`INSERT INTO cleardev_human_decision_requests (id, development_project_id, decision_kind, binding_schema_version, binding_json, display_json, content_sha256, status, decision, created_at)
		VALUES ('stale-engine-change-request', ?, ?, 1, ?, '{"title":"stale"}', ?, 'PENDING', '', ?)`,
		requirementID, core.HumanDecisionKindControlledEngineChange, staleBinding,
		strings.Repeat("0", 64), f.s.now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The live request is created after the dead row and the other target.
	if _, err := f.s.RequestControlledEngineChange(ctx, requirementID,
		core.ControlledEngineChangeBinding{TargetHarness: string(domain.HarnessOpenCode), Model: "command-goat/deepseek/deepseek-v4.1-flash"}); err != nil {
		t.Fatalf("request rejected: %v", err)
	}
	if authorized, err := f.store.ControlledEngineChangeAuthorized(ctx, aoProjectID, domain.HarnessOpenCode, "command-goat/deepseek/deepseek-v4.1-flash"); err != nil || authorized {
		t.Fatal("unapproved engine change already authorized")
	}
	requests, err := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var request core.HumanDecisionRequest
	for _, r := range requests {
		if r.DecisionKind == core.HumanDecisionKindControlledEngineChange && strings.Contains(r.BindingJSON, "command-goat/deepseek/deepseek-v4.1-flash") {
			request = r
		}
	}
	if request.ID == "" {
		t.Fatal("no live engine change offer was registered")
	}
	if request.ID == "stale-engine-change-request" {
		t.Fatalf("the dead request absorbed the live one: %s", request.ID)
	}
	if !strings.Contains(request.BindingJSON, aoProjectID) || !strings.Contains(request.BindingJSON, string(domain.HarnessOpenCode)) {
		t.Fatalf("engine change offer is not bound to the project and target: %s", request.BindingJSON)
	}
	// The desktop offer loop must actually surface the request, and its issued
	// offer is the one the human answers.
	offer, ok, err := f.s.IssueHumanDecisionOffer(ctx, "engine-change-desktop")
	if err != nil || !ok || offer.RequestID != request.ID {
		t.Fatalf("engine change offer was not issued to the desktop: ok=%v err=%v", ok, err)
	}
	// Same harness, different model: one request per exact target, never
	// absorbed by each other or by the dead row.
	staleReq := core.DevelopmentRequirement{ID: requirementID, AOProjectID: aoProjectID}
	otherModel := core.ControlledEngineChangeBinding{
		DevelopmentRequirementID: requirementID, AOProjectID: aoProjectID,
		TargetHarness: string(domain.HarnessOpenCode), Model: "command-goat/other/model"}
	otherID, err := f.store.CreateControlledEngineChangeRequest(ctx, staleReq, otherModel, f.s.now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	otherAgain, err := f.store.CreateControlledEngineChangeRequest(ctx, staleReq, otherModel, f.s.now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if otherID == "" || otherAgain != otherID {
		t.Fatalf("same exact target must reuse one request: %q vs %q", otherID, otherAgain)
	}
	if otherID == request.ID || otherID == "stale-engine-change-request" {
		t.Fatalf("a dead or live request absorbed the different-model one: %s", otherID)
	}

	now := f.s.now().UTC()
	result := core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding, ContentSHA256: offer.ContentSHA256,
		Nonce: offer.Nonce, Decision: core.HumanDecisionApprove}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	if authorized, err := f.store.ControlledEngineChangeAuthorized(ctx, aoProjectID, domain.HarnessOpenCode, "command-goat/deepseek/deepseek-v4.1-flash"); err != nil || !authorized {
		t.Fatalf("approved engine change not authorized: %v", err)
	}
	after, found, err := f.store.GetProject(ctx, aoProjectID)
	if err != nil || !found {
		t.Fatal(err)
	}
	choice := after.Config.ClearDev
	if choice == nil || choice.Harness != domain.HarnessOpenCode || choice.Model != "command-goat/deepseek/deepseek-v4.1-flash" || choice.Effort != "" {
		t.Fatalf("project config did not follow the authorized engine change: %+v", choice)
	}
	// The superseded legacy row remains immutable history, but cannot keep
	// the completed, approved change on the active decision list.
	pending, err := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherSeen := false
	for _, item := range pending {
		if item.ID == "stale-engine-change-request" {
			t.Fatal("approved replacement still blocked by legacy request")
		}
		otherSeen = otherSeen || item.ID == otherID
	}
	if !otherSeen {
		t.Fatal("different target decision disappeared")
	}
	legacy, found, err := f.store.GetClearDevHumanDecisionRequest(ctx, "stale-engine-change-request")
	if err != nil || !found || legacy.Status != core.HumanDecisionRequestPending || legacy.BindingJSON != staleBinding {
		t.Fatal("legacy history changed", err)
	}

	// Only the recognized legacy spelling of the exact earlier request is
	// excluded. Malformed/foreign/current/newer requests remain unresolved.
	for _, tc := range []struct {
		name, binding string
		at            time.Time
	}{
		{"other-model", strings.Replace(staleBinding, "deepseek-v4.1-flash", "other-model", 1), now.Add(-time.Minute)},
		{"other-requirement", strings.Replace(staleBinding, requirementID, "different-requirement", 1), now.Add(-time.Minute)},
		{"other-project", strings.Replace(staleBinding, aoProjectID, "different-project", 1), now.Add(-time.Minute)},
		{"unknown-field", strings.TrimSuffix(staleBinding, "}") + `,"unknown":true}`, now.Add(-time.Minute)},
		{"duplicate-key", strings.TrimSuffix(staleBinding, "}") + `,"model":"command-goat/deepseek/deepseek-v4.1-flash"}`, now.Add(-time.Minute)},
		{"current-schema", strings.Replace(staleBinding, "developmentProjectId", "developmentRequirementId", 1), now.Add(-time.Minute)},
		{"newer-request", staleBinding, now.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := "legacy-negative-" + tc.name
			if _, err := raw.Exec(`INSERT INTO cleardev_human_decision_requests (id,development_project_id,decision_kind,binding_schema_version,binding_json,display_json,content_sha256,status,decision,created_at) VALUES (?,?,?,1,?,'{"title":"negative"}',?,'PENDING','',?)`, id, requirementID, core.HumanDecisionKindControlledEngineChange, tc.binding, coreDigest([]byte(id)), tc.at); err != nil {
				t.Fatal(err)
			}
			items, err := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.ID == id {
					return
				}
			}
			t.Fatal("unresolved request was hidden")
		})
	}

	// An incomplete imported resolution without the atomic human-decision
	// effect is not evidence, even when the current project target matches.
	missingEffectBinding := strings.Replace(staleBinding, "command-goat/deepseek/deepseek-v4.1-flash", otherModel.Model, 1)
	if _, err := raw.Exec(`INSERT INTO cleardev_human_decision_requests (id,development_project_id,decision_kind,binding_schema_version,binding_json,display_json,content_sha256,status,decision,created_at) VALUES ('legacy-no-effect',?,?,1,?,'{"title":"negative"}',?,'PENDING','',?)`, requirementID, core.HumanDecisionKindControlledEngineChange, missingEffectBinding, coreDigest([]byte("legacy-no-effect")), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE cleardev_human_decision_requests SET status='RESOLVED',decision='APPROVE',resolved_at=? WHERE id=?`, now, otherID); err != nil {
		t.Fatal(err)
	}
	originalChoice := *after.Config.ClearDev
	after.Config.ClearDev.Model = otherModel.Model
	// Simulate inconsistent imported state in this isolated negative fixture;
	// the public API correctly forbids an unapproved frozen-choice change.
	configJSON, err := json.Marshal(after.Config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE projects SET config=? WHERE id=?`, string(configJSON), aoProjectID); err != nil {
		t.Fatal(err)
	}
	items, err := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacyVisible, missingEffectVisible := false, false
	for _, item := range items {
		legacyVisible = legacyVisible || item.ID == "stale-engine-change-request"
		missingEffectVisible = missingEffectVisible || item.ID == "legacy-no-effect"
	}
	if !missingEffectVisible {
		t.Fatal("resolution without effect hid legacy request")
	}
	if !legacyVisible {
		t.Fatal("changed project target hid the old unresolved request")
	}
	after.Config.ClearDev = &originalChoice
	configJSON, err = json.Marshal(after.Config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE projects SET config=? WHERE id=?`, string(configJSON), aoProjectID); err != nil {
		t.Fatal(err)
	}
	// Replay is refused and leaves the aligned config unchanged.
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err == nil {
		t.Fatal("replayed engine change approval accepted")
	}
	_ = now
}

func TestControlledEngineChangeRejectKeepsFrozenChoice(t *testing.T) {
	f, _, before := settledProjectFailure(t, "BLOCKED")
	ctx := context.Background()
	contract, project, err := core.ProjectContractFromRun(before.Run)
	if err != nil || !project {
		t.Fatal(err)
	}
	aoProjectID := contract.Selection.AOProjectID
	if _, err := f.s.RequestControlledEngineChange(ctx, before.Run.DevelopmentRequirementID,
		core.ControlledEngineChangeBinding{TargetHarness: string(domain.HarnessOpenCode), Model: "command-goat/deepseek/deepseek-v4.1-flash"}); err != nil {
		t.Fatal(err)
	}
	requests, err := f.store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var request core.HumanDecisionRequest
	for _, r := range requests {
		if r.DecisionKind == core.HumanDecisionKindControlledEngineChange {
			request = r
		}
	}
	if request.ID == "" {
		t.Fatal("no engine change offer was registered")
	}
	now := f.s.now().UTC()
	offer, err := f.store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{
		RequestID: request.ID, DesktopRunID: "engine-change-reject-test", Nonce: mustComplexNonce(t),
		IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	result := core.HumanDecisionResult{ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: offer.Binding, ContentSHA256: offer.ContentSHA256,
		Nonce: offer.Nonce, Decision: core.HumanDecisionReject}
	if err := f.s.ApplyHumanDecisionResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	after, found, err := f.store.GetProject(ctx, aoProjectID)
	if err != nil || !found {
		t.Fatal(err)
	}
	choice := after.Config.ClearDev
	if choice != nil {
		t.Fatalf("rejected engine change moved the frozen choice: %+v", choice)
	}
	if authorized, err := f.store.ControlledEngineChangeAuthorized(ctx, aoProjectID, domain.HarnessOpenCode, "command-goat/deepseek/deepseek-v4.1-flash"); err != nil || authorized {
		t.Fatal("rejected engine change became authorized")
	}
}
