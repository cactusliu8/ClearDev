package cleardev

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Real SQLite/native envelope and executor ledger, explicitly fake model/Git/checks.
func legacyFinalRecheckFixture(t *testing.T) (*autoExecutionFixture, *requirementFinalReviewHarness) {
	t.Helper()
	f, h := newRequirementFinalReviewFixture(t)
	f.service.boundedMailAttempts = true
	h.verdict = "NEEDS_HUMAN"
	f.service.finalReviews = interceptFinalReviewStore{RequirementFinalReviewFactStore: f.store, create: func(r core.RequirementFinalReview) (core.RequirementFinalReview, error) {
		var p core.RequirementFinalReviewPacket
		if err := json.Unmarshal([]byte(r.ReviewPacketJSON), &p); err != nil {
			return r, err
		}
		p.AttemptEvidence = nil // legacy packet, not fabricated persisted terminal state
		raw, err := json.Marshal(p)
		if err != nil {
			return r, err
		}
		r.ReviewPacketJSON = string(raw)
		r.ReviewPacketSHA256 = coreDigest(raw)
		r.PromptSHA256 = coreDigest([]byte(requirementFinalReviewPrompt(r)))
		return r, nil
	}}
	f.confirm(t)
	e := finalReviewExecution(t, f)
	if e.FinalReview == nil || e.FinalReview.Verdict != "NEEDS_HUMAN" {
		t.Fatalf("legacy stop absent: %+v", e.FinalReview)
	}
	f.service.finalReviews = f.store
	return f, h
}

func finalRecheckOffer(t *testing.T, f *autoExecutionFixture) core.HumanDecisionResult {
	t.Helper()
	if err := f.service.BackfillHumanDecisionRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := pendingHumanRequestByKind(t, f.store, f.view.Requirement.ID, core.HumanDecisionKindFinalReviewRecheck)
	now := f.clock()
	o, err := f.store.IssueClearDevHumanDecisionDispatch(context.Background(), core.IssueHumanDecisionDispatchCommand{RequestID: req.ID, DesktopRunID: "explicit-final-recheck-test", Nonce: mustComplexNonce(t), IssuedAt: now, ExpiresAt: now.Add(core.HumanDecisionOfferTTL)})
	if err != nil {
		t.Fatal(err)
	}
	return core.HumanDecisionResult{ProtocolVersion: o.ProtocolVersion, Kind: core.HumanDecisionResultKind, DesktopRunID: o.DesktopRunID, RequestID: o.RequestID, DecisionKind: o.DecisionKind, BindingSchemaVersion: o.BindingSchemaVersion, Binding: o.Binding, ContentSHA256: o.ContentSHA256, Nonce: o.Nonce, Decision: core.HumanDecisionApprove}
}

func TestFinalRecheckNativeApprovalSameReviewerNoBuilder(t *testing.T) {
	f, h := legacyFinalRecheckFixture(t)
	before := finalReviewExecution(t, f)
	result := finalRecheckOffer(t, f)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.finalSends != 1 {
		t.Fatal("sent before native approval")
	}
	h.verdict = "PASS"
	if err := f.service.ApplyHumanDecisionResult(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	after := f.completed(t)
	if after.FinalReview == nil || after.FinalReview.PreviousReviewID != before.FinalReview.ID || after.FinalReview.Verdict != "PASS" || after.FinalReview.AOSessionID != before.FinalReview.AOSessionID || h.finalSends != 2 {
		t.Fatalf("recheck did not pass in original session: %+v sends=%d", after.FinalReview, h.finalSends)
	}
	if !reflect.DeepEqual(before.Dispatches, after.Dispatches) {
		t.Fatal("recheck changed Builder attempts")
	}
	var p core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(after.FinalReview.ReviewPacketJSON), &p); err != nil {
		t.Fatal(err)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), after.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.PreviousReview == nil || p.PreviousReview.Verdict != "NEEDS_HUMAN" || !reflect.DeepEqual(p.AttemptEvidence, core.NewFinalReviewAttemptEvidence(slots)) {
		t.Fatal("missing original stop or trusted attempt evidence")
	}
	if err := f.service.ApplyHumanDecisionResult(context.Background(), result); err == nil {
		t.Fatal("replayed approval accepted")
	}
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.finalSends != 2 {
		t.Fatal("replay resent final review")
	}
}

func TestFinalRecheckInvalidApprovalAndRepeatedStop(t *testing.T) {
	for _, mode := range []string{"nonce", "candidate", "desktop", "expired", "cancelled", "direction", "reject", "later", "second-stop", "dirty-candidate", "dirty-reviewer"} {
		t.Run(mode, func(t *testing.T) {
			f, h := legacyFinalRecheckFixture(t)
			before := finalReviewExecution(t, f)
			r := finalRecheckOffer(t, f)
			switch mode {
			case "nonce":
				r.Nonce = mustComplexNonce(t)
			case "candidate":
				var b core.FinalReviewRecheckBinding
				_ = json.Unmarshal(r.Binding, &b)
				b.CandidateSHA = forty("f")
				r.Binding, _ = json.Marshal(b)
			case "desktop":
				r.DesktopRunID = "wrong-desktop"
			case "expired":
				f.service.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
			case "cancelled":
				if err := f.service.CancelRequirement(context.Background(), f.view.Requirement.ID, "explicit recheck cancellation"); err != nil {
					t.Fatal(err)
				}
			case "direction":
				f.service.runBackground = func(func()) {}
				if _, err := f.service.ProposeDirectionIntent(context.Background(), f.view.Requirement.ID, ProposeDirectionIntentInput{RequestID: "final-recheck-direction", DevelopmentRequirementID: f.view.Requirement.ID, Message: directionMessage}); err != nil {
					t.Fatal(err)
				}
			case "later":
				r.Decision = core.HumanDecisionLater
			case "dirty-reviewer":
				h.dirtyReviewer = true
			case "reject":
				r.Decision = core.HumanDecisionReject
			case "dirty-candidate":
				h.dirtySource = true
			}
			err := f.service.ApplyHumanDecisionResult(context.Background(), r)
			invalid := mode == "nonce" || mode == "candidate" || mode == "desktop" || mode == "expired" || mode == "cancelled" || mode == "direction"
			if invalid && err == nil {
				t.Fatal("invalid approval accepted")
			}
			if !invalid && err != nil {
				t.Fatal(err)
			}
			after := finalReviewExecution(t, f)
			if !reflect.DeepEqual(before.Dispatches, after.Dispatches) {
				t.Fatal("Builder count changed")
			}
			assertMailNotCompleted(t, f)
			expected := 1
			if mode == "second-stop" {
				expected = 2
			}
			if h.finalSends != expected {
				t.Fatalf("unexpected sends %d", h.finalSends)
			}
			if err := f.service.BackfillHumanDecisionRequests(context.Background()); err != nil {
				t.Fatal(err)
			}
			pending, err := f.store.ListPendingClearDevHumanDecisionRequests(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "second-stop" || mode == "reject" {
				for _, p := range pending {
					if p.DecisionKind == core.HumanDecisionKindFinalReviewRecheck {
						t.Fatal("second authorization created")
					}
				}
			}
		})
	}
}

func TestFinalRecheckApprovalRestartAndConcurrentDecision(t *testing.T) {
	f, h := legacyFinalRecheckFixture(t)
	before := finalReviewExecution(t, f)
	r := finalRecheckOffer(t, f)
	f.service.runBackground = func(func()) {} // stop scheduling, not the approval transaction
	h.verdict = "PASS"
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if f.service.ApplyHumanDecisionResult(context.Background(), r) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || h.finalSends != 1 {
		t.Fatal("native decision not single-use or sent before restart")
	}
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := f.completed(t)
	if h.finalSends != 2 || after.FinalReview.AOSessionID != before.FinalReview.AOSessionID {
		t.Fatal("restart changed reviewer or duplicated send")
	}
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.finalSends != 2 {
		t.Fatal("completed restart resent")
	}
}

func TestFinalReviewPacketExplainsThreeDevelopmentAttempts(t *testing.T) {
	f, h := newRequirementFinalReviewFixture(t)
	f.service.boundedMailAttempts = true
	f.service.checks = &boundedMailChecks{mailFlowChecks: h.mailFlowChecks, failures: 2}
	f.confirm(t)
	e := finalReviewExecution(t, f)
	if e.FinalReview == nil || e.FinalReview.Verdict != "PASS" || e.Run.CompletedAt == nil {
		t.Fatalf("missing completed review: %+v", e.FinalReview)
	}
	var p core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(e.FinalReview.ReviewPacketJSON), &p); err != nil {
		t.Fatal(err)
	}
	if p.AttemptEvidence == nil || p.AttemptEvidence.DevelopmentLimit != 3 || len(p.AttemptEvidence.Slots) != 3 {
		t.Fatalf("normal attempt authority missing: %+v", p.AttemptEvidence)
	}
	for _, slot := range p.AttemptEvidence.Slots {
		if slot.Kind != core.MailAttemptDevelopment {
			t.Fatal("normal test failure counted as reviewer repair")
		}
	}
	if !strings.Contains(requirementFinalReviewPrompt(*e.FinalReview), "旧 maxReworkCount 不是正常开发尝试的总上限") {
		t.Fatal("missing precedence explanation")
	}
}

func TestFinalRecheckPreservesOriginalSQLHistory(t *testing.T) {
	f, h := legacyFinalRecheckFixture(t)
	old := *finalReviewExecution(t, f).FinalReview
	db, err := sql.Open("sqlite", "file:"+filepath.Join(f.dir, "ao.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	read := func() string {
		var text string
		if err := db.QueryRow(`SELECT json_object('id',id,'packet',review_packet_json,'prompt',prompt_sha256,'status',status,'session',ao_session_id,'result',result_id,'verdict',verdict,'summary',summary,'created',created_at,'settled',settled_at) FROM cleardev_requirement_final_reviews WHERE id=?`, old.ID).Scan(&text); err != nil {
			t.Fatal(err)
		}
		return text
	}
	before := read()
	h.verdict = "PASS"
	r := finalRecheckOffer(t, f)
	if err = f.service.ApplyHumanDecisionResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if before != read() {
		t.Fatal("original conclusion or transport changed")
	}
	for _, stmt := range []string{
		`UPDATE cleardev_requirement_final_reviews SET verdict='PASS' WHERE id=?`,
		`UPDATE cleardev_requirement_final_reviews SET rowid=rowid+1000 WHERE id=?`,
		`DELETE FROM cleardev_requirement_final_reviews WHERE id=?`,
		`INSERT OR REPLACE INTO cleardev_requirement_final_reviews SELECT * FROM cleardev_requirement_final_reviews WHERE id=?`,
	} {
		if _, err := db.Exec(stmt, old.ID); err == nil {
			t.Fatalf("history mutation accepted: %s", stmt)
		}
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM cleardev_requirement_final_reviews WHERE execution_run_id=?`, old.ExecutionRunID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("review history count %d %v", count, err)
	}
	if before != read() {
		t.Fatal("failed mutation changed old history")
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var beforeVersion int
	if err = db.QueryRow("SELECT max(version_id) FROM goose_db_version WHERE is_applied=1").Scan(&beforeVersion); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.DownTo(context.Background(), 146); err == nil {
		t.Fatal("downgrade removed authorized recheck history")
	}
	var version int
	if err = db.QueryRow("SELECT max(version_id) FROM goose_db_version WHERE is_applied=1").Scan(&version); err != nil || version != beforeVersion {
		t.Fatalf("recheck downgrade changed schema %d %v", version, err)
	}
	if before != read() {
		t.Fatal("downgrade changed original review")
	}

}

func TestFinalRecheckPreflightDefersWithoutConsumingSend(t *testing.T) {
	f, h := legacyFinalRecheckFixture(t)
	r := finalRecheckOffer(t, f)
	h.verdict = "PASS"
	checker := &scriptedControlledPreflight{fn: func(string) (ports.ChatControlledPreflight, error) {
		return livePreflightCatalog([]string{"gpt-5.6-terra"}, "gpt-5.6-terra"), ports.ErrChatQuotaExhausted
	}}
	f.service.preflightChecker = checker
	if err := f.service.ApplyHumanDecisionResult(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	e := finalReviewExecution(t, f)
	if h.finalSends != 1 || e.FinalReview.Status != "PENDING" {
		t.Fatalf("preflight consumed review: sends%d review%+v", h.finalSends, e.FinalReview)
	}
	f.reopen(t)
	attachRequirementFinalReviewFixture(f, h)
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.finalSends != 2 || f.completed(t).FinalReview.Verdict != "PASS" {
		t.Fatal("pending recheck did not resume")
	}
}
