package cleardev

import "time"

// InitialRequirement is the validated fact set written by one atomic public
// requirement-create request. It intentionally contains no development tasks.
type InitialRequirement struct {
	Requirement      DevelopmentRequirement
	Version          RequirementVersion
	BenchmarkBinding *BenchmarkBinding
}

// InitialDevelopmentTask groups records which must be created atomically after
// storage has selected the current confirmed requirement version.
type InitialDevelopmentTask struct {
	Task       DevelopmentTask
	Permission PermissionVersion
	Checks     []RequiredCheck
}

// CreateRequirementVersionCommand contains the new immutable text only. The
// repository assigns the next version number and initial DRAFT status.
type CreateRequirementVersionCommand struct {
	DevelopmentRequirementID string
	RequirementText          string
	At                       time.Time
}

// CreateDevelopmentTaskCommand does not let the caller select a requirement
// version. Storage binds it to the current confirmed version in its transaction.
type CreateDevelopmentTaskCommand struct {
	DevelopmentRequirementID string
	Title                    string
	Mode                     WorkMode
	MaxReworkCount           int
	Rules                    PathRules
	RequiredChecks           []RequiredCheckDefinition
	At                       time.Time
}

// RequiredCheckDefinition is the caller-supplied shape used only while a new
// task and its first required checks are atomically created.
type RequiredCheckDefinition struct {
	ID   string
	Name string
	Kind string
}

// ActionRequest selects one dedicated state action. Target statuses are never
// supplied by callers; storage derives them from Action.
type ActionRequest struct {
	Action                          Action
	SubjectID                       string
	ReplacementRequirementVersionID string
	Reason                          ReasonCode
	ReasonText                      string
	TrustedHumanDecision            bool
	Source                          EventSource
	SourceAOSessionID               string
	At                              time.Time
}

// CandidateObservation is a trusted, normalized worktree observation ready to
// be appended. Sequence is assigned transactionally by storage.
type CandidateObservation struct {
	ID                string
	DevelopmentTaskID string
	AOSessionID       string
	CommitSHA         string
	ObservedAt        time.Time
}

// IntegrationCandidateObservation is the requirement-level counterpart. The
// repository binds it to the current confirmed version and task-set version.
type IntegrationCandidateObservation struct {
	ID                       string
	DevelopmentRequirementID string
	AOSessionID              string
	CommitSHA                string
	ObservedAt               time.Time
}
