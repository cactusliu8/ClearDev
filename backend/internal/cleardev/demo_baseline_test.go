package cleardev

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDemoBaselinePreservesLockedFilesAndOriginalTests(t *testing.T) {
	dir := t.TempDir()
	if err := CopyDemoBaseline(dir); err != nil {
		t.Fatal(err)
	}
	err := fs.WalkDir(DemoTemplateFS, demoTemplateRoot, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel := strings.TrimPrefix(name, demoTemplateRoot+"/")
		isTest := strings.HasPrefix(rel, "test/")
		locked := rel == "package.json" || rel == "package-lock.json" || strings.HasPrefix(rel, "migrations/") || strings.HasPrefix(rel, "scripts/") || strings.HasPrefix(rel, ".github/")
		if !isTest && !locked {
			return nil
		}
		original, err := DemoTemplateFS.ReadFile(name)
		if err != nil {
			return err
		}
		current, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return err
		}
		if isTest && !bytes.HasPrefix(current, original) {
			t.Errorf("original test is not a byte-for-byte prefix: %s", rel)
		}
		if locked && !bytes.Equal(current, original) {
			t.Errorf("legacy locked file changed: %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDemoBaselineDoesNotOverwriteAnExistingProject(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "user-work")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyDemoBaseline(dir); err == nil {
		t.Fatal("nonempty destination was accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("destination changed: entries=%v err=%v", entries, err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}
}

// Two fresh repositories here are baseline verification, NOT the two formal
// Agent-driven incremental demonstrations. Those remain separate evidence.
func TestDemoBaselineFreshInstancesPassFullChecks(t *testing.T) {
	for _, tool := range []string{"git", "node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("baseline verification requires %s", tool)
		}
	}
	var frozenSHA string
	for _, instance := range []string{"first", "second"} {
		t.Run(instance, func(t *testing.T) {
			dir := t.TempDir()
			if err := CopyDemoBaseline(dir); err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			env := append(os.Environ(),
				"HOME="+home, "npm_config_cache="+filepath.Join(home, ".npm"),
				"npm_config_offline=true", "npm_config_update_notifier=false",
				"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
				"GIT_AUTHOR_NAME=ClearDev Baseline", "GIT_AUTHOR_EMAIL=baseline@localhost",
				"GIT_COMMITTER_NAME=ClearDev Baseline", "GIT_COMMITTER_EMAIL=baseline@localhost",
				"GIT_AUTHOR_DATE=2026-09-19T00:00:00Z", "GIT_COMMITTER_DATE=2026-09-19T00:00:00Z",
			)
			run := func(name string, args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, name, args...)
				cmd.Dir, cmd.Env = dir, env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %v: %v\n%s", name, args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("git", "init", "--quiet", "-b", "demo-baseline")
			run("git", "add", ".")
			run("git", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Freeze healthy complex-mail-app baseline")
			sha := run("git", "rev-parse", "HEAD")
			if frozenSHA == "" {
				frozenSHA = sha
			} else if frozenSHA != sha {
				t.Fatalf("fresh baseline differs: %s != %s", sha, frozenSHA)
			}
			assertClean := func() {
				t.Helper()
				if status := run("git", "status", "--porcelain", "--untracked-files=all"); status != "" {
					t.Fatalf("baseline is dirty: %s", status)
				}
				if got := run("git", "rev-parse", "HEAD"); got != sha {
					t.Fatalf("baseline SHA changed: %s", got)
				}
			}
			assertClean()
			for _, script := range []string{"build", "test:template", "check:migrations", "test"} {
				output := run("npm", "run", script)
				t.Logf("candidate=%s npm run %s:\n%s", sha, script, output)
				assertClean()
			}
		})
	}
}
