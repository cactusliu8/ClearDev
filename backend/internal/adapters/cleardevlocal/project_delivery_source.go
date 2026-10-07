package cleardevlocal

import (
	"context"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// InspectDeliveredProjectSource retains the registered-repository cleanliness,
// default-ref and origin checks, then resolves the exact immutable commit the
// backend proved was finally delivered. It never checks out, resets or updates
// a branch. Merely calling this observer does not authorize the chosen commit.
func (r *Runner) InspectDeliveredProjectSource(ctx context.Context, workspace, defaultBranch, candidateSHA string) (ports.ClearDevProjectSource, error) {
	if !validCommit(candidateSHA) {
		return ports.ClearDevProjectSource{}, ports.ErrClearDevCandidateInvalid
	}
	observed, err := r.InspectProjectSource(ctx, workspace, defaultBranch)
	if err != nil {
		return observed, err
	}
	resolved, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "--end-of-options", candidateSHA+"^{commit}")
	if err != nil || strings.TrimSpace(string(resolved)) != candidateSHA {
		return observed, ports.ErrClearDevCandidateInvalid
	}
	tree, err := gitOutput(ctx, workspace, "ls-tree", "-r", "--name-only", candidateSHA)
	if err != nil {
		return observed, err
	}
	observed.BaseCommitSHA, observed.Empty = candidateSHA, len(tree) == 0
	return observed, nil
}
