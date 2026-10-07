package cleardev

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ErrControlledExecutionChoiceChanged is returned after recording a failed
// inspection whose selected tool/model changed before it could be committed.
var ErrControlledExecutionChoiceChanged = errors.New("controlled execution choice changed during preflight")

// Controlled preflight outcomes. FAILED never permanently ends a role binding.
const (
	ControlledPreflightPassed = "PASSED"
	ControlledPreflightFailed = "FAILED"
)

// ControlledPreflight is one append-only inspection recorded before a ClearDev
// role session exists. CatalogJSON is the normalized model-id array.
type ControlledPreflight struct {
	ID                       string
	DevelopmentRequirementID string
	RoleBindingID            string
	AOProjectID              string
	RequestedModel           string
	ResolvedModel            string
	Provider                 string
	CatalogJSON              string
	CatalogSHA256            string
	Outcome                  string
	ReasonCode               ReasonCode
	Retryable                bool
	RetryAt                  *time.Time
	ProviderErrorCode        string
	ErrorSummary             string
	Evidence                 *domain.ControlledPreflightEvidence
	CheckedAt                time.Time
}

// ControlledPreflightView is the public read model for the latest check.
type ControlledPreflightView struct {
	ID                string                              `json:"id"`
	RoleBindingID     string                              `json:"roleBindingId"`
	AOProjectID       string                              `json:"aoProjectId"`
	RequestedModel    string                              `json:"requestedModel"`
	ResolvedModel     string                              `json:"resolvedModel"`
	Provider          string                              `json:"provider"`
	CatalogSHA256     string                              `json:"catalogSha256"`
	CatalogModelIDs   []string                            `json:"catalogModelIds"`
	Outcome           string                              `json:"outcome" enum:"PASSED,FAILED"`
	ReasonCode        ReasonCode                          `json:"reasonCode,omitempty"`
	Retryable         bool                                `json:"retryable"`
	RetryAt           *time.Time                          `json:"retryAt,omitempty"`
	ProviderErrorCode string                              `json:"providerErrorCode,omitempty"`
	ErrorSummary      string                              `json:"errorSummary,omitempty"`
	CheckedAt         time.Time                           `json:"checkedAt"`
	Evidence          *domain.ControlledPreflightEvidence `json:"evidence,omitempty"`
}

// PublicView hides the durable catalog JSON while keeping the digest and ids.
func (record ControlledPreflight) PublicView() ControlledPreflightView {
	return ControlledPreflightView{
		ID:                record.ID,
		RoleBindingID:     record.RoleBindingID,
		AOProjectID:       record.AOProjectID,
		RequestedModel:    record.RequestedModel,
		ResolvedModel:     record.ResolvedModel,
		Provider:          record.Provider,
		CatalogSHA256:     record.CatalogSHA256,
		CatalogModelIDs:   catalogModelIDs(record.CatalogJSON),
		Outcome:           record.Outcome,
		ReasonCode:        record.ReasonCode,
		Retryable:         record.Retryable,
		RetryAt:           record.RetryAt,
		ProviderErrorCode: record.ProviderErrorCode,
		ErrorSummary:      record.ErrorSummary,
		Evidence:          record.Evidence,
		CheckedAt:         record.CheckedAt,
	}
}

func catalogModelIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}
