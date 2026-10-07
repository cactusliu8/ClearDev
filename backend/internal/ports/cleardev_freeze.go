package ports

import (
	"context"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// ClearDevMailFreezeRequest is assembled only from durable execution/session
// facts. No path, parent, permission, command or commit is taken from model JSON.
type ClearDevMailFreezeRequest struct {
	RunID            string
	RepoPath         string
	WorkspacePath    string
	Branch           string
	BaseSHA          string
	ParentSHA        string
	WritePaths       []string
	ForbiddenPaths   []string
	ProjectExecution *core.ProjectExecutionContract `json:"projectExecution,omitempty"`
	// Daemon-only proof from the exact granted effective alias for its original
	// logical step. It admits only that seal's retained index, never new staging.
	// This field is not part of any HTTP request or model result schema.
	BuilderHandoffSnapshot *ClearDevBuilderHandoffSnapshot `json:"builderHandoffSnapshot,omitempty"`
}

// ClearDevMailCandidateFreezer publishes a bounded, immutable snapshot without
// executing repository code. It does not mark any check or review as passed.
type ClearDevMailCandidateFreezer interface {
	FreezeMailCandidate(context.Context, ClearDevMailFreezeRequest) (ClearDevCandidateInspection, error)
}

// ClearDevMailBuilderBaseRequest binds an idle Builder to a trusted integrated
// descendant without detaching or rewriting its controlled branch.
type ClearDevMailBuilderBaseRequest struct {
	ProjectExecution *core.ProjectExecutionContract `json:"projectExecution,omitempty"`
	RepoPath         string
	WorkspacePath    string
	Branch           string
	ExpectedHeadSHA  string
	BaseSHA          string
}

// ClearDevMailBuilderBasePreparer preserves the branch required by trusted freeze.
type ClearDevMailBuilderBasePreparer interface {
	PrepareMailBuilderBase(context.Context, ClearDevMailBuilderBaseRequest) error
}
