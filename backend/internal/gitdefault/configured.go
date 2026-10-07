package gitdefault

import "strings"

// ConfiguredBaseRefCandidates is the shared workspace/source ordering for an
// explicit default branch. Automatic defaults are resolved from repository
// metadata, not by passing the "auto" sentinel or consulting the checkout.
func ConfiguredBaseRefCandidates(defaultBranch string) []string {
	if strings.Contains(defaultBranch, "/") {
		// Preserve Git's resolution for qualified refs and slash branch names.
		return []string{defaultBranch}
	}
	// Match existing worktree creation: a cached remote wins, with a local
	// fallback for repositories without a fetched/default remote branch.
	return []string{"origin/" + defaultBranch, "refs/heads/" + defaultBranch}
}
