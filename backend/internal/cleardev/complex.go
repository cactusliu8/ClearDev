package cleardev

import "time"

// Complex protocol version and compilation input limits.
const (
	ComplexProtocolVersion   = 1
	MaxComplexPRDBytes       = 65536
	MaxComplexAnswerRunes    = 10000
	MaxComplexClarifications = 8
	MinComplexPlanTasks      = 1
	MaxComplexPlanTasks      = 6
	// MaxComplexAutomaticReplans bounds automatic plan revisions after the first
	// plan of one confirmed requirement version. The first plan plus three
	// revisions is the maximum; a fourth valid REPLAN derives NEEDS_HUMAN.
	MaxComplexAutomaticReplans = 3
)

const (
	SubjectComplexRequirement     SubjectType = "COMPLEX_REQUIREMENT"
	SubjectComplexRoleBinding     SubjectType = "COMPLEX_ROLE_BINDING"
	SubjectComplexAgentStep       SubjectType = "COMPLEX_AGENT_STEP"
	SubjectComplexCompilation     SubjectType = "COMPLEX_COMPILATION"
	SubjectComplexClarification   SubjectType = "COMPLEX_CLARIFICATION"
	SubjectComplexEngineeringPlan SubjectType = "COMPLEX_ENGINEERING_PLAN"
	SubjectComplexPlanReview      SubjectType = "COMPLEX_PLAN_REVIEW"

	ActionCreateComplexRequirement     Action = "CREATE_COMPLEX_REQUIREMENT"
	ActionBindComplexRole              Action = "BIND_COMPLEX_ROLE"
	ActionCreateComplexAgentStep       Action = "CREATE_COMPLEX_AGENT_STEP"
	ActionSendComplexAgentStep         Action = "SEND_COMPLEX_AGENT_STEP"
	ActionSettleComplexAgentStep       Action = "SETTLE_COMPLEX_AGENT_STEP"
	ActionRecordComplexClarification   Action = "RECORD_COMPLEX_CLARIFICATION"
	ActionRecordComplexAnswers         Action = "RECORD_COMPLEX_ANSWERS"
	ActionRecordComplexCompilation     Action = "RECORD_COMPLEX_COMPILATION"
	ActionCreateComplexEngineeringPlan Action = "CREATE_COMPLEX_ENGINEERING_PLAN"
	ActionRecordComplexPlanReview      Action = "RECORD_COMPLEX_PLAN_REVIEW"

	ReasonComplexPlanRequired ReasonCode = "COMPLEX_PLAN_REQUIRED"
	// ReasonComplexReplanLimitReached is derived when one confirmed requirement
	// version already used its first plan plus the maximum automatic replans and
	// the last valid review is still REPLAN. No further planning message is sent.
	ReasonComplexReplanLimitReached ReasonCode = "PLANNING_REPLAN_LIMIT_REACHED"
)

const (
	ComplexAgentStepCompilation     AgentStepKind = "REQUIREMENT_COMPILATION"
	ComplexAgentStepEngineeringPlan AgentStepKind = "COMPLEX_ENGINEERING_PLAN"
	ComplexAgentStepPlanReview      AgentStepKind = "COMPLEX_PLAN_REVIEW"
)

// ComplexPlanningPhase is derived from durable S04 facts on every read. It is
// not stored and cannot replace S01 OverallProgress.
type ComplexPlanningPhase string

// Named ComplexPlanningPhase values used by the S04 read model.
const (
	ComplexPlanningCompiling             ComplexPlanningPhase = "COMPILING"
	ComplexPlanningAwaitingClarification ComplexPlanningPhase = "AWAITING_CLARIFICATION"
	ComplexPlanningAwaitingConfirmation  ComplexPlanningPhase = "AWAITING_CONFIRMATION"
	ComplexPlanningPlanning              ComplexPlanningPhase = "PLANNING"
	ComplexPlanningAwaitingPlanReview    ComplexPlanningPhase = "AWAITING_PLAN_REVIEW"
	ComplexPlanningApproved              ComplexPlanningPhase = "APPROVED"
	ComplexPlanningNeedsHuman            ComplexPlanningPhase = "NEEDS_HUMAN"
	ComplexPlanningRejected              ComplexPlanningPhase = "REJECTED"
)

// ComplexRequirement is the immutable original-PRD marker. It is not a
// requirement version.
type ComplexRequirement struct {
	DevelopmentRequirementID   string    `json:"developmentRequirementId"`
	OriginalPRDText            string    `json:"originalPrdText"`
	OriginalPRDSHA256          string    `json:"originalPrdSha256"`
	TargetRequirementVersionID string    `json:"targetRequirementVersionId"`
	CreatedAt                  time.Time `json:"createdAt"`
}

// ComplexRoleBinding links one S04 responsibility to one AO Chat session.
type ComplexRoleBinding struct {
	ID                            string            `json:"id"`
	DevelopmentRequirementID      string            `json:"developmentRequirementId"`
	Role                          StandardRole      `json:"role"`
	SessionCreationIdempotencyKey string            `json:"sessionCreationIdempotencyKey"`
	AOSessionID                   string            `json:"aoSessionId,omitempty"`
	Status                        RoleBindingStatus `json:"status"`
	ReasonCode                    ReasonCode        `json:"reasonCode,omitempty"`
	RequestedAt                   time.Time         `json:"requestedAt"`
	BoundAt                       *time.Time        `json:"boundAt,omitempty"`
	EndedAt                       *time.Time        `json:"endedAt,omitempty"`
}

// ComplexClarificationQuestion is one saved steward question.
type ComplexClarificationQuestion struct {
	CompilationRequestID string   `json:"compilationRequestId"`
	QuestionKey          string   `json:"questionKey"`
	Text                 string   `json:"text"`
	Reason               string   `json:"reason"`
	RequirementKeys      []string `json:"requirementKeys"`
	Ordinal              int      `json:"ordinal"`
}

// ComplexClarificationAnswer is one immutable user answer bound to one
// question key of one compilation request.
type ComplexClarificationAnswer struct {
	CompilationRequestID string    `json:"compilationRequestId"`
	QuestionKey          string    `json:"questionKey"`
	Text                 string    `json:"text"`
	CreatedAt            time.Time `json:"createdAt"`
}

// ComplexCompilationRequest is one steward compilation round.
type ComplexCompilationRequest struct {
	ID                       string    `json:"id"`
	DevelopmentRequirementID string    `json:"developmentRequirementId"`
	AgentStepID              string    `json:"agentStepId"`
	ClarificationRound       int       `json:"clarificationRound"`
	CompilationContextSHA256 string    `json:"compilationContextSha256"`
	AdditionalRoundReason    string    `json:"additionalRoundReason,omitempty"`
	CreatedAt                time.Time `json:"createdAt"`
}

// ComplexIDMap records the authority mapping from a temporary Agent key to a
// backend-assigned stable identifier.
type ComplexIDMap struct {
	CompilationID string `json:"compilationId"`
	Kind          string `json:"kind" enum:"REQUIREMENT,ACCEPTANCE"`
	TemporaryKey  string `json:"temporaryKey"`
	StableID      string `json:"stableId"`
	Ordinal       int    `json:"ordinal"`
}

// ComplexCompilation is an append-only READY or NEEDS_HUMAN compilation fact.
type ComplexCompilation struct {
	ID                        string    `json:"id"`
	DevelopmentRequirementID  string    `json:"developmentRequirementId"`
	CompilationRequestID      string    `json:"compilationRequestId"`
	AgentStepID               string    `json:"agentStepId"`
	Outcome                   string    `json:"outcome"`
	Summary                   string    `json:"summary"`
	NormalizedRequirementJSON string    `json:"normalizedRequirementJson"`
	CompilationSHA256         string    `json:"compilationSha256"`
	TurnID                    string    `json:"turnId"`
	FinalMessageID            string    `json:"finalMessageId"`
	RawMessageText            string    `json:"rawMessageText"`
	RawMessageSHA256          string    `json:"rawMessageSha256"`
	CreatedAt                 time.Time `json:"createdAt"`
}

// ComplexEngineeringPlan is an append-only validated plan version.
type ComplexEngineeringPlan struct {
	ID                       string    `json:"id"`
	PlanningRequestID        string    `json:"planningRequestId"`
	DevelopmentRequirementID string    `json:"developmentRequirementId"`
	RequirementVersionID     string    `json:"requirementVersionId"`
	RequirementSHA256        string    `json:"requirementSha256"`
	CompilationSHA256        string    `json:"compilationSha256"`
	Version                  int64     `json:"version"`
	PlannerRoleBindingID     string    `json:"plannerRoleBindingId"`
	AgentStepID              string    `json:"agentStepId"`
	TurnID                   string    `json:"turnId"`
	FinalMessageID           string    `json:"finalMessageId"`
	PlanJSON                 string    `json:"planJson"`
	PlanSHA256               string    `json:"planSha256"`
	CreatedAt                time.Time `json:"createdAt"`
}

// ComplexPlanReview is the steward conclusion for one immutable plan.
type ComplexPlanReview struct {
	ID                   string            `json:"id"`
	PlanID               string            `json:"planId"`
	StewardRoleBindingID string            `json:"stewardRoleBindingId"`
	AgentStepID          string            `json:"agentStepId"`
	ReviewRequestID      string            `json:"reviewRequestId"`
	Verdict              PlanReviewVerdict `json:"verdict"`
	ReasonCode           ReasonCode        `json:"reasonCode"`
	Summary              string            `json:"summary"`
	FindingsJSON         string            `json:"findingsJson"`
	PlanSHA256           string            `json:"planSha256"`
	TurnID               string            `json:"turnId"`
	FinalMessageID       string            `json:"finalMessageId"`
	CreatedAt            time.Time         `json:"createdAt"`
}

// ComplexPlanningSnapshot is the raw S04 history used to derive the read model.
type ComplexPlanningSnapshot struct {
	Requirement         ComplexRequirement
	RoleBindings        []ComplexRoleBinding
	AgentSteps          []AgentStep
	CompilationRequests []ComplexCompilationRequest
	Questions           []ComplexClarificationQuestion
	Answers             []ComplexClarificationAnswer
	Compilations        []ComplexCompilation
	IDMaps              []ComplexIDMap
	Plans               []ComplexEngineeringPlan
	Reviews             []ComplexPlanReview
	Validations         []ComplexPlanValidation
	PlannerAnswers      []PlannerClarificationAnswer
}

// CreateComplexRequirementCommand is the atomic public create input.
type CreateComplexRequirementCommand struct {
	Requirement                DevelopmentRequirement
	OriginalPRDText            string
	OriginalPRDSHA256          string
	TargetRequirementVersionID string
	StewardRoleBinding         ComplexRoleBinding
	BenchmarkBinding           *BenchmarkBinding
}

// RecordComplexClarificationCommand persists one valid clarification round.
type RecordComplexClarificationCommand struct {
	Request   ComplexCompilationRequest
	Questions []ComplexClarificationQuestion
}

// SubmitComplexClarificationCommand saves answers for one unanswered round.
type SubmitComplexClarificationCommand struct {
	DevelopmentRequirementID string
	CompilationRequestID     string
	ClarificationRound       int
	Answers                  []ComplexClarificationAnswer
	At                       time.Time
}

// SettleComplexCompilationCommand atomically records a READY or NEEDS_HUMAN
// compilation, and for READY also the first pending v1 plus the S03 request.
type SettleComplexCompilationCommand struct {
	Request     ComplexCompilationRequest
	Compilation ComplexCompilation
	IDMaps      []ComplexIDMap
	Version     RequirementVersion
	At          time.Time
}

// CreateComplexPlanCommand appends one validated plan version.
type CreateComplexPlanCommand struct {
	Plan       ComplexEngineeringPlan
	Validation *ComplexPlanValidation
}

// RecordComplexPlanReviewCommand appends one steward review. REPLAN also
// records the next planning request identity so restart can continue.
type RecordComplexPlanReviewCommand struct {
	Review              ComplexPlanReview
	NextPlanningRequest string
	At                  time.Time
}

type CreateComplexRoleBindingCommand struct {
	Binding ComplexRoleBinding
}

type FailComplexRoleBindingCommand struct {
	RoleBindingID string
	ReasonCode    ReasonCode
	At            time.Time
}
