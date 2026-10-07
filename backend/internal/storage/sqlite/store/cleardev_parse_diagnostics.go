package store

import (
	"context"
	"database/sql"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// ReadClearDevAgentStepResultParse reads the first immutable parser conclusion.
// Replaying a result must not replace its historical diagnostic wording.
func (s *Store) ReadClearDevAgentStepResultParse(ctx context.Context, resultID string) (core.AgentStepResultParse, bool, error) {
	row, err := s.qr.GetClearDevAgentStepResultParse(ctx, resultID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.AgentStepResultParse{}, false, nil
	}
	if err != nil {
		return core.AgentStepResultParse{}, false, err
	}
	return core.AgentStepResultParse{ResultID: row.ResultID, Conclusion: core.AgentResultParseConclusion(row.Conclusion), ErrorSummary: row.ErrorSummary, ParsedAt: row.ParsedAt}, true, nil
}
