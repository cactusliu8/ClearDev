package cleardevlocal

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git object identity, not a security credential.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const mailFreezeMaxBytes = 16 << 20
const mailFreezeMaxFiles = 2048

type mailFreezeGit struct{ directory, workspace string }
type mailFreezeFile struct {
	mode, oid string
	data      []byte
}

// Plumbing never asks Git to run repository filters, hooks, external diff,
// credential helpers or fsmonitor. File contents are hashed via stdin, without
// --path, and index entries are supplied explicitly from a confined read.
func (g mailFreezeGit) run(ctx context.Context, input []byte, args ...string) (string, error) {
	prefix := make([]string, 0, 13+len(args))
	prefix = append(prefix, "--no-replace-objects", "--git-dir="+g.directory, "--work-tree="+g.workspace,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"-c", "core.splitIndex=false", "-c", "commit.gpgsign=false")
	command := exec.CommandContext(ctx, "git", append(prefix, args...)...) //nolint:gosec // Fixed plumbing argv; no shell or model commands.
	command.Dir = g.workspace
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") {
			command.Env = append(command.Env, item)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0",
		"GIT_AUTHOR_NAME=ClearDev", "GIT_AUTHOR_EMAIL=cleardev@localhost", "GIT_COMMITTER_NAME=ClearDev", "GIT_COMMITTER_EMAIL=cleardev@localhost",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	command.Stdin = bytes.NewReader(input)
	output := &candidateDiagnosticBuffer{limit: mailFreezeMaxBytes + 1}
	diagnostic := &candidateDiagnosticBuffer{limit: 4096}
	command.Stdout, command.Stderr = output, diagnostic
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%w: freeze git %s: %w: %s", ports.ErrClearDevGitUnavailable, args[0], err, diagnostic.String())
	}
	raw := output.String()
	if len(raw) > mailFreezeMaxBytes {
		return "", fmt.Errorf("%w: freeze Git output exceeds bound", ports.ErrClearDevCandidateInvalid)
	}
	return raw, nil
}

func freezeBlobID(data []byte) string {
	hash := sha1.New() //nolint:gosec // Exact Git blob identity.
	_, _ = fmt.Fprintf(hash, "blob %d\x00", len(data))
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func (g mailFreezeGit) baseFiles(ctx context.Context, sha string) (map[string]mailFreezeFile, error) {
	raw, err := g.run(ctx, nil, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return nil, err
	}
	files := map[string]mailFreezeFile{}
	if raw == "" {
		return files, nil
	}
	total := 0
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
		meta, name, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !freezePathValid(name) || !slices.Contains([]string{"100644", "100755"}, fields[0]) || fields[1] != "blob" || !validCommit(fields[2]) || len(files) >= mailFreezeMaxFiles {
			return nil, fmt.Errorf("%w: invalid baseline entry", ports.ErrClearDevCandidateInvalid)
		}
		data, err := g.run(ctx, nil, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > mailFreezeMaxBytes || freezeBlobID([]byte(data)) != fields[2] {
			return nil, fmt.Errorf("%w: baseline object or size invalid", ports.ErrClearDevCandidateInvalid)
		}
		files[name] = mailFreezeFile{fields[0], fields[2], []byte(data)}
	}
	return files, nil
}

func freezePathValid(name string) bool {
	return name != "" && filepath.ToSlash(filepath.Clean(name)) == name && !filepath.IsAbs(name) && !strings.ContainsAny(name, "\\\x00\t\r\n") &&
		name != ".." && !strings.HasPrefix(name, "../") && name != ".git" && !strings.HasPrefix(name, ".git/")
}

// Working bytes are read through os.Root. Symlinks, submodules, devices and
// torn/replaced files are rejected instead of following a path out of the tree.
func (g mailFreezeGit) workingFiles(ctx context.Context, baseline map[string]mailFreezeFile) (map[string]mailFreezeFile, error) {
	return g.workingFilesWithExcludes(ctx, baseline, nil)
}

// Exclusions apply only to untracked runtime material, never to tracked source.
// Project candidates and continuation receipts share the root npm runtime exclusion.
func (g mailFreezeGit) workingFilesWithExcludes(ctx context.Context, baseline map[string]mailFreezeFile, excludes []string) (map[string]mailFreezeFile, error) {
	raw, err := g.run(ctx, nil, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, err
	}
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	for _, pattern := range excludes {
		args = append(args, "--exclude="+pattern)
	}
	untracked, err := g.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	raw += untracked
	names := map[string]bool{}
	for name := range baseline {
		names[name] = true
	}
	for _, name := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
		if name == "" && raw == "" {
			continue
		}
		if !freezePathValid(name) {
			return nil, fmt.Errorf("%w: invalid working path", ports.ErrClearDevCandidateInvalid)
		}
		names[name] = true
	}
	if len(names) > mailFreezeMaxFiles {
		return nil, &ports.ClearDevCandidateHandoffError{Cause: ports.ErrClearDevCandidateInvalid, ProblemCode: "SOURCE_LIMIT"}
	}
	root, err := os.OpenRoot(g.workspace)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	files := map[string]mailFreezeFile{}
	total := 0
	for name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			info, err := root.Lstat(parent)
			if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !info.IsDir() {
				return nil, &ports.ClearDevCandidateHandoffError{Cause: ports.ErrClearDevMailScopeViolation, ProblemCode: "UNSAFE_PATH", Path: parent}
			}
		}
		before, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !before.Mode().IsRegular() || before.Size() > mailFreezeMaxBytes {
			return nil, &ports.ClearDevCandidateHandoffError{Cause: ports.ErrClearDevMailScopeViolation, ProblemCode: "UNSAFE_PATH", Path: name}
		}
		file, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		opened, statErr := file.Stat()
		data, readErr := io.ReadAll(io.LimitReader(file, int64(mailFreezeMaxBytes-total)+1))
		closeErr := file.Close()
		after, afterErr := root.Lstat(name)
		if statErr != nil || afterErr != nil || readErr != nil || closeErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != int64(len(data)) || !before.ModTime().Equal(after.ModTime()) {
			return nil, fmt.Errorf("%w: file changed while freezing %s", ports.ErrClearDevCandidateInvalid, name)
		}
		total += len(data)
		if total > mailFreezeMaxBytes {
			return nil, &ports.ClearDevCandidateHandoffError{Cause: ports.ErrClearDevCandidateInvalid, ProblemCode: "SOURCE_LIMIT"}
		}
		mode := "100644"
		if before.Mode()&0o111 != 0 {
			mode = "100755"
		}
		files[name] = mailFreezeFile{mode, freezeBlobID(data), data}
	}
	return files, nil
}

func freezeFilesEqual(left, right map[string]mailFreezeFile) bool {
	if len(left) != len(right) {
		return false
	}
	for name, a := range left {
		b, exists := right[name]
		if !exists || a.mode != b.mode || a.oid != b.oid {
			return false
		}
	}
	return true
}

func validateFreezeScope(request ports.ClearDevMailFreezeRequest, base, candidate map[string]mailFreezeFile) error {
	project := request.ProjectExecution != nil
	if project {
		if err := core.ValidateProjectExecutionContract(*request.ProjectExecution); err != nil {
			return err
		}
		if request.RepoPath != request.ProjectExecution.Selection.RepositoryPath {
			return ports.ErrClearDevCandidateInvalid
		}
		basisRules := core.PathRules{WritePaths: request.ProjectExecution.Basis.WritePaths}
		for _, name := range request.WritePaths {
			classification, err := basisRules.ClassifyPath(name)
			if err != nil || classification != core.PathAllowed || !core.ProjectPath(name, false) {
				return ports.ErrClearDevMailScopeViolation
			}
		}
	}
	rules := core.PathRules{WritePaths: request.WritePaths, ForbiddenPaths: request.ForbiddenPaths}
	if len(request.WritePaths) == 0 || rules.Validate() != nil {
		return ports.ErrClearDevMailScopeViolation
	}
	changed := map[string]bool{}
	for name, before := range base {
		after, exists := candidate[name]
		if !exists || before.oid != after.oid || before.mode != after.mode {
			changed[name] = true
		}
		if !project && core.MailTestPath(name) && (!exists || before.mode != after.mode || !bytes.HasPrefix(after.data, before.data)) {
			return fmt.Errorf("%w: old test must retain its mode and byte prefix: %s", ports.ErrClearDevMailScopeViolation, name)
		}
	}
	for name := range candidate {
		if _, exists := base[name]; !exists {
			changed[name] = true
		}
	}
	for name := range changed {
		classification, err := rules.ClassifyPath(name)
		if err != nil || classification != core.PathAllowed || (project && !core.ProjectPath(name, false)) || (!project && !core.MailWritePathAllowed(name)) {
			return &ports.ClearDevCandidateHandoffError{Cause: ports.ErrClearDevMailScopeViolation, ProblemCode: "PATH_SCOPE", Path: name}
		}
		if !project && core.MailTestPath(name) && !core.MailExtraTestPath(name) && (!strings.HasPrefix(name, "test/") || !slices.Contains([]string{".json", ".txt", ".csv", ".md"}, filepath.Ext(name))) {
			return fmt.Errorf("%w: changed test has no approved entry: %s", ports.ErrClearDevMailScopeViolation, name)
		}
	}
	return nil
}

// Index verification reads only stage records. write-tree can refresh cached
// entries through a configured clean filter, so it is deliberately not used.
func (g mailFreezeGit) indexFiles(ctx context.Context) (map[string]mailFreezeFile, error) {
	raw, err := g.run(ctx, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	files := map[string]mailFreezeFile{}
	if raw == "" {
		return files, nil
	}
	for _, entry := range strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		parts := strings.Fields(meta)
		if !ok || !freezePathValid(name) || len(parts) != 3 || parts[2] != "0" || !validCommit(parts[1]) || !slices.Contains([]string{"100644", "100755"}, parts[0]) || len(files) >= mailFreezeMaxFiles {
			return nil, ports.ErrClearDevCandidateInvalid
		}
		files[name] = mailFreezeFile{mode: parts[0], oid: parts[1]}
	}
	return files, nil
}

func (g mailFreezeGit) writeTree(ctx context.Context, files map[string]mailFreezeFile) (string, error) {
	directories := map[string]map[string]mailFreezeFile{"": {}}
	for name, file := range files {
		oid, err := g.run(ctx, file.data, "hash-object", "-w", "--stdin")
		if err != nil || strings.TrimSpace(oid) != file.oid {
			return "", ports.ErrClearDevGitUnavailable
		}
		dir := filepath.ToSlash(filepath.Dir(name))
		if dir == "." {
			dir = ""
		}
		if directories[dir] == nil {
			directories[dir] = map[string]mailFreezeFile{}
		}
		directories[dir][filepath.Base(name)] = file
		for dir != "" {
			parent := filepath.ToSlash(filepath.Dir(dir))
			if parent == "." {
				parent = ""
			}
			if directories[parent] == nil {
				directories[parent] = map[string]mailFreezeFile{}
			}
			dir = parent
		}
	}
	names := make([]string, 0, len(directories))
	for name := range directories {
		names = append(names, name)
	}
	// Children are built before their parents; mktree sorts direct entries.
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	var root string
	for _, dir := range names {
		var entries bytes.Buffer
		for name, file := range directories[dir] {
			kind := "blob"
			if file.mode == "040000" {
				kind = "tree"
			}
			_, _ = fmt.Fprintf(&entries, "%s %s %s\t%s\x00", file.mode, kind, file.oid, name)
		}
		tree, err := g.run(ctx, entries.Bytes(), "mktree", "-z")
		if err != nil {
			return "", err
		}
		tree = strings.TrimSpace(tree)
		if !validCommit(tree) {
			return "", ports.ErrClearDevCandidateInvalid
		}
		if dir == "" {
			root = tree
			continue
		}
		parent := filepath.ToSlash(filepath.Dir(dir))
		if parent == "." {
			parent = ""
		}
		directories[parent][filepath.Base(dir)] = mailFreezeFile{mode: "040000", oid: tree}
	}
	return root, nil
}
