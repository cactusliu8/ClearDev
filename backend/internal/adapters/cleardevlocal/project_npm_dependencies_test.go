package cleardevlocal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type localRegistryTransport struct{ target *url.URL }

func TestProjectNPMNewMachineNeedsNoHostCache(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-host-cache")
	t.Setenv("CLEARDEV_NPM_CACHE_DIR", missing)
	if _, err := dependencyPreparationNPMCache(t.TempDir(), false); err == nil {
		t.Fatal("legacy cache requirement changed")
	}
	cache, err := dependencyPreparationNPMCache(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 0 {
		t.Fatal("fresh preparation cache not empty")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("preparation mutated host cache")
	}
}

func (t localRegistryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.URL.Scheme, cloned.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(cloned)
}

func TestProjectNPMMissingCacheInstallsVerifiedPackageInRealOfflineContainer(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	// Nine distinct packages cross multiple bounded imports; the final partial
	// batch must also be installed, and no lifecycle script may execute.
	packages := map[string]any{}
	dependencies := map[string]string{}
	archives := map[string][]byte{}
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("example-%d", i)
		dependencies[name] = "1.0.0"
		var archive bytes.Buffer
		compressed := gzip.NewWriter(&archive)
		tarball := tar.NewWriter(compressed)
		manifest, _ := json.Marshal(map[string]any{"name": name, "version": "1.0.0", "main": "index.js", "scripts": map[string]string{"postinstall": "node -e 'process.exit(99)'"}})
		for path, content := range map[string]string{"package/package.json": string(manifest), "package/index.js": "exports.answer = 42;\n"} {
			if err := tarball.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(content))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tarball.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tarball.Close(); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		path := "/" + name + "/-/" + name + "-1.0.0.tgz"
		archives[path] = append([]byte(nil), archive.Bytes()...)
		digest := sha512.Sum512(archive.Bytes())
		packages["node_modules/"+name] = map[string]any{"version": "1.0.0", "resolved": "https://registry.npmjs.org" + path, "integrity": "sha512-" + base64.StdEncoding.EncodeToString(digest[:])}
	}
	packages[""] = map[string]any{"name": "fixture", "version": "1.0.0", "dependencies": dependencies}
	lock, _ := json.Marshal(map[string]any{"name": "fixture", "version": "1.0.0", "requires": true, "lockfileVersion": 3, "packages": packages})
	manifest, _ := json.Marshal(map[string]any{"name": "fixture", "version": "1.0.0", "dependencies": dependencies, "scripts": map[string]string{"postinstall": "node -e 'process.exit(98)'"}})
	staging := t.TempDir()
	writeTestFile(t, filepath.Join(staging, "package.json"), string(manifest))
	writeTestFile(t, filepath.Join(staging, "package-lock.json"), string(lock))
	for _, name := range []string{"package.json", "package-lock.json"} {
		if err := os.Chmod(filepath.Join(staging, name), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	if err := os.Chmod(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	request := ports.ClearDevCheckPreflightRequest{Image: core.StandardCandidateCheckImage, Timeout: time.Minute, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64}
	image, err := inspectCheckImage(context.Background(), request.Image)
	if err != nil {
		t.Fatal(err)
	}
	container, err := startDependencyPreparationContainer(context.Background(), request, image, staging, cache, filepath.Join(t.TempDir(), "cid"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	if err := runDependencyPreparation(context.Background(), request, container, staging); err == nil || !strings.Contains(err.Error(), "ENOTCACHED") {
		t.Fatalf("empty cache did not reproduce: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, ok := archives[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := projectNPMDownloadClient()
	client.Transport = localRegistryTransport{target: target}
	if err := prepareMissingProjectNPMDependenciesWithClient(context.Background(), request, container, staging, lock, client); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("docker", "exec", container, "node", "-e", "for(let i=0;i<9;i++) if (require('/prepare/node_modules/example-'+i).answer !== 42) process.exit(1)").CombinedOutput()
	if err != nil {
		t.Fatalf("actual offline install failed: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(staging, "downloads")); !os.IsNotExist(err) {
		t.Fatal("download staging was retained")
	}
	if _, err := os.Stat(filepath.Join(staging, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("installed into candidate source")
	}
}

func npmArtifactFixture(t *testing.T, payload []byte) ([]byte, lockedNPMArtifact) {
	t.Helper()
	digest := sha512.Sum512(payload)
	entry := map[string]any{"version": "1.0.0", "resolved": "https://registry.npmjs.org/example/-/example-1.0.0.tgz", "integrity": "sha512-" + base64.StdEncoding.EncodeToString(digest[:])}
	raw, err := json.Marshal(map[string]any{"lockfileVersion": 3, "packages": map[string]any{"": map[string]any{}, "node_modules/example": entry}})
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := lockedNPMArtifacts(raw)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("parse artifact: %v %v", artifacts, err)
	}
	return raw, artifacts[0]
}

func TestProjectNPMDownloadsExactBytesAndRejectsInvalidSources(t *testing.T) {
	payload := []byte("verified package bytes")
	raw, artifact := npmArtifactFixture(t, payload)
	for _, bad := range []string{"http://registry.npmjs.org/example.tgz", "https://localhost/example.tgz", "https://registry.npmjs.org:443/example.tgz", "https://user:secret@registry.npmjs.org/example.tgz", "https://registry.npmjs.org/example.tgz?token=secret"} {
		if _, err := lockedNPMArtifacts([]byte(strings.Replace(string(raw), artifact.URL, bad, 1))); err == nil {
			t.Fatalf("accepted source %s", bad)
		}
	}
	if _, err := lockedNPMArtifacts([]byte(strings.Replace(string(raw), artifact.Integrity, "sha512-invalid", 1))); err == nil {
		t.Fatal("accepted incomplete digest")
	}
	if _, err := lockedNPMArtifacts([]byte(`{"lockfileVersion":3,"packages":{"node_modules/example":{"link":true}}}`)); err == nil {
		t.Fatal("accepted linked package")
	}
	for _, mode := range []string{"success", "corrupt", "oversize", "redirect", "missing", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("download leaked credentials")
				}
				switch mode {
				case "redirect":
					http.Redirect(w, r, "https://private.example/package.tgz", http.StatusFound)
				case "missing":
					w.WriteHeader(http.StatusNotFound)
				case "corrupt":
					_, _ = w.Write([]byte("different bytes"))
				case "oversize":
					w.(http.Flusher).Flush()
					_, _ = w.Write(payload)
				default:
					_, _ = w.Write(payload)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := projectNPMDownloadClient()
			client.Transport = localRegistryTransport{target: target}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			limit := int64(100)
			if mode == "oversize" {
				limit = 3
			}
			directory := filepath.Join(t.TempDir(), "downloads")
			err := downloadLockedNPMArtifacts(ctx, client, []lockedNPMArtifact{artifact}, directory, limit)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(directory, artifact.Filename))
				if err != nil || string(got) != string(payload) {
					t.Fatal("downloaded bytes changed")
				}
			} else if err == nil {
				t.Fatalf("accepted %s", mode)
			}
			if mode == "oversize" && !errors.Is(err, errCheckCapacityExceeded) {
				t.Fatalf("wrong capacity error: %v", err)
			}
			if mode == "redirect" && calls.Load() != 2 {
				t.Fatalf("followed forbidden redirect: %d", calls.Load())
			}
		})
	}
}

func TestProjectDependencyRecoveryRequiresExactSettledReceipt(t *testing.T) {
	runner, freeze := newProjectFreezeFixture(t, true)
	check := freeze.ProjectExecution.Basis.Checks[0]
	request, err := normalizeCheckRequest(ports.ClearDevCheckRequest{
		RunID: "project-dependency-recovery-receipt", WorkspacePath: freeze.WorkspacePath,
		CandidateSHA: freeze.BaseSHA, Image: core.StandardCandidateCheckImage, Argv: check.Argv,
		Timeout: time.Duration(check.TimeoutSeconds) * time.Second, ProjectExecution: freeze.ProjectExecution,
	})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := inspectCandidateSource(context.Background(), freeze.WorkspacePath, freeze.BaseSHA, t.TempDir(), defaultCandidateSourceLimits())
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := checkRequestSHA256(request)
	if err != nil {
		t.Fatal(err)
	}
	path, err := checkRunStatePath(request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	result := ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, CandidateSHA: request.CandidateSHA,
		SourceManifestID: facts.ManifestID, SourceRootTreeOID: facts.Manifest.RootTreeOID,
		SourceItems: int64(facts.Manifest.ItemCount), SourceBytes: facts.Manifest.BlobBytes}
	base := checkRunState{Version: checkRunStateVersion, Fingerprint: fingerprint, State: "settled", Result: &result,
		SourceManifest: &facts.Manifest, Error: "offline npm dependency preparation failed: ENOTCACHED"}
	for _, tc := range []struct {
		name                string
		mutate              func(*checkRunState, *ports.ClearDevCheckResult)
		eligible, wantError bool
	}{
		{"eligible", func(*checkRunState, *ports.ClearDevCheckResult) {}, true, false},
		{"fresh installation failure", func(s *checkRunState, r *ports.ClearDevCheckResult) {
			s.Error = "project dependency installation failed: timeout"
			r.Image = projectInstallImage
			r.CheckEnvironmentID = "prepared"
		}, true, false},
		{"fresh command already executed", func(s *checkRunState, r *ports.ClearDevCheckResult) {
			s.Error = "project dependency installation failed: timeout"
			r.Image = projectInstallImage
			r.CheckEnvironmentID = "prepared"
			r.TrialCommandExecuted = true
		}, false, false},
		{"unknown action", func(s *checkRunState, _ *ports.ClearDevCheckResult) { s.State = "started" }, false, false},
		{"wrong fingerprint", func(s *checkRunState, _ *ports.ClearDevCheckResult) { s.Fingerprint = strings.Repeat("0", 64) }, false, false},
		{"no manifest", func(s *checkRunState, _ *ports.ClearDevCheckResult) { s.SourceManifest = nil }, false, false},
		{"corrupt source binding", func(_ *checkRunState, r *ports.ClearDevCheckResult) { r.SourceManifestID = strings.Repeat("0", 64) }, false, true},
		{"command failure", func(_ *checkRunState, r *ports.ClearDevCheckResult) { r.Outcome = ports.ClearDevCheckFail }, false, false},
		{"command already started", func(_ *checkRunState, r *ports.ClearDevCheckResult) { r.CheckEnvironmentID = "prepared" }, false, false},
		{"other infrastructure failure", func(s *checkRunState, _ *ports.ClearDevCheckResult) { s.Error = "docker unavailable" }, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, changedResult := base, result
			state.Result = &changedResult
			tc.mutate(&state, &changedResult)
			if err := writeCheckRunState(path, state); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			eligible, err := runner.CanRetryProjectDependencyCheck(context.Background(), request)
			if eligible != tc.eligible || (err != nil) != tc.wantError {
				t.Fatalf("eligible=%v error=%v", eligible, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("eligibility changed the original receipt")
			}
		})
	}
}

// Explicit Docker process double: a slow import must retain the context cause,
// not report only the operating system's generic "signal: killed" text.
func TestProjectNPMImportReportsDeadline(t *testing.T) {
	baselineGateData(t)
	directory := t.TempDir()
	docker := filepath.Join(directory, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexec sleep 10\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	payload := []byte("verified bytes for explicit process double")
	lock, _ := npmArtifactFixture(t, payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := projectNPMDownloadClient()
	client.Transport = localRegistryTransport{target: target}
	err := prepareMissingProjectNPMDependenciesWithClient(context.Background(), ports.ClearDevCheckPreflightRequest{Timeout: 200 * time.Millisecond}, "explicit-container-double", t.TempDir(), lock, client)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "import verified npm packages") {
		t.Fatalf("import deadline cause missing: %v", err)
	}
}
