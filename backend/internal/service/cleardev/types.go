// Package cleardev is the daemon-owned ClearDev control service. It is the
// only production entry to requirement, task, candidate and evidence writes.
package cleardev

import (
	"context"
	"log/slog"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
)

// CreateRequirementInput is the complete public create contract shared by
// HTTP and the thin CLI. Task planning deliberately happens later.
type CreateRequirementInput struct {
	AOProjectID     string `json:"aoProjectId" description:"Registered single-repository AO project identifier."`
	Name            string `json:"name" description:"Human-readable development requirement name."`
	RequirementText string `json:"requirementText" description:"Initial requirement source text."`
}

// CreateDevelopmentTaskInput is an internal control-plane input. It is never
// exposed by the S01 HTTP or CLI surface.
type CreateDevelopmentTaskInput struct {
	Title          string
	Mode           core.WorkMode
	MaxReworkCount int
	Permissions    CreatePathPermissionsInput
	RequiredChecks []CreateRequiredCheckInput
}

// CreatePathPermissionsInput contains the four frozen path rule classes.
type CreatePathPermissionsInput struct {
	WritePaths                 []string
	GeneratedPaths             []string
	SharedPathsRequireApproval []string
	ForbiddenPaths             []string
}

// CreateRequiredCheckInput names one required check and its category.
type CreateRequiredCheckInput struct {
	Name string
	Kind string
}

// RequirementView is the answer-first read model. The overall progress is
// recomputed from durable facts on every read.
type RequirementView struct {
	MessageBudget             *core.MessageBudgetView        `json:"messageBudget"`
	Requirement               core.DevelopmentRequirement    `json:"requirement"`
	RequirementVersions       []core.RequirementVersion      `json:"requirementVersions"`
	DevelopmentTasks          []DevelopmentTaskView          `json:"developmentTasks"`
	OverallProgress           core.OverallProgress           `json:"overallProgress"`
	IntegrationCandidates     []core.IntegrationCandidate    `json:"integrationCandidates"`
	RequirementEvidence       []core.EvidenceRecord          `json:"requirementEvidence"`
	Events                    []core.RequirementEvent        `json:"events"`
	StandardFlow              *core.StandardFlowSnapshot     `json:"standardFlow,omitempty"`
	StandardFlowStatus        *StandardFlowStatus            `json:"standardFlowStatus,omitempty"`
	ComplexPlanning           *ComplexPlanningView           `json:"complexPlanning,omitempty"`
	ComplexExecution          *core.ComplexExecutionSnapshot `json:"complexExecution,omitempty"`
	QuickExecution            *core.ComplexQuickSnapshot     `json:"quickExecution,omitempty"`
	DirectionChange           *DirectionChangeView           `json:"directionChange,omitempty"`
	TrustedProgress           core.TrustedProgressSummary    `json:"trustedProgress"`
	LatestControlledPreflight *core.ControlledPreflightView  `json:"latestControlledPreflight,omitempty"`
	AgentStepAttempts         []core.AgentStepAttemptView    `json:"agentStepAttempts"`
}

// ProjectProgressView is the AO-project progress list. It is derived on read.
type ProjectProgressView struct {
	AOProjectID  string                        `json:"aoProjectId"`
	Requirements []core.TrustedProgressSummary `json:"requirements"`
}

// ProposeDirectionIntentInput is the public S05 entry. It cannot approve,
// reject, cancel, or confirm anything.
type ProposeDirectionIntentInput struct {
	RequestID                string `json:"requestId" description:"Idempotent direction-intent identifier."`
	DevelopmentRequirementID string `json:"developmentRequirementId" description:"Exact ClearDev development requirement identifier."`
	Message                  string `json:"message" description:"Non-empty user direction message handed to the original Steward."`
}

// DirectionChangeView is derived from durable S05 facts on every read.
type DirectionChangeView struct {
	Phase               core.DirectionChangePhase           `json:"phase"`
	Intent              *core.DirectionIntent               `json:"intent,omitempty"`
	Request             *core.DirectionRequest              `json:"request,omitempty"`
	Gate                *core.DirectionStopGate             `json:"gate,omitempty"`
	Snapshots           []core.DirectionTaskSnapshotItem    `json:"snapshots,omitempty"`
	Processings         []core.DirectionTaskProcessing      `json:"processings,omitempty"`
	Checkpoints         []core.DirectionCheckpoint          `json:"checkpoints,omitempty"`
	Revision            *core.DirectionRevision             `json:"revision,omitempty"`
	TargetVersion       *core.RequirementVersion            `json:"targetVersion,omitempty"`
	DecisionRequestID   string                              `json:"decisionRequestId,omitempty"`
	CompilationRequests []core.ComplexCompilationRequest    `json:"compilationRequests,omitempty"`
	AgentSteps          []core.AgentStep                    `json:"agentSteps,omitempty"`
	Questions           []core.ComplexClarificationQuestion `json:"questions,omitempty"`
	Answers             []core.ComplexClarificationAnswer   `json:"answers,omitempty"`
}

// CreateComplexRequirementInput is the independent S04 create contract. The
// original PRD is not a requirement version.
type CreateComplexRequirementInput struct {
	AOProjectID string `json:"aoProjectId" description:"Registered single-repository AO project identifier."`
	Name        string `json:"name" description:"Human-readable development requirement name."`
	PRDText     string `json:"prdText" description:"Original complex product-requirements text. It is not a requirement version."`
}

// SubmitComplexClarificationsInput binds answers to one unanswered round.
type SubmitComplexClarificationsInput struct {
	CompilationRequestID string                            `json:"compilationRequestId" description:"Exact unanswered compilation request identifier."`
	ClarificationRound   int                               `json:"clarificationRound" description:"Clarification round bound to that request."`
	Answers              []ComplexClarificationAnswerInput `json:"answers"`
}

// ComplexClarificationAnswerInput is one user answer for one question key.
type ComplexClarificationAnswerInput struct {
	QuestionKey string `json:"questionKey" description:"Question key from the unanswered compilation request."`
	Text        string `json:"text" description:"Trimmed non-empty answer text."`
}

// ComplexPlanningView is derived from durable S04 facts on every read.
type ComplexPlanningView struct {
	Phase                core.ComplexPlanningPhase           `json:"phase" enum:"COMPILING,AWAITING_CLARIFICATION,AWAITING_CONFIRMATION,PLANNING,AWAITING_PLAN_REVIEW,APPROVED,VALIDATED,PROJECT_PLANNED,NEEDS_HUMAN,REJECTED"`
	ReasonCode           core.ReasonCode                     `json:"reasonCode,omitempty"`
	Requirement          core.ComplexRequirement             `json:"requirement"`
	RoleBindings         []core.ComplexRoleBinding           `json:"roleBindings"`
	AgentSteps           []core.AgentStep                    `json:"agentSteps"`
	CompilationRequests  []core.ComplexCompilationRequest    `json:"compilationRequests"`
	Questions            []core.ComplexClarificationQuestion `json:"questions"`
	Answers              []core.ComplexClarificationAnswer   `json:"answers"`
	Compilations         []core.ComplexCompilation           `json:"compilations"`
	IDMaps               []core.ComplexIDMap                 `json:"idMaps"`
	Plans                []core.ComplexEngineeringPlan       `json:"plans"`
	Reviews              []core.ComplexPlanReview            `json:"reviews"`
	Validations          []core.ComplexPlanValidation        `json:"validations"`
	PlannerClarification *core.PlannerClarificationView      `json:"plannerClarification,omitempty"`
	PlannerAnswerHistory []core.PlannerClarificationAnswer   `json:"plannerAnswerHistory"`
	ProductClarification *core.ComplexPlannerClarification   `json:"productClarification,omitempty"`
}

// StandardFlowAttention is the S02 workflow-specific attention state. REPLAN
// is deliberately separate from the S01 task attention enum because it can be
// derived before any development task exists.
type StandardFlowAttention string

const (
	StandardFlowAttentionNone              StandardFlowAttention = "NONE"
	StandardFlowAttentionReplan            StandardFlowAttention = "REPLAN"
	StandardFlowAttentionNeedsHuman        StandardFlowAttention = "NEEDS_HUMAN"
	StandardFlowAttentionBlocked           StandardFlowAttention = "BLOCKED"
	StandardFlowAttentionRework            StandardFlowAttention = "REWORK"
	StandardFlowAttentionIntegrationFailed StandardFlowAttention = "INTEGRATION_FAILED"
)

// StandardFlowStatus is derived on every requirement read. It never becomes a
// second persisted lifecycle state and cannot change S01 OverallProgress.
type StandardFlowStatus struct {
	Phase      core.OverallPhase     `json:"phase" enum:"CANCELLED,AWAITING_CONFIRMATION,DEFINING_REQUIREMENT,PLANNING_TASKS,DEVELOPING,VERIFYING,INTEGRATING,COMPLETED"`
	Attention  StandardFlowAttention `json:"attention" enum:"NONE,REPLAN,NEEDS_HUMAN,BLOCKED,REWORK,INTEGRATION_FAILED"`
	ReasonCode core.ReasonCode       `json:"reasonCode,omitempty"`
}

// DevelopmentTaskView groups durable task facts with its current evidence
// gate. It is part of the read response but has no public write endpoint.
type DevelopmentTaskView struct {
	DevelopmentTask    core.DevelopmentTask     `json:"developmentTask"`
	PermissionVersions []core.PermissionVersion `json:"permissionVersions"`
	RequiredChecks     []core.RequiredCheck     `json:"requiredChecks"`
	Candidates         []core.CandidateCommit   `json:"candidates"`
	CurrentCandidate   *core.CandidateCommit    `json:"currentCandidate,omitempty"`
	Evidence           []core.EvidenceRecord    `json:"evidence"`
	EffectiveEvidence  []core.EvidenceRecord    `json:"effectiveEvidence"`
	MissingEvidence    []string                 `json:"missingEvidence"`
}

// ClearDevHTTPService is the deliberately small public ClearDev HTTP surface.
type ClearDevHTTPService interface {
	CreateRequirement(context.Context, CreateRequirementInput) (RequirementView, error)
	CreateComplexRequirement(context.Context, CreateComplexRequirementInput) (RequirementView, error)
	GetRequirement(context.Context, string) (RequirementView, error)
	SubmitComplexClarifications(context.Context, string, SubmitComplexClarificationsInput) (RequirementView, error)
	StartStandardFlow(context.Context, string) (RequirementView, error)
	StartComplexStandardExecution(context.Context, string) (RequirementView, error)
	ProposeDirectionIntent(context.Context, string, ProposeDirectionIntentInput) (RequirementView, error)
	ListProjectProgress(context.Context, string) (ProjectProgressView, error)
	RequestProgressExplanation(context.Context, string) (RequirementView, error)
	RequestDevelopmentTaskRework(context.Context, string, string) error
	RequestExtraReviewBudget(context.Context, string, string) (RequirementView, error)
	RequestExtraBuilderTurn(context.Context, string, string) (RequirementView, error)
	RequestControlledEngineChange(context.Context, string, core.ControlledEngineChangeBinding) (RequirementView, error)
}

// StandardFactStore is the S02-only durable workflow boundary. Every method
// either appends an immutable fact or performs one narrow compare-and-swap.
// It stays separate from FactStore so S01 callers and tests do not gain these
// internal workflow writes accidentally.
type StandardFactStore interface {
	GetClearDevStandardFlow(context.Context, string) (core.StandardFlowSnapshot, bool, error)
	ListClearDevRunnableStandardFlows(context.Context) ([]string, error)
	StartClearDevStandardFlow(context.Context, core.StartStandardFlowCommand) (core.RoleSessionBinding, bool, error)
	CreateClearDevRoleBinding(context.Context, core.CreateRoleBindingCommand) (core.RoleSessionBinding, bool, error)
	BindClearDevRoleBinding(context.Context, string, string, time.Time) (bool, error)
	FailClearDevRoleBinding(context.Context, core.FailRoleBindingCommand) (bool, error)
	CreateClearDevStandardAgentStep(context.Context, core.AgentStep) (core.AgentStep, bool, error)
	MarkClearDevStandardAgentStepSent(context.Context, string, time.Time) (bool, error)
	SettleClearDevStandardAgentStep(context.Context, core.AgentStep) (bool, error)
	CreateClearDevEngineeringPlan(context.Context, core.EngineeringPlan) error
	RecordClearDevPlanReview(context.Context, core.PlanReview) error
	CreateClearDevPendingDispatch(context.Context, core.CreatePendingDispatchCommand) (core.Dispatch, bool, error)
	SettleClearDevStandardDispatchFailure(context.Context, string, core.DispatchStatus, core.ReasonCode, time.Time) (bool, error)
	AcceptClearDevStandardDispatch(context.Context, core.DispatchAcceptance) error
	AppendClearDevStandardCandidate(context.Context, core.StandardCandidateObservation) (core.CandidateCommit, error)
	CreateClearDevCandidateCheckRun(context.Context, core.CandidateCheckRun) (core.CandidateCheckRun, bool, error)
	SettleClearDevCandidateCheckRun(context.Context, core.SettleCandidateCheckRunCommand) (bool, error)
	CreateClearDevLocalReview(context.Context, core.LocalReview) (core.LocalReview, bool, error)
	SettleClearDevLocalReview(context.Context, core.SettleLocalReviewCommand) (bool, error)
	AppendClearDevStandardEvidence(context.Context, core.StandardEvidenceRecord) error
	ApplyClearDevStandardFailure(context.Context, core.StandardFailureCommand) error
	ApplyClearDevStandardBuilderOutcome(context.Context, core.StandardBuilderOutcomeCommand) error
	ApplyClearDevStandardReviewerBindingFailure(context.Context, core.StandardReviewerBindingFailureCommand) error
	CompleteClearDevStandardTask(context.Context, core.CompleteStandardTaskCommand) error
}

// ComplexFactStore is the S04-only durable workflow boundary. It never writes
// STANDARD single-task, dispatch, or development-task facts.
type ComplexFactStore interface {
	CreateClearDevComplexRequirement(context.Context, core.CreateComplexRequirementCommand) error
	GetClearDevComplexPlanning(context.Context, string) (core.ComplexPlanningSnapshot, bool, error)
	ListClearDevRunnableComplexFlows(context.Context) ([]string, error)
	CreateClearDevComplexRoleBinding(context.Context, core.CreateComplexRoleBindingCommand) (core.ComplexRoleBinding, bool, error)
	BindClearDevComplexRoleBinding(context.Context, string, string, time.Time) (bool, error)
	FailClearDevComplexRoleBinding(context.Context, core.FailComplexRoleBindingCommand) (bool, error)
	CreateClearDevComplexAgentStep(context.Context, core.AgentStep) (core.AgentStep, bool, error)
	MarkClearDevComplexAgentStepSent(context.Context, string, time.Time) (bool, error)
	SettleClearDevComplexAgentStep(context.Context, core.AgentStep) (bool, error)
	RecordClearDevComplexClarification(context.Context, core.RecordComplexClarificationCommand) error
	SubmitClearDevComplexClarificationAnswers(context.Context, core.SubmitComplexClarificationCommand) error
	SettleClearDevComplexCompilation(context.Context, core.SettleComplexCompilationCommand) error
	CreateClearDevComplexEngineeringPlan(context.Context, core.CreateComplexPlanCommand) error
	RecordClearDevComplexPlanReview(context.Context, core.RecordComplexPlanReviewCommand) error
}

// PlannerRuntimeFactStore freezes requests and applies actual settled replies
// inside the existing execution store; it exposes no approval or terminal API.
type PlannerRuntimeFactStore interface {
	PrepareClearDevPlannerRuntime(context.Context, string, time.Time) (core.PlannerCoordinationRequest, bool, error)
	ApplyClearDevPlannerRuntime(context.Context, string, time.Time) (bool, error)
	StopClearDevPlannerRuntime(context.Context, string, core.ReasonCode, string, time.Time) (bool, error)
}

// ComplexExecutionFactStore is the S06-only durable boundary. It deliberately
// does not expose S01/S02 task transitions: every mutating method owns the
// execution-run, task-set, candidate-round, and evidence binding checks.
type ComplexExecutionFactStore interface {
	GetClearDevComplexExecution(context.Context, string) (core.ComplexExecutionSnapshot, bool, error)
	ListClearDevRunnableComplexExecutions(context.Context) ([]string, error)
	StartClearDevComplexExecution(context.Context, core.StartComplexExecutionCommand) (core.ComplexExecutionRun, bool, error)
	CreateClearDevComplexExecutionRoleBinding(context.Context, core.ComplexExecutionRoleBinding) (core.ComplexExecutionRoleBinding, bool, error)
	ContinueClearDevComplexExecutionSteward(context.Context, string, core.ComplexExecutionRoleBinding, core.AgentStep, time.Time) (bool, error)
	BindClearDevComplexExecutionRoleBinding(context.Context, string, string, string, string, time.Time) (bool, error)
	FailClearDevComplexExecutionRoleBinding(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	EndClearDevComplexExecutionRoleBinding(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	CreateClearDevComplexExecutionAgentStep(context.Context, core.AgentStep) (core.AgentStep, bool, error)
	MarkClearDevComplexExecutionAgentStepSent(context.Context, string, time.Time) (bool, error)
	SettleClearDevComplexExecutionAgentStep(context.Context, core.AgentStep) (bool, error)
	FailClearDevComplexExecutionAgentStep(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	SettleClearDevComplexExecutionRequest(context.Context, string, core.ComplexExecutionDecision, core.ReasonCode, time.Time) error
	MaterializeClearDevComplexExecution(context.Context, core.MaterializeComplexExecutionCommand) error
	OccupyClearDevComplexExceptionBudget(context.Context, string, string, string, time.Time) (bool, error)
	AuthorizeExtraReviewBudget(context.Context, string) error
	AuthorizeExtraBuilderTurn(context.Context, string) error
	RecordClearDevComplexExceptionScopeRequest(context.Context, core.ComplexScopeExpansionRequest) error
	AcceptClearDevComplexExceptionScope(context.Context, core.ComplexScopeExpansionDecision, core.PermissionVersion, time.Time) error
	RejectClearDevComplexExceptionScope(context.Context, core.ComplexScopeExpansionDecision, time.Time) error
	CreateClearDevComplexExceptionAgentStep(context.Context, string, string, string, core.AgentStep) (core.AgentStep, bool, error)
	MarkClearDevComplexExceptionAgentStepSent(context.Context, string, time.Time) (bool, error)
	SettleClearDevComplexExceptionAgentStep(context.Context, core.AgentStep) (bool, error)
	FailClearDevComplexExceptionAgentStep(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	CreateClearDevComplexExceptionOnDemandBinding(context.Context, core.ComplexOnDemandBinding) (core.ComplexOnDemandBinding, bool, error)
	BindClearDevComplexExceptionOnDemand(context.Context, string, string, string, string, time.Time) (bool, error)
	FailClearDevComplexExceptionOnDemand(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	EndClearDevComplexExceptionOnDemand(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	RecordClearDevComplexExceptionSpecialist(context.Context, core.ComplexSpecialistResult, []core.ComplexSpecialistCheck) error
	StartClearDevComplexExceptionSpecialistCheck(context.Context, string) (bool, error)
	SettleClearDevComplexExceptionSpecialistCheck(context.Context, core.ComplexSpecialistCheck, time.Time) (bool, error)
	RecordClearDevComplexExceptionGeneratedProof(context.Context, core.ComplexGeneratedProof) error
	StartClearDevComplexExceptionGeneratedProof(context.Context, string) (bool, error)
	SettleClearDevComplexExceptionGeneratedProof(context.Context, core.ComplexGeneratedProof, time.Time) (bool, error)
	RecordClearDevComplexExceptionRecovery(context.Context, core.ComplexRecoveryAction) error
	CreateClearDevComplexExecutionDispatch(context.Context, core.CreateComplexExecutionDispatchCommand) (core.ComplexExecutionDispatch, bool, error)
	AppendClearDevComplexExecutionCandidate(context.Context, core.AppendComplexExecutionCandidateCommand) (core.CandidateCommit, error)
	CreateClearDevComplexExecutionCheckRun(context.Context, core.ComplexExecutionCheckRun) (core.ComplexExecutionCheckRun, bool, error)
	StartClearDevComplexExecutionCheckRun(context.Context, string, time.Time) (bool, error)
	SettleClearDevComplexExecutionCheckRun(context.Context, core.SettleComplexExecutionCheckCommand) (bool, error)
	CreateClearDevComplexExecutionReview(context.Context, core.CreateComplexExecutionReviewCommand) (core.ComplexExecutionReview, bool, error)
	SettleClearDevComplexExecutionReview(context.Context, core.SettleComplexExecutionReviewCommand) (bool, error)
	ApplyClearDevComplexExecutionFailure(context.Context, string, string, string, bool, core.ReasonCode, time.Time) error
	VerifyClearDevComplexExecutionCandidate(context.Context, core.VerifyComplexExecutionCandidateCommand) error
	CompleteClearDevComplexExecution(context.Context, core.CompleteComplexExecutionCommand) error
	ActivateClearDevComplexExecutionBatch(context.Context, string, string, string, time.Time) error
	RecordClearDevComplexExecutionComposition(context.Context, core.ComplexExecutionComposition) error
	StartClearDevComplexExecutionComposition(context.Context, string) error
	SettleClearDevComplexExecutionComposition(context.Context, core.ComplexExecutionComposition) error
	RebaseClearDevComplexExecutionBuilder(context.Context, string, string) error
	GetClearDevComplexQuickExecution(context.Context, string) (core.ComplexQuickSnapshot, bool, error)
	ListClearDevRunnableComplexQuickExecutions(context.Context) ([]string, error)
	StartClearDevComplexQuickExecution(context.Context, core.StartComplexQuickExecutionCommand) (core.ComplexQuickRun, bool, error)
	CreateClearDevComplexQuickRoleBinding(context.Context, core.ComplexExecutionRoleBinding) (core.ComplexExecutionRoleBinding, bool, error)
	ContinueClearDevComplexQuickSteward(context.Context, string, core.ComplexExecutionRoleBinding, core.AgentStep, time.Time) (bool, error)
	BindClearDevComplexQuickRoleBinding(context.Context, string, string, string, string, time.Time) (bool, error)
	FailClearDevComplexQuickRoleBinding(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	EndClearDevComplexQuickRoleBinding(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	CreateClearDevComplexQuickAgentStep(context.Context, core.AgentStep) (core.AgentStep, bool, error)
	MarkClearDevComplexQuickAgentStepSent(context.Context, string, time.Time) (bool, error)
	SettleClearDevComplexQuickAgentStep(context.Context, core.AgentStep) (bool, error)
	FailClearDevComplexQuickAgentStep(context.Context, string, core.ReasonCode, time.Time) (bool, error)
	SettleClearDevComplexQuickRequest(context.Context, string, core.ReasonCode, time.Time) error
	MaterializeClearDevComplexQuickExecution(context.Context, core.MaterializeComplexQuickExecutionCommand) error
	CreateClearDevComplexQuickDispatch(context.Context, core.CreateComplexExecutionDispatchCommand) (core.ComplexExecutionDispatch, bool, error)
	AppendClearDevComplexQuickCandidate(context.Context, core.AppendComplexExecutionCandidateCommand) (core.CandidateCommit, error)
	CreateClearDevComplexQuickCheckRun(context.Context, core.ComplexExecutionCheckRun) (core.ComplexExecutionCheckRun, bool, error)
	StartClearDevComplexQuickCheckRun(context.Context, string, time.Time) (bool, error)
	SettleClearDevComplexQuickCheckRun(context.Context, core.SettleComplexExecutionCheckCommand) (bool, error)
	ApplyClearDevComplexQuickFailure(context.Context, string, string, string, bool, core.ReasonCode, time.Time) error
	CompleteClearDevComplexQuickExecution(context.Context, core.CompleteComplexQuickExecutionCommand) error
}

// RequirementFinalReviewFactStore owns requirement-wide review facts. It is
// separate from task-local reviews and never exposes a caller-selected verdict.
type RequirementFinalReviewFactStore interface {
	CreateClearDevRequirementFinalReview(context.Context, core.RequirementFinalReview) error
	BindClearDevRequirementFinalReview(context.Context, string, string, string, time.Time) error
	MarkClearDevRequirementFinalReviewSent(context.Context, string, time.Time) error
	SettleClearDevRequirementFinalReview(context.Context, string, string, string, time.Time) error
	FailClearDevRequirementFinalReview(context.Context, string, core.ReasonCode, time.Time) error
}

// DirectionFactStore is the S05 durable boundary. Ordinary S01 callers cannot
// ignore an active stop gate through this interface.
type DirectionFactStore interface {
	GetClearDevDirectionChange(context.Context, string) (core.DirectionChangeSnapshot, bool, error)
	ListClearDevRunnableDirectionRequirements(context.Context) ([]string, error)
	HasActiveClearDevDirectionStop(context.Context, string) (bool, error)
	GetClearDevTaskDispatchFacts(context.Context, string) (string, string, string, error)
	GetClearDevDirectionCompilationRequest(context.Context, string) (core.ComplexCompilationRequest, bool, error)
	CreateClearDevDirectionIntent(context.Context, core.CreateDirectionIntentCommand) (core.DirectionIntent, bool, error)
	CreateClearDevDirectionAgentStep(context.Context, core.AgentStep) (core.AgentStep, bool, error)
	MarkClearDevDirectionAgentStepSent(context.Context, string, time.Time) (bool, error)
	SettleClearDevDirectionAgentStep(context.Context, core.AgentStep) (bool, error)
	AcceptClearDevDirectionChange(context.Context, core.AcceptDirectionChangeCommand) error
	OccupyClearDevDirectionTask(context.Context, core.OccupyDirectionTaskCommand) (bool, error)
	MarkClearDevDirectionInterruptUnknown(context.Context, string, string) error
	FinalizeClearDevDirectionTask(context.Context, core.FinalizeDirectionTaskCommand) error
	CreateClearDevDirectionRevision(context.Context, core.CreateDirectionRevisionCommand) error
	RecordClearDevDirectionClarification(context.Context, core.RecordDirectionClarificationCommand) error
	SubmitClearDevDirectionClarificationAnswers(context.Context, core.SubmitComplexClarificationCommand) error
	SettleClearDevDirectionCompilation(context.Context, core.SettleDirectionCompilationCommand) error
}

// StandardSessionService is deliberately the ordinary AO session service. The
// coordinator asks for a concrete mode/config and then verifies the returned
// durable record; it never trusts project defaults.
type StandardSessionService interface {
	Spawn(context.Context, ports.SpawnConfig) (domain.Session, int, int, error)
}

// AgentSessionRecovery resumes the exact AO session already bound to a
// controlled role. It must not create a replacement session.
type AgentSessionRecovery func(context.Context, domain.SessionID) error

// StandardChatService exposes the idempotent send and durable transcript read
// needed to prove exactly which completed turn produced a workflow result.
type StandardChatService interface {
	RelayChatTurnWithID(context.Context, domain.SessionID, string, string) (string, error)
	Snapshot(context.Context, domain.SessionID) (chatsvc.Snapshot, error)
	Interrupt(context.Context, domain.SessionID) error
}

// FactStore is the narrow persistence surface. Every mutating method owns one
// SQLite transaction and appends accepted or rejected events there.
type FactStore interface {
	CreateClearDevRequirement(context.Context, core.InitialRequirement) error
	GetClearDevRequirement(context.Context, string) (core.RequirementSnapshot, bool, error)
	GetClearDevTaskContext(context.Context, string) (core.DevelopmentTask, core.DevelopmentRequirement, bool, error)
	CreateClearDevRequirementVersion(context.Context, core.RequirementVersion) error
	CreateClearDevTask(context.Context, core.InitialDevelopmentTask) error
	AppendClearDevPermissionVersion(context.Context, core.PermissionVersion) error
	ApplyClearDevAction(context.Context, core.ActionRequest) error
	AppendClearDevCandidate(context.Context, core.CandidateObservation) (core.CandidateCommit, error)
	AppendClearDevIntegrationCandidate(context.Context, core.IntegrationCandidateObservation) (core.IntegrationCandidate, error)
	AppendClearDevEvidence(context.Context, core.EvidenceRecord) error
	ListClearDevRequirementIDsByAOProject(context.Context, string) ([]string, error)
}

// BenchmarkFactStore is the narrow S12B read boundary. Requirement creation
// still writes its optional binding atomically through FactStore's
// InitialRequirement command; ordinary test stores need not implement this.
type BenchmarkFactStore interface {
	GetClearDevBenchmarkBinding(context.Context, string) (core.BenchmarkBinding, bool, error)
}

// HumanDecisionStore is the S03 desktop-decision persistence surface. It is
// separate from FactStore so ordinary S01 callers cannot settle decisions.
type HumanDecisionStore interface {
	BackfillClearDevHumanDecisionRequests(context.Context) error
	ListPendingClearDevHumanDecisionRequests(context.Context) ([]core.HumanDecisionRequest, error)
	GetClearDevHumanDecisionRequest(context.Context, string) (core.HumanDecisionRequest, bool, error)
	GetClearDevHumanDecisionEffect(context.Context, string) (core.HumanDecisionEffect, bool, error)
	IssueClearDevHumanDecisionDispatch(context.Context, core.IssueHumanDecisionDispatchCommand) (core.HumanDecisionOffer, error)
	SettleClearDevHumanDecision(context.Context, core.HumanDecisionResult, time.Time) error
	DismissClearDevHumanDecisionDispatch(context.Context, core.DismissHumanDecisionDispatchCommand) error
	DismissOpenClearDevHumanDecisionDispatches(context.Context, string, core.HumanDecisionDispatchOutcome, time.Time) error
	CreateExtraReviewBudgetRequest(context.Context, core.DevelopmentRequirement, core.ExtraReviewBudgetBinding, time.Time) (string, error)
	CreateExtraBuilderTurnRequest(context.Context, core.DevelopmentRequirement, core.ExtraBuilderTurnBinding, time.Time) (string, error)
	CreateControlledEngineChangeRequest(context.Context, core.DevelopmentRequirement, core.ControlledEngineChangeBinding, time.Time) (string, error)
}

// ParseCorrectionStore records the one mechanical JSON correction per Agent step.
type ParseCorrectionStore interface {
	GetClearDevParseCorrection(context.Context, string) (core.ParseCorrection, bool, error)
	RecordClearDevParseCorrection(context.Context, core.ParseCorrection) error
}

// AgentAttemptStore owns append-only send, observation, result, and parse
// evidence. Existing step tables remain the workflow's business-state summary.
type AgentAttemptStore interface {
	ReserveClearDevAgentMessage(context.Context, core.ReserveAgentMessageCommand) (bool, error)
	ConfirmClearDevAgentMessage(context.Context, core.AgentAttemptEvent) error
	GetClearDevMessageBudget(context.Context, string) (core.MessageBudgetView, error)
	EnsureClearDevAgentStepAttempt(context.Context, core.AgentStepAttempt) (core.AgentStepAttempt, bool, error)
	EnsureClearDevAgentAttemptEvent(context.Context, core.AgentAttemptEvent) (bool, error)
	RecordClearDevAgentAttemptEvent(context.Context, core.AgentAttemptEvent) error
	RecordClearDevAgentStepResult(context.Context, core.AgentStepResult) error
	RecordClearDevAgentStepResultParse(context.Context, core.AgentStepResultParse) error
	ListClearDevAgentStepAttempts(context.Context, string) ([]core.AgentStepAttemptView, error)
	ListClearDevAgentStepAttemptStates(context.Context, string, string) ([]core.AgentStepAttemptView, error)
	ListLatestClearDevAgentAttemptStates(context.Context, string) ([]core.AgentStepAttemptView, error)
	GetClearDevAgentAttemptState(context.Context, string, string) (core.AgentStepAttemptView, error)
	GetClearDevAgentDeliveryState(context.Context, string, string, string) (core.AgentStepAttemptView, error)
}

// ControlledPreflightStore persists append-only session-start inspections.
type ControlledPreflightStore interface {
	RecordClearDevControlledPreflight(context.Context, core.ControlledPreflight) error
	GetLatestClearDevControlledPreflight(context.Context, string) (core.ControlledPreflight, bool, error)
	GetLatestClearDevControlledPreflightForBinding(context.Context, string) (core.ControlledPreflight, bool, error)
}

// ControlledPreflightChecker is the conversation-layer live catalog, login, and
// quota inspection. ClearDev only classifies its errors with errors.Is.
type ControlledPreflightChecker interface {
	CheckControlledPreflight(context.Context, domain.AgentHarness, string) (ports.ChatControlledPreflight, error)
}

// ProgressExplanationStore persists steward notes for one fact snapshot.
type ProgressExplanationStore interface {
	OccupyClearDevProgressExplanation(context.Context, core.OccupyProgressExplanationCommand) (core.ProgressExplanationRequest, bool, error)
	GetClearDevProgressExplanation(context.Context, string) (core.ProgressExplanationRequest, bool, error)
	GetClearDevProgressExplanationBySnapshot(context.Context, string, string) (core.ProgressExplanationRequest, bool, error)
	ListClearDevProgressExplanations(context.Context, string) ([]core.ProgressExplanationRequest, error)
	ListClearDevRunnableProgressExplanations(context.Context) ([]core.ProgressExplanationRequest, error)
	BindClearDevProgressExplanationSession(context.Context, string, string, string) (bool, error)
	MarkClearDevProgressExplanationSent(context.Context, string, time.Time) (bool, error)
	SettleClearDevProgressExplanation(context.Context, string, string, string, time.Time) (bool, error)
	FailClearDevProgressExplanation(context.Context, string, core.ReasonCode, time.Time) (bool, error)
}

// AOReader exposes only immutable project/session facts needed for binding.
type AOReader interface {
	GetProject(context.Context, string) (domain.ProjectRecord, bool, error)
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
}

// HumanDecisionAuthorizer is an internal typed capability. Production S01 does
// not expose it through the public API.
type HumanDecisionAuthorizer interface {
	Authorize(context.Context, HumanDecision) error
}

// HumanDecision contains no caller-asserted role; possession of the injected
// capability is the authority boundary.
type HumanDecision struct {
	Action                          core.Action
	RequirementVersionID            string
	ReplacementRequirementVersionID string
	DevelopmentRequirementID        string
	Reason                          string
}

// Deps are explicit so tests can inject deterministic time, ids and worktree
// observations without using Git or the wall clock.
type Deps struct {
	Facts                       FactStore
	BenchmarkFacts              BenchmarkFactStore
	StandardFacts               StandardFactStore
	ComplexFacts                ComplexFactStore
	ComplexExecutionFacts       ComplexExecutionFactStore
	RequirementFinalReviews     RequirementFinalReviewFactStore
	DirectionFacts              DirectionFactStore
	HumanDecisions              HumanDecisionStore
	DesktopRunID                string
	DesktopChannelConnected     func() bool
	ProgressExplanations        ProgressExplanationStore
	ParseCorrections            ParseCorrectionStore
	AgentAttempts               AgentAttemptStore
	ControlledPreflights        ControlledPreflightStore
	ControlledPreflightChecker  ControlledPreflightChecker
	AO                          AOReader
	Workspace                   ports.WorkspaceObserver
	Human                       HumanDecisionAuthorizer
	Sessions                    StandardSessionService
	RecoverAgentSession         AgentSessionRecovery
	RestoreOriginalAgentSession func(context.Context, domain.SessionID) (string, error)
	Chat                        StandardChatService
	Inspector                   ports.ClearDevCandidateInspector
	Checks                      ports.ClearDevCheckRunner
	ResultPreview               ResultPreviewManager
	BenchmarkManifest           *core.BenchmarkBackendManifest
	BackgroundContext           context.Context
	RunBackground               func(func())
	// AutoAdvanceComplexPlans is enabled by the normal daemon assembly. It
	// advances admitted Task Contracts or legacy reviewed single-task plans,
	// and makes completed starts read-only. Benchmark campaigns remain explicit.
	AutoAdvanceComplexPlans bool
	// PlannerTaskContracts enables V2 results for fresh Planner turns in the
	// normal daemon. Persisted V1 prompts/results retain their original route.
	PlannerTaskContracts bool
	// PlannerRuntimeCoordination opts fresh admitted runs into bounded Stage
	// coordination. Existing immutable run packages are never upgraded.
	PlannerRuntimeCoordination bool
	// AutomaticFailureRouting unifies safe native recovery and bounded role escalation.
	// Production enables this; no model or HTTP caller can use it to grant authority.
	AutomaticFailureRouting bool
	// BoundedMailAttempts freezes the current Sprint policy for new mail runs only.
	BoundedMailAttempts bool
	StepTimeout         time.Duration
	PollInterval        time.Duration
	Logger              *slog.Logger
	NewID               func() string
	Clock               func() time.Time
}
