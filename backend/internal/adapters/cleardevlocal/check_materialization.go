package cleardevlocal

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git SHA-1 is the repository object identity, not a password hash.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	candidateSourceManifestVersion     = 1
	candidateSourceDefaultMaxBytes     = checkSourceBytesLimit
	candidateSourceDefaultMaxItems     = 100_000
	candidateSourceMaxComponentBytes   = 255
	candidateSourceMaxPathBytes        = 4096
	candidateSourceDefaultMaxCommit    = int64(16 * 1024 * 1024)
	candidateSourceGitHeaderLimit      = 256
	candidateSourceGitDiagnosticLimit  = 64 * 1024
	candidateSourceGitPathFileLimit    = 16 * 1024
	candidateSourceAlternatesFileLimit = 64 * 1024
)

type candidateSourceLimits struct {
	MaxBytes       int64
	MaxItems       int
	MaxTreeBytes   int64
	MaxCommitBytes int64
}

type candidateSourceManifestEntry struct {
	Path          string `json:"path"`
	Mode          string `json:"mode"`
	ObjectOID     string `json:"objectOid"`
	ObjectSize    int64  `json:"objectSize"`
	ContentSHA256 string `json:"contentSha256"`
}

type candidateSourceManifest struct {
	Version      int                            `json:"version"`
	CandidateSHA string                         `json:"candidateSha"`
	RootTreeOID  string                         `json:"rootTreeOid"`
	ItemCount    int                            `json:"itemCount"`
	BlobBytes    int64                          `json:"blobBytes"`
	Entries      []candidateSourceManifestEntry `json:"entries"`
}

type candidateSourceManifestFacts struct {
	Manifest        candidateSourceManifest
	ManifestID      string
	ManifestPath    string
	SourceDirectory string
}

type candidateGitObjectInfo struct {
	OID  string
	Type string
	Size int64
}

type candidateGitObjectDigest struct {
	Info          candidateGitObjectInfo
	ContentSHA256 string
}

type candidateRawTreeEntry struct {
	Name      string
	Mode      string
	ObjectOID string
}

type candidateScannedTree struct {
	Digest  candidateGitObjectDigest
	Entries []candidateRawTreeEntry
}

type candidateTreeFrame struct {
	Path      string
	ObjectOID string
}

type candidateGitObjectReader struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	input    *bufio.Writer
	output   *bufio.Reader
	stderr   *candidateDiagnosticBuffer
	viewRoot string
	closed   bool
}

type candidateDiagnosticBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func defaultCandidateSourceLimits() candidateSourceLimits {
	return normalizeCandidateSourceLimits(candidateSourceLimits{})
}

func normalizeCandidateSourceLimits(limits candidateSourceLimits) candidateSourceLimits {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = candidateSourceDefaultMaxBytes
	}
	if limits.MaxItems <= 0 {
		limits.MaxItems = candidateSourceDefaultMaxItems
	}
	if limits.MaxCommitBytes <= 0 {
		limits.MaxCommitBytes = candidateSourceDefaultMaxCommit
	}
	if limits.MaxTreeBytes <= 0 {
		// Every accepted tree record contains at most a six-byte mode, one
		// space, a 255-byte component, one NUL byte, and a 20-byte object id.
		limits.MaxTreeBytes = int64(limits.MaxItems) * (6 + 1 + candidateSourceMaxComponentBytes + 1 + sha1.Size)
	}
	return limits
}

// inspectCandidateSource reads and verifies every raw object reachable from an
// exact commit. scratchDirectory must be a caller-owned capacity-limited area.
func inspectCandidateSource(
	ctx context.Context,
	workspacePath string,
	candidateSHA string,
	scratchDirectory string,
	limits candidateSourceLimits,
) (candidateSourceManifestFacts, error) {
	limits = normalizeCandidateSourceLimits(limits)
	if !validCandidateSourceOID(candidateSHA) {
		return candidateSourceManifestFacts{}, errors.New("candidate source commit must be a full lowercase SHA-1 object id")
	}
	reader, err := newCandidateGitObjectReader(ctx, workspacePath, scratchDirectory)
	if err != nil {
		return candidateSourceManifestFacts{}, err
	}
	manifest, scanErr := scanCandidateSource(reader, candidateSHA, limits)
	closeErr := reader.Close()
	if scanErr != nil {
		return candidateSourceManifestFacts{}, scanErr
	}
	if closeErr != nil {
		return candidateSourceManifestFacts{}, closeErr
	}
	_, manifestID, err := canonicalCandidateSourceManifest(manifest)
	if err != nil {
		return candidateSourceManifestFacts{}, err
	}
	return candidateSourceManifestFacts{Manifest: manifest, ManifestID: manifestID}, nil
}

// materializeCandidateSource scans the complete candidate before creating the
// destination. It then writes exact blobs, independently verifies the on-disk
// tree, and finally saves the canonical manifest outside the candidate root.
func materializeCandidateSource(
	ctx context.Context,
	workspacePath string,
	candidateSHA string,
	scratchDirectory string,
	destinationDirectory string,
	manifestPath string,
	limits candidateSourceLimits,
) (candidateSourceManifestFacts, error) {
	if err := validateCandidateMaterializationPaths(scratchDirectory, destinationDirectory, manifestPath); err != nil {
		return candidateSourceManifestFacts{}, err
	}
	facts, err := inspectCandidateSource(ctx, workspacePath, candidateSHA, scratchDirectory, limits)
	if err != nil {
		return candidateSourceManifestFacts{}, err
	}
	if err := os.Mkdir(destinationDirectory, 0o700); err != nil {
		return candidateSourceManifestFacts{}, fmt.Errorf("create candidate source directory: %w", err)
	}
	root, err := os.OpenRoot(destinationDirectory)
	if err != nil {
		return candidateSourceManifestFacts{}, fmt.Errorf("open candidate source directory: %w", err)
	}
	if err := createCandidateSourceDirectories(root, facts.Manifest); err != nil {
		_ = root.Close()
		return candidateSourceManifestFacts{}, err
	}
	reader, err := newCandidateGitObjectReader(ctx, workspacePath, scratchDirectory)
	if err != nil {
		_ = root.Close()
		return candidateSourceManifestFacts{}, err
	}
	writeErr := writeCandidateSourceFiles(reader, root, facts.Manifest)
	closeReaderErr := reader.Close()
	if writeErr == nil {
		writeErr = closeReaderErr
	}
	if writeErr == nil {
		writeErr = sealCandidateSourceDirectories(root, facts.Manifest)
	}
	closeRootErr := root.Close()
	if writeErr == nil {
		writeErr = closeRootErr
	}
	if writeErr != nil {
		return candidateSourceManifestFacts{}, writeErr
	}
	if err := os.Chmod(destinationDirectory, 0o555); err != nil { //nolint:gosec // the materialized source must be readable and immutable in the non-root checker.
		return candidateSourceManifestFacts{}, fmt.Errorf("seal candidate source root: %w", err)
	}
	if err := verifyMaterializedCandidateSource(destinationDirectory, facts.Manifest); err != nil {
		return candidateSourceManifestFacts{}, err
	}
	manifestID, err := persistCandidateSourceManifest(manifestPath, facts.Manifest)
	if err != nil {
		return candidateSourceManifestFacts{}, err
	}
	if manifestID != facts.ManifestID {
		return candidateSourceManifestFacts{}, errors.New("candidate source manifest changed before persistence")
	}
	facts.ManifestPath = manifestPath
	facts.SourceDirectory = destinationDirectory
	return facts, nil
}

// readCandidateSourceRootFile reads an exact root npm manifest blob named by a
// previously verified candidate source manifest.
func readCandidateSourceRootFile(
	ctx context.Context,
	workspacePath string,
	scratchDirectory string,
	manifest candidateSourceManifest,
	name string,
	maxBytes int64,
) ([]byte, error) {
	if name != "package.json" && name != "package-lock.json" {
		return nil, errors.New("candidate root file is not an approved npm manifest")
	}
	if maxBytes < 0 {
		return nil, errors.New("candidate root file limit is invalid")
	}
	if _, _, err := canonicalCandidateSourceManifest(manifest); err != nil {
		return nil, err
	}
	var selected *candidateSourceManifestEntry
	for index := range manifest.Entries {
		entry := &manifest.Entries[index]
		if entry.Path == name {
			selected = entry
			break
		}
	}
	if selected == nil || (selected.Mode != "100644" && selected.Mode != "100755") {
		return nil, fmt.Errorf("candidate root file %q is missing", name)
	}
	if selected.ObjectSize > maxBytes {
		return nil, fmt.Errorf("candidate root file %q exceeds its size limit", name)
	}
	reader, err := newCandidateGitObjectReader(ctx, workspacePath, scratchDirectory)
	if err != nil {
		return nil, err
	}
	info, err := reader.objectInfo(selected.ObjectOID)
	if err == nil && (info.Type != "blob" || info.Size != selected.ObjectSize) {
		err = fmt.Errorf("candidate root file %q changed object metadata", name)
	}
	var payload bytes.Buffer
	if err == nil {
		payload.Grow(int(info.Size))
		var digest candidateGitObjectDigest
		digest, err = reader.copyObject(info, &payload)
		if err == nil && digest.ContentSHA256 != selected.ContentSHA256 {
			err = fmt.Errorf("candidate root file %q changed content", name)
		}
	}
	closeErr := reader.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return payload.Bytes(), nil
}

// verifyMaterializedCandidateSource independently checks every path and mode,
// hashes each file as a Git blob, and rebuilds every tree through the root.
func verifyMaterializedCandidateSource(destinationDirectory string, manifest candidateSourceManifest) error {
	if _, _, err := canonicalCandidateSourceManifest(manifest); err != nil {
		return err
	}
	root, err := os.OpenRoot(destinationDirectory)
	if err != nil {
		return fmt.Errorf("open materialized candidate source: %w", err)
	}
	defer func() { _ = root.Close() }()
	expected := make(map[string]candidateSourceManifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		expected[entry.Path] = entry
	}
	seen := make(map[string]bool, len(expected))
	err = fs.WalkDir(root.FS(), ".", func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		entry, ok := expected[path]
		if !ok {
			return fmt.Errorf("materialized candidate contains unexpected path %q", path)
		}
		info, infoErr := item.Info()
		if infoErr != nil {
			return infoErr
		}
		if !candidateSourceModeMatchesInfo(entry.Mode, info) {
			return fmt.Errorf("materialized candidate path %q changed type or mode", path)
		}
		seen[path] = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk materialized candidate source: %w", err)
	}
	if len(seen) != len(expected) {
		return errors.New("materialized candidate source is missing a manifest path")
	}
	actualOIDs := make(map[string]string, len(expected))
	children := make(map[string][]candidateRawTreeEntry)
	for _, entry := range manifest.Entries {
		parent, name := candidateSourceParent(entry.Path)
		children[parent] = append(children[parent], candidateRawTreeEntry{Name: name, Mode: entry.Mode, ObjectOID: entry.ObjectOID})
		if entry.Mode == "040000" {
			continue
		}
		file, openErr := root.Open(filepath.FromSlash(entry.Path))
		if openErr != nil {
			return fmt.Errorf("open materialized candidate file %q: %w", entry.Path, openErr)
		}
		oid, contentSHA256, hashErr := hashCandidateGitObject("blob", entry.ObjectSize, file)
		closeErr := file.Close()
		if hashErr != nil {
			return fmt.Errorf("hash materialized candidate file %q: %w", entry.Path, hashErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close materialized candidate file %q: %w", entry.Path, closeErr)
		}
		if oid != entry.ObjectOID || contentSHA256 != entry.ContentSHA256 {
			return fmt.Errorf("materialized candidate file %q does not match its object", entry.Path)
		}
		actualOIDs[entry.Path] = oid
	}
	directories := candidateSourceDirectoryEntries(manifest)
	sort.Slice(directories, func(left, right int) bool {
		leftDepth := strings.Count(directories[left].Path, "/")
		rightDepth := strings.Count(directories[right].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return directories[left].Path > directories[right].Path
	})
	for _, directory := range directories {
		oid, objectSize, contentSHA256, treeErr := rebuildCandidateTree(children[directory.Path], actualOIDs, directory.Path)
		if treeErr != nil {
			return treeErr
		}
		if oid != directory.ObjectOID || objectSize != directory.ObjectSize || contentSHA256 != directory.ContentSHA256 {
			return fmt.Errorf("materialized candidate directory %q does not match its tree", directory.Path)
		}
		actualOIDs[directory.Path] = oid
	}
	rootOID, _, _, err := rebuildCandidateTree(children[""], actualOIDs, "")
	if err != nil {
		return err
	}
	if rootOID != manifest.RootTreeOID {
		return errors.New("materialized candidate root does not match its tree")
	}
	return nil
}

func scanCandidateSource(reader *candidateGitObjectReader, candidateSHA string, limits candidateSourceLimits) (candidateSourceManifest, error) {
	commitInfo, err := reader.objectInfo(candidateSHA)
	if err != nil {
		return candidateSourceManifest{}, err
	}
	if commitInfo.Type != "commit" || commitInfo.Size > limits.MaxCommitBytes {
		return candidateSourceManifest{}, errors.New("candidate object is not a supported commit")
	}
	var commit bytes.Buffer
	commit.Grow(int(commitInfo.Size))
	if _, err := reader.copyObject(commitInfo, &commit); err != nil {
		return candidateSourceManifest{}, err
	}
	rootTreeOID, err := parseCandidateCommitTree(commit.Bytes())
	if err != nil {
		return candidateSourceManifest{}, err
	}
	manifest := candidateSourceManifest{
		Version: candidateSourceManifestVersion, CandidateSHA: candidateSHA, RootTreeOID: rootTreeOID,
		Entries: []candidateSourceManifestEntry{},
	}
	treeCache := make(map[string]candidateScannedTree)
	blobCache := make(map[string]candidateGitObjectDigest)
	seenPaths := make(map[string]bool)
	frames := []candidateTreeFrame{{ObjectOID: rootTreeOID}}
	var treeBytes int64
	for len(frames) != 0 {
		last := len(frames) - 1
		frame := frames[last]
		frames = frames[:last]
		tree, ok := treeCache[frame.ObjectOID]
		if !ok {
			info, infoErr := reader.objectInfo(frame.ObjectOID)
			if infoErr != nil {
				return candidateSourceManifest{}, infoErr
			}
			if info.Type != "tree" || info.Size > limits.MaxTreeBytes-treeBytes {
				return candidateSourceManifest{}, errors.New("candidate tree metadata exceeds its limit or has the wrong type")
			}
			var raw bytes.Buffer
			raw.Grow(int(info.Size))
			digest, readErr := reader.copyObject(info, &raw)
			if readErr != nil {
				return candidateSourceManifest{}, readErr
			}
			parsed, parseErr := parseCandidateRawTree(raw.Bytes())
			if parseErr != nil {
				return candidateSourceManifest{}, parseErr
			}
			tree = candidateScannedTree{Digest: digest, Entries: parsed}
			treeCache[frame.ObjectOID] = tree
			treeBytes += info.Size
		}
		if frame.Path != "" {
			manifest.Entries = append(manifest.Entries, candidateSourceManifestEntry{
				Path: frame.Path, Mode: "040000", ObjectOID: frame.ObjectOID,
				ObjectSize: tree.Digest.Info.Size, ContentSHA256: tree.Digest.ContentSHA256,
			})
		}
		for _, child := range tree.Entries {
			path, pathErr := candidateSourceChildPath(frame.Path, child.Name)
			if pathErr != nil {
				return candidateSourceManifest{}, pathErr
			}
			if seenPaths[path] {
				return candidateSourceManifest{}, fmt.Errorf("candidate source contains duplicate path %q", path)
			}
			if manifest.ItemCount >= limits.MaxItems {
				return candidateSourceManifest{}, errors.New("candidate source item count exceeds its limit")
			}
			seenPaths[path] = true
			manifest.ItemCount++
			switch child.Mode {
			case "040000":
				frames = append(frames, candidateTreeFrame{Path: path, ObjectOID: child.ObjectOID})
			case "100644", "100755":
				blob, found := blobCache[child.ObjectOID]
				if !found {
					info, infoErr := reader.objectInfo(child.ObjectOID)
					if infoErr != nil {
						return candidateSourceManifest{}, infoErr
					}
					if info.Type != "blob" {
						return candidateSourceManifest{}, fmt.Errorf("candidate source path %q does not reference a blob", path)
					}
					if info.Size > limits.MaxBytes-manifest.BlobBytes {
						return candidateSourceManifest{}, errors.New("candidate source byte count exceeds its limit")
					}
					blob, err = reader.copyObject(info, io.Discard)
					if err != nil {
						return candidateSourceManifest{}, err
					}
					blobCache[child.ObjectOID] = blob
				}
				if found && blob.Info.Size > limits.MaxBytes-manifest.BlobBytes {
					return candidateSourceManifest{}, errors.New("candidate source byte count exceeds its limit")
				}
				manifest.BlobBytes += blob.Info.Size
				manifest.Entries = append(manifest.Entries, candidateSourceManifestEntry{
					Path: path, Mode: child.Mode, ObjectOID: child.ObjectOID,
					ObjectSize: blob.Info.Size, ContentSHA256: blob.ContentSHA256,
				})
			default:
				return candidateSourceManifest{}, fmt.Errorf("candidate source path %q has unsupported mode %q", path, child.Mode)
			}
		}
	}
	sort.Slice(manifest.Entries, func(left, right int) bool {
		return bytes.Compare([]byte(manifest.Entries[left].Path), []byte(manifest.Entries[right].Path)) < 0
	})
	if len(manifest.Entries) != manifest.ItemCount {
		return candidateSourceManifest{}, errors.New("candidate source manifest item count is inconsistent")
	}
	return manifest, nil
}

func parseCandidateCommitTree(payload []byte) (string, error) {
	headerEnd := bytes.Index(payload, []byte("\n\n"))
	if headerEnd < 0 {
		return "", errors.New("candidate commit has no complete header")
	}
	var treeOID string
	for _, line := range bytes.Split(payload[:headerEnd], []byte{'\n'}) {
		if bytes.IndexByte(line, 0) >= 0 {
			return "", errors.New("candidate commit header contains NUL")
		}
		if !bytes.HasPrefix(line, []byte("tree ")) {
			continue
		}
		value := string(bytes.TrimPrefix(line, []byte("tree ")))
		if treeOID != "" || !validCandidateSourceOID(value) {
			return "", errors.New("candidate commit has an invalid tree header")
		}
		treeOID = value
	}
	if treeOID == "" {
		return "", errors.New("candidate commit has no tree header")
	}
	return treeOID, nil
}

func parseCandidateRawTree(payload []byte) ([]candidateRawTreeEntry, error) {
	entries := make([]candidateRawTreeEntry, 0)
	var previous *candidateRawTreeEntry
	for offset := 0; offset < len(payload); {
		spaceOffset := bytes.IndexByte(payload[offset:], ' ')
		if spaceOffset <= 0 {
			return nil, errors.New("candidate tree contains a malformed mode")
		}
		spaceOffset += offset
		rawMode := string(payload[offset:spaceOffset])
		var mode string
		switch rawMode {
		case "40000":
			mode = "040000"
		case "100644", "100755":
			mode = rawMode
		case "120000":
			return nil, errors.New("candidate tree contains a symbolic link")
		case "160000":
			return nil, errors.New("candidate tree contains a submodule")
		default:
			return nil, fmt.Errorf("candidate tree contains unsupported mode %q", rawMode)
		}
		nameStart := spaceOffset + 1
		nulOffset := bytes.IndexByte(payload[nameStart:], 0)
		if nulOffset < 0 {
			return nil, errors.New("candidate tree contains an unterminated path")
		}
		nulOffset += nameStart
		oidStart := nulOffset + 1
		oidEnd := oidStart + sha1.Size
		if oidEnd > len(payload) {
			return nil, errors.New("candidate tree contains a truncated object id")
		}
		nameBytes := payload[nameStart:nulOffset]
		if err := validateCandidateSourceComponent(nameBytes); err != nil {
			return nil, err
		}
		entry := candidateRawTreeEntry{Name: string(nameBytes), Mode: mode, ObjectOID: hex.EncodeToString(payload[oidStart:oidEnd])}
		if previous != nil && compareCandidateTreeEntries(*previous, entry) >= 0 {
			return nil, errors.New("candidate tree entries are duplicate or not in canonical order")
		}
		entries = append(entries, entry)
		previous = &entries[len(entries)-1]
		offset = oidEnd
	}
	return entries, nil
}

func validateCandidateSourceComponent(name []byte) error {
	if len(name) == 0 || len(name) > candidateSourceMaxComponentBytes || !utf8.Valid(name) {
		return errors.New("candidate tree contains an invalid path component")
	}
	value := string(name)
	if value == "." || value == ".." || strings.EqualFold(value, ".git") || strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("candidate tree contains unsupported path component %q", value)
	}
	for _, character := range value {
		if character == 0 || character < 0x20 || character == 0x7f {
			return errors.New("candidate tree path contains a control character")
		}
	}
	return nil
}

func candidateSourceChildPath(parent, name string) (string, error) {
	path := name
	if parent != "" {
		path = parent + "/" + name
	}
	if len(path) > candidateSourceMaxPathBytes || !fs.ValidPath(path) {
		return "", errors.New("candidate source path exceeds its limit or escapes the root")
	}
	return path, nil
}

func compareCandidateTreeEntries(left, right candidateRawTreeEntry) int {
	leftName := []byte(left.Name)
	rightName := []byte(right.Name)
	common := min(len(leftName), len(rightName))
	if compared := bytes.Compare(leftName[:common], rightName[:common]); compared != 0 {
		return compared
	}
	leftTerminator := byte(0)
	if len(leftName) > common {
		leftTerminator = leftName[common]
	} else if left.Mode == "040000" {
		leftTerminator = '/'
	}
	rightTerminator := byte(0)
	if len(rightName) > common {
		rightTerminator = rightName[common]
	} else if right.Mode == "040000" {
		rightTerminator = '/'
	}
	return int(leftTerminator) - int(rightTerminator)
}

func canonicalCandidateSourceManifest(manifest candidateSourceManifest) ([]byte, string, error) {
	if manifest.Version != candidateSourceManifestVersion || !validCandidateSourceOID(manifest.CandidateSHA) || !validCandidateSourceOID(manifest.RootTreeOID) {
		return nil, "", errors.New("candidate source manifest identity is invalid")
	}
	if manifest.ItemCount != len(manifest.Entries) || manifest.ItemCount < 0 || manifest.ItemCount > candidateSourceDefaultMaxItems || manifest.BlobBytes < 0 || manifest.BlobBytes > candidateSourceDefaultMaxBytes {
		return nil, "", errors.New("candidate source manifest totals are invalid")
	}
	var blobBytes int64
	seen := make(map[string]candidateSourceManifestEntry, len(manifest.Entries))
	for index, entry := range manifest.Entries {
		if !validCandidateSourceOID(entry.ObjectOID) || !validCandidateSourceSHA256(entry.ContentSHA256) || entry.ObjectSize < 0 {
			return nil, "", errors.New("candidate source manifest object is invalid")
		}
		if entry.Mode != "040000" && entry.Mode != "100644" && entry.Mode != "100755" {
			return nil, "", errors.New("candidate source manifest mode is invalid")
		}
		if err := validateCandidateManifestPath(entry.Path); err != nil {
			return nil, "", err
		}
		if _, found := seen[entry.Path]; found || (index > 0 && bytes.Compare([]byte(manifest.Entries[index-1].Path), []byte(entry.Path)) >= 0) {
			return nil, "", errors.New("candidate source manifest paths are duplicate or unsorted")
		}
		seen[entry.Path] = entry
		if entry.Mode != "040000" {
			if entry.ObjectSize > candidateSourceDefaultMaxBytes-blobBytes {
				return nil, "", errors.New("candidate source manifest byte total overflows")
			}
			blobBytes += entry.ObjectSize
		}
		parent, _ := candidateSourceParent(entry.Path)
		if parent != "" {
			parentEntry, ok := seen[parent]
			if !ok || parentEntry.Mode != "040000" {
				return nil, "", fmt.Errorf("candidate source manifest path %q has no directory parent", entry.Path)
			}
		}
	}
	if blobBytes != manifest.BlobBytes {
		return nil, "", errors.New("candidate source manifest byte total is inconsistent")
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return nil, "", fmt.Errorf("encode candidate source manifest: %w", err)
	}
	// The full manifest is a source artifact too. Charging it together with
	// blob content keeps every host-side source write inside the fixed 256 MiB
	// source allowance, including repositories with many long paths.
	if int64(len(payload)) > candidateSourceDefaultMaxBytes-manifest.BlobBytes {
		return nil, "", fmt.Errorf("%w: candidate source and manifest exceed %d bytes", errCheckCapacityExceeded, candidateSourceDefaultMaxBytes)
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

func validateCandidateManifestPath(path string) error {
	if path == "" || len(path) > candidateSourceMaxPathBytes || !utf8.ValidString(path) || !fs.ValidPath(path) || strings.ContainsRune(path, '\\') {
		return errors.New("candidate source manifest path is invalid")
	}
	for _, component := range strings.Split(path, "/") {
		if err := validateCandidateSourceComponent([]byte(component)); err != nil {
			return err
		}
	}
	return nil
}

func persistCandidateSourceManifest(path string, manifest candidateSourceManifest) (string, error) {
	payload, manifestID, err := canonicalCandidateSourceManifest(manifest)
	if err != nil {
		return "", err
	}
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".candidate-source-manifest-")
	if err != nil {
		return "", fmt.Errorf("create candidate source manifest: %w", err)
	}
	temporaryPath := file.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("set candidate source manifest permissions: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("write candidate source manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("sync candidate source manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close candidate source manifest: %w", err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return "", fmt.Errorf("publish candidate source manifest: %w", err)
	}
	return manifestID, nil
}

func validateCandidateMaterializationPaths(scratchDirectory, destinationDirectory, manifestPath string) error {
	for label, path := range map[string]string{
		"scratch": scratchDirectory, "destination": destinationDirectory, "manifest": manifestPath,
	} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("candidate source %s path is empty", label)
		}
	}
	if err := ensureCandidateGitScratchDirectory(scratchDirectory); err != nil {
		return err
	}
	destinationParent, err := os.Stat(filepath.Dir(destinationDirectory))
	if err != nil || !destinationParent.IsDir() {
		return errors.New("candidate source destination parent is unavailable")
	}
	manifestParent, err := os.Stat(filepath.Dir(manifestPath))
	if err != nil || !manifestParent.IsDir() {
		return errors.New("candidate source manifest parent is unavailable")
	}
	for _, path := range []string{scratchDirectory, manifestPath} {
		inside, relationErr := candidatePathInside(destinationDirectory, path)
		if relationErr != nil {
			return relationErr
		}
		if inside {
			return errors.New("candidate source scratch and manifest paths must be outside the destination")
		}
	}
	return nil
}

func candidatePathInside(root, path string) (bool, error) {
	rootAbsolute, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	pathAbsolute, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(rootAbsolute, pathAbsolute)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func createCandidateSourceDirectories(root *os.Root, manifest candidateSourceManifest) error {
	directories := candidateSourceDirectoryEntries(manifest)
	sort.Slice(directories, func(left, right int) bool {
		leftDepth := strings.Count(directories[left].Path, "/")
		rightDepth := strings.Count(directories[right].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return directories[left].Path < directories[right].Path
	})
	for _, directory := range directories {
		if err := root.Mkdir(filepath.FromSlash(directory.Path), 0o700); err != nil {
			return fmt.Errorf("create candidate source directory %q: %w", directory.Path, err)
		}
	}
	return nil
}

func writeCandidateSourceFiles(reader *candidateGitObjectReader, root *os.Root, manifest candidateSourceManifest) error {
	for _, entry := range manifest.Entries {
		if entry.Mode == "040000" {
			continue
		}
		info, err := reader.objectInfo(entry.ObjectOID)
		if err != nil {
			return err
		}
		if info.Type != "blob" || info.Size != entry.ObjectSize {
			return fmt.Errorf("candidate source file %q changed object metadata", entry.Path)
		}
		file, err := root.OpenFile(filepath.FromSlash(entry.Path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create candidate source file %q: %w", entry.Path, err)
		}
		digest, writeErr := reader.copyObject(info, file)
		if writeErr == nil && digest.ContentSHA256 != entry.ContentSHA256 {
			writeErr = errors.New("candidate source blob changed after scanning")
		}
		if writeErr == nil {
			writeErr = file.Sync()
		}
		mode := fs.FileMode(0o444)
		if entry.Mode == "100755" {
			mode = 0o555
		}
		if writeErr == nil {
			writeErr = file.Chmod(mode)
		}
		closeErr := file.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return fmt.Errorf("write candidate source file %q: %w", entry.Path, writeErr)
		}
	}
	return nil
}

func sealCandidateSourceDirectories(root *os.Root, manifest candidateSourceManifest) error {
	directories := candidateSourceDirectoryEntries(manifest)
	sort.Slice(directories, func(left, right int) bool {
		leftDepth := strings.Count(directories[left].Path, "/")
		rightDepth := strings.Count(directories[right].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		return directories[left].Path > directories[right].Path
	})
	for _, directory := range directories {
		if err := root.Chmod(filepath.FromSlash(directory.Path), 0o555); err != nil {
			return fmt.Errorf("seal candidate source directory %q: %w", directory.Path, err)
		}
	}
	return nil
}

func candidateSourceDirectoryEntries(manifest candidateSourceManifest) []candidateSourceManifestEntry {
	directories := make([]candidateSourceManifestEntry, 0)
	for _, entry := range manifest.Entries {
		if entry.Mode == "040000" {
			directories = append(directories, entry)
		}
	}
	return directories
}

func candidateSourceModeMatchesInfo(mode string, info fs.FileInfo) bool {
	switch mode {
	case "040000":
		return info.IsDir() && info.Mode().Perm() == 0o555
	case "100644":
		return info.Mode().IsRegular() && info.Mode().Perm() == 0o444
	case "100755":
		return info.Mode().IsRegular() && info.Mode().Perm() == 0o555
	default:
		return false
	}
}

func candidateSourceParent(path string) (string, string) {
	separator := strings.LastIndexByte(path, '/')
	if separator < 0 {
		return "", path
	}
	return path[:separator], path[separator+1:]
}

func rebuildCandidateTree(children []candidateRawTreeEntry, actualOIDs map[string]string, directory string) (string, int64, string, error) {
	entries := append([]candidateRawTreeEntry(nil), children...)
	sort.Slice(entries, func(left, right int) bool {
		return compareCandidateTreeEntries(entries[left], entries[right]) < 0
	})
	var raw bytes.Buffer
	for _, entry := range entries {
		path := entry.Name
		if directory != "" {
			path = directory + "/" + entry.Name
		}
		oid, ok := actualOIDs[path]
		if !ok {
			return "", 0, "", fmt.Errorf("materialized candidate tree is missing object %q", path)
		}
		rawMode := entry.Mode
		if rawMode == "040000" {
			rawMode = "40000"
		}
		raw.WriteString(rawMode)
		raw.WriteByte(' ')
		raw.WriteString(entry.Name)
		raw.WriteByte(0)
		rawOID, err := hex.DecodeString(oid)
		if err != nil || len(rawOID) != sha1.Size {
			return "", 0, "", errors.New("materialized candidate tree has an invalid object id")
		}
		raw.Write(rawOID)
	}
	oid, contentSHA256, err := hashCandidateGitObject("tree", int64(raw.Len()), bytes.NewReader(raw.Bytes()))
	return oid, int64(raw.Len()), contentSHA256, err
}

func hashCandidateGitObject(objectType string, size int64, payload io.Reader) (string, string, error) {
	if size < 0 {
		return "", "", errors.New("git object size is negative")
	}
	gitHash := sha1.New() //nolint:gosec // Git object identity is defined by SHA-1.
	contentHash := sha256.New()
	if _, err := fmt.Fprintf(gitHash, "%s %d%c", objectType, size, byte(0)); err != nil {
		return "", "", err
	}
	written, err := io.CopyN(io.MultiWriter(gitHash, contentHash), payload, size)
	if err != nil || written != size {
		return "", "", errors.New("git object payload is shorter than its declared size")
	}
	var extra [1]byte
	if count, readErr := payload.Read(extra[:]); count != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return "", "", errors.New("git object payload is longer than its declared size")
	}
	return hex.EncodeToString(gitHash.Sum(nil)), hex.EncodeToString(contentHash.Sum(nil)), nil
}

func validCandidateSourceOID(value string) bool {
	if len(value) != sha1.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validCandidateSourceSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func newCandidateGitObjectReader(ctx context.Context, workspacePath, scratchDirectory string) (*candidateGitObjectReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	objectsPath, err := resolveCandidateGitObjects(workspacePath)
	if err != nil {
		return nil, err
	}
	if err := ensureCandidateGitScratchDirectory(scratchDirectory); err != nil {
		return nil, err
	}
	viewRoot, err := os.MkdirTemp(scratchDirectory, ".cleardev-git-object-view-")
	if err != nil {
		return nil, fmt.Errorf("create trusted Git object view: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(viewRoot) }
	gitDirectory := filepath.Join(viewRoot, "repository")
	viewObjects := filepath.Join(gitDirectory, "objects")
	for _, directory := range []string{
		gitDirectory, viewObjects, filepath.Join(viewObjects, "info"), filepath.Join(gitDirectory, "refs"),
		filepath.Join(viewRoot, "home"), filepath.Join(viewRoot, "xdg"), filepath.Join(viewRoot, "tmp"),
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			cleanup()
			return nil, fmt.Errorf("create trusted Git directory: %w", err)
		}
	}
	if err := linkCandidateGitObjects(objectsPath, viewObjects); err != nil {
		cleanup()
		return nil, err
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tbare = true\n"
	if err := os.WriteFile(filepath.Join(gitDirectory, "config"), []byte(config), 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("write trusted Git config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "HEAD"), []byte("ref: refs/heads/unused\n"), 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("write trusted Git HEAD: %w", err)
	}
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("find Git executable: %w", err)
	}
	gitExecutable, err = filepath.Abs(gitExecutable)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("resolve Git executable: %w", err)
	}
	command := exec.CommandContext(ctx, gitExecutable,
		"--no-replace-objects", "--git-dir="+gitDirectory, "cat-file", "--batch-command",
	) //nolint:gosec // The executable is resolved before the blank child environment is installed.
	command.Dir = viewRoot
	command.Env = []string{
		"LC_ALL=C",
		"LANG=C",
		"HOME=" + filepath.Join(viewRoot, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(viewRoot, "xdg"),
		"TMPDIR=" + filepath.Join(viewRoot, "tmp"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_COUNT=0",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OBJECT_DIRECTORY=" + viewObjects,
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open trusted Git input: %w", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cleanup()
		return nil, fmt.Errorf("open trusted Git output: %w", err)
	}
	diagnostics := &candidateDiagnosticBuffer{limit: candidateSourceGitDiagnosticLimit}
	command.Stderr = diagnostics
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		cleanup()
		return nil, fmt.Errorf("start trusted Git object reader: %w", err)
	}
	return &candidateGitObjectReader{
		command: command, stdin: stdin, input: bufio.NewWriter(stdin),
		output: bufio.NewReaderSize(stdout, candidateSourceGitHeaderLimit), stderr: diagnostics, viewRoot: viewRoot,
	}, nil
}

func ensureCandidateGitScratchDirectory(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("candidate Git scratch directory is empty")
	}
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create candidate Git scratch directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect candidate Git scratch directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("candidate Git scratch directory is not a real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // path is a directory that must be private to the AO user.
			return fmt.Errorf("restrict candidate Git scratch directory: %w", err)
		}
	}
	return nil
}

func (reader *candidateGitObjectReader) objectInfo(oid string) (candidateGitObjectInfo, error) {
	if reader.closed || !validCandidateSourceOID(oid) {
		return candidateGitObjectInfo{}, errors.New("trusted Git object request is invalid")
	}
	if _, err := fmt.Fprintf(reader.input, "info %s\n", oid); err != nil {
		return candidateGitObjectInfo{}, fmt.Errorf("write trusted Git object request: %w", err)
	}
	if err := reader.input.Flush(); err != nil {
		return candidateGitObjectInfo{}, fmt.Errorf("flush trusted Git object request: %w", err)
	}
	return reader.readObjectHeader(oid)
}

func (reader *candidateGitObjectReader) copyObject(info candidateGitObjectInfo, destination io.Writer) (candidateGitObjectDigest, error) {
	if reader.closed || !validCandidateSourceOID(info.OID) || info.Size < 0 {
		return candidateGitObjectDigest{}, errors.New("trusted Git object content request is invalid")
	}
	if _, err := fmt.Fprintf(reader.input, "contents %s\n", info.OID); err != nil {
		return candidateGitObjectDigest{}, fmt.Errorf("write trusted Git object content request: %w", err)
	}
	if err := reader.input.Flush(); err != nil {
		return candidateGitObjectDigest{}, fmt.Errorf("flush trusted Git object content request: %w", err)
	}
	contentInfo, err := reader.readObjectHeader(info.OID)
	if err != nil {
		return candidateGitObjectDigest{}, err
	}
	if contentInfo != info {
		return candidateGitObjectDigest{}, errors.New("trusted Git object metadata changed while reading")
	}
	gitHash := sha1.New() //nolint:gosec // Git object identity is defined by SHA-1.
	contentHash := sha256.New()
	if _, err := fmt.Fprintf(gitHash, "%s %d%c", info.Type, info.Size, byte(0)); err != nil {
		return candidateGitObjectDigest{}, err
	}
	written, err := io.CopyN(io.MultiWriter(destination, gitHash, contentHash), reader.output, info.Size)
	if err != nil || written != info.Size {
		return candidateGitObjectDigest{}, errors.New("trusted Git returned a truncated object")
	}
	terminator, err := reader.output.ReadByte()
	if err != nil || terminator != '\n' {
		return candidateGitObjectDigest{}, errors.New("trusted Git object response is not terminated")
	}
	if actual := hex.EncodeToString(gitHash.Sum(nil)); actual != info.OID {
		return candidateGitObjectDigest{}, errors.New("trusted Git object content does not match its object id")
	}
	return candidateGitObjectDigest{Info: info, ContentSHA256: hex.EncodeToString(contentHash.Sum(nil))}, nil
}

func (reader *candidateGitObjectReader) readObjectHeader(requestedOID string) (candidateGitObjectInfo, error) {
	line, err := reader.output.ReadSlice('\n')
	if err != nil {
		return candidateGitObjectInfo{}, fmt.Errorf("read trusted Git object header: %w: %s", err, reader.stderr.String())
	}
	if len(line) > candidateSourceGitHeaderLimit || len(line) == 0 || line[len(line)-1] != '\n' {
		return candidateGitObjectInfo{}, errors.New("trusted Git object header exceeds its limit")
	}
	fields := bytes.Fields(bytes.TrimSuffix(line, []byte{'\n'}))
	if len(fields) == 2 && string(fields[0]) == requestedOID && string(fields[1]) == "missing" {
		return candidateGitObjectInfo{}, fmt.Errorf("candidate Git object %s is missing", requestedOID)
	}
	if len(fields) != 3 || string(fields[0]) != requestedOID {
		return candidateGitObjectInfo{}, errors.New("trusted Git returned an invalid object header")
	}
	objectType := string(fields[1])
	if objectType != "commit" && objectType != "tree" && objectType != "blob" {
		return candidateGitObjectInfo{}, fmt.Errorf("candidate Git object has unsupported type %q", objectType)
	}
	size, err := strconv.ParseInt(string(fields[2]), 10, 64)
	if err != nil || size < 0 {
		return candidateGitObjectInfo{}, errors.New("trusted Git returned an invalid object size")
	}
	return candidateGitObjectInfo{OID: requestedOID, Type: objectType, Size: size}, nil
}

func (reader *candidateGitObjectReader) Close() error {
	if reader.closed {
		return nil
	}
	reader.closed = true
	flushErr := reader.input.Flush()
	closeErr := reader.stdin.Close()
	waitErr := reader.command.Wait()
	removeErr := os.RemoveAll(reader.viewRoot)
	if flushErr != nil {
		return fmt.Errorf("flush trusted Git object reader: %w", flushErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close trusted Git object reader: %w", closeErr)
	}
	if waitErr != nil {
		return fmt.Errorf("wait for trusted Git object reader: %w: %s", waitErr, reader.stderr.String())
	}
	if removeErr != nil {
		return fmt.Errorf("remove trusted Git object view: %w", removeErr)
	}
	return nil
}

func (buffer *candidateDiagnosticBuffer) Write(payload []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		chunk := payload
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		_, _ = buffer.buffer.Write(chunk)
	}
	return len(payload), nil
}

func (buffer *candidateDiagnosticBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func resolveCandidateGitObjects(workspacePath string) (string, error) {
	workspacePath, err := filepath.Abs(filepath.Clean(workspacePath))
	if err != nil {
		return "", fmt.Errorf("resolve candidate workspace: %w", err)
	}
	workspaceInfo, err := os.Stat(workspacePath)
	if err != nil || !workspaceInfo.IsDir() {
		return "", errors.New("candidate workspace is unavailable")
	}
	dotGitPath := filepath.Join(workspacePath, ".git")
	dotGitInfo, err := os.Lstat(dotGitPath)
	if err != nil {
		return "", fmt.Errorf("inspect candidate Git directory: %w", err)
	}
	gitDirectory := dotGitPath
	if !dotGitInfo.IsDir() {
		if !dotGitInfo.Mode().IsRegular() {
			return "", errors.New("candidate .git entry is not a directory or regular gitdir file")
		}
		value, readErr := readCandidateGitPathFile(dotGitPath, "gitdir: ")
		if readErr != nil {
			return "", readErr
		}
		gitDirectory = value
		if !filepath.IsAbs(gitDirectory) {
			gitDirectory = filepath.Join(workspacePath, gitDirectory)
		}
	}
	gitDirectory, err = filepath.EvalSymlinks(filepath.Clean(gitDirectory))
	if err != nil {
		return "", fmt.Errorf("resolve candidate gitdir: %w", err)
	}
	gitInfo, err := os.Stat(gitDirectory)
	if err != nil || !gitInfo.IsDir() {
		return "", errors.New("candidate gitdir is unavailable")
	}
	commonDirectory := gitDirectory
	commonPath := filepath.Join(gitDirectory, "commondir")
	commonInfo, commonErr := os.Lstat(commonPath)
	if commonErr == nil {
		if !commonInfo.Mode().IsRegular() {
			return "", errors.New("candidate commondir is not a regular file")
		}
		commonDirectory, err = readCandidateGitPathFile(commonPath, "")
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(commonDirectory) {
			commonDirectory = filepath.Join(gitDirectory, commonDirectory)
		}
	} else if !errors.Is(commonErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect candidate commondir: %w", commonErr)
	}
	commonDirectory, err = filepath.EvalSymlinks(filepath.Clean(commonDirectory))
	if err != nil {
		return "", fmt.Errorf("resolve candidate common Git directory: %w", err)
	}
	objectsPath, err := filepath.EvalSymlinks(filepath.Join(commonDirectory, "objects"))
	if err != nil {
		return "", fmt.Errorf("resolve candidate Git objects: %w", err)
	}
	objectsInfo, err := os.Stat(objectsPath)
	if err != nil || !objectsInfo.IsDir() {
		return "", errors.New("candidate Git object directory is unavailable")
	}
	for _, name := range []string{"alternates", "http-alternates"} {
		if err := rejectCandidateGitAlternates(filepath.Join(objectsPath, "info", name)); err != nil {
			return "", err
		}
	}
	return objectsPath, nil
}

func readCandidateGitPathFile(path, prefix string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read candidate Git path file: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, candidateSourceGitPathFileLimit+1))
	closeErr := file.Close()
	if readErr != nil {
		return "", fmt.Errorf("read candidate Git path file: %w", readErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close candidate Git path file: %w", closeErr)
	}
	if len(payload) == 0 || len(payload) > candidateSourceGitPathFileLimit || bytes.IndexByte(payload, 0) >= 0 {
		return "", errors.New("candidate Git path file is invalid")
	}
	line := strings.TrimSuffix(string(payload), "\n")
	line = strings.TrimSuffix(line, "\r")
	if strings.ContainsAny(line, "\r\n") || !strings.HasPrefix(line, prefix) {
		return "", errors.New("candidate Git path file is malformed")
	}
	value := strings.TrimPrefix(line, prefix)
	if value == "" {
		return "", errors.New("candidate Git path file is empty")
	}
	return value, nil
}

func rejectCandidateGitAlternates(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect candidate Git alternates: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("candidate Git alternates entry is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read candidate Git alternates: %w", err)
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, candidateSourceAlternatesFileLimit+1))
	closeErr := file.Close()
	if readErr != nil {
		return fmt.Errorf("read candidate Git alternates: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close candidate Git alternates: %w", closeErr)
	}
	if len(payload) > candidateSourceAlternatesFileLimit || len(bytes.TrimSpace(payload)) != 0 {
		return errors.New("candidate Git object alternates are not allowed")
	}
	return nil
}

func linkCandidateGitObjects(sourceObjects, viewObjects string) error {
	packSource := filepath.Join(sourceObjects, "pack")
	packInfo, err := os.Lstat(packSource)
	if err == nil {
		if !packInfo.IsDir() || packInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("candidate Git pack directory is not a real directory")
		}
		if err := os.Symlink(packSource, filepath.Join(viewObjects, "pack")); err != nil {
			return fmt.Errorf("link candidate Git pack directory: %w", err)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(filepath.Join(viewObjects, "pack"), 0o700); err != nil {
			return fmt.Errorf("create empty candidate Git pack directory: %w", err)
		}
	} else {
		return fmt.Errorf("inspect candidate Git pack directory: %w", err)
	}
	for value := 0; value < 256; value++ {
		name := fmt.Sprintf("%02x", value)
		source := filepath.Join(sourceObjects, name)
		info, statErr := os.Lstat(source)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect candidate loose object directory %q: %w", name, statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("candidate loose object directory %q is not a real directory", name)
		}
		if err := os.Symlink(source, filepath.Join(viewObjects, name)); err != nil {
			return fmt.Errorf("link candidate loose object directory %q: %w", name, err)
		}
	}
	return nil
}
