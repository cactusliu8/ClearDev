package cleardevlocal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// This fixture is authored test code, not a model-generated product or a human
// approval. Its Git worktrees, trusted freeze and opt-in Docker checks are real.
func newProjectFreezeFixture(t *testing.T, existing bool) (*Runner, ports.ClearDevMailFreezeRequest) {
	t.Helper()
	baselineGateData(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "config", "user.name", "Project execution test")
	runGit(t, repo, "config", "user.email", "project-test@localhost")
	origin := "EMPTY"
	if existing {
		origin = "EXISTING"
		for _, directory := range []string{"app", "checks", "config"} {
			if err := os.MkdirAll(filepath.Join(repo, directory), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeTestFile(t, filepath.Join(repo, "app/store.cjs"), "exports.list = () => [];\n")
		writeTestFile(t, filepath.Join(repo, "checks/store.test.cjs"), "const test=require('node:test'); const assert=require('node:assert/strict'); test('original empty store',()=>assert.deepEqual(require('../app/store.cjs').list(),[]));\n")
		writeTestFile(t, filepath.Join(repo, "config/application.json"), "{\"name\":\"Notes\"}\n")
		runGit(t, repo, "add", ".")
	}
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "explicit project fixture baseline")
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	managed := t.TempDir()
	workspace := filepath.Join(managed, "builder")
	branch := "cleardev-complex-builder-project-test"
	runGit(t, repo, "worktree", "add", "-b", branch, workspace, base)
	runtime := core.DefaultProjectRuntimeV1()
	runtime.PrepareArgv = []string{"node", "migrations/prepare.cjs"}
	runtime.HealthPath = "/healthz"
	basis := core.ProjectExecutionBasis{
		WritePaths:      []string{"app/**", "checks/**", "migrations/**", "config/**", "package.json", "package-lock.json"},
		DependencyNeeds: []string{"Node built-in SQLite; no downloaded package is needed."},
		Checks: []core.ProjectCheckSpec{
			{ID: "notes-behavior", Argv: []string{"node", "--test", "checks/store.test.cjs"}, TimeoutSeconds: 30, MainPaths: []string{"app/**", "migrations/**", "config/**", "package.json", "package-lock.json"}},
			{ID: "failure-boundary", Argv: []string{"node", "checks/failure.cjs"}, TimeoutSeconds: 10, MainPaths: []string{"checks/**"}},
			{ID: "timeout-boundary", Argv: []string{"node", "checks/timeout.cjs"}, TimeoutSeconds: 1, MainPaths: []string{"checks/**"}},
		},
		// The description deliberately carries "<", ">" and "&": the contract
		// then has two different JSON spellings, so this fixture proves the
		// prepared source answers with the one identity the preview compares.
		Launch:  core.ProjectLaunch{Argv: []string{"node", "app/server.cjs"}, WorkingDirectory: ".", Description: "Run the local note service for <n> notes & retries."},
		Runtime: &runtime,
	}
	contract, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: "fixture-admission", PlanID: "fixture-plan", PlanSHA256: strings.Repeat("a", 64),
		RequirementSHA256: strings.Repeat("b", 64), BaseCommitSHA: base,
	}, core.ProductStage{
		ID: "fixture-stage", ProductID: "fixture-product", DiscussionID: "fixture-discussion", DevelopmentRequirementID: "fixture-requirement",
		DefinitionSHA256: strings.Repeat("d", 64), BaseCommitSHA: base,
		Selection: &core.ProductSelection{AOProjectID: "fixture-project", RepositoryPath: repo, BaseCommitSHA: base,
			SourceDiscussionID: "fixture-choice", Reason: "Explicit adapter test project; not a product approval.",
			Option: core.ProductOption{Key: strings.ToLower(origin), Title: "Notes test fixture", Origin: origin,
				Description: "Exercise project execution and retained data.", Tradeoffs: []string{"Small deterministic test application."}}},
		Definition: core.ProductStageDefinition{ExecutionBasis: &basis},
	}, "fixture-execution", "fixture-version")
	if err != nil {
		t.Fatal(err)
	}
	return NewWithRoot(managed), ports.ClearDevMailFreezeRequest{
		RunID: "fixture-dispatch", RepoPath: repo, WorkspacePath: workspace, Branch: branch, BaseSHA: base, ParentSHA: base,
		WritePaths: append([]string(nil), basis.WritePaths...), ForbiddenPaths: []string{".git/**"}, ProjectExecution: &contract,
	}
}

func writeProjectSQLiteFixture(t *testing.T, workspace string) {
	t.Helper()
	for _, directory := range []string{"app", "checks", "migrations", "config"} {
		if err := os.MkdirAll(filepath.Join(workspace, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(workspace, "app/store.cjs"), `const { DatabaseSync } = require('node:sqlite');
const { mkdirSync } = require('node:fs');
const { join } = require('node:path');
exports.open = function open() {
  const directory = process.env.CLEARDEV_DATA_DIR;
  if (!directory) throw new Error('CLEARDEV_DATA_DIR is required');
  mkdirSync(directory, { recursive: true });
  const db = new DatabaseSync(join(directory, 'notes.db'));
  db.exec('CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)');
  return {
    add(body) { if (typeof body !== 'string' || !body.trim()) throw new Error('body required'); return db.prepare('INSERT INTO notes(body) VALUES (?)').run(body); },
    list() { return db.prepare('SELECT id, body FROM notes ORDER BY id').all().map(row => ({ id: row.id, body: row.body })); },
    close() { db.close(); }
  };
};
`)
	writeTestFile(t, filepath.Join(workspace, "migrations/prepare.cjs"), "const store=require('../app/store.cjs').open(); store.close(); console.log('notes schema prepared without deleting existing data');\n")
	writeTestFile(t, filepath.Join(workspace, "checks/store.test.cjs"), `const test = require('node:test');
const assert = require('node:assert/strict');
const { open } = require('../app/store.cjs');
test('migration, insertion and reopening retain the same note', () => {
  assert.equal(require('../config/application.json').schemaVersion, 1);
  assert.equal(process.env.NODE_ENV, 'test');
  assert.equal(process.env.CLEARDEV_DATA_DIR, '/tmp/cleardev-project-data');
  let store = open();
  assert.deepEqual(store.list(), []);
  assert.throws(() => store.add(' '), /body required/);
  store.add('retained note');
  store.close();
  store = open();
  assert.deepEqual(store.list(), [{ id: 1, body: 'retained note' }]);
  store.close();
});
`)
	writeTestFile(t, filepath.Join(workspace, "checks/failure.cjs"), "console.error('deliberate failed assertion boundary'); process.exitCode=3;\n")
	writeTestFile(t, filepath.Join(workspace, "checks/timeout.cjs"), "setInterval(()=>{},1000);\n")
	writeTestFile(t, filepath.Join(workspace, "config/application.json"), "{\"name\":\"Persistent notes\",\"schemaVersion\":1}\n")
}

func TestProjectTrustedFreezeEmptyAndExistingChanges(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "EMPTY", true: "EXISTING"}[existing], func(t *testing.T) {
			runner, request := newProjectFreezeFixture(t, existing)
			writeProjectSQLiteFixture(t, request.WorkspacePath)
			// Changing the original test is intentional and accompanied by a
			// stronger behavior check. The old mail append-only rule must not leak.
			frozen, err := runner.FreezeMailCandidate(context.Background(), request)
			if err != nil || frozen.BaseSHA != request.BaseSHA || frozen.CandidateSHA == request.BaseSHA {
				t.Fatalf("project freeze: %+v %v", frozen, err)
			}
			if head := strings.TrimSpace(runGit(t, request.RepoPath, "rev-parse", "main")); head != request.BaseSHA {
				t.Fatal("project freeze moved the user's baseline branch")
			}
			if status := runGit(t, request.WorkspacePath, "status", "--porcelain", "--untracked-files=all"); status != "" {
				t.Fatalf("trusted freeze left a dirty worktree: %s", status)
			}
			replayed, err := NewWithRoot(runner.managedRoot).FreezeMailCandidate(context.Background(), request)
			if err != nil || !reflect.DeepEqual(replayed, frozen) {
				t.Fatalf("freeze replay: %+v %v", replayed, err)
			}
			request.ProjectExecution.RequestID = "different-admission"
			if _, err := runner.FreezeMailCandidate(context.Background(), request); err == nil {
				t.Fatal("changed admission reused a frozen candidate")
			}
		})
	}
}

func TestProjectTrustedFreezeRejectsUnconfirmedScope(t *testing.T) {
	for _, name := range []string{"outside.txt", "app/linked.cjs"} {
		t.Run(name, func(t *testing.T) {
			runner, request := newProjectFreezeFixture(t, false)
			writeProjectSQLiteFixture(t, request.WorkspacePath)
			if name == "app/linked.cjs" {
				if err := os.Symlink(filepath.Join(request.RepoPath, ".git/config"), filepath.Join(request.WorkspacePath, name)); err != nil {
					t.Fatal(err)
				}
			} else {
				writeTestFile(t, filepath.Join(request.WorkspacePath, name), "not authorized\n")
			}
			if _, err := runner.FreezeMailCandidate(context.Background(), request); err == nil {
				t.Fatal("unconfirmed or redirected project write was frozen")
			}
			if head := strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-parse", "HEAD")); head != request.BaseSHA {
				t.Fatal("failed freeze published a candidate")
			}
		})
	}
}

func TestProjectRuntimeRealSQLiteCheckAndFailureBoundaries(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	runner, freeze := newProjectFreezeFixture(t, false)
	ctx := context.Background()
	// Real infrastructure preflight accepts the genuinely empty Git baseline.
	if err := runner.PrepareProjectExecution(ctx, *freeze.ProjectExecution); err != nil {
		t.Fatal(err)
	}
	writeProjectSQLiteFixture(t, freeze.WorkspacePath)
	frozen, err := runner.FreezeMailCandidate(ctx, freeze)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range freeze.ProjectExecution.Basis.Checks {
		t.Run(spec.ID, func(t *testing.T) {
			request := ports.ClearDevCheckRequest{RunID: "real-project-" + spec.ID, WorkspacePath: freeze.WorkspacePath,
				CandidateSHA: frozen.CandidateSHA, Image: core.StandardCandidateCheckImage, Argv: spec.Argv,
				Timeout: time.Duration(spec.TimeoutSeconds) * time.Second, MemoryBytes: 512 * 1024 * 1024,
				PidsLimit: 64, OutputLimit: 64 * 1024, ProjectExecution: freeze.ProjectExecution}
			result, err := runner.RunCandidateCheck(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			expected := ports.ClearDevCheckFail
			switch spec.ID {
			case "notes-behavior":
				expected = ports.ClearDevCheckPass
			case "timeout-boundary":
				expected = ports.ClearDevCheckTimedOut
			}
			if result.DependencyCacheKey != "" || result.DependencyEnvironmentID != "" || result.DependencyTreeSHA256 != "" {
				t.Fatal("fresh project retained an installed-environment binding")
			}
			if result.Image != projectInstallImage {
				t.Fatal("actual installer/check image not reported")
			}
			contractDigest, _ := projectCheckContractDigest(freeze.ProjectExecution)
			if result.Outcome != expected || result.CandidateSHA != frozen.CandidateSHA || result.ProjectExecutionSHA256 != contractDigest ||
				result.CheckEnvironmentID == "" || result.SourceManifestID == "" || result.SourceRootTreeOID == "" || result.OutputSHA256 == "" || result.ImageID == "" ||
				(spec.ID == "timeout-boundary" && !result.TimedOut) {
				t.Fatalf("real project check: %+v", result)
			}
			t.Logf("candidate=%s check=%s outcome=%s exit=%d timeout=%v node=%s environment=%s output=%s", result.CandidateSHA, spec.ID, result.Outcome, result.ExitCode, result.TimedOut, result.NodeVersion, result.CheckEnvironmentID, result.OutputSummary)
			replayed, err := NewWithRoot(runner.managedRoot).RunCandidateCheck(ctx, request)
			if err != nil || !reflect.DeepEqual(replayed, result) {
				t.Fatalf("check replay changed the exact result: %+v %v", replayed, err)
			}
			encoded, _ := json.Marshal(request.ProjectExecution)
			var changed core.ProjectExecutionContract
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			changed.RequestID = "changed-request"
			request.ProjectExecution = &changed
			if _, err := runner.RunCandidateCheck(ctx, request); err == nil {
				t.Fatal("changed project contract reused a check result")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(freeze.WorkspacePath, "notes.db")); !os.IsNotExist(err) {
		t.Fatal("isolated project checks wrote their database into the Builder source")
	}
	if status := runGit(t, freeze.WorkspacePath, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("project checks modified the frozen candidate: %s", status)
	}
}

func TestProjectCandidateCheckUsesNonemptyDependencyLock(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	runner, freeze := newProjectFreezeFixture(t, false)
	writeProjectSQLiteFixture(t, freeze.WorkspacePath)
	writeTestFile(t, filepath.Join(freeze.WorkspacePath, "package.json"), `{"name":"notes-fixture","version":"1.0.0","private":true,"dependencies":{"is-number":"7.0.0"}}`+"\n")
	writeTestFile(t, filepath.Join(freeze.WorkspacePath, "package-lock.json"), `{"name":"notes-fixture","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"notes-fixture","version":"1.0.0","dependencies":{"is-number":"7.0.0"}},"node_modules/is-number":{"version":"7.0.0","resolved":"https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz","integrity":"sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng=="}}}`+"\n")
	path := filepath.Join(freeze.WorkspacePath, "checks/store.test.cjs")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, "const assertDependency=require('node:assert/strict'); assertDependency.equal(require('is-number')(42), true);\n"+string(original))
	frozen, err := runner.FreezeMailCandidate(context.Background(), freeze)
	if err != nil {
		t.Fatal(err)
	}
	check := freeze.ProjectExecution.Basis.Checks[0]
	result, err := runner.RunCandidateCheck(context.Background(), ports.ClearDevCheckRequest{
		RunID: "project-dependency-lock", WorkspacePath: freeze.WorkspacePath, CandidateSHA: frozen.CandidateSHA,
		Image: core.StandardCandidateCheckImage, Argv: check.Argv, Timeout: time.Duration(check.TimeoutSeconds) * time.Second,
		MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, ProjectExecution: freeze.ProjectExecution,
	})
	if err != nil || result.Outcome != ports.ClearDevCheckPass || result.PackageLockSHA256 == "" || result.DependencyTreeSHA256 != "" || result.DependencyCacheKey != "" || result.Image != core.ProjectCandidateCheckImage {
		t.Fatalf("project's nonempty dependency lock was not checked: outcome=%s lock=%s tree=%s err=%v output=%s", result.Outcome, result.PackageLockSHA256, result.DependencyTreeSHA256, err, result.OutputSummary)
	}
}

func TestProjectNPMCheckWithoutDependenciesOrLock(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	runner, freeze := newProjectFreezeFixture(t, false)
	writeProjectSQLiteFixture(t, freeze.WorkspacePath)
	writeTestFile(t, filepath.Join(freeze.WorkspacePath, "package.json"), `{"name":"notes-no-dependencies","version":"1.0.0","private":true,"scripts":{"test":"node --test checks/store.test.cjs"}}`+"\n")
	check := core.ProjectCheckSpec{ID: "npm-tests", Argv: []string{"npm", "test"}, TimeoutSeconds: 30, MainPaths: []string{"app/**", "checks/**", "migrations/**", "config/**", "package.json", "package-lock.json"}}
	freeze.ProjectExecution.Basis.Checks = []core.ProjectCheckSpec{check}
	frozen, err := runner.FreezeMailCandidate(context.Background(), freeze)
	if err != nil {
		t.Fatal(err)
	}
	request := ports.ClearDevCheckRequest{RunID: "project-npm-no-dependencies", WorkspacePath: freeze.WorkspacePath,
		CandidateSHA: frozen.CandidateSHA, Image: core.StandardCandidateCheckImage, Argv: check.Argv,
		Timeout: 30 * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 64 * 1024, ProjectExecution: freeze.ProjectExecution}
	result, err := runner.RunCandidateCheck(context.Background(), request)
	if err != nil || result.Outcome != ports.ClearDevCheckPass || result.PackageJSONSHA256 == "" || result.PackageLockSHA256 != "" || result.DependencyEnvironmentID != "" {
		t.Fatalf("no-dependency npm check: %+v %v", result, err)
	}
	replayed, err := NewWithRoot(runner.managedRoot).RunCandidateCheck(context.Background(), request)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("replayed no-dependency check changed: %+v %v", replayed, err)
	}
	source, err := runner.PrepareProjectResult(context.Background(), freeze.WorkspacePath, frozen.CandidateSHA, *freeze.ProjectExecution)
	if err != nil || source.Environment.PackageJSONSHA256 != result.PackageJSONSHA256 || source.Environment.DependencyEnvironmentID != "" {
		t.Fatalf("no-dependency result preparation: %+v %v", source.Environment, err)
	}
	if err := source.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPackageDeclaresDependenciesRequiresLock(t *testing.T) {
	for _, example := range []string{
		`{"scripts":{"test":"node --test"}}`,
		`{"dependencies":{},"devDependencies":[],"workspaces":[]}`,
		`{"dependencies": { }, "workspaces": [ ]}`,
	} {
		if packageDeclaresDependencies([]byte(example)) {
			t.Fatalf("empty dependency declaration required a lock: %s", example)
		}
	}
	for _, example := range []string{
		`{"dependencies":{"is-number":"7.0.0"}}`,
		`{"devDependencies":{"vitest":"*"}}`,
		`{"workspaces":["packages/*"]}`,
		`{"bundledDependencies":["local"]}`,
		`[]`,
	} {
		if !packageDeclaresDependencies([]byte(example)) {
			t.Fatalf("dependency declaration bypassed a lock: %s", example)
		}
	}
}
