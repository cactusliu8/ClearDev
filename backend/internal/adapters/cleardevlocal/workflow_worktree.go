package cleardevlocal

import (
	"context"
	"encoding/json"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// InspectWorkflowWorkingTree binds unfinished work without changing files,
// index, refs, candidates or review evidence. It reuses the confined freeze reader.
func (r *Runner) InspectWorkflowWorkingTree(ctx context.Context, request ports.ClearDevMailFreezeRequest) (string, error) {
	if request.ProjectExecution == nil || request.RepoPath != request.ProjectExecution.Selection.RepositoryPath {
		return "", ports.ErrClearDevCandidateInvalid
	}
	if err := core.ValidateProjectExecutionContract(*request.ProjectExecution); err != nil {
		return "", err
	}
	g, err := r.mailFreezeRepository(request)
	if err != nil {
		return "", err
	}
	identity := func() error {
		head, err := g.run(ctx, nil, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
		branch, err := g.run(ctx, nil, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return err
		}
		if strings.TrimSpace(head) != request.ParentSHA || strings.TrimSpace(branch) != request.Branch {
			return ports.ErrClearDevCandidateInvalid
		}
		return nil
	}
	if err := identity(); err != nil {
		return "", err
	}
	base, err := g.baseFiles(ctx, request.ParentSHA)
	if err != nil {
		return "", err
	}
	files, err := g.workingFilesWithExcludes(ctx, base, []string{"node_modules/"})
	if err != nil {
		return "", err
	}
	// This is a continuation receipt, not candidate publication. Keep even
	// out-of-scope source so the Builder can reconcile it with the Planner.
	// The final freeze independently enforces the approved write boundaries.
	index, err := g.run(ctx, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return "", err
	}
	indexNames := make(map[string]bool, len(base))
	if index != "" {
		for _, line := range strings.Split(strings.TrimSuffix(index, "\x00"), "\x00") {
			meta, name, ok := strings.Cut(line, "\t")
			fields := strings.Fields(meta)
			if !ok || len(fields) != 3 || fields[2] != "0" || (fields[0] != "100644" && fields[0] != "100755") || !validCommit(fields[1]) || !freezePathValid(name) {
				return "", ports.ErrClearDevMailScopeViolation
			}
			if indexNames[name] {
				return "", ports.ErrClearDevCandidateInvalid
			}
			if original, exists := base[name]; !exists || original.oid != fields[1] {
				kind, err := g.run(ctx, nil, "cat-file", "-t", fields[1])
				if err != nil || strings.TrimSpace(kind) != "blob" {
					return "", ports.ErrClearDevCandidateInvalid
				}
			}
			indexNames[name] = true
		}
	}
	hashes := make(map[string][2]string, len(files))
	for name, file := range files {
		hashes[name] = [2]string{file.mode, file.oid}
	}
	raw, err := json.Marshal(struct {
		Head, Index string
		Files       map[string][2]string
	}{request.ParentSHA, index, hashes})
	if err != nil {
		return "", err
	}
	again, err := g.workingFilesWithExcludes(ctx, base, []string{"node_modules/"})
	if err != nil {
		return "", err
	}
	indexAgain, err := g.run(ctx, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return "", err
	}
	if !freezeFilesEqual(files, again) || indexAgain != index {
		return "", ports.ErrClearDevWorkspaceDirty
	}
	if err := identity(); err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}
