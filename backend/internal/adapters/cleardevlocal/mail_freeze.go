package cleardevlocal

import (
	"context"
	"crypto/sha1" //nolint:gosec // Git object identity, not an authority token.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type mailFreezeState struct {
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
	TreeSHA     string `json:"treeSha"`
	Candidate   string `json:"candidate"`
}

func freezeCommit(state mailFreezeState, parent string) []byte {
	return []byte(fmt.Sprintf("tree %s\nparent %s\nauthor ClearDev <cleardev@localhost> 946684800 +0000\ncommitter ClearDev <cleardev@localhost> 946684800 +0000\n\nClearDev trusted candidate %s\n", state.TreeSHA, parent, state.Fingerprint))
}

func freezeCommitID(raw []byte) string {
	hash := sha1.New() //nolint:gosec // Exact Git commit identity.
	_, _ = fmt.Fprintf(hash, "commit %d\x00", len(raw))
	_, _ = hash.Write(raw)
	return hex.EncodeToString(hash.Sum(nil))
}

// FreezeMailCandidate snapshots the exact idle Builder tree. It writes only
// content-addressed objects, its own durable receipt, and the bound Builder's
// ref/index. No working file, main branch, old test or model setting is changed.
func (r *Runner) FreezeMailCandidate(ctx context.Context, request ports.ClearDevMailFreezeRequest) (ports.ClearDevCandidateInspection, error) {
	return r.freezeMailCandidate(ctx, request, func(string) error { return nil })
}

// checkpoint is an internal deterministic crash seam; production supplies no effects.
func (r *Runner) freezeMailCandidate(ctx context.Context, request ports.ClearDevMailFreezeRequest, checkpoint func(string) error) (inspection ports.ClearDevCandidateInspection, retErr error) {
	defer func() {
		if retErr != nil && !errors.Is(retErr, ports.ErrClearDevCandidateInvalid) && !errors.Is(retErr, ports.ErrClearDevCandidateNotDescendant) && !errors.Is(retErr, ports.ErrClearDevMailScopeViolation) && !errors.Is(retErr, context.Canceled) {
			retErr = fmt.Errorf("%w: trusted freeze unavailable: %w", ports.ErrClearDevGitUnavailable, retErr)
		}
	}()
	g, err := r.mailFreezeRepository(request)
	if err != nil {
		return inspection, err
	}
	statePath, err := durableRunStatePath("cleardev-mail-freezes", request.RunID, "mail-freeze")
	if err != nil {
		return inspection, err
	}
	if pathWithin(request.WorkspacePath, statePath) {
		return inspection, ports.ErrClearDevCandidateInvalid
	}
	lockPath, err := durableRunStatePath("cleardev-mail-freezes", "workspace:"+request.WorkspacePath, "mail-freeze")
	if err != nil {
		return inspection, err
	}
	unlock, err := lockCheckFile(ctx, lockPath+".lock")
	if err != nil {
		return inspection, err
	}
	defer unlock()
	// Revalidate paths after waiting for another process's publication.
	if _, err := r.mailFreezeRepository(request); err != nil {
		return inspection, err
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return inspection, err
	}
	fingerprint := sha256Hex(rawRequest)
	state := mailFreezeState{Version: 1, Fingerprint: fingerprint}
	rawState, err := readJSONStateBytes(statePath, "mail-freeze")
	replay := err == nil
	if replay {
		if json.Unmarshal(rawState, &state) != nil || state.Version != 1 || state.Fingerprint != fingerprint || !validCommit(state.TreeSHA) || state.Candidate != freezeCommitID(freezeCommit(state, request.ParentSHA)) {
			return inspection, fmt.Errorf("%w: freeze receipt binding changed", ports.ErrClearDevCandidateInvalid)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return inspection, err
	}
	if _, err := g.run(ctx, nil, "check-ref-format", "refs/heads/"+request.Branch); err != nil {
		return inspection, err
	}
	if _, err := g.run(ctx, nil, "merge-base", "--is-ancestor", request.BaseSHA, request.ParentSHA); err != nil {
		return inspection, ports.ErrClearDevCandidateNotDescendant
	}
	head, err := g.freezeHead(ctx, request.Branch)
	if err != nil || head != request.ParentSHA && (!replay || head != state.Candidate) {
		return inspection, fmt.Errorf("%w: unexpected Builder HEAD", ports.ErrClearDevCandidateInvalid)
	}
	base, err := g.baseFiles(ctx, request.BaseSHA)
	if err != nil {
		return inspection, err
	}
	files, err := g.candidateFiles(ctx, base, request.ProjectExecution != nil)
	if err != nil {
		return inspection, handoffPublicationProof(err, !replay)
	}
	if err := validateFreezeScope(request, base, files); err != nil {
		return inspection, handoffPublicationProof(err, !replay)
	}
	parentTree, err := g.run(ctx, nil, "rev-parse", request.ParentSHA+"^{tree}")
	if err != nil {
		return inspection, err
	}
	parentFiles, err := g.baseFiles(ctx, request.ParentSHA)
	if err != nil {
		return inspection, err
	}
	approvedHandoffIndex, err := approvedBuilderHandoffFreezeIndex(request, base)
	if err != nil {
		return inspection, err
	}
	indexFiles, err := g.indexFiles(ctx)
	allowPublishedIndex := replay && (request.BuilderHandoffSnapshot == nil || head == state.Candidate)
	if err != nil || !freezeFilesEqual(indexFiles, parentFiles) && (approvedHandoffIndex == nil || !freezeFilesEqual(indexFiles, approvedHandoffIndex)) && (!allowPublishedIndex || !freezeFilesEqual(indexFiles, files)) {
		return inspection, fmt.Errorf("%w: Builder index changed outside trusted publication", ports.ErrClearDevCandidateInvalid)
	}
	if replay {
		saved, err := g.baseFiles(ctx, state.Candidate)
		if err != nil || !freezeFilesEqual(files, saved) {
			return inspection, fmt.Errorf("%w: working tree no longer matches frozen receipt", ports.ErrClearDevCandidateInvalid)
		}
	} else {
		temporary, err := acquireCheckTemporary(ctx, request.RunID+":freeze", "source-inspection", checkSourceReservationBytes)
		if err != nil {
			return inspection, err
		}
		defer func() {
			if err := temporary.Release(context.WithoutCancel(ctx)); retErr == nil {
				retErr = err
			}
		}()
		state.TreeSHA, err = g.writeTree(ctx, files)
		if err != nil {
			return inspection, fmt.Errorf("%w: freeze tree unavailable: %w", ports.ErrClearDevGitUnavailable, err)
		}
		if !validCommit(state.TreeSHA) {
			return inspection, ports.ErrClearDevGitUnavailable
		}
		if state.TreeSHA == strings.TrimSpace(parentTree) {
			return inspection, &ports.ClearDevCandidateHandoffError{Cause: ports.ErrClearDevCandidateInvalid, ProblemCode: "NO_IMPLEMENTATION_CHANGE", BeforePublication: true}
		}
		commit := freezeCommit(state, request.ParentSHA)
		state.Candidate = freezeCommitID(commit)
		written, err := g.run(ctx, commit, "hash-object", "-t", "commit", "-w", "--stdin")
		if err != nil || strings.TrimSpace(written) != state.Candidate {
			return inspection, ports.ErrClearDevGitUnavailable
		}
		// The immutable tree/commit are durable before any branch is changed.
		created, saved, err := createOrReadJSONState(statePath, "mail-freeze", state)
		if err != nil {
			return inspection, err
		}
		if !created {
			var existing mailFreezeState
			if json.Unmarshal(saved, &existing) != nil || existing != state {
				return inspection, fmt.Errorf("%w: concurrent freeze has another binding", ports.ErrClearDevCandidateInvalid)
			}
		}
	}
	if err := checkpoint("prepared"); err != nil {
		return inspection, err
	}
	if _, err := r.mailFreezeRepository(request); err != nil {
		return inspection, err
	}
	// Never recapture changed files on replay, or publish a different tree than
	// the one saved before the CAS. Repeated object writes are not new commits.
	again, err := g.candidateFiles(ctx, base, request.ProjectExecution != nil)
	if err != nil || !freezeFilesEqual(files, again) {
		return inspection, fmt.Errorf("%w: working tree changed during freeze", ports.ErrClearDevCandidateInvalid)
	}
	head, err = g.freezeHead(ctx, request.Branch)
	if err != nil {
		return inspection, err
	}
	if request.BuilderHandoffSnapshot != nil {
		currentIndex, err := g.indexFiles(ctx)
		if err != nil || !freezeFilesEqual(currentIndex, indexFiles) {
			return inspection, fmt.Errorf("%w: approved handoff index changed during freeze", ports.ErrClearDevCandidateInvalid)
		}
	}
	if head == request.ParentSHA {
		if _, err := g.run(ctx, nil, "update-ref", "refs/heads/"+request.Branch, state.Candidate, request.ParentSHA); err != nil {
			return inspection, err
		}
	} else if head != state.Candidate {
		return inspection, fmt.Errorf("%w: freeze parent CAS failed", ports.ErrClearDevCandidateInvalid)
	}
	if err := checkpoint("ref-published"); err != nil {
		return inspection, err
	}
	// read-tree WITHOUT -u changes only this worktree's index. A crash after
	// update-ref is recovered from the same prepared receipt and exact bytes.
	indexFiles, err = g.indexFiles(ctx)
	allowPublishedIndex = request.BuilderHandoffSnapshot == nil || replay
	if err != nil || !freezeFilesEqual(indexFiles, parentFiles) && (!allowPublishedIndex || !freezeFilesEqual(indexFiles, files)) && (approvedHandoffIndex == nil || !freezeFilesEqual(indexFiles, approvedHandoffIndex)) {
		return inspection, fmt.Errorf("%w: index changed during freeze", ports.ErrClearDevCandidateInvalid)
	}
	if !freezeFilesEqual(indexFiles, files) {
		if _, err := g.run(ctx, nil, "read-tree", state.Candidate); err != nil {
			return inspection, err
		}
	}
	if err := checkpoint("index-published"); err != nil {
		return inspection, err
	}
	if _, err := r.mailFreezeRepository(request); err != nil {
		return inspection, err
	}
	finalFiles, err := g.candidateFiles(ctx, base, request.ProjectExecution != nil)
	if err != nil || !freezeFilesEqual(files, finalFiles) {
		return inspection, fmt.Errorf("%w: post-publication files changed", ports.ErrClearDevCandidateInvalid)
	}
	head, err = g.freezeHead(ctx, request.Branch)
	if err != nil || head != state.Candidate {
		return inspection, ports.ErrClearDevCandidateInvalid
	}
	diff, err := g.run(ctx, nil, "diff-tree", "--no-commit-id", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-status", "-r", "-z", request.BaseSHA, state.Candidate)
	if err != nil {
		return inspection, err
	}
	paths, err := parseNameStatusZ([]byte(diff))
	return ports.ClearDevCandidateInspection{BaseSHA: request.BaseSHA, CandidateSHA: state.Candidate, Paths: paths}, err
}

func (g mailFreezeGit) freezeHead(ctx context.Context, branch string) (string, error) {
	ref, err := g.run(ctx, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || strings.TrimSpace(ref) != "refs/heads/"+branch {
		return "", ports.ErrClearDevCandidateInvalid
	}
	head, err := g.run(ctx, nil, "rev-parse", "--verify", "HEAD^{commit}")
	return strings.TrimSpace(head), err
}

func pathWithin(root, name string) bool {
	relative, err := filepath.Rel(root, name)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// Only a reciprocal registered-repository linked worktree underneath the
// daemon's managed root is eligible. .git pointers cannot redirect a write.
func (r *Runner) mailFreezeRepository(request ports.ClearDevMailFreezeRequest) (mailFreezeGit, error) {
	invalid := ports.ErrClearDevCandidateInvalid
	if !validRunID(request.RunID) || !validCommit(request.BaseSHA) || !validCommit(request.ParentSHA) || !strings.HasPrefix(request.Branch, "cleardev-complex-builder-") || strings.ContainsAny(request.Branch, "\x00\r\n") {
		return mailFreezeGit{}, invalid
	}
	for _, p := range []string{r.managedRoot, request.RepoPath, request.WorkspacePath} {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !filepath.IsAbs(p) || filepath.Clean(p) != p || resolved != p {
			return mailFreezeGit{}, invalid
		}
	}
	if !pathWithin(r.managedRoot, request.WorkspacePath) || request.WorkspacePath == r.managedRoot || pathWithin(request.WorkspacePath, request.RepoPath) {
		return mailFreezeGit{}, invalid
	}
	dotGit := filepath.Join(request.WorkspacePath, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return mailFreezeGit{}, invalid
	}
	pointer, err := os.ReadFile(dotGit)
	if err != nil || !strings.HasPrefix(string(pointer), "gitdir: ") {
		return mailFreezeGit{}, invalid
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(string(pointer), "gitdir: "))
	common := filepath.Join(request.RepoPath, ".git")
	resolved, err := filepath.EvalSymlinks(gitDir)
	if err != nil || resolved != gitDir || filepath.Dir(gitDir) != filepath.Join(common, "worktrees") {
		return mailFreezeGit{}, invalid
	}
	back, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil || strings.TrimSpace(string(back)) != dotGit {
		return mailFreezeGit{}, invalid
	}
	shared, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil || filepath.Clean(filepath.Join(gitDir, strings.TrimSpace(string(shared)))) != common {
		return mailFreezeGit{}, invalid
	}
	return mailFreezeGit{directory: gitDir, workspace: request.WorkspacePath}, nil
}

// Only an initial confined read before a prepared receipt may carry no-publish
// proof. Replays or late publication errors cannot authorize editable recovery.
func handoffPublicationProof(err error, before bool) error {
	var detail *ports.ClearDevCandidateHandoffError
	if !errors.As(err, &detail) {
		return err
	}
	proof := *detail
	proof.BeforePublication = before
	return &proof
}

func (g mailFreezeGit) candidateFiles(ctx context.Context, base map[string]mailFreezeFile, project bool) (map[string]mailFreezeFile, error) {
	if project {
		return g.workingFilesWithExcludes(ctx, base, []string{"/node_modules/"})
	}
	return g.workingFiles(ctx, base)
}
