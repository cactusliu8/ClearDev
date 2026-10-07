package ports

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// ClearDevProjectExecutionPreparer explicitly advertises support for the
// versioned project contract. A legacy mail runner must not silently accept a
// project execution request and ignore its checks or runtime configuration.
// Preparation checks infrastructure only; an empty source need not already
// contain the application, package manifest, migrations or tests to be built.
type ClearDevProjectExecutionPreparer interface {
	PrepareProjectExecution(context.Context, core.ProjectExecutionContract) error
}

// ClearDevProjectResultSource is a disposable backend-materialized runtime
// copy, not the Builder worktree or an arbitrary path supplied by the UI. The
// existing preview process owner releases it only after its process exits.
// Durable application data is never stored in this disposable source tree.
type ClearDevProjectResultSource struct {
	WorkspacePath  string
	CandidateSHA   string
	ContractSHA256 string
	Environment    ClearDevCheckEnvironment
	Release        func(context.Context) error
}

// ClearDevProjectResultPreparer materializes only an exact delivered project candidate.
type ClearDevProjectResultPreparer interface {
	PrepareProjectResult(context.Context, string, string, core.ProjectExecutionContract) (ClearDevProjectResultSource, error)
}

// ClearDevProjectResultPrepare prepares a disposable runtime copy for the existing preview owner.
type ClearDevProjectResultPrepare func(context.Context) (ClearDevProjectResultSource, error)
