package ports

import (
	"context"
	"errors"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var (
	// ErrBuilderHandoffInvalid identifies invalid sealed material or bindings.
	ErrBuilderHandoffInvalid = errors.New("builder handoff snapshot is invalid")
	// ErrBuilderHandoffChanged means observed source or target facts changed.
	ErrBuilderHandoffChanged = errors.New("builder handoff source changed")
	// ErrBuilderHandoffLimit identifies material exceeding a handoff bound.
	ErrBuilderHandoffLimit = errors.New("builder handoff material exceeds its bound")
	// ErrBuilderHandoffBeforeCopy proves Restore did not attempt to change
	// HEAD, the index, or working files. Object import alone does not change them.
	ErrBuilderHandoffBeforeCopy = errors.New("builder handoff failed before copying target state")
	// ErrBuilderSessionOperationBusy is a local admission refusal before a new
	// lifecycle operation has been claimed or its external action attempted.
	ErrBuilderSessionOperationBusy = errors.New("builder lifecycle operation is already open")
	// ErrBuilderSessionFenced identifies an identity retired by an exact grant.
	ErrBuilderSessionFenced = errors.New("builder session has been retired by an authorized handoff")
)

// ClearDevBuilderHandoffSnapshotRequest is daemon-owned. Its scope is the
// original task contract; shared paths remain forbidden without authority.
type ClearDevBuilderHandoffSnapshotRequest struct {
	SnapshotKey                string                         `json:"snapshotKey"`
	RepoPath                   string                         `json:"repoPath"`
	WorkspacePath              string                         `json:"workspacePath"`
	Branch                     string                         `json:"branch"`
	BaseSHA                    string                         `json:"baseSha"`
	ExecutionPackageSHA256     string                         `json:"executionPackageSha256,omitempty"`
	WritePaths                 []string                       `json:"writePaths"`
	GeneratedPaths             []string                       `json:"generatedPaths,omitempty"`
	SharedPathsRequireApproval []string                       `json:"sharedPathsRequireApproval,omitempty"`
	ForbiddenPaths             []string                       `json:"forbiddenPaths"`
	ProjectExecution           *core.ProjectExecutionContract `json:"projectExecution,omitempty"`
	HandoffText                string                         `json:"handoffText"`
}

// ClearDevBuilderHandoffSnapshot references sealed AO-owned material. It never
// includes working-file bytes in an HTTP read model or a decision result.
type ClearDevBuilderHandoffSnapshot struct {
	Version             int    `json:"version"`
	SnapshotKey         string `json:"snapshotKey"`
	RepoPath            string `json:"repoPath"`
	SourceWorkspacePath string `json:"sourceWorkspacePath"`
	SourceBranch        string `json:"sourceBranch"`
	BaseSHA             string `json:"baseSha"`
	HeadSHA             string `json:"headSha"`
	ManifestPath        string `json:"manifestPath"`
	ManifestSHA256      string `json:"manifestSha256"`
	ObjectsPath         string `json:"objectsPath"`
	ObjectsSHA256       string `json:"objectsSha256"`
	HandoffPath         string `json:"handoffPath"`
	HandoffSHA256       string `json:"handoffSha256"`
	SnapshotSHA256      string `json:"snapshotSha256"`
	IndexSHA256         string `json:"indexSha256"`
	FileCount           int    `json:"fileCount"`
	ContentBytes        int64  `json:"contentBytes"`
	ObjectCount         int    `json:"objectCount"`
	ObjectBytes         int64  `json:"objectBytes"`
	TotalBytes          int64  `json:"totalBytes"`
}

// ClearDevBuilderHandoffTarget binds an independent managed destination and
// distinguishes its admission base from the original dispatch base.
type ClearDevBuilderHandoffTarget struct {
	RepoPath      string
	WorkspacePath string
	Branch        string
	BaseSHA       string
	// The new session retains the contract's initial admission base. BaseSHA
	// remains the current dispatch base; an empty initial base defaults to it.
	InitialBaseSHA string
}

// ClearDevBuilderHandoffSnapshotter seals bounded original material, restores
// only its admitted target, and exposes separate read-only verification.
type ClearDevBuilderHandoffSnapshotter interface {
	CaptureBuilderHandoff(context.Context, ClearDevBuilderHandoffSnapshotRequest) (ClearDevBuilderHandoffSnapshot, error)
	VerifyBuilderHandoff(context.Context, ClearDevBuilderHandoffSnapshot) error
	RestoreBuilderHandoff(context.Context, ClearDevBuilderHandoffSnapshot, ClearDevBuilderHandoffTarget) error
	VerifyBuilderHandoffTarget(context.Context, ClearDevBuilderHandoffSnapshot, ClearDevBuilderHandoffTarget) error
}

// BuilderHandoffContext is a private spawn field. It belongs to the standing
// system context, never an initial user message or a public session request.
type BuilderHandoffContext struct {
	ReferencePath  string `json:"referencePath"`
	SHA256         string `json:"sha256"`
	SnapshotSHA256 string `json:"snapshotSha256"`
}

// BuilderHandoffSessionObservation carries current identity and exact observed
// process/native/lifecycle facts. A false stop flag provides no exit proof.
type BuilderHandoffSessionObservation struct {
	Session            domain.SessionRecord
	NativeRef          NativeSessionRef
	NativeAvailability NativeSessionAvailability
	RuntimeStopped     bool
	NoOpenTurn         bool
	OperationIdle      bool
	ObservedAt         time.Time
}

// BuilderHandoffSessionObserver probes only the exact persisted identity.
// False and Unknown never prove a missing or unobservable worker has stopped.
type BuilderHandoffSessionObserver interface {
	ObserveBuilderHandoffSession(context.Context, domain.SessionID) (BuilderHandoffSessionObservation, error)
}

// ChatProcessObserver is a nonblocking, read-only probe of the exact owned
// provider process. Only completion of its OS wait may return true. Stream
// closure, a missing registry entry, and controller enums are insufficient.
type ChatProcessObserver interface {
	ProcessStopped() bool
}

type builderOperationContextKey struct{}

// WithBuilderSessionOperation carries a daemon-owned operation already claimed
// by Session Manager into the Chat delivery path. No HTTP input sets this value.
func WithBuilderSessionOperation(ctx context.Context, id domain.SessionID) context.Context {
	return context.WithValue(ctx, builderOperationContextKey{}, id)
}

// BuilderSessionOperationAdmitted reports a daemon-owned claim for this exact
// session, allowing nested private delivery calls to share one operation.
func BuilderSessionOperationAdmitted(ctx context.Context, id domain.SessionID) bool {
	claimed, ok := ctx.Value(builderOperationContextKey{}).(domain.SessionID)
	return ok && claimed == id
}
