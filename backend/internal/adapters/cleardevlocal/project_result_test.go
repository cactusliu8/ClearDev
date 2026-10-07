package cleardevlocal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
)

// Real Git, Docker preparation, Node HTTP processes and SQLite files. The app
// and contract are authored fixtures; this is not model or desktop acceptance.
func TestProjectResultRealRuntimeRetainsDataAcrossStages(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	runner, first := newProjectFreezeFixture(t, false)
	writeProjectSQLiteFixture(t, first.WorkspacePath)
	writeProjectServer(t, first.WorkspacePath, false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	frozen1, err := runner.FreezeMailCandidate(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	manager := previewserver.New(nil, os.Getenv("AO_DATA_DIR"))
	defer manager.Close()
	t.Setenv("AO_PREVIEW_PRIVATE_TEST", "must-not-reach-project")
	t.Setenv("CODEX_HOME", "/must-not-be-inherited")
	preparations := 0
	var sources []string
	var preparedSource ports.ClearDevProjectResultSource
	start := func(session string, request ports.ClearDevMailFreezeRequest, candidate string) (previewserver.Status, error) {
		return manager.StartProject(ctx, domain.SessionID(session), request.WorkspacePath, candidate, *request.ProjectExecution,
			func(ctx context.Context) (ports.ClearDevProjectResultSource, error) {
				preparations++
				source, err := runner.PrepareProjectResult(ctx, request.WorkspacePath, candidate, *request.ProjectExecution)
				if err == nil {
					sources = append(sources, source.WorkspacePath)
					preparedSource = source
				}
				return source, err
			})
	}
	stop := func(session string) {
		t.Helper()
		status, err := manager.Stop(ctx, domain.SessionID(session))
		if err != nil || status.State != previewserver.StateStopped {
			t.Fatalf("stop project: %+v %v", status, err)
		}
	}
	ready1, err := start("project-stage-1", first, frozen1.CandidateSHA)
	if err != nil || ready1.State != previewserver.StateReady {
		t.Fatalf("first real project startup: %+v %v", ready1, err)
	}
	firstPreparationCount := preparations
	canonical, err := core.ProjectExecutionContractDigest(*first.ProjectExecution)
	if err != nil {
		t.Fatal(err)
	}
	if core.LegacyProjectExecutionContractDigest(*first.ProjectExecution) == canonical {
		t.Fatal("the fixture contract must contain HTML-escapable text so both spellings differ")
	}
	if preparedSource.ContractSHA256 != canonical || preparedSource.Environment.ProjectExecutionSHA256 != canonical {
		t.Fatalf("the prepared source must answer with the canonical contract identity: %q %q",
			preparedSource.ContractSHA256, preparedSource.Environment.ProjectExecutionSHA256)
	}
	repeated, err := start("project-stage-1", first, frozen1.CandidateSHA)
	if err != nil || repeated.Port != ready1.Port || preparations != firstPreparationCount {
		t.Fatal("duplicate open replaced the application or repeated data preparation")
	}
	projectHTTP(t, ready1.URL, "POST", "notes", `{"body":"Keep this note across versions"}`, http.StatusCreated)
	if got := projectHTTP(t, ready1.URL, "GET", "notes", "", 200); !strings.Contains(got, "Keep this note across versions") {
		t.Fatalf("first functional note missing: %s", got)
	}
	stop("project-stage-1")
	if _, err := os.Stat(sources[0]); !os.IsNotExist(err) {
		t.Fatal("stopping did not release the disposable source through the shared temporary ledger")
	}
	reopened, err := start("project-stage-1", first, frozen1.CandidateSHA)
	if err != nil {
		t.Fatal(err)
	}
	if got := projectHTTP(t, reopened.URL, "GET", "notes", "", 200); !strings.Contains(got, "Keep this note across versions") {
		t.Fatal("stop/reopen lost existing project data")
	}

	second := nextProjectFreezeFixture(t, runner, first, frozen1.CandidateSHA, "2")
	writeProjectUpgrade(t, second.WorkspacePath)
	frozen2, err := runner.FreezeMailCandidate(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := start("project-stage-2", second, frozen2.CandidateSHA); err == nil {
		t.Fatal("a second version concurrently acquired the first version's live database")
	}
	stop("project-stage-1")
	ready2, err := start("project-stage-2", second, frozen2.CandidateSHA)
	if err != nil || ready2.State != previewserver.StateReady {
		t.Fatalf("second stage startup/migration: %+v %v", ready2, err)
	}
	before := projectHTTP(t, ready2.URL, "GET", "notes", "", 200)
	if !strings.Contains(before, "Keep this note across versions") || !strings.Contains(before, `"archived":0`) {
		t.Fatalf("schema upgrade lost old data or omitted the new column: %s", before)
	}
	projectHTTP(t, ready2.URL, "PATCH", "notes/1", `{"archived":true}`, 200)
	after := projectHTTP(t, ready2.URL, "GET", "notes", "", 200)
	if !strings.Contains(after, `"archived":1`) {
		t.Fatalf("new stage feature did not work: %s", after)
	}
	stop("project-stage-2")

	// The next candidate deliberately destroys only its copied upgrade data,
	// then exits before health. The selected v2 data must remain unchanged.
	third := nextProjectFreezeFixture(t, runner, second, frozen2.CandidateSHA, "3")
	writeTestFile(t, filepath.Join(third.WorkspacePath, "migrations/prepare.cjs"), "const {DatabaseSync}=require('node:sqlite'); const {join}=require('node:path'); const db=new DatabaseSync(join(process.env.CLEARDEV_DATA_DIR,'notes.db')); db.exec('DELETE FROM notes'); db.close(); throw new Error('deliberate failed migration in a disposable data copy');\n")
	frozen3, err := runner.FreezeMailCandidate(ctx, third)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := start("project-stage-3", third, frozen3.CandidateSHA)
	if err == nil || failed.State == previewserver.StateReady {
		t.Fatal("failed migration was presented as a ready delivered application")
	}
	ready2, err = start("project-stage-2", second, frozen2.CandidateSHA)
	if err != nil {
		t.Fatal(err)
	}
	retained := projectHTTP(t, ready2.URL, "GET", "notes", "", 200)
	if !strings.Contains(retained, "Keep this note across versions") || !strings.Contains(retained, `"archived":1`) {
		t.Fatalf("failed next-version migration modified the previously selected data: %s", retained)
	}
	stop("project-stage-2")
	if _, err := start("project-stage-1", first, frozen1.CandidateSHA); err == nil {
		t.Fatal("an older delivery silently downgraded newer user data")
	}
	for _, request := range []ports.ClearDevMailFreezeRequest{first, second, third} {
		if status := runGit(t, request.WorkspacePath, "status", "--porcelain", "--untracked-files=all"); status != "" {
			t.Fatalf("preview/build/migration modified a frozen worktree: %s", status)
		}
	}
	if main := strings.TrimSpace(runGit(t, first.RepoPath, "rev-parse", "main")); main != first.BaseSHA {
		t.Fatal("opening/continuing an application moved main")
	}
	t.Logf("real runtime stage1=%s stage2=%s failed-stage3=%s; original note and new archive flag survived stop/reopen/upgrade/failure; main=%s", frozen1.CandidateSHA, frozen2.CandidateSHA, frozen3.CandidateSHA, first.BaseSHA)
}

func TestCopyProjectDependencyTreePreservesContainedLinksAndRejectsEscapes(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, ".bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "pkg", "tool.js"), []byte("console.log('ok')\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "pkg", "tool.js"), filepath.Join(source, ".bin", "tool")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "node_modules")
	if err := copyProjectDependencyTree(ctx, source, destination); err != nil {
		t.Fatalf("copy contained dependency tree: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(destination, ".bin", "tool")); err != nil || target != filepath.Join("..", "pkg", "tool.js") {
		t.Fatalf("copied npm link target=%q err=%v", target, err)
	}
	if body, err := os.ReadFile(filepath.Join(destination, "pkg", "tool.js")); err != nil || string(body) != "console.log('ok')\n" {
		t.Fatalf("copied dependency body=%q err=%v", body, err)
	}

	escaping := t.TempDir()
	if err := os.MkdirAll(filepath.Join(escaping, ".bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(escaping, ".bin", "escape")); err != nil {
		t.Fatal(err)
	}
	if err := copyProjectDependencyTree(ctx, escaping, filepath.Join(t.TempDir(), "node_modules")); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping dependency link error=%v", err)
	}
}

func nextProjectFreezeFixture(t *testing.T, runner *Runner, previous ports.ClearDevMailFreezeRequest, base, ordinal string) ports.ClearDevMailFreezeRequest {
	t.Helper()
	prior := previous.ProjectExecution
	selection := prior.Selection
	selection.BaseCommitSHA = base
	selection.Reason = "Explicit fixture selection of the previous exact delivered candidate."
	selection.SourceDiscussionID = "fixture-delivery-choice-" + ordinal
	contract, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: "fixture-admission-" + ordinal, PlanID: "fixture-plan-" + ordinal, PlanSHA256: strings.Repeat(ordinal, 64),
		RequirementSHA256: strings.Repeat("b", 64), BaseCommitSHA: base,
	}, core.ProductStage{
		ID: "fixture-stage-" + ordinal, ProductID: prior.ProductID, DiscussionID: "fixture-discussion-" + ordinal,
		DevelopmentRequirementID: "fixture-requirement-" + ordinal, DefinitionSHA256: strings.Repeat("d", 64),
		BaseCommitSHA: base, Selection: &selection, Definition: core.ProductStageDefinition{ExecutionBasis: &prior.Basis},
	}, "fixture-execution-"+ordinal, "fixture-version-"+ordinal)
	if err != nil {
		t.Fatal(err)
	}
	branch := "cleardev-complex-builder-project-test-" + ordinal
	workspace := filepath.Join(runner.managedRoot, "builder-stage-"+ordinal)
	runGit(t, previous.RepoPath, "worktree", "add", "-b", branch, workspace, base)
	return ports.ClearDevMailFreezeRequest{RunID: "fixture-dispatch-" + ordinal, RepoPath: previous.RepoPath,
		WorkspacePath: workspace, Branch: branch, BaseSHA: base, ParentSHA: base,
		WritePaths: contract.Basis.WritePaths, ForbiddenPaths: []string{".git/**"}, ProjectExecution: &contract}
}

func writeProjectServer(t *testing.T, workspace string, upgraded bool) {
	t.Helper()
	patch := ""
	if upgraded {
		patch = `if (req.method === 'PATCH' && req.url === '/notes/1') { store.archive(1, JSON.parse(body).archived === true); return reply(200, {ok:true}); }`
	}
	writeTestFile(t, filepath.Join(workspace, "app/server.cjs"), fmt.Sprintf(`const http = require('node:http');
const { open } = require('./store.cjs');
if (process.env.AO_PREVIEW_PRIVATE_TEST || process.env.CODEX_HOME) throw new Error('control environment leaked');
const store = open();
const server = http.createServer((req,res) => {
  let body='';
  req.on('data', part => { body += part; if (body.length > 65536) req.destroy(); });
  req.on('end', () => {
    const reply = (status,value) => { res.writeHead(status, {'content-type':'application/json'}); res.end(JSON.stringify(value)); };
    try {
      if (req.method==='GET' && req.url==='/healthz') return reply(200,{application:'notes',status:'ok'});
      if (req.method==='GET' && req.url==='/') return reply(200,{application:'notes'});
      if (req.method==='GET' && req.url==='/notes') return reply(200,store.list());
      if (req.method==='POST' && req.url==='/notes') { store.add(JSON.parse(body).body); return reply(201,{ok:true}); }
      %s
      reply(404,{error:'not found'});
    } catch (error) { reply(400,{error:error.message}); }
  });
});
server.listen(Number(process.env.PORT), process.env.HOST);
process.once('SIGTERM', () => server.close(() => { store.close(); process.exit(0); }));
`, patch))
}

func writeProjectUpgrade(t *testing.T, workspace string) {
	t.Helper()
	writeTestFile(t, filepath.Join(workspace, "app/store.cjs"), `const { DatabaseSync } = require('node:sqlite');
const { mkdirSync } = require('node:fs');
const { join } = require('node:path');
exports.open = function open() {
  const directory = process.env.CLEARDEV_DATA_DIR;
  if (!directory) throw new Error('CLEARDEV_DATA_DIR required');
  mkdirSync(directory,{recursive:true});
  const db = new DatabaseSync(join(directory,'notes.db'));
  db.exec('CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)');
  if (!db.prepare('PRAGMA table_info(notes)').all().some(row => row.name==='archived')) db.exec('ALTER TABLE notes ADD COLUMN archived INTEGER NOT NULL DEFAULT 0');
  return {
    add(body) { if (typeof body!=='string' || !body.trim()) throw new Error('body required'); return db.prepare('INSERT INTO notes(body) VALUES (?)').run(body); },
    archive(id, archived) { return db.prepare('UPDATE notes SET archived=? WHERE id=?').run(archived?1:0,id); },
    list() { return db.prepare('SELECT id,body,archived FROM notes ORDER BY id').all().map(row=>({id:row.id,body:row.body,archived:row.archived})); },
    close() { db.close(); }
  };
};
`)
	writeProjectServer(t, workspace, true)
	writeTestFile(t, filepath.Join(workspace, "checks/store.test.cjs"), `const test=require('node:test'); const assert=require('node:assert/strict'); const {open}=require('../app/store.cjs');
test('create, archive and reopen preserve functional notes',()=>{let store=open(); assert.deepEqual(store.list(),[]); assert.throws(()=>store.add(' ')); store.add('retained'); store.archive(1,true); store.close(); store=open(); assert.deepEqual(store.list(),[{id:1,body:'retained',archived:1}]); store.close();});
`)
}

func projectHTTP(t *testing.T, base, method, path, body string, status int) string {
	t.Helper()
	request, err := http.NewRequest(method, strings.TrimSuffix(base, "/")+"/"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil || response.StatusCode != status || !json.Valid(raw) {
		t.Fatalf("%s %s: status=%d response=%s err=%v", method, path, response.StatusCode, raw, err)
	}
	return string(raw)
}
