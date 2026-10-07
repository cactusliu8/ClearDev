package cleardevlocal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/gitdefault"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// InspectProjectSource does not run package scripts or trust a repository's
// description of itself. A dirty/unborn repository must be resolved by the
// existing project registration/baseline workflow, not silently initialized here.
func (r *Runner) InspectProjectSource(ctx context.Context, workspace, branch string) (ports.ClearDevProjectSource, error) {
	var result ports.ClearDevProjectSource
	// Status can invoke clean filters or fsmonitor. Discovery is read-only and
	// must not execute project-provided helpers while inspecting user files.
	filters, filterErr := gitOutput(ctx, workspace, "config", "--local", "--get-regexp", `^filter\.`)
	if len(filters) > 0 {
		return result, fmt.Errorf("%w: source inspection does not execute configured Git filters", ports.ErrClearDevGitUnavailable)
	}
	if filterErr != nil {
		var exit *exec.ExitError
		if !errors.As(filterErr, &exit) || exit.ExitCode() != 1 {
			return result, filterErr
		}
	}
	status, err := gitOutput(ctx, workspace, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return result, fmt.Errorf("%w: inspect project status: %w", ports.ErrClearDevGitUnavailable, err)
	}
	if len(status) != 0 {
		return result, ports.ErrClearDevWorkspaceDirty
	}
	baseBranch := (domain.ProjectConfig{DefaultBranch: strings.TrimSpace(branch)}).WorktreeBaseBranch()
	refs := gitdefault.ConfiguredBaseRefCandidates(baseBranch)
	if baseBranch == "" {
		// Share native default metadata without its legacy config backfill or
		// live fetch. A later real ref change remains guarded by the saved SHA.
		resolved, err := gitdefault.New("", nil).InspectReadOnly(ctx, workspace)
		if err != nil {
			return result, fmt.Errorf("%w: resolve automatic project source: %w", ports.ErrClearDevCandidateInvalid, err)
		}
		refs = []string{resolved.Ref}
	}
	for _, ref := range refs {
		resolved, err := gitOutput(ctx, workspace, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
		if err == nil {
			result.BaseCommitSHA = strings.TrimSpace(string(resolved))
			name, refErr := gitOutput(ctx, workspace, "rev-parse", "--verify", "--symbolic-full-name", "--end-of-options", ref)
			if refErr != nil {
				return result, refErr
			}
			result.ResolvedRef = strings.TrimSpace(string(name))
			break
		}
	}
	if !validCommit(result.BaseCommitSHA) {
		return result, ports.ErrClearDevCandidateInvalid
	}
	tree, err := gitOutput(ctx, workspace, "ls-tree", "-r", "--name-only", result.BaseCommitSHA)
	if err != nil {
		return result, err
	}
	result.Empty = len(tree) == 0
	remote, err := gitOutput(ctx, workspace, "remote", "get-url", "origin")
	if err == nil {
		result.RepositoryURL = strings.TrimSpace(string(remote))
	}
	return result, nil
}
