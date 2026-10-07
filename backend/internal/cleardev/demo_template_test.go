package cleardev

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDemoTemplateBuildsAndPassesTemplateChecks(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is required to check the frozen demo template")
	}
	dir := t.TempDir()
	if err := CopyDemoTemplate(dir); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"build", "test:template", "check:migrations"} {
		cmd := exec.Command("npm", "run", script)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "npm_config_update_notifier=false")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("npm run %s: %v\n%s", script, err, output)
		}
	}
}

func TestCopyDemoTemplateWritesLockedLayout(t *testing.T) {
	dir := t.TempDir()
	if err := CopyDemoTemplate(dir); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"package.json", "PRD.md", "backend/src/emails.ts", "frontend/index.html",
		"migrations/000_placeholder.sql", "scripts/check-migrations.mjs", ".github/workflows/ci.yml",
	} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
	}
}
