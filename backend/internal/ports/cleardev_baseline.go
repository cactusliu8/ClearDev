package ports

import "context"

// ClearDevBaselineRequest is owned by the service, never by an Agent or HTTP
// caller. RunID is stable across retry/restart and binds the inspected SHA.
type ClearDevBaselineRequest struct {
	RunID         string
	WorkspacePath string
	// Local base branch selected through the existing project configuration.
	// Empty/auto retains the historical HEAD path; no Agent-supplied revision.
	BaseBranch string
	// Trusted planning-time identity inspection only; never supplied by HTTP.
	IdentifyOnly bool
}

// ClearDevBaselineResult is evidence from the trusted checker. Required=false
// means this is not the fixed mail starting product; it is not a health PASS.
// Failures retain their classified reason and any completed check evidence.
type ClearDevBaselineResult struct {
	Required     bool
	CandidateSHA string
	ReasonCode   string
	TestRunID    string
	HealthRunID  string
	Tests        ClearDevCheckResult
	Health       ClearDevCheckResult
}

// ClearDevMailBaselineSelector identifies the same explicitly selected local
// branch used for new role worktrees, without running application code.
type ClearDevMailBaselineSelector interface {
	IdentifyMailProjectAtBranch(context.Context, string, string) (ClearDevBaselineResult, error)
}

// ClearDevDevelopmentBaselineChecker is required by the Builder creation path.
// Implementations must inspect immutable Git facts, run checks in isolation,
// and recheck source identity before returning a successful result.
type ClearDevDevelopmentBaselineChecker interface {
	CheckDevelopmentBaseline(context.Context, ClearDevBaselineRequest) (ClearDevBaselineResult, error)
}
