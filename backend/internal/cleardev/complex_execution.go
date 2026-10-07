package cleardev

import (
	"sort"
	"strings"
	"time"
)

// ComplexExecutionProtocolVersion, ComplexStandardTaskSetVersion, and
// ComplexStandardMaxReworkCount freezes the S06 execution protocol limits and
// keeps the historical mail value of one rework round. A generic project task
// may be sent back ComplexProjectMaxReworkCount times before it stops for a
// person; the mail flow's attempt rules live in its own attempt slots.
const (
	ComplexExecutionProtocolVersion = 1
	ComplexStandardTaskSetVersion   = int64(1)
	ComplexStandardMaxReworkCount   = 1
	ComplexProjectMaxReworkCount    = 3

	// ComplexFinalReviewMaxReworkCount is the stage final review's own
	// send-back allowance, counted per run and independent of any task's
	// rework budget: each settled REWORK verdict may be dispatched once by a
	// person, three times in total before the run stops blocked.
	ComplexFinalReviewMaxReworkCount = 3
)

// ComplexExecutionAgentStepBuilderTask identifies a Builder task dispatch.
const (
	ComplexExecutionAgentStepBuilderTask AgentStepKind = "BUILDER_TASK"
)

// SubjectComplexExecutionRun and the related S06 facts deliberately have their
// own subjects and actions. In particular, they must not be written through
// the S02 single-task tables.
const (
	SubjectComplexExecutionRun          SubjectType = "COMPLEX_EXECUTION_RUN"
	SubjectComplexExecutionTask         SubjectType = "COMPLEX_EXECUTION_TASK"
	SubjectComplexExecutionDispatch     SubjectType = "COMPLEX_EXECUTION_DISPATCH"
	SubjectComplexExecutionVerification SubjectType = "COMPLEX_EXECUTION_VERIFICATION"
	SubjectComplexExecutionIntegration  SubjectType = "COMPLEX_EXECUTION_INTEGRATION"

	ActionRequestComplexExecution  Action = "REQUEST_COMPLEX_EXECUTION"
	ActionAcceptComplexExecution   Action = "ACCEPT_COMPLEX_EXECUTION"
	ActionDispatchComplexTask      Action = "DISPATCH_COMPLEX_TASK"
	ActionVerifyComplexCandidate   Action = "VERIFY_COMPLEX_CANDIDATE"
	ActionCompleteComplexExecution Action = "COMPLETE_COMPLEX_EXECUTION"

	ReasonParallelNotAvailable          ReasonCode = "PARALLEL_NOT_AVAILABLE"
	ReasonComplexExecutionPlanNotReady  ReasonCode = "COMPLEX_EXECUTION_PLAN_NOT_READY"
	ReasonComplexExecutionStopped       ReasonCode = "COMPLEX_EXECUTION_STOPPED"
	ReasonComplexDependencyIncomplete   ReasonCode = "DEPENDENCY_EVIDENCE_INCOMPLETE"
	ReasonComplexExecutionTaskSetChange ReasonCode = "EXECUTION_TASK_SET_CHANGED"
)

// ComplexExecutionRun is the immutable S06 binding to one confirmed v2 and
// one reviewed complex plan. Status records a durable terminal outcome; the
// UI phase is always recomputed from this and the child facts below.
type ComplexExecutionRun struct {
	// RuntimeProjectExecution is derived from append-only Planner decisions. The admission envelope remains unchanged.
	RuntimeProjectExecution  *ProjectExecutionContract `json:"runtimeProjectExecution,omitempty"`
	ID                       string                    `json:"id"`
	DevelopmentRequirementID string                    `json:"developmentRequirementId"`
	RequirementVersionID     string                    `json:"requirementVersionId"`
	RequirementSHA256        string                    `json:"requirementSha256"`
	PlanID                   string                    `json:"planId"`
	PlanReviewID             string                    `json:"planReviewId"`
	PlanSHA256               string                    `json:"planSha256"`
	Mode                     WorkMode                  `json:"mode"`
	ModeReason               string                    `json:"modeReason"`
	// FixedBuilderCount is the frozen team size chosen by the control plane.
	// STANDARD runs always record 1; PARALLEL runs record 2 or 3.
	FixedBuilderCount      int   `json:"fixedBuilderCount"`
	ExpectedTaskSetVersion int64 `json:"expectedTaskSetVersion"`
	TaskSetVersion         int64 `json:"taskSetVersion"`
	// ExecutionPackageJSON is the immutable execution-level envelope saved with
	// the PENDING run. Per-task packets are saved on ComplexExecutionTask.
	// Keeping both facts explicit prevents a store from inventing JSON merely to
	// satisfy the durable S06 contract.
	ExecutionPackageJSON   string                   `json:"executionPackageJson"`
	ExecutionPackageSHA256 string                   `json:"executionPackageSha256"`
	StewardRoleBindingID   string                   `json:"stewardRoleBindingId"`
	BuilderRoleBindingID   string                   `json:"builderRoleBindingId,omitempty"`
	BuilderAOSessionID     string                   `json:"builderAoSessionId,omitempty"`
	InitialBaseCommitSHA   string                   `json:"initialBaseCommitSha,omitempty"`
	Decision               ComplexExecutionDecision `json:"decision,omitempty"`
	ReasonCode             ReasonCode               `json:"reasonCode,omitempty"`
	CreatedAt              time.Time                `json:"createdAt"`
	CompletedAt            *time.Time               `json:"completedAt,omitempty"`
}

// ComplexExecutionDecision is the only result the Project Steward may return
// when S06 asks whether an already approved plan may be dispatched.
type ComplexExecutionDecision string

// ComplexExecutionDecisionDispatch and ComplexExecutionDecisionNeedsHuman are
// the only accepted Project Steward outcomes.
const (
	ComplexExecutionDecisionDispatch   ComplexExecutionDecision = "DISPATCH"
	ComplexExecutionDecisionNeedsHuman ComplexExecutionDecision = "NEEDS_HUMAN"
)

// ComplexExecutionTask maps the immutable complex-plan task key to exactly
// one formal work item. DependencyTaskKeys retain plan order and are never
// inferred from mutable task text.
type ComplexExecutionTask struct {
	ID                     string                `json:"id"`
	ExecutionRunID         string                `json:"executionRunId"`
	TaskKey                string                `json:"taskKey"`
	DevelopmentTaskID      string                `json:"developmentTaskId"`
	Ordinal                int                   `json:"ordinal"`
	DependencyTaskKeys     []string              `json:"dependencyTaskKeys"`
	ExecutionPackageJSON   string                `json:"executionPackageJson"`
	ExecutionPackageSHA256 string                `json:"executionPackageSha256"`
	Status                 DevelopmentTaskStatus `json:"status"`
	ReworkCount            int                   `json:"reworkCount"`
	CurrentRound           int                   `json:"currentRound"`
	CurrentDispatchID      string                `json:"currentDispatchId,omitempty"`
}

// ComplexExecutionRoleBinding binds an S06 responsibility to an AO session.
// Steward and Planner retain their S04 source binding; a continuation records
// why a replacement Steward session exists instead of silently changing
// authority. Builder and Reviewer bindings additionally carry their exact
// worktree/candidate scope.
type ComplexExecutionRoleBinding struct {
	ID                          string       `json:"id"`
	ExecutionRunID              string       `json:"executionRunId"`
	SourceComplexRoleBindingID  string       `json:"sourceComplexRoleBindingId,omitempty"`
	ContinuationOfRoleBindingID string       `json:"continuationOfRoleBindingId,omitempty"`
	Role                        StandardRole `json:"role"`
	// BuilderSlot numbers the BUILDER bindings of a PARALLEL run from 1. It is
	// empty for every other role and for the single S06 builder.
	BuilderSlot                   int               `json:"builderSlot,omitempty"`
	TaskMappingID                 string            `json:"taskMappingId,omitempty"`
	CandidateCommitID             string            `json:"candidateCommitId,omitempty"`
	SessionCreationIdempotencyKey string            `json:"sessionCreationIdempotencyKey"`
	AOSessionID                   string            `json:"aoSessionId,omitempty"`
	WorkspacePath                 string            `json:"workspacePath,omitempty"`
	BaseCommitSHA                 string            `json:"baseCommitSha,omitempty"`
	Status                        RoleBindingStatus `json:"status"`
	ReasonCode                    ReasonCode        `json:"reasonCode,omitempty"`
	RequestedAt                   time.Time         `json:"requestedAt"`
	BoundAt                       *time.Time        `json:"boundAt,omitempty"`
	EndedAt                       *time.Time        `json:"endedAt,omitempty"`
}

// ComplexExecutionCheckSpecFact is the persisted fixed command definition
// selected from FrozenComplexCheckCatalog. Candidate checks can refer only to
// this fact, never to argv supplied by a Builder or Reviewer.
type ComplexExecutionCheckSpecFact struct {
	ID                     string             `json:"id"`
	ExecutionRunID         string             `json:"executionRunId"`
	ComplexExecutionTaskID string             `json:"complexExecutionTaskId,omitempty"`
	CheckID                string             `json:"checkId"`
	Kind                   CandidateCheckKind `json:"kind"`
	CheckSpecSHA256        string             `json:"checkSpecSha256"`
	Argv                   []string           `json:"argv"`
	TimeoutSeconds         int                `json:"timeoutSeconds"`
	CreatedAt              time.Time          `json:"createdAt"`
}

// ComplexExecutionCheckRun records one trusted local check of one candidate.
// StartedAt is separate from SettledAt so a restart can distinguish a stored
// request from an externally uncertain invocation.
type ComplexExecutionCheckRun struct {
	ID                     string                         `json:"id"`
	ExecutionRunID         string                         `json:"executionRunId"`
	ComplexExecutionTaskID string                         `json:"complexExecutionTaskId,omitempty"`
	DispatchID             string                         `json:"dispatchId,omitempty"`
	CandidateCommitID      string                         `json:"candidateCommitId,omitempty"`
	BaseCommitSHA          string                         `json:"baseCommitSha"`
	CandidateCommitSHA     string                         `json:"candidateCommitSha,omitempty"`
	CheckSpecFactID        string                         `json:"checkSpecFactId"`
	Kind                   CandidateCheckKind             `json:"kind"`
	Argv                   []string                       `json:"argv"`
	Status                 ComplexExecutionCheckRunStatus `json:"status"`
	Result                 EvidenceResult                 `json:"result,omitempty"`
	ReasonCode             ReasonCode                     `json:"reasonCode,omitempty"`
	ContainerImageID       string                         `json:"containerImageId,omitempty"`
	ExitCode               *int                           `json:"exitCode,omitempty"`
	TimedOut               bool                           `json:"timedOut"`
	OutputSummary          string                         `json:"outputSummary,omitempty"`
	OutputSHA256           string                         `json:"outputSha256,omitempty"`
	OutputTruncated        bool                           `json:"outputTruncated"`
	ChangedPathsJSON       string                         `json:"changedPathsJson,omitempty"`
	RetryOrdinal           int                            `json:"retryOrdinal,omitempty"`
	CreatedAt              time.Time                      `json:"createdAt"`
	StartedAt              *time.Time                     `json:"startedAt,omitempty"`
	SettledAt              *time.Time                     `json:"settledAt,omitempty"`
}

// ComplexExecutionCheckRunStatus distinguishes a persisted request from an
// externally started check. A STARTED row with no terminal result is an
// unknown external outcome after restart and must fail closed instead of
// launching the same check again.
type ComplexExecutionCheckRunStatus string

// ComplexExecutionCheckRunPending and the related values describe the durable
// lifecycle of one isolated check request.
const (
	ComplexExecutionCheckRunPending ComplexExecutionCheckRunStatus = "PENDING"
	ComplexExecutionCheckRunStarted ComplexExecutionCheckRunStatus = "STARTED"
	ComplexExecutionCheckRunSettled ComplexExecutionCheckRunStatus = "SETTLED"
	ComplexExecutionCheckRunFailed  ComplexExecutionCheckRunStatus = "FAILED"
)

// ComplexExecutionReview is an independent candidate review. It records the
// exact packet and role/Agent step that produced the verdict, so a stale
// reviewer result cannot be copied onto a later Builder round.
type ComplexExecutionReview struct {
	ID                     string             `json:"id"`
	ExecutionRunID         string             `json:"executionRunId"`
	ComplexExecutionTaskID string             `json:"complexExecutionTaskId"`
	DispatchID             string             `json:"dispatchId"`
	CandidateCommitID      string             `json:"candidateCommitId"`
	CandidateCommitSHA     string             `json:"candidateCommitSha"`
	BaseCommitSHA          string             `json:"baseCommitSha"`
	ReviewPacketJSON       string             `json:"reviewPacketJson"`
	ReviewPacketSHA256     string             `json:"reviewPacketSha256"`
	CandidateWorkspacePath string             `json:"candidateWorkspacePath"`
	ReviewerRoleBindingID  string             `json:"reviewerRoleBindingId"`
	AgentStepID            string             `json:"agentStepId"`
	Status                 LocalReviewStatus  `json:"status"`
	Verdict                LocalReviewVerdict `json:"verdict,omitempty"`
	ReasonCode             ReasonCode         `json:"reasonCode,omitempty"`
	Summary                string             `json:"summary,omitempty"`
	CreatedAt              time.Time          `json:"createdAt"`
	SettledAt              *time.Time         `json:"settledAt,omitempty"`
}

// ComplexExecutionDispatch is one Builder message round. Its base is frozen
// before the message is sent; a rework round therefore cannot borrow evidence
// or a client-message id from an earlier candidate.
type ComplexExecutionDispatch struct {
	ID                     string `json:"id"`
	ExecutionRunID         string `json:"executionRunId"`
	ComplexExecutionTaskID string `json:"complexExecutionTaskId"`
	DevelopmentTaskID      string `json:"developmentTaskId"`
	Round                  int    `json:"round"`
	BaseCommitSHA          string `json:"baseCommitSha"`
	AgentStepID            string `json:"agentStepId"`
	ExecutionPackageSHA256 string `json:"executionPackageSha256"`
	ClientMessageID        string `json:"clientMessageId"`
	CandidateCommitID      string `json:"candidateCommitId,omitempty"`
	CandidateCommitSHA     string `json:"candidateCommitSha,omitempty"`
	// BatchID is set for PARALLEL dispatches and empty for STANDARD ones.
	BatchID              string                         `json:"batchId,omitempty"`
	BuilderRoleBindingID string                         `json:"builderRoleBindingId,omitempty"`
	Status               ComplexExecutionDispatchStatus `json:"status"`
	ReasonCode           ReasonCode                     `json:"reasonCode,omitempty"`
	CreatedAt            time.Time                      `json:"createdAt"`
	SettledAt            *time.Time                     `json:"settledAt,omitempty"`
}

// ComplexExecutionDispatchStatus is the durable lifecycle of one Builder
// dispatch round.
type ComplexExecutionDispatchStatus string

// ComplexExecutionDispatchPending and the related values are the allowed
// Builder dispatch states.
const (
	ComplexExecutionDispatchPending    ComplexExecutionDispatchStatus = "PENDING"
	ComplexExecutionDispatchRunning    ComplexExecutionDispatchStatus = "RUNNING"
	ComplexExecutionDispatchObserved   ComplexExecutionDispatchStatus = "OBSERVED"
	ComplexExecutionDispatchReviewing  ComplexExecutionDispatchStatus = "REVIEWING"
	ComplexExecutionDispatchVerified   ComplexExecutionDispatchStatus = "VERIFIED"
	ComplexExecutionDispatchRework     ComplexExecutionDispatchStatus = "REWORK"
	ComplexExecutionDispatchBlocked    ComplexExecutionDispatchStatus = "BLOCKED"
	ComplexExecutionDispatchNeedsHuman ComplexExecutionDispatchStatus = "NEEDS_HUMAN"
	ComplexExecutionDispatchFailed     ComplexExecutionDispatchStatus = "FAILED"
)

// ComplexExecutionVerification is appended only after the exact dispatch
// candidate has passed scope, every required check, and an independent local
// review. It is the sole fact that makes a dependency dispatchable.
type ComplexExecutionVerification struct {
	ReplacementRecoveryID  string    `json:"replacementRecoveryId,omitempty"`
	ReplacementAttemptID   string    `json:"replacementAttemptId,omitempty"`
	ReplacementResultID    string    `json:"replacementResultId,omitempty"`
	ID                     string    `json:"id"`
	ExecutionRunID         string    `json:"executionRunId"`
	ComplexExecutionTaskID string    `json:"complexExecutionTaskId"`
	DispatchID             string    `json:"dispatchId"`
	CandidateCommitID      string    `json:"candidateCommitId"`
	CandidateCommitSHA     string    `json:"candidateCommitSha"`
	Round                  int       `json:"round"`
	ScopeEvidenceID        string    `json:"scopeEvidenceId"`
	RequiredCheckRunIDs    []string  `json:"requiredCheckRunIds"`
	LocalReviewID          string    `json:"localReviewId"`
	VerifiedAt             time.Time `json:"verifiedAt"`
}

// ComplexExecutionIntegration is the one requirement-level evidence source
// that permits every S06 work item to become DONE together.
type ComplexExecutionIntegration struct {
	ID                     string    `json:"id"`
	ExecutionRunID         string    `json:"executionRunId"`
	IntegrationCandidateID string    `json:"integrationCandidateId"`
	CandidateCommitSHA     string    `json:"candidateCommitSha"`
	CheckRunIDs            []string  `json:"checkRunIds"`
	CompletedAt            time.Time `json:"completedAt"`
}

// ComplexExecutionSnapshot is the S06 history needed by the scheduler and
// reader. Storage fills the durable facts below; the service fills Phase,
// PhaseReason, and MissingEvidence on reads. Those three fields are never
// persisted as a second lifecycle state.
type ComplexExecutionSnapshot struct {
	BuilderReplacementTransitions []BuilderReplacementTransition  `json:"-"`
	WorkflowRecoveries            []WorkflowRecovery              `json:"workflowRecoveries"`
	FixedRecoveries               []FixedRecoveryEvidence         `json:"fixedRecoveries"`
	Phase                         ComplexExecutionPhase           `json:"phase"`
	PhaseReason                   ReasonCode                      `json:"phaseReason,omitempty"`
	MissingEvidence               []string                        `json:"missingEvidence"`
	Run                           ComplexExecutionRun             `json:"run"`
	RoleBindings                  []ComplexExecutionRoleBinding   `json:"roleBindings"`
	AgentSteps                    []AgentStep                     `json:"agentSteps"`
	Tasks                         []ComplexExecutionTask          `json:"tasks"`
	Dispatches                    []ComplexExecutionDispatch      `json:"dispatches"`
	CheckSpecs                    []ComplexExecutionCheckSpecFact `json:"checkSpecs"`
	CheckRuns                     []ComplexExecutionCheckRun      `json:"checkRuns"`
	Reviews                       []ComplexExecutionReview        `json:"reviews"`
	Verifications                 []ComplexExecutionVerification  `json:"verifications"`
	Integration                   *ComplexExecutionIntegration    `json:"integration,omitempty"`
	FinalReview                   *RequirementFinalReview         `json:"finalReview,omitempty"`
	FinalReviewReworkCount        int                             `json:"finalReviewReworkCount,omitempty"`
	// Batches and Compositions exist only for PARALLEL runs. STANDARD runs
	// leave both empty and keep the S06 successor-candidate chain.
	Batches        []ComplexExecutionBatch       `json:"batches"`
	Compositions   []ComplexExecutionComposition `json:"compositions"`
	Exception      *ComplexExceptionFacts        `json:"exception,omitempty"`
	PlannerRuntime *PlannerRuntimeSnapshot       `json:"plannerRuntime,omitempty"`
}

// StartComplexExecutionCommand durably creates the request and its stable
// Steward message before any external Chat call occurs.
type StartComplexExecutionCommand struct {
	Run                ComplexExecutionRun
	StewardRoleBinding ComplexExecutionRoleBinding
	AgentStep          AgentStep
}

// MaterializeComplexExecutionCommand is the all-or-nothing conversion of an
// accepted request into every planned work item and its frozen authority. A
// PARALLEL run additionally freezes its dispatch batches and the full builder
// pool in the same transaction.
type MaterializeComplexExecutionCommand struct {
	ExecutionRunID  string
	Tasks           []ComplexExecutionTask
	CheckSpecs      []ComplexExecutionCheckSpecFact
	BuilderBindings []ComplexExecutionRoleBinding
	Batches         []ComplexExecutionBatch
	At              time.Time
}

// CreateComplexExecutionDispatchCommand persists a task round and the stable
// Builder message before it is sent. BatchID is set for PARALLEL dispatches
// and empty for STANDARD ones.
type CreateComplexExecutionDispatchCommand struct {
	Dispatch  ComplexExecutionDispatch
	AgentStep AgentStep
	BatchID   string
}

// AppendComplexExecutionCandidateCommand records only a Control Plane Git
// observation associated with one dispatch baseline and rework round.
type AppendComplexExecutionCandidateCommand struct {
	ExecutionRunID         string
	ComplexExecutionTaskID string
	DispatchID             string
	Round                  int
	BaseCommitSHA          string
	Candidate              CandidateObservation
}

// SettleComplexExecutionCheckCommand writes a terminal or infrastructure
// outcome for a check run that was first persisted by its stable identity.
type SettleComplexExecutionCheckCommand struct {
	CheckRunID       string
	Result           EvidenceResult
	ReasonCode       ReasonCode
	ContainerImageID string
	ExitCode         *int
	TimedOut         bool
	OutputSummary    string
	OutputSHA256     string
	OutputTruncated  bool
	ChangedPathsJSON string
	At               time.Time
}

// CreateComplexExecutionReviewCommand creates the candidate packet and its
// independent Reviewer role/Agent-step binding together.
type CreateComplexExecutionReviewCommand struct {
	Review          ComplexExecutionReview
	ReviewerBinding ComplexExecutionRoleBinding
	AgentStep       AgentStep
}

// SettleComplexExecutionReviewCommand only accepts the parsed terminal result
// of the stored Reviewer Agent step.
type SettleComplexExecutionReviewCommand struct {
	ReviewID       string
	TurnID         string
	FinalMessageID string
	Verdict        LocalReviewVerdict
	ReasonCode     ReasonCode
	Summary        string
	At             time.Time
}

// VerifyComplexExecutionCandidateCommand appends the current-round evidence
// conjunction that is required before dependent work can begin.
type VerifyComplexExecutionCandidateCommand struct {
	Verification ComplexExecutionVerification
}

// CompleteComplexExecutionCommand is intentionally small: storage derives and
// rechecks every task, candidate, check, review, and current task-set binding
// before it atomically persists integration and all DONE transitions.
type CompleteComplexExecutionCommand struct {
	ExecutionRunID string
	Integration    ComplexExecutionIntegration
	At             time.Time
}

// ComplexExecutionPhase is derived from persisted execution facts. A verified
// non-final task remains REVIEW rather than DONE until final integration.
type ComplexExecutionPhase string

// ComplexExecutionAwaitingSteward and the related values are derived display
// phases; they are never stored as an independent source of truth.
const (
	ComplexExecutionAwaitingSteward    ComplexExecutionPhase = "AWAITING_STEWARD"
	ComplexExecutionBindingBuilder     ComplexExecutionPhase = "BINDING_BUILDER"
	ComplexExecutionReadyToDispatch    ComplexExecutionPhase = "READY_TO_DISPATCH"
	ComplexExecutionBuilding           ComplexExecutionPhase = "BUILDING"
	ComplexExecutionReviewing          ComplexExecutionPhase = "REVIEWING"
	ComplexExecutionReworking          ComplexExecutionPhase = "REWORKING"
	ComplexExecutionComposing          ComplexExecutionPhase = "COMPOSING"
	ComplexExecutionIntegrating        ComplexExecutionPhase = "INTEGRATING"
	ComplexExecutionAwaitingSpecialist ComplexExecutionPhase = "AWAITING_SPECIALIST"
	ComplexExecutionAwaitingScope      ComplexExecutionPhase = "AWAITING_SCOPE"
	ComplexExecutionRecovering         ComplexExecutionPhase = "RECOVERING"
	ComplexExecutionCompleted          ComplexExecutionPhase = "COMPLETED"
	ComplexExecutionNeedsHuman         ComplexExecutionPhase = "NEEDS_HUMAN"
	ComplexExecutionBlocked            ComplexExecutionPhase = "BLOCKED"
)

// DeriveComplexExecutionPhase derives the externally visible state without
// treating a verified predecessor as DONE. Invalid or stranded dependency
// facts fail closed as BLOCKED.
func DeriveComplexExecutionPhase(snapshot ComplexExecutionSnapshot) (ComplexExecutionPhase, ReasonCode) {
	run := snapshot.Run
	if blocked, reason := PlannerRuntimeBarrier(snapshot); blocked {
		if reason != ReasonPlannerRuntimePending {
			return ComplexExecutionNeedsHuman, reason
		}
		if PlannerRuntimeQuiescent(snapshot) {
			return ComplexExecutionCoordinating, reason
		}
	}
	if phase, reason, stop := requirementFinalReviewStop(snapshot); stop {
		return phase, reason
	}
	if snapshot.Integration != nil && run.CompletedAt != nil {
		return ComplexExecutionCompleted, ReasonNone
	}
	if run.Decision == ComplexExecutionDecisionNeedsHuman || run.ReasonCode == ReasonHumanDecisionRequired {
		return ComplexExecutionNeedsHuman, ReasonHumanDecisionRequired
	}
	if run.ReasonCode != ReasonNone {
		return ComplexExecutionBlocked, run.ReasonCode
	}
	activeSteward := false
	for _, binding := range snapshot.RoleBindings {
		if binding.Role == StandardRoleSteward && (binding.Status == RoleBindingStatusRequested || binding.Status == RoleBindingStatusBound) {
			activeSteward = true
		}
		if binding.Status == RoleBindingStatusFailed {
			if WorkflowRecoveryResolvesBinding(snapshot, binding) {
				continue
			}
			if parallelPreDispatchBuilderTerminalRecoverable(snapshot, binding) {
				continue
			}
			reason := binding.ReasonCode
			if reason == ReasonNone {
				reason = ReasonComplexExecutionStopped
			}
			return ComplexExecutionBlocked, reason
		}
	}
	for _, binding := range snapshot.RoleBindings {
		if binding.Status != RoleBindingStatusEnded || (binding.Role != StandardRoleBuilder && (binding.Role != StandardRoleSteward || activeSteward)) {
			continue
		}
		if builderReplacementResolvesEndedRole(snapshot, binding) {
			continue
		}
		if parallelPreDispatchBuilderTerminalRecoverable(snapshot, binding) {
			continue
		}
		reason := binding.ReasonCode
		if reason == ReasonNone {
			reason = ReasonComplexExecutionStopped
		}
		return ComplexExecutionBlocked, reason
	}
	for _, step := range snapshot.AgentSteps {
		if step.SendStatus == AgentStepSendStatusFailed && !MailReplacementResolvedOriginalStep(snapshot, step) && !WorkflowRecoveryResolvesStep(snapshot, step) {
			reason := step.ReasonCode
			if reason == ReasonNone {
				reason = ReasonComplexExecutionStopped
			}
			return ComplexExecutionBlocked, reason
		}
	}
	if run.Decision != ComplexExecutionDecisionDispatch {
		contract, err := PlannerTaskContractRun(run)
		if err != nil {
			return ComplexExecutionBlocked, ReasonPreconditionNotMet
		}
		if contract {
			return ComplexExecutionPreparingTasks, ReasonNone
		}
		return ComplexExecutionAwaitingSteward, ReasonNone
	}
	if phase, reason, ok := deriveComplexExceptionPhase(snapshot); ok {
		return phase, reason
	}
	if run.Mode == WorkModeParallel {
		return deriveComplexParallelPhase(snapshot)
	}
	if strings.TrimSpace(run.BuilderAOSessionID) == "" || strings.TrimSpace(run.InitialBaseCommitSHA) == "" {
		return ComplexExecutionBindingBuilder, ReasonNone
	}
	for _, task := range snapshot.Tasks {
		switch task.Status {
		case DevelopmentTaskStatusNeedsHuman:
			return ComplexExecutionNeedsHuman, ReasonHumanDecisionRequired
		case DevelopmentTaskStatusBlocked, DevelopmentTaskStatusCancelled:
			if dispatch, ok := currentComplexExecutionDispatch(snapshot, task); ok && dispatch.ReasonCode != ReasonNone {
				return ComplexExecutionBlocked, dispatch.ReasonCode
			}
			return ComplexExecutionBlocked, ReasonComplexExecutionStopped
		case DevelopmentTaskStatusRunning:
			return ComplexExecutionBuilding, ReasonNone
		case DevelopmentTaskStatusRework:
			return ComplexExecutionReworking, ReasonNone
		case DevelopmentTaskStatusReview:
			if !ComplexExecutionTaskVerified(snapshot, task) {
				return ComplexExecutionReviewing, ReasonNone
			}
		}
	}
	if complexExecutionAllTasksVerified(snapshot) {
		if failed, ok := CurrentIntegrationCheckFailure(snapshot); ok {
			reason := failed.ReasonCode
			if reason == ReasonNone {
				reason = "CHECK_FAILED"
			}
			return ComplexExecutionBlocked, reason
		}
		// Verified predecessors only describe the candidate a dispatched
		// rework round will replace; the round itself still has to run.
		for _, task := range snapshot.Tasks {
			if task.ReworkCount > task.CurrentRound && !StoppedCheckRecoveryRetainsReworkCount(snapshot, task) {
				return ComplexExecutionReworking, ReasonNone
			}
		}
		if required, _ := RequirementFinalReviewRequired(run); required && !requirementFinalReviewPassed(snapshot.FinalReview) {
			return ComplexExecutionIntegrating, "REQUIREMENT_FINAL_REVIEW_PENDING"
		}
		return ComplexExecutionIntegrating, ReasonNone
	}
	if _, ok := NextEligibleComplexStandardTask(snapshot); ok {
		return ComplexExecutionReadyToDispatch, ReasonNone
	}
	return ComplexExecutionBlocked, ReasonComplexDependencyIncomplete
}

func parallelPreDispatchBuilderTerminalRecoverable(snapshot ComplexExecutionSnapshot, binding ComplexExecutionRoleBinding) bool {
	if snapshot.Run.Mode != WorkModeParallel || binding.Role != StandardRoleBuilder ||
		(binding.Status != RoleBindingStatusFailed && binding.Status != RoleBindingStatusEnded) {
		return false
	}
	if binding.ReasonCode != ReasonBuilderSpawnFailed && binding.ReasonCode != ReasonCode("CHECKER_UNAVAILABLE") {
		return false
	}
	for _, other := range snapshot.RoleBindings {
		if other.Role == StandardRoleBuilder && other.BuilderSlot == binding.BuilderSlot &&
			other.ContinuationOfRoleBindingID == binding.ID &&
			(other.Status == RoleBindingStatusRequested || other.Status == RoleBindingStatusBound) {
			// Creating a continuation is pre-dispatch only, but once the
			// replacement is active the superseded terminal binding must stay
			// non-blocking for the lifetime of that continuation, including
			// after its first task dispatch is persisted.
			return true
		}
	}
	return len(snapshot.Dispatches) == 0 && binding.ContinuationOfRoleBindingID == ""
}

// DeriveComplexExecutionMissingEvidence names the next proof required by the
// derived phase. It is deliberately computed from the same immutable facts as
// DeriveComplexExecutionPhase and therefore cannot make a task complete.
func DeriveComplexExecutionMissingEvidence(snapshot ComplexExecutionSnapshot) []string {
	if blocked, reason := PlannerRuntimeBarrier(snapshot); blocked {
		return []string{string(reason)}
	}
	phase, _ := DeriveComplexExecutionPhase(snapshot)
	if _, failed := CurrentIntegrationCheckFailure(snapshot); failed {
		return []string{"INTEGRATION_CHECKS"}
	}
	if required, _ := RequirementFinalReviewRequired(snapshot.Run); required && complexExecutionAllTasksVerified(snapshot) && !requirementFinalReviewPassed(snapshot.FinalReview) {
		missing := []string{"REQUIREMENT_FINAL_REVIEW_PASS"}
		if !complexExecutionIntegrationChecksPassed(snapshot) {
			missing = append([]string{"INTEGRATION_CHECKS"}, missing...)
		}
		return missing
	}
	switch phase {
	case ComplexExecutionCompleted, ComplexExecutionNeedsHuman:
		return []string{}
	case ComplexExecutionPreparingTasks:
		return []string{"TASK_CONTRACT_MATERIALIZATION"}
	case ComplexExecutionAwaitingSteward:
		return []string{"STEWARD_DECISION"}
	case ComplexExecutionBindingBuilder:
		return []string{"BUILDER_BINDING"}
	case ComplexExecutionAwaitingSpecialist:
		return []string{"SPECIALIST_PASS", "SPECIALIST_CHECK"}
	case ComplexExecutionAwaitingScope:
		return []string{"SCOPE_EXPANSION_DECISION"}
	case ComplexExecutionRecovering:
		return []string{"RECOVERY_ACTION"}
	case ComplexExecutionReadyToDispatch:
		return []string{"TASK_DISPATCH"}
	case ComplexExecutionReworking:
		return []string{"REWORK_CANDIDATE"}
	case ComplexExecutionComposing:
		return []string{"COMPOSITION_RESULT"}
	case ComplexExecutionIntegrating:
		if complexExecutionIntegrationChecksPassed(snapshot) {
			return []string{"INTEGRATION_RESULT"}
		}
		return []string{"INTEGRATION_CHECKS", "INTEGRATION_RESULT"}
	case ComplexExecutionBlocked:
		return []string{"EXECUTION_RECOVERY"}
	}

	for _, task := range snapshot.Tasks {
		if task.Status != DevelopmentTaskStatusRunning && task.Status != DevelopmentTaskStatusReview {
			continue
		}
		dispatch, ok := currentComplexExecutionDispatch(snapshot, task)
		if !ok || strings.TrimSpace(dispatch.CandidateCommitID) == "" {
			return []string{"CANDIDATE_COMMIT"}
		}
		missing := []string{}
		if !complexExecutionCheckPassed(snapshot, dispatch, CandidateCheckScope) {
			missing = append(missing, "SCOPE_CHECK")
		}
		if complexExecutionGeneratedProofMissing(snapshot, task, dispatch) {
			missing = append(missing, "GENERATED_PROOF")
		}
		if !complexExecutionCheckPassed(snapshot, dispatch, CandidateCheckRequired) {
			missing = append(missing, "REQUIRED_CHECKS")
		}
		if !complexExecutionReviewPassed(snapshot, dispatch) {
			missing = append(missing, "REVIEWER_PASS")
		}
		if len(missing) == 0 && !ComplexExecutionTaskVerified(snapshot, task) {
			missing = append(missing, "CANDIDATE_VERIFICATION")
		}
		return missing
	}
	return []string{"EXECUTION_PROGRESS"}
}

func complexExecutionIntegrationChecksPassed(snapshot ComplexExecutionSnapshot) bool {
	var finalTask ComplexExecutionTask
	found := false
	for _, task := range snapshot.Tasks {
		if !ComplexExecutionTaskVerified(snapshot, task) || (found && task.Ordinal <= finalTask.Ordinal) {
			continue
		}
		finalTask, found = task, true
	}
	if !found {
		return false
	}
	dispatch, ok := currentComplexExecutionDispatch(snapshot, finalTask)
	return ok && complexExecutionCheckPassed(snapshot, dispatch, CandidateCheckIntegration)
}

func currentComplexExecutionDispatch(snapshot ComplexExecutionSnapshot, task ComplexExecutionTask) (ComplexExecutionDispatch, bool) {
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.ID == task.CurrentDispatchID && dispatch.ComplexExecutionTaskID == task.ID && dispatch.Round == task.CurrentRound {
			return dispatch, true
		}
	}
	return ComplexExecutionDispatch{}, false
}

func complexExecutionCheckPassed(snapshot ComplexExecutionSnapshot, dispatch ComplexExecutionDispatch, kind CandidateCheckKind) bool {
	found := false
	for _, spec := range snapshot.CheckSpecs {
		if spec.Kind != kind || (kind != CandidateCheckIntegration && spec.ComplexExecutionTaskID != dispatch.ComplexExecutionTaskID) {
			continue
		}
		found = true
		passed := false
		for _, run := range snapshot.CheckRuns {
			if run.CheckSpecFactID == spec.ID && run.DispatchID == dispatch.ID && run.CandidateCommitID == dispatch.CandidateCommitID && run.Status == ComplexExecutionCheckRunSettled && run.Result == EvidenceResultPass {
				passed = true
				break
			}
		}
		if !passed {
			return false
		}
	}
	return found
}

func complexExecutionGeneratedProofMissing(snapshot ComplexExecutionSnapshot, task ComplexExecutionTask, dispatch ComplexExecutionDispatch) bool {
	if snapshot.Exception == nil {
		return false
	}
	for _, command := range snapshot.Exception.GeneratedCommands {
		if command.ComplexExecutionTaskID != task.ID {
			continue
		}
		covered := false
		for _, proof := range snapshot.Exception.GeneratedProofs {
			if proof.CommandFactID == command.ID && proof.DispatchID == dispatch.ID && proof.CandidateCommitID == dispatch.CandidateCommitID && proof.Status == "SETTLED" && proof.Result == EvidenceResultPass {
				covered = true
				break
			}
		}
		if !covered {
			return true
		}
	}
	return false
}

func complexExecutionReviewPassed(snapshot ComplexExecutionSnapshot, dispatch ComplexExecutionDispatch) bool {
	for _, review := range snapshot.Reviews {
		if review.DispatchID == dispatch.ID && review.CandidateCommitID == dispatch.CandidateCommitID && review.Status == LocalReviewStatusSettled && review.Verdict == LocalReviewPass {
			return true
		}
	}
	return false
}

// NextEligibleComplexStandardTask returns the first plan-order task whose
// dependencies have a current verified candidate. It never schedules a second
// task while a Builder, Reviewer, or rework round is active.
func NextEligibleComplexStandardTask(snapshot ComplexExecutionSnapshot) (ComplexExecutionTask, bool) {
	if snapshot.Integration != nil || hasActiveComplexExecutionTask(snapshot) {
		return ComplexExecutionTask{}, false
	}
	tasks := append([]ComplexExecutionTask(nil), snapshot.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Ordinal != tasks[j].Ordinal {
			return tasks[i].Ordinal < tasks[j].Ordinal
		}
		return tasks[i].TaskKey < tasks[j].TaskKey
	})
	byKey := make(map[string]ComplexExecutionTask, len(tasks))
	for _, task := range tasks {
		if task.TaskKey == "" {
			return ComplexExecutionTask{}, false
		}
		if _, exists := byKey[task.TaskKey]; exists {
			return ComplexExecutionTask{}, false
		}
		byKey[task.TaskKey] = task
	}
	for _, task := range tasks {
		if task.Status != DevelopmentTaskStatusPlanned {
			continue
		}
		ready := true
		for _, dependencyKey := range task.DependencyTaskKeys {
			dependency, exists := byKey[dependencyKey]
			if !exists || !ComplexExecutionTaskVerified(snapshot, dependency) {
				ready = false
				break
			}
		}
		if ready {
			return task, true
		}
	}
	return ComplexExecutionTask{}, false
}

// ComplexExecutionTaskVerified requires a verification for the task's current
// dispatch and round. A record for an older rework round can never unlock a
// successor.
func ComplexExecutionTaskVerified(snapshot ComplexExecutionSnapshot, task ComplexExecutionTask) bool {
	if task.Status != DevelopmentTaskStatusReview || strings.TrimSpace(task.CurrentDispatchID) == "" || task.CurrentRound < 0 {
		return false
	}
	for _, verification := range snapshot.Verifications {
		if verification.ExecutionRunID != snapshot.Run.ID || verification.ComplexExecutionTaskID != task.ID ||
			verification.DispatchID != task.CurrentDispatchID || verification.Round != task.CurrentRound ||
			strings.TrimSpace(verification.CandidateCommitID) == "" || !validComplexExecutionCommitSHA(verification.CandidateCommitSHA) ||
			strings.TrimSpace(verification.ScopeEvidenceID) == "" || strings.TrimSpace(verification.LocalReviewID) == "" ||
			len(verification.RequiredCheckRunIDs) == 0 {
			continue
		}
		return true
	}
	return false
}

func hasActiveComplexExecutionTask(snapshot ComplexExecutionSnapshot) bool {
	for _, task := range snapshot.Tasks {
		switch task.Status {
		case DevelopmentTaskStatusRunning, DevelopmentTaskStatusRework:
			return true
		case DevelopmentTaskStatusReview:
			if !ComplexExecutionTaskVerified(snapshot, task) {
				return true
			}
		}
	}
	return false
}

// deriveComplexParallelPhase derives the PARALLEL run phase from the frozen
// batches. A wave only advances after its composition settles; a blocked
// composition keeps every earlier fact readable but fails closed.
func deriveComplexParallelPhase(snapshot ComplexExecutionSnapshot) (ComplexExecutionPhase, ReasonCode) {
	run := snapshot.Run
	boundBuilders := 0
	for _, binding := range snapshot.RoleBindings {
		if binding.Role == StandardRoleBuilder && binding.Status == RoleBindingStatusBound {
			boundBuilders++
		}
	}
	if boundBuilders < run.FixedBuilderCount {
		return ComplexExecutionBindingBuilder, ReasonNone
	}
	batches := append([]ComplexExecutionBatch(nil), snapshot.Batches...)
	sort.SliceStable(batches, func(i, j int) bool { return batches[i].Ordinal < batches[j].Ordinal })
	for _, batch := range batches {
		for _, composition := range snapshot.Compositions {
			if composition.BatchID != batch.ID {
				continue
			}
			if composition.Status == ComplexExecutionCompositionBlocked || composition.Status == ComplexExecutionCompositionFailed {
				reason := composition.ReasonCode
				if reason == ReasonNone {
					reason = ReasonCompositionConflict
				}
				return ComplexExecutionBlocked, reason
			}
		}
		switch batch.Status {
		case ComplexExecutionBatchComposed:
			continue
		case ComplexExecutionBatchBlocked:
			reason := ReasonCompositionConflict
			for _, composition := range snapshot.Compositions {
				if composition.BatchID == batch.ID && composition.ReasonCode != ReasonNone {
					reason = composition.ReasonCode
				}
			}
			return ComplexExecutionBlocked, reason
		case ComplexExecutionBatchComposing:
			return ComplexExecutionComposing, ReasonNone
		case ComplexExecutionBatchPending:
			return ComplexExecutionReadyToDispatch, ReasonNone
		}
		for _, task := range snapshot.Tasks {
			if !complexExecutionTaskInBatch(batch, task.TaskKey) {
				continue
			}
			switch task.Status {
			case DevelopmentTaskStatusNeedsHuman:
				return ComplexExecutionNeedsHuman, ReasonHumanDecisionRequired
			case DevelopmentTaskStatusBlocked, DevelopmentTaskStatusCancelled:
				if dispatch, ok := currentComplexExecutionDispatch(snapshot, task); ok && dispatch.ReasonCode != ReasonNone {
					return ComplexExecutionBlocked, dispatch.ReasonCode
				}
				return ComplexExecutionBlocked, ReasonComplexExecutionStopped
			case DevelopmentTaskStatusRunning:
				return ComplexExecutionBuilding, ReasonNone
			case DevelopmentTaskStatusPlanned:
				return ComplexExecutionReadyToDispatch, ReasonNone
			case DevelopmentTaskStatusRework:
				return ComplexExecutionReworking, ReasonNone
			case DevelopmentTaskStatusReview:
				if !ComplexExecutionTaskVerified(snapshot, task) {
					return ComplexExecutionReviewing, ReasonNone
				}
			}
		}
		return ComplexExecutionComposing, ReasonNone
	}
	return ComplexExecutionIntegrating, ReasonNone
}

func complexExecutionTaskInBatch(batch ComplexExecutionBatch, taskKey string) bool {
	for _, key := range batch.TaskKeys {
		if key == taskKey {
			return true
		}
	}
	return false
}

// ComplexExecutionFinalComposition returns the composition of the last batch.
// It is the only commit integration checks may run against in PARALLEL mode.
func ComplexExecutionFinalComposition(snapshot ComplexExecutionSnapshot) (ComplexExecutionComposition, bool) {
	batches := append([]ComplexExecutionBatch(nil), snapshot.Batches...)
	if len(batches) == 0 {
		return ComplexExecutionComposition{}, false
	}
	sort.SliceStable(batches, func(i, j int) bool { return batches[i].Ordinal < batches[j].Ordinal })
	final := batches[len(batches)-1]
	for _, composition := range snapshot.Compositions {
		if composition.BatchID == final.ID && composition.Status == ComplexExecutionCompositionComposed {
			return composition, true
		}
	}
	return ComplexExecutionComposition{}, false
}

func complexExecutionAllTasksVerified(snapshot ComplexExecutionSnapshot) bool {
	if len(snapshot.Tasks) == 0 {
		return false
	}
	for _, task := range snapshot.Tasks {
		if !ComplexExecutionTaskVerified(snapshot, task) {
			return false
		}
	}
	return true
}

func validComplexExecutionCommitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// CurrentIntegrationCheckFailure ignores older candidates and replaced attempts.
func CurrentIntegrationCheckFailure(snapshot ComplexExecutionSnapshot) (ComplexExecutionCheckRun, bool) {
	if _, project, err := ProjectContractFromRun(snapshot.Run); err != nil || !project {
		return ComplexExecutionCheckRun{}, false
	}
	if snapshot.Run.Mode != WorkModeStandard || snapshot.Run.CompletedAt != nil || !complexExecutionAllTasksVerified(snapshot) {
		return ComplexExecutionCheckRun{}, false
	}
	var final ComplexExecutionTask
	for _, task := range snapshot.Tasks {
		if task.ReworkCount > task.CurrentRound && !StoppedCheckRecoveryRetainsReworkCount(snapshot, task) {
			return ComplexExecutionCheckRun{}, false
		}
		if final.ID == "" || task.Ordinal > final.Ordinal {
			final = task
		}
	}
	for _, spec := range snapshot.CheckSpecs {
		if spec.Kind != CandidateCheckIntegration {
			continue
		}
		var latest ComplexExecutionCheckRun
		for _, check := range snapshot.CheckRuns {
			if check.DispatchID != final.CurrentDispatchID || check.CheckSpecFactID != spec.ID {
				continue
			}
			if latest.ID == "" || check.RetryOrdinal > latest.RetryOrdinal || check.RetryOrdinal == latest.RetryOrdinal && check.CreatedAt.After(latest.CreatedAt) {
				latest = check
			}
		}
		for _, verification := range snapshot.Verifications {
			if verification.DispatchID != final.CurrentDispatchID || verification.Round != final.CurrentRound || verification.CandidateCommitID != latest.CandidateCommitID || verification.CandidateCommitSHA != latest.CandidateCommitSHA {
				continue
			}
			if latest.SettledAt != nil && (latest.Status == ComplexExecutionCheckRunFailed || latest.Status == ComplexExecutionCheckRunSettled && latest.Result == EvidenceResultFail) {
				return latest, true
			}
		}
	}
	return ComplexExecutionCheckRun{}, false
}
