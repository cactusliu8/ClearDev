package cleardev

// ProjectPlanningState is derived from the immutable Stage link and latest
// discussion, plus a read-only observation of the selected repository version.
// It is not a second workflow. Availability means an explicit admission may be
// requested, never that a saved Plan V3 or an old plan has execution authority.
type ProjectPlanningState struct {
	ProductID          string `json:"productId"`
	StageID            string `json:"stageId"`
	DiscussionID       string `json:"discussionId"`
	LatestDiscussionID string `json:"latestDiscussionId"`
	BaseCommitSHA      string `json:"baseCommitSha"`
	Current            bool   `json:"current"`
	SourceCurrent      bool   `json:"sourceCurrent"`
	PlanReady          bool   `json:"planReady"`
	ExecutionAvailable bool   `json:"executionAvailable"`
	ExecutionAdmitted  bool   `json:"executionAdmitted"`
	ExecutionRunID     string `json:"executionRunId,omitempty"`
	// Execution inputs are derived from the immutable Stage, including
	// resolved defaults; they are not a new UI-owned execution state.
	ExecutionBasis   *ProjectExecutionBasis `json:"executionBasis,omitempty"`
	ExecutionRuntime *ProjectRuntime        `json:"executionRuntime,omitempty"`
	ReasonCode       ReasonCode             `json:"reasonCode,omitempty"`
}

func applyProjectPlanningProgress(summary *TrustedProgressSummary, facts TrustedProgressFacts) {
	if facts.ProjectPlanning == nil {
		return
	}
	state := *facts.ProjectPlanning
	if facts.ComplexPlanning != nil {
		phase, _ := DeriveComplexPlanningPhase(*facts.ComplexPlanning, facts.Snapshot.RequirementVersions)
		state.PlanReady = phase == ComplexPlanningProjectPlanned
	}
	if facts.ComplexExecution != nil {
		contract, admitted, err := ProjectContractFromRun(facts.ComplexExecution.Run)
		if err == nil && admitted && contract.StageID == state.StageID && contract.ProductID == state.ProductID {
			state.ExecutionAdmitted, state.ExecutionRunID, state.ExecutionAvailable = true, contract.ExecutionRunID, false
			// Completed immutable deliveries remain history even when a newer
			// discussion supersedes this Stage. Opening still checks its exact
			// integrated source; it never reuses the old plan for new execution.
			if facts.ComplexExecution.Run.CompletedAt != nil {
				state.ReasonCode = ReasonNone
			}
		}
	}
	if state.ReasonCode == ReasonNone && state.PlanReady && !state.ExecutionAdmitted {
		state.ReasonCode = ReasonProjectPlanningOnly
		if state.ExecutionAvailable {
			state.ReasonCode = ReasonProjectAdmission
		}
	}
	summary.ProjectPlanning = &state
	if summary.Phase == TrustedPhaseCancelled || state.ReasonCode == ReasonNone {
		return
	}
	summary.Phase, summary.Attention, summary.ReasonCode = TrustedPhaseBlocked, OverallAttentionBlocked, state.ReasonCode
	summary.CurrentWork, summary.ControlledWork, summary.PendingDecisions = nil, nil, nil
	summary.PendingRequirementVersionID = ""
	summary.Blockers = []TrustedIssue{{Kind: "PROJECT_PLANNING_GATE", SubjectType: "PRODUCT_STAGE", SubjectID: state.StageID, ReasonCode: state.ReasonCode}}
	summary.NextOwner = TrustedOwner{Role: "HUMAN", Action: "REVISE_PROJECT"}
	switch state.ReasonCode {
	case ReasonProjectPlanningOnly:
		summary.NextOwner = TrustedOwner{Role: "SYSTEM", Action: "WAIT_FOR_EXECUTION_CAPABILITY"}
	case ReasonProjectAdmission:
		summary.NextOwner = TrustedOwner{Role: "HUMAN", Action: "ADMIT_PROJECT_EXECUTION"}
	}
	if !state.Current {
		summary.CanRequestExplanation = false
		summary.ExplanationUnavailableReason = string(ReasonProductPlanSuperseded)
	}
}
