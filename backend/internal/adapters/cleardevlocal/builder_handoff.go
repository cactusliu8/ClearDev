package cleardevlocal

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Exact Git object identity.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	builderHandoffMaxObjects       = 16384
	builderHandoffMaxObjectBytes   = 64 << 20
	builderHandoffMaxManifestBytes = 1 << 20
	builderHandoffMaxTextBytes     = 64 << 10
	builderHandoffMaxTotalBytes    = 82 << 20
	builderHandoffTimeout          = 30 * time.Second
)

type builderHandoffEntry struct {
	Path, Mode, OID, SHA256 string
	Size                    int64
}
type builderHandoffManifest struct {
	Version      int
	Request      ports.ClearDevBuilderHandoffSnapshotRequest
	HeadSHA      string
	Index, Files []builderHandoffEntry
}
type builderHandoffObject struct {
	oid, kind string
	data      []byte
}

// CaptureBuilderHandoff seals objects and regular bytes without changing the
// source ref, index or files and without executing repository hooks or filters.
func (r *Runner) CaptureBuilderHandoff(ctx context.Context, request ports.ClearDevBuilderHandoffSnapshotRequest) (ports.ClearDevBuilderHandoffSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, builderHandoffTimeout)
	defer cancel()
	var zero ports.ClearDevBuilderHandoffSnapshot
	if !validRunID(request.SnapshotKey) || len(request.HandoffText) > builderHandoffMaxTextBytes || request.ProjectExecution == nil {
		return zero, ports.ErrBuilderHandoffInvalid
	}
	manifest, files, material, err := r.captureBuilderHandoff(ctx, request)
	if err != nil {
		return zero, err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return zero, err
	}
	if len(raw) > builderHandoffMaxManifestBytes {
		return zero, ports.ErrBuilderHandoffLimit
	}
	dir, err := builderHandoffDirectory(request.SnapshotKey)
	if err != nil {
		return zero, err
	}
	snapshot := ports.ClearDevBuilderHandoffSnapshot{
		Version: 1, SnapshotKey: request.SnapshotKey, RepoPath: request.RepoPath, SourceWorkspacePath: request.WorkspacePath, SourceBranch: request.Branch, BaseSHA: request.BaseSHA, HeadSHA: manifest.HeadSHA,
		ManifestPath: filepath.Join(dir, "manifest.json"), ManifestSHA256: sha256Hex(raw), ObjectsPath: filepath.Join(dir, "objects"), ObjectsSHA256: sha256Hex(material),
		HandoffPath: filepath.Join(dir, "handoff.txt"), HandoffSHA256: sha256Hex([]byte(request.HandoffText)), FileCount: len(manifest.Files), ObjectBytes: int64(len(material))}
	index, _ := json.Marshal(manifest.Index)
	snapshot.IndexSHA256 = sha256Hex(index)
	objects, err := parseBuilderHandoffObjects(material)
	if err != nil {
		return zero, err
	}
	snapshot.ObjectCount = len(objects)
	for _, file := range manifest.Files {
		snapshot.ContentBytes += file.Size
	}
	snapshot.TotalBytes = snapshot.ContentBytes + int64(len(raw)+len(material)+len(request.HandoffText))
	if snapshot.TotalBytes > builderHandoffMaxTotalBytes {
		return zero, ports.ErrBuilderHandoffLimit
	}
	snapshot.SnapshotSHA256, err = builderHandoffDigest(snapshot)
	if err != nil {
		return zero, err
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return zero, err
	}
	if resolved, err := filepath.EvalSymlinks(parent); err != nil || resolved != parent {
		return zero, ports.ErrBuilderHandoffInvalid
	}
	unlock, err := lockCheckFile(ctx, dir+".lock")
	if err != nil {
		return zero, err
	}
	defer unlock()
	if _, err := os.Lstat(dir); err == nil {
		saved, _, _, err := readBuilderHandoff(snapshot)
		if err != nil || saved != snapshot {
			return zero, ports.ErrBuilderHandoffChanged
		}
		if err := r.VerifyBuilderHandoff(ctx, saved); err != nil {
			return zero, err
		}
		return saved, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, err
	}
	temp, err := os.MkdirTemp(parent, ".handoff-")
	if err != nil {
		return zero, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := os.Mkdir(filepath.Join(temp, "content"), 0o700); err != nil {
		return zero, err
	}
	for _, entry := range manifest.Files {
		if _, err := os.Lstat(filepath.Join(temp, "content", entry.SHA256)); err == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(temp, "content", entry.SHA256), files[entry.Path].data, 0o400); err != nil {
			return zero, err
		}
	}
	sealed, err := json.Marshal(snapshot)
	if err != nil {
		return zero, err
	}
	if snapshot.TotalBytes+int64(len(sealed)) > builderHandoffMaxTotalBytes {
		return zero, ports.ErrBuilderHandoffLimit
	}
	for name, data := range map[string][]byte{"manifest.json": raw, "objects": material, "handoff.txt": []byte(request.HandoffText), "snapshot.json": sealed} {
		if err := os.WriteFile(filepath.Join(temp, name), data, 0o400); err != nil {
			return zero, err
		}
	}
	again, againFiles, _, err := r.captureBuilderHandoff(ctx, request)
	if err != nil {
		return zero, err
	}
	againRaw, _ := json.Marshal(again)
	if !bytes.Equal(raw, againRaw) || !freezeFilesEqual(files, againFiles) {
		return zero, ports.ErrBuilderHandoffChanged
	}
	if err := os.Rename(temp, dir); err != nil {
		return zero, err
	}
	return snapshot, nil
}

// VerifyBuilderHandoffTarget observes an existing copy without importing,
// resetting, creating receipts or changing any working byte.
func (r *Runner) VerifyBuilderHandoffTarget(ctx context.Context, snapshot ports.ClearDevBuilderHandoffSnapshot, target ports.ClearDevBuilderHandoffTarget) error {
	ctx, cancel := context.WithTimeout(ctx, builderHandoffTimeout)
	defer cancel()
	if err := r.VerifyBuilderHandoff(ctx, snapshot); err != nil {
		return err
	}
	_, manifest, _, err := readBuilderHandoff(snapshot)
	if err != nil {
		return err
	}
	if target.RepoPath != snapshot.RepoPath || target.BaseSHA != snapshot.BaseSHA || target.WorkspacePath == snapshot.SourceWorkspacePath || target.Branch == snapshot.SourceBranch {
		return ports.ErrBuilderHandoffInvalid
	}
	g, err := r.mailFreezeRepository(ports.ClearDevMailFreezeRequest{RunID: snapshot.SnapshotKey, RepoPath: target.RepoPath, WorkspacePath: target.WorkspacePath, Branch: target.Branch, BaseSHA: target.BaseSHA, ParentSHA: target.BaseSHA})
	if err != nil {
		return err
	}
	if _, err := builderHandoffInitialBase(ctx, g, target); err != nil {
		return err
	}
	head, err := g.freezeHead(ctx, target.Branch)
	if err != nil || head != snapshot.HeadSHA {
		return ports.ErrBuilderHandoffChanged
	}
	index, err := g.indexFiles(ctx)
	if err != nil {
		return err
	}
	actualIndex, _ := json.Marshal(builderHandoffEntries(index, false))
	if sha256Hex(actualIndex) != snapshot.IndexSHA256 {
		return ports.ErrBuilderHandoffChanged
	}
	base, err := g.baseFiles(ctx, snapshot.BaseSHA)
	if err != nil {
		return err
	}
	for _, entry := range manifest.Files {
		base[entry.Path] = mailFreezeFile{}
	}
	files, err := g.workingFiles(ctx, nil)
	if err != nil {
		return err
	}
	actual, _ := json.Marshal(builderHandoffEntries(files, true))
	want, _ := json.Marshal(manifest.Files)
	if !bytes.Equal(actual, want) {
		return ports.ErrBuilderHandoffChanged
	}
	return nil
}

func (r *Runner) captureBuilderHandoff(ctx context.Context, request ports.ClearDevBuilderHandoffSnapshotRequest) (builderHandoffManifest, map[string]mailFreezeFile, []byte, error) {
	manifest := builderHandoffManifest{Version: 1, Request: request}
	freeze := ports.ClearDevMailFreezeRequest{RunID: request.SnapshotKey, RepoPath: request.RepoPath, WorkspacePath: request.WorkspacePath, Branch: request.Branch, BaseSHA: request.BaseSHA, ParentSHA: request.BaseSHA,
		WritePaths: append(append([]string(nil), request.WritePaths...), request.GeneratedPaths...), ForbiddenPaths: append(append([]string(nil), request.ForbiddenPaths...), request.SharedPathsRequireApproval...), ProjectExecution: request.ProjectExecution}
	g, err := r.mailFreezeRepository(freeze)
	if err != nil {
		return manifest, nil, nil, err
	}
	head, err := g.freezeHead(ctx, request.Branch)
	if err != nil {
		return manifest, nil, nil, err
	}
	if _, err := g.run(ctx, nil, "merge-base", "--is-ancestor", request.BaseSHA, head); err != nil {
		return manifest, nil, nil, ports.ErrBuilderHandoffInvalid
	}
	manifest.HeadSHA = head
	base, err := g.baseFiles(ctx, request.BaseSHA)
	if err != nil {
		return manifest, nil, nil, err
	}
	headFiles, err := g.builderHandoffTreeEntries(ctx, head)
	if err != nil {
		return manifest, nil, nil, err
	}
	index, err := g.indexFiles(ctx)
	if err != nil {
		return manifest, nil, nil, err
	}
	files, err := g.workingFiles(ctx, nil)
	if err != nil {
		return manifest, nil, nil, err
	}
	for _, candidate := range []map[string]mailFreezeFile{headFiles, index, files} {
		if err := validateFreezeScope(freeze, base, candidate); err != nil {
			return manifest, nil, nil, err
		}
	}
	manifest.Index = builderHandoffEntries(index, false)
	manifest.Files = builderHandoffEntries(files, true)
	material, objects, err := g.builderHandoffObjectMaterial(ctx, request.BaseSHA, head, manifest.Index)
	if err != nil {
		return manifest, nil, nil, err
	}
	for _, object := range objects {
		if object.kind != "commit" || object.oid == request.BaseSHA {
			continue
		}
		commitFiles, err := g.builderHandoffTreeEntries(ctx, object.oid)
		if err != nil {
			return manifest, nil, nil, err
		}
		if err := validateFreezeScope(freeze, base, commitFiles); err != nil {
			return manifest, nil, nil, err
		}
	}
	finalHead, err := g.freezeHead(ctx, request.Branch)
	if err != nil || finalHead != head {
		return manifest, nil, nil, ports.ErrBuilderHandoffChanged
	}
	finalIndex, err := g.indexFiles(ctx)
	if err != nil || !freezeFilesEqual(index, finalIndex) {
		return manifest, nil, nil, ports.ErrBuilderHandoffChanged
	}
	finalFiles, err := g.workingFiles(ctx, nil)
	if err != nil || !freezeFilesEqual(files, finalFiles) {
		return manifest, nil, nil, ports.ErrBuilderHandoffChanged
	}
	return manifest, files, material, nil
}

func (g mailFreezeGit) builderHandoffTreeEntries(ctx context.Context, sha string) (map[string]mailFreezeFile, error) {
	raw, err := g.builderHandoffRun(ctx, nil, builderHandoffMaxManifestBytes, "ls-tree", "-r", "-z", sha)
	if err != nil {
		return nil, err
	}
	files := make(map[string]mailFreezeFile)
	if len(raw) == 0 {
		return files, nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		metadata, name, ok := strings.Cut(line, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || !freezePathValid(name) || !slices.Contains([]string{"100644", "100755"}, fields[0]) || fields[1] != "blob" || !validCommit(fields[2]) || len(files) >= builderHandoffMaxObjects {
			return nil, ports.ErrBuilderHandoffInvalid
		}
		files[name] = mailFreezeFile{mode: fields[0], oid: fields[2]}
	}
	return files, nil
}

func builderHandoffEntries(files map[string]mailFreezeFile, content bool) []builderHandoffEntry {
	entries := make([]builderHandoffEntry, 0, len(files))
	for path, file := range files {
		entry := builderHandoffEntry{Path: path, Mode: file.mode, OID: file.oid}
		if content {
			entry.SHA256 = sha256Hex(file.data)
			entry.Size = int64(len(file.data))
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

// Enumeration excludes baseline ancestors and unrelated refs. Baseline tree
// closure and staged blobs are added explicitly, then sealed with raw bytes.
func (g mailFreezeGit) builderHandoffObjectMaterial(ctx context.Context, base, head string, index []builderHandoffEntry) ([]byte, []builderHandoffObject, error) {
	ids := map[string]bool{base: true}
	add := func(id string) error {
		if !validCommit(id) {
			return ports.ErrBuilderHandoffInvalid
		}
		ids[id] = true
		if len(ids) > builderHandoffMaxObjects {
			return ports.ErrBuilderHandoffLimit
		}
		return nil
	}
	raw, err := g.builderHandoffRun(ctx, nil, builderHandoffMaxManifestBytes, "rev-list", "--objects", "--no-object-names", head, "^"+base)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range strings.Fields(string(raw)) {
		if err := add(id); err != nil {
			return nil, nil, err
		}
	}
	tree, err := g.run(ctx, nil, "rev-parse", base+"^{tree}")
	if err != nil {
		return nil, nil, err
	}
	if err := add(strings.TrimSpace(tree)); err != nil {
		return nil, nil, err
	}
	raw, err = g.builderHandoffRun(ctx, nil, builderHandoffMaxManifestBytes, "ls-tree", "-r", "-t", "-z", base)
	if err != nil {
		return nil, nil, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00") {
		if line == "" {
			continue
		}
		meta, _, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, nil, ports.ErrBuilderHandoffInvalid
		}
		if err := add(fields[2]); err != nil {
			return nil, nil, err
		}
	}
	for _, entry := range index {
		if err := add(entry.OID); err != nil {
			return nil, nil, err
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	input := []byte(strings.Join(ordered, "\n") + "\n")
	raw, err = g.builderHandoffRun(ctx, input, builderHandoffMaxManifestBytes, "cat-file", "--batch-check")
	if err != nil {
		return nil, nil, err
	}
	var total int64
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || !slices.Contains([]string{"commit", "tree", "blob"}, fields[1]) {
			return nil, nil, ports.ErrBuilderHandoffInvalid
		}
		n, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || n < 0 {
			return nil, nil, ports.ErrBuilderHandoffInvalid
		}
		total += n + int64(len(line)) + 2
		if total > builderHandoffMaxObjectBytes {
			return nil, nil, ports.ErrBuilderHandoffLimit
		}
	}
	raw, err = g.builderHandoffRun(ctx, input, builderHandoffMaxObjectBytes, "cat-file", "--batch")
	if err != nil {
		return nil, nil, err
	}
	objects, err := parseBuilderHandoffObjects(raw)
	if err != nil || len(objects) != len(ordered) {
		return nil, nil, ports.ErrBuilderHandoffInvalid
	}
	for i, object := range objects {
		if object.oid != ordered[i] {
			return nil, nil, ports.ErrBuilderHandoffInvalid
		}
	}
	return raw, objects, nil
}

func (g mailFreezeGit) builderHandoffRun(ctx context.Context, input []byte, limit int, args ...string) ([]byte, error) {
	prefix := make([]string, 0, 13+len(args))
	prefix = append(prefix, "--no-replace-objects", "--git-dir="+g.directory, "--work-tree="+g.workspace, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.splitIndex=false", "-c", "commit.gpgsign=false")
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...) //nolint:gosec // Fixed plumbing argv.
	cmd.Dir = g.workspace
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "GIT_") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.Stdin = bytes.NewReader(input)
	output := &candidateDiagnosticBuffer{limit: limit + 1}
	diagnostic := &candidateDiagnosticBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = output, diagnostic
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: handoff git %s: %w", ports.ErrClearDevGitUnavailable, args[0], err)
	}
	raw := []byte(output.String())
	if len(raw) > limit {
		return nil, ports.ErrBuilderHandoffLimit
	}
	return raw, nil
}

func parseBuilderHandoffObjects(raw []byte) ([]builderHandoffObject, error) {
	if len(raw) > builderHandoffMaxObjectBytes {
		return nil, ports.ErrBuilderHandoffLimit
	}
	objects := make([]builderHandoffObject, 0)
	seen := map[string]bool{}
	for len(raw) > 0 {
		header, rest, ok := bytes.Cut(raw, []byte{'\n'})
		parts := strings.Fields(string(header))
		if !ok || len(parts) != 3 || !validCommit(parts[0]) || seen[parts[0]] || !slices.Contains([]string{"commit", "tree", "blob"}, parts[1]) {
			return nil, ports.ErrBuilderHandoffInvalid
		}
		n, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || n < 0 || n >= int64(len(rest)) || rest[n] != '\n' {
			return nil, ports.ErrBuilderHandoffInvalid
		}
		data := rest[:n]
		digest := sha1.New() //nolint:gosec // Exact Git identifier verification.
		_, _ = fmt.Fprintf(digest, "%s %d\x00", parts[1], n)
		_, _ = digest.Write(data)
		if hex.EncodeToString(digest.Sum(nil)) != parts[0] {
			return nil, ports.ErrBuilderHandoffInvalid
		}
		seen[parts[0]] = true
		objects = append(objects, builderHandoffObject{parts[0], parts[1], data})
		if len(objects) > builderHandoffMaxObjects {
			return nil, ports.ErrBuilderHandoffLimit
		}
		raw = rest[n+1:]
	}
	return objects, nil
}

func builderHandoffDirectory(key string) (string, error) {
	if !validRunID(key) {
		return "", ports.ErrBuilderHandoffInvalid
	}
	root := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".ao")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", ports.ErrBuilderHandoffInvalid
	}
	return filepath.Join(root, "cleardev-builder-handoffs", sha256Hex([]byte(key))), nil
}
func builderHandoffDigest(snapshot ports.ClearDevBuilderHandoffSnapshot) (string, error) {
	snapshot.SnapshotSHA256 = ""
	raw, err := json.Marshal(snapshot)
	return sha256Hex(raw), err
}
func boundedHandoffRead(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ports.ErrBuilderHandoffInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, ports.ErrBuilderHandoffChanged
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	after, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !os.SameFile(info, after) || info.Size() != int64(len(raw)) || !info.ModTime().Equal(after.ModTime()) {
		return nil, ports.ErrBuilderHandoffChanged
	}
	if int64(len(raw)) > limit {
		return nil, ports.ErrBuilderHandoffLimit
	}
	return raw, nil
}

func readBuilderHandoff(snapshot ports.ClearDevBuilderHandoffSnapshot) (ports.ClearDevBuilderHandoffSnapshot, builderHandoffManifest, []builderHandoffObject, error) {
	var manifest builderHandoffManifest
	dir, err := builderHandoffDirectory(snapshot.SnapshotKey)
	if err != nil {
		return snapshot, manifest, nil, err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir || snapshot.Version != 1 || snapshot.ManifestPath != filepath.Join(dir, "manifest.json") || snapshot.ObjectsPath != filepath.Join(dir, "objects") || snapshot.HandoffPath != filepath.Join(dir, "handoff.txt") {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	digest, err := builderHandoffDigest(snapshot)
	if err != nil || digest != snapshot.SnapshotSHA256 {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	receipt, err := boundedHandoffRead(filepath.Join(dir, "snapshot.json"), builderHandoffMaxManifestBytes)
	var saved ports.ClearDevBuilderHandoffSnapshot
	if err != nil || json.Unmarshal(receipt, &saved) != nil || saved != snapshot {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	raw, err := boundedHandoffRead(snapshot.ManifestPath, builderHandoffMaxManifestBytes)
	if err != nil || sha256Hex(raw) != snapshot.ManifestSHA256 || json.Unmarshal(raw, &manifest) != nil {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	if manifest.Version != 1 || manifest.Request.SnapshotKey != snapshot.SnapshotKey || manifest.Request.RepoPath != snapshot.RepoPath || manifest.Request.WorkspacePath != snapshot.SourceWorkspacePath || manifest.Request.Branch != snapshot.SourceBranch || manifest.Request.BaseSHA != snapshot.BaseSHA || manifest.HeadSHA != snapshot.HeadSHA {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	index, _ := json.Marshal(manifest.Index)
	if sha256Hex(index) != snapshot.IndexSHA256 || len(manifest.Files) != snapshot.FileCount || snapshot.FileCount > mailFreezeMaxFiles || snapshot.ContentBytes > mailFreezeMaxBytes || snapshot.TotalBytes > builderHandoffMaxTotalBytes {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	handoff, err := boundedHandoffRead(snapshot.HandoffPath, builderHandoffMaxTextBytes)
	if err != nil || sha256Hex(handoff) != snapshot.HandoffSHA256 || string(handoff) != manifest.Request.HandoffText {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	material, err := boundedHandoffRead(snapshot.ObjectsPath, builderHandoffMaxObjectBytes)
	if err != nil || sha256Hex(material) != snapshot.ObjectsSHA256 || int64(len(material)) != snapshot.ObjectBytes {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	objects, err := parseBuilderHandoffObjects(material)
	if err != nil || len(objects) != snapshot.ObjectCount {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	previousIndex := ""
	for _, entry := range manifest.Index {
		if !freezePathValid(entry.Path) || entry.Path <= previousIndex || !validCommit(entry.OID) || !slices.Contains([]string{"100644", "100755"}, entry.Mode) || entry.Size != 0 || entry.SHA256 != "" {
			return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
		}
		previousIndex = entry.Path
	}
	if len(manifest.Index) > mailFreezeMaxFiles {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffLimit
	}
	var total int64
	previous := ""
	contentDir := filepath.Join(dir, "content")
	resolved, err = filepath.EvalSymlinks(contentDir)
	if err != nil || resolved != contentDir {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	for _, entry := range manifest.Files {
		if !freezePathValid(entry.Path) || entry.Path <= previous || !slices.Contains([]string{"100644", "100755"}, entry.Mode) || len(entry.SHA256) != 64 || strings.ContainsAny(entry.SHA256, "/\\") {
			return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
		}
		raw, err := boundedHandoffRead(filepath.Join(contentDir, entry.SHA256), mailFreezeMaxBytes)
		if err != nil || sha256Hex(raw) != entry.SHA256 || freezeBlobID(raw) != entry.OID || int64(len(raw)) != entry.Size {
			return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
		}
		total += entry.Size
		previous = entry.Path
	}
	if total != snapshot.ContentBytes || snapshot.TotalBytes != total+int64(len(raw)+len(material)+len(handoff)) || snapshot.TotalBytes+int64(len(receipt)) > builderHandoffMaxTotalBytes {
		return snapshot, manifest, nil, ports.ErrBuilderHandoffInvalid
	}
	return saved, manifest, objects, nil
}

// VerifyBuilderHandoff is read-only, including missing or invalid seals.
func (r *Runner) VerifyBuilderHandoff(ctx context.Context, snapshot ports.ClearDevBuilderHandoffSnapshot) error {
	ctx, cancel := context.WithTimeout(ctx, builderHandoffTimeout)
	defer cancel()
	_, manifest, _, err := readBuilderHandoff(snapshot)
	if err != nil {
		return err
	}
	actual, _, material, err := r.captureBuilderHandoff(ctx, manifest.Request)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(actual)
	if sha256Hex(raw) != snapshot.ManifestSHA256 || sha256Hex(material) != snapshot.ObjectsSHA256 {
		return ports.ErrBuilderHandoffChanged
	}
	return nil
}

// RestoreBuilderHandoff touches only the independent managed target. Objects
// are imported with hash-object; index and working bytes are supplied directly.
func (r *Runner) RestoreBuilderHandoff(ctx context.Context, snapshot ports.ClearDevBuilderHandoffSnapshot, target ports.ClearDevBuilderHandoffTarget) (retErr error) {
	copyAttempted := false
	defer func() {
		if retErr != nil && !copyAttempted {
			retErr = errors.Join(ports.ErrBuilderHandoffBeforeCopy, retErr)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, builderHandoffTimeout)
	defer cancel()
	if err := r.VerifyBuilderHandoff(ctx, snapshot); err != nil {
		return err
	}
	_, manifest, objects, err := readBuilderHandoff(snapshot)
	if err != nil {
		return err
	}
	if target.RepoPath != snapshot.RepoPath || target.BaseSHA != snapshot.BaseSHA || target.WorkspacePath == snapshot.SourceWorkspacePath || target.Branch == snapshot.SourceBranch {
		return ports.ErrBuilderHandoffInvalid
	}
	g, err := r.mailFreezeRepository(ports.ClearDevMailFreezeRequest{RunID: snapshot.SnapshotKey, RepoPath: target.RepoPath, WorkspacePath: target.WorkspacePath, Branch: target.Branch, BaseSHA: target.BaseSHA, ParentSHA: target.BaseSHA})
	if err != nil {
		return err
	}
	initialBase, err := builderHandoffInitialBase(ctx, g, target)
	if err != nil {
		return err
	}
	unlock, err := lockCheckFile(ctx, filepath.Join(filepath.Dir(snapshot.ManifestPath), "restore-"+sha256Hex([]byte(target.WorkspacePath))+".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	base, err := g.baseFiles(ctx, initialBase)
	if err != nil {
		return err
	}
	head, err := g.freezeHead(ctx, target.Branch)
	if err != nil {
		return err
	}
	index, err := g.indexFiles(ctx)
	if err != nil {
		return err
	}
	current, err := g.workingFiles(ctx, nil)
	if err != nil {
		return err
	}
	wantFiles := make(map[string]mailFreezeFile)
	wantIndex := make(map[string]mailFreezeFile)
	dir := filepath.Dir(snapshot.ManifestPath)
	for _, entry := range manifest.Files {
		raw, err := boundedHandoffRead(filepath.Join(dir, "content", entry.SHA256), mailFreezeMaxBytes)
		if err != nil {
			return err
		}
		wantFiles[entry.Path] = mailFreezeFile{entry.Mode, entry.OID, raw}
	}
	for _, entry := range manifest.Index {
		wantIndex[entry.Path] = mailFreezeFile{mode: entry.Mode, oid: entry.OID}
	}
	if head == snapshot.HeadSHA && freezeFilesEqual(current, wantFiles) && freezeFilesEqual(index, wantIndex) {
		return r.VerifyBuilderHandoff(ctx, snapshot)
	}
	if head != initialBase || !freezeFilesEqual(current, base) || !freezeFilesEqual(index, base) {
		return ports.ErrBuilderHandoffChanged
	}

	for _, entry := range manifest.Files {
		if _, tracked := base[entry.Path]; tracked {
			continue
		}
		info, err := os.Lstat(filepath.Join(target.WorkspacePath, entry.Path))
		if err == nil && !info.IsDir() {
			return ports.ErrBuilderHandoffChanged
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, object := range objects {
		oid, err := g.run(ctx, object.data, "hash-object", "-t", object.kind, "-w", "--stdin")
		if err != nil || strings.TrimSpace(oid) != object.oid {
			return ports.ErrBuilderHandoffInvalid
		}
	}
	if err := r.VerifyBuilderHandoff(ctx, snapshot); err != nil {
		return err
	}
	// Once this command is invoked even an error may describe a lost response.
	// Later failures may only be reconciled by the read-only target verifier.
	copyAttempted = true
	if _, err := g.run(ctx, nil, "update-ref", "refs/heads/"+target.Branch, snapshot.HeadSHA, initialBase); err != nil {
		return err
	}
	if _, err := g.run(ctx, nil, "read-tree", "--empty"); err != nil {
		return err
	}
	var indexInput bytes.Buffer
	for _, entry := range manifest.Index {
		fmt.Fprintf(&indexInput, "%s %s\t%s\x00", entry.Mode, entry.OID, entry.Path)
	}
	if _, err := g.run(ctx, indexInput.Bytes(), "update-index", "-z", "--index-info"); err != nil {
		return err
	}
	root, err := os.OpenRoot(target.WorkspacePath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for name := range base {
		if _, exists := wantFiles[name]; !exists {
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}

	baselineDirs := make(map[string]bool)
	for name := range base {
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			baselineDirs[parent] = true
		}
	}
	dirs := make([]string, 0, len(baselineDirs))
	for name := range baselineDirs {
		dirs = append(dirs, name)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, name := range dirs {
		if _, needsFile := wantFiles[name]; needsFile {
			if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return ports.ErrBuilderHandoffChanged
			}
		} else {
			_ = root.Remove(name)
		}
	}
	for _, entry := range manifest.Files {
		for parent := filepath.Dir(entry.Path); parent != "."; parent = filepath.Dir(parent) {
			info, err := root.Lstat(parent)
			if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !info.IsDir() {
				return ports.ErrBuilderHandoffInvalid
			}
		}
		if err := root.MkdirAll(filepath.Dir(entry.Path), 0o755); err != nil {
			return err
		}
		if info, err := root.Lstat(entry.Path); err == nil && !info.Mode().IsRegular() {
			return ports.ErrBuilderHandoffInvalid
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		mode := os.FileMode(0o644)
		if entry.Mode == "100755" {
			mode = 0o755
		}
		if err := root.WriteFile(entry.Path, wantFiles[entry.Path].data, mode); err != nil {
			return err
		}
		if err := root.Chmod(entry.Path, mode); err != nil {
			return err
		}
	}
	head, err = g.freezeHead(ctx, target.Branch)
	if err != nil || head != snapshot.HeadSHA {
		return ports.ErrBuilderHandoffChanged
	}
	index, err = g.indexFiles(ctx)
	if err != nil || !freezeFilesEqual(index, wantIndex) {
		return ports.ErrBuilderHandoffChanged
	}
	current, err = g.workingFiles(ctx, nil)
	if err != nil || !freezeFilesEqual(current, wantFiles) {
		return ports.ErrBuilderHandoffChanged
	}
	return r.VerifyBuilderHandoff(ctx, snapshot)
}

func builderHandoffInitialBase(ctx context.Context, g mailFreezeGit, target ports.ClearDevBuilderHandoffTarget) (string, error) {
	initial := target.InitialBaseSHA
	if initial == "" {
		initial = target.BaseSHA
	}
	if !validCommit(initial) {
		return "", ports.ErrBuilderHandoffInvalid
	}
	kind, err := g.run(ctx, nil, "cat-file", "-t", initial)
	if err != nil || strings.TrimSpace(kind) != "commit" {
		return "", ports.ErrBuilderHandoffInvalid
	}
	if _, err := g.run(ctx, nil, "merge-base", "--is-ancestor", initial, target.BaseSHA); err != nil {
		return "", ports.ErrBuilderHandoffInvalid
	}
	return initial, nil
}

// The Service supplies this private proof only after checking the exact grant,
// alias and original logical step. This adapter checks the seal and target
// binding; it never accepts a caller-provided list of index entries.
func approvedBuilderHandoffFreezeIndex(request ports.ClearDevMailFreezeRequest, base map[string]mailFreezeFile) (map[string]mailFreezeFile, error) {
	if request.BuilderHandoffSnapshot == nil {
		return nil, nil
	}
	snapshot := *request.BuilderHandoffSnapshot
	if request.ProjectExecution == nil || snapshot.RepoPath != request.RepoPath || snapshot.BaseSHA != request.BaseSHA || snapshot.HeadSHA != request.ParentSHA || pathWithin(snapshot.SourceWorkspacePath, request.WorkspacePath) || pathWithin(request.WorkspacePath, snapshot.SourceWorkspacePath) || snapshot.SourceBranch == request.Branch || !strings.HasPrefix(request.Branch, "cleardev-complex-builder-handoff-") {
		return nil, ports.ErrClearDevCandidateInvalid
	}
	_, manifest, _, err := readBuilderHandoff(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: handoff index seal is unavailable: %w", ports.ErrClearDevCandidateInvalid, err)
	}
	expectedContract, err := json.Marshal(request.ProjectExecution)
	if err != nil {
		return nil, err
	}
	sealedContract, err := json.Marshal(manifest.Request.ProjectExecution)
	if err != nil || manifest.Request.ProjectExecution == nil || !bytes.Equal(expectedContract, sealedContract) {
		return nil, ports.ErrClearDevCandidateInvalid
	}
	index := make(map[string]mailFreezeFile, len(manifest.Index))
	for _, entry := range manifest.Index {
		index[entry.Path] = mailFreezeFile{mode: entry.Mode, oid: entry.OID}
	}
	// Sealing does not bypass the current task's scope or its forbidden paths.
	if err := validateFreezeScope(request, base, index); err != nil {
		return nil, err
	}
	return index, nil
}
