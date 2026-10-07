package cleardev

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pressly/goose/v3"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// All migration attempts below target this test's temporary SQLite database.
// The requirement, checks, reviews and completion are created by the ordinary
// service/store workflow; downgrade is never used to manufacture a PASS.
func TestRequirementFinalReviewDowngradePreservesContractAndHistory(t *testing.T) {
	for _, boundary := range []string{"CONTRACT_ONLY", "REQUESTED", "SETTLED", "COMPLETED"} {
		t.Run(boundary, func(t *testing.T) {
			var f *autoExecutionFixture
			switch boundary {
			case "CONTRACT_ONLY":
				f, _ = newRequirementFinalReviewFixture(t)
				f.service.finalReviews = interceptFinalReviewStore{RequirementFinalReviewFactStore: f.store, create: func(review core.RequirementFinalReview) (core.RequirementFinalReview, error) {
					return review, errors.New("explicit stop before any final review request")
				}}
				f.confirm(t)
			case "COMPLETED":
				f, _ = newRequirementFinalReviewFixture(t)
				f.confirm(t)
				f.completed(t)
			default:
				f, _, _ = pauseAtFinalReview(t, boundary)
			}
			before := finalReviewExecution(t, f)
			required, err := core.RequirementFinalReviewRequired(before.Run)
			if err != nil || !required || (boundary == "CONTRACT_ONLY" && before.FinalReview != nil) {
				t.Fatalf("test did not freeze the intended contract: required=%v err=%v", required, err)
			}
			db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			provider, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../../storage/sqlite/migrations"))
			if err != nil {
				t.Fatal(err)
			}
			var beforeVersion int
			if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&beforeVersion); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.DownTo(context.Background(), 134); err == nil {
				t.Fatal("downgrade removed a frozen final-review contract or its evidence")
			}
			var version int
			if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied=1`).Scan(&version); err != nil || version != beforeVersion {
				t.Fatalf("refused downgrade changed migration version: version=%d err=%v", version, err)
			}
			after := finalReviewExecution(t, f)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refused downgrade changed requirement or final-review history")
			}
		})
	}
}

func TestRequirementFinalReviewTerminalEvidenceIsImmutable(t *testing.T) {
	f, _, before := pauseAtFinalReview(t, "SETTLED")
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// These forbidden writes must all be rejected by the database itself, not
	// merely by the service. They never create or edit a passing fixture.
	for _, statement := range []string{
		`DELETE FROM cleardev_requirement_final_reviews`,
		`UPDATE cleardev_requirement_final_reviews SET verdict='BLOCKED'`,
		`UPDATE cleardev_requirement_final_reviews SET status='REQUESTED'`,
		`UPDATE cleardev_requirement_final_reviews SET candidate_commit_sha='ffffffffffffffffffffffffffffffffffffffff'`,
		`UPDATE cleardev_requirement_final_reviews SET plan_id='other-plan'`,
		`UPDATE cleardev_requirement_final_reviews SET review_packet_json='{}'`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("accepted a forbidden terminal history rewrite: %s", statement)
		}
	}
	after := finalReviewExecution(t, f)
	if after.FinalReview == nil || !reflect.DeepEqual(before, *after.FinalReview) {
		t.Fatal("rejected writes changed final review history")
	}
	assertMailNotCompleted(t, f)
}
