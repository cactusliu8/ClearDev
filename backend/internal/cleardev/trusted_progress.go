package cleardev

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"time"
)

// TrustedPhase is the single user-facing stage derived from current facts.
type TrustedPhase string

// User-facing trusted-progress stages derived from current facts.
const (
	TrustedPhaseCancelled             TrustedPhase = "CANCELLED"
	TrustedPhaseAwaitingConfirmation  TrustedPhase = "AWAITING_CONFIRMATION"
	TrustedPhaseDefiningRequirement   TrustedPhase = "DEFINING_REQUIREMENT"
	TrustedPhaseAwaitingClarification TrustedPhase = "AWAITING_CLARIFICATION"
	TrustedPhasePlanning              TrustedPhase = "PLANNING"
	TrustedPhaseCoordinating          TrustedPhase = "COORDINATING"
	TrustedPhaseAwaitingPlanReview    TrustedPhase = "AWAITING_PLAN_REVIEW"
	TrustedPhaseAwaitingSteward       TrustedPhase = "AWAITING_STEWARD"
	TrustedPhaseDeveloping            TrustedPhase = "DEVELOPING"
	TrustedPhaseVerifying             TrustedPhase = "VERIFYING"
	TrustedPhaseReworking             TrustedPhase = "REWORKING"
	TrustedPhaseIntegrating           TrustedPhase = "INTEGRATING"
	TrustedPhaseAwaitingScope         TrustedPhase = "AWAITING_SCOPE"
	TrustedPhaseAwaitingSpecialist    TrustedPhase = "AWAITING_SPECIALIST"
	TrustedPhaseRecovering            TrustedPhase = "RECOVERING"
	TrustedPhaseCompleted             TrustedPhase = "COMPLETED"
	TrustedPhaseNeedsHuman            TrustedPhase = "NEEDS_HUMAN"
	TrustedPhaseBlocked               TrustedPhase = "BLOCKED"
	TrustedPhaseUnknown               TrustedPhase = "UNKNOWN"
)

// Trusted owner roles and next actions are machine values. UNKNOWN is required
// when current facts are not enough to name a person or program step.
const (
	TrustedOwnerNone         = "NONE"
	TrustedOwnerUnknown      = "UNKNOWN"
	TrustedOwnerHuman        = "HUMAN"
	TrustedOwnerSteward      = "STEWARD"
	TrustedOwnerPlanner      = "PLANNER"
	TrustedOwnerBuilder      = "BUILDER"
	TrustedOwnerReviewer     = "REVIEWER"
	TrustedOwnerSpecialist   = "SPECIALIST"
	TrustedOwnerRecovery     = "RECOVERY"
	TrustedOwnerControlPlane = "CONTROL_PLANE"

	TrustedActionNone                  = "NONE"
	TrustedActionUnknown               = "UNKNOWN"
	TrustedActionConfirmRequirement    = "CONFIRM_REQUIREMENT"
	TrustedActionAnswerClarification   = "ANSWER_CLARIFICATION"
	TrustedActionCompileRequirement    = "COMPILE_REQUIREMENT"
	TrustedActionProducePlan           = "PRODUCE_PLAN"
	TrustedActionCoordinateEngineering = "COORDINATE_ENGINEERING"
	TrustedActionReviewPlan            = "REVIEW_PLAN"
	TrustedActionApproveDispatch       = "APPROVE_DISPATCH"
	TrustedActionRequestDispatch       = "REQUEST_DISPATCH"
	TrustedActionImplementTask         = "IMPLEMENT_TASK"
	TrustedActionStartTask             = "START_TASK"
	TrustedActionReviewCandidate       = "REVIEW_CANDIDATE"
	TrustedActionReworkTask            = "REWORK_TASK"
	TrustedActionIntegrate             = "RUN_INTEGRATION"
	TrustedActionDecideScope           = "DECIDE_SCOPE"
	TrustedActionCompleteSpecialist    = "COMPLETE_SPECIALIST"
	TrustedActionCompleteRecovery      = "COMPLETE_RECOVERY"
	TrustedActionDecide                = "DECIDE"
	TrustedActionUnblock               = "UNBLOCK"
)

// Sort ranks keep the project progress list stable: needs human, blocked or
// rework, in progress, then completed or cancelled.
const (
	TrustedSortNeedsHuman           = 0
	TrustedSortBlockedOrRework      = 1
	TrustedSortInProgress           = 2
	TrustedSortCompletedOrCancelled = 3

	trustedRecentFactLimit = 8
)

// TrustedProgressFacts is the complete current-fact input for one requirement.
// Explanation text is attached after derivation and is never hashed.
type TrustedProgressFacts struct {
	Controlled                []ControlledProgressFacts
	Snapshot                  RequirementSnapshot
	StandardFlow              *StandardFlowSnapshot
	ComplexPlanning           *ComplexPlanningSnapshot
	ProjectPlanning           *ProjectPlanningState
	DirectionChange           *DirectionChangeSnapshot
	ComplexExecution          *ComplexExecutionSnapshot
	QuickExecution            *ComplexQuickSnapshot
	PendingDecisions          []HumanDecisionRequest
	HasStewardSession         bool
	StewardSessionID          string
	LatestControlledPreflight *ControlledPreflightView
	Now                       time.Time
}

// TrustedProgressSummary is the answer-first read model. It is never stored as
// an independent lifecycle. Same current facts always produce the same hash.
type TrustedProgressSummary struct {
	ControlledWork               []ControlledWork                `json:"controlledWork"`
	DevelopmentRequirementID     string                          `json:"developmentRequirementId"`
	AOProjectID                  string                          `json:"aoProjectId"`
	Name                         string                          `json:"name"`
	Phase                        TrustedPhase                    `json:"phase" enum:"CANCELLED,AWAITING_CONFIRMATION,DEFINING_REQUIREMENT,AWAITING_CLARIFICATION,PLANNING,COORDINATING,AWAITING_PLAN_REVIEW,AWAITING_STEWARD,DEVELOPING,VERIFYING,REWORKING,INTEGRATING,AWAITING_SCOPE,AWAITING_SPECIALIST,RECOVERING,COMPLETED,NEEDS_HUMAN,BLOCKED,UNKNOWN"`
	Attention                    OverallAttention                `json:"attention" enum:"NONE,NEEDS_HUMAN,BLOCKED,REWORK,INTEGRATION_FAILED"`
	ReasonCode                   ReasonCode                      `json:"reasonCode,omitempty"`
	CurrentRequirementVersionID  string                          `json:"currentRequirementVersionId,omitempty"`
	PendingRequirementVersionID  string                          `json:"pendingRequirementVersionId,omitempty"`
	TaskSetVersion               *int64                          `json:"taskSetVersion,omitempty"`
	ExecutionMode                string                          `json:"executionMode,omitempty"`
	TaskCounts                   TaskCounts                      `json:"taskCounts"`
	Tasks                        []TrustedTaskItem               `json:"tasks"`
	CurrentWork                  []TrustedIssue                  `json:"currentWork"`
	Blockers                     []TrustedIssue                  `json:"blockers"`
	PendingDecisions             []TrustedIssue                  `json:"pendingDecisions"`
	NextOwner                    TrustedOwner                    `json:"nextOwner"`
	MissingEvidence              []TrustedMissingEvidence        `json:"missingEvidence"`
	CurrentCandidates            []TrustedCandidate              `json:"currentCandidates"`
	RecentFacts                  []TrustedRecentFact             `json:"recentFacts"`
	PlannerCoordination          []TrustedPlannerCoordination    `json:"plannerCoordination,omitempty"`
	ProjectPlanning              *ProjectPlanningState           `json:"projectPlanning,omitempty"`
	LatestFactSequence           int64                           `json:"latestFactSequence"`
	FactSummarySHA256            string                          `json:"factSummarySha256"`
	SortRank                     int                             `json:"sortRank"`
	CanRequestExplanation        bool                            `json:"canRequestExplanation"`
	ExplanationUnavailableReason string                          `json:"explanationUnavailableReason,omitempty"`
	Explanation                  *TrustedProgressExplanationView `json:"explanation,omitempty"`
	CitableFacts                 []TrustedCitableFact            `json:"-"`
}

// TrustedTaskItem is one development task in the current or historical set.
type TrustedTaskItem struct {
	DevelopmentTaskID    string                `json:"developmentTaskId"`
	Title                string                `json:"title"`
	Status               DevelopmentTaskStatus `json:"status"`
	RequirementVersionID string                `json:"requirementVersionId"`
	Current              bool                  `json:"current"`
}

// TrustedIssue is a current-work, blocker, or pending-decision item bound to a fact.
type TrustedIssue struct {
	Kind        string     `json:"kind"`
	SubjectType string     `json:"subjectType,omitempty"`
	SubjectID   string     `json:"subjectId,omitempty"`
	ReasonCode  ReasonCode `json:"reasonCode,omitempty"`
}

// TrustedOwner names who acts next and what that action is.
type TrustedOwner struct {
	Role   string `json:"role"`
	Action string `json:"action"`
}

// TrustedMissingEvidence is one still-required proof for the current result.
type TrustedMissingEvidence struct {
	Kind                   string `json:"kind"`
	DevelopmentTaskID      string `json:"developmentTaskId,omitempty"`
	IntegrationCandidateID string `json:"integrationCandidateId,omitempty"`
}

// TrustedCandidate is the current task or integration candidate, if any.
type TrustedCandidate struct {
	Kind              string `json:"kind"`
	ID                string `json:"id"`
	DevelopmentTaskID string `json:"developmentTaskId,omitempty"`
	CommitSHA         string `json:"commitSha,omitempty"`
	Sequence          int64  `json:"sequence,omitempty"`
	Current           bool   `json:"current"`
}

// TrustedRecentFact is one recent accepted event for display.
type TrustedRecentFact struct {
	Sequence    int64  `json:"sequence"`
	Action      Action `json:"action"`
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
}

// TrustedCitableFact is an identifier the Project Steward may quote.
type TrustedCitableFact struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// TrustedProgressExplanationView is a stored explanation attached at read time.
type TrustedProgressExplanationView struct {
	RequestID         string   `json:"requestId"`
	Status            string   `json:"status" enum:"PENDING,SENT,SETTLED,FAILED"`
	Stale             bool     `json:"stale"`
	FactSummarySHA256 string   `json:"factSummarySha256"`
	Summary           string   `json:"summary,omitempty"`
	CitedFactIDs      []string `json:"citedFactIds,omitempty"`
	Phase             string   `json:"phase,omitempty"`
	Attention         string   `json:"attention,omitempty"`
	NextOwnerRole     string   `json:"nextOwnerRole,omitempty"`
	PendingDecision   bool     `json:"pendingDecision"`
	ReasonCode        string   `json:"reasonCode,omitempty"`
}

// DeriveTrustedProgress computes the user-facing summary from durable facts.
func DeriveTrustedProgress(facts TrustedProgressFacts) TrustedProgressSummary {
	facts = SelectTrustedProgressFacts(facts)
	snapshot := facts.Snapshot
	overall := DeriveOverallProgress(snapshot, facts.Now)
	summary := TrustedProgressSummary{
		DevelopmentRequirementID:    snapshot.Requirement.ID,
		AOProjectID:                 snapshot.Requirement.AOProjectID,
		Name:                        snapshot.Requirement.Name,
		Phase:                       trustedPhaseFromOverall(overall.Phase),
		Attention:                   overall.Attention,
		CurrentRequirementVersionID: overall.CurrentRequirementVersionID,
		PendingRequirementVersionID: overall.PendingRequirementVersionID,
		TaskSetVersion:              overall.TaskSetVersion,
		TaskCounts:                  overall.TaskCounts,
		CanRequestExplanation:       facts.HasStewardSession,
	}
	if !facts.HasStewardSession {
		summary.ExplanationUnavailableReason = "STEWARD_UNAVAILABLE"
	}
	summary.Tasks = trustedTaskItems(snapshot, overall.CurrentRequirementVersionID)
	summary.CurrentCandidates = trustedCurrentCandidates(snapshot, overall.CurrentRequirementVersionID, facts.Now)
	summary.LatestFactSequence = latestRequirementEventSequence(snapshot.Events)
	summary.RecentFacts = trustedRecentFacts(snapshot.Events)
	for _, item := range overall.MissingEvidence {
		summary.MissingEvidence = append(summary.MissingEvidence, TrustedMissingEvidence(item))
	}
	applyTrustedOverlays(&summary, facts, overall)
	fillTrustedIssues(&summary, facts, overall)
	applyTrustedPlannerRuntime(&summary, facts)
	summary.NextOwner = deriveTrustedNextOwner(summary, facts)
	if summary.NextOwner.Role == "" {
		summary.NextOwner = TrustedOwner{Role: TrustedOwnerUnknown, Action: TrustedActionUnknown}
	}
	applyControlledProgress(&summary, facts)
	applyProjectPlanningProgress(&summary, facts)
	summary.CitableFacts = trustedCitableFacts(summary, snapshot, facts.PendingDecisions)
	summary.CitableFacts = append(summary.CitableFacts, plannerRuntimeCitableFacts(summary.PlannerCoordination)...)
	normalizeTrustedProgress(&summary)
	summary.SortRank = trustedProgressSortRank(summary)
	summary.FactSummarySHA256 = trustedProgressFactHash(summary)
	return summary
}

// SortTrustedProgressSummaries applies the frozen project-list order.
func SortTrustedProgressSummaries(items []TrustedProgressSummary) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SortRank != items[j].SortRank {
			return items[i].SortRank < items[j].SortRank
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].DevelopmentRequirementID < items[j].DevelopmentRequirementID
	})
}

func trustedPhaseFromOverall(phase OverallPhase) TrustedPhase {
	switch phase {
	case OverallPhaseCancelled:
		return TrustedPhaseCancelled
	case OverallPhaseAwaitingConfirmation:
		return TrustedPhaseAwaitingConfirmation
	case OverallPhaseDefiningRequirement:
		return TrustedPhaseDefiningRequirement
	case OverallPhasePlanningTasks:
		return TrustedPhasePlanning
	case OverallPhaseDeveloping:
		return TrustedPhaseDeveloping
	case OverallPhaseVerifying:
		return TrustedPhaseVerifying
	case OverallPhaseIntegrating:
		return TrustedPhaseIntegrating
	case OverallPhaseCompleted:
		return TrustedPhaseCompleted
	default:
		return TrustedPhaseUnknown
	}
}

func applyTrustedOverlays(summary *TrustedProgressSummary, facts TrustedProgressFacts, overall OverallProgress) {
	if summary.Phase == TrustedPhaseCancelled {
		return
	}
	if facts.DirectionChange != nil && facts.ComplexPlanning != nil {
		plans := facts.ComplexPlanning.Plans
		reviews := facts.ComplexPlanning.Reviews
		directionPhase := DeriveDirectionChangePhase(*facts.DirectionChange, facts.Snapshot.RequirementVersions, plans, reviews)
		if directionPhase != DirectionChangeNone && directionPhase != DirectionChangeApproved && directionPhase != DirectionChangeRejected {
			overlayDirectionPhase(summary, directionPhase)
			return
		}
	}
	if summary.PendingRequirementVersionID != "" {
		return
	}
	if facts.QuickExecution != nil {
		phase, reason, missing := CurrentQuickProgress(*facts.QuickExecution)
		if phase != ComplexExecutionCompleted {
			overlayExecutionPhase(summary, phase, reason, string(facts.QuickExecution.Run.Mode))
			appendMissingKinds(summary, missing)
			return
		}
		summary.Phase = TrustedPhaseCompleted
		summary.ExecutionMode = string(facts.QuickExecution.Run.Mode)
		return
	}
	if facts.ComplexExecution != nil {
		phase, reason, missing := CurrentExecutionProgress(*facts.ComplexExecution, facts.Controlled, facts.Now)
		if phase != ComplexExecutionCompleted {
			overlayExecutionPhase(summary, phase, reason, string(facts.ComplexExecution.Run.Mode))
			appendMissingKinds(summary, missing)
			return
		}
		summary.Phase = TrustedPhaseCompleted
		summary.ExecutionMode = string(facts.ComplexExecution.Run.Mode)
		return
	}
	if facts.ComplexPlanning != nil {
		planningPhase, reason := DeriveComplexPlanningPhase(*facts.ComplexPlanning, facts.Snapshot.RequirementVersions)
		if planningPhase != ComplexPlanningApproved {
			overlayPlanningPhase(summary, planningPhase, reason)
			return
		}
		if overall.Phase == OverallPhasePlanningTasks || overall.Phase == OverallPhaseDefiningRequirement {
			summary.Phase = TrustedPhaseAwaitingSteward
			summary.ReasonCode = reason
		}
	}
}

func overlayDirectionPhase(summary *TrustedProgressSummary, phase DirectionChangePhase) {
	switch phase {
	case DirectionChangeAwaitingSteward:
		summary.Phase = TrustedPhaseAwaitingSteward
	case DirectionChangeAwaitingDecision:
		summary.Phase = TrustedPhaseNeedsHuman
		summary.Attention = OverallAttentionNeedsHuman
	case DirectionChangeProcessing:
		summary.Phase = TrustedPhaseDeveloping
	case DirectionChangeCompiling:
		summary.Phase = TrustedPhaseDefiningRequirement
	case DirectionChangeAwaitingClarification:
		summary.Phase = TrustedPhaseAwaitingClarification
		summary.Attention = OverallAttentionNeedsHuman
	case DirectionChangeAwaitingConfirmation:
		summary.Phase = TrustedPhaseAwaitingConfirmation
		summary.Attention = OverallAttentionNeedsHuman
	case DirectionChangePlanning:
		summary.Phase = TrustedPhasePlanning
	case DirectionChangeAwaitingPlanReview:
		summary.Phase = TrustedPhaseAwaitingPlanReview
	case DirectionChangeNeedsHuman:
		summary.Phase = TrustedPhaseNeedsHuman
		summary.Attention = OverallAttentionNeedsHuman
	default:
		summary.Phase = TrustedPhaseUnknown
	}
}

func overlayExecutionPhase(summary *TrustedProgressSummary, phase ComplexExecutionPhase, reason ReasonCode, mode string) {
	summary.ExecutionMode = mode
	if reason != ReasonNone {
		summary.ReasonCode = reason
	}
	switch phase {
	case ComplexExecutionAwaitingSteward:
		summary.Phase = TrustedPhaseAwaitingSteward
	case ComplexExecutionCoordinating:
		summary.Phase = TrustedPhaseCoordinating
	case ComplexExecutionPreparingTasks, ComplexExecutionBindingBuilder, ComplexExecutionReadyToDispatch, ComplexExecutionBuilding:
		summary.Phase = TrustedPhaseDeveloping
	case ComplexExecutionReviewing:
		summary.Phase = TrustedPhaseVerifying
	case ComplexExecutionReworking:
		summary.Phase = TrustedPhaseReworking
		if summary.Attention == OverallAttentionNone {
			summary.Attention = OverallAttentionRework
		}
	case ComplexExecutionComposing, ComplexExecutionIntegrating:
		summary.Phase = TrustedPhaseIntegrating
	case ComplexExecutionAwaitingSpecialist:
		summary.Phase = TrustedPhaseAwaitingSpecialist
	case ComplexExecutionAwaitingScope:
		summary.Phase = TrustedPhaseAwaitingScope
	case ComplexExecutionRecovering:
		summary.Phase = TrustedPhaseRecovering
	case ComplexExecutionNeedsHuman:
		summary.Phase = TrustedPhaseNeedsHuman
		summary.Attention = OverallAttentionNeedsHuman
	case ComplexExecutionBlocked:
		summary.Phase = TrustedPhaseBlocked
		summary.Attention = OverallAttentionBlocked
	case ComplexExecutionCompleted:
		summary.Phase = TrustedPhaseCompleted
	default:
		summary.Phase = TrustedPhaseUnknown
	}
}

func overlayPlanningPhase(summary *TrustedProgressSummary, phase ComplexPlanningPhase, reason ReasonCode) {
	if reason != ReasonNone {
		summary.ReasonCode = reason
	}
	switch phase {
	case ComplexPlanningCompiling:
		summary.Phase = TrustedPhaseDefiningRequirement
	case ComplexPlanningAwaitingClarification:
		summary.Phase = TrustedPhaseAwaitingClarification
		summary.Attention = OverallAttentionNeedsHuman
	case ComplexPlanningAwaitingConfirmation:
		summary.Phase = TrustedPhaseAwaitingConfirmation
		summary.Attention = OverallAttentionNeedsHuman
	case ComplexPlanningPlanning, ComplexPlanningValidated:
		summary.Phase = TrustedPhasePlanning
	case ComplexPlanningProjectPlanned:
		summary.Phase, summary.Attention = TrustedPhaseBlocked, OverallAttentionBlocked
	case ComplexPlanningAwaitingPlanReview:
		summary.Phase = TrustedPhaseAwaitingPlanReview
	case ComplexPlanningNeedsHuman:
		summary.Phase = TrustedPhaseNeedsHuman
		summary.Attention = OverallAttentionNeedsHuman
	case ComplexPlanningRejected:
		summary.Phase = TrustedPhaseNeedsHuman
		summary.Attention = OverallAttentionNeedsHuman
	default:
		summary.Phase = TrustedPhaseUnknown
	}
}

func appendMissingKinds(summary *TrustedProgressSummary, kinds []string) {
	seen := map[string]struct{}{}
	for _, item := range summary.MissingEvidence {
		seen[item.Kind] = struct{}{}
	}
	for _, kind := range kinds {
		if kind == "" {
			continue
		}
		if _, ok := seen[kind]; ok {
			continue
		}
		summary.MissingEvidence = append(summary.MissingEvidence, TrustedMissingEvidence{Kind: kind})
		seen[kind] = struct{}{}
	}
}

func fillTrustedIssues(summary *TrustedProgressSummary, facts TrustedProgressFacts, overall OverallProgress) {
	if strings.TrimSpace(overall.PendingRequirementVersionID) != "" {
		summary.PendingDecisions = append(summary.PendingDecisions, TrustedIssue{
			Kind: "REQUIREMENT_CONFIRMATION", SubjectType: string(SubjectRequirementVersion),
			SubjectID: overall.PendingRequirementVersionID, ReasonCode: ReasonHumanDecisionRequired,
		})
	}
	for _, request := range facts.PendingDecisions {
		if request.Status != HumanDecisionRequestPending {
			continue
		}
		summary.PendingDecisions = append(summary.PendingDecisions, TrustedIssue{
			Kind: request.DecisionKind, SubjectType: string(SubjectHumanDecisionRequest), SubjectID: request.ID,
		})
	}
	if summary.Phase == TrustedPhaseAwaitingClarification {
		summary.PendingDecisions = append(summary.PendingDecisions, TrustedIssue{Kind: "CLARIFICATION_ANSWERS"})
	}
	if summary.Phase == TrustedPhaseAwaitingScope {
		summary.CurrentWork = append(summary.CurrentWork, TrustedIssue{Kind: "SCOPE_EXPANSION"})
	}
	if facts.DirectionChange != nil && facts.ComplexPlanning != nil {
		phase := DeriveDirectionChangePhase(*facts.DirectionChange, facts.Snapshot.RequirementVersions, facts.ComplexPlanning.Plans, facts.ComplexPlanning.Reviews)
		if phase == DirectionChangeAwaitingDecision {
			summary.PendingDecisions = append(summary.PendingDecisions, TrustedIssue{Kind: "DIRECTION_DECISION"})
		}
	}
	currentID := overall.CurrentRequirementVersionID
	for _, task := range facts.Snapshot.DevelopmentTasks {
		if currentID == "" || task.RequirementVersionID != currentID || task.Status == DevelopmentTaskStatusCancelled {
			continue
		}
		switch task.Status {
		case DevelopmentTaskStatusRunning, DevelopmentTaskStatusReview:
			summary.CurrentWork = append(summary.CurrentWork, TrustedIssue{
				Kind: string(task.Status), SubjectType: string(SubjectDevelopmentTask), SubjectID: task.ID,
			})
		case DevelopmentTaskStatusRework:
			summary.CurrentWork = append(summary.CurrentWork, TrustedIssue{
				Kind: string(task.Status), SubjectType: string(SubjectDevelopmentTask), SubjectID: task.ID,
			})
			summary.Blockers = append(summary.Blockers, TrustedIssue{
				Kind: "TASK_REWORK", SubjectType: string(SubjectDevelopmentTask), SubjectID: task.ID,
			})
		case DevelopmentTaskStatusBlocked:
			summary.Blockers = append(summary.Blockers, TrustedIssue{
				Kind: "TASK_BLOCKED", SubjectType: string(SubjectDevelopmentTask), SubjectID: task.ID,
			})
		case DevelopmentTaskStatusNeedsHuman:
			summary.PendingDecisions = append(summary.PendingDecisions, TrustedIssue{
				Kind: "TASK_NEEDS_HUMAN", SubjectType: string(SubjectDevelopmentTask), SubjectID: task.ID,
				ReasonCode: ReasonHumanDecisionRequired,
			})
		case DevelopmentTaskStatusPlanned:
			if summary.Phase == TrustedPhaseDeveloping || summary.Phase == TrustedPhasePlanning {
				summary.CurrentWork = append(summary.CurrentWork, TrustedIssue{
					Kind: "TASK_PLANNED", SubjectType: string(SubjectDevelopmentTask), SubjectID: task.ID,
				})
			}
		}
	}
	appendCurrentReviewFailures(summary, facts, currentID)
	if overall.Attention == OverallAttentionIntegrationFailed {
		summary.Blockers = append(summary.Blockers, TrustedIssue{Kind: "INTEGRATION_FAILED"})
	}
	if summary.Phase == TrustedPhaseBlocked {
		summary.Blockers = append(summary.Blockers, TrustedIssue{Kind: "EXECUTION_BLOCKED", ReasonCode: summary.ReasonCode})
	}
	if len(summary.PendingDecisions) > 0 && summary.Attention == OverallAttentionNone {
		summary.Attention = OverallAttentionNeedsHuman
	}
	sortTrustedIssues(summary.CurrentWork)
	sortTrustedIssues(summary.Blockers)
	sortTrustedIssues(summary.PendingDecisions)
}

func deriveTrustedNextOwner(summary TrustedProgressSummary, facts TrustedProgressFacts) TrustedOwner {
	if len(summary.PendingDecisions) > 0 && (summary.Phase == TrustedPhaseAwaitingConfirmation || summary.Phase == TrustedPhaseNeedsHuman || summary.Phase == TrustedPhaseAwaitingClarification) {
		switch summary.Phase {
		case TrustedPhaseAwaitingConfirmation:
			return TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionConfirmRequirement}
		case TrustedPhaseAwaitingClarification:
			return TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionAnswerClarification}
		default:
			return TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionDecide}
		}
	}
	if summary.Attention == OverallAttentionNeedsHuman || summary.Phase == TrustedPhaseNeedsHuman {
		return TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionDecide}
	}
	if summary.Attention == OverallAttentionBlocked || summary.Phase == TrustedPhaseBlocked || len(summary.Blockers) > 0 && summary.Attention != OverallAttentionRework {
		if summary.Attention == OverallAttentionIntegrationFailed {
			return TrustedOwner{Role: TrustedOwnerControlPlane, Action: TrustedActionIntegrate}
		}
		return TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionUnblock}
	}
	if summary.Attention == OverallAttentionRework || summary.Phase == TrustedPhaseReworking {
		return TrustedOwner{Role: TrustedOwnerBuilder, Action: TrustedActionReworkTask}
	}
	switch summary.Phase {
	case TrustedPhaseCancelled, TrustedPhaseCompleted:
		return TrustedOwner{Role: TrustedOwnerNone, Action: TrustedActionNone}
	case TrustedPhaseAwaitingConfirmation:
		return TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionConfirmRequirement}
	case TrustedPhaseAwaitingClarification:
		return TrustedOwner{Role: TrustedOwnerHuman, Action: TrustedActionAnswerClarification}
	case TrustedPhaseDefiningRequirement:
		return TrustedOwner{Role: TrustedOwnerSteward, Action: TrustedActionCompileRequirement}
	case TrustedPhaseCoordinating:
		return TrustedOwner{Role: TrustedOwnerPlanner, Action: TrustedActionCoordinateEngineering}
	case TrustedPhasePlanning:
		if facts.ComplexPlanning != nil {
			phase, _ := DeriveComplexPlanningPhase(*facts.ComplexPlanning, facts.Snapshot.RequirementVersions)
			if phase == ComplexPlanningValidated {
				return TrustedOwner{Role: TrustedOwnerControlPlane, Action: TrustedActionStartTask}
			}
			if phase == ComplexPlanningApproved {
				return TrustedOwner{Role: TrustedOwnerSteward, Action: TrustedActionRequestDispatch}
			}
		}
		return TrustedOwner{Role: TrustedOwnerPlanner, Action: TrustedActionProducePlan}
	case TrustedPhaseAwaitingPlanReview:
		return TrustedOwner{Role: TrustedOwnerSteward, Action: TrustedActionReviewPlan}
	case TrustedPhaseAwaitingSteward:
		return TrustedOwner{Role: TrustedOwnerSteward, Action: TrustedActionApproveDispatch}
	case TrustedPhaseDeveloping:
		if facts.ComplexExecution != nil {
			phase, _ := DeriveComplexExecutionPhase(*facts.ComplexExecution)
			if phase == ComplexExecutionPreparingTasks || phase == ComplexExecutionReadyToDispatch || phase == ComplexExecutionBindingBuilder {
				return TrustedOwner{Role: TrustedOwnerControlPlane, Action: TrustedActionStartTask}
			}
		}
		if facts.QuickExecution != nil {
			phase, _ := DeriveComplexQuickPhase(*facts.QuickExecution)
			if phase == ComplexExecutionReadyToDispatch || phase == ComplexExecutionBindingBuilder {
				return TrustedOwner{Role: TrustedOwnerControlPlane, Action: TrustedActionStartTask}
			}
		}
		if hasCurrentTaskStatus(facts.Snapshot, summary.CurrentRequirementVersionID, DevelopmentTaskStatusPlanned) &&
			!hasCurrentTaskStatus(facts.Snapshot, summary.CurrentRequirementVersionID, DevelopmentTaskStatusRunning) {
			return TrustedOwner{Role: TrustedOwnerBuilder, Action: TrustedActionStartTask}
		}
		return TrustedOwner{Role: TrustedOwnerBuilder, Action: TrustedActionImplementTask}
	case TrustedPhaseVerifying:
		return TrustedOwner{Role: TrustedOwnerReviewer, Action: TrustedActionReviewCandidate}
	case TrustedPhaseIntegrating:
		return TrustedOwner{Role: TrustedOwnerControlPlane, Action: TrustedActionIntegrate}
	case TrustedPhaseAwaitingScope:
		return TrustedOwner{Role: TrustedOwnerSteward, Action: TrustedActionDecideScope}
	case TrustedPhaseAwaitingSpecialist:
		return TrustedOwner{Role: TrustedOwnerSpecialist, Action: TrustedActionCompleteSpecialist}
	case TrustedPhaseRecovering:
		return TrustedOwner{Role: TrustedOwnerRecovery, Action: TrustedActionCompleteRecovery}
	default:
		return TrustedOwner{Role: TrustedOwnerUnknown, Action: TrustedActionUnknown}
	}
}

func appendCurrentReviewFailures(summary *TrustedProgressSummary, facts TrustedProgressFacts, currentVersionID string) {
	switch {
	case facts.QuickExecution != nil:
		return
	case facts.ComplexExecution != nil:
		appendCurrentComplexReviewFailures(summary, *facts.ComplexExecution, facts.Snapshot, currentVersionID)
	case facts.StandardFlow != nil:
		appendCurrentStandardReviewFailures(summary, *facts.StandardFlow, facts.Snapshot, currentVersionID)
	}
}

func appendCurrentStandardReviewFailures(summary *TrustedProgressSummary, flow StandardFlowSnapshot, snapshot RequirementSnapshot, currentVersionID string) {
	if currentVersionID == "" || flow.RequirementVersionID != currentVersionID {
		return
	}
	currentCandidates := currentTaskCandidateIDs(snapshot, currentVersionID)
	acceptedDispatches := acceptedDispatchIDs(flow, currentVersionID)
	for _, review := range flow.LocalReviews {
		if !failedReviewVerdict(review.Verdict) {
			continue
		}
		candidate, ok := candidateByID(snapshot, review.CandidateCommitID)
		if !ok || currentCandidates[candidate.DevelopmentTaskID] != candidate.ID {
			continue
		}
		if !acceptedDispatches[review.DispatchID] {
			continue
		}
		appendReviewFailedBlocker(summary, review.ID, review.ReasonCode)
	}
}

func appendCurrentComplexReviewFailures(summary *TrustedProgressSummary, execution ComplexExecutionSnapshot, snapshot RequirementSnapshot, currentVersionID string) {
	tasks := map[string]ComplexExecutionTask{}
	for _, task := range execution.Tasks {
		tasks[task.ID] = task
	}
	dispatches := map[string]ComplexExecutionDispatch{}
	for _, dispatch := range execution.Dispatches {
		dispatches[dispatch.ID] = dispatch
	}
	currentCandidates := currentTaskCandidateIDs(snapshot, currentVersionID)
	for _, review := range execution.Reviews {
		if !failedReviewVerdict(review.Verdict) {
			continue
		}
		task, ok := tasks[review.ComplexExecutionTaskID]
		if !ok || review.DispatchID != task.CurrentDispatchID {
			continue
		}
		devTask, ok := taskByIDInSnapshot(snapshot, task.DevelopmentTaskID)
		if !ok || currentVersionID == "" || devTask.RequirementVersionID != currentVersionID || devTask.Status == DevelopmentTaskStatusCancelled {
			continue
		}
		dispatch, ok := dispatches[review.DispatchID]
		if !ok || dispatch.Round != task.CurrentRound {
			continue
		}
		if dispatch.CandidateCommitID != "" && review.CandidateCommitID != dispatch.CandidateCommitID {
			continue
		}
		if currentID := currentCandidates[task.DevelopmentTaskID]; currentID != "" && review.CandidateCommitID != currentID {
			continue
		}
		appendReviewFailedBlocker(summary, review.ID, review.ReasonCode)
	}
}

func failedReviewVerdict(verdict LocalReviewVerdict) bool {
	return verdict == LocalReviewRework || verdict == LocalReviewBlocked
}

func appendReviewFailedBlocker(summary *TrustedProgressSummary, reviewID string, reason ReasonCode) {
	summary.Blockers = append(summary.Blockers, TrustedIssue{
		Kind: "REVIEW_FAILED", SubjectType: string(SubjectLocalReview), SubjectID: reviewID,
		ReasonCode: reason,
	})
}

func acceptedDispatchIDs(flow StandardFlowSnapshot, currentVersionID string) map[string]bool {
	ids := map[string]bool{}
	for _, dispatch := range flow.Dispatches {
		if dispatch.Status == DispatchStatusAccepted && dispatch.RequirementVersionID == currentVersionID {
			ids[dispatch.ID] = true
		}
	}
	return ids
}

func currentTaskCandidateIDs(snapshot RequirementSnapshot, currentVersionID string) map[string]string {
	latest := map[string]CandidateCommit{}
	for _, candidate := range snapshot.Candidates {
		task, ok := taskByIDInSnapshot(snapshot, candidate.DevelopmentTaskID)
		if !ok || currentVersionID == "" || task.RequirementVersionID != currentVersionID || task.Status == DevelopmentTaskStatusCancelled {
			continue
		}
		prev, exists := latest[candidate.DevelopmentTaskID]
		if !exists || candidate.Sequence > prev.Sequence {
			latest[candidate.DevelopmentTaskID] = candidate
		}
	}
	ids := make(map[string]string, len(latest))
	for taskID, candidate := range latest {
		ids[taskID] = candidate.ID
	}
	return ids
}

func candidateByID(snapshot RequirementSnapshot, id string) (CandidateCommit, bool) {
	for _, candidate := range snapshot.Candidates {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return CandidateCommit{}, false
}

func hasCurrentTaskStatus(snapshot RequirementSnapshot, currentVersionID string, status DevelopmentTaskStatus) bool {
	for _, task := range snapshot.DevelopmentTasks {
		if currentVersionID != "" && task.RequirementVersionID == currentVersionID && task.Status == status {
			return true
		}
	}
	return false
}

func trustedTaskItems(snapshot RequirementSnapshot, currentVersionID string) []TrustedTaskItem {
	items := make([]TrustedTaskItem, 0, len(snapshot.DevelopmentTasks))
	for _, task := range snapshot.DevelopmentTasks {
		items = append(items, TrustedTaskItem{
			DevelopmentTaskID: task.ID, Title: task.Title, Status: task.Status,
			RequirementVersionID: task.RequirementVersionID,
			Current:              currentVersionID != "" && task.RequirementVersionID == currentVersionID && task.Status != DevelopmentTaskStatusCancelled,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Current != items[j].Current {
			return items[i].Current
		}
		return items[i].DevelopmentTaskID < items[j].DevelopmentTaskID
	})
	return items
}

func trustedCurrentCandidates(snapshot RequirementSnapshot, currentVersionID string, now time.Time) []TrustedCandidate {
	var out []TrustedCandidate
	current, _, _ := requirementVersions(snapshot.RequirementVersions)
	if current != nil {
		candidate, result := currentIntegrationResult(snapshot, *current, now)
		if candidate != nil {
			out = append(out, TrustedCandidate{
				Kind: "INTEGRATION", ID: candidate.ID, CommitSHA: candidate.CommitSHA,
				Sequence: candidate.Sequence, Current: result != EvidenceResultFail,
			})
		}
	}
	latestByTask := map[string]CandidateCommit{}
	for _, candidate := range snapshot.Candidates {
		task, ok := taskByIDInSnapshot(snapshot, candidate.DevelopmentTaskID)
		if !ok || currentVersionID == "" || task.RequirementVersionID != currentVersionID {
			continue
		}
		prev, exists := latestByTask[candidate.DevelopmentTaskID]
		if !exists || candidate.Sequence > prev.Sequence {
			latestByTask[candidate.DevelopmentTaskID] = candidate
		}
	}
	for _, candidate := range latestByTask {
		out = append(out, TrustedCandidate{
			Kind: "TASK", ID: candidate.ID, DevelopmentTaskID: candidate.DevelopmentTaskID,
			CommitSHA: candidate.CommitSHA, Sequence: candidate.Sequence, Current: true,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func taskByIDInSnapshot(snapshot RequirementSnapshot, id string) (DevelopmentTask, bool) {
	for _, task := range snapshot.DevelopmentTasks {
		if task.ID == id {
			return task, true
		}
	}
	return DevelopmentTask{}, false
}

func latestRequirementEventSequence(events []RequirementEvent) int64 {
	var latest int64
	for _, event := range events {
		if event.Sequence > latest {
			latest = event.Sequence
		}
	}
	return latest
}

func trustedRecentFacts(events []RequirementEvent) []TrustedRecentFact {
	accepted := make([]RequirementEvent, 0, len(events))
	for _, event := range events {
		if event.Outcome == EventAccepted {
			accepted = append(accepted, event)
		}
	}
	sort.Slice(accepted, func(i, j int) bool {
		return accepted[i].Sequence > accepted[j].Sequence
	})
	if len(accepted) > trustedRecentFactLimit {
		accepted = accepted[:trustedRecentFactLimit]
	}
	out := make([]TrustedRecentFact, 0, len(accepted))
	for _, event := range accepted {
		out = append(out, TrustedRecentFact{
			Sequence: event.Sequence, Action: event.Action,
			SubjectType: string(event.SubjectType), SubjectID: event.SubjectID,
		})
	}
	return out
}

func trustedCitableFacts(summary TrustedProgressSummary, snapshot RequirementSnapshot, pending []HumanDecisionRequest) []TrustedCitableFact {
	facts := make([]TrustedCitableFact, 0, len(snapshot.Events)+len(snapshot.RequirementVersions)+len(summary.Tasks)+len(summary.CurrentCandidates)+len(pending))
	for _, event := range snapshot.Events {
		facts = append(facts, TrustedCitableFact{ID: fmt.Sprintf("event:%d", event.Sequence), Kind: "EVENT"})
	}
	for _, version := range snapshot.RequirementVersions {
		facts = append(facts, TrustedCitableFact{ID: "version:" + version.ID, Kind: "REQUIREMENT_VERSION"})
	}
	for _, task := range summary.Tasks {
		facts = append(facts, TrustedCitableFact{ID: "task:" + task.DevelopmentTaskID, Kind: "DEVELOPMENT_TASK"})
	}
	for _, candidate := range summary.CurrentCandidates {
		facts = append(facts, TrustedCitableFact{ID: "candidate:" + candidate.ID, Kind: candidate.Kind})
	}
	for _, request := range pending {
		facts = append(facts, TrustedCitableFact{ID: "decision:" + request.ID, Kind: "HUMAN_DECISION"})
	}
	seen := map[string]bool{}
	for _, w := range summary.ControlledWork {
		for _, item := range []struct{ prefix, id, kind string }{{"attempt:", w.AttemptID, "AGENT_ATTEMPT"}, {"attempt-event:", w.FailureEventID, "ATTEMPT_EVENT"}, {"preflight:", w.PreflightID, "CONTROLLED_PREFLIGHT"}, {"recovery:", w.RecoveryRequestID, "FIXED_RECOVERY"}, {"replacement-result:", w.ReplacementResultID, "REPLACEMENT_REVIEW_RESULT"}} {
			id := item.prefix + item.id
			if item.id != "" && !seen[id] {
				facts = append(facts, TrustedCitableFact{ID: id, Kind: item.kind})
				seen[id] = true
			}
		}
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].ID < facts[j].ID })
	return facts
}

func sortTrustedIssues(items []TrustedIssue) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].SubjectID < items[j].SubjectID
	})
}

func trustedProgressSortRank(summary TrustedProgressSummary) int {
	switch {
	case summary.Attention == OverallAttentionNeedsHuman || summary.Phase == TrustedPhaseNeedsHuman ||
		summary.Phase == TrustedPhaseAwaitingConfirmation || summary.Phase == TrustedPhaseAwaitingClarification ||
		len(summary.PendingDecisions) > 0:
		return TrustedSortNeedsHuman
	case summary.Attention == OverallAttentionBlocked || summary.Attention == OverallAttentionRework ||
		summary.Attention == OverallAttentionIntegrationFailed || summary.Phase == TrustedPhaseBlocked ||
		summary.Phase == TrustedPhaseReworking || len(summary.Blockers) > 0:
		return TrustedSortBlockedOrRework
	case summary.Phase == TrustedPhaseCompleted || summary.Phase == TrustedPhaseCancelled:
		return TrustedSortCompletedOrCancelled
	default:
		return TrustedSortInProgress
	}
}

type trustedProgressHashBody struct {
	ControlledWork              []ControlledWork             `json:"controlledWork"`
	DevelopmentRequirementID    string                       `json:"developmentRequirementId"`
	AOProjectID                 string                       `json:"aoProjectId"`
	Name                        string                       `json:"name"`
	Phase                       TrustedPhase                 `json:"phase"`
	Attention                   OverallAttention             `json:"attention"`
	ReasonCode                  ReasonCode                   `json:"reasonCode,omitempty"`
	CurrentRequirementVersionID string                       `json:"currentRequirementVersionId,omitempty"`
	PendingRequirementVersionID string                       `json:"pendingRequirementVersionId,omitempty"`
	TaskSetVersion              *int64                       `json:"taskSetVersion,omitempty"`
	ExecutionMode               string                       `json:"executionMode,omitempty"`
	TaskCounts                  TaskCounts                   `json:"taskCounts"`
	Tasks                       []TrustedTaskItem            `json:"tasks"`
	CurrentWork                 []TrustedIssue               `json:"currentWork"`
	Blockers                    []TrustedIssue               `json:"blockers"`
	PendingDecisions            []TrustedIssue               `json:"pendingDecisions"`
	NextOwner                   TrustedOwner                 `json:"nextOwner"`
	MissingEvidence             []TrustedMissingEvidence     `json:"missingEvidence"`
	CurrentCandidates           []TrustedCandidate           `json:"currentCandidates"`
	PlannerCoordination         []TrustedPlannerCoordination `json:"plannerCoordination,omitempty"`
	ProjectPlanning             *ProjectPlanningState        `json:"projectPlanning,omitempty"`
	LatestFactSequence          int64                        `json:"latestFactSequence"`
}

func trustedProgressFactHash(summary TrustedProgressSummary) string {
	body := trustedProgressHashBody{
		ControlledWork:           summary.ControlledWork,
		DevelopmentRequirementID: summary.DevelopmentRequirementID, AOProjectID: summary.AOProjectID,
		Name: summary.Name, Phase: summary.Phase, Attention: summary.Attention, ReasonCode: summary.ReasonCode,
		CurrentRequirementVersionID: summary.CurrentRequirementVersionID, PendingRequirementVersionID: summary.PendingRequirementVersionID,
		TaskSetVersion: summary.TaskSetVersion, ExecutionMode: summary.ExecutionMode, TaskCounts: summary.TaskCounts,
		Tasks: summary.Tasks, CurrentWork: summary.CurrentWork, Blockers: summary.Blockers,
		PendingDecisions: summary.PendingDecisions, NextOwner: summary.NextOwner, MissingEvidence: summary.MissingEvidence,
		CurrentCandidates: summary.CurrentCandidates, LatestFactSequence: summary.LatestFactSequence,
		PlannerCoordination: summary.PlannerCoordination, ProjectPlanning: summary.ProjectPlanning,
	}
	raw, err := marshalCanonicalJSON(body)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest)
}

func normalizeTrustedProgress(summary *TrustedProgressSummary) {
	if summary.Tasks == nil {
		summary.Tasks = []TrustedTaskItem{}
	}
	if summary.CurrentWork == nil {
		summary.CurrentWork = []TrustedIssue{}
	}
	if summary.Blockers == nil {
		summary.Blockers = []TrustedIssue{}
	}
	if summary.PendingDecisions == nil {
		summary.PendingDecisions = []TrustedIssue{}
	}
	if summary.MissingEvidence == nil {
		summary.MissingEvidence = []TrustedMissingEvidence{}
	}
	if summary.CurrentCandidates == nil {
		summary.CurrentCandidates = []TrustedCandidate{}
	}
	if summary.RecentFacts == nil {
		summary.RecentFacts = []TrustedRecentFact{}
	}
	if summary.CitableFacts == nil {
		summary.CitableFacts = []TrustedCitableFact{}
	}
}

// CitableFactIDSet returns the identifiers a steward result may quote.
func CitableFactIDSet(facts []TrustedCitableFact) map[string]struct{} {
	out := make(map[string]struct{}, len(facts))
	for _, fact := range facts {
		out[fact.ID] = struct{}{}
	}
	return out
}

// Progress explanation request statuses. SETTLED notes never change derived phase.
const (
	ProgressExplanationPending = "PENDING"
	ProgressExplanationSent    = "SENT"
	ProgressExplanationSettled = "SETTLED"
	ProgressExplanationFailed  = "FAILED"
)

// ProgressExplanationRequest is the durable steward-note occupancy for one snapshot.
type ProgressExplanationRequest struct {
	ID                            string
	DevelopmentRequirementID      string
	FactSummarySHA256             string
	MaxEventSequence              int64
	SourceStewardSessionID        string
	ContinuationOfSessionID       string
	AOSessionID                   string
	SessionCreationIdempotencyKey string
	ClientMessageID               string
	PromptText                    string
	PromptSHA256                  string
	Status                        string
	ResultJSON                    string
	ResultSHA256                  string
	ReasonCode                    ReasonCode
	CreatedAt                     time.Time
	SentAt                        *time.Time
	SettledAt                     *time.Time
}

// OccupyProgressExplanationCommand inserts one snapshot occupancy.
type OccupyProgressExplanationCommand struct {
	Request ProgressExplanationRequest
}
