package cleardev

// RecoveryRetryPlannerCoordination spends the unused second attempt of the
// original coordination request. It is not another business round or grant.
const RecoveryRetryPlannerCoordination = "RETRY_PLANNER_COORDINATION"

// PlannerCoordinationRecoveryBinding pins the source and native identity. It
// is constructed by the backend, never supplied as authority by an Agent.
type PlannerCoordinationRecoveryBinding struct {
	NativeTurnID           string `json:"nativeTurnId"`
	NativeTurnState        string `json:"nativeTurnState"`
	TerminalEventID        int64  `json:"terminalEventId"`
	RequirementID          string `json:"requirementId"`
	ExecutionRunID         string `json:"executionRunId"`
	EventID                string `json:"eventId"`
	LogicalStepID          string `json:"logicalStepId"`
	FirstAttemptID         string `json:"firstAttemptId"`
	FailureEventID         string `json:"failureEventId"`
	RoleBindingID          string `json:"roleBindingId"`
	AOSessionID            string `json:"aoSessionId"`
	ProviderConversationID string `json:"providerConversationId"`
	WorkspacePath          string `json:"workspacePath"`
	SessionCreationKey     string `json:"sessionCreationKey"`
	CreationFingerprint    string `json:"creationFingerprint"`
	Harness                string `json:"harness"`
	Model                  string `json:"model"`
	PromptSHA256           string `json:"promptSha256"`
	ClientMessageID        string `json:"clientMessageId"`
	ContextSHA256          string `json:"contextSha256"`
}

// PlannerCoordinationRecoveryTarget makes a stale public action unexecutable.
func PlannerCoordinationRecoveryTarget(b PlannerCoordinationRecoveryBinding) (string, error) {
	raw, err := CanonicalJSONBytes(b)
	if err != nil {
		return "", err
	}
	return b.LogicalStepID + ":retry:" + sha256Hex(raw), nil
}

// PlannerCoordinationRecoveryState carries internal admission evidence. History
// is exposed through the existing recovery view; the binding stays private.
type PlannerCoordinationRecoveryState struct {
	Binding  PlannerCoordinationRecoveryBinding
	Option   WorkflowRecoveryOption
	History  []WorkflowRecovery
	Messages []PlanningStepRecoveryMessage
}

// PlannerCoordinationRecovery exposes the retry beside its original event.
type PlannerCoordinationRecovery struct {
	EventID  string           `json:"eventId"`
	Recovery WorkflowRecovery `json:"recovery"`
}

// PlannerRuntimeEffectiveDecision does not remove the original STOP from
// history. Registration masks it only for this event; until a subsequent
// decision and its contracts exist this event remains a pending barrier.
func PlannerRuntimeEffectiveDecision(h *PlannerRuntimeSnapshot, eventID string) (PlannerCoordinationDecision, bool) {
	if h == nil {
		return PlannerCoordinationDecision{}, false
	}
	for _, grant := range h.ExtraCoordinationGrants {
		if grant.EventID != eventID {
			continue
		}
		for _, d := range h.ExtraCoordinationDecisions {
			if d.EventID == eventID {
				return d, true
			}
		}
		return PlannerCoordinationDecision{}, false
	}
	for _, r := range h.Recoveries {
		if r.EventID != eventID {
			continue
		}
		for _, d := range h.RecoveryDecisions {
			if d.EventID == eventID {
				return d, true
			}
		}
		return PlannerCoordinationDecision{}, false
	}
	for _, d := range h.Decisions {
		if d.EventID == eventID {
			return d, true
		}
	}
	return PlannerCoordinationDecision{}, false
}
