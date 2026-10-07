package cleardevlocal

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMaterializeCandidateSourceWritesStableVerifiedManifest(t *testing.T) {
	repo := newCandidateMaterializationRepo(t)
	writeCandidateMaterializationFile(t, filepath.Join(repo, "package.json"), "{\"private\":true}\n", 0o600)
	if err := os.Mkdir(filepath.Join(repo, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCandidateMaterializationFile(t, filepath.Join(repo, "src", "app.js"), "export const answer = 42;\n", 0o600)
	writeCandidateMaterializationFile(t, filepath.Join(repo, "tool.sh"), "#!/bin/sh\nexit 0\n", 0o700)
	candidateMaterializationGit(t, repo, "add", ".")
	candidateMaterializationGit(t, repo, "commit", "-m", "candidate")
	candidate := strings.TrimSpace(candidateMaterializationGit(t, repo, "rev-parse", "HEAD"))

	destination := filepath.Join(t.TempDir(), "candidate")
	t.Cleanup(func() { cleanupPreparedTree(destination) })
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	scratchDirectory := filepath.Join(t.TempDir(), "git")
	facts, err := materializeCandidateSource(
		context.Background(), repo, candidate, scratchDirectory, destination, manifestPath, defaultCandidateSourceLimits(),
	)
	if err != nil {
		t.Fatalf("materialize candidate source: %v", err)
	}
	if facts.Manifest.CandidateSHA != candidate || facts.Manifest.RootTreeOID == "" || facts.ManifestID == "" {
		t.Fatalf("incomplete facts: %#v", facts)
	}
	if facts.ManifestPath != manifestPath || facts.SourceDirectory != destination {
		t.Fatalf("paths not recorded: %#v", facts)
	}
	if info, err := os.Lstat(scratchDirectory); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("scratch directory was not safely created: mode=%v err=%v", info, err)
	}
	if facts.Manifest.ItemCount != 4 {
		t.Fatalf("item count = %d, want 4", facts.Manifest.ItemCount)
	}
	for index := 1; index < len(facts.Manifest.Entries); index++ {
		if facts.Manifest.Entries[index-1].Path >= facts.Manifest.Entries[index].Path {
			t.Fatalf("manifest is not sorted: %#v", facts.Manifest.Entries)
		}
	}
	assertCandidateMaterializationMode(t, filepath.Join(destination, "package.json"), 0o444)
	assertCandidateMaterializationMode(t, filepath.Join(destination, "src"), 0o555)
	assertCandidateMaterializationMode(t, filepath.Join(destination, "src", "app.js"), 0o444)
	assertCandidateMaterializationMode(t, filepath.Join(destination, "tool.sh"), 0o555)
	if err := verifyMaterializedCandidateSource(destination, facts.Manifest); err != nil {
		t.Fatalf("verify materialized candidate: %v", err)
	}
	packageJSON, err := readCandidateSourceRootFile(
		context.Background(), repo, t.TempDir(), facts.Manifest, "package.json", 1024,
	)
	if err != nil {
		t.Fatalf("read package.json from manifest: %v", err)
	}
	if string(packageJSON) != "{\"private\":true}\n" {
		t.Fatalf("package.json = %q", packageJSON)
	}
	if _, err := readCandidateSourceRootFile(context.Background(), repo, t.TempDir(), facts.Manifest, "src/app.js", 1024); err == nil {
		t.Fatal("non-root npm manifest was accepted")
	}

	canonical, manifestID, err := canonicalCandidateSourceManifest(facts.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persisted, canonical) || manifestID != facts.ManifestID {
		t.Fatal("persisted manifest or manifest id changed")
	}
	second, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), defaultCandidateSourceLimits())
	if err != nil {
		t.Fatalf("inspect candidate again: %v", err)
	}
	if second.ManifestID != facts.ManifestID || !bytes.Equal(mustCandidateManifestJSON(t, second.Manifest), canonical) {
		t.Fatal("same candidate did not produce a stable manifest")
	}
}

func TestCandidateSourceRawReaderIgnoresExecutableGitControls(t *testing.T) {
	repo := newCandidateMaterializationRepo(t)
	writeCandidateMaterializationFile(t, filepath.Join(repo, ".gitattributes"), "payload.txt filter=evil\n", 0o600)
	writeCandidateMaterializationFile(t, filepath.Join(repo, "payload.txt"), "original\n", 0o600)
	candidateMaterializationGit(t, repo, "add", ".")
	candidateMaterializationGit(t, repo, "commit", "-m", "original")
	original := strings.TrimSpace(candidateMaterializationGit(t, repo, "rev-parse", "HEAD"))
	writeCandidateMaterializationFile(t, filepath.Join(repo, "payload.txt"), "replacement\n", 0o600)
	candidateMaterializationGit(t, repo, "add", "payload.txt")
	candidateMaterializationGit(t, repo, "commit", "-m", "replacement")
	replacement := strings.TrimSpace(candidateMaterializationGit(t, repo, "rev-parse", "HEAD"))
	candidateMaterializationGit(t, repo, "replace", original, replacement)

	sentinel := filepath.Join(t.TempDir(), "executed")
	executableDirectory := t.TempDir()
	executable := filepath.Join(executableDirectory, "host-action")
	writeCandidateMaterializationFile(t, executable, "#!/bin/sh\n: > '"+sentinel+"'\ncat\n", 0o700)
	writeCandidateMaterializationFile(t, filepath.Join(executableDirectory, "post-checkout"), "#!/bin/sh\n: > '"+sentinel+"'\n", 0o700)
	candidateMaterializationGit(t, repo, "config", "core.hooksPath", executableDirectory)
	candidateMaterializationGit(t, repo, "config", "filter.evil.smudge", executable)
	candidateMaterializationGit(t, repo, "config", "filter.evil.process", executable)
	candidateMaterializationGit(t, repo, "config", "filter.evil.required", "true")
	infoDirectory := filepath.Join(repo, ".git", "info")
	if err := os.MkdirAll(infoDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCandidateMaterializationFile(t, filepath.Join(infoDirectory, "attributes"), "payload.txt filter=evil\n", 0o600)
	maliciousConfig := filepath.Join(t.TempDir(), "malicious.gitconfig")
	writeCandidateMaterializationFile(t, maliciousConfig, "[core]\n\thooksPath = "+executableDirectory+"\n[filter \"evil\"]\n\tsmudge = "+executable+"\n\trequired = true\n", 0o600)
	t.Setenv("GIT_CONFIG_GLOBAL", maliciousConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", maliciousConfig)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", executableDirectory)
	t.Setenv("GIT_CONFIG_PARAMETERS", "'filter.evil.smudge'='"+executable+"'")
	t.Setenv("GIT_REPLACE_REF_BASE", "refs/replace")
	t.Setenv("GIT_WORK_TREE", repo)
	t.Setenv("GIT_ATTR_NOSYSTEM", "0")
	localConfig, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := localConfig.WriteString("[malformed-local-config\n"); err != nil {
		_ = localConfig.Close()
		t.Fatal(err)
	}
	if err := localConfig.Close(); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "candidate")
	t.Cleanup(func() { cleanupPreparedTree(destination) })
	_, err = materializeCandidateSource(
		context.Background(), repo, original, t.TempDir(), destination,
		filepath.Join(t.TempDir(), "manifest.json"), defaultCandidateSourceLimits(),
	)
	if err != nil {
		t.Fatalf("materialize hostile repository: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(destination, "payload.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "original\n" {
		t.Fatalf("replacement or filter changed payload: %q", payload)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("host Git hook or filter ran: %v", err)
	}
}

func TestCandidateSourceRejectsTreeModeObjectTypeMismatch(t *testing.T) {
	repo := newCandidateMaterializationRepo(t)
	blobOID := strings.TrimSpace(candidateMaterializationGitInput(t, repo, []byte("not a tree\n"), "hash-object", "-w", "--stdin"))
	rawOID, err := hex.DecodeString(blobOID)
	if err != nil {
		t.Fatal(err)
	}
	rawTree := append([]byte("40000 directory\x00"), rawOID...)
	treeOID := strings.TrimSpace(candidateMaterializationGitInput(t, repo, rawTree, "hash-object", "-w", "-t", "tree", "--stdin"))
	candidate := strings.TrimSpace(candidateMaterializationGit(t, repo, "commit-tree", treeOID, "-m", "type mismatch"))
	if _, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), defaultCandidateSourceLimits()); err == nil {
		t.Fatal("tree mode pointing at a blob was accepted")
	}
}

func TestCandidateSourceRejectsGitAlternatesBeforeReadingObjects(t *testing.T) {
	repo, candidate := committedCandidateMaterializationRepo(t, map[string]string{"file.txt": "data\n"})
	infoDirectory := filepath.Join(repo, ".git", "objects", "info")
	if err := os.MkdirAll(infoDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCandidateMaterializationFile(t, filepath.Join(infoDirectory, "alternates"), t.TempDir()+"\n", 0o600)
	_, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), defaultCandidateSourceLimits())
	if err == nil || !strings.Contains(err.Error(), "alternates") {
		t.Fatalf("non-empty alternates error = %v", err)
	}
}

func TestCandidateSourceRejectsSymlinkAndSubmodule(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("test requires Git symlink mode")
		}
		repo := newCandidateMaterializationRepo(t)
		if err := os.Symlink("target", filepath.Join(repo, "link")); err != nil {
			t.Fatal(err)
		}
		candidateMaterializationGit(t, repo, "add", "link")
		candidateMaterializationGit(t, repo, "commit", "-m", "symlink")
		candidate := strings.TrimSpace(candidateMaterializationGit(t, repo, "rev-parse", "HEAD"))
		_, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), defaultCandidateSourceLimits())
		if err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlink error = %v", err)
		}
	})

	t.Run("submodule", func(t *testing.T) {
		repo, base := committedCandidateMaterializationRepo(t, map[string]string{"base.txt": "base\n"})
		candidateMaterializationGit(t, repo, "update-index", "--add", "--cacheinfo", "160000", base, "vendor")
		candidateMaterializationGit(t, repo, "commit", "-m", "gitlink")
		candidate := strings.TrimSpace(candidateMaterializationGit(t, repo, "rev-parse", "HEAD"))
		_, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), defaultCandidateSourceLimits())
		if err == nil || !strings.Contains(err.Error(), "submodule") {
			t.Fatalf("submodule error = %v", err)
		}
	})
}

func TestCandidateSourceCapacityLimitsUseExactLogicalValues(t *testing.T) {
	repo, candidate := committedCandidateMaterializationRepo(t, map[string]string{"a.txt": "abc", "b.txt": "1234"})
	exact := candidateSourceLimits{MaxBytes: 7, MaxItems: 2}
	facts, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), exact)
	if err != nil {
		t.Fatalf("exact limits were rejected: %v", err)
	}
	if facts.Manifest.BlobBytes != 7 || facts.Manifest.ItemCount != 2 {
		t.Fatalf("totals = %d/%d, want 7/2", facts.Manifest.BlobBytes, facts.Manifest.ItemCount)
	}
	if _, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), candidateSourceLimits{MaxBytes: 6, MaxItems: 2}); err == nil {
		t.Fatal("byte limit +1 was accepted")
	}
	if _, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), candidateSourceLimits{MaxBytes: 7, MaxItems: 1}); err == nil {
		t.Fatal("item limit +1 was accepted")
	}
	if _, err := inspectCandidateSource(context.Background(), repo, candidate, t.TempDir(), candidateSourceLimits{MaxBytes: 7, MaxItems: 2, MaxTreeBytes: 1}); err == nil {
		t.Fatal("tree metadata limit +1 was accepted")
	}
}

func TestParseCandidateRawTreeRejectsUnsafeRecords(t *testing.T) {
	oid, err := hex.DecodeString(strings.Repeat("1", 40))
	if err != nil {
		t.Fatal(err)
	}
	entry := func(mode, name string) []byte {
		value := []byte(mode + " " + name)
		value = append(value, 0)
		return append(value, oid...)
	}
	valid := append(entry("100644", "a"), entry("100755", "b")...)
	if parsed, err := parseCandidateRawTree(valid); err != nil || len(parsed) != 2 {
		t.Fatalf("valid tree parse = %#v, %v", parsed, err)
	}
	for name, payload := range map[string][]byte{
		"unsorted":  append(entry("100644", "b"), entry("100644", "a")...),
		"duplicate": append(entry("100644", "a"), entry("100755", "a")...),
		"dotdot":    entry("100644", ".."),
		"backslash": entry("100644", `dir\file`),
		"symlink":   entry("120000", "link"),
		"submodule": entry("160000", "vendor"),
		"badmode":   entry("100600", "secret"),
		"truncated": valid[:len(valid)-1],
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCandidateRawTree(payload); err == nil {
				t.Fatalf("unsafe tree %q was accepted", name)
			}
		})
	}
}

func TestVerifyMaterializedCandidateSourceDetectsTampering(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		tamper func(*testing.T, string)
	}{
		{
			name: "content",
			tamper: func(t *testing.T, destination string) {
				path := filepath.Join(destination, "file.txt")
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				writeCandidateMaterializationFile(t, path, "evil\n", 0o600)
				if err := os.Chmod(path, 0o444); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode",
			tamper: func(t *testing.T, destination string) {
				if err := os.Chmod(filepath.Join(destination, "file.txt"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra path",
			tamper: func(t *testing.T, destination string) {
				if err := os.Chmod(destination, 0o755); err != nil {
					t.Fatal(err)
				}
				writeCandidateMaterializationFile(t, filepath.Join(destination, "extra.txt"), "extra\n", 0o600)
				if err := os.Chmod(destination, 0o555); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo, candidate := committedCandidateMaterializationRepo(t, map[string]string{"file.txt": "safe\n"})
			destination := filepath.Join(t.TempDir(), "candidate")
			t.Cleanup(func() { cleanupPreparedTree(destination) })
			facts, err := materializeCandidateSource(
				context.Background(), repo, candidate, t.TempDir(), destination,
				filepath.Join(t.TempDir(), "manifest.json"), defaultCandidateSourceLimits(),
			)
			if err != nil {
				t.Fatal(err)
			}
			testCase.tamper(t, destination)
			if err := verifyMaterializedCandidateSource(destination, facts.Manifest); err == nil {
				t.Fatal("tampered candidate source was accepted")
			}
		})
	}
}

func newCandidateMaterializationRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	candidateMaterializationGit(t, repo, "init")
	candidateMaterializationGit(t, repo, "config", "user.email", "test@example.com")
	candidateMaterializationGit(t, repo, "config", "user.name", "ClearDev Test")
	return repo
}

func committedCandidateMaterializationRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	repo := newCandidateMaterializationRepo(t)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	for _, path := range paths {
		fullPath := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		writeCandidateMaterializationFile(t, fullPath, files[path], 0o600)
	}
	candidateMaterializationGit(t, repo, "add", ".")
	candidateMaterializationGit(t, repo, "commit", "-m", "candidate")
	return repo, strings.TrimSpace(candidateMaterializationGit(t, repo, "rev-parse", "HEAD"))
}

func candidateMaterializationGit(t *testing.T, repo string, arguments ...string) string {
	t.Helper()
	argv := append([]string{"-C", repo}, arguments...)
	output, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func candidateMaterializationGitInput(t *testing.T, repo string, input []byte, arguments ...string) string {
	t.Helper()
	argv := append([]string{"-C", repo}, arguments...)
	command := exec.Command("git", argv...)
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func writeCandidateMaterializationFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertCandidateMaterializationMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), mode)
	}
}

func mustCandidateManifestJSON(t *testing.T, manifest candidateSourceManifest) []byte {
	t.Helper()
	payload, _, err := canonicalCandidateSourceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
