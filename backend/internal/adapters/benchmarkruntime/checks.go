package benchmarkruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CheckImage is the immutable v17 image, including its fixed Node 22.23.1.
// The ordinary product's default image remains unchanged. Results retain the
// effective image and tool versions returned by the existing trusted checker.
const CheckImage = "sha256:8c2b17457ca964a066a0ebd1c0cbd3d4617103407d2ba6873cc609b9b593de8c"

// BoundChecks limits the fixed checker image override to bound workspaces.
// Candidate checks use StandardCandidateCheckImage; G3/G4 specialist checks use
// StandardRequiredImage. Both are rewritten to CheckImage.
type BoundChecks struct {
	binding  Binding
	delegate ports.ClearDevCheckRunner
}

// NewChecks wraps the existing trusted candidate checker.
func NewChecks(binding Binding, delegate ports.ClearDevCheckRunner) *BoundChecks {
	return &BoundChecks{binding: binding, delegate: delegate}
}

func allowedBoundCheckImage(image string) bool {
	return image == core.StandardCandidateCheckImage || image == core.StandardRequiredImage
}

func (c *BoundChecks) validate(workspace, image string) error {
	root := filepath.Join(c.binding.DataRoot, "worktrees")
	rel, err := filepath.Rel(root, workspace)
	if !runtimeRecordClasses[c.binding.RecordClass] || c.binding.PlanCommit != PlanCommit || err != nil || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !allowedBoundCheckImage(image) {
		return fmt.Errorf("bound candidate checker request mismatch")
	}
	return nil
}

// PrepareCandidateChecks validates the scope before preparing the fixed image.
func (c *BoundChecks) PrepareCandidateChecks(ctx context.Context, request ports.ClearDevCheckPreflightRequest) (ports.ClearDevCheckEnvironment, error) {
	if err := c.validate(request.WorkspacePath, request.Image); err != nil {
		return ports.ClearDevCheckEnvironment{}, err
	}
	// The live benchmark image override is part of the formal benchmark
	// contract, not the generic project execution contract. PROJECT_EXECUTION_V1
	// receipts must retain StandardCandidateCheckImage exactly or their durable
	// candidate/environment binding becomes invalid.
	if request.ProjectExecution == nil {
		request.Image = CheckImage
	} else if request.Image != core.StandardCandidateCheckImage {
		return ports.ClearDevCheckEnvironment{}, fmt.Errorf("bound project checker request mismatch")
	}
	return c.delegate.PrepareCandidateChecks(ctx, request)
}

// RunCandidateCheck delegates the actual check with the bound image. Explicit
// project executions keep their separately versioned standard image contract.
func (c *BoundChecks) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	if err := c.validate(request.WorkspacePath, request.Image); err != nil {
		return ports.ClearDevCheckResult{}, err
	}
	if request.ProjectExecution == nil {
		request.Image = CheckImage
	} else if request.Image != core.StandardCandidateCheckImage {
		return ports.ClearDevCheckResult{}, fmt.Errorf("bound project checker request mismatch")
	}
	return c.delegate.RunCandidateCheck(ctx, request)
}

// PrepareProjectExecution preserves the delegate's explicit generic-project
// capability through the benchmark wrapper. It does not apply benchmark image
// policy; the delegate probes the versioned project runtime contract itself.
func (c *BoundChecks) PrepareProjectExecution(ctx context.Context, contract core.ProjectExecutionContract) error {
	preparer, ok := c.delegate.(ports.ClearDevProjectExecutionPreparer)
	if !ok {
		return fmt.Errorf("bound project execution preparation unavailable")
	}
	return preparer.PrepareProjectExecution(ctx, contract)
}

// PrepareProjectResult likewise keeps completed-project runtime preparation
// available while still requiring a workspace owned by this daemon's managed
// worktree root.
func (c *BoundChecks) PrepareProjectResult(ctx context.Context, workspace, candidate string, contract core.ProjectExecutionContract) (ports.ClearDevProjectResultSource, error) {
	if err := c.validate(workspace, core.StandardCandidateCheckImage); err != nil {
		return ports.ClearDevProjectResultSource{}, err
	}
	preparer, ok := c.delegate.(ports.ClearDevProjectResultPreparer)
	if !ok {
		return ports.ClearDevProjectResultSource{}, fmt.Errorf("bound project result preparation unavailable")
	}
	return preparer.PrepareProjectResult(ctx, workspace, candidate, contract)
}

// ReleaseCandidateChecks releases the existing checker's owned reference.
func (c *BoundChecks) ReleaseCandidateChecks(ctx context.Context, runID string) error {
	releaser, ok := c.delegate.(ports.ClearDevCheckReferenceReleaser)
	if !ok {
		return fmt.Errorf("bound candidate checker release unavailable")
	}
	return releaser.ReleaseCandidateChecks(ctx, runID)
}
