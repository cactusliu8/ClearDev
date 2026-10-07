//go:build linux

package cleardev

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/humanauthority"
)

// The model/checks are explicit test doubles. The window, native button event,
// Electron production host, Go private-channel client, and SQLite are real.
// Never call this a human approval or a production demonstration.
func TestNativeElectronExtraMailAuthority(t *testing.T) {
	if os.Getenv("CLEARDEV_REQUIRE_NATIVE_ELECTRON") != "1" {
		t.Skip("set CLEARDEV_REQUIRE_NATIVE_ELECTRON=1 under xvfb-run to run the native test")
	}
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	dependencies := os.Getenv("CLEARDEV_NATIVE_DEPENDENCIES")
	if dependencies == "" {
		dependencies = filepath.Join(root, "frontend/node_modules")
	}
	if !filepath.IsAbs(dependencies) {
		t.Fatal("native dependency cache must be absolute")
	}
	electron := filepath.Join(dependencies, "electron/dist/electron")
	bundler := filepath.Join(dependencies, "rolldown/bin/cli.mjs")
	for _, file := range []string{electron, bundler} {
		if _, err := os.Stat(file); err != nil {
			t.Fatal("required local native dependency unavailable", err)
		}
	}
	if _, err := exec.LookPath("xdotool"); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "mail-authority.cjs")
	build := exec.Command("node", bundler, filepath.Join(root, "frontend/test-native/mail-authority-main.ts"), "--platform", "node", "--format", "cjs", "--external", "electron", "--file", bundle)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native test bundle: %v %s", err, output)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	evidenceBase := filepath.Join(home, ".ao", "demo-sprint-native")
	if err := os.MkdirAll(evidenceBase, 0o700); err != nil {
		t.Fatal(err)
	}
	if helper := os.Getenv("CLEARDEV_NATIVE_SYSTEM_SANDBOX"); helper != "" {
		electron = nativeRuntimeWithSystemSandbox(t, filepath.Dir(electron), evidenceBase, helper)
	}
	for _, choice := range []core.HumanDecisionChoice{core.HumanDecisionLater, core.HumanDecisionReject, core.HumanDecisionApprove} {
		t.Run(string(choice), func(t *testing.T) {
			f, _ := newBoundedMailFixture(t, 3)
			f.confirm(t)
			before := boundedExecution(t, f)
			if len(before.Dispatches) != 3 {
				t.Fatal("fixture did not actually exhaust three bounded attempts")
			}
			f.service.now = func() time.Time { return time.Now().UTC() }
			f.service.stepTimeout = 10 * time.Second
			evidence, err := os.MkdirTemp(evidenceBase, strings.ToLower(string(choice))+"-")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			bootstrapReader, bootstrapWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = bootstrapReader.Close(); _ = bootstrapWriter.Close() }()
			cmd := exec.CommandContext(ctx, electron, bundle)
			cmd.Env = nativeTestEnv(evidence)
			cmd.ExtraFiles = []*os.File{bootstrapWriter}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = bootstrapWriter.Close()
			waited := false
			defer func() {
				if !waited {
					_, _ = io.WriteString(stdin, "quit\n")
					_ = stdin.Close()
					cancel()
					_ = cmd.Wait()
				}
			}()
			type bootstrapRead struct {
				value humanauthority.Bootstrap
				err   error
			}
			ready := make(chan bootstrapRead, 1)
			go func() { b, err := humanauthority.ReadBootstrap(bootstrapReader); ready <- bootstrapRead{b, err} }()
			var bootstrap humanauthority.Bootstrap
			select {
			case r := <-ready:
				if r.err != nil {
					cancel()
					_ = cmd.Wait()
					waited = true
					t.Fatalf("native bootstrap unavailable (no secrets logged): %v; stderr=%s", r.err, stderr.String())
				}
				bootstrap = r.value
			case <-ctx.Done():
				t.Fatal("native Electron did not start")
			}
			channelCtx, channelCancel := context.WithCancel(ctx)
			handler := &nativeDecisionObserver{Service: f.service, result: make(chan nativeDecisionOutcome, 1), cancel: channelCancel}
			client := humanauthority.NewClient(bootstrap.HumanAuthorityEndpoint, bootstrap.HumanAuthorityToken, bootstrap.DesktopRunID, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
			clientDone := make(chan struct{})
			go func() { defer close(clientDone); client.Run(channelCtx) }()
			defer func() { channelCancel(); <-clientDone }()
			window, err := waitNativeAuthorityWindow(ctx, cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			// Persist the actual native window before deterministic test keyboard input.
			capture := exec.CommandContext(ctx, "xwd", "-silent", "-id", window, "-out", filepath.Join(evidence, "native-dialog.xwd"))
			if output, err := capture.CombinedOutput(); err != nil {
				t.Fatalf("capture native dialog: %v %s", err, output)
			}
			keys := []string{"Escape"}
			if choice == core.HumanDecisionReject {
				keys = []string{"Left", "Return"}
			}
			if choice == core.HumanDecisionApprove {
				keys = []string{"Left", "Left", "Return"}
			}
			focus := exec.CommandContext(ctx, "xdotool", "windowfocus", "--sync", window)
			if output, err := focus.CombinedOutput(); err != nil {
				t.Fatalf("native focus: %v %s", err, output)
			}
			// Focus is already pinned above on an isolated X display. XTEST input
			// avoids a synthetic key-up targeting a modal that Escape just closed.
			keyArgs := append([]string{"key", "--delay", "80"}, keys...)
			if output, err := exec.CommandContext(ctx, "xdotool", keyArgs...).CombinedOutput(); err != nil {
				t.Fatalf("native key input: %v %s", err, output)
			}
			var got nativeDecisionOutcome
			select {
			case got = <-handler.result:
			case <-ctx.Done():
				t.Fatal("native dialog did not return through the private channel")
			}
			if got.err != nil || got.result.Decision != choice {
				t.Fatalf("native choice=%s wanted=%s apply=%v", got.result.Decision, choice, got.err)
			}
			after := boundedExecution(t, f)
			want := 3
			if choice == core.HumanDecisionApprove {
				want = 4
			}
			if len(after.Dispatches) != want || f.counts().builderSends != want {
				t.Fatal("native decision did not enforce exactly one extra attempt")
			}
			if choice == core.HumanDecisionApprove {
				if phase, _ := core.DeriveComplexExecutionPhase(after); phase != core.ComplexExecutionCompleted {
					t.Fatalf("authorized attempt did not complete: %s", phase)
				}
			} else {
				assertMailNotCompleted(t, f)
			}
			if err := f.service.ApplyHumanDecisionResult(context.Background(), got.result); err == nil {
				t.Fatal("consumed native decision was replayable")
			}
			channelCancel()
			<-clientDone
			_, _ = io.WriteString(stdin, "quit\n")
			_ = stdin.Close()
			if err := cmd.Wait(); err != nil {
				waited = true
				t.Fatalf("native Electron exit: %v %s", err, stderr.String())
			}
			waited = true
			if err := os.WriteFile(filepath.Join(evidence, "native-events.jsonl"), stdout.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(bootstrap.HumanAuthorityEndpoint); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("test private socket was not cleaned up")
			}
			t.Logf("real native %s -> production private channel -> %d dispatches; screenshot/events=%s; explicit synthetic input, not human approval", choice, want, evidence)
		})
	}
}

type nativeDecisionOutcome struct {
	result core.HumanDecisionResult
	err    error
}
type nativeDecisionObserver struct {
	*Service
	result chan nativeDecisionOutcome
	cancel context.CancelFunc
}

func (h *nativeDecisionObserver) ApplyHumanDecisionResult(ctx context.Context, result core.HumanDecisionResult) error {
	err := h.Service.ApplyHumanDecisionResult(ctx, result)
	h.cancel()
	h.result <- nativeDecisionOutcome{result, err}
	return err
}
func nativeTestEnv(root string) []string {
	out := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "ELECTRON_RUN_AS_NODE" || key == "CLEARDEV_NATIVE_AUTHORITY_TEST" || key == "CLEARDEV_NATIVE_AUTHORITY_ROOT" {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "CLEARDEV_NATIVE_AUTHORITY_TEST=1", "CLEARDEV_NATIVE_AUTHORITY_ROOT="+root)
}

// Use an already installed root-owned sandbox helper without chmod, sudo,
// disabling sandboxing, modifying node_modules, or copying a Chromium runtime.
// Hard links keep the executable's origin in this isolated test directory.
func nativeRuntimeWithSystemSandbox(t *testing.T, dist, evidenceBase, helper string) string {
	t.Helper()
	if !filepath.IsAbs(helper) {
		t.Fatal("sandbox helper must be absolute")
	}
	info, err := os.Stat(helper)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode()&os.ModeSetuid == 0 || info.Mode().Perm()&0o022 != 0 {
		t.Fatal("system sandbox helper must already be root-owned, setuid, and not writable by other users")
	}
	dir, err := os.MkdirTemp(evidenceBase, "runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	entries, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "chrome-sandbox" {
			continue
		}
		source, destination := filepath.Join(dist, name), filepath.Join(dir, name)
		if name == "electron" {
			err = os.Link(source, destination)
		} else {
			err = os.Symlink(source, destination)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(helper, filepath.Join(dir, "chrome-sandbox")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "electron")
}

func waitNativeAuthorityWindow(ctx context.Context, pid int) (string, error) {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		output, err := exec.CommandContext(ctx, "xdotool", "search", "--all", "--onlyvisible", "--pid", strconv.Itoa(pid), "--name", "^Authorize one additional development attempt$").Output()
		if err == nil {
			ids := strings.Fields(string(output))
			if len(ids) == 1 {
				return ids[0], nil
			}
			if len(ids) > 1 {
				return "", errors.New("ambiguous native authority windows")
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			return "", fmt.Errorf("native extra-authority dialog did not become visible for PID %d", pid)
		case <-tick.C:
		}
	}
}
