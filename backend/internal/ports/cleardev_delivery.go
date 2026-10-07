package ports

import (
	"context"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// ErrClearDevMailScopeViolation is a project change outside the fixed contract.
var ErrClearDevMailScopeViolation = errors.New("MAIL_SCOPE_OR_TEST_PROTECTION_FAILED")

// ClearDevDeliveryRequest contains immutable bindings owned by the control plane,
// never policy or argv supplied by an Agent.
type ClearDevDeliveryRequest struct {
	RunID          string
	DeliveryPolicy string
	WorkspacePath  string
	BaseSHA        string
	CandidateSHA   string
}

// ClearDevMailProjectIdentifier discovers the mail contract from trusted Git.
type ClearDevMailProjectIdentifier interface {
	IdentifyMailProject(context.Context, string) (ClearDevBaselineResult, error)
}

// ClearDevDeliveryBranchInspector checks a completed source branch without
// creating or moving any ref. Result preview uses this to avoid a stale baseline.
type ClearDevDeliveryBranchInspector interface {
	InspectDeliveryBranch(context.Context, string, string, string) error
}

// ClearDevMailDeliveryChecker produces scope and executed delivery evidence.
type ClearDevMailDeliveryChecker interface {
	CheckMailCandidateScope(context.Context, ClearDevDeliveryRequest) (core.MailScopeProof, error)
	RunMailDeliveryCheck(context.Context, ClearDevDeliveryRequest) (ClearDevCheckResult, error)
}
