package cleardevlocal

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type lockedNPMArtifact struct {
	URL       string
	Integrity string
	Filename  string
}

// A new machine need not already have a host npm cache. Use an empty, bounded
// staging directory to reproduce the cache miss without creating user files.
func dependencyPreparationNPMCache(staging string, project bool) (string, error) {
	cache, err := trustedNPMCachePath()
	if err == nil || !project {
		return cache, err
	}
	cache = filepath.Join(staging, "empty-npm-cache")
	if err := os.Mkdir(cache, 0o555); err != nil { //nolint:gosec // empty, read-only cache must be traversable by the isolated container user.
		return "", err
	}
	return cache, nil
}

// CanRetryProjectDependencyCheck proves a settled preparation failure for the
// exact request; it never grants a retry of an unknown external action.
func (r *Runner) CanRetryProjectDependencyCheck(ctx context.Context, request ports.ClearDevCheckRequest) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if request.ProjectExecution == nil || request.RunID == "" {
		return false, nil
	}
	request, err := normalizeCheckRequest(request)
	if err != nil {
		return false, err
	}
	path, err := checkRunStatePath(request.RunID)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	var state checkRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		return false, err
	}
	fingerprint, err := checkReceiptFingerprint(request, state)
	if err != nil {
		return false, err
	}
	if state.Version != checkRunStateVersion || state.Fingerprint != fingerprint || state.State != "settled" || state.Result == nil || state.SourceManifest == nil {
		return false, nil
	}
	result := state.Result
	if result.CandidateSHA != request.CandidateSHA || result.TrialCommandExecuted || (result.CheckEnvironmentID != "" && result.Image != projectInstallImage) || result.Outcome != ports.ClearDevCheckInfraError {
		return false, nil
	}
	if err := verifySavedCandidateManifest(*state.SourceManifest, checkEnvironmentForResult(*result)); err != nil {
		return false, err
	}
	eligible := strings.HasPrefix(state.Error, "offline npm dependency preparation failed:") || strings.HasPrefix(state.Error, "project npm dependency preparation failed:") || (result.Image == projectInstallImage && strings.HasPrefix(state.Error, "project dependency installation failed:"))
	if eligible && result.Image == projectInstallImage {
		return workflowCheckResourcesReleased(ctx, request.RunID)
	}
	return eligible, nil
}

// Only public registry tarballs are fetched. No package metadata, candidate
// configuration, install script, Builder directory or credential is used.
func lockedNPMArtifacts(raw []byte) ([]lockedNPMArtifact, error) {
	var lock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Resolved  string `json:"resolved"`
			Integrity string `json:"integrity"`
			Link      bool   `json:"link"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil || lock.Packages == nil || (lock.LockfileVersion != 2 && lock.LockfileVersion != 3) {
		return nil, errors.New("automatic dependency download requires a v2/v3 npm lockfile")
	}
	artifacts := []lockedNPMArtifact{}
	seen := map[string]bool{}
	for path, entry := range lock.Packages {
		if path == "" {
			continue
		}
		if entry.Link || !strings.HasPrefix(path, "node_modules/") {
			return nil, errors.New("automatic dependency download does not support linked or local packages")
		}
		if err := validateNPMArtifactURL(entry.Resolved); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(entry.Integrity, "sha512-") {
			return nil, errors.New("automatic dependency download requires SHA-512 integrity for every package")
		}
		digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(entry.Integrity, "sha512-"))
		if err != nil || len(digest) != sha512.Size {
			return nil, errors.New("locked package SHA-512 integrity is invalid")
		}
		key := sha256.Sum256([]byte(entry.Integrity))
		filename := hex.EncodeToString(key[:]) + ".tgz"
		if !seen[filename] {
			artifacts = append(artifacts, lockedNPMArtifact{URL: entry.Resolved, Integrity: entry.Integrity, Filename: filename})
			seen[filename] = true
		}
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Filename < artifacts[j].Filename })
	return artifacts, nil
}

func validateNPMArtifactURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "registry.npmjs.org" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, ".tgz") || strings.ContainsAny(u.Path, "\\\x00\r\n") {
		return errors.New("automatic dependency download accepts only public registry.npmjs.org HTTPS tarballs without credentials")
	}
	return nil
}

func validateNPMTransferURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Host == "registry.npmmirror.com" || u.Host == "cdn.npmmirror.com" {
		u.Host = "registry.npmjs.org"
	}
	return validateNPMArtifactURL(u.String())
}

func projectNPMDownloadClient() *http.Client {
	return &http.Client{
		// A progressing transfer is bounded by the preparation context, not two minutes.

		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("package download has too many redirects")
			}
			return validateNPMTransferURL(req.URL.String())
		},
	}
}

func downloadLockedNPMArtifacts(ctx context.Context, client *http.Client, artifacts []lockedNPMArtifact, directory string, maxBytes int64) error {
	if maxBytes <= 0 || len(artifacts) > int(productionCheckCapacityLimits().Dependency.Items)-4 {
		return fmt.Errorf("%w: dependency download allowance is exhausted", errCheckCapacityExceeded)
	}
	if err := os.Mkdir(directory, 0o755); err != nil { //nolint:gosec // verified public tarballs are read by the non-root offline container.
		return err
	}
	remaining := maxBytes
	for _, artifact := range artifacts {
		if err := downloadNPMWithRetry(ctx, client, artifact, directory, &remaining, dependencyDownloadIdle, time.Second); err != nil {
			return err
		}
	}
	return nil
}

func downloadLockedNPMArtifact(ctx context.Context, client *http.Client, artifact lockedNPMArtifact, directory string, remaining *int64, timer *time.Timer, idle time.Duration) error {
	if err := validateNPMTransferURL(artifact.URL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, http.NoBody)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download locked npm package: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return npmHTTPError(response.StatusCode)
	}
	if response.ContentLength > *remaining {
		return fmt.Errorf("%w: locked npm downloads exceed their fixed allowance", errCheckCapacityExceeded)
	}
	file, err := os.OpenFile(filepath.Join(directory, artifact.Filename), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha512.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(npmProgressReader{response.Body, timer, idle}, *remaining))
	*remaining -= written
	if err != nil {
		return err
	}
	var extra [1]byte
	if n, readErr := response.Body.Read(extra[:]); n != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return fmt.Errorf("%w: locked npm downloads exceed their fixed allowance", errCheckCapacityExceeded)
	}
	if "sha512-"+base64.StdEncoding.EncodeToString(hash.Sum(nil)) != artifact.Integrity {
		return errors.New("downloaded npm package does not match the candidate lockfile SHA-512")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Chmod(0o444)
}

// Public package bytes are downloaded separately and verified before entering
// the original network-disabled installation container. npm's integrity cache
// can consume a local tarball without querying the registry or running scripts.
func prepareMissingProjectNPMDependencies(ctx context.Context, request ports.ClearDevCheckPreflightRequest, containerID, staging string, lock []byte) error {
	return prepareMissingProjectNPMDependenciesWithClient(ctx, request, containerID, staging, lock, projectNPMDownloadClient())
}

func prepareMissingProjectNPMDependenciesWithClient(ctx context.Context, request ports.ClearDevCheckPreflightRequest, containerID, staging string, lock []byte, client *http.Client) error {
	artifacts, err := lockedNPMArtifacts(lock)
	if err != nil {
		return err
	}
	downloadCtx, cancel := context.WithTimeout(ctx, request.Timeout)
	defer cancel()
	directory := filepath.Join(staging, "downloads")
	defer func() { _ = os.RemoveAll(directory) }()
	allowance := productionCheckCapacityLimits().Dependency.Bytes - int64(len(lock)) - checkManifestLimit
	cacheRoot, err := npmArchiveCacheRoot()
	if err != nil {
		return err
	}
	if err := downloadCachedNPMArtifacts(downloadCtx, client, artifacts, directory, cacheRoot, allowance, npmArchiveCacheBytes, dependencyDownloadIdle, time.Second); err != nil {
		return err
	}
	// Reuse the pinned image's npm tarball importer in one process. Starting
	// npm anew per package/batch can consume the entire preparation deadline.
	argv := []string{"exec", "--user=65532:65532", "--workdir=/prepare", containerID, "node", "-e", projectNPMCacheImportScript}
	for _, artifact := range artifacts {
		argv = append(argv, "/input/downloads/"+artifact.Filename)
	}
	collector := newOutputCollector(checkPreparationOutputLimit, cancel)
	command := exec.CommandContext(downloadCtx, "docker", argv...) //nolint:gosec // fixed offline script and digest-generated local artifact filenames.
	command.Stdout, command.Stderr = collector, collector
	if err := command.Run(); err != nil {
		if collector.LimitExceeded() {
			return errors.New("import verified npm packages exceeded its output limit")
		}
		if cause := downloadCtx.Err(); cause != nil {
			return fmt.Errorf("import verified npm packages: %w: %s", cause, collector.String())
		}
		return fmt.Errorf("import verified npm packages: %w: %s", err, collector.String())
	}
	return runDependencyPreparationUsingCache(downloadCtx, request, containerID, staging, "/prepare/.download-cache")
}

// This is npm cache.add's tarball + manifest sequence, using the bundled
// pacote from the immutable Node/npm image. Only verified local paths enter
// argv; package content never becomes code. Four imports at most are active.
const projectNPMCacheImportScript = `
const path = require('node:path');
const npmRoot = path.resolve(path.dirname(process.execPath), '../lib/node_modules/npm');
const pacote = require(require.resolve('pacote', {paths: [npmRoot]}));
const options = {offline: true, ignoreScripts: true, cache: '/prepare/.download-cache/_cacache'};
(async () => {
  const files = process.argv.slice(1);
  for (let start = 0; start < files.length; start += 4) {
    const batch = files.slice(start, start + 4);
    await Promise.all(batch.map(async spec => {
      await pacote.tarball.stream(spec, stream => { stream.resume(); return stream.promise(); }, options);
      await pacote.manifest(spec, {...options, fullMetadata: true});
    }));
    console.log('Imported verified packages ' + (start + batch.length) + '/' + files.length);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
`
