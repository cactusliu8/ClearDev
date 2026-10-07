package sqlite

import (
	"strings"
	"testing"
)

func init() { shippedMigrations[137] = "0137_cleardev_mail_v2_bounded_parallel.sql" }

func TestMigration0137KeepsV1AndBoundsV2ParallelEligibility(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 137)

	for _, item := range []struct {
		kind string
		name string
		want []string
	}{
		{
			kind: "view", name: "cleardev_bounded_mail_runs",
			want: []string{
				"deliveryPolicy')='MAIL_INCREMENT_V1'",
				"mode='STANDARD' AND fixed_builder_count=1",
				"deliveryPolicy')='MAIL_INCREMENT_V2'",
				"mode='PARALLEL' AND fixed_builder_count=2",
				"attemptPolicy')='MAIL_ATTEMPTS_V1'",
			},
		},
		{
			kind: "trigger", name: "cleardev_review_check_request_valid",
			want: []string{"MAIL_INCREMENT_V1", "MAIL_INCREMENT_V2", "MAIL_ATTEMPTS_V1", "fixed_builder_count=2"},
		},
		{
			kind: "view", name: "cleardev_mail_reviewer_rework_sources",
			want: []string{"MAIL_INCREMENT_V1", "MAIL_INCREMENT_V2", "MAIL_ATTEMPTS_V1", "BETWEEN 1 AND 3"},
		},
	} {
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type=? AND name=?`, item.kind, item.name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		for _, want := range item.want {
			if !strings.Contains(ddl, want) {
				t.Fatalf("%s %s does not retain %q: %s", item.kind, item.name, want, ddl)
			}
		}
	}
}
