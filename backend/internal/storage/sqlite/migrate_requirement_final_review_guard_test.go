package sqlite

// 0136 preserves the shipped 0135 migration and only replaces its final-review
// update guard so a detected invalid reviewer binding can become a durable stop.
func init() { shippedMigrations[136] = "0136_cleardev_final_review_failure_guard.sql" }
