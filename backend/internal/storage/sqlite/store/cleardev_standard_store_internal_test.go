package store

import (
	"database/sql"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func TestStandardCompletionRunsRejectsAuxiliaryScopeCheck(t *testing.T) {
	pass := func(id, kind, name string) gen.CleardevCandidateCheckRun {
		return gen.CleardevCandidateCheckRun{
			ID:                id,
			CandidateCommitID: sql.NullString{String: "candidate-1", Valid: true},
			CheckKind:         kind,
			CheckName:         name,
			Status:            string(core.CandidateCheckRunStatusSettled),
			Result:            sql.NullString{String: string(core.EvidenceResultPass), Valid: true},
		}
	}
	runs := []gen.CleardevCandidateCheckRun{
		pass("review-worktree", string(core.CandidateCheckScope), "review-worktree"),
		pass("integration", string(core.CandidateCheckIntegration), "integration"),
	}
	if _, complete := standardCompletionRuns(runs, "candidate-1", nil); complete {
		t.Fatal("auxiliary review-worktree check satisfied the mandatory scope gate")
	}

	runs = append(runs, pass("scope", string(core.CandidateCheckScope), "scope"))
	if integrationRunID, complete := standardCompletionRuns(runs, "candidate-1", nil); !complete || integrationRunID != "integration" {
		t.Fatalf("real scope and integration passes = (%q, %t), want integration/true", integrationRunID, complete)
	}
}
