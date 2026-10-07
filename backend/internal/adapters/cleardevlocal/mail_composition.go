package cleardevlocal

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// PrepareMailBuilderBase fast-forwards only the exact clean, controlled Builder
// branch. Complete-task compositions retain the old frozen candidate as an
// ancestor; no reset, detached HEAD, user branch rewrite, or conflict repair is needed.
func (r *Runner) PrepareMailBuilderBase(ctx context.Context, request ports.ClearDevMailBuilderBaseRequest) error {
	if request.ProjectExecution != nil && (core.ValidateProjectExecutionContract(*request.ProjectExecution) != nil || request.ProjectExecution.Selection.RepositoryPath != request.RepoPath) {
		return ports.ErrClearDevCandidateInvalid
	}
	freezeRequest := ports.ClearDevMailFreezeRequest{
		RunID: "prepare-mail-builder-base", RepoPath: request.RepoPath,
		WorkspacePath: request.WorkspacePath, Branch: request.Branch,
		BaseSHA: request.BaseSHA, ParentSHA: request.ExpectedHeadSHA,
	}
	g, err := r.mailFreezeRepository(freezeRequest)
	if err != nil {
		return err
	}
	lockPath, err := durableRunStatePath("cleardev-mail-freezes", "workspace:"+request.WorkspacePath, "mail-freeze")
	if err != nil {
		return err
	}
	unlock, err := lockCheckFile(ctx, lockPath+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := r.mailFreezeRepository(freezeRequest); err != nil {
		return err
	}
	head, err := g.freezeHead(ctx, request.Branch)
	if err != nil || (head != request.ExpectedHeadSHA && head != request.BaseSHA) {
		return ports.ErrClearDevCandidateInvalid
	}
	status, err := g.run(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" && (request.ProjectExecution == nil || !onlyUntrackedProjectDependencies(request.WorkspacePath, []byte(status))) {
		return ports.ErrClearDevWorkspaceDirty
	}
	if _, err := g.run(ctx, nil, "merge-base", "--is-ancestor", request.ExpectedHeadSHA, request.BaseSHA); err != nil {
		return ports.ErrClearDevCandidateNotDescendant
	}
	if head != request.BaseSHA {
		if _, err := g.run(ctx, nil, "merge", "--ff-only", "--no-edit", request.BaseSHA); err != nil {
			return err
		}
	}
	head, err = g.freezeHead(ctx, request.Branch)
	if err != nil || head != request.BaseSHA {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

type mailCompositionState struct {
	Version     int                         `json:"version"`
	Fingerprint string                      `json:"fingerprint"`
	Status      string                      `json:"status"`
	Result      ports.ClearDevComposeResult `json:"result"`
}

// composeMailCandidates keeps the complete reviewed history, including earlier
// trusted freezes before rework. Its receipt makes settled replay exact; an
// unsettled external Git action stops safely without deleting or recomposing it.
func (r *Runner) composeMailCandidates(ctx context.Context, request ports.ClearDevComposeRequest) (ports.ClearDevComposeResult, error) {
	if !validRunID(request.RequestID) || !filepath.IsAbs(request.RepoPath) || !validCommit(request.BaseSHA) || len(request.CandidateSHAs) < 1 || len(request.CandidateSHAs) > 2 {
		return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
	}
	g, err := mailCompositionGit(ctx, request.RepoPath)
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	for _, candidate := range request.CandidateSHAs {
		if !validCommit(candidate) {
			return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
		}
		if _, err := g.run(ctx, nil, "merge-base", "--is-ancestor", request.BaseSHA, candidate); err != nil {
			return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateNotDescendant
		}
	}
	workspace, err := r.composeWorkspace(sha256.Sum256([]byte(request.RequestID)))
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	statePath, err := durableRunStatePath("cleardev-mail-compositions", request.RequestID, "mail-composition")
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	unlock, err := lockCheckFile(ctx, statePath+".lock")
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	defer unlock()
	raw, err := json.Marshal(request)
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	state := mailCompositionState{Version: 1, Fingerprint: sha256Hex(raw), Status: "started", Result: ports.ClearDevComposeResult{WorkspacePath: workspace}}
	created, saved, err := createOrReadJSONState(statePath, "mail-composition", state)
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	branch := core.ComplexCompositionBranch(request.RequestID)
	if !created {
		var prior mailCompositionState
		if json.Unmarshal(saved, &prior) != nil || prior.Version != state.Version || prior.Fingerprint != state.Fingerprint || prior.Result.WorkspacePath != workspace {
			return ports.ClearDevComposeResult{}, ports.ErrClearDevCandidateInvalid
		}
		if prior.Status == "conflict" && len(prior.Result.ConflictPaths) != 0 {
			return prior.Result, ports.ErrClearDevCompositionConflict
		}
		if prior.Status != "settled" || !validCommit(prior.Result.OutputSHA) {
			return ports.ClearDevComposeResult{}, fmt.Errorf("%w: unsettled composition preserved; automatic replay is unsafe", ports.ErrClearDevCandidateInvalid)
		}
		if err := r.verifyMailComposition(ctx, prior.Result, branch); err != nil {
			return ports.ClearDevComposeResult{}, err
		}
		return prior.Result, nil
	}
	if _, err := os.Lstat(workspace); !errors.Is(err, os.ErrNotExist) {
		return ports.ClearDevComposeResult{}, fmt.Errorf("%w: existing composition workspace preserved", ports.ErrClearDevCandidateInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(workspace), 0o750); err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	if _, err := g.run(ctx, nil, "worktree", "add", "-b", branch, workspace, request.BaseSHA); err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	g, err = mailCompositionGit(ctx, workspace)
	if err != nil {
		return ports.ClearDevComposeResult{}, err
	}
	state.Result.WorkspacePath = workspace
	for _, candidate := range request.CandidateSHAs {
		if _, err := g.run(ctx, nil, "merge", "--no-ff", "--no-edit", "--no-verify", candidate); err != nil {
			conflicts, conflictErr := g.run(ctx, nil, "diff", "--name-only", "--diff-filter=U", "-z")
			if conflictErr == nil && conflicts != "" {
				state.Status = "conflict"
				state.Result.ConflictPaths = strings.Split(strings.TrimSuffix(conflicts, "\x00"), "\x00")
				if saveErr := writeJSONState(statePath, "mail-composition", state); saveErr != nil {
					return state.Result, saveErr
				}
				// Keep index, conflict markers and MERGE_HEAD for inspection.
				return state.Result, ports.ErrClearDevCompositionConflict
			}
			return state.Result, err
		}
	}
	state.Result.OutputSHA, err = g.freezeHead(ctx, branch)
	if err != nil {
		return state.Result, err
	}
	if err := r.verifyMailComposition(ctx, state.Result, branch); err != nil {
		return state.Result, err
	}
	state.Status = "settled"
	if err := writeJSONState(statePath, "mail-composition", state); err != nil {
		return state.Result, err
	}
	return state.Result, nil
}

func mailCompositionGit(ctx context.Context, workspace string) (mailFreezeGit, error) {
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil || resolved != workspace || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return mailFreezeGit{}, ports.ErrClearDevCandidateInvalid
	}
	directory, err := gitOutput(ctx, workspace, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return mailFreezeGit{}, err
	}
	return mailFreezeGit{workspace: workspace, directory: strings.TrimSpace(string(directory))}, nil
}

// InspectDeliveryBranch binds an exposed continuation branch to the exact
// completed commit. Standard deliveries use the Builder branch; parallel
// deliveries use the composition branch.
func (r *Runner) InspectDeliveryBranch(ctx context.Context, workspace, branch, sha string) error {
	if !validCommit(sha) || branch == "" || strings.ContainsAny(branch, "\x00\r\n") {
		return ports.ErrClearDevCandidateInvalid
	}
	g, err := mailCompositionGit(ctx, workspace)
	if err != nil {
		return err
	}
	head, err := g.freezeHead(ctx, branch)
	if err != nil || head != sha {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

func (r *Runner) verifyMailComposition(ctx context.Context, result ports.ClearDevComposeResult, branch string) error {
	g, err := mailCompositionGit(ctx, result.WorkspacePath)
	if err != nil {
		return err
	}
	head, err := g.freezeHead(ctx, branch)
	if err != nil || head != result.OutputSHA {
		return ports.ErrClearDevCandidateInvalid
	}
	inspection, err := r.InspectCandidate(ctx, result.WorkspacePath, result.OutputSHA)
	if err != nil {
		return err
	}
	if inspection.CandidateSHA != result.OutputSHA || len(inspection.Paths) != 0 {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}
