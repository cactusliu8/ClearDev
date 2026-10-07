package cleardevlocal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func TestCheckVolumeCleanupRetainsLeaseUntilVolumesGone(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	bin := t.TempDir()
	docker := filepath.Join(bin, "docker")
	volume := strings.Repeat("b", 64)
	// The container has disappeared, but its recorded volume remains in use.
	script := "#!/bin/sh\ncase \"$1\" in\ninspect) exit 1;;\nvolume) [ \"$2\" = ls ] && printf '%s\\n' '" + volume + "'; exit 1;;\nesac\nexit 0\n"
	if err := os.WriteFile(docker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ctx := context.Background()
	lease, err := acquireCheckTemporary(ctx, "volume-recovery", "candidate-check", checkSourceReservationBytes, "volume-check")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, lease.CIDFile(), strings.Repeat("a", 64))
	writeTestFile(t, lease.CIDFile()+".volumes.json", `["`+volume+`"]`)
	if err := lease.Release(ctx); err == nil {
		t.Fatal("surviving volume released lease")
	}
	if _, err := os.Stat(lease.CIDFile() + ".volumes.json"); err != nil {
		t.Fatal("lost recovery evidence:", err)
	}
	allowed, err := workflowCheckResourcesReleased(ctx, "volume-check")
	if err != nil || allowed {
		t.Fatalf("allowed retry with retained volume: %v %v", allowed, err)
	}
	// Recovery uses the sidecar even when Docker can no longer inspect the container.
	if err := os.WriteFile(docker, []byte("#!/bin/sh\n[ \"$1\" = inspect ] && exit 1\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(ctx); err != nil {
		t.Fatal(err)
	}
	allowed, err = workflowCheckResourcesReleased(ctx, "volume-check")
	if err != nil || !allowed {
		t.Fatalf("confirmed removal did not release lease: %v %v", allowed, err)
	}
}

func TestCheckStartupCancellationPreservesVolumeCleanup(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "retained"}[retained], func(t *testing.T) {
			bin := t.TempDir()
			cid := filepath.Join(bin, "cid")
			flag := filepath.Join(bin, "retain")
			volume := strings.Repeat("b", 64)
			if retained {
				writeTestFile(t, flag, "retained")
			}
			script := `#!/bin/sh
case "$1" in
run)
  for arg in "$@"; do case "$arg" in --cidfile=*) cid=${arg#--cidfile=};; esac; done
  printf '%s' '` + strings.Repeat("a", 64) + `' > "$cid"
  exec /bin/sleep 30;;
inspect) printf '%s' '[{"Type":"volume","Name":"` + volume + `","Destination":"/workspace"}]';;
volume)
  if [ -f "$CLEARDEV_STARTUP_RETAIN" ]; then
    [ "$2" = ls ] && { printf '%s' '` + volume + `'; exit 0; }
    exit 1
  fi;;
esac
exit 0
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("CLEARDEV_STARTUP_RETAIN", flag)
			source := t.TempDir()
			usage, err := measureCheckTree(source, productionCheckCapacityLimits().Source)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				for ctx.Err() == nil {
					if raw, err := os.ReadFile(cid); err == nil && validDockerID(string(raw)) {
						cancel()
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
			runtime := core.DefaultProjectRuntimeV1()
			result, err := RunCheckContainer(ctx, CheckContainerRequest{Image: projectInstallImage, FreshInstall: true, ProjectRuntime: &runtime, SourceDir: source, SourceBytes: usage.Bytes, SourceItems: usage.Items, CIDFile: cid, Argv: []string{"node", "-e", "process.exit(0)"}, Timeout: time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64, OutputLimit: 4096})
			<-done
			if err == nil || result.CommandExecuted {
				t.Fatalf("startup cancellation accepted: %+v %v", result, err)
			}
			if retained {
				if !strings.Contains(err.Error(), "check volume remains") {
					t.Fatal("lost cleanup failure:", err)
				}
				if raw := readTestFile(t, cid+".volumes.json"); !strings.Contains(raw, volume) {
					t.Fatal("lost volume identity")
				}
				if err := os.Remove(flag); err != nil {
					t.Fatal(err)
				}
				if err := removeCheckContainerAndConfirm(context.Background(), cid); err != nil {
					t.Fatal(err)
				}
			} else if _, err := os.Stat(cid); !os.IsNotExist(err) {
				t.Fatal("confirmed startup cleanup retained cid:", err)
			}
			if _, err := os.Stat(cid + ".volumes.json"); !os.IsNotExist(err) {
				t.Fatal("confirmed cleanup retained sidecar:", err)
			}
		})
	}
}

func TestCheckCleanupWaitsForRemovalConfirmation(t *testing.T) {
	for _, mode := range []string{"async", "present", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AO_DATA_DIR", t.TempDir())
			bin := t.TempDir()
			t.Setenv("CLEARDEV_REMOVE_MODE", mode)
			t.Setenv("CLEARDEV_REMOVE_SEEN", filepath.Join(bin, "seen"))
			id := strings.Repeat("a", 64)
			script := `#!/bin/sh
case "$1" in
rm) echo 'removal is already in progress' >&2; exit 1;;
ps)
 [ "$CLEARDEV_REMOVE_MODE" = unknown ] && exit 1
 if [ "$CLEARDEV_REMOVE_MODE" = present ] || [ ! -f "$CLEARDEV_REMOVE_SEEN" ]; then
  printf x > "$CLEARDEV_REMOVE_SEEN"
  printf '%s\n' '` + id + `'
 fi;;
esac
exit 0
`
			writeTestFile(t, filepath.Join(bin, "docker"), script)
			if err := os.Chmod(filepath.Join(bin, "docker"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			lease, err := acquireCheckTemporary(context.Background(), "async-removal", "candidate-check", checkSourceReservationBytes, "async-run")
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, lease.CIDFile(), id)
			writeTestFile(t, lease.CIDFile()+".volumes.json", "[]")
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			err = lease.Release(ctx)
			if mode == "async" && err != nil {
				t.Fatal(err)
			}
			if mode != "async" && err == nil {
				t.Fatal("unconfirmed cleanup accepted")
			}
			allowed, probeErr := workflowCheckResourcesReleased(context.Background(), "async-run")
			if probeErr != nil || allowed != (mode == "async") {
				t.Fatalf("released=%v err=%v", allowed, probeErr)
			}
			if mode != "async" {
				if _, err := os.Stat(lease.CIDFile() + ".volumes.json"); err != nil {
					t.Fatal("cleanup evidence lost", err)
				}
			}
		})
	}
}

func TestStartupCleanupPreservesLiveOwner(t *testing.T) {
	t.Setenv("AO_DATA_DIR", t.TempDir())
	lease, err := acquireCheckTemporary(context.Background(), "live-cleanup", "candidate-check", checkSourceReservationBytes, "live-run")
	if err != nil {
		t.Fatal(err)
	}
	if err := New().RecoverCheckResources(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.Root()); err != nil {
		t.Fatal("live directory removed", err)
	}
	allowed, err := workflowCheckResourcesReleased(context.Background(), "live-run")
	if err != nil || allowed {
		t.Fatalf("live reservation lost: %v %v", allowed, err)
	}
}

func TestCheckCleanupAllowsSlowLocalRemoval(t *testing.T) {
	if CheckCleanupTimeout != 10*time.Minute {
		t.Fatal("cleanup budget is not ten minutes")
	}
	bin := t.TempDir()
	cid := filepath.Join(bin, "container.cid")
	writeTestFile(t, cid, strings.Repeat("a", 64))
	writeTestFile(t, cid+".volumes.json", "[]")
	writeTestFile(t, filepath.Join(bin, "docker"), "#!/bin/sh\n[ \"$1\" = rm ] && /bin/sleep 16\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "docker"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := removeCheckContainerAndConfirm(ctx, cid); err != nil {
		t.Fatal("local removal exceeded old 15 second budget:", err)
	}
	if _, err := os.Stat(cid + ".volumes.json"); !os.IsNotExist(err) {
		t.Fatal("volume evidence not settled:", err)
	}
}
