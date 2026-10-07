//go:build !windows

package e2e

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevdemo"
)

// TestClearDevFullDemo is the S11 gated demonstration. It uses the production
// in-repo demo command path: real Electron, native v1 confirmation, real Codex
// roles, and an isolated AO data directory. It does not use fake desktop
// authority or test seeders.
func TestClearDevFullDemo(t *testing.T) {
	requireE2E(t)
	requireElectronE2E(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("full demonstration needs git: %v", err)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("full demonstration needs docker: %v", err)
	}

	parent := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer cancel()
	result, err := cleardevdemo.Run(ctx, cleardevdemo.Options{
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
		ParentDir: parent,
	})
	if err != nil {
		t.Fatalf("full demonstration failed: %v (evidence=%s)", err, result.Layout.EvidenceDir)
	}
	if _, err := cleardevdemo.VerifyEvidenceDir(result.Layout.EvidenceDir); err != nil {
		t.Fatalf("offline evidence check failed: %v", err)
	}
	if result.Evidence.Mode != "PARALLEL" || result.Evidence.FixedBuilderCount != 2 {
		t.Fatalf("evidence mode = %#v", result.Evidence)
	}
	t.Logf("S11 full demonstration evidence=%s requirement=%s integration=%s durationMs=%d",
		result.Layout.EvidenceDir, result.Evidence.RequirementID, result.Evidence.IntegrationSHA, result.Evidence.DurationMS)
}
