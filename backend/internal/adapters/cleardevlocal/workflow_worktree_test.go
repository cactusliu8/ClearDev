package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowWorkingTreePreservesUnfinishedFiles(t *testing.T) {
	r, request := newProjectFreezeFixture(t, true)
	file := filepath.Join(request.WorkspacePath, "app/store.cjs")
	writeTestFile(t, file, "exports.list = () => ['unfinished'];\n")
	status := runGit(t, request.WorkspacePath, "status", "--porcelain")
	index := runGit(t, request.WorkspacePath, "ls-files", "--stage")
	digest, err := r.InspectWorkflowWorkingTree(context.Background(), request)
	if err != nil || len(digest) != 64 {
		t.Fatalf("digest=%s err=%v", digest, err)
	}
	again, err := r.InspectWorkflowWorkingTree(context.Background(), request)
	if err != nil || digest != again {
		t.Fatal("unchanged context was not stable", err)
	}
	if runGit(t, request.WorkspacePath, "status", "--porcelain") != status || runGit(t, request.WorkspacePath, "ls-files", "--stage") != index || strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-parse", "HEAD")) != request.ParentSHA {
		t.Fatal("read-only continuation changed files, index or head")
	}
	writeTestFile(t, file, "exports.list = () => ['changed'];\n")
	changed, err := r.InspectWorkflowWorkingTree(context.Background(), request)
	if err != nil || changed == digest {
		t.Fatal("working changes did not change the digest", err)
	}
	writeTestFile(t, filepath.Join(request.WorkspacePath, "unapproved.txt"), "outside scope")
	if _, err = r.InspectWorkflowWorkingTree(context.Background(), request); err != nil {
		t.Fatal("unfinished out-of-scope file prevented continuation", err)
	}
	if _, err = r.FreezeMailCandidate(context.Background(), request); err == nil {
		t.Fatal("continuation accidentally authorized out-of-scope delivery")
	}
	if err = os.Remove(filepath.Join(request.WorkspacePath, "unapproved.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(file, filepath.Join(request.WorkspacePath, "app/link.cjs")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.InspectWorkflowWorkingTree(context.Background(), request); err == nil {
		t.Fatal("working tree symlink accepted")
	}
}

func TestWorkflowWorkingTreeRejectsUnsafeIndex(t *testing.T) {
	for _, mode := range []string{"forbidden-staged", "symlink-index", "conflict-index"} {
		t.Run(mode, func(t *testing.T) {
			r, request := newProjectFreezeFixture(t, true)
			ctx := context.Background()
			g, err := r.mailFreezeRepository(request)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "forbidden-staged":
				request.WritePaths = []string{"app/**"}
				file := filepath.Join(request.WorkspacePath, "config/application.json")
				original, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, file, "{\"name\":\"staged change\"}\n")
				runGit(t, request.WorkspacePath, "add", "config/application.json")
				if err = os.WriteFile(file, original, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink-index":
				oid := strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-parse", "HEAD:app/store.cjs"))
				runGit(t, request.WorkspacePath, "update-index", "--add", "--cacheinfo", "120000,"+oid+",app/link.cjs")
			case "conflict-index":
				oid := strings.TrimSpace(runGit(t, request.WorkspacePath, "rev-parse", "HEAD:app/store.cjs"))
				input := "0 " + strings.Repeat("0", 40) + "\tapp/store.cjs\n100644 " + oid + " 1\tapp/store.cjs\n"
				if _, err = g.run(ctx, []byte(input), "update-index", "--index-info"); err != nil {
					t.Fatal(err)
				}
			}
			before := runGit(t, request.WorkspacePath, "ls-files", "--stage")
			_, err = r.InspectWorkflowWorkingTree(ctx, request)
			if mode == "forbidden-staged" {
				if err != nil {
					t.Fatal("staged unfinished work prevented continuation", err)
				}
			} else if err == nil {
				t.Fatal("unsafe index accepted")
			}
			if after := runGit(t, request.WorkspacePath, "ls-files", "--stage"); after != before {
				t.Fatal("refusal changed index")
			}
		})
	}
}

func TestWorkflowWorkingTreeRetainsDependenciesAndBindsLock(t *testing.T) {
	r, request := newProjectFreezeFixture(t, true)
	// The lock is not approved for final delivery in this task yet.
	request.WritePaths = []string{"app/**"}
	lock := filepath.Join(request.WorkspacePath, "package-lock.json")
	writeTestFile(t, lock, "{\"lockfileVersion\":3}\n")
	cache := filepath.Join(request.WorkspacePath, "node_modules", "pkg", "cache.txt")
	if err := os.MkdirAll(filepath.Dir(cache), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cache, "installed dependency")
	link := filepath.Join(request.WorkspacePath, "node_modules", "pkg", "bin")
	if err := os.Symlink("cache.txt", link); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	digest, err := r.InspectWorkflowWorkingTree(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cache, "changed runtime cache")
	again, err := r.InspectWorkflowWorkingTree(ctx, request)
	if err != nil || digest != again {
		t.Fatalf("runtime cache entered source receipt: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("dependency symlink lost", err)
	}
	writeTestFile(t, lock, "{\"lockfileVersion\":2}\n")
	changed, err := r.InspectWorkflowWorkingTree(ctx, request)
	if err != nil || changed == digest {
		t.Fatalf("lock file not bound: %v", err)
	}
	runGit(t, request.WorkspacePath, "add", "node_modules/pkg/cache.txt")
	tracked, err := r.InspectWorkflowWorkingTree(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cache, "tracked source changed")
	after, err := r.InspectWorkflowWorkingTree(ctx, request)
	if err != nil || tracked == after {
		t.Fatalf("tracked dependency escaped receipt: %v", err)
	}
	if _, err = r.FreezeMailCandidate(ctx, request); err == nil {
		t.Fatal("unfinished context authorized delivery")
	}
}
