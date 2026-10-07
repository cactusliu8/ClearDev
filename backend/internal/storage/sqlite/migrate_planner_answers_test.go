package sqlite

import "testing"

func init() { shippedMigrations[182] = "0182_cleardev_planner_answers.sql" }

func TestMigration0182PlannerAnswersUpgradeDownReplay(t *testing.T) {
	db := openMigrationTestDB(t)
	upTo(t, db, 181)
	const schema = `SELECT group_concat(sql,';') FROM (SELECT sql FROM sqlite_schema WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' AND name<>'goose_db_version' ORDER BY name)`
	var before, after string
	if err := db.QueryRow(schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 182)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('cleardev_planner_answers','cleardev_planner_answers_immutable','cleardev_planner_answers_keep','cleardev_planner_answers_bound','cleardev_planner_answers_cdc')`).Scan(&count); err != nil || count != 5 {
		t.Fatal("Planner answer schema incomplete")
	}
	if _, err := db.Exec(`INSERT INTO cleardev_planner_answers VALUES('unbound','missing','missing',printf('%064d',0),'["a"]',printf('%064d',0),'next','2026-09-30T00:00:00Z')`); err == nil {
		t.Fatal("unbound answers admitted")
	}
	downTo(t, db, 181)
	if err := db.QueryRow(schema).Scan(&after); err != nil || after != before {
		t.Fatal("empty downgrade changed original schema")
	}
	upTo(t, db, 182)
}
