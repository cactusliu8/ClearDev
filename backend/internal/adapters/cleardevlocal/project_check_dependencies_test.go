package cleardevlocal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func projectDependencyContainerFixture(t *testing.T, install string) CheckContainerRequest {
	t.Helper()
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	source, dependencies := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(source, "package.json"), `{"name":"check-fixture","version":"1.0.0","dependencies":{"fixture-driver":"1.0.0"},"scripts":{"install":"node root-install.cjs"}}`)
	writeTestFile(t, filepath.Join(source, "root-install.cjs"), `require('fs').writeFileSync('node_modules/root-installed', 'ok')`)
	writeTestFile(t, filepath.Join(source, "check.cjs"), `
const fs=require('fs'), assert=require('assert/strict'), cp=require('child_process');
assert.equal(require('fixture-driver'), 42);
assert.equal(fs.readFileSync('node_modules/root-installed','utf8'), 'ok');
assert.equal(fs.existsSync('node_modules/from-previous-check'), false);
fs.writeFileSync('node_modules/from-previous-check','unique');
fs.mkdirSync('node_modules/fixture-driver/build/Release', {recursive:true});
fs.writeFileSync('node_modules/fixture-driver/build/Release/run', '#!/bin/sh\nexit 0\n', {mode:0o700});
cp.execFileSync('./node_modules/fixture-driver/build/Release/run');
assert.throws(()=>fs.writeFileSync('check.cjs','changed'));
assert.throws(()=>fs.writeFileSync('/dependency-template/fixture-driver/package.json','changed'));
console.log('DEPENDENCY_COPY_PASS');
`)
	if err := os.Mkdir(filepath.Join(dependencies, "fixture-driver"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dependencies, "fixture-driver", "package.json"), `{"name":"fixture-driver","version":"1.0.0","scripts":{"install":"node install.cjs"},"main":"index.cjs"}`)
	writeTestFile(t, filepath.Join(dependencies, "fixture-driver", "install.cjs"), install)
	if err := sealDependencyTree(dependencies); err != nil {
		t.Fatal(err)
	}
	if err := sealDependencyTree(source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupPreparedTree(source); cleanupPreparedTree(dependencies) })
	usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	runtime := core.DefaultProjectRuntimeV1()
	return CheckContainerRequest{Image: core.StandardCandidateCheckImage, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items,
		DependencyDir: dependencies, ProjectRuntime: &runtime, Argv: []string{"node", "check.cjs"}, Timeout: 30 * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024}
}

func TestProjectCheckDependencyFailuresRealContainer(t *testing.T) {
	for _, tc := range []struct {
		name, install string
		timeout       time.Duration
		outcome       CheckContainerOutcome
	}{
		{"failed-install", "console.log('INSTALL_FAILED');process.exit(7)", 30 * time.Second, CheckContainerFail},
		{"timed-out-install", "console.log('INSTALL_STARTED');setInterval(()=>{},1000)", 2 * time.Second, CheckContainerTimedOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := projectDependencyContainerFixture(t, tc.install)
			request.Timeout = tc.timeout
			result, err := RunCheckContainer(context.Background(), request)
			if err != nil || result.Outcome != tc.outcome || result.CommandExecuted || strings.Contains(result.Output, "DEPENDENCY_COPY_PASS") {
				t.Fatalf("installation must stop the check: %+v %v", result, err)
			}
		})
	}
}

func TestProjectCheckDependencyCancellationRealContainer(t *testing.T) {
	request := projectDependencyContainerFixture(t, `setInterval(()=>{},1000)`)
	request.CIDFile = filepath.Join(t.TempDir(), "container.cid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(1500*time.Millisecond, cancel)
	defer timer.Stop()
	result, err := RunCheckContainer(ctx, request)
	if err == nil || result.CommandExecuted {
		t.Fatalf("cancelled install reached check: %+v %v", result, err)
	}
	if _, err := os.Stat(request.CIDFile); !os.IsNotExist(err) {
		t.Fatalf("cancelled container was not confirmed removed: %v", err)
	}
}

func TestProjectDependencyExportRejectsOutsideLinksRealContainer(t *testing.T) {
	request := projectDependencyContainerFixture(t, `require('fs').symlinkSync('/etc/passwd','outside');require('fs').writeFileSync('index.cjs','module.exports=42')`)
	request.Argv = nil
	request.DependencyExport = t.TempDir()
	result, err := RunCheckContainer(context.Background(), request)
	if err == nil || result.Outcome != CheckContainerInfraError {
		t.Fatalf("outside link exported: %+v %v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(request.DependencyExport, "fixture-driver/outside")); !os.IsNotExist(err) {
		t.Fatal("outside link was created on host")
	}
}

func TestProjectDependencyDownloadRealContainer(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Local fixture server on Docker's bridge; no published host port and no
	// external registry. This proves that lifecycle downloads actually run.
	raw, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--detach", "--network=bridge", "--read-only", "--cap-drop=ALL", "--user=65532:65532", "--memory=67108864", core.StandardCandidateCheckImage, "node", "-e", `require('http').createServer((q,s)=>s.end('module.exports=42')).listen(8080,'0.0.0.0')`).CombinedOutput()
	if err != nil {
		t.Fatalf("start local dependency server: %s %v", raw, err)
	}
	id := strings.TrimSpace(string(raw))
	if !validDockerID(id) {
		t.Fatalf("invalid server id: %q", id)
	}
	t.Cleanup(func() {
		if err := removeCheckContainer(context.Background(), id); err != nil {
			t.Error(err)
		}
	})
	raw, err = exec.CommandContext(ctx, "docker", "inspect", "--format", `{{(index .NetworkSettings.Networks "bridge").IPAddress}}`, id).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect local server: %s %v", raw, err)
	}
	url := "http://" + strings.TrimSpace(string(raw)) + ":8080/driver"
	install := fmt.Sprintf(`require('http').get(%q,r=>{let text='';r.on('data',b=>text+=b);r.on('end',()=>require('fs').writeFileSync('index.cjs',text));}).on('error',e=>{console.error(e);process.exit(1)});`, url)
	request := projectDependencyContainerFixture(t, install)
	result, err := RunCheckContainer(ctx, request)
	if err != nil || result.Outcome != CheckContainerPass || !strings.Contains(result.Output, "DEPENDENCY_COPY_PASS") {
		t.Fatalf("local download: %+v %v", result, err)
	}
}

func TestProjectCheckDependencyExportRealContainer(t *testing.T) {
	request := projectDependencyContainerFixture(t, `require('fs').writeFileSync('index.cjs','module.exports=42')`)
	request.DependencyExport = t.TempDir()
	request.Argv = nil
	result, err := RunCheckContainer(context.Background(), request)
	if err != nil || result.Outcome != CheckContainerPass {
		t.Fatalf("export: %+v %v", result, err)
	}
	b, err := os.ReadFile(filepath.Join(request.DependencyExport, "fixture-driver", "index.cjs"))
	if err != nil || string(b) != "module.exports=42" {
		t.Fatalf("installed output missing: %q %v", b, err)
	}
}

func TestProjectCheckNativeDriverRealContainer(t *testing.T) {
	path := os.Getenv("CLEARDEV_TEST_NATIVE_DRIVER")
	if path == "" {
		t.Skip("set CLEARDEV_TEST_NATIVE_DRIVER to the verified better-sqlite3 Node22 Linux x64 release asset")
	}
	native, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// This is an explicit third-party test input, not a Builder test result.
	if sha256Hex(native) != "cdc21ea975a7b7a2c9cab78a930b71ed61ff6a426a394ca8d18adc939b45d04d" {
		t.Fatal("native test input digest differs")
	}
	request := projectDependencyContainerFixture(t, `require('fs').copyFileSync('/workspace/input.node','driver.node');require('fs').writeFileSync('index.cjs','module.exports=42')`)
	if err := os.Chmod(request.SourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.SourceDir, "input.node"), native, 0o444); err != nil {
		t.Fatal(err)
	}
	code := `const driver=require('./node_modules/fixture-driver/driver.node'); if(typeof driver.Database!=='function')throw Error('driver did not load'); console.log('NATIVE_LOAD_PASS');`
	if err := os.WriteFile(filepath.Join(request.SourceDir, "native-check.cjs"), []byte(code), 0o444); err != nil {
		t.Fatal(err)
	}
	request.Argv = []string{"node", "native-check.cjs"}
	usage, err := measureCheckTree(request.SourceDir, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	request.SourceBytes, request.SourceItems = usage.Bytes, usage.Items
	result, err := RunCheckContainer(context.Background(), request)
	if err != nil || result.Outcome != CheckContainerPass || !strings.Contains(result.Output, "NATIVE_LOAD_PASS") {
		t.Fatalf("native output: %+v %v", result, err)
	}
}

func TestProjectCheckDependenciesRealContainer(t *testing.T) {
	request := projectDependencyContainerFixture(t, `require('fs').writeFileSync('index.cjs','module.exports=42')`)
	before, err := dependencyTreeSHA256(request.DependencyDir)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := RunCheckContainer(context.Background(), request)
		if err != nil || result.Outcome != CheckContainerPass || !strings.Contains(result.Output, "DEPENDENCY_COPY_PASS") {
			t.Fatalf("independent copy failed: result=%+v err=%v", result, err)
		}
	}
	after, err := dependencyTreeSHA256(request.DependencyDir)
	if err != nil || before != after {
		t.Fatalf("shared template changed: %s %s %v", before, after, err)
	}
}

func TestProjectInstalledDependenciesReachPreview(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	runner, freeze := newProjectFreezeFixture(t, false)
	writeProjectSQLiteFixture(t, freeze.WorkspacePath)
	writeTestFile(t, filepath.Join(freeze.WorkspacePath, "package.json"), `{"name":"installed-preview-fixture","version":"1.0.0","scripts":{"install":"node migrations/install.cjs"}}`)
	writeTestFile(t, filepath.Join(freeze.WorkspacePath, "migrations/install.cjs"), `const fs=require('fs');if(fs.existsSync('node_modules/previous-check'))throw Error('contaminated');fs.writeFileSync('node_modules/installed.cjs','module.exports=42');console.log('INSTALL_IN_CONTAINER');console.log('x'.repeat(8192));`)
	check := filepath.Join(freeze.WorkspacePath, "checks/store.test.cjs")
	original, err := os.ReadFile(check)
	if err != nil {
		t.Fatal(err)
	}
	// Include an observable use of the installed output in the real check.
	writeTestFile(t, check, string(original)+"\nrequire('assert/strict').equal(require('../node_modules/installed.cjs'),42);require('fs').writeFileSync('node_modules/previous-check','do not carry into preview');\n")
	frozen, err := runner.FreezeMailCandidate(context.Background(), freeze)
	if err != nil {
		t.Fatal(err)
	}
	spec := freeze.ProjectExecution.Basis.Checks[0]
	result, err := runner.RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{RunID: "installed-preview-check", WorkspacePath: freeze.WorkspacePath, CandidateSHA: frozen.CandidateSHA, Image: core.StandardCandidateCheckImage, Argv: spec.Argv, Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, ProjectExecution: freeze.ProjectExecution})
	if err != nil || result.Outcome != ports.ClearDevCheckPass {
		t.Fatalf("check: %+v %v", result, err)
	}
	if !strings.Contains(result.OutputSummary, "INSTALL_IN_CONTAINER") || result.OutputTruncated || result.OutputSHA256 != sha256Hex([]byte(result.OutputSummary)) {
		t.Fatalf("successful installation evidence missing or invalid: %+v", result)
	}
	limited, err := runner.RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{RunID: "installed-preview-limited", WorkspacePath: freeze.WorkspacePath, CandidateSHA: frozen.CandidateSHA, Image: core.StandardCandidateCheckImage, Argv: spec.Argv, Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 4096, ProjectExecution: freeze.ProjectExecution})
	if err != nil || limited.Outcome != ports.ClearDevCheckPass || !limited.OutputTruncated || len(limited.OutputSummary) != 4096 || limited.OutputSHA256 != sha256Hex([]byte(limited.OutputSummary)) || !strings.Contains(limited.OutputSummary, "INSTALL_IN_CONTAINER") {
		t.Fatalf("combined preparation/check output limit invalid: %+v %v", limited, err)
	}
	for range 2 {
		source, err := runner.PrepareProjectResult(context.Background(), freeze.WorkspacePath, frozen.CandidateSHA, *freeze.ProjectExecution)
		if err != nil {
			t.Fatal(err)
		}
		b, readErr := os.ReadFile(filepath.Join(source.WorkspacePath, "node_modules/installed.cjs"))
		if readErr != nil || string(b) != "module.exports=42" {
			t.Errorf("prepared install missing: %q %v", b, readErr)
		}
		if _, err := os.Stat(filepath.Join(source.WorkspacePath, "node_modules/previous-check")); !os.IsNotExist(err) {
			t.Error("previous check leaked into preview")
		}
		if err := source.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectTemplateExecutableSurvivesCopy(t *testing.T) {
	request := projectDependencyContainerFixture(t, "")
	tool := filepath.Join(request.DependencyDir, "fixture-driver", "install.cjs")
	if err := os.Chmod(tool, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("#!/usr/bin/env node\nrequire('fs').writeFileSync('index.cjs','module.exports=42')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tool, 0o555); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(request.DependencyDir, "fixture-driver", "package.json")
	if err := os.Chmod(manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"name":"fixture-driver","version":"1.0.0","main":"index.cjs","scripts":{"install":"./install.cjs"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sealDependencyTree(request.DependencyDir); err != nil {
		t.Fatal(err)
	}
	result, err := RunCheckContainer(context.Background(), request)
	if err != nil || result.Outcome != CheckContainerPass || !result.CommandExecuted {
		t.Fatalf("template executable must survive: %+v %v", result, err)
	}
}

func TestProjectInstallerCompilesNativeWithoutHeaderDownload(t *testing.T) {
	requireProductionCheckPrerequisites(t, projectInstallImage)
	request := projectDependencyContainerFixture(t, `const fs=require('fs'),cp=require('child_process');
fs.writeFileSync('addon.cc', '#include <node_api.h>\nstatic napi_value init(napi_env e,napi_value x){napi_value v;napi_create_int32(e,42,&v);return v;}\nNAPI_MODULE(NODE_GYP_MODULE_NAME, init)\n');
fs.writeFileSync('binding.gyp',JSON.stringify({targets:[{target_name:'addon',sources:['addon.cc']}]}));
cp.execFileSync('node',['/usr/local/lib/node_modules/npm/node_modules/node-gyp/bin/node-gyp.js','rebuild','--nodedir=/usr/local'],{stdio:'inherit'});
fs.writeFileSync('index.cjs',"module.exports=require('./build/Release/addon.node')");`)
	request.Image = projectInstallImage
	request.Offline = true
	result, err := RunCheckContainer(context.Background(), request)
	if err != nil || result.Outcome != CheckContainerPass || !strings.Contains(result.Output, "gyp info ok") {
		t.Fatalf("native compile failed: %+v %v", result, err)
	}
}

func TestProjectDownloadExhaustionSelectsSourceBuild(t *testing.T) {
	if os.Getenv("CLEARDEV_DOWNLOAD_FAILURE_CHILD") != "1" {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "download unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProjectDownloadExhaustionSelectsSourceBuild$", "-test.v")
		command.Env = append(os.Environ(), "CLEARDEV_DOWNLOAD_FAILURE_CHILD=1", "HTTPS_PROXY="+server.URL, "https_proxy="+server.URL, "NO_PROXY=", "no_proxy=")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fallback subprocess: %v %s", err, output)
		}
		return
	}
	t.Setenv("AO_DATA_DIR", t.TempDir())
	request := projectDependencyContainerFixture(t, `if(process.env.npm_config_build_from_source!=='true')throw Error('fallback flag missing'); require('fs').writeFileSync('index.cjs','module.exports=42');`)
	if err := os.Chmod(request.SourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(request.SourceDir, ".cleardev")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "downloads.json"), `[{"package":"fixture-driver","packageVersion":"1.0.0","archiveUrl":"https://github.com/example/fixture/releases/download/v1.0.0/fixture.tar.gz","archiveSha256":"`+strings.Repeat("a", 64)+`"}]`)
	if err := sealDependencyTree(request.SourceDir); err != nil {
		t.Fatal(err)
	}
	usage, err := measureCheckTree(request.SourceDir, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	seedFreshFixtureArchive(t, request.SourceDir, request.DependencyDir)
	usage, err = measureCheckTree(request.SourceDir, productionCheckCapacityLimits().Source)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	prepared := preparedCheckEnvironment{public: ports.ClearDevCheckEnvironment{SourceBytes: usage.Bytes, SourceItems: usage.Items}}
	destination, err := prepareProjectResultDependencies(context.Background(), prepared, request.SourceDir, root, *request.ProjectRuntime)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "fixture-driver", "index.cjs"))
	if err != nil || string(data) != "module.exports=42" {
		t.Fatalf("fallback not installed: %s %v", data, err)
	}
	log, err := os.ReadFile(filepath.Join(root, "dependency-download.log"))
	if err != nil || strings.Count(string(log), "attempt=") != 3 || !strings.Contains(string(log), "compiling from dependency sources") {
		t.Fatalf("missing bounded fallback evidence: %s %v", log, err)
	}
}
