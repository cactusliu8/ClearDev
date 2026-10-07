package ports

import (
	"context"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

var (
	ErrClearDevGitUnavailable         = errors.New("cleardev git checker unavailable")
	ErrClearDevWorkspaceDirty         = errors.New("cleardev builder worktree is dirty")
	ErrClearDevCandidateInvalid       = errors.New("cleardev candidate commit is invalid")
	ErrClearDevCandidateNotDescendant = errors.New("cleardev candidate is not a descendant of the dispatch baseline")
	ErrClearDevCompositionConflict    = errors.New("cleardev candidate composition has conflicts")
)

// ClearDevDiffPath is one trusted Git name-status record. OldPath is populated
// for renames so the control service can authorize both sides of the move.
type ClearDevDiffPath struct {
	Status  string
	Path    string
	OldPath string
}

// ClearDevCandidateInspection is a clean worktree HEAD proven to descend from
// the dispatch baseline, plus the real zero-delimited Git diff between them.
type ClearDevCandidateInspection struct {
	BaseSHA      string
	CandidateSHA string
	Paths        []ClearDevDiffPath
}

// ClearDevComposeRequest is one controlled combine of verified candidates.
// RequestID makes the managed composition workspace deterministic so a
// restarted run reuses or replaces exactly its own directory.
type ClearDevComposeRequest struct {
	RequestID     string
	RepoPath      string
	BaseSHA       string
	CandidateSHAs []string
	// CompleteTaskDeltas merges complete reviewed mail task histories, preserves
	// conflicts, and publishes a stable delivery branch. False keeps legacy S07.
	CompleteTaskDeltas bool
}

// ClearDevComposeResult is the composed commit of one combine. ConflictPaths
// is set only when ErrClearDevCompositionConflict is returned.
type ClearDevComposeResult struct {
	WorkspacePath string
	OutputSHA     string
	ConflictPaths []string
}

type ClearDevCandidateInspector interface {
	InspectCandidate(ctx context.Context, workspacePath, baseSHA string) (ClearDevCandidateInspection, error)
	PrepareReviewBranch(ctx context.Context, workspacePath, branch, candidateSHA string) error
	// PrepareBaseWorkspace verifies a builder worktree is clean and parked on
	// the recorded candidate, then detaches it onto the new frozen base.
	PrepareBaseWorkspace(ctx context.Context, workspacePath, expectedHeadSHA, newBaseSHA string) error
	// ComposeCandidates cherry-picks the ordered candidates onto the common
	// base inside a separate managed worktree and never resolves conflicts.
	ComposeCandidates(ctx context.Context, request ClearDevComposeRequest) (ClearDevComposeResult, error)
}

type ClearDevCheckRequest struct {
	// ExecutionProfile is backend-owned, not a project command or quota grant.
	ExecutionProfile string `json:"executionProfile,omitempty"`
	TrialStepID      string `json:"trialStepId,omitempty"`
	// RunID is a stable, control-plane-owned idempotency key for one external
	// check action. An empty value preserves the original S02 one-shot
	// behavior. Callers must reuse a non-empty value after restart instead of
	// creating another Docker action.
	RunID         string
	WorkspacePath string
	CandidateSHA  string
	Image         string
	Argv          []string
	Timeout       time.Duration
	MemoryBytes   int64
	PidsLimit     int64
	OutputLimit   int
	// AllowInfraInject is test-only and is not part of the idempotency
	// fingerprint. Production callers leave it false.
	AllowInfraInject bool
	// Optional only for the explicit project policy; nil preserves historical
	// request fingerprints and the old checker semantics.
	ProjectExecution *core.ProjectExecutionContract `json:"projectExecution,omitempty"`
}

// ClearDevCheckPreflightRequest binds the executable environment before any
// Builder task is dispatched. Argv contains the exact approved commands that
// may run against CandidateSHA; the preflight never executes those commands.
type ClearDevCheckPreflightRequest struct {
	RunID            string
	WorkspacePath    string
	CandidateSHA     string
	Image            string
	Argv             [][]string
	ProjectExecution *core.ProjectExecutionContract `json:"projectExecution,omitempty"`
	Timeout          time.Duration
	MemoryBytes      int64
	PidsLimit        int64
}

// ClearDevCheckEnvironment is the auditable identity of one prepared check
// environment. Dependency fields are empty for checks that do not use npm.
type ClearDevCheckEnvironment struct {
	CandidateSHA            string
	SourceManifestID        string
	SourceRootTreeOID       string
	SourceBytes             int64
	SourceItems             int64
	Image                   string
	ImageID                 string
	ApprovedArgvSHA256      string
	CheckEnvironmentID      string
	PackageJSONSHA256       string
	PackageLockSHA256       string
	DependencyCacheKey      string
	DependencyEnvironmentID string
	DependencyTreeSHA256    string
	DependencyBytes         int64
	DependencyItems         int64
	NodeVersion             string
	NPMVersion              string
	ProjectExecutionSHA256  string `json:"projectExecutionSha256,omitempty"`
}

type ClearDevCheckOutcome string

const (
	ClearDevCheckPass       ClearDevCheckOutcome = "PASS"
	ClearDevCheckFail       ClearDevCheckOutcome = "FAIL"
	ClearDevCheckTimedOut   ClearDevCheckOutcome = "TIMED_OUT"
	ClearDevCheckInfraError ClearDevCheckOutcome = "INFRA_ERROR"
)

type ClearDevCheckResult struct {
	TrialCommandExecuted    bool   `json:"trialCommandExecuted,omitempty"`
	TrialArtifactsJSON      string `json:"trialArtifactsJson,omitempty"`
	Outcome                 ClearDevCheckOutcome
	CandidateSHA            string
	SourceManifestID        string
	SourceRootTreeOID       string
	SourceBytes             int64
	SourceItems             int64
	Image                   string
	ImageID                 string
	ApprovedArgvSHA256      string
	CheckEnvironmentID      string
	PackageJSONSHA256       string
	PackageLockSHA256       string
	DependencyCacheKey      string
	DependencyEnvironmentID string
	DependencyTreeSHA256    string
	DependencyBytes         int64
	DependencyItems         int64
	NodeVersion             string
	NPMVersion              string
	ProjectExecutionSHA256  string `json:"projectExecutionSha256,omitempty"`
	ExitCode                int
	TimedOut                bool
	OutputSummary           string
	OutputSHA256            string
	OutputTruncated         bool
}

type ClearDevCheckRunner interface {
	PrepareCandidateChecks(context.Context, ClearDevCheckPreflightRequest) (ClearDevCheckEnvironment, error)
	RunCandidateCheck(context.Context, ClearDevCheckRequest) (ClearDevCheckResult, error)
}

// ClearDevCheckReferenceReleaser is implemented by production check runners
// that persist dependency-cache references for unfinished preflights and
// tasks. It is intentionally optional so non-persistent test runners do not
// need to model the local cache.
type ClearDevCheckReferenceReleaser interface {
	ReleaseCandidateChecks(context.Context, string) error
}

// ClearDevGeneratedProofRequest rebuilds frozen generated files in a disposable checkout.
type ClearDevGeneratedProofRequest struct {
	RunID         string
	WorkspacePath string
	CandidateSHA  string
	Image         string
	Argv          []string
	OutputPaths   []string
	Timeout       time.Duration
	MemoryBytes   int64
	PidsLimit     int64
}

// ClearDevGeneratedProofResult is the rebuild comparison for one candidate.
type ClearDevGeneratedProofResult struct {
	Outcome          ClearDevCheckOutcome
	ImageID          string
	OutputSHA256JSON string
	ReasonCode       string
}

// ClearDevGeneratedProofRunner rebuilds generated files with a frozen argv command.
type ClearDevGeneratedProofRunner interface {
	RunGeneratedProof(context.Context, ClearDevGeneratedProofRequest) (ClearDevGeneratedProofResult, error)
}

// ClearDevRecoveryWorkspaceInspector verifies an existing managed Git worktree without changing it.
type ClearDevRecoveryWorkspaceInspector interface {
	ValidateRecoveryWorkspace(ctx context.Context, path, branch, expectedSHA string) error
}

// ClearDevFinalReviewEvidence keeps large immutable review packets out of the
// initial model context without discarding any evidence.
type ClearDevFinalReviewEvidence interface {
	FinalReviewEvidencePath(context.Context, string) (string, error)
	WriteFinalReviewEvidence(context.Context, string, string, string, string) error
}
