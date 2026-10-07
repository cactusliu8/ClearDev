package cli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevdemo"
)

func TestClearDevDemoRejectsCallerControlArguments(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := clearDevServer(t, 200, `{}`)
	writeRunFileFor(t, cfg, srv)

	for _, args := range [][]string{
		{"cleardev", "demo", "--mode", "PARALLEL"},
		{"cleardev", "demo", "PARALLEL"},
		{"cleardev", "demo", "--path", "/tmp/demo"},
	} {
		_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
		if err == nil {
			t.Fatalf("%v succeeded, want usage error", args)
		}
		if ExitCode(err) != 2 {
			t.Fatalf("%v exit=%d stderr=%s err=%v, want usage error 2", args, ExitCode(err), errOut, err)
		}
	}
	if _, _, _, count := capture.snapshot(); count != 0 {
		t.Fatalf("demo usage errors called the daemon %d times", count)
	}
}

func TestClearDevDemoVerifyChecksEvidenceOffline(t *testing.T) {
	dir := t.TempDir()
	writeCLIEvidencePack(t, dir)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "demo", "verify", dir)
	if err != nil {
		t.Fatalf("verify failed: %v\nstderr=%s", err, errOut)
	}
	if !jsonContains(out, "ok") || !strings.Contains(out, cleardevdemo.EvidencePackConsistencyScope) || !strings.Contains(out, `"schemaVersion": 2`) {
		t.Fatalf("verify output = %s", out)
	}

	forged := t.TempDir()
	if err := os.WriteFile(filepath.Join(forged, "evidence.json"), []byte(`{"schemaVersion":1,"requirementId":"x","mode":"PARALLEL","fixedBuilderCount":2,"progressPhase":"COMPLETED","integrationCommitSha":"0000000000000000000000000000000000000000","sample":{"accepted":2,"rejected":1,"duplicates":2}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "demo", "verify", forged); err == nil {
		t.Fatal("forged evidence was accepted")
	}

	_, _, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "cleardev", "demo", "verify")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("verify without dir err=%v, want usage error", err)
	}
}

func jsonContains(raw, key string) bool {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false
	}
	_, ok := payload[key]
	return ok
}

func TestClearDevDemoVerifyDoesNotNeedDaemon(t *testing.T) {
	if _, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return false }}, "cleardev", "demo", "verify", t.TempDir()); err == nil {
		t.Fatal("expected missing evidence to fail")
	} else if errors.Is(err, os.ErrNotExist) || ExitCode(err) == 1 {
		return
	}
}

func writeCLIEvidencePack(t *testing.T, dir string) {
	t.Helper()
	prd, err := core.FrozenDemoPRD()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(prd, "\n") {
		prd += "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "prd.md"), []byte(prd), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("c", 40)
	combo := strings.Repeat("d", 40)
	template := strings.Repeat("e", 40)
	hash := strings.Repeat("b", 64)
	raw, err := os.ReadFile(filepath.Join(dir, "prd.md"))
	if err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(raw))
	sample, _ := json.Marshal(cleardevdemo.FrozenSampleCounts)
	_ = os.WriteFile(filepath.Join(dir, "sample-api.json"), append(sample, '\n'), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "sample-page.json"), append(sample, '\n'), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "template-commit.txt"), []byte(template+"\n"), 0o600)
	reqBody := `{"requirement":"demo-mail-list"}`
	reqHash := fmt.Sprintf("%x", sha256.Sum256([]byte(reqBody)))
	planBody := fmt.Sprintf(`{"requirementVersionId":"ver","requirementVersionSha256":%q,"tasks":[{"key":"api"}]}`, reqHash)
	_ = os.WriteFile(filepath.Join(dir, "requirement.json"), []byte(reqBody), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "plan.json"), []byte(planBody), 0o600)
	planHash := fmt.Sprintf("%x", sha256.Sum256([]byte(planBody)))
	review := map[string]string{"id": "review", "planId": "plan", "planSha256": planHash, "verdict": "APPROVED"}
	reviewRaw, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "review.json"), append(reviewRaw, '\n'), 0o600)
	buildCandidate := strings.Repeat("f", 40)
	writeCLIJSONFile(t, filepath.Join(dir, "cleardev-build-source.json"), map[string]any{
		"schemaVersion": 1, "candidateCommit": buildCandidate, "sourceClean": true,
		"sourceScope": []string{"backend", "frontend"}, "builtAt": "2026-08-29T00:00:00Z",
	})
	writeCLIJSONFile(t, filepath.Join(dir, "desktop-startup.json"), map[string]any{
		"schemaVersion": 1, "stage": "DAEMON_READY", "lastSuccessfulStage": "DAEMON_READY", "events": []map[string]string{
			{"stage": "APP_READY", "timestamp": "2026-08-29T00:00:00.000Z"},
			{"stage": "WINDOW_STARTING", "timestamp": "2026-08-29T00:00:00.100Z"},
			{"stage": "WINDOW_READY", "timestamp": "2026-08-29T00:00:00.200Z"},
			{"stage": "DAEMON_STARTING", "timestamp": "2026-08-29T00:00:00.300Z"},
			{"stage": "DAEMON_STARTED", "timestamp": "2026-08-29T00:00:00.400Z"},
			{"stage": "DAEMON_READY", "timestamp": "2026-08-29T00:00:00.500Z"},
		},
	})
	_ = os.WriteFile(filepath.Join(dir, "visible-desktop.xwd"), []byte("visible desktop pixels"), 0o600)
	writeCLIJSONFile(t, filepath.Join(dir, "visibility.json"), map[string]any{
		"schemaVersion": 1, "hostDisplay": ":0", "vncAddress": "127.0.0.1:5909", "passwordProtected": true,
		"viewerExecutable": "/usr/bin/vncviewer", "viewerWindowWidth": 1280, "viewerWindowHeight": 800,
		"screenshotSource": "vnc-streamed-xvfb-root",
		"screenshotSha256": cliFileSHA256(t, filepath.Join(dir, "visible-desktop.xwd")),
	})
	writeCLIJSONFile(t, filepath.Join(dir, "cleanup.json"), map[string]any{
		"schemaVersion": 1, "processesStopped": true, "portsClosed": true, "workDirectoriesGone": true,
		"checkedPorts": []int{3001, 9222, 4173, 5909},
	})
	evidence := cleardevdemo.Evidence{
		SchemaVersion: cleardevdemo.EvidenceSchemaV2, RequirementID: "req", RequirementVersionID: "ver", RequirementSHA256: reqHash,
		PlanID: "plan", PlanSHA256: planHash, PlanReviewID: "review", PlanReviewVerdict: "APPROVED",
		TaskSetVersion: core.ComplexStandardTaskSetVersion, Mode: "PARALLEL", FixedBuilderCount: 2,
		ProgressPhase: "COMPLETED", TemplateCommit: template, IntegrationSHA: combo, ExplanationSHA256: hash,
		IntegrationCheckIDs: []string{"demo-integration"},
		Sample:              map[string]int{"accepted": 2, "rejected": 1, "duplicates": 2},
		RoleSessions:        map[string]string{"STEWARD": "repo-1"},
		Tasks: []map[string]any{
			{"id": "t1", "status": "DONE", "currentCandidateCommitSha": sha, "requiredCheckIds": []string{"demo-database"}},
			{"id": "t2", "status": "DONE", "currentCandidateCommitSha": other, "requiredCheckIds": []string{"demo-backend", "demo-api"}},
		},
		CheckRuns: []map[string]any{
			{"kind": "SCOPE", "status": "SETTLED", "result": "PASS", "complexExecutionTaskId": "t1", "candidateCommitSha": sha},
			{"kind": "REQUIRED_CHECK", "status": "SETTLED", "result": "PASS", "checkId": "demo-database", "complexExecutionTaskId": "t1", "candidateCommitSha": sha},
			{"kind": "SCOPE", "status": "SETTLED", "result": "PASS", "complexExecutionTaskId": "t2", "candidateCommitSha": other},
			{"kind": "REQUIRED_CHECK", "status": "SETTLED", "result": "PASS", "checkId": "demo-backend", "complexExecutionTaskId": "t2", "candidateCommitSha": other},
			{"kind": "REQUIRED_CHECK", "status": "SETTLED", "result": "PASS", "checkId": "demo-api", "complexExecutionTaskId": "t2", "candidateCommitSha": other},
			{"kind": "INTEGRATION", "status": "SETTLED", "result": "PASS", "checkId": "demo-integration", "candidateCommitSha": combo},
		},
		Reviews: []map[string]any{
			{"status": "SETTLED", "verdict": "PASS", "complexExecutionTaskId": "t1", "candidateCommitSha": sha},
			{"status": "SETTLED", "verdict": "PASS", "complexExecutionTaskId": "t2", "candidateCommitSha": other},
		},
		SpecialistChecks:     []map[string]any{{"checkId": "sqlite-migration-specialist", "result": "PASS", "status": "SETTLED"}},
		Usage:                map[string]any{"agentSteps": 4, "checkRuns": 6, "reviews": 2},
		InputSHA256:          map[string]string{"prd.md": sum},
		BuildCandidateCommit: buildCandidate,
		BuildSourceClean:     true,
		BuildSourceSHA256:    cliFileSHA256(t, filepath.Join(dir, "cleardev-build-source.json")),
		RuntimeSHA256: map[string]string{
			"electron": hash, "daemon": hash, "agent-browser": hash,
		},
		StartupSHA256:    cliFileSHA256(t, filepath.Join(dir, "desktop-startup.json")),
		VisibilitySHA256: cliFileSHA256(t, filepath.Join(dir, "visibility.json")),
		CleanupSHA256:    cliFileSHA256(t, filepath.Join(dir, "cleanup.json")),
	}
	raw, err = json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.json"), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCLIJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cliFileSHA256(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
