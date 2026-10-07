package cleardevlocal

import (
	"context"
	"errors"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// IdentifyMailProjectAtBranch shares exactly the Builder baseline selection.
// Existing project settings select the branch; this does not move main, copy a
// database, reuse old test receipts or alter a completed delivery.
func (r *Runner) IdentifyMailProjectAtBranch(ctx context.Context, workspace, branch string) (ports.ClearDevBaselineResult, error) {
	return r.CheckDevelopmentBaseline(ctx, ports.ClearDevBaselineRequest{RunID: "mail-project-identity", WorkspacePath: workspace, BaseBranch: branch, IdentifyOnly: true})
}

func selectedBaselineSHA(ctx context.Context, request ports.ClearDevBaselineRequest, rootHead string) (string, error) {
	branch := strings.TrimSpace(request.BaseBranch)
	if branch == "" || branch == "auto" {
		return rootHead, nil
	}
	ref := "refs/heads/" + branch
	if _, err := baselineGit(ctx, request.WorkspacePath, "check-ref-format", ref); err != nil {
		return "", errors.New("selected baseline is not a valid local branch")
	}
	sha, err := baselineGit(ctx, request.WorkspacePath, "rev-parse", "--verify", ref+"^{commit}")
	sha = strings.TrimSpace(sha)
	if err != nil || !validCommit(sha) {
		return "", errors.New("selected local baseline branch is unavailable")
	}
	// This sprint supports only forward local increments on the current root.
	// A stale or unrelated branch cannot silently replace existing features.
	if _, err := baselineGit(ctx, request.WorkspacePath, "merge-base", "--is-ancestor", rootHead, sha); err != nil {
		return "", errors.New("selected baseline does not descend from the registered project HEAD")
	}
	return sha, nil
}
