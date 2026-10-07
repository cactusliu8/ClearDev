package cleardev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type realProductSourceInspector struct {
	*projectPlanningAgent
	runner *cleardevlocal.Runner
}

func (h realProductSourceInspector) InspectProjectSource(ctx context.Context, path, branch string) (ports.ClearDevProjectSource, error) {
	return h.runner.InspectProjectSource(ctx, path, branch)
}
func (h realProductSourceInspector) PrepareProjectSource(ctx context.Context, in ports.ClearDevSourcePreparation) (ports.ClearDevProjectSource, error) {
	return h.runner.PrepareProjectSource(ctx, in)
}

func TestSourcePreparationRealGitSQLiteAndDiscussion(t *testing.T) {
	ctx := context.Background()
	f := newProjectPlanningFixture(t, "EMPTY")
	git := func(path string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	target, upstream, serverRoot := t.TempDir(), t.TempDir(), t.TempDir()
	for _, path := range []string{target, upstream} {
		git(path, "init", "-b", "main")
		git(path, "config", "user.name", "Test")
		git(path, "config", "user.email", "test@example.invalid")
		git(path, "commit", "--allow-empty", "-m", path)
	}
	if err := os.WriteFile(filepath.Join(upstream, "README.md"), []byte("actual source"), 0600); err != nil {
		t.Fatal(err)
	}
	git(upstream, "add", "README.md")
	git(upstream, "commit", "-m", "source")
	source := git(upstream, "rev-parse", "HEAD")
	base := git(target, "rev-parse", "HEAD")
	git(serverRoot, "clone", "--bare", upstream, "upstream.git")
	git(filepath.Join(serverRoot, "upstream.git"), "update-server-info")
	server := httptest.NewServer(http.FileServer(http.Dir(serverRoot)))
	defer server.Close()
	sourceURL := server.URL + "/upstream.git"
	project, found, err := f.store.GetProject(ctx, "notes-project")
	if err != nil || !found {
		t.Fatal(err)
	}
	project.Path = target
	project.Config.DefaultBranch = "refs/heads/main"
	if err := f.store.UpsertProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	f.s.inspector = realProductSourceInspector{f.h, cleardevlocal.New()}
	f.h.replies = append(f.h.replies, strings.ReplaceAll(genericProjectReply(""), "https://example.org/notes/notes.git", sourceURL))
	initial, err := f.s.CreateProductGoal(ctx, CreateProductGoalInput{AOProjectID: "s04-project", RequestID: "real-source", Name: "source test", GoalText: "use existing source"})
	if err != nil {
		t.Fatal(err)
	}
	input := projectChoice(initial, "discovered")
	input.Choice.ExpectedBaseCommitSHA = base
	f.h.replies = append(f.h.replies, strings.ReplaceAll(genericProjectReply("discovered"), "https://example.org/notes/notes.git", sourceURL))
	view, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input)
	if err != nil || view.Phase != "READY" || view.Selection == nil || view.Selection.BaseCommitSHA == base {
		t.Fatalf("source flow: %+v %v", view, err)
	}
	if !view.SourceCurrent || len(view.Discussions) != 2 || len(f.h.relays) != 2 {
		t.Fatalf("source was not bound once: %+v", view)
	}
	actual := git(target, "rev-parse", "HEAD")
	if actual != view.Selection.BaseCommitSHA || git(target, "show", "-s", "--format=%P", actual) != base+" "+source {
		t.Fatal("selected baseline does not match imported history")
	}
	if data, err := os.ReadFile(filepath.Join(target, "README.md")); err != nil || string(data) != "actual source" {
		t.Fatal("actual source was not materialized")
	}
	server.Close()
	if _, err := f.s.SubmitProductDiscussion(ctx, initial.Goal.ID, input); err != nil || len(f.h.relays) != 2 {
		t.Fatalf("replay: %v", err)
	}
}
