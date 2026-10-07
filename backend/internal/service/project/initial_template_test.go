package project_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/project"
)

func TestInitializeRepositoryPreparesHealthyMailBaseline(t *testing.T) {
	manager := newManager(t)
	dir := isolatedPlainFolder(t)
	result, err := manager.InitializeRepository(context.Background(), project.InitializeRepositoryInput{Path: dir, Template: "complex-mail-app"})
	if err != nil || result.Path != dir {
		t.Fatalf("initialization=%#v err=%v", result, err)
	}
	run := func(name string, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "npm_config_offline=true", "npm_config_update_notifier=false")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sha := run("git", "rev-parse", "HEAD")
	if len(sha) != 40 || run("git", "status", "--porcelain", "--untracked-files=all") != "" {
		t.Fatal("prepared baseline is not a clean committed repository")
	}
	out := run("npm", "test")
	if !strings.Contains(out, "# pass 12") || !strings.Contains(out, "# fail 0") || !strings.Contains(out, "# skipped 0") {
		t.Fatalf("full baseline suite did not run: %s", out)
	}
	if run("git", "rev-parse", "HEAD") != sha || run("git", "status", "--porcelain", "--untracked-files=all") != "" {
		t.Fatal("baseline verification changed the prepared repository")
	}
	// Register through the existing normal product service, not direct SQL.
	registered, err := manager.Add(context.Background(), project.AddInput{Path: dir})
	if err != nil {
		t.Fatalf("prepared baseline cannot be registered: %v (%#v)", err, registered)
	}
}

func TestInitializeMailTemplateRefusesUnsupportedOrExistingContent(t *testing.T) {
	for _, mode := range []string{"unknown", "existing-file", "unborn-repository"} {
		t.Run(mode, func(t *testing.T) {
			manager := newManager(t)
			dir := isolatedPlainFolder(t)
			template := "complex-mail-app"
			switch mode {
			case "unknown":
				template = "agent-selected-template"
			case "existing-file":
				if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("user work"), 0o600); err != nil {
					t.Fatal(err)
				}
			default:
				if out, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			}
			before, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.InitializeRepository(context.Background(), project.InitializeRepositoryInput{Path: dir, Template: template}); err == nil {
				t.Fatal("unsafe template initialization succeeded")
			}
			after, err := os.ReadDir(dir)
			if err != nil || len(before) != len(after) {
				t.Fatalf("destination changed: %v", err)
			}
			if mode == "existing-file" {
				data, err := os.ReadFile(filepath.Join(dir, "keep"))
				if err != nil || string(data) != "user work" {
					t.Fatal("user file changed")
				}
			}
		})
	}
}
