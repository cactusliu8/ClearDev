package store_test

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
	svc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestHumanDecisionReopenRetainsOriginalAndRejectsOldResults(t *testing.T) {
	for _, outcome := range []core.HumanDecisionDispatchOutcome{core.HumanDecisionDispatchExpired, core.HumanDecisionDispatchDisconnected, core.HumanDecisionDispatchLater} {
		t.Run(string(outcome), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store := sqlitetest.MustOpenAt(t, dir)
			seed := cleardevtest.SeedTwoPendingConfirmations(t, store, "reopen-original")[0]
			now := time.Now().UTC().Truncate(time.Second)
			old := issueOffer(t, store, seed, "desktop", now)
			req, _, _ := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
			switch outcome {
			case core.HumanDecisionDispatchLater:
				if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(old, core.HumanDecisionLater), now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			case core.HumanDecisionDispatchDisconnected:
				if err := store.DismissClearDevHumanDecisionDispatch(ctx, core.DismissHumanDecisionDispatchCommand{Nonce: old.Nonce, DesktopRunID: "desktop", Outcome: outcome, At: now.Add(time.Second)}); err != nil {
					t.Fatal(err)
				}
			}
			history, _ := store.ListClearDevHumanDecisionDispatchHistory(ctx, seed.RequestID)
			at := now.Add(core.HumanDecisionOfferTTL + time.Second)
			service := svc.New(svc.Deps{Facts: store, HumanDecisions: store, AO: store, DesktopRunID: "desktop", Clock: func() time.Time { return at }})
			input := svc.ReopenHumanDecisionInput{RequestID: "original-reopen", DecisionRequestID: req.ID, ContentSHA256: req.ContentSHA256, PreviousDispatchID: history[0].ID}
			if _, err := service.ReopenHumanDecision(ctx, seed.RequirementID, input); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ReopenHumanDecision(ctx, seed.RequirementID, input); err != nil {
				t.Fatal("same registration failed", err)
			}
			changed := input
			changed.ContentSHA256 = strings.Repeat("b", 64)
			if _, err := service.ReopenHumanDecision(ctx, seed.RequirementID, changed); err == nil {
				t.Fatal("conflicting replay accepted")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := sqlite.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			service = svc.New(svc.Deps{Facts: store, HumanDecisions: store, AO: store, DesktopRunID: "desktop", Clock: func() time.Time { return at }})
			rows, err := store.ListClearDevHumanDecisionReopens(ctx, req.ID)
			if err != nil || len(rows) != 1 {
				t.Fatalf("registration lost: %v", err)
			}
			fresh, ok, err := service.IssueHumanDecisionOffer(ctx, "desktop")
			if err != nil || !ok {
				t.Fatalf("reopen offer unavailable: %v", err)
			}
			if fresh.RequestID != old.RequestID || fresh.ContentSHA256 != old.ContentSHA256 || string(fresh.Binding) != string(old.Binding) || fresh.Nonce == old.Nonce {
				t.Fatal("reopen changed original decision or reused nonce")
			}
			history, err = store.ListClearDevHumanDecisionDispatchHistory(ctx, req.ID)
			if err != nil || len(history) != 2 || history[0].Outcome != outcome || history[1].ID != rows[0].NextDispatchID {
				t.Fatalf("history changed: %+v %v", history, err)
			}
			if _, err := service.ReopenHumanDecision(ctx, seed.RequirementID, input); err != nil {
				t.Fatal("post-dispatch lost-response replay rejected", err)
			}
			history, _ = store.ListClearDevHumanDecisionDispatchHistory(ctx, req.ID)
			if len(history) != 2 {
				t.Fatal("registration replay issued another offer")
			}
			if err := service.ApplyHumanDecisionResult(ctx, resultFromOffer(old, core.HumanDecisionApprove)); err == nil {
				t.Fatal("old approval applied")
			}
			if err := service.ApplyHumanDecisionResult(ctx, resultFromOffer(fresh, core.HumanDecisionLater)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: req.ID, DesktopRunID: "desktop", Nonce: mustReopenNonce(t), IssuedAt: at, ExpiresAt: at.Add(core.HumanDecisionOfferTTL), InitialOnly: true}); err == nil {
				t.Fatal("already shown decision automatically reissued")
			}
			original, _, _ := store.GetClearDevHumanDecisionRequest(ctx, req.ID)
			if original.BindingJSON != req.BindingJSON || original.DisplayJSON != req.DisplayJSON || original.Status != core.HumanDecisionRequestPending {
				t.Fatal("original decision was changed")
			}
			if _, found, err := store.GetClearDevHumanDecisionEffect(ctx, req.ID); err != nil || found {
				t.Fatal("display recovery created an approval effect")
			}
			if cleardevtest.RequirementStatus(t, store, seed.RequirementID, seed.VersionID) != core.RequirementVersionStatusPendingConfirmation {
				t.Fatal("display recovery confirmed requirement")
			}
		})
	}
}
func mustReopenNonce(t *testing.T) string {
	t.Helper()
	n, err := humanauthority.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestHumanDecisionReopenConcurrentRegistrationAndChangedBinding(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seed := cleardevtest.SeedTwoPendingConfirmations(t, store, "reopen-concurrent")[0]
	now := time.Now().UTC().Truncate(time.Second)
	old := issueOffer(t, store, seed, "desktop", now)
	req, _, _ := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
	history, _ := store.ListClearDevHumanDecisionDispatchHistory(ctx, req.ID)
	intent := core.HumanDecisionReopen{RequestID: "same-request", DecisionRequestID: req.ID, ContentSHA256: req.ContentSHA256, PreviousDispatchID: history[0].ID, DesktopRunID: "desktop", CreatedAt: now.Add(core.HumanDecisionOfferTTL + time.Second)}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.RecordClearDevHumanDecisionReopen(ctx, seed.RequirementID, intent)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := store.ListClearDevHumanDecisionReopens(ctx, req.ID)
	if len(rows) != 1 {
		t.Fatal("concurrent replay duplicated registration")
	}
	competitor := intent
	competitor.RequestID = "competing-request"
	if _, err := store.RecordClearDevHumanDecisionReopen(ctx, seed.RequirementID, competitor); err == nil {
		t.Fatal("competing registration accepted")
	}
	// Cancel through the existing transaction and explicit test-only trusted action.
	if err := applyClearDev(t, store, core.ActionCancelRequirement, seed.RequirementID, intent.CreatedAt, true, "fixture cancellation"); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateClearDevHumanDecisionReopen(ctx, req.ID, intent.CreatedAt); err == nil {
		t.Fatal("cancelled decision still current")
	}
	if _, err := store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: req.ID, DesktopRunID: "desktop", Nonce: mustReopenNonce(t), IssuedAt: intent.CreatedAt, ExpiresAt: intent.CreatedAt.Add(core.HumanDecisionOfferTTL), ReopenRequestID: intent.RequestID}); err == nil {
		t.Fatal("cancelled original was reopened")
	}
	if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(old, core.HumanDecisionApprove), intent.CreatedAt); err == nil {
		t.Fatal("expired original result approved cancelled requirement")
	}
}
func TestHumanDecisionReopenReadOnlyHistoryAndDowngradeGuard(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	seed := cleardevtest.SeedTwoPendingConfirmations(t, store, "reopen-read")[0]
	now := time.Now().UTC().Truncate(time.Second)
	old := issueOffer(t, store, seed, "desktop", now)
	req, _, _ := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
	history, _ := store.ListClearDevHumanDecisionDispatchHistory(ctx, req.ID)
	service := svc.New(svc.Deps{Facts: store, HumanDecisions: store, AO: store, DesktopRunID: "desktop", Clock: func() time.Time { return now.Add(core.HumanDecisionOfferTTL + time.Second) }})
	db := openClearDevRawDB(t, dir)
	defer func() { _ = db.Close() }()
	var before, after int
	if err := db.QueryRow(`SELECT count(*) FROM change_log`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		view, err := service.GetHumanDecisionDisplays(ctx, seed.RequirementID)
		if err != nil || !view.Items[0].CanReopen {
			t.Fatal("read did not classify expired display", err)
		}
	}
	if err := db.QueryRow(`SELECT count(*) FROM change_log`).Scan(&after); err != nil || after != before {
		t.Fatal("read changed persistent facts")
	}
	if _, err := service.ReopenHumanDecision(ctx, seed.RequirementID, svc.ReopenHumanDecisionInput{RequestID: "keep", DecisionRequestID: req.ID, ContentSHA256: req.ContentSHA256, PreviousDispatchID: history[0].ID}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE cleardev_human_decision_reopens SET content_sha256='changed'`, `DELETE FROM cleardev_human_decision_reopens`, `INSERT OR REPLACE INTO cleardev_human_decision_reopens SELECT * FROM cleardev_human_decision_reopens`} {
		if _, err := db.Exec(query); err == nil {
			t.Fatal("reopen history could be overwritten")
		}
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var versionBefore, versionAfter int
	_ = db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&versionBefore)
	if _, err := provider.DownTo(ctx, 181); err == nil {
		t.Fatal("downgrade erased protected facts")
	}
	_ = db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&versionAfter)
	if versionBefore != versionAfter {
		t.Fatal("refused downgrade moved version")
	}
	if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(old, core.HumanDecisionApprove), now.Add(core.HumanDecisionOfferTTL+time.Second)); err == nil {
		t.Fatal("expired predecessor was usable")
	}
}

func TestHumanDecisionReopenInvalidatesOlderDesktopNonce(t *testing.T) {
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	seed := cleardevtest.SeedTwoPendingConfirmations(t, store, "reopen-old-desktop")[0]
	now := time.Now().UTC().Truncate(time.Second)
	orphan := issueOffer(t, store, seed, "previous-desktop", now)
	latest := issueOffer(t, store, seed, "current-desktop", now.Add(time.Second))
	if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(latest, core.HumanDecisionLater), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	request, _, _ := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
	history, _ := store.ListClearDevHumanDecisionDispatchHistory(ctx, request.ID)
	_, err := store.RecordClearDevHumanDecisionReopen(ctx, seed.RequirementID, core.HumanDecisionReopen{RequestID: "explicit-reopen", DecisionRequestID: request.ID, ContentSHA256: request.ContentSHA256, PreviousDispatchID: history[1].ID, DesktopRunID: "current-desktop", CreatedAt: now.Add(3 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SettleClearDevHumanDecision(ctx, resultFromOffer(orphan, core.HumanDecisionApprove), now.Add(4*time.Second)); err == nil {
		t.Fatal("older desktop nonce survived explicit reopen")
	}
	if _, found, err := store.GetClearDevHumanDecisionEffect(ctx, request.ID); err != nil || found {
		t.Fatal("obsolete desktop approval effect exists")
	}
}

func TestHumanDecisionReopenInitialOfferRejectsStoppedRequirement(t *testing.T) {
	for _, stop := range []string{"cancelled", "paused"} {
		for _, previouslyShown := range []bool{false, true} {
			name := stop + "/first-display"
			if previouslyShown {
				name = stop + "/new-desktop"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				store := sqlitetest.MustOpenAt(t, dir)
				seed := cleardevtest.SeedTwoPendingConfirmations(t, store, "initial-stopped")[0]
				now := time.Now().UTC().Truncate(time.Second)
				if previouslyShown {
					old := issueOffer(t, store, seed, "old-desktop", now)
					if err := store.DismissClearDevHumanDecisionDispatch(ctx, core.DismissHumanDecisionDispatchCommand{Nonce: old.Nonce, DesktopRunID: "old-desktop", Outcome: core.HumanDecisionDispatchDisconnected, At: now.Add(time.Second)}); err != nil {
						t.Fatal(err)
					}
				}
				stopReopenRequirement(t, store, dir, seed.RequirementID, stop, now.Add(2*time.Second))
				request, _, err := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
				if err != nil || request.Status != core.HumanDecisionRequestPending {
					t.Fatalf("original pending request unavailable: %+v %v", request, err)
				}
				db := openClearDevRawDB(t, dir)
				defer func() { _ = db.Close() }()
				before := decisionReopenFactCounts(t, db)
				at := now.Add(3 * time.Second)
				_, err = store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: request.ID, DesktopRunID: "new-desktop", Nonce: mustReopenNonce(t), IssuedAt: at, ExpiresAt: at.Add(core.HumanDecisionOfferTTL), InitialOnly: true})
				if err == nil {
					t.Error("initial offer issued a new nonce for a stopped requirement")
				}
				if after := decisionReopenFactCounts(t, db); after != before {
					t.Errorf("refused offer changed dispatch/history/effects/CDC: before=%+v after=%+v", before, after)
				}
				after, _, err := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
				if err != nil || !reflect.DeepEqual(request, after) {
					t.Fatal("refused offer changed the original pending request", err)
				}
			})
		}
	}
}

func TestHumanDecisionReopenServiceSkipsStoppedPendingRequirement(t *testing.T) {
	for _, stop := range []string{"cancelled", "paused"} {
		t.Run(stop, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store := sqlitetest.MustOpenAt(t, dir)
			cleardevtest.SeedTwoPendingConfirmations(t, store, "scan-stopped")
			pending, err := store.ListPendingClearDevHumanDecisionRequests(ctx)
			if err != nil || len(pending) != 2 {
				t.Fatalf("fixture did not create two pending requests: %+v %v", pending, err)
			}
			stopped, current := pending[0], pending[1]
			now := time.Now().UTC().Truncate(time.Second)
			stopReopenRequirement(t, store, dir, stopped.DevelopmentRequirementID, stop, now)
			request, _, err := store.GetClearDevHumanDecisionRequest(ctx, stopped.ID)
			if err != nil {
				t.Fatal(err)
			}
			service := svc.New(svc.Deps{Facts: store, HumanDecisions: store, AO: store, DesktopRunID: "scan-desktop", Clock: func() time.Time { return now.Add(time.Second) }})
			for _, desktop := range []string{"scan-desktop", "another-desktop"} {
				offer, ok, err := service.IssueHumanDecisionOffer(ctx, desktop)
				if err != nil || !ok || offer.RequestID != current.ID {
					t.Errorf("scan did not skip the stopped request and offer the next current request: request=%s ok=%v err=%v", offer.RequestID, ok, err)
				}
				if _, ok, err := service.IssueHumanDecisionOffer(ctx, desktop); err != nil || ok {
					t.Errorf("same desktop issued another pending offer: ok=%v err=%v", ok, err)
				}
			}
			history, err := store.ListClearDevHumanDecisionDispatchHistory(ctx, stopped.ID)
			if err != nil || len(history) != 0 {
				t.Errorf("scan issued a nonce for the stopped request: %+v %v", history, err)
			}
			after, _, err := store.GetClearDevHumanDecisionRequest(ctx, stopped.ID)
			if err != nil || request.Status != core.HumanDecisionRequestPending || !reflect.DeepEqual(request, after) {
				t.Fatal("scan changed the retained stopped request", err)
			}
			if _, found, err := store.GetClearDevHumanDecisionEffect(ctx, stopped.ID); err != nil || found {
				t.Fatal("scan applied a stopped decision", err)
			}
		})
	}
}

func TestHumanDecisionReopenRegistrationRejectsPausedRequirement(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	seed := cleardevtest.SeedTwoPendingConfirmations(t, store, "register-paused")[0]
	now := time.Now().UTC().Truncate(time.Second)
	old := issueOffer(t, store, seed, "desktop", now)
	if err := store.DismissClearDevHumanDecisionDispatch(ctx, core.DismissHumanDecisionDispatchCommand{Nonce: old.Nonce, DesktopRunID: "desktop", Outcome: core.HumanDecisionDispatchDisconnected, At: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	request, _, err := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.ListClearDevHumanDecisionDispatchHistory(ctx, seed.RequestID)
	if err != nil || len(history) != 1 {
		t.Fatal("original dispatch unavailable", err)
	}
	stopReopenRequirement(t, store, dir, seed.RequirementID, "paused", now.Add(2*time.Second))
	db := openClearDevRawDB(t, dir)
	defer func() { _ = db.Close() }()
	before := decisionReopenFactCounts(t, db)
	at := now.Add(3 * time.Second)
	if err := store.ValidateClearDevHumanDecisionReopen(ctx, request.ID, at); err == nil {
		t.Error("paused requirement passed the current decision check")
	}
	service := svc.New(svc.Deps{Facts: store, HumanDecisions: store, AO: store, DesktopRunID: "desktop", Clock: func() time.Time { return at }})
	view, err := service.GetHumanDecisionDisplays(ctx, seed.RequirementID)
	if err != nil || len(view.Items) != 1 || view.Items[0].DecisionRequestID != request.ID || view.Items[0].CanReopen || view.Items[0].ReasonCode != "DECISION_NOT_CURRENT" {
		t.Errorf("paused requirement remained reopenable in the read-only view: %+v %v", view, err)
	}
	_, err = store.RecordClearDevHumanDecisionReopen(ctx, seed.RequirementID, core.HumanDecisionReopen{RequestID: "paused-registration", DecisionRequestID: request.ID, ContentSHA256: request.ContentSHA256, PreviousDispatchID: history[0].ID, DesktopRunID: "desktop", CreatedAt: at})
	if err == nil {
		t.Error("paused requirement accepted a reopen registration")
	}
	if after := decisionReopenFactCounts(t, db); after != before {
		t.Errorf("refused paused registration changed history/CDC: before=%+v after=%+v", before, after)
	}
}

func TestHumanDecisionReopenDispatchRejectsPauseAfterRegistration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dir)
	seed := cleardevtest.SeedTwoPendingConfirmations(t, store, "dispatch-paused")[0]
	now := time.Now().UTC().Truncate(time.Second)
	old := issueOffer(t, store, seed, "desktop", now)
	if err := store.DismissClearDevHumanDecisionDispatch(ctx, core.DismissHumanDecisionDispatchCommand{Nonce: old.Nonce, DesktopRunID: "desktop", Outcome: core.HumanDecisionDispatchDisconnected, At: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	request, _, err := store.GetClearDevHumanDecisionRequest(ctx, seed.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := store.ListClearDevHumanDecisionDispatchHistory(ctx, seed.RequestID)
	if err != nil || len(history) != 1 {
		t.Fatal("original dispatch unavailable", err)
	}
	intent := core.HumanDecisionReopen{RequestID: "before-pause", DecisionRequestID: request.ID, ContentSHA256: request.ContentSHA256, PreviousDispatchID: history[0].ID, DesktopRunID: "desktop", CreatedAt: now.Add(2 * time.Second)}
	registered, err := store.RecordClearDevHumanDecisionReopen(ctx, seed.RequirementID, intent)
	if err != nil {
		t.Fatal("current requirement registration failed", err)
	}
	stopReopenRequirement(t, store, dir, seed.RequirementID, "paused", now.Add(3*time.Second))
	db := openClearDevRawDB(t, dir)
	defer func() { _ = db.Close() }()
	before := decisionReopenFactCounts(t, db)
	at := now.Add(4 * time.Second)
	_, err = store.IssueClearDevHumanDecisionDispatch(ctx, core.IssueHumanDecisionDispatchCommand{RequestID: request.ID, DesktopRunID: "desktop", Nonce: mustReopenNonce(t), IssuedAt: at, ExpiresAt: at.Add(core.HumanDecisionOfferTTL), ReopenRequestID: intent.RequestID})
	if err == nil {
		t.Error("reopen issued a new nonce after the requirement paused")
	}
	if after := decisionReopenFactCounts(t, db); after != before {
		t.Errorf("refused paused dispatch changed history/CDC: before=%+v after=%+v", before, after)
	}
	reopens, err := store.ListClearDevHumanDecisionReopens(ctx, seed.RequestID)
	if err != nil || len(reopens) != 1 || !reflect.DeepEqual(reopens[0], registered) {
		t.Fatal("pause erased or changed the original reopen registration", err)
	}
	afterHistory, err := store.ListClearDevHumanDecisionDispatchHistory(ctx, seed.RequestID)
	if err != nil || !reflect.DeepEqual(afterHistory, history) {
		t.Errorf("pause did not retain the ended original dispatch: %+v %v", afterHistory, err)
	}
}

func stopReopenRequirement(t *testing.T, store *sqlite.Store, dir, id, stop string, at time.Time) {
	t.Helper()
	if stop == "cancelled" {
		if err := applyClearDev(t, store, core.ActionCancelRequirement, id, at, true, "fixture cancellation before display"); err != nil {
			t.Fatal(err)
		}
		return
	}
	// No requirement-pause API exists. Use a legal persisted legacy pause marker
	// in this isolated test database, preserving the original state as its source.
	db := openClearDevRawDB(t, dir)
	defer func() { _ = db.Close() }()
	var originalState string
	if err := db.QueryRow(`SELECT state FROM cleardev_development_projects WHERE id=?`, id).Scan(&originalState); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cleardev_development_projects SET paused_from_state=state,state='NEEDS_HUMAN' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	var state, pausedFrom string
	if err := db.QueryRow(`SELECT state,paused_from_state FROM cleardev_development_projects WHERE id=?`, id).Scan(&state, &pausedFrom); err != nil || state != "NEEDS_HUMAN" || pausedFrom == "" || pausedFrom != originalState {
		t.Fatalf("fixture did not persist a real pause marker: state=%s pausedFrom=%s originalState=%s err=%v", state, pausedFrom, originalState, err)
	}
}

type reopenFactCounts struct {
	dispatches, reopens, effects, changes int
}

func decisionReopenFactCounts(t *testing.T, db *sql.DB) reopenFactCounts {
	t.Helper()
	var counts reopenFactCounts
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM cleardev_human_decision_dispatches), (SELECT count(*) FROM cleardev_human_decision_reopens), (SELECT count(*) FROM cleardev_human_decision_effects), (SELECT count(*) FROM change_log)`).Scan(&counts.dispatches, &counts.reopens, &counts.effects, &counts.changes); err != nil {
		t.Fatal(err)
	}
	return counts
}
