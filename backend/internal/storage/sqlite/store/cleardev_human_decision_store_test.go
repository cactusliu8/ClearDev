package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestClearDevSubmitCreatesHumanDecisionRequestAtomically(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	seeded := cleardevtest.SeedTwoPendingConfirmations(t, store, "ao-human-submit")
	now := time.Now().UTC().Truncate(time.Second)
	if err := applyClearDev(t, store, core.ActionConfirmRequirementVersion, seeded[0].VersionID, now, false, ""); err == nil {
		t.Fatal("confirm without a trusted human decision was accepted")
	}
	if cleardevtest.RequirementStatus(t, store, seeded[0].RequirementID, seeded[0].VersionID) != core.RequirementVersionStatusPendingConfirmation {
		t.Fatal("untrusted confirm changed the version")
	}
	request, ok, err := store.GetClearDevHumanDecisionRequest(ctx, seeded[0].RequestID)
	if err != nil || !ok || request.Status != core.HumanDecisionRequestPending {
		t.Fatalf("request = %+v ok=%v err=%v", request, ok, err)
	}
	if err := store.BackfillClearDevHumanDecisionRequests(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := store.ListPendingClearDevHumanDecisionRequests(ctx)
	if err != nil || len(again) != 2 {
		t.Fatalf("backfill duplicated requests: %d err=%v", len(again), err)
	}
	db := openClearDevRawDB(t, dataDir)
	t.Cleanup(func() { _ = db.Close() })
	var cdc int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM change_log WHERE event_type = 'cleardev_project_updated'`).Scan(&cdc); err != nil {
		t.Fatal(err)
	}
	if cdc < 2 {
		t.Fatalf("human decision submit CDC count = %d, want at least 2", cdc)
	}
}

func TestClearDevHumanDecisionApproveRejectLaterAndReplay(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seeded := cleardevtest.SeedTwoPendingConfirmations(t, store, "ao-human-settle")
	now := time.Now().UTC().Truncate(time.Second)
	approve := issueAndSettle(t, store, seeded[0], "desk-approve", core.HumanDecisionApprove, now)
	if cleardevtest.RequirementStatus(t, store, seeded[0].RequirementID, seeded[0].VersionID) != core.RequirementVersionStatusConfirmed {
		t.Fatal("approved version was not confirmed")
	}
	effect, ok, err := store.GetClearDevHumanDecisionEffect(ctx, seeded[0].RequestID)
	if err != nil || !ok || effect.Decision != core.HumanDecisionApprove || effect.EventSequence <= 0 {
		t.Fatalf("approve effect = %+v ok=%v err=%v", effect, ok, err)
	}
	if err := store.SettleClearDevHumanDecision(ctx, approve, now.Add(time.Second)); err == nil {
		t.Fatal("replayed approve was accepted")
	}
	if cleardevtest.RequirementStatus(t, store, seeded[0].RequirementID, seeded[0].VersionID) != core.RequirementVersionStatusConfirmed {
		t.Fatal("replay changed the confirmed version")
	}

	later := issueOffer(t, store, seeded[1], "desk-reject", now.Add(2*time.Second))
	if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(later, core.HumanDecisionLater), now.Add(2*time.Second)); err != nil {
		t.Fatalf("later: %v", err)
	}
	if cleardevtest.RequirementStatus(t, store, seeded[1].RequirementID, seeded[1].VersionID) != core.RequirementVersionStatusPendingConfirmation {
		t.Fatal("later changed the requirement version")
	}
	issueAndSettle(t, store, seeded[1], "desk-reject", core.HumanDecisionReject, now.Add(3*time.Second))
	if cleardevtest.RequirementStatus(t, store, seeded[1].RequirementID, seeded[1].VersionID) != core.RequirementVersionStatusRejected {
		t.Fatal("rejected version was not rejected")
	}
	snapshot, _, err := store.GetClearDevRequirement(ctx, seeded[1].RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	foundReject := false
	for _, event := range snapshot.Events {
		if event.Action == core.ActionRejectRequirementVersion && event.Outcome == core.EventAccepted {
			if event.Reason != core.ReasonRejectedByDesktopHuman || event.ReasonText != core.RejectedByDesktopHumanReason {
				t.Fatalf("reject event = %#v", event)
			}
			foundReject = true
		}
	}
	if !foundReject {
		t.Fatal("rejected version is missing the desktop human reason")
	}
}

func TestClearDevHumanDecisionExpiryAndBadHashHaveNoDomainEffect(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seeded := cleardevtest.SeedTwoPendingConfirmations(t, store, "ao-human-invalid")
	now := time.Now().UTC().Truncate(time.Second)
	expired, err := store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{
		RequestID: seeded[0].RequestID, DesktopRunID: "desk-exp", Nonce: mustNonce(t),
		IssuedAt: now.Add(-11 * time.Minute), ExpiresAt: now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(expired, core.HumanDecisionApprove), now); err == nil {
		t.Fatal("expired offer was applied")
	}
	if cleardevtest.RequirementStatus(t, store, seeded[0].RequirementID, seeded[0].VersionID) != core.RequirementVersionStatusPendingConfirmation {
		t.Fatal("expired offer changed the version")
	}

	offer := issueOffer(t, store, seeded[1], "desk-hash", now)
	offer.ContentSHA256 = strings.Repeat("c", 64)
	if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(offer, core.HumanDecisionApprove), now); err == nil {
		t.Fatal("bad content hash was applied")
	}
	if cleardevtest.RequirementStatus(t, store, seeded[1].RequirementID, seeded[1].VersionID) != core.RequirementVersionStatusPendingConfirmation {
		t.Fatal("bad hash changed the version")
	}
}

func TestClearDevHumanDecisionConcurrentApproveReject(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	first := sqlitetest.MustOpenAt(t, dataDir)
	second := openSecondClearDevStore(t, dataDir)
	seeded := cleardevtest.SeedTwoPendingConfirmations(t, first, "ao-human-race")
	now := time.Now().UTC().Truncate(time.Second)
	approveOffer := issueOffer(t, first, seeded[0], "desk-a", now)
	rejectOffer := issueOffer(t, first, seeded[0], "desk-b", now.Add(time.Second))
	errs := concurrentClearDevCalls(
		func() error {
			return first.SettleClearDevHumanDecision(ctx, resultFromOffer(approveOffer, core.HumanDecisionApprove), now.Add(2*time.Second))
		},
		func() error {
			return second.SettleClearDevHumanDecision(ctx, resultFromOffer(rejectOffer, core.HumanDecisionReject), now.Add(2*time.Second))
		},
	)
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
			continue
		}
		var rule *core.RuleError
		if errors.As(err, &rule) || isSQLiteBusy(err) || strings.Contains(strings.ToLower(err.Error()), "already") {
			continue
		}
		t.Fatalf("concurrent settle unexpected error: %v", err)
	}
	if successes != 1 {
		t.Fatalf("concurrent settle successes = %d errors=%v", successes, errs)
	}
	status := cleardevtest.RequirementStatus(t, first, seeded[0].RequirementID, seeded[0].VersionID)
	if status != core.RequirementVersionStatusConfirmed && status != core.RequirementVersionStatusRejected {
		t.Fatalf("raced version status = %s", status)
	}
}

func TestClearDevDesktopChannelApprovesAndRejects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := sqlitetest.MustOpen(t)
	seeded := cleardevtest.SeedTwoPendingConfirmations(t, store, "ao-human-channel")
	service := cleardevsvc.New(cleardevsvc.Deps{Facts: store, HumanDecisions: store, AO: store})
	token, err := humanauthority.NewToken()
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
	t.Cleanup(func() { _ = listener.Close() })
	choices := map[string]core.HumanDecisionChoice{
		seeded[0].RequestID: core.HumanDecisionApprove,
		seeded[1].RequestID: core.HumanDecisionReject,
	}
	go func() {
		_ = humanauthority.ServeFakeDesktop(ctx, listener, token, desktopRunID, func(offer core.HumanDecisionOffer) core.HumanDecisionChoice {
			return choices[offer.RequestID]
		})
	}()
	go humanauthority.NewClient(endpoint, token, desktopRunID, service, nil).Run(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		approve := cleardevtest.RequirementStatus(t, store, seeded[0].RequirementID, seeded[0].VersionID)
		reject := cleardevtest.RequirementStatus(t, store, seeded[1].RequirementID, seeded[1].VersionID)
		if approve == core.RequirementVersionStatusConfirmed && reject == core.RequirementVersionStatusRejected {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("channel did not settle both versions: approve=%s reject=%s",
		cleardevtest.RequirementStatus(t, store, seeded[0].RequirementID, seeded[0].VersionID),
		cleardevtest.RequirementStatus(t, store, seeded[1].RequirementID, seeded[1].VersionID))
}

func TestClearDevHumanDecisionRuleRejectDoesNotSettleRequest(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	seeded := cleardevtest.SeedTwoPendingConfirmations(t, store, "ao-human-cancel")
	now := time.Now().UTC().Truncate(time.Second)
	offer := issueOffer(t, store, seeded[0], "desk-cancel", now)
	if err := applyClearDev(t, store, core.ActionCancelRequirement, seeded[0].RequirementID, now.Add(time.Second), true, "stop"); err != nil {
		t.Fatal(err)
	}
	err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(offer, core.HumanDecisionApprove), now.Add(2*time.Second))
	var rule *core.RuleError
	if !errors.As(err, &rule) || rule.Code != core.ReasonRequirementCancelled {
		t.Fatalf("settle after cancel = %v", err)
	}
	request, ok, getErr := store.GetClearDevHumanDecisionRequest(ctx, seeded[0].RequestID)
	if getErr != nil || !ok || request.Status != core.HumanDecisionRequestPending || request.Decision != "" {
		t.Fatalf("request after rule reject = %+v ok=%v err=%v", request, ok, getErr)
	}
	if cleardevtest.RequirementStatus(t, store, seeded[0].RequirementID, seeded[0].VersionID) != core.RequirementVersionStatusPendingConfirmation {
		t.Fatal("rule reject changed the requirement version")
	}
	if _, hasEffect, effectErr := store.GetClearDevHumanDecisionEffect(ctx, seeded[0].RequestID); effectErr != nil || hasEffect {
		t.Fatalf("effect after rule reject hasEffect=%v err=%v", hasEffect, effectErr)
	}
	nonceSHA, err := core.HumanDecisionNonceSHA256(offer.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	db := openClearDevRawDB(t, dataDir)
	t.Cleanup(func() { _ = db.Close() })
	var consumed sql.NullTime
	var outcome string
	if err := db.QueryRowContext(ctx, `SELECT consumed_at, outcome FROM cleardev_human_decision_dispatches WHERE nonce_sha256 = ?`, nonceSHA).Scan(&consumed, &outcome); err != nil {
		t.Fatal(err)
	}
	if consumed.Valid || outcome != "" {
		t.Fatalf("dispatch was consumed after rule reject: consumed=%v outcome=%q", consumed, outcome)
	}
	snapshot, _, err := store.GetClearDevRequirement(ctx, seeded[0].RequirementID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range snapshot.Events {
		if event.Action == core.ActionSettleHumanDecision && event.Outcome == core.EventAccepted {
			t.Fatalf("accepted settle event was written: %#v", event)
		}
	}
}

func issueOffer(t *testing.T, store *sqlite.Store, seeded cleardevtest.PendingConfirmation, desktopRunID string, at time.Time) core.HumanDecisionOffer {
	t.Helper()
	offer, err := store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{
		RequestID: seeded.RequestID, DesktopRunID: desktopRunID, Nonce: mustNonce(t),
		IssuedAt: at, ExpiresAt: at.Add(core.HumanDecisionOfferTTL),
	})
	if err != nil {
		t.Fatal(err)
	}
	return offer
}

func issueAndSettle(t *testing.T, store *sqlite.Store, seeded cleardevtest.PendingConfirmation, desktopRunID string, decision core.HumanDecisionChoice, at time.Time) core.HumanDecisionResult {
	t.Helper()
	offer := issueOffer(t, store, seeded, desktopRunID, at)
	result := resultFromOffer(offer, decision)
	if err := store.SettleClearDevHumanDecision(context.Background(), result, at); err != nil {
		t.Fatal(err)
	}
	return result
}

func resultFromOffer(offer core.HumanDecisionOffer, decision core.HumanDecisionChoice) core.HumanDecisionResult {
	return core.HumanDecisionResult{
		ProtocolVersion: offer.ProtocolVersion, Kind: core.HumanDecisionResultKind,
		DesktopRunID: offer.DesktopRunID, RequestID: offer.RequestID, DecisionKind: offer.DecisionKind,
		BindingSchemaVersion: offer.BindingSchemaVersion, Binding: append(json.RawMessage(nil), offer.Binding...),
		ContentSHA256: offer.ContentSHA256, Nonce: offer.Nonce, Decision: decision,
	}
}

func mustNonce(t *testing.T) string {
	t.Helper()
	nonce, err := humanauthority.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return nonce
}
