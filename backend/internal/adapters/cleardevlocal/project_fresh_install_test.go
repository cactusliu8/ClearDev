package cleardevlocal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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

func TestFreshProjectSameContainerAndNoInstalledReuse(t *testing.T) {
	requireProductionCheckPrerequisites(t, projectInstallImage)
	baselineGateData(t)
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "package.json"), `{"name":"fresh","version":"1.0.0","scripts":{"install":"node install.cjs"}}`)
	writeTestFile(t, filepath.Join(source, "install.cjs"), `const fs=require('fs'),os=require('os');if(fs.existsSync('node_modules/identity'))throw Error('old installation reused');require('child_process').execFileSync('cc',['-x','c','-','-o','node_modules/native-check'],{input:'int main(){return 0;}'});setTimeout(()=>fs.writeFileSync('node_modules/identity',os.hostname()),2500);`)
	writeTestFile(t, filepath.Join(source, "check.cjs"), `const fs=require('fs'),os=require('os');if(fs.readFileSync('node_modules/identity','utf8')!==os.hostname())throw Error('different container'); require('child_process').execFileSync('node_modules/native-check');console.log('fresh-installed-and-checked');`)
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(source) })
	usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.DefaultProjectRuntimeV1()
	for i := 0; i < 2; i++ {
		cid := filepath.Join(t.TempDir(), "container.cid")
		called := false
		var volumes []string
		request := CheckContainerRequest{Image: projectInstallImage, FreshInstall: true, ProjectRuntime: &runtime, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items, Argv: []string{"node", "check.cjs"}, Timeout: 2 * time.Second, PreparationDeadline: time.Now().Add(time.Minute), MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, CIDFile: cid, ExecutionProfile: core.NodeCheckSmallThreadsV1}
		request.BeforeExecute = func() error {
			called = true
			id, err := os.ReadFile(cid)
			if err != nil {
				return err
			}
			out, err := exec.Command("docker", "inspect", "--format", "{{range .Mounts}}{{if eq .Type \"volume\"}}{{.Name}} {{end}}{{end}}", strings.TrimSpace(string(id))).Output()
			volumes = strings.Fields(string(out))
			return err
		}
		result, err := RunCheckContainer(context.Background(), request)
		if err != nil || result.Outcome != CheckContainerPass || !called || !result.CommandExecuted || !strings.Contains(result.Output, "fresh-installed-and-checked") {
			t.Fatalf("round %d result=%+v err=%v", i, result, err)
		}
		if len(volumes) != 2 {
			t.Fatalf("expected disk volumes, got %v", volumes)
		}
		for _, v := range volumes {
			if err := exec.Command("docker", "volume", "inspect", v).Run(); err == nil {
				t.Fatalf("disposable volume survived: %s", v)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(source, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("candidate source mutated")
	}
}

func TestFreshProjectPreparationFailureDoesNotExecute(t *testing.T) {
	requireProductionCheckPrerequisites(t, projectInstallImage)
	baselineGateData(t)
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "package.json"), `{"name":"fail-install","scripts":{"install":"node -e 'process.exit(7)'"}}`)
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(source) })
	usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.DefaultProjectRuntimeV1()
	request := CheckContainerRequest{Image: projectInstallImage, FreshInstall: true, ProjectRuntime: &runtime, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items, Argv: []string{"node", "-e", "console.log('SHOULD_NOT_RUN')"}, Timeout: time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, CIDFile: filepath.Join(t.TempDir(), "cid"), BeforeExecute: func() error {
		t.Error("execution registered before install success")
		return fmt.Errorf("must not execute")
	}}
	result, err := RunCheckContainer(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "project dependency installation failed") || result.CommandExecuted || result.Outcome != CheckContainerInfraError || strings.Contains(result.Output, "SHOULD_NOT_RUN") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestFreshProjectInstallsCachedPackageEveryRun(t *testing.T) {
	requireProductionCheckPrerequisites(t, projectInstallImage)
	runner, freeze := newProjectFreezeFixture(t, true)
	freeze.ProjectExecution.Runtime.PrepareArgv = []string{}
	freeze.ProjectExecution.Basis.Runtime.PrepareArgv = []string{}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"package/package.json": `{"name":"example","version":"1.0.0","main":"index.js","scripts":{"install":"node setup.js"}}`,
		"package/setup.js":     `require('fs').writeFileSync('index.js','module.exports='+JSON.stringify(require('os').hostname()))`,
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, artifact := npmArtifactFixture(t, archive.Bytes())
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write(archive.Bytes()) }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := projectNPMDownloadClient()
	client.Transport = localRegistryTransport{target: target}
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "package.json"), `{"name":"fresh-package","version":"1.0.0","dependencies":{"example":"1.0.0"}}`)
	lock, _ := json.Marshal(map[string]any{"name": "fresh-package", "version": "1.0.0", "lockfileVersion": 3, "requires": true, "packages": map[string]any{"": map[string]any{"name": "fresh-package", "version": "1.0.0", "dependencies": map[string]string{"example": "1.0.0"}}, "node_modules/example": map[string]any{"version": "1.0.0", "resolved": artifact.URL, "integrity": artifact.Integrity, "hasInstallScript": true}}})
	writeTestFile(t, filepath.Join(source, "package-lock.json"), string(lock))
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(source) })
	usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.DefaultProjectRuntimeV1()
	cache, err := npmArchiveCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		root := t.TempDir()
		archives := filepath.Join(root, "archives")
		if err := downloadCachedNPMArtifacts(context.Background(), client, []lockedNPMArtifact{artifact}, archives, cache, 1024*1024, 1024*1024, time.Second, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		request := CheckContainerRequest{Image: projectInstallImage, FreshInstall: true, NPMArchives: archives, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items, ProjectRuntime: &runtime, CIDFile: filepath.Join(root, "container.cid"), Argv: []string{"node", "-e", "if(require('example')!==require('os').hostname())throw Error('not installed in this container');console.log('CACHE_PACKAGE_PASS')"}, Timeout: 5 * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024}
		result, err := RunCheckContainer(context.Background(), request)
		if err != nil || result.Outcome != CheckContainerPass || !strings.Contains(result.Output, "CACHE_PACKAGE_PASS") {
			t.Fatalf("round %d: %+v err=%v", i, result, err)
		}
	}
	for _, name := range []string{"package.json", "package-lock.json"} {
		raw, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(freeze.WorkspacePath, name), string(raw))
	}
	writeTestFile(t, filepath.Join(freeze.WorkspacePath, "checks/store.test.cjs"), "require('assert').equal(require('example'),require('os').hostname());require('assert').equal(require('fs').readFileSync('/sys/fs/cgroup/memory.max','utf8').trim(),'2147483648');require('assert').equal(require('fs').readFileSync('/sys/fs/cgroup/pids.max','utf8').trim(),'128');console.log('x'.repeat(70000));")
	frozen, err := runner.FreezeMailCandidate(context.Background(), freeze)
	if err != nil {
		t.Fatal(err)
	}
	spec := freeze.ProjectExecution.Basis.Checks[0]
	result, err := runner.RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{RunID: "fresh-cache-runner", WorkspacePath: freeze.WorkspacePath, CandidateSHA: frozen.CandidateSHA, Image: core.StandardCandidateCheckImage, Argv: spec.Argv, Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, ProjectExecution: freeze.ProjectExecution})
	if err != nil || result.Outcome != ports.ClearDevCheckPass || result.DependencyCacheKey != "" || result.DependencyEnvironmentID != "" {
		t.Fatalf("real runner: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("AO_DATA_DIR"), checkDependencyDirectory, "index.json")); !os.IsNotExist(err) {
		t.Fatalf("installed cache index created: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("warm cache redownloaded: %d", calls.Load())
	}
}

func seedFreshFixtureArchive(t *testing.T, source, dependencies string) {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	root := filepath.Join(dependencies, "fixture-driver")
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: "package/" + filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, artifact := npmArtifactFixture(t, archive.Bytes())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive.Bytes()) }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := projectNPMDownloadClient()
	client.Transport = localRegistryTransport{target: target}
	cache, err := npmArchiveCacheRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := downloadCachedNPMArtifacts(context.Background(), client, []lockedNPMArtifact{artifact}, filepath.Join(t.TempDir(), "archives"), cache, 1024*1024, 1024*1024, time.Second, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"name": "check-fixture", "lockfileVersion": 3, "requires": true, "packages": map[string]any{"": map[string]any{"name": "check-fixture", "dependencies": map[string]string{"fixture-driver": "1.0.0"}}, "node_modules/fixture-driver": map[string]any{"version": "1.0.0", "resolved": artifact.URL, "integrity": artifact.Integrity, "hasInstallScript": true}}})
	if err := os.Chmod(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(source, "package-lock.json"), string(raw))
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
}

func TestFreshProjectImportsMoreThanCommandArgumentLimit(t *testing.T) {
	requireProductionCheckPrerequisites(t, projectInstallImage)
	baselineGateData(t)
	source, archives := t.TempDir(), t.TempDir()
	dependencies := map[string]string{}
	packages := map[string]any{}
	const count = 70
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("fixture-%02d", i)
		dependencies[name] = "1.0.0"
		manifest := fmt.Sprintf(`{"name":%q,"version":"1.0.0","main":"index.js"}`, name)
		var archive bytes.Buffer
		gz := gzip.NewWriter(&archive)
		tw := tar.NewWriter(gz)
		for path, body := range map[string]string{"package/package.json": manifest, "package/index.js": fmt.Sprintf("module.exports=%d", i)} {
			if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0644, Size: int64(len(body))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		_, artifact := npmArtifactFixture(t, archive.Bytes())
		artifact.URL = fmt.Sprintf("https://registry.npmjs.org/%s/-/%s-1.0.0.tgz", name, name)
		packages["node_modules/"+name] = map[string]any{"version": "1.0.0", "resolved": artifact.URL, "integrity": artifact.Integrity}
		writeTestFile(t, filepath.Join(archives, artifact.Filename), archive.String())
	}
	manifest := map[string]any{"name": "large-install", "version": "1.0.0", "dependencies": dependencies}
	packages[""] = manifest
	lock := map[string]any{"name": "large-install", "version": "1.0.0", "lockfileVersion": 3, "requires": true, "packages": packages}
	for name, value := range map[string]any{"package.json": manifest, "package-lock.json": lock} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(source, name), string(raw))
	}
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(source) })
	// Bind mounts must be readable by the isolated container UID.
	if err := sealDependencyTree(archives); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(archives) })
	usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.DefaultProjectRuntimeV1()
	script := `for(let i=0;i<70;i++){if(require('fixture-'+String(i).padStart(2,'0'))!==i)throw Error('missing package '+i)}console.log('ALL_70_INSTALLED')`
	result, err := RunCheckContainer(context.Background(), CheckContainerRequest{Image: projectInstallImage, FreshInstall: true, NPMArchives: archives, ProjectRuntime: &runtime, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items, CIDFile: filepath.Join(t.TempDir(), "cid"), Argv: []string{"node", "-e", script}, Timeout: 5 * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, Offline: true})
	if err != nil || result.Outcome != CheckContainerPass || !strings.Contains(result.Output, "Imported verified packages 70/70") || !strings.Contains(result.Output, "ALL_70_INSTALLED") {
		t.Fatalf("large offline install: %+v %v", result, err)
	}
}

func TestFreshProjectDiskCapacityStopsBeforeCheck(t *testing.T) {
	requireProductionCheckPrerequisites(t, projectInstallImage)
	baselineGateData(t)
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "package.json"), `{"name":"capacity","scripts":{"install":"node install.cjs"}}`)
	writeTestFile(t, filepath.Join(source, "install.cjs"), `const fs=require('fs');fs.writeFileSync('node_modules/large','');fs.truncateSync('node_modules/large',2*1024*1024*1024);`)
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(source) })
	usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.DefaultProjectRuntimeV1()
	result, err := RunCheckContainer(context.Background(), CheckContainerRequest{Image: projectInstallImage, FreshInstall: true, ProjectRuntime: &runtime, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items, Argv: []string{"node", "-e", "console.log('BAD_FORMAL_RUN')"}, Timeout: time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 4096, CIDFile: filepath.Join(t.TempDir(), "cid")})
	if !errors.Is(err, errCheckCapacityExceeded) || result.CommandExecuted || result.Outcome != CheckContainerInfraError {
		t.Fatalf("capacity: %+v err=%v", result, err)
	}
}

func TestFreshProjectPreparationCancellationCleansIdentity(t *testing.T) {
	requireProductionCheckPrerequisites(t, projectInstallImage)
	baselineGateData(t)
	source := t.TempDir()
	writeTestFile(t, filepath.Join(source, "package.json"), `{"name":"wait-install","scripts":{"install":"node -e 'console.log(\"INSTALL_WAIT\");setTimeout(()=>{},30000)'"}}`)
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(source) })
	usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.DefaultProjectRuntimeV1()
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadline := time.Now().Add(time.Minute)
			if mode == "deadline" {
				deadline = time.Now().Add(3 * time.Second)
			} else {
				timer := time.AfterFunc(3*time.Second, cancel)
				defer timer.Stop()
			}
			cid := filepath.Join(t.TempDir(), "cid")
			result, err := RunCheckContainer(ctx, CheckContainerRequest{Image: projectInstallImage, FreshInstall: true, ProjectRuntime: &runtime, PreparationDeadline: deadline, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items, Argv: []string{"node", "-e", "console.log('BAD_FORMAL_RUN')"}, Timeout: time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 4096, CIDFile: cid})
			if err == nil || result.CommandExecuted || result.Outcome != CheckContainerInfraError {
				t.Fatalf("cancel: %+v %v", result, err)
			}
			for _, path := range []string{cid, cid + ".volumes.json"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("cleanup identity retained after completed cleanup: %s %v", path, err)
				}
			}
		})
	}
}

func TestProjectCheckMemoryLimitRejectsExcess(t *testing.T) {
	runtime := core.DefaultProjectRuntimeV1()
	request := CheckContainerRequest{Image: projectInstallImage, ProjectRuntime: &runtime, FreshInstall: true, SourceDir: t.TempDir(), Argv: []string{"node", "-e", "process.exit(0)"}, Timeout: time.Second, MemoryBytes: core.ProjectCheckMemoryBytes, PidsLimit: 64, OutputLimit: 4096}
	args, err := CheckContainerRunArgs(request, filepath.Join(t.TempDir(), "cid"))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--memory=2147483648") || !strings.Contains(joined, "--memory-swap=2147483648") {
		t.Fatal(joined)
	}
	request.MemoryBytes++
	if _, err := CheckContainerRunArgs(request, filepath.Join(t.TempDir(), "cid")); err == nil {
		t.Fatal("accepted above 2GiB")
	}
}
