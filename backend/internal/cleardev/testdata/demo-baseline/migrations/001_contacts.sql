-- Initial schema of the healthy baseline, frozen before any incremental task.
CREATE TABLE IF NOT EXISTS contacts (
  id INTEGER PRIMARY KEY,
  normalized_email TEXT NOT NULL UNIQUE
);
