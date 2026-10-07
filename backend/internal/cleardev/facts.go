package cleardev

import "time"

// DevelopmentRequirement is the user-visible record for one implementation
// request. It is linked to, but remains distinct from, an AO project.
type DevelopmentRequirement struct {
	ID               string     `json:"id"`
	AOProjectID      string     `json:"aoProjectId"`
	Name             string     `json:"name"`
	CancelledAt      *time.Time `json:"cancelledAt,omitempty"`
	CancelReason     ReasonCode `json:"cancelReason,omitempty"`
	CancelReasonText string     `json:"cancelReasonText,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

// RequirementVersion is one immutable-after-confirmation version of a
// development requirement.
type RequirementVersion struct {
	ID                       string                   `json:"id"`
	DevelopmentRequirementID string                   `json:"developmentRequirementId"`
	Version                  int64                    `json:"version"`
	RequirementText          string                   `json:"requirementText"`
	SHA256                   string                   `json:"sha256"`
	Status                   RequirementVersionStatus `json:"status" enum:"DRAFT,PENDING_CONFIRMATION,CONFIRMED,REJECTED,SUPERSEDED"`
	SupersededByID           string                   `json:"supersededById,omitempty"`
	TaskSetVersion           int64                    `json:"taskSetVersion"`
	CreatedAt                time.Time                `json:"createdAt"`
	ConfirmedAt              *time.Time               `json:"confirmedAt,omitempty"`
}

// DevelopmentTask is one independently gated unit of development work. New
// records use PLANNED as their initial state. READY is only a physical legacy
// value and is mapped to PLANNED before it reaches this model.
type DevelopmentTask struct {
	ID                       string                `json:"id"`
	DevelopmentRequirementID string                `json:"developmentRequirementId"`
	RequirementVersionID     string                `json:"requirementVersionId"`
	Title                    string                `json:"title"`
	Mode                     WorkMode              `json:"mode" enum:"QUICK,STANDARD"`
	Status                   DevelopmentTaskStatus `json:"status" enum:"PLANNED,RUNNING,REVIEW,REWORK,NEEDS_HUMAN,BLOCKED,DONE,CANCELLED"`
	PausedFromStatus         DevelopmentTaskStatus `json:"pausedFromStatus,omitempty" enum:"PLANNED,RUNNING,REVIEW,REWORK"`
	MaxReworkCount           int                   `json:"maxReworkCount"`
	ReworkCount              int                   `json:"reworkCount"`
	CreatedAt                time.Time             `json:"createdAt"`
	UpdatedAt                time.Time             `json:"updatedAt"`
}

// PermissionVersion freezes the four path-authority lists a candidate used.
type PermissionVersion struct {
	ID                string    `json:"id"`
	DevelopmentTaskID string    `json:"developmentTaskId"`
	Version           int64     `json:"version"`
	Rules             PathRules `json:"rules"`
	CreatedAt         time.Time `json:"createdAt"`
}

// RequiredCheck names one check the Control Plane must record for a candidate.
type RequiredCheck struct {
	ID                string    `json:"id"`
	DevelopmentTaskID string    `json:"developmentTaskId"`
	Name              string    `json:"name"`
	Kind              string    `json:"kind"`
	CreatedAt         time.Time `json:"createdAt"`
}

// CandidateCommit is an append-only observation of a Builder worktree HEAD.
type CandidateCommit struct {
	ID                  string    `json:"id"`
	DevelopmentTaskID   string    `json:"developmentTaskId"`
	Sequence            int64     `json:"sequence"`
	AOSessionID         string    `json:"aoSessionId"`
	PermissionVersionID string    `json:"permissionVersionId"`
	CommitSHA           string    `json:"commitSha"`
	CreatedAt           time.Time `json:"createdAt"`
}

// IntegrationCandidate is an append-only observed integration HEAD. Old 0106
// candidates have nil requirement and task-set bindings and cannot complete a
// v3 requirement.
type IntegrationCandidate struct {
	ID                       string    `json:"id"`
	DevelopmentRequirementID string    `json:"developmentRequirementId"`
	RequirementVersionID     string    `json:"requirementVersionId,omitempty"`
	TaskSetVersion           *int64    `json:"taskSetVersion,omitempty"`
	Sequence                 int64     `json:"sequence"`
	AOSessionID              string    `json:"aoSessionId"`
	CommitSHA                string    `json:"commitSha"`
	CreatedAt                time.Time `json:"createdAt"`
}

// EvidenceRecord is a persisted trusted result. Task records bind a
// CandidateCommit; requirement integration records bind an IntegrationCandidate.
type EvidenceRecord struct {
	Sequence                 int64          `json:"sequence"`
	ID                       string         `json:"id"`
	DevelopmentRequirementID string         `json:"developmentRequirementId"`
	SubjectType              SubjectType    `json:"subjectType" enum:"DEVELOPMENT_TASK,DEVELOPMENT_REQUIREMENT"`
	SubjectID                string         `json:"subjectId"`
	Kind                     EvidenceKind   `json:"kind" enum:"SCOPE,REQUIRED_CHECK,INTEGRATION,REVIEW"`
	Key                      string         `json:"key,omitempty"`
	Result                   EvidenceResult `json:"result" enum:"PASS,FAIL"`
	CandidateCommitID        string         `json:"candidateCommitId,omitempty"`
	IntegrationCandidateID   string         `json:"integrationCandidateId,omitempty"`
	CommitSHA                string         `json:"commitSha"`
	Source                   EvidenceSource `json:"source" enum:"CONTROL_PLANE_CHECKER,REVIEW_ADAPTER"`
	SourceAOSessionID        string         `json:"sourceAoSessionId,omitempty"`
	CreatedAt                time.Time      `json:"createdAt"`
	ExpiresAt                *time.Time     `json:"expiresAt,omitempty"`
}

// SubjectType identifies the fact an event or evidence record belongs to.
type SubjectType string

const (
	SubjectDevelopmentRequirement SubjectType = "DEVELOPMENT_REQUIREMENT"
	SubjectRequirementVersion     SubjectType = "REQUIREMENT_VERSION"
	SubjectDevelopmentTask        SubjectType = "DEVELOPMENT_TASK"
	SubjectPermission             SubjectType = "PERMISSION_VERSION"
	SubjectRequiredCheck          SubjectType = "REQUIRED_CHECK"
	SubjectCandidate              SubjectType = "CANDIDATE_COMMIT"
	SubjectIntegrationCandidate   SubjectType = "INTEGRATION_CANDIDATE"
	SubjectEvidence               SubjectType = "EVIDENCE"
	SubjectHumanDecisionRequest   SubjectType = "HUMAN_DECISION_REQUEST"
	SubjectHumanDecisionDispatch  SubjectType = "HUMAN_DECISION_DISPATCH"
	SubjectHumanDecisionEffect    SubjectType = "HUMAN_DECISION_EFFECT"
)

// EventOutcome is persisted for both accepted and rejected state actions.
type EventOutcome string

const (
	EventAccepted EventOutcome = "ACCEPTED"
	EventRejected EventOutcome = "REJECTED"
)

// EventSource distinguishes ordinary Control Plane actions, trusted human
// decisions, checkers, review adapters, and compatible database migrations.
type EventSource string

const (
	EventSourceControlPlane  EventSource = "CONTROL_PLANE"
	EventSourceHumanDecision EventSource = "HUMAN_DECISION"
	EventSourceChecker       EventSource = "CONTROL_PLANE_CHECKER"
	EventSourceReviewAdapter EventSource = "REVIEW_ADAPTER"
	EventSourceMigration     EventSource = "MIGRATION"
)

// Action names a dedicated service operation. Callers never provide a target
// status directly; storage derives it from one of these actions.
type Action string

const (
	ActionCreateRequirement              Action = "CREATE_REQUIREMENT"
	ActionCreateRequirementVersion       Action = "CREATE_REQUIREMENT_VERSION"
	ActionSubmitRequirementConfirmation  Action = "SUBMIT_REQUIREMENT_CONFIRMATION"
	ActionConfirmRequirementVersion      Action = "CONFIRM_REQUIREMENT_VERSION"
	ActionRejectRequirementVersion       Action = "REJECT_REQUIREMENT_VERSION"
	ActionSupersedeRequirementVersion    Action = "SUPERSEDE_REQUIREMENT_VERSION"
	ActionCancelRequirement              Action = "CANCEL_REQUIREMENT"
	ActionCreateDevelopmentTask          Action = "CREATE_DEVELOPMENT_TASK"
	ActionCreatePermissionVersion        Action = "CREATE_PERMISSION_VERSION"
	ActionCreateRequiredCheck            Action = "CREATE_REQUIRED_CHECK"
	ActionStartDevelopmentTask           Action = "START_DEVELOPMENT_TASK"
	ActionSubmitDevelopmentTaskReview    Action = "SUBMIT_DEVELOPMENT_TASK_REVIEW"
	ActionCompleteDevelopmentTask        Action = "COMPLETE_DEVELOPMENT_TASK"
	ActionRequestDevelopmentTaskRework   Action = "REQUEST_DEVELOPMENT_TASK_REWORK"
	ActionEscalateDevelopmentTask        Action = "ESCALATE_DEVELOPMENT_TASK"
	ActionRestartDevelopmentTask         Action = "RESTART_DEVELOPMENT_TASK"
	ActionPauseDevelopmentTaskNeedsHuman Action = "PAUSE_DEVELOPMENT_TASK_NEEDS_HUMAN"
	ActionBlockDevelopmentTask           Action = "BLOCK_DEVELOPMENT_TASK"
	ActionResumeDevelopmentTask          Action = "RESUME_DEVELOPMENT_TASK"
	ActionCancelDevelopmentTask          Action = "CANCEL_DEVELOPMENT_TASK"
	ActionRegisterCandidate              Action = "REGISTER_CANDIDATE"
	ActionRegisterIntegrationCandidate   Action = "REGISTER_INTEGRATION_CANDIDATE"
	ActionRecordEvidence                 Action = "RECORD_EVIDENCE"
	ActionCreateHumanDecisionRequest     Action = "CREATE_HUMAN_DECISION_REQUEST"
	ActionIssueHumanDecisionDispatch     Action = "ISSUE_HUMAN_DECISION_DISPATCH"
	ActionSettleHumanDecision            Action = "SETTLE_HUMAN_DECISION"
	ActionDismissHumanDecisionDispatch   Action = "DISMISS_HUMAN_DECISION_DISPATCH"
)

// RequirementEvent is the append-only audit record that also drives ClearDev CDC.
type RequirementEvent struct {
	Sequence                 int64        `json:"sequence"`
	AOProjectID              string       `json:"aoProjectId"`
	DevelopmentRequirementID string       `json:"developmentRequirementId"`
	SubjectType              SubjectType  `json:"subjectType"`
	SubjectID                string       `json:"subjectId"`
	Action                   Action       `json:"action"`
	PreviousState            string       `json:"previousState,omitempty"`
	TargetState              string       `json:"targetState,omitempty"`
	Outcome                  EventOutcome `json:"outcome" enum:"ACCEPTED,REJECTED"`
	Reason                   ReasonCode   `json:"reasonCode,omitempty"`
	ReasonText               string       `json:"reasonText,omitempty"`
	Source                   EventSource  `json:"source"`
	SourceAOSessionID        string       `json:"sourceAoSessionId,omitempty"`
	CreatedAt                time.Time    `json:"createdAt"`
}

// RequirementSnapshot is the complete durable fact view used by the service.
// Any effective or missing fields are computed by the read-model layer.
type RequirementSnapshot struct {
	Requirement           DevelopmentRequirement `json:"requirement"`
	RequirementVersions   []RequirementVersion   `json:"requirementVersions"`
	DevelopmentTasks      []DevelopmentTask      `json:"developmentTasks"`
	PermissionVersions    []PermissionVersion    `json:"permissionVersions"`
	RequiredChecks        []RequiredCheck        `json:"requiredChecks"`
	Candidates            []CandidateCommit      `json:"candidates"`
	IntegrationCandidates []IntegrationCandidate `json:"integrationCandidates"`
	Evidence              []EvidenceRecord       `json:"evidence"`
	Events                []RequirementEvent     `json:"events"`
}

// RuleError carries a stable reason code through service and HTTP layers.
type RuleError struct {
	Code    ReasonCode
	Message string
}

func (e *RuleError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}
