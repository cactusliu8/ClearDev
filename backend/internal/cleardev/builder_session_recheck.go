package cleardev

import (
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// BuilderSessionRecheckContext is program-generated intent, never evidence that
// a user repaired the environment. It keeps the historical non-empty storage
// contract without requiring a person to retype a known technical problem.
const BuilderSessionRecheckContext = "System request: check the original Builder and continue its exact unsent task if safe; no environment repair or task completion is claimed."

// NormalizeWorkflowRecoverySupplement supplies neutral intent only for the
// original Builder's explicit technical recheck; other empty actions stay empty.
func NormalizeWorkflowRecoverySupplement(action, supplement string) string {
	supplement = strings.TrimSpace(supplement)
	if action == RecoveryRetryBuilderSession && supplement == "" {
		return BuilderSessionRecheckContext
	}
	return supplement
}

// BuilderSessionCheck is an immutable, bounded observation for one explicit
// recheck. STARTED precedes restoration; FINISHED records its result;
// BEFORE_SEND can record a later failed validation, never a second restoration.
// READY authorizes nothing: the original sender repeats its live admission.
type BuilderSessionCheck struct {
	RecoveryID    string    `json:"recoveryId"`
	Checkpoint    string    `json:"checkpoint" enum:"STARTED,FINISHED,BEFORE_SEND"`
	Stage         string    `json:"stage"`
	Outcome       string    `json:"outcome" enum:"PENDING,READY,FAILED"`
	ReasonCode    string    `json:"reasonCode,omitempty"`
	PreflightID   string    `json:"preflightId,omitempty"`
	CheckedAt     time.Time `json:"checkedAt"`
	BindingSHA256 string    `json:"-"`
}

// BuilderSessionBindingDigest covers identity, not transient activity. Native
// conversation identifiers stay out of new public diagnostics.
func BuilderSessionBindingDigest(record domain.SessionRecord) string {
	binding := struct {
		ID, Project, Creation, Harness, Mode, Permission, Native, Workspace, Model string
	}{string(record.ID), string(record.ProjectID), record.CreationIdempotencyKey,
		string(record.Harness), string(record.Mode), string(record.PermissionMode),
		record.Metadata.ProviderConversationID, record.Metadata.WorkspacePath, record.Metadata.Model}
	raw, err := CanonicalJSONBytes(binding)
	if err != nil {
		return ""
	}
	return sha256Hex(raw)
}
