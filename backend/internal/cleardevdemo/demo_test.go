package cleardevdemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
)

func TestPreflightMissingGitDoesNotCreateLayout(t *testing.T) {
	parent := t.TempDir()
	err := Preflight(context.Background(), Options{
		ParentDir: parent,
		LookPath: func(name string) (string, error) {
			if name == "git" {
				return "", errors.New("git missing")
			}
			return "/bin/" + name, nil
		},
	})
	if err == nil {
		t.Fatal("expected preflight to fail")
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("preflight created %d entries: %v", len(entries), entries)
	}
}

func TestRunPreflightFailureLeavesParentEmpty(t *testing.T) {
	parent := t.TempDir()
	_, err := Run(context.Background(), Options{
		ParentDir: parent,
		LookPath: func(name string) (string, error) {
			return "", errors.New("missing " + name)
		},
	})
	if err == nil {
		t.Fatal("expected run to fail preflight")
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed preflight created %d entries", len(entries))
	}
}

func TestAllocateLayoutRejectsExistingEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "evidence"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLayout(root); err == nil {
		t.Fatal("expected existing evidence directory to fail")
	}
}

func TestAllocateLayoutCreatesFreshTree(t *testing.T) {
	layout, err := AllocateLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if layout.Port == 0 || layout.DebugPort == 0 || layout.AppPort == 0 || layout.VNCPort == 0 {
		t.Fatalf("ports were not allocated: %#v", layout)
	}
	ports := map[int]bool{}
	for _, port := range []int{layout.Port, layout.DebugPort, layout.AppPort, layout.VNCPort} {
		ports[port] = true
	}
	if len(ports) != 4 {
		t.Fatalf("layout reused a port: %#v", layout)
	}
	second, err := AllocateLayout(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if second.EvidenceDir == layout.EvidenceDir {
		t.Fatal("second layout reused the evidence directory")
	}
}

func TestAllocateLayoutSeedsIndependentDisabledUpdateSettings(t *testing.T) {
	parent := t.TempDir()
	first, err := AllocateLayout(parent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := AllocateLayout(parent)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(filepath.Dir(first.RunFile), demoUpdateSettingsFileName)
	secondPath := filepath.Join(filepath.Dir(second.RunFile), demoUpdateSettingsFileName)
	if firstPath == secondPath {
		t.Fatal("two layouts reused update settings")
	}
	for _, settingsPath := range []string{firstPath, secondPath} {
		raw, readErr := os.ReadFile(settingsPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var settings map[string]any
		if err := json.Unmarshal(raw, &settings); err != nil {
			t.Fatal(err)
		}
		expected := map[string]any{"enabled": false, "channel": "latest", "nightlyAck": false, "feature": nil}
		if !reflect.DeepEqual(settings, expected) {
			t.Fatalf("unexpected isolated update settings: %+v", settings)
		}
		info, statErr := os.Stat(settingsPath)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("update settings mode = %03o, want 600", info.Mode().Perm())
		}
		if rel, relErr := filepath.Rel(parent, settingsPath); relErr != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("update settings escaped the isolated parent: path=%s rel=%s err=%v", settingsPath, rel, relErr)
		}
	}
}

func TestClickNativeApproveUsesOnlyExactVisibleElectronDialog(t *testing.T) {
	const expectedTitle = "Confirm requirement version v1"
	now := time.Unix(0, 0)
	closed := false
	var screenshotID string
	var keyWindowIDs []string
	inspect := func(string, ...int) []xWindow {
		windows := []xWindow{
			{ID: "update", Name: "Keep Agent Orchestrator up to date automatically?", PID: 42, Visible: true, Width: 580, Height: 180},
			{ID: "wrong-pid", Name: expectedTitle, PID: 99, Visible: true, Width: 600, Height: 300},
			{ID: "hidden", Name: expectedTitle, PID: 42, Visible: false, Width: 600, Height: 300},
		}
		if !closed {
			windows = append(windows, xWindow{ID: "confirm", Name: expectedTitle, PID: 42, Visible: true, Width: 1280, Height: 300})
		}
		return windows
	}
	ops := nativeDialogOps{
		now:     func() time.Time { return now },
		sleep:   func(duration time.Duration) { now = now.Add(duration) },
		inspect: inspect,
		screenshotWindow: func(_ string, id string, _ string) error {
			screenshotID = id
			return nil
		},
		screenshotDesktop: func(string, string) error { return nil },
		xdotool: func(_ string, args ...string) (string, error) {
			if len(args) > 2 && args[0] == "key" && args[1] == "--window" {
				keyWindowIDs = append(keyWindowIDs, args[2])
				closed = true
			}
			return "", nil
		},
	}
	if err := clickNativeApproveWithOps("fake", "confirm", map[string]bool{"main": true}, expectedTitle, []int{42}, time.Second, time.Second, ops); err != nil {
		t.Fatal(err)
	}
	if screenshotID != "confirm" {
		t.Fatalf("screenshotted window %q, want confirm", screenshotID)
	}
	if len(keyWindowIDs) != 1 || keyWindowIDs[0] != "confirm" {
		t.Fatalf("key targets = %v, want only confirm", keyWindowIDs)
	}
}

func TestClickNativeApproveDoesNotTouchUnrelatedWindow(t *testing.T) {
	const expectedTitle = "Confirm requirement version v1"
	now := time.Unix(0, 0)
	keys := 0
	ops := nativeDialogOps{
		now:   func() time.Time { return now },
		sleep: func(duration time.Duration) { now = now.Add(duration) },
		inspect: func(string, ...int) []xWindow {
			return []xWindow{{ID: "update", Name: "Keep Agent Orchestrator up to date automatically?", PID: 42, Visible: true, Width: 580, Height: 180}}
		},
		screenshotWindow:  func(string, string, string) error { return nil },
		screenshotDesktop: func(string, string) error { return nil },
		xdotool: func(_ string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "key" {
				keys++
			}
			return "", nil
		},
	}
	err := clickNativeApproveWithOps("fake", "confirm", map[string]bool{"main": true}, expectedTitle, []int{42}, 500*time.Millisecond, time.Second, ops)
	if err == nil || !strings.Contains(err.Error(), "Keep Agent Orchestrator up to date automatically?") {
		t.Fatalf("unexpected missing-dialog error: %v", err)
	}
	if keys != 0 {
		t.Fatalf("sent %d key sequences to an unrelated window", keys)
	}
}

func TestClickNativeApproveReportsDialogThatDoesNotClose(t *testing.T) {
	const expectedTitle = "Confirm requirement version v1"
	now := time.Unix(0, 0)
	keys := 0
	dialog := xWindow{ID: "confirm", Name: expectedTitle, PID: 42, Visible: true, Width: 600, Height: 300}
	ops := nativeDialogOps{
		now:               func() time.Time { return now },
		sleep:             func(duration time.Duration) { now = now.Add(duration) },
		inspect:           func(string, ...int) []xWindow { return []xWindow{dialog} },
		screenshotWindow:  func(string, string, string) error { return nil },
		screenshotDesktop: func(string, string) error { return nil },
		xdotool: func(_ string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "key" {
				keys++
			}
			return "", nil
		},
	}
	err := clickNativeApproveWithOps("fake", "confirm", map[string]bool{}, expectedTitle, []int{42}, time.Second, 200*time.Millisecond, ops)
	if err == nil || !strings.Contains(err.Error(), "did not close dialog confirm") ||
		!strings.Contains(err.Error(), "Name:Confirm requirement version v1") ||
		!strings.Contains(err.Error(), "PID:42") || !strings.Contains(err.Error(), "Visible:true") {
		t.Fatalf("unexpected close error: %v", err)
	}
	if keys != 3 {
		t.Fatalf("key attempts = %d, want 3", keys)
	}
}

func TestFinalizeDemoCleanupRemovesManagedWorkAndKeepsEvidence(t *testing.T) {
	layout, err := prepareLayout(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.AppDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cleanup, err := finalizeDemoCleanup(layout, &ProcessGroup{})
	if err != nil {
		t.Fatal(err)
	}
	if !cleanup.ProcessesStopped || !cleanup.PortsClosed || !cleanup.WorkDirectoriesGone {
		t.Fatalf("cleanup = %+v", cleanup)
	}
	if _, err := os.Stat(filepath.Join(layout.EvidenceDir, evidenceCleanupFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(layout.EvidenceDir); err != nil {
		t.Fatal(err)
	}
}

func TestWaitDesktopStartupReturnsRecordedFailureWithoutWaitingForTimeout(t *testing.T) {
	layout, err := prepareLayout(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	electron := exec.Command("sleep", "30")
	electron.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := electron.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-electron.Process.Pid, syscall.SIGKILL)
		_, _ = electron.Process.Wait()
	})
	if err := writeJSON(layout.StartupFile, map[string]any{
		"schemaVersion": 1,
		"stage":         "FAILED",
		"failure": map[string]string{
			"stage": "HUMAN_AUTHORITY_STARTING", "code": "human_authority_start_failed",
			"message": "channel failed", "reason": "stack: permission denied",
		},
	}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, _, err = waitDesktopStartup(layout, electron, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "HUMAN_AUTHORITY_STARTING") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("wait error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("recorded failure took %s instead of returning immediately", elapsed)
	}
}

func TestWaitDesktopStartupReportsEarlyElectronExitAndLastStage(t *testing.T) {
	layout, err := prepareLayout(filepath.Join(t.TempDir(), "run"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.EvidenceDir, "electron.log"), []byte("window constructor failed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(layout.StartupFile, map[string]any{
		"schemaVersion": 1,
		"stage":         "WINDOW_STARTING",
	}); err != nil {
		t.Fatal(err)
	}
	electron := exec.Command("true")
	if err := electron.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = electron.Process.Wait()
	started := time.Now()
	_, _, err = waitDesktopStartup(layout, electron, 10*time.Second)
	if err == nil || !strings.Contains(err.Error(), "WINDOW_STARTING") || !strings.Contains(err.Error(), "window constructor failed") {
		t.Fatalf("wait error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("early exit took %s instead of returning immediately", elapsed)
	}
}

func TestTCPPortLoopbackInspectionRejectsWildcardListener(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/net/tcp inspection is Linux-specific")
	}
	loopback, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loopback.Close() }()
	loopbackPort := loopback.Addr().(*net.TCPAddr).Port
	ok, err := tcpPortIsLoopbackOnly(loopbackPort)
	if err != nil || !ok {
		t.Fatalf("loopback listener ok=%t err=%v", ok, err)
	}

	wildcard, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wildcard.Close() }()
	wildcardPort := wildcard.Addr().(*net.TCPAddr).Port
	ok, err = tcpPortIsLoopbackOnly(wildcardPort)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wildcard listener was accepted as loopback-only")
	}
}

func TestInspectPackagedRuntimeRequiresCleanBuildSourceAndExecutableModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode check is Unix-specific")
	}
	_, electron, resources := writePackagedRuntimeFixture(t)
	got, err := inspectPackagedRuntime(Options{ElectronBin: electron})
	if err != nil {
		t.Fatal(err)
	}
	if got.BuildSource.CandidateCommit != strings.Repeat("a", 40) || len(got.RuntimeSHA256) != 3 {
		t.Fatalf("packaged evidence = %+v", got)
	}
	if err := os.Chmod(filepath.Join(resources, "agent-browser", "agent-browser"), 0o744); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectPackagedRuntime(Options{ElectronBin: electron}); err == nil || !strings.Contains(err.Error(), "mode is 744") {
		t.Fatalf("wrong packaged mode error = %v", err)
	}
}

func TestInspectPackagedRuntimeRejectsLinkedOrNonRegularRuntimeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic-link fixtures are Unix-specific")
	}
	t.Run("target symbolic link", func(t *testing.T) {
		_, electron, resources := writePackagedRuntimeFixture(t)
		daemon := filepath.Join(resources, "daemon", "ao")
		external := filepath.Join(t.TempDir(), "outside-daemon")
		if err := os.WriteFile(external, []byte("external"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(daemon); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, daemon); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackagedRuntime(Options{ElectronBin: electron}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("target symbolic-link error = %v", err)
		}
	})

	t.Run("parent link escapes package", func(t *testing.T) {
		_, electron, resources := writePackagedRuntimeFixture(t)
		browserDir := filepath.Join(resources, "agent-browser")
		externalDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(externalDir, "agent-browser"), []byte("external"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(browserDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(externalDir, browserDir); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackagedRuntime(Options{ElectronBin: electron}); err == nil || !strings.Contains(err.Error(), "outside package root") {
			t.Fatalf("parent symbolic-link escape error = %v", err)
		}
	})

	t.Run("target is not regular", func(t *testing.T) {
		_, electron, resources := writePackagedRuntimeFixture(t)
		daemon := filepath.Join(resources, "daemon", "ao")
		if err := os.Remove(daemon); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(daemon, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := inspectPackagedRuntime(Options{ElectronBin: electron}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("non-regular target error = %v", err)
		}
	})
}

func writePackagedRuntimeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	electron := filepath.Join(root, "agent-orchestrator")
	resources := filepath.Join(root, "resources")
	for _, file := range []string{
		electron,
		filepath.Join(resources, "daemon", "ao"),
		filepath.Join(resources, "agent-browser", "agent-browser"),
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(filepath.Base(file)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	buildSource := BuildSource{
		SchemaVersion: 1, CandidateCommit: strings.Repeat("a", 40), SourceClean: true,
		SourceScope: []string{"backend", "frontend"}, BuiltAt: "2026-08-29T00:00:00Z",
	}
	if err := writeJSON(filepath.Join(resources, buildSourceFileName), buildSource); err != nil {
		t.Fatal(err)
	}
	return root, electron, resources
}

func TestVisibleVNCViewerLive(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CLEARDEV_VISIBLE_TEST")) != "1" {
		t.Skip("set CLEARDEV_VISIBLE_TEST=1 to open the packaged app through loopback VNC")
	}
	if runtime.GOOS != "linux" {
		t.Skip("the visible VNC gate is Linux-specific")
	}
	electronBinary := strings.TrimSpace(os.Getenv("AO_ELECTRON_BIN"))
	if electronBinary == "" {
		t.Fatal("AO_ELECTRON_BIN is required")
	}
	hostDisplay := strings.TrimSpace(os.Getenv("DISPLAY"))
	if hostDisplay == "" {
		t.Fatal("DISPLAY is required")
	}
	layout, err := prepareLayout(filepath.Join(t.TempDir(), "visible"))
	if err != nil {
		t.Fatal(err)
	}
	group := &ProcessGroup{}
	t.Cleanup(group.Kill)
	display, xvfb, err := startXvfb(layout.EvidenceDir)
	if err != nil {
		t.Fatal(err)
	}
	group.add(xvfb.Process.Pid)
	vnc, err := startVNC(layout, display)
	if err != nil {
		t.Fatal(err)
	}
	group.add(vnc.Process.Pid)
	viewer, viewerWindow, _, err := startVisibleViewer(Options{}, layout, hostDisplay)
	if err != nil {
		t.Fatal(err)
	}
	group.add(viewer.Process.Pid)
	electron, err := startPackagedElectron(Options{ElectronBin: electronBinary}, layout, display, layout.EvidenceDir, layout.DebugPort)
	if err != nil {
		t.Fatal(err)
	}
	group.add(electron.Process.Pid)
	daemonPID, daemonPort, err := waitDesktopStartup(layout, electron, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	group.add(daemonPID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := waitDaemonHTTP(ctx, daemonPort, daemonPID, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitLargeWindow(display, electron.Process.Pid, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	_, _ = xdotool(hostDisplay, "windowactivate", "--sync", viewerWindow.ID)
	time.Sleep(500 * time.Millisecond)
	screenshot := filepath.Join(layout.EvidenceDir, "visible-live")
	if err := screenshotDesktop(display, screenshot); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(screenshot + ".xwd"); err != nil || info.Size() < 1024 {
		t.Fatalf("visible screenshot info=%v err=%v", info, err)
	}
	group.Kill()
	if !waitPortsClosed([]int{layout.Port, layout.DebugPort, layout.VNCPort}, 5*time.Second) {
		t.Fatal("visible live test left a listener")
	}
}

func TestPackagedElectronUsesIndependentSession(t *testing.T) {
	attributes := packagedElectronProcessAttributes()
	if !attributes.Setsid {
		t.Fatal("packaged Electron must start in a new session so its login-shell probe cannot be stopped by terminal job control")
	}
	if attributes.Setpgid {
		t.Fatal("Setsid already creates the managed process group; Setpgid must not be combined with it")
	}
}

func TestCurrentVerifiedCandidateSHAPrefersLatestRound(t *testing.T) {
	exec := &core.ComplexExecutionSnapshot{
		Verifications: []core.ComplexExecutionVerification{
			{ComplexExecutionTaskID: "task", Round: 0, CandidateCommitSHA: strings.Repeat("a", 40)},
			{ComplexExecutionTaskID: "task", Round: 1, CandidateCommitSHA: strings.Repeat("c", 40)},
			{ComplexExecutionTaskID: "other", Round: 9, CandidateCommitSHA: strings.Repeat("d", 40)},
		},
	}
	if got := currentVerifiedCandidateSHA(exec, "task"); got != strings.Repeat("c", 40) {
		t.Fatalf("got %s", got)
	}
}

func TestCompactTasksRecordsRequiredCheckIDs(t *testing.T) {
	exec := &core.ComplexExecutionSnapshot{
		Tasks: []core.ComplexExecutionTask{
			{ID: "task-api", TaskKey: "api", Status: core.DevelopmentTaskStatusDone},
		},
		Verifications: []core.ComplexExecutionVerification{
			{ComplexExecutionTaskID: "task-api", Round: 0, CandidateCommitSHA: strings.Repeat("c", 40)},
		},
		CheckSpecs: []core.ComplexExecutionCheckSpecFact{
			{ID: "s-api", Kind: core.CandidateCheckRequired, CheckID: "demo-api", ComplexExecutionTaskID: "task-api"},
			{ID: "s-backend", Kind: core.CandidateCheckRequired, CheckID: "demo-backend", ComplexExecutionTaskID: "task-api"},
			{ID: "s-db", Kind: core.CandidateCheckRequired, CheckID: "demo-database", ComplexExecutionTaskID: "other"},
			{ID: "s-int", Kind: core.CandidateCheckIntegration, CheckID: "demo-integration"},
			{ID: "s-dup", Kind: core.CandidateCheckRequired, CheckID: "demo-api", ComplexExecutionTaskID: "task-api"},
		},
		CheckRuns: []core.ComplexExecutionCheckRun{
			{ID: "r1", Kind: core.CandidateCheckRequired, CheckSpecFactID: "s-api", ComplexExecutionTaskID: "task-api", CandidateCommitSHA: strings.Repeat("c", 40)},
		},
	}
	tasks := compactTasks(exec)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %#v", tasks)
	}
	got := fmt.Sprint(tasks[0]["requiredCheckIds"])
	if got != "[demo-api demo-backend]" {
		t.Fatalf("requiredCheckIds = %s", got)
	}
	if catalog := catalogCheckIDs(exec, core.CandidateCheckIntegration, ""); fmt.Sprint(catalog) != "[demo-integration]" {
		t.Fatalf("integration ids = %v", catalog)
	}
	runs := compactCheckRuns(exec)
	if len(runs) != 1 || mapString(runs[0], "checkId") != "demo-api" {
		t.Fatalf("check runs = %#v", runs)
	}
}

func TestVerifyEvidenceDirAcceptsCompletePack(t *testing.T) {
	dir := t.TempDir()
	writeCompleteEvidenceDir(t, dir, nil)
	got, err := VerifyEvidenceDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != EvidenceSchemaV2 || got.Legacy {
		t.Fatalf("verify result = %+v", got)
	}
}

func TestVerifyEvidenceDirRejectsForgedAndIncompletePacks(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(dir string, evidence *Evidence)
	}{
		{"wrong PRD", func(dir string, evidence *Evidence) {
			_ = os.WriteFile(filepath.Join(dir, "prd.md"), []byte("not the frozen prd\n"), 0o600)
			evidence.InputSHA256["prd.md"] = mustSHA256File(t, filepath.Join(dir, "prd.md"))
		}},
		{"all-zero combination", func(_ string, evidence *Evidence) {
			evidence.IntegrationSHA = strings.Repeat("0", 40)
			for _, run := range evidence.CheckRuns {
				if fmt.Sprint(run["kind"]) == "INTEGRATION" {
					run["candidateCommitSha"] = evidence.IntegrationSHA
				}
			}
		}},
		{"missing tasks", func(_ string, evidence *Evidence) { evidence.Tasks = nil }},
		{"missing checks", func(_ string, evidence *Evidence) { evidence.CheckRuns = nil; evidence.Usage["checkRuns"] = 0 }},
		{"missing reviews", func(_ string, evidence *Evidence) { evidence.Reviews = nil; evidence.Usage["reviews"] = 0 }},
		{"missing explanation", func(_ string, evidence *Evidence) { evidence.ExplanationSHA256 = "" }},
		{"missing usage", func(_ string, evidence *Evidence) { evidence.Usage = nil }},
		{"missing sample page", func(dir string, _ *Evidence) {
			_ = os.Remove(filepath.Join(dir, "sample-page.json"))
		}},
		{"mismatched usage", func(_ string, evidence *Evidence) { evidence.Usage["reviews"] = 99 }},
		{"rebound required check", func(_ string, evidence *Evidence) {
			fake := strings.Repeat("f", 40)
			for _, run := range evidence.CheckRuns {
				if fmt.Sprint(run["kind"]) == "REQUIRED_CHECK" && fmt.Sprint(run["complexExecutionTaskId"]) == "task-api" {
					run["candidateCommitSha"] = fake
				}
			}
		}},
		{"rebound one of multiple required checks", func(_ string, evidence *Evidence) {
			fake := strings.Repeat("f", 40)
			for _, run := range evidence.CheckRuns {
				if fmt.Sprint(run["kind"]) == "REQUIRED_CHECK" && fmt.Sprint(run["checkId"]) == "demo-api" {
					run["candidateCommitSha"] = fake
				}
			}
		}},
		{"mismatched review", func(_ string, evidence *Evidence) {
			evidence.Reviews[1]["candidateCommitSha"] = strings.Repeat("f", 40)
		}},
		{"mismatched template commit", func(dir string, _ *Evidence) {
			_ = os.WriteFile(filepath.Join(dir, "template-commit.txt"), []byte(strings.Repeat("f", 40)+"\n"), 0o600)
		}},
		{"forged requirement hash", func(_ string, evidence *Evidence) {
			evidence.RequirementSHA256 = strings.Repeat("a", 64)
		}},
		{"wrong plan review verdict", func(_ string, evidence *Evidence) {
			evidence.PlanReviewVerdict = string(core.PlanReviewReplan)
		}},
		{"wrong task set version", func(_ string, evidence *Evidence) { evidence.TaskSetVersion = 99 }},
		{"empty specialist status", func(_ string, evidence *Evidence) {
			evidence.SpecialistChecks[0]["status"] = ""
		}},
		{"rebound requirement version", func(_ string, evidence *Evidence) {
			evidence.RequirementVersionID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		}},
		{"rebound plan id", func(_ string, evidence *Evidence) {
			evidence.PlanID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		}},
		{"rebound plan review id", func(_ string, evidence *Evidence) {
			evidence.PlanReviewID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
		}},
		{"missing review body", func(dir string, _ *Evidence) {
			_ = os.Remove(filepath.Join(dir, evidenceReviewFile))
		}},
		{"rebound build candidate", func(_ string, evidence *Evidence) {
			evidence.BuildCandidateCommit = strings.Repeat("9", 40)
		}},
		{"missing packaged runtime hash", func(_ string, evidence *Evidence) {
			delete(evidence.RuntimeSHA256, "agent-browser")
		}},
		{"missing desktop startup", func(dir string, _ *Evidence) {
			_ = os.Remove(filepath.Join(dir, evidenceStartupFile))
		}},
		{"missing visible desktop", func(dir string, _ *Evidence) {
			_ = os.Remove(filepath.Join(dir, "visible-desktop.xwd"))
		}},
		{"missing cleanup", func(dir string, _ *Evidence) {
			_ = os.Remove(filepath.Join(dir, evidenceCleanupFile))
		}},
		{"invalid startup timestamp", func(dir string, evidence *Evidence) {
			evidence.StartupSHA256 = rewriteEvidenceJSON(t, filepath.Join(dir, evidenceStartupFile), func(value map[string]any) {
				events := value["events"].([]any)
				events[0].(map[string]any)["timestamp"] = "not-a-time"
			})
		}},
		{"wildcard VNC evidence", func(dir string, evidence *Evidence) {
			evidence.VisibilitySHA256 = rewriteEvidenceJSON(t, filepath.Join(dir, evidenceVisibilityFile), func(value map[string]any) {
				value["vncAddress"] = "0.0.0.0:5909"
			})
		}},
		{"reported cleanup failure", func(dir string, evidence *Evidence) {
			evidence.CleanupSHA256 = rewriteEvidenceJSON(t, filepath.Join(dir, evidenceCleanupFile), func(value map[string]any) {
				value["failureReason"] = "listener remained open"
			})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCompleteEvidenceDir(t, dir, tc.mutate)
			if _, err := VerifyEvidenceDir(dir); err == nil {
				t.Fatalf("accepted %s", tc.name)
			}
		})
	}
}

func TestBrowserImportScriptSubmitsFrozenSample(t *testing.T) {
	script := browserImportScript()
	if !strings.Contains(script, "user@example.com") || !strings.Contains(script, "/api/import") || !strings.Contains(script, "import-form") {
		t.Fatalf("browser import script = %s", script)
	}
}

func TestSubmitSampleOnMatchingTargetTalksToPageCDP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]cdpTarget{{
			Type:                 "page",
			URL:                  "http://127.0.0.1:9/",
			WebSocketDebuggerURL: fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/1", port),
		}})
	})
	mux.HandleFunc("/devtools/page/1", func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if acceptErr != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
			ctx := context.Background()
			for {
				var msg struct {
					ID     int64          `json:"id"`
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if readErr := wsjson.Read(ctx, conn, &msg); readErr != nil {
					return
				}
				result := map[string]any{}
				if msg.Method == "Runtime.evaluate" {
					expr, _ := msg.Params["expression"].(string)
					if await, _ := msg.Params["awaitPromise"].(bool); await {
						result = map[string]any{"result": map[string]any{"value": `{"api":{"accepted":2,"rejected":1,"duplicates":2},"page":{"accepted":2,"rejected":1,"duplicates":2}}`}}
					} else if strings.Contains(expr, "import-form") {
						result = map[string]any{"result": map[string]any{"value": "yes"}}
					}
				}
				if writeErr := wsjson.Write(ctx, conn, map[string]any{"id": msg.ID, "result": result}); writeErr != nil {
					return
				}
			}
		}()
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	page, api, err := submitSampleOnMatchingTarget(ctx, port, "http://127.0.0.1:9/", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !sampleCountsMatch(page) || !sampleCountsMatch(api) {
		t.Fatalf("page=%#v api=%#v", page, api)
	}
}

func writeCompleteEvidenceDir(t *testing.T, dir string, mutate func(dir string, evidence *Evidence)) {
	t.Helper()
	prd, err := core.FrozenDemoPRD()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prd.md"), []byte(prd), 0o600); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(prd, "\n") {
		if err := os.WriteFile(filepath.Join(dir, "prd.md"), []byte(prd+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("c", 40)
	combo := strings.Repeat("d", 40)
	template := strings.Repeat("e", 40)
	reqBody := `{"requirement":"demo-mail-list"}`
	if err := os.WriteFile(filepath.Join(dir, evidenceRequirementFile), []byte(reqBody), 0o600); err != nil {
		t.Fatal(err)
	}
	reqHash := sha256String(reqBody)
	versionID := "22222222-2222-2222-2222-222222222222"
	planID := "33333333-3333-3333-3333-333333333333"
	reviewID := "44444444-4444-4444-4444-444444444444"
	planRaw, err := json.Marshal(map[string]any{
		"requirementVersionId":     versionID,
		"requirementVersionSha256": reqHash,
		"tasks":                    []any{map[string]any{"key": "api"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, evidencePlanFile), planRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	planHash := sha256String(string(planRaw))
	if err := writeJSON(filepath.Join(dir, evidenceReviewFile), boundReviewIdentifiers{
		ID:         reviewID,
		PlanID:     planID,
		PlanSHA256: planHash,
		Verdict:    string(core.PlanReviewApproved),
	}); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("b", 64)
	buildCandidate := strings.Repeat("f", 40)
	buildSource := BuildSource{
		SchemaVersion:   1,
		CandidateCommit: buildCandidate,
		SourceClean:     true,
		SourceScope:     []string{"backend", "frontend", "package.json"},
		BuiltAt:         "2026-08-29T00:00:00Z",
	}
	if err := writeJSON(filepath.Join(dir, buildSourceFileName), buildSource); err != nil {
		t.Fatal(err)
	}
	startup := map[string]any{
		"schemaVersion":       1,
		"stage":               "DAEMON_READY",
		"lastSuccessfulStage": "DAEMON_READY",
		"events": []map[string]any{
			{"stage": "APP_READY", "timestamp": "2026-08-29T00:00:00.000Z"},
			{"stage": "WINDOW_STARTING", "timestamp": "2026-08-29T00:00:00.100Z"},
			{"stage": "WINDOW_READY", "timestamp": "2026-08-29T00:00:00.200Z"},
			{"stage": "DAEMON_STARTING", "timestamp": "2026-08-29T00:00:00.300Z"},
			{"stage": "HUMAN_AUTHORITY_STARTING", "timestamp": "2026-08-29T00:00:00.400Z"},
			{"stage": "HUMAN_AUTHORITY_READY", "timestamp": "2026-08-29T00:00:00.500Z"},
			{"stage": "DAEMON_STARTED", "timestamp": "2026-08-29T00:00:00.600Z"},
			{"stage": "DAEMON_READY", "timestamp": "2026-08-29T00:00:00.700Z"},
		},
	}
	if err := writeJSON(filepath.Join(dir, evidenceStartupFile), startup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visible-desktop.xwd"), []byte("visible desktop pixels"), 0o600); err != nil {
		t.Fatal(err)
	}
	visibility := VisibilityEvidence{
		SchemaVersion:      1,
		HostDisplay:        ":0",
		VNCAddress:         "127.0.0.1:5909",
		PasswordProtected:  true,
		ViewerExecutable:   "/usr/bin/vncviewer",
		ViewerWindowWidth:  1280,
		ViewerWindowHeight: 800,
		ScreenshotSource:   "vnc-streamed-xvfb-root",
		ScreenshotSHA256:   mustSHA256File(t, filepath.Join(dir, "visible-desktop.xwd")),
	}
	if err := writeJSON(filepath.Join(dir, evidenceVisibilityFile), visibility); err != nil {
		t.Fatal(err)
	}
	cleanup := CleanupEvidence{
		SchemaVersion:       1,
		ProcessesStopped:    true,
		PortsClosed:         true,
		WorkDirectoriesGone: true,
		CheckedPorts:        []int{3001, 9222, 4173, 5909},
	}
	if err := writeJSON(filepath.Join(dir, evidenceCleanupFile), cleanup); err != nil {
		t.Fatal(err)
	}
	evidence := Evidence{
		SchemaVersion:        EvidenceSchemaV2,
		RequirementID:        "11111111-1111-1111-1111-111111111111",
		RequirementVersionID: versionID,
		RequirementSHA256:    reqHash,
		PlanID:               planID,
		PlanSHA256:           planHash,
		PlanReviewID:         reviewID,
		PlanReviewVerdict:    string(core.PlanReviewApproved),
		TaskSetVersion:       core.ComplexStandardTaskSetVersion,
		Mode:                 "PARALLEL",
		FixedBuilderCount:    2,
		ProgressPhase:        "COMPLETED",
		TemplateCommit:       template,
		IntegrationSHA:       combo,
		IntegrationCheckIDs:  []string{"demo-integration"},
		ExplanationSHA256:    hash,
		Sample:               map[string]int{"accepted": 2, "rejected": 1, "duplicates": 2},
		RoleSessions:         map[string]string{"STEWARD": "repo-1"},
		Tasks: []map[string]any{
			{"id": "task-schema", "status": "DONE", "taskKey": "schema", "currentCandidateCommitSha": sha, "requiredCheckIds": []string{"demo-database"}},
			{"id": "task-api", "status": "DONE", "taskKey": "api", "currentCandidateCommitSha": other, "requiredCheckIds": []string{"demo-backend", "demo-api"}},
		},
		CheckRuns: []map[string]any{
			{"kind": "SCOPE", "status": "SETTLED", "result": "PASS", "complexExecutionTaskId": "task-schema", "candidateCommitSha": sha},
			{"kind": "REQUIRED_CHECK", "status": "SETTLED", "result": "PASS", "checkId": "demo-database", "complexExecutionTaskId": "task-schema", "candidateCommitSha": sha},
			{"kind": "SCOPE", "status": "SETTLED", "result": "PASS", "complexExecutionTaskId": "task-api", "candidateCommitSha": other},
			{"kind": "REQUIRED_CHECK", "status": "SETTLED", "result": "PASS", "checkId": "demo-backend", "complexExecutionTaskId": "task-api", "candidateCommitSha": other},
			{"kind": "REQUIRED_CHECK", "status": "SETTLED", "result": "PASS", "checkId": "demo-api", "complexExecutionTaskId": "task-api", "candidateCommitSha": other},
			{"kind": "INTEGRATION", "status": "SETTLED", "result": "PASS", "checkId": "demo-integration", "candidateCommitSha": combo},
		},
		Reviews: []map[string]any{
			{"status": "SETTLED", "verdict": "PASS", "complexExecutionTaskId": "task-schema", "candidateCommitSha": sha},
			{"status": "SETTLED", "verdict": "PASS", "complexExecutionTaskId": "task-api", "candidateCommitSha": other},
		},
		SpecialistChecks:     []map[string]any{{"checkId": "sqlite-migration-specialist", "result": "PASS", "status": "SETTLED"}},
		Usage:                map[string]any{"agentSteps": 4, "checkRuns": 6, "reviews": 2},
		InputSHA256:          map[string]string{"prd.md": mustSHA256File(t, filepath.Join(dir, "prd.md"))},
		BuildCandidateCommit: buildCandidate,
		BuildSourceClean:     true,
		BuildSourceSHA256:    mustSHA256File(t, filepath.Join(dir, buildSourceFileName)),
		RuntimeSHA256: map[string]string{
			"electron":      hash,
			"daemon":        hash,
			"agent-browser": hash,
		},
		StartupSHA256:    mustSHA256File(t, filepath.Join(dir, evidenceStartupFile)),
		VisibilitySHA256: mustSHA256File(t, filepath.Join(dir, evidenceVisibilityFile)),
		CleanupSHA256:    mustSHA256File(t, filepath.Join(dir, evidenceCleanupFile)),
	}
	sample, err := json.Marshal(FrozenSampleCounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample-api.json"), append(sample, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sample-page.json"), append(sample, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "template-commit.txt"), []byte(template+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(dir, &evidence)
	}
	if err := writeJSON(filepath.Join(dir, "evidence.json"), evidence); err != nil {
		t.Fatal(err)
	}
}

func mustSHA256File(t *testing.T, path string) string {
	t.Helper()
	sum, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func rewriteEvidenceJSON(t *testing.T, path string, mutate func(map[string]any)) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	mutate(value)
	if err := writeJSON(path, value); err != nil {
		t.Fatal(err)
	}
	return mustSHA256File(t, path)
}

func TestVerifyEvidenceDirRejectsWrongMode(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "prd.md"), []byte("prd\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "template-commit.txt"), []byte("abc\n"), 0o600)
	evidence := Evidence{SchemaVersion: 1, RequirementID: "req", RequirementVersionID: "ver", PlanID: "plan", Mode: "STANDARD", FixedBuilderCount: 1, ProgressPhase: "COMPLETED", IntegrationSHA: "0123456789abcdef0123456789abcdef01234567", Sample: FrozenSampleCounts}
	raw, _ := json.Marshal(evidence)
	_ = os.WriteFile(filepath.Join(dir, "evidence.json"), raw, 0o600)
	if _, err := VerifyEvidenceDir(dir); err == nil {
		t.Fatal("expected standard mode to fail verification")
	}
}

func TestVerifyEvidenceDirAcceptsLegacyV1Pack(t *testing.T) {
	dir := t.TempDir()
	writeCompleteEvidenceDir(t, dir, func(_ string, evidence *Evidence) {
		evidence.SchemaVersion = EvidenceSchemaV1
		evidence.TaskSetVersion = 0
		evidence.PlanReviewVerdict = ""
	})
	_ = os.Remove(filepath.Join(dir, evidenceRequirementFile))
	_ = os.Remove(filepath.Join(dir, evidencePlanFile))
	got, err := VerifyEvidenceDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Legacy || got.SchemaVersion != EvidenceSchemaV1 {
		t.Fatalf("legacy result = %+v", got)
	}
}

func TestAttachProcessLogClosesParentFile(t *testing.T) {
	dir := t.TempDir()
	before := openFDCount(t)
	var children []*exec.Cmd
	t.Cleanup(func() {
		for _, child := range children {
			if child.Process != nil {
				_ = child.Process.Kill()
				_, _ = child.Process.Wait()
			}
		}
	})
	for i := 0; i < 8; i++ {
		logFile, err := os.Create(filepath.Join(dir, fmt.Sprintf("proc-%d.log", i)))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sleep", "30")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := attachProcessLog(cmd, logFile); err != nil {
			t.Fatal(err)
		}
		children = append(children, cmd)
		if err := logFile.Close(); err == nil {
			t.Fatal("parent still held the log file after a successful start")
		}
	}
	for _, child := range children {
		if child.Process != nil {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		}
	}
	children = nil
	after := openFDCount(t)
	if after > before+2 {
		t.Fatalf("parent FDs grew from %d to %d after repeated starts", before, after)
	}

	failLog, err := os.Create(filepath.Join(dir, "fail.log"))
	if err != nil {
		t.Fatal(err)
	}
	missing := exec.Command(filepath.Join(dir, "missing-bin"))
	if err := attachProcessLog(missing, failLog); err == nil {
		t.Fatal("expected missing binary to fail")
	}
	if err := failLog.Close(); err == nil {
		t.Fatal("parent still held the log file after a failed start")
	}
}

func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestProcessGroupKillsOnlyTrackedPIDs(t *testing.T) {
	tracked := exec.Command("sleep", "30")
	tracked.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := tracked.Start(); err != nil {
		t.Fatal(err)
	}
	other := exec.Command("sleep", "30")
	other.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-tracked.Process.Pid, syscall.SIGKILL)
		_, _ = tracked.Process.Wait()
		_ = syscall.Kill(-other.Process.Pid, syscall.SIGKILL)
		_, _ = other.Process.Wait()
	})
	group := &ProcessGroup{}
	group.add(tracked.Process.Pid)
	group.Kill()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && processAlive(tracked.Process.Pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(tracked.Process.Pid) {
		t.Fatal("tracked process is still alive")
	}
	if !processAlive(other.Process.Pid) {
		t.Fatal("cleanup killed an unrelated process")
	}
}

func TestClarificationAnswersCoverDuplicateAndReject(t *testing.T) {
	view := cleardevsvc.RequirementView{
		ComplexPlanning: &cleardevsvc.ComplexPlanningView{
			CompilationRequests: []core.ComplexCompilationRequest{{ID: "compile-1", ClarificationRound: 0}},
			Questions: []core.ComplexClarificationQuestion{
				{CompilationRequestID: "compile-1", QuestionKey: "invalid-email", Text: "格式无效的地址是否计入拒绝数量？"},
				{CompilationRequestID: "compile-1", QuestionKey: "duplicate-email", Text: "重复地址是计入拒绝数量，还是忽略不计？"},
			},
		},
	}
	answers, err := clarificationAnswers(view)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := answers["answers"].([]map[string]string)
	if len(list) != 2 {
		t.Fatalf("answers = %#v", answers)
	}
	if list[0]["text"] != "计入拒绝数量。" || list[1]["text"] != "重复地址忽略不计，只保留第一次出现的记录。" {
		t.Fatalf("frozen answers = %#v", list)
	}
}

func TestClarificationAnswersRejectUnknownQuestion(t *testing.T) {
	view := cleardevsvc.RequirementView{
		ComplexPlanning: &cleardevsvc.ComplexPlanningView{
			CompilationRequests: []core.ComplexCompilationRequest{{ID: "compile-1"}},
			Questions: []core.ComplexClarificationQuestion{
				{CompilationRequestID: "compile-1", QuestionKey: "need-login", Text: "是否需要账号登录？"},
			},
		},
	}
	if _, err := clarificationAnswers(view); err == nil {
		t.Fatal("expected unknown clarification to fail")
	}
}

func TestCloseHTTPResponseIgnoresNilBody(t *testing.T) {
	closeHTTPResponse(nil)
	closeHTTPResponse(&http.Response{})
	closeHTTPResponse(&http.Response{Body: http.NoBody})
}

func TestPreferredCDPTargetsSkipsEmptyDebuggerURL(t *testing.T) {
	got := preferredCDPTargets([]cdpTarget{
		{Type: "page", URL: "app://./index.html", WebSocketDebuggerURL: ""},
		{Type: "page", URL: "app://./index.html", WebSocketDebuggerURL: "ws://127.0.0.1:9/devtools/page/1"},
		{Type: "iframe", URL: "app://./index.html", WebSocketDebuggerURL: "ws://127.0.0.1:9/devtools/page/2"},
	})
	if len(got) != 1 || got[0].WebSocketDebuggerURL != "ws://127.0.0.1:9/devtools/page/1" {
		t.Fatalf("preferred = %#v", got)
	}
}

func TestBrowserPreviewSessionIDPrefersWorker(t *testing.T) {
	got := browserPreviewSessionID(map[string]string{
		"STEWARD":   "repo-1",
		"PLANNER":   "repo-2",
		"BUILDER-1": "repo-3",
	})
	if got != "repo-3" {
		t.Fatalf("got %q, want builder session", got)
	}
}

func TestSubmitFrozenSampleInBrowserLive(t *testing.T) {
	debug := strings.TrimSpace(os.Getenv("CLEARDEV_LIVE_DEBUG_PORT"))
	app := strings.TrimSpace(os.Getenv("CLEARDEV_LIVE_APP_PORT"))
	if debug == "" || app == "" {
		t.Skip("live browser ports are not set")
	}
	debugPort, err := strconv.Atoi(debug)
	if err != nil {
		t.Fatal(err)
	}
	appPort, err := strconv.Atoi(app)
	if err != nil {
		t.Fatal(err)
	}
	previewAPI := strings.TrimSpace(os.Getenv("CLEARDEV_LIVE_PREVIEW_API"))
	open := sampleBrowserOpen{
		ProjectID: strings.TrimSpace(os.Getenv("CLEARDEV_LIVE_PROJECT_ID")),
		SessionID: strings.TrimSpace(os.Getenv("CLEARDEV_LIVE_SESSION_ID")),
	}
	if previewAPI != "" {
		open.Preview = func(ctx context.Context, appURL string) error {
			body := strings.NewReader(`{"url":` + strconv.Quote(appURL) + `}`)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, previewAPI, body)
			if err != nil {
				return err
			}
			req.Header.Set("content-type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK {
				return fmt.Errorf("preview status %d", res.StatusCode)
			}
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	page, api, err := submitFrozenSampleInBrowser(ctx, debugPort, appPort, open)
	if err != nil {
		t.Fatal(err)
	}
	if !sampleCountsMatch(page) || !sampleCountsMatch(api) {
		t.Fatalf("page=%#v api=%#v", page, api)
	}
}

func TestCheckoutCommitExtractsExactTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("from-commit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "ClearDev Demo"},
		{"config", "user.email", "cleardev-demo@example.invalid"},
		{"add", "-A"},
		{"commit", "-m", "marker"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	sha, err := gitRevParse(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("dirty-worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "final")
	if err := checkoutCommit(repo, sha, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "marker.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "from-commit\n" {
		t.Fatalf("checked out %q, want the committed tree", got)
	}
}
