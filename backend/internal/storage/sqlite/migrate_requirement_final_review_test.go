package sqlite

// Register only the new migration; historical ledger entries and migrations
// remain unchanged and are still checked by TestMigrationVersionLedger.
func init() { shippedMigrations[135] = "0135_cleardev_requirement_final_review.sql" }
