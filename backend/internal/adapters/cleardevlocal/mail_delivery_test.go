package cleardevlocal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func mailCommit(t *testing.T, repo string) string {
	t.Helper()
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "-c", "commit.gpgsign=false", "commit", "-qm", "candidate")
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func TestMailScopeUsesRealTreesAndProtectsOldTestBytes(t *testing.T) {
	for _, mode := range []string{"append", "fixture", "edit", "delete", "rename", "mode", "package", "nested-package", "schema", "db-module", "symlink", "unexecuted-test", "dirty"} {
		t.Run(mode, func(t *testing.T) {
			baselineGateData(t)
			repo := baselineGateRepo(t, false)
			base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			testPath := filepath.Join(repo, "test", "backend.test.js")
			old, err := os.ReadFile(testPath)
			if err != nil {
				t.Fatal(err)
			}
			write := func(name string, data []byte) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "append", "dirty":
				write("test/backend.test.js", append(old, []byte("\n// preserved prefix\n")...))
			case "fixture":
				write("test/input.json", []byte(`{"emails":["a@example.com"]}`))
			case "edit":
				write("test/backend.test.js", []byte("// old assertions removed\n"))
			case "delete":
				if err := os.Remove(testPath); err != nil {
					t.Fatal(err)
				}
			case "rename":
				if err := os.Rename(testPath, testPath+".old"); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(testPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "package":
				write("package.json", []byte(`{"scripts":{"test":"true"}}`))
			case "nested-package":
				write("frontend/package.json", []byte(`{}`))
			case "schema":
				if err := os.Mkdir(filepath.Join(repo, "backend", "src", "schema"), 0o700); err != nil {
					t.Fatal(err)
				}
				write("backend/src/schema/add.sql", []byte("CREATE TABLE bypass(id);"))
			case "db-module":
				write("backend/src/db.ts", []byte("// changed persistence\n"))
			case "symlink":
				if err := os.Symlink("../package.json", filepath.Join(repo, "test", "linked.js")); err != nil {
					t.Fatal(err)
				}
			case "unexecuted-test":
				write("frontend/new.test.js", []byte("throw Error('not in approved test entry')\n"))
			}
			candidate := base
			if mode != "dirty" {
				candidate = mailCommit(t, repo)
			}
			proof, err := New().CheckMailCandidateScope(context.Background(), ports.ClearDevDeliveryRequest{RunID: "scope", WorkspacePath: repo, BaseSHA: base, CandidateSHA: candidate})
			if mode == "append" || mode == "fixture" {
				if err != nil || proof.OldTestCount != 8 {
					t.Fatalf("scope=%+v err=%v", proof, err)
				}
			} else if err == nil {
				t.Fatalf("%s bypassed scope protection", mode)
			}
		})
	}
}

func TestMailDeliveryRealChecksExtraTestsAndRestartEvidence(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	repo := baselineGateRepo(t, false)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "test", "added.test.js"), []byte("import test from 'node:test'; import assert from 'node:assert/strict'; test('new entry really runs',()=>assert.equal(2+2,4));\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := mailCommit(t, repo)
	request := ports.ClearDevDeliveryRequest{RunID: "delivery", WorkspacePath: repo, BaseSHA: base, CandidateSHA: candidate}
	first, err := New().RunMailDeliveryCheck(context.Background(), request)
	if err != nil || first.Outcome != ports.ClearDevCheckPass {
		t.Fatalf("result=%+v err=%v", first, err)
	}
	if err := core.ValidateMailDeliveryProof(first.OutputSummary, base, candidate, request.RunID, first.ImageID); err != nil {
		t.Fatal(err)
	}
	var proof core.MailDeliveryProof
	if err := json.Unmarshal([]byte(first.OutputSummary), &proof); err != nil {
		t.Fatal(err)
	}
	if proof.ExtraTests == nil || !reflect.DeepEqual(proof.Scope.ExtraTestPaths, []string{"test/added.test.js"}) {
		t.Fatal("new test entry was not executed")
	}
	t.Logf("same candidate=%s: full npm test, added.test.js and trusted health passed; scope=%s", candidate, proof.Scope.SourceManifestID)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 93\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	second, err := New().RunMailDeliveryCheck(context.Background(), request)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("replay changed evidence or repeated Docker: %v", err)
	}
	if status := strings.TrimSpace(runGit(t, repo, "status", "--porcelain", "--untracked-files=all")); status != "" {
		t.Fatalf("candidate dirty: %s", status)
	}
	if err := os.WriteFile(filepath.Join(repo, "frontend", "new.css"), []byte("/* another candidate */\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.CandidateSHA = mailCommit(t, repo)
	if _, err := New().RunMailDeliveryCheck(context.Background(), request); err == nil {
		t.Fatal("new SHA reused old delivery evidence")
	}
}

func TestMailDeliveryNewFailingTestCannotHideBehindPassingNPMTest(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	repo := baselineGateRepo(t, false)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "test", "unlisted.test.js"), []byte("import test from 'node:test'; test('must run',()=>{throw Error('NEW_TEST_EXECUTED');});\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := mailCommit(t, repo)
	result, err := New().RunMailDeliveryCheck(context.Background(), ports.ClearDevDeliveryRequest{RunID: "failing-extra", WorkspacePath: repo, BaseSHA: base, CandidateSHA: candidate})
	if err != nil || result.Outcome != ports.ClearDevCheckFail || !strings.Contains(result.OutputSummary, "NEW_TEST_EXECUTED") {
		t.Fatalf("extra test was ignored: %+v %v", result, err)
	}
	state, err := durableRunStatePath(checkRunStateDirectory, "failing-extra:health", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("health ran after failing extra test: %v", err)
	}
}
