package cleardevlocal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The receipt is written before publishing the import commit. A lost response
// can therefore be reconciled against HEAD without fetching a different source.
type sourcePreparationReceipt struct {
	RequestID string
	URL       string
	Base      string
	Source    string
	Prepared  string
}

func preparationGit(ctx context.Context, path string, args ...string) (string, error) {
	argv := append([]string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "commit.gpgSign=false", "-c", "protocol.ext.allow=never", "-C", path}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=ClearDev", "GIT_AUTHOR_EMAIL=cleardev@localhost", "GIT_COMMITTER_NAME=ClearDev", "GIT_COMMITTER_EMAIL=cleardev@localhost")
	out, err := cmd.Output()
	if err != nil {
		detail := ""
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			detail = strings.TrimSpace(string(exit.Stderr))
			if len(detail) > 8192 {
				detail = detail[:8192] + " [truncated]"
			}
		}
		return "", fmt.Errorf("source preparation git %s failed: %w: %s", args[0], err, detail)
	}
	return strings.TrimSpace(string(out)), nil
}

func validPreparationURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00") || strings.HasPrefix(raw, "-") {
		return false
	}
	if strings.HasPrefix(raw, "git@") && strings.Contains(raw, ":") && !strings.Contains(raw, "::") {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if u.User != nil {
		if _, password := u.User.Password(); password {
			return false
		}
	}
	switch u.Scheme {
	case "https", "http":
		return u.Host != "" && u.User == nil
	case "ssh", "git":
		return u.Host != ""
	case "file":
		return u.Host == "" && filepath.IsAbs(u.Path)
	default:
		return false
	}
}

func writePreparationReceipt(path string, receipt sourcePreparationReceipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".source-receipt-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// PrepareProjectSource downloads into a private ref. Only a clean, physically
// empty checkout with a single empty initialization commit may be populated.
// Existing code is never merged with upstream: its ancestry is only inspected.
func (r *Runner) PrepareProjectSource(ctx context.Context, in ports.ClearDevSourcePreparation) (ports.ClearDevProjectSource, error) {
	var zero ports.ClearDevProjectSource
	if in.RequestID == "" || !validCommit(in.ExpectedBase) || !validPreparationURL(in.RepositoryURL) || in.Current == nil {
		return zero, fmt.Errorf("invalid source preparation request")
	}
	root, err := preparationGit(ctx, in.Workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return zero, err
	}
	actual, err := filepath.EvalSymlinks(in.Workspace)
	if err != nil || filepath.Clean(root) != filepath.Clean(actual) {
		return zero, fmt.Errorf("source target is not the repository root")
	}
	gitdir, err := preparationGit(ctx, in.Workspace, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return zero, err
	}
	unlock, err := lockCheckFile(ctx, filepath.Join(gitdir, "cleardev-source.lock"))
	if err != nil {
		return zero, err
	}
	defer unlock()
	if err := in.Current(ctx); err != nil {
		return zero, err
	}
	// Local filters could execute during checkout. Source preparation never runs
	// project scripts; those configurations require an explicit separate setup.
	filters, _ := preparationGit(ctx, in.Workspace, "config", "--get-regexp", `^filter\.`)
	if filters != "" {
		return zero, fmt.Errorf("source target has checkout filters; preparation requires a plain Git checkout")
	}
	digest := sha256.Sum256([]byte(in.RequestID + "\x00" + in.RepositoryURL + "\x00" + in.ExpectedBase))
	key := hex.EncodeToString(digest[:])
	receiptPath := filepath.Join(gitdir, "cleardev-source-"+key+".json")
	receipt := sourcePreparationReceipt{RequestID: in.RequestID, URL: in.RepositoryURL, Base: in.ExpectedBase}
	if data, readErr := os.ReadFile(receiptPath); readErr == nil {
		if json.Unmarshal(data, &receipt) != nil || receipt.RequestID != in.RequestID || receipt.URL != in.RepositoryURL || receipt.Base != in.ExpectedBase {
			return zero, fmt.Errorf("source preparation receipt does not match request")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return zero, readErr
	}
	observed, err := r.InspectProjectSource(ctx, in.Workspace, in.Branch)
	if err != nil {
		return zero, err
	}
	if receipt.Prepared != "" && observed.BaseCommitSHA == receipt.Prepared {
		if err := verifyPreparationCommit(ctx, in.Workspace, receipt); err != nil {
			return zero, err
		}
		return observed, nil
	}
	if observed.BaseCommitSHA != in.ExpectedBase {
		return zero, fmt.Errorf("source target baseline changed")
	}
	originalBranch := ""
	if observed.Empty {
		originalBranch, err = preparationGit(ctx, in.Workspace, "symbolic-ref", "-q", "HEAD")
		if err != nil {
			return zero, fmt.Errorf("source target must be on a local branch")
		}
		if observed.ResolvedRef != originalBranch {
			return zero, fmt.Errorf("empty source target must select its checked-out local branch, not a remote tracking ref")
		}
		head, err := preparationGit(ctx, in.Workspace, "rev-parse", "HEAD")
		if err != nil || head != in.ExpectedBase {
			return zero, fmt.Errorf("source target must have the selected empty branch checked out")
		}
		history, err := preparationGit(ctx, in.Workspace, "rev-list", "--count", in.ExpectedBase)
		if err != nil || history != "1" {
			return zero, fmt.Errorf("source target contains existing history; no files were replaced")
		}
		entries, err := os.ReadDir(in.Workspace)
		if err != nil {
			return zero, err
		}
		for _, entry := range entries {
			if entry.Name() != ".git" {
				return zero, ports.ErrClearDevWorkspaceDirty
			}
		}
	}
	sourceRef := "refs/cleardev/source/" + key
	if receipt.Source == "" {
		// A successful private ref update survives interruption before receipt save.
		source, _ := preparationGit(ctx, in.Workspace, "rev-parse", "--verify", sourceRef+"^{commit}")
		if !validCommit(source) {
			if _, err := preparationGit(ctx, in.Workspace, "fetch", "--no-tags", "--no-recurse-submodules", "--", in.RepositoryURL, "HEAD:"+sourceRef); err != nil {
				return zero, err
			}
			source, err = preparationGit(ctx, in.Workspace, "rev-parse", "--verify", sourceRef+"^{commit}")
			if err != nil {
				return zero, err
			}
		}
		receipt.Source = source
		if err := writePreparationReceipt(receiptPath, receipt); err != nil {
			return zero, err
		}
	}
	// Always verify that the receipt still names the downloaded ref, not an
	// arbitrary local commit substituted into the receipt.
	fetched, err := preparationGit(ctx, in.Workspace, "rev-parse", "--verify", sourceRef+"^{commit}")
	if err != nil || fetched != receipt.Source {
		return zero, fmt.Errorf("downloaded source receipt changed")
	}
	if !observed.Empty {
		if _, err := preparationGit(ctx, in.Workspace, "merge-base", in.ExpectedBase, receipt.Source); err != nil {
			return zero, fmt.Errorf("existing project has no verified ancestry with the chosen source")
		}
		if err := in.Current(ctx); err != nil {
			return zero, err
		}
		current, err := r.InspectProjectSource(ctx, in.Workspace, in.Branch)
		if err != nil {
			return zero, err
		}
		if current.BaseCommitSHA != in.ExpectedBase || current.RepositoryURL != observed.RepositoryURL {
			return zero, fmt.Errorf("existing source changed during download")
		}
		return current, nil
	}
	tree, err := preparationGit(ctx, in.Workspace, "rev-parse", receipt.Source+"^{tree}")
	if err != nil {
		return zero, err
	}
	if receipt.Prepared == "" {
		receipt.Prepared, err = preparationGit(ctx, in.Workspace, "commit-tree", tree, "-p", in.ExpectedBase, "-p", receipt.Source, "-m", "Prepare selected project source")
		if err != nil {
			return zero, err
		}
		if err := writePreparationReceipt(receiptPath, receipt); err != nil {
			return zero, err
		}
	}
	if err := verifyPreparationCommit(ctx, in.Workspace, receipt); err != nil {
		return zero, err
	}
	if err := in.Current(ctx); err != nil {
		return zero, err
	}
	// Check again after the network operation. merge --ff-only also refuses
	// untracked collisions and changed tracked files; never reset or clean.
	current, err := r.InspectProjectSource(ctx, in.Workspace, in.Branch)
	if err != nil || current.BaseCommitSHA != in.ExpectedBase || current.RepositoryURL != observed.RepositoryURL {
		return zero, fmt.Errorf("source target changed during download")
	}
	entries, err := os.ReadDir(in.Workspace)
	if err != nil {
		return zero, err
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return zero, ports.ErrClearDevWorkspaceDirty
		}
	}
	branch, err := preparationGit(ctx, in.Workspace, "symbolic-ref", "-q", "HEAD")
	if err != nil || branch != originalBranch {
		return zero, fmt.Errorf("source checkout branch changed during download")
	}
	head, err := preparationGit(ctx, in.Workspace, "rev-parse", "HEAD")
	if err != nil || head != in.ExpectedBase {
		return zero, fmt.Errorf("source checkout version changed during download")
	}
	if _, err := preparationGit(ctx, in.Workspace, "merge", "--ff-only", "--no-edit", receipt.Prepared); err != nil {
		return zero, err
	}
	result, err := r.InspectProjectSource(ctx, in.Workspace, in.Branch)
	if err != nil {
		return zero, err
	}
	if result.BaseCommitSHA != receipt.Prepared {
		return zero, fmt.Errorf("prepared source is not the selected baseline")
	}
	return result, nil
}

func verifyPreparationCommit(ctx context.Context, workspace string, receipt sourcePreparationReceipt) error {
	digest := sha256.Sum256([]byte(receipt.RequestID + "\x00" + receipt.URL + "\x00" + receipt.Base))
	fetched, err := preparationGit(ctx, workspace, "rev-parse", "--verify", "refs/cleardev/source/"+hex.EncodeToString(digest[:])+"^{commit}")
	if err != nil || fetched != receipt.Source {
		return fmt.Errorf("downloaded source receipt changed")
	}
	if !validCommit(receipt.Source) || !validCommit(receipt.Prepared) {
		return fmt.Errorf("source receipt has invalid commits")
	}
	parents, err := preparationGit(ctx, workspace, "show", "-s", "--format=%P", receipt.Prepared)
	if err != nil || parents != receipt.Base+" "+receipt.Source {
		return fmt.Errorf("prepared source lost its exact parents")
	}
	tree, err := preparationGit(ctx, workspace, "rev-parse", receipt.Source+"^{tree}")
	if err != nil {
		return err
	}
	preparedTree, err := preparationGit(ctx, workspace, "rev-parse", receipt.Prepared+"^{tree}")
	if err != nil || tree != preparedTree {
		return fmt.Errorf("prepared source tree differs from downloaded source")
	}
	return nil
}
