package cleardev

import "time"

// StandardRole is one of the four fixed responsibilities in the S02 local
// STANDARD flow. Roles are durable facts, not permissions to mutate data.
type StandardRole string

const (
	StandardRoleSteward            StandardRole = "STEWARD"
	StandardRoleEngineeringPlanner StandardRole = "ENGINEERING_PLANNER"
	StandardRoleBuilder            StandardRole = "BUILDER"
	StandardRoleReviewer           StandardRole = "REVIEWER"
)

// S02-specific facts use the existing project-event stream so their accepted
// and rejected writes continue to drive cleardev_project_updated CDC.
const (
	SubjectStandardRoleBinding SubjectType = "STANDARD_ROLE_BINDING"
	SubjectStandardAgentStep   SubjectType = "STANDARD_AGENT_STEP"
	SubjectEngineeringPlan     SubjectType = "ENGINEERING_PLAN"
	SubjectPlanReview          SubjectType = "PLAN_REVIEW"
	SubjectDispatch            SubjectType = "DISPATCH"
	SubjectCandidateCheckRun   SubjectType = "CANDIDATE_CHECK_RUN"
	SubjectLocalReview         SubjectType = "LOCAL_REVIEW"

	ActionStartStandardFlow       Action = "START_STANDARD_FLOW"
	ActionBindStandardRole        Action = "BIND_STANDARD_ROLE"
	ActionCreateStandardAgentStep Action = "CREATE_STANDARD_AGENT_STEP"
	ActionSendStandardAgentStep   Action = "SEND_STANDARD_AGENT_STEP"
	ActionSettleStandardAgentStep Action = "SETTLE_STANDARD_AGENT_STEP"
	ActionCreateEngineeringPlan   Action = "CREATE_ENGINEERING_PLAN"
	ActionRecordPlanReview        Action = "RECORD_PLAN_REVIEW"
	ActionRequestDispatch         Action = "REQUEST_DISPATCH"
	ActionAcceptDispatch          Action = "ACCEPT_DISPATCH"
	ActionRejectDispatch          Action = "REJECT_DISPATCH"
	ActionRecordCandidateCheck    Action = "RECORD_CANDIDATE_CHECK_RUN"
	ActionRecordLocalReview       Action = "RECORD_LOCAL_REVIEW"
)

const (
	ReasonStandardFlowExists    ReasonCode = "STANDARD_FLOW_EXISTS"
	ReasonRoleBindingMismatch   ReasonCode = "ROLE_BINDING_MISMATCH"
	ReasonStepNotSettled        ReasonCode = "STEP_NOT_SETTLED"
	ReasonDispatchAlreadySet    ReasonCode = "DISPATCH_ALREADY_SETTLED"
	ReasonStalePlan             ReasonCode = "STALE_PLAN"
	ReasonTaskSetChanged        ReasonCode = "TASK_SET_CHANGED"
	ReasonBuilderSpawnFailed    ReasonCode = "BUILDER_SPAWN_FAILED"
	ReasonSessionMismatch       ReasonCode = "SESSION_MISMATCH"
	ReasonEvidenceSourceInvalid ReasonCode = "EVIDENCE_SOURCE_INVALID"
)

// RoleBindingStatus records whether the Control Plane has obtained the exact
// AO session that a role may use. A failed or ended binding is historical and
// cannot later be rebound.
type RoleBindingStatus string

const (
	RoleBindingStatusRequested RoleBindingStatus = "REQUESTED"
	RoleBindingStatusBound     RoleBindingStatus = "BOUND"
	RoleBindingStatusFailed    RoleBindingStatus = "FAILED"
	RoleBindingStatusEnded     RoleBindingStatus = "ENDED"
)

// AgentStepKind identifies one request/result pair permitted by the frozen
// S02 protocol. The value must match the protocol kind, not an Agent claim.
type AgentStepKind string

const (
	AgentStepRequestPlanning AgentStepKind = "REQUEST_PLANNING"
	AgentStepEngineeringPlan AgentStepKind = "ENGINEERING_PLAN"
	AgentStepPlanReview      AgentStepKind = "PLAN_REVIEW"
	AgentStepDispatchRequest AgentStepKind = "DISPATCH_REQUEST"
	AgentStepBuilderResult   AgentStepKind = "BUILDER_RESULT"
	AgentStepLocalReview     AgentStepKind = "LOCAL_REVIEW"
	AgentStepStatusReport    AgentStepKind = "STATUS_REPORT"
)

// AgentStepSendStatus is deliberately separate from the result content. A
// saved request is not a sent request, and a sent request is not a result.
type AgentStepSendStatus string

const (
	AgentStepSendStatusPending AgentStepSendStatus = "PENDING"
	AgentStepSendStatusSent    AgentStepSendStatus = "SENT"
	AgentStepSendStatusSettled AgentStepSendStatus = "SETTLED"
	AgentStepSendStatusFailed  AgentStepSendStatus = "FAILED"
)

// PlanReviewVerdict is the only result of a steward review of one immutable
// engineering-plan version.
type PlanReviewVerdict string

const (
	PlanReviewApproved   PlanReviewVerdict = "APPROVED"
	PlanReviewReplan     PlanReviewVerdict = "REPLAN"
	PlanReviewNeedsHuman PlanReviewVerdict = "NEEDS_HUMAN"
)

// DispatchStatus describes the one-way outcome of an immutable dispatch
// request. PENDING never proves that a Builder has been started.
type DispatchStatus string

const (
	DispatchStatusPending  DispatchStatus = "PENDING"
	DispatchStatusAccepted DispatchStatus = "ACCEPTED"
	DispatchStatusRejected DispatchStatus = "REJECTED"
	DispatchStatusFailed   DispatchStatus = "FAILED"
)

// CandidateCheckKind identifies program-observed evidence, not Agent-reported
// test results.
type CandidateCheckKind string

const (
	CandidateCheckScope       CandidateCheckKind = "SCOPE"
	CandidateCheckRequired    CandidateCheckKind = "REQUIRED_CHECK"
	CandidateCheckIntegration CandidateCheckKind = "INTEGRATION"
)

// CandidateCheckRunStatus separates a durable run request from its immutable
// observed terminal result, so daemon restart can safely resume a pending run.
type CandidateCheckRunStatus string

const (
	CandidateCheckRunStatusPending CandidateCheckRunStatus = "PENDING"
	CandidateCheckRunStatusSettled CandidateCheckRunStatus = "SETTLED"
	CandidateCheckRunStatusFailed  CandidateCheckRunStatus = "FAILED"
)

// LocalReviewVerdict is a Reviewer result attached to one fixed candidate.
type LocalReviewVerdict string

const (
	LocalReviewPass       LocalReviewVerdict = "PASS"
	LocalReviewRework     LocalReviewVerdict = "REWORK"
	LocalReviewBlocked    LocalReviewVerdict = "BLOCKED"
	LocalReviewNeedsHuman LocalReviewVerdict = "NEEDS_HUMAN"
)

// LocalReviewStatus separates assignment from the immutable parsed reviewer
// verdict. A pending review exists before its stable Chat request is sent.
type LocalReviewStatus string

const (
	LocalReviewStatusPending LocalReviewStatus = "PENDING"
	LocalReviewStatusSettled LocalReviewStatus = "SETTLED"
	LocalReviewStatusFailed  LocalReviewStatus = "FAILED"
)

// RoleSessionBinding links one S02 responsibility to exactly one AO session
// creation request and, once successful, to that AO session.
type RoleSessionBinding struct {
	ID                            string            `json:"id"`
	DevelopmentRequirementID      string            `json:"developmentRequirementId"`
	RequirementVersionID          string            `json:"requirementVersionId"`
	Role                          StandardRole      `json:"role"`
	EngineeringPlanID             string            `json:"engineeringPlanId,omitempty"`
	DispatchID                    string            `json:"dispatchId,omitempty"`
	DevelopmentTaskID             string            `json:"developmentTaskId,omitempty"`
	CandidateCommitID             string            `json:"candidateCommitId,omitempty"`
	SessionCreationIdempotencyKey string            `json:"sessionCreationIdempotencyKey"`
	AOSessionID                   string            `json:"aoSessionId,omitempty"`
	Status                        RoleBindingStatus `json:"status"`
	ReasonCode                    ReasonCode        `json:"reasonCode,omitempty"`
	RequestedAt                   time.Time         `json:"requestedAt"`
	BoundAt                       *time.Time        `json:"boundAt,omitempty"`
	EndedAt                       *time.Time        `json:"endedAt,omitempty"`
}

// AgentStep persists the stable outbound request identity before an AO Chat
// turn exists, then records only the exact completed turn and final message.
type AgentStep struct {
	ID               string              `json:"id"`
	RoleBindingID    string              `json:"roleBindingId"`
	Kind             AgentStepKind       `json:"kind"`
	RequestID        string              `json:"requestId"`
	ClientMessageID  string              `json:"clientMessageId"`
	PromptSHA256     string              `json:"promptSha256"`
	SendStatus       AgentStepSendStatus `json:"sendStatus"`
	TurnID           string              `json:"turnId,omitempty"`
	FinalMessageID   string              `json:"finalMessageId,omitempty"`
	FinalMessageText string              `json:"finalMessageText,omitempty"`
	MessageSHA256    string              `json:"messageSha256,omitempty"`
	RequestedAt      time.Time           `json:"requestedAt"`
	SentAt           *time.Time          `json:"sentAt,omitempty"`
	CompletedAt      *time.Time          `json:"completedAt,omitempty"`
	FailedAt         *time.Time          `json:"failedAt,omitempty"`
	ReasonCode       ReasonCode          `json:"reasonCode,omitempty"`
}

// EngineeringPlan is append-only. PlanJSON is normalized by the protocol
// parser before it reaches storage, and PlanSHA256 identifies that exact text.
type EngineeringPlan struct {
	ID                   string    `json:"id"`
	RequirementVersionID string    `json:"requirementVersionId"`
	RequirementSHA256    string    `json:"requirementSha256"`
	Version              int64     `json:"version"`
	PlannerRoleBindingID string    `json:"plannerRoleBindingId"`
	AgentStepID          string    `json:"agentStepId"`
	TurnID               string    `json:"turnId"`
	FinalMessageID       string    `json:"finalMessageId"`
	PlanJSON             string    `json:"planJson"`
	PlanSHA256           string    `json:"planSha256"`
	CreatedAt            time.Time `json:"createdAt"`
}

// PlanReview is the exact steward conclusion for an immutable plan version.
type PlanReview struct {
	ID                   string            `json:"id"`
	EngineeringPlanID    string            `json:"engineeringPlanId"`
	StewardRoleBindingID string            `json:"stewardRoleBindingId"`
	AgentStepID          string            `json:"agentStepId"`
	TurnID               string            `json:"turnId"`
	FinalMessageID       string            `json:"finalMessageId"`
	Verdict              PlanReviewVerdict `json:"verdict"`
	ReasonCode           ReasonCode        `json:"reasonCode"`
	Summary              string            `json:"summary"`
	CreatedAt            time.Time         `json:"createdAt"`
}

// Dispatch saves the immutable request and its one-way outcome. The accepted
// fields are populated only by the acceptance compare-and-swap transaction.
type Dispatch struct {
	ID                            string         `json:"id"`
	RequirementVersionID          string         `json:"requirementVersionId"`
	EngineeringPlanID             string         `json:"engineeringPlanId"`
	PlanReviewID                  string         `json:"planReviewId"`
	StewardRoleBindingID          string         `json:"stewardRoleBindingId"`
	AgentStepID                   string         `json:"agentStepId"`
	Mode                          WorkMode       `json:"mode"`
	PreallocatedDevelopmentTaskID string         `json:"preallocatedDevelopmentTaskId"`
	ExpectedTaskSetVersion        int64          `json:"expectedTaskSetVersion"`
	ExecutionPackageJSON          string         `json:"executionPackageJson"`
	ExecutionPackageSHA256        string         `json:"executionPackageSha256"`
	BuilderSessionIdempotencyKey  string         `json:"builderSessionIdempotencyKey"`
	BuilderRoleBindingID          string         `json:"builderRoleBindingId"`
	BaseCommitSHA                 string         `json:"baseCommitSha,omitempty"`
	Status                        DispatchStatus `json:"status"`
	ReasonCode                    ReasonCode     `json:"reasonCode,omitempty"`
	RequestedAt                   time.Time      `json:"requestedAt"`
	DecidedAt                     *time.Time     `json:"decidedAt,omitempty"`
}

// CandidateCheckRun is an immutable result of the restricted checker on one
// fixed candidate. It stores only program-observed command and output facts.
type CandidateCheckRun struct {
	ID                 string                  `json:"id"`
	DevelopmentTaskID  string                  `json:"developmentTaskId"`
	CandidateCommitID  string                  `json:"candidateCommitId"`
	DispatchID         string                  `json:"dispatchId"`
	BaseCommitSHA      string                  `json:"baseCommitSha"`
	CandidateCommitSHA string                  `json:"candidateCommitSha"`
	Kind               CandidateCheckKind      `json:"kind"`
	Name               string                  `json:"name"`
	CheckSpecSHA256    string                  `json:"checkSpecSha256"`
	ArgvJSON           string                  `json:"argvJson"`
	ContainerImageID   string                  `json:"containerImageId"`
	Status             CandidateCheckRunStatus `json:"status"`
	ExitCode           *int                    `json:"exitCode,omitempty"`
	TimedOut           bool                    `json:"timedOut"`
	OutputSummary      string                  `json:"outputSummary,omitempty"`
	OutputSHA256       string                  `json:"outputSha256,omitempty"`
	ChangedPathsJSON   string                  `json:"changedPathsJson,omitempty"`
	Result             EvidenceResult          `json:"result,omitempty"`
	CreatedAt          time.Time               `json:"createdAt"`
	SettledAt          *time.Time              `json:"settledAt,omitempty"`
	ReasonCode         ReasonCode              `json:"reasonCode,omitempty"`
}

// LocalReview is an immutable Reviewer verdict for the exact candidate and
// review packet shown to that independently bound Reviewer session.
type LocalReview struct {
	ID                    string             `json:"id"`
	CandidateCommitID     string             `json:"candidateCommitId"`
	DispatchID            string             `json:"dispatchId"`
	ReviewPacketJSON      string             `json:"reviewPacketJson"`
	ReviewPacketSHA256    string             `json:"reviewPacketSha256"`
	ReviewerRoleBindingID string             `json:"reviewerRoleBindingId"`
	AgentStepID           string             `json:"agentStepId"`
	Status                LocalReviewStatus  `json:"status"`
	TurnID                string             `json:"turnId,omitempty"`
	FinalMessageID        string             `json:"finalMessageId,omitempty"`
	Verdict               LocalReviewVerdict `json:"verdict,omitempty"`
	ReasonCode            ReasonCode         `json:"reasonCode,omitempty"`
	Summary               string             `json:"summary,omitempty"`
	CreatedAt             time.Time          `json:"createdAt"`
	SettledAt             *time.Time         `json:"settledAt,omitempty"`
}

// StartStandardFlowCommand creates the one durable steward spawn request for
// the current confirmed version. It carries no requirement content or result.
type StartStandardFlowCommand struct {
	DevelopmentRequirementID     string    `json:"developmentRequirementId"`
	StewardRoleBindingID         string    `json:"stewardRoleBindingId"`
	StewardSessionIdempotencyKey string    `json:"stewardSessionIdempotencyKey"`
	At                           time.Time `json:"at"`
}

// CreateRoleBindingCommand is idempotent only for the same immutable binding
// shape. Callers cannot use it to replace a role's AO session or spawn key.
type CreateRoleBindingCommand struct {
	Binding RoleSessionBinding `json:"binding"`
}

// FailRoleBindingCommand records a stable pre-bind spawn failure once.
type FailRoleBindingCommand struct {
	RoleBindingID string     `json:"roleBindingId"`
	ReasonCode    ReasonCode `json:"reasonCode"`
	At            time.Time  `json:"at"`
}

// CreatePendingDispatchCommand saves the dispatch and its Builder spawn fact
// in the same transaction. It intentionally contains no AO session ID.
type CreatePendingDispatchCommand struct {
	Dispatch       Dispatch           `json:"dispatch"`
	BuilderBinding RoleSessionBinding `json:"builderBinding"`
}

// SettleCandidateCheckRunCommand supplies terminal facts observed by the
// restricted checker. It cannot select a command, candidate, or base again.
type SettleCandidateCheckRunCommand struct {
	CandidateCheckRunID string         `json:"candidateCheckRunId"`
	ContainerImageID    string         `json:"containerImageId,omitempty"`
	ExitCode            *int           `json:"exitCode,omitempty"`
	TimedOut            bool           `json:"timedOut"`
	OutputSummary       string         `json:"outputSummary"`
	OutputSHA256        string         `json:"outputSha256"`
	ChangedPathsJSON    string         `json:"changedPathsJson"`
	Result              EvidenceResult `json:"result"`
	At                  time.Time      `json:"at"`
	ReasonCode          ReasonCode     `json:"reasonCode,omitempty"`
}

// SettleLocalReviewCommand persists the parsed terminal result only after the
// associated Reviewer Chat step has settled on the exact turn/message.
type SettleLocalReviewCommand struct {
	LocalReviewID  string             `json:"localReviewId"`
	TurnID         string             `json:"turnId,omitempty"`
	FinalMessageID string             `json:"finalMessageId,omitempty"`
	Verdict        LocalReviewVerdict `json:"verdict,omitempty"`
	ReasonCode     ReasonCode         `json:"reasonCode"`
	Summary        string             `json:"summary,omitempty"`
	At             time.Time          `json:"at"`
}

// StandardFailureCommand applies the frozen S02 rework/blocking rule from one
// persisted failed checker run or local review. It never accepts Agent text as
// a failure source and never widens the S01 generic transition entry point.
type StandardFailureCommand struct {
	DispatchID            string     `json:"dispatchId"`
	CandidateCheckRunID   string     `json:"candidateCheckRunId,omitempty"`
	LocalReviewID         string     `json:"localReviewId,omitempty"`
	InfrastructureFailure bool       `json:"infrastructureFailure"`
	ReasonCode            ReasonCode `json:"reasonCode"`
	ReasonText            string     `json:"reasonText,omitempty"`
	At                    time.Time  `json:"at"`
}

// StandardBuilderOutcomeCommand is the dedicated S02 path for a valid Builder
// result that says BLOCKED or NEEDS_HUMAN before any checker/review exists.
type StandardBuilderOutcomeCommand struct {
	DispatchID  string                `json:"dispatchId"`
	AgentStepID string                `json:"agentStepId"`
	Outcome     DevelopmentTaskStatus `json:"outcome"`
	ReasonCode  ReasonCode            `json:"reasonCode"`
	ReasonText  string                `json:"reasonText,omitempty"`
	At          time.Time             `json:"at"`
}

// StandardReviewerBindingFailureCommand maps a persisted failed Reviewer
// spawn/binding to BLOCKED before a local review fact or Chat step can exist.
type StandardReviewerBindingFailureCommand struct {
	RoleBindingID string     `json:"roleBindingId"`
	ReasonCode    ReasonCode `json:"reasonCode"`
	ReasonText    string     `json:"reasonText,omitempty"`
	At            time.Time  `json:"at"`
}

// CompleteStandardTaskCommand is intentionally small: all candidate, review,
// check, and integration evidence identities are derived inside one storage
// transaction from the accepted dispatch and its current candidate.
type CompleteStandardTaskCommand struct {
	DispatchID             string    `json:"dispatchId"`
	IntegrationCandidateID string    `json:"integrationCandidateId"`
	IntegrationEvidenceID  string    `json:"integrationEvidenceId"`
	At                     time.Time `json:"at"`
}

// StandardCandidateObservation is the trusted S02-only candidate write
// command. Candidate SHA and worktree cleanliness have already been observed
// by the Control Plane, never supplied by a Builder result message.
type StandardCandidateObservation struct {
	CandidateCommitID string    `json:"candidateCommitId"`
	DispatchID        string    `json:"dispatchId"`
	CommitSHA         string    `json:"commitSha"`
	ObservedAt        time.Time `json:"observedAt"`
}

// StandardIntegrationCandidateObservation records the same accepted dispatch
// after the matching task candidate passes its integration pre-check.
type StandardIntegrationCandidateObservation struct {
	IntegrationCandidateID string    `json:"integrationCandidateId"`
	CandidateCommitID      string    `json:"candidateCommitId"`
	DispatchID             string    `json:"dispatchId"`
	ObservedAt             time.Time `json:"observedAt"`
}

// StandardEvidenceRecord is the S02 source-bound form of existing ClearDev
// evidence. Exactly one source ID must be supplied: a checker run or a local
// review. The underlying candidate/integration binding stays in Evidence.
type StandardEvidenceRecord struct {
	Evidence            EvidenceRecord `json:"evidence"`
	CandidateCheckRunID string         `json:"candidateCheckRunId,omitempty"`
	LocalReviewID       string         `json:"localReviewId,omitempty"`
}

// DispatchAcceptance is the one atomic task creation operation after a
// Builder session has been bound. No empty task may survive a failed accept.
type DispatchAcceptance struct {
	DispatchID       string                 `json:"dispatchId"`
	BuilderSessionID string                 `json:"builderSessionId"`
	BaseCommitSHA    string                 `json:"baseCommitSha"`
	InitialTask      InitialDevelopmentTask `json:"initialTask"`
	IntegrationCheck RequiredCheck          `json:"integrationCheck"`
	At               time.Time              `json:"at"`
}

// StandardFlowFacts is the storage read model for an S02 flow. It deliberately
// contains raw facts only; phase and progress remain derived by the service.
type StandardFlowSnapshot struct {
	DevelopmentRequirementID string               `json:"developmentRequirementId"`
	RequirementVersionID     string               `json:"requirementVersionId"`
	RoleBindings             []RoleSessionBinding `json:"roleBindings"`
	AgentSteps               []AgentStep          `json:"agentSteps"`
	EngineeringPlans         []EngineeringPlan    `json:"engineeringPlans"`
	PlanReviews              []PlanReview         `json:"planReviews"`
	Dispatches               []Dispatch           `json:"dispatches"`
	CandidateCheckRuns       []CandidateCheckRun  `json:"candidateCheckRuns"`
	LocalReviews             []LocalReview        `json:"localReviews"`
}

// StandardFlowFacts is retained as a clear name for callers which deal only
// in raw facts; it is the same read-only snapshot.
type StandardFlowFacts = StandardFlowSnapshot
