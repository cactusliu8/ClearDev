package cleardev

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// An extra grant authorizes exactly one implementation and its checks/review.
// Unused earlier automatic repair capacity is not a second implementation grant.
func TestBoundedMailExtraReviewerReworkCannotOpenAnotherAttempt(t *testing.T) {
	f, h := newBoundedMailFixture(t, 3)
	h.reworkOnce = true
	f.confirm(t)
	before := boundedExecution(t, f)
	if len(before.Dispatches) != 3 || len(before.Reviews) != 0 {
		t.Fatal("expected three development failures before the precise grant")
	}
	applyFakeDesktopDecision(t, f.store, f.service, f.clock, f.view.Requirement.ID, core.HumanDecisionKindExtraMailAttempt, core.HumanDecisionApprove)
	after := boundedExecution(t, f)
	assertMailNotCompleted(t, f)
	if len(after.Dispatches) != 4 || len(after.Reviews) != 1 || f.counts().builderSends != 4 {
		t.Fatalf("one extra authorization started more work: dispatches=%d reviews=%d sends=%d", len(after.Dispatches), len(after.Reviews), f.counts().builderSends)
	}
	if after.Dispatches[3].ReasonCode != core.MailAttemptFinalLimitReason {
		t.Fatalf("extra REWORK did not stop with final-limit reason: %s", after.Dispatches[3].ReasonCode)
	}
	slots, err := f.store.ListClearDevMailAttempts(context.Background(), after.Run.ID)
	if err != nil || len(slots) != 4 || slots[3].Kind != core.MailAttemptHumanExtra {
		t.Fatalf("slots=%+v err=%v", slots, err)
	}
	// An attempted direct storage insertion must also be rejected, without
	// dropping guards or altering any existing state.
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO cleardev_mail_attempt_slots(dispatch_id,execution_run_id,task_id,round,attempt_kind,created_at) VALUES(?,?,?,4,'REVIEW_REPAIR',CURRENT_TIMESTAMP)`, "unapproved-after-extra", after.Run.ID, after.Tasks[0].ID)
	_ = db.Close()
	if err == nil || (!strings.Contains(err.Error(), "mail human extra attempt is terminal") &&
		!strings.Contains(err.Error(), "mail attempt has no bounded automatic or exact human authorization")) {
		t.Fatalf("terminal storage guard did not reject: %v", err)
	}
	counts := f.counts()
	f.reopen(t)
	f.service.checks, f.service.inspector = h, h
	if err := f.service.ResumeComplexStandardExecutions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.counts() != counts {
		t.Fatal("restart reopened exhausted human-extra attempt")
	}
}
