package cleardevlocal

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// This runs a deterministic test process in the actual Codex sandbox, not a
// real model and not a formal Demo. Git metadata is outside /tmp and outside
// the writable workspace, exactly as in the preserved failed demonstration.
func TestTrustedMailFreezeActualCodexSandboxHandoff(t *testing.T) {
	if os.Getenv("CLEARDEV_REQUIRE_CODEX_HANDOFF") != "1" {
		t.Skip("opt-in actual Codex sandbox and Docker handoff")
	}
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(home, ".ao", "demo-sprint-nonformal")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "freeze-handoff-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			cleanupPreparedTree(root)
		} else {
			t.Log("preserved nonformal failure:", root)
		}
	})
	repo := filepath.Join(root, "repo")
	managed := filepath.Join(root, "worktrees")
	workspace := filepath.Join(managed, "builder")
	for _, p := range []string{repo, managed} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AO_DATA_DIR", filepath.Join(root, "data"))
	if err := core.CopyDemoBaseline(repo); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@localhost", "-c", "commit.gpgsign=false", "commit", "-qm", "nonformal healthy baseline")
	sha := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	branch := "cleardev-complex-builder-actual-sandbox"
	runGit(t, repo, "worktree", "add", "-b", branch, workspace, sha)
	program := `const fs=require('node:fs'),cp=require('node:child_process'),net=require('node:net');
fs.appendFileSync('frontend/app.js','\n// Explicit nonformal sandbox handoff probe.\n');
const git=cp.spawnSync('git',['add','frontend/app.js'],{encoding:'utf8'});
const readonly=git.status!==0 && /Read-only file system|Permission denied/.test(git.stderr);
if(!readonly){console.error('Git metadata was unexpectedly writable');process.exit(2);}
const server=net.createServer();server.on('error',error=>{if(!['EPERM','EACCES'].includes(error.code)){process.exitCode=3;return;}console.log(JSON.stringify({filesChanged:true,gitMetadataReadOnly:true,loopbackDenied:true,modelUsed:false}));});
server.listen(0,'127.0.0.1',()=>{server.close();console.error('Loopback unexpectedly allowed');process.exitCode=4;});`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "sandbox", "-C", workspace, "-P", "freeze_handoff_probe", "-c", `permissions.freeze_handoff_probe.extends=":workspace"`, "-c", `permissions.freeze_handoff_probe.network.enabled=false`, "--", "node", "-e", program)
	cmd.Dir = workspace
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("actual restricted sandbox probe failed: %v\n%s", err, output)
	}
	var proof struct {
		FilesChanged        bool `json:"filesChanged"`
		GitMetadataReadOnly bool `json:"gitMetadataReadOnly"`
		LoopbackDenied      bool `json:"loopbackDenied"`
		ModelUsed           bool `json:"modelUsed"`
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if json.Unmarshal([]byte(lines[len(lines)-1]), &proof) != nil || !proof.FilesChanged || !proof.GitMetadataReadOnly || !proof.LoopbackDenied || proof.ModelUsed {
		t.Fatalf("missing actual sandbox facts: %s", output)
	}
	runner := NewWithRoot(managed)
	request := ports.ClearDevMailFreezeRequest{RunID: "actual-sandbox-handoff", RepoPath: repo, WorkspacePath: workspace, Branch: branch, BaseSHA: sha, ParentSHA: sha, WritePaths: []string{"frontend/**"}}
	frozen, err := runner.FreezeMailCandidate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	check := ports.ClearDevDeliveryRequest{RunID: "actual-sandbox-delivery", WorkspacePath: workspace, BaseSHA: sha, CandidateSHA: frozen.CandidateSHA}
	result, err := runner.RunMailDeliveryCheck(context.Background(), check)
	if err != nil || result.Outcome != ports.ClearDevCheckPass || core.ValidateMailDeliveryProof(result.OutputSummary, sha, frozen.CandidateSHA, check.RunID, result.ImageID) != nil {
		t.Fatalf("restricted Builder -> trusted freeze -> real checks failed: %v outcome=%s", err, result.Outcome)
	}
	t.Logf("actual Codex sandbox denied Git metadata and loopback; trusted candidate %s passed full npm test and HTTP health in production isolation; no model, no formal demo", frozen.CandidateSHA)
}
