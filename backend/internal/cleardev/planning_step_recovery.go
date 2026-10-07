package cleardev

// RecoveryRetryPlanningStep consumes the unused second original-request attempt
// of a stopped discussion or compilation. It is not a new product discussion,
// an additional parse correction, or authority to confirm a specification.
const RecoveryRetryPlanningStep = "RETRY_PLANNING_STEP"

// PlanningStepRecoveryBinding records the immutable source of a bounded retry.
// It is backend-generated; ordinary clients cannot supply these bindings.
type PlanningStepRecoveryBinding struct {
	RequirementID          string `json:"requirementId"`
	ProductID              string `json:"productId"`
	DiscussionID           string `json:"discussionId"`
	StageID                string `json:"stageId"`
	StageDefinitionSHA256  string `json:"stageDefinitionSha256"`
	SelectionSHA256        string `json:"selectionSha256"`
	TargetVersionID        string `json:"targetVersionId"`
	LogicalStepID          string `json:"logicalStepId"`
	StepRequestID          string `json:"stepRequestId"`
	FirstAttemptID         string `json:"firstAttemptId"`
	FailureEventID         string `json:"failureEventId"`
	RoleBindingID          string `json:"roleBindingId"`
	AOSessionID            string `json:"aoSessionId"`
	ProviderConversationID string `json:"providerConversationId"`
	WorkspacePath          string `json:"workspacePath"`
	SessionCreationKey     string `json:"sessionCreationKey"`
	Harness                string `json:"harness"`
	Model                  string `json:"model"`
	PromptSHA256           string `json:"promptSha256"`
	ClientMessageID        string `json:"clientMessageId"`
}

// PlanningStepRecoveryTarget makes a displayed action stale when its source,
// failure or original session identity changes, without adding client-selected
// binding fields to the shared HTTP request.
func PlanningStepRecoveryTarget(binding PlanningStepRecoveryBinding) (string, error) {
	raw, err := CanonicalJSONBytes(binding)
	if err != nil {
		return "", err
	}
	return binding.LogicalStepID + ":retry:" + sha256Hex(raw), nil
}

// PlanningStepRecoveryState is internal evidence for the shared recovery view.
// An empty unavailable reason grants only eligibility for the explicit request;
// the transaction and actual sender both recheck all bindings.
type PlanningStepRecoveryState struct {
	Binding   PlanningStepRecoveryBinding
	Option    WorkflowRecoveryOption
	Selection *ProductSelection
	History   []WorkflowRecovery
	Messages  []PlanningStepRecoveryMessage
}

// PlanningStepRecoveryMessage ties the original native conversation snapshot
// back to the confirmed per-message delivery facts.
type PlanningStepRecoveryMessage struct {
	ClientMessageID string
	PromptSHA256    string
	TurnID          string
}
