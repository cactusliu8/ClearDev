package sqlite

import (
	"strings"
	"testing"
)

func init() {
	shippedMigrations[178] = "0178_cleardev_delivery_view_escaping.sql"
	shippedMigrations[179] = "0179_cleardev_check_guard_escaping.sql"
}

// The delivery view proves that the run package and the admission mirror carry
// the same contract by comparing their stored text. Go's default encoder spells
// "<", ">" and "&" as \u003c, \u003e and \u0026 while the canonical encoder
// keeps the characters, so the view must accept both spellings of one object
// and still reject a different contract.
func TestProjectDeliveryViewAcceptsBothEscapingForms(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 177)
	var before string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='view' AND name='cleardev_completed_project_deliveries'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "json_extract(r.execution_package_json,'$.projectExecution') IS admission.contract_json") {
		t.Fatal("0154 delivery view lost its exact-text comparison")
	}
	upTo(t, db, 178)
	var after string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='view' AND name='cleardev_completed_project_deliveries'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after == before || !strings.Contains(after, `'\u003c', '<'`) {
		t.Fatalf("delivery view does not normalise HTML escaping:\n%s", after)
	}
	// Every other predicate must survive the rewrite verbatim.
	for _, predicate := range []string{
		"review.rowid=(SELECT max(latest.rowid) FROM cleardev_requirement_final_reviews latest WHERE latest.execution_run_id=r.id)",
		"json_extract(admission.contract_json,'$.policy') IS 'PROJECT_EXECUTION_V1'",
		"review.base_commit_sha IS json_extract(admission.contract_json,'$.baseCommitSha')",
		"AND NOT EXISTS(SELECT 1 FROM json_each(review.check_run_ids_json) expected",
	} {
		if !strings.Contains(after, predicate) {
			t.Fatalf("delivery view lost predicate %q", predicate)
		}
	}
	// The normalisation compares the same object in both spellings and refuses
	// different content.
	var same, different int
	if err := db.QueryRow(`SELECT
	  replace(replace(replace('{"title":"total: \u003cn\u003e done"}','\u003c','<'),'\u003e','>'),'\u0026','&')
	  IS replace(replace(replace('{"title":"total: <n> done"}','\u003c','<'),'\u003e','>'),'\u0026','&')`).Scan(&same); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT
	  replace(replace(replace('{"title":"total: \u003cn\u003e"}','\u003c','<'),'\u003e','>'),'\u0026','&')
	  IS replace(replace(replace('{"title":"total: <m>"}','\u003c','<'),'\u003e','>'),'\u0026','&')`).Scan(&different); err != nil {
		t.Fatal(err)
	}
	if same != 1 || different != 0 {
		t.Fatalf("escaping normalisation is wrong: same=%d different=%d", same, different)
	}
	// Downgrade restores the exact 0154 predicate and keeps the schema valid.
	if err := migrateDown(177, db); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='view' AND name='cleardev_completed_project_deliveries'`).Scan(&restored); err != nil || restored != before {
		t.Fatalf("0154 delivery view was not restored byte for byte: %v", err)
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("migration introduced foreign-key violations: %d %v", violations, err)
	}
}

// The same escaping mismatch reaches the review-check and replacement guards:
// they compare the stored request/run text with the admission mirror. Both
// spellings of one contract must pass, every other predicate must survive, and
// the downgrade must restore the previous definitions byte for byte.
func TestProjectCheckGuardsAcceptBothEscapingForms(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 178)
	guarded := []string{
		"cleardev_review_check_request_valid",
		"cleardev_review_check_result_valid",
		"cleardev_project_replacement_authorities",
		"cleardev_project_replacement_result_exact",
	}
	before := map[string]string{}
	for _, name := range guarded {
		var sql string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		before[name] = sql
		if strings.Count(sql, "IS admission.contract_json") == 0 {
			t.Fatalf("%s no longer compares the admission mirror", name)
		}
	}
	upTo(t, db, 179)
	for _, name := range guarded {
		var sql string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&sql); err != nil {
			t.Fatal(err)
		}
		if sql == before[name] {
			t.Fatalf("%s was not rewritten by 0179", name)
		}
		if strings.Contains(sql, ") IS admission.contract_json") {
			t.Fatalf("%s still compares the raw text", name)
		}
		if strings.Count(sql, "replace(replace(replace(admission.contract_json") == 0 {
			t.Fatalf("%s lost the escaping normalisation", name)
		}
		// Rolling the normalisation back must reproduce the previous definition,
		// which proves only the comparison changed.
		scrubbed := normalizeBack(t, sql)
		if scrubbed != before[name] {
			t.Fatalf("%s changed more than the admission comparison", name)
		}
	}
	// This fixture has no controlled execution, so the downgrade runs and must
	// restore the previous definitions byte for byte. The refusal path (an
	// execution history keeps version 179) is asserted by the existing store
	// downgrade tests.
	if err := migrateDown(178, db); err != nil {
		t.Fatal(err)
	}
	for _, name := range guarded {
		var restored string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&restored); err != nil {
			t.Fatal(err)
		}
		if restored != before[name] {
			t.Fatalf("%s was not restored byte for byte by the downgrade", name)
		}
	}
	var violations int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("migration introduced foreign-key violations: %d %v", violations, err)
	}
}

// normalizeBack removes the escaping normalisation wrappers from a guard body so
// the result can be compared with the pre-0179 definition.
func normalizeBack(t *testing.T, sql string) string {
	t.Helper()
	out := sql
	for {
		start := strings.Index(out, "replace(replace(replace(")
		if start < 0 {
			return out
		}
		end := strings.Index(out[start:], ", '\\u0026', '&')")
		if end < 0 {
			t.Fatalf("unterminated normalisation in %q", out[start:start+40])
		}
		tail := out[start+end+len(", '\\u0026', '&')"):]
		inner := out[start : start+end]
		// Strip the three nested wrappers down to the compared expression.
		inner = strings.TrimPrefix(inner, "replace(replace(replace(")
		inner = strings.TrimSuffix(inner, ", '\\u003c', '<'), '\\u003e', '>')")
		out = out[:start] + inner + tail
	}
}
