package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Existing files may contain appended new tests without belonging to npm test's
// fixed file list. Preserving bytes is necessary, but does not prove execution.
func TestMailScopeSchedulesAppendedExecutableTests(t *testing.T) {
	baselineGateData(t)
	repo := baselineGateRepo(t, false)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	paths := []string{"test/backend.test.js", "test/start-app.js", "test/template.test.js"}
	for _, name := range paths {
		original, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), append(original, []byte("\n// appended test content\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	candidate := mailCommit(t, repo)
	proof, err := New().CheckMailCandidateScope(context.Background(), ports.ClearDevDeliveryRequest{RunID: "appended-scope", WorkspacePath: repo, BaseSHA: base, CandidateSHA: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(proof.ExtraTestPaths, paths) {
		t.Fatalf("appended executable tests are not scheduled: got %q, want %q", proof.ExtraTestPaths, paths)
	}
}

func TestMailDeliveryAppendedUnlistedFailureMustExecute(t *testing.T) {
	requireProductionCheckPrerequisites(t, core.StandardCandidateCheckImage)
	baselineGateData(t)
	repo := baselineGateRepo(t, false)
	base := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	name := filepath.Join(repo, "test", "template.test.js")
	original, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	appended := "\ntest('appended test outside npm list', () => { throw Error('APPENDED_TEST_EXECUTED'); });\n"
	if err := os.WriteFile(name, append(original, []byte(appended)...), 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := mailCommit(t, repo)
	request := ports.ClearDevDeliveryRequest{RunID: "appended-failure", WorkspacePath: repo, BaseSHA: base, CandidateSHA: candidate}
	result, err := New().RunMailDeliveryCheck(context.Background(), request)
	if err != nil || result.Outcome != ports.ClearDevCheckFail || !strings.Contains(result.OutputSummary, "APPENDED_TEST_EXECUTED") {
		t.Fatalf("appended assertion was not executed: result=%+v err=%v", result, err)
	}
	state, err := durableRunStatePath(checkRunStateDirectory, request.RunID+":health", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("health ran after an appended test failed: %v", err)
	}
}
