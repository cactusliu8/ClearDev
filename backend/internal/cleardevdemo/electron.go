package cleardevdemo

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
)

type xWindow struct {
	ID      string
	Name    string
	PID     int
	Visible bool
	Width   int
	Height  int
}

type nativeDialogOps struct {
	now               func() time.Time
	sleep             func(time.Duration)
	inspect           func(string, ...int) []xWindow
	screenshotWindow  func(string, string, string) error
	screenshotDesktop func(string, string) error
	xdotool           func(string, ...string) (string, error)
}

func overlayEnv(overrides map[string]string) []string {
	return overlayEnvWithout(overrides)
}

func overlayEnvWithout(overrides map[string]string, excluded ...string) []string {
	seen := map[string]bool{}
	exclude := map[string]bool{}
	for _, key := range excluded {
		exclude[key] = true
	}
	out := make([]string, 0, len(os.Environ())+len(overrides))
	for key, value := range overrides {
		if exclude[key] {
			continue
		}
		out = append(out, key+"="+value)
		seen[key] = true
	}
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || seen[key] || exclude[key] {
			continue
		}
		out = append(out, item)
	}
	return out
}

func startXvfb(evidenceDir string) (display string, cmd *exec.Cmd, err error) {
	for n := 21; n <= 99; n++ {
		display = fmt.Sprintf(":%d", n)
		if _, statErr := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n)); statErr == nil {
			continue
		}
		logFile, createErr := os.Create(filepath.Join(evidenceDir, fmt.Sprintf("xvfb-%d.log", n)))
		if createErr != nil {
			return "", nil, createErr
		}
		proc := exec.Command("Xvfb", display, "-screen", "0", "1280x800x24", "-ac", "-nolisten", "tcp")
		proc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if startErr := attachProcessLog(proc, logFile); startErr != nil {
			continue
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if !processAlive(proc.Process.Pid) {
				break
			}
			if _, displayErr := xdotool(display, "getmouselocation", "--shell"); displayErr == nil {
				return display, proc, nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = proc.Process.Kill()
		_, _ = proc.Process.Wait()
	}
	return "", nil, fmt.Errorf("could not start Xvfb")
}

func startVNC(layout Layout, display string) (*exec.Cmd, error) {
	passwordRaw := make([]byte, 6)
	if _, err := rand.Read(passwordRaw); err != nil {
		return nil, fmt.Errorf("generate VNC password: %w", err)
	}
	password := base64.RawURLEncoding.EncodeToString(passwordRaw)
	store := exec.Command("x11vnc", "-storepasswd", password, layout.VNCAuthFile)
	store.Env = overlayEnv(map[string]string{"HOME": layout.HomeDir})
	if output, err := store.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("create one-time VNC password: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := os.Chmod(layout.VNCAuthFile, 0o600); err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(layout.EvidenceDir, "vnc-server.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(
		"x11vnc",
		"-display", display,
		"-listen", "127.0.0.1",
		"-no6",
		"-rfbport", strconv.Itoa(layout.VNCPort),
		"-rfbauth", layout.VNCAuthFile,
		"-forever",
		"-shared",
		"-noxdamage",
		"-norc",
		"-safer",
	)
	cmd.Env = overlayEnvWithout(
		map[string]string{"DISPLAY": display, "HOME": layout.HomeDir},
		"WAYLAND_DISPLAY",
		"XDG_SESSION_TYPE",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := attachProcessLog(cmd, logFile); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(cmd.Process.Pid) {
			return nil, fmt.Errorf(
				"x11vnc exited before listening: %s",
				readDiagnosticTail(filepath.Join(layout.EvidenceDir, "vnc-server.log")),
			)
		}
		conn, dialErr := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", layout.VNCPort), 200*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			loopbackOnly, inspectErr := tcpPortIsLoopbackOnly(layout.VNCPort)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if !loopbackOnly {
				return nil, fmt.Errorf("VNC port %d is not bound only to 127.0.0.1", layout.VNCPort)
			}
			return cmd, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("x11vnc did not listen on 127.0.0.1:%d", layout.VNCPort)
}

func startVisibleViewer(opts Options, layout Layout, hostDisplay string) (*exec.Cmd, xWindow, string, error) {
	viewer, err := opts.lookPath("vncviewer")
	if err != nil {
		return nil, xWindow{}, "", err
	}
	before := listedWindows(hostDisplay)
	logFile, err := os.Create(filepath.Join(layout.EvidenceDir, "vnc-viewer.log"))
	if err != nil {
		return nil, xWindow{}, "", err
	}
	cmd := exec.Command( //nolint:gosec // executable is resolved by exec.LookPath; all arguments are separate fixed-purpose values
		viewer,
		"-display", hostDisplay,
		"-ViewOnly",
		"-Shared",
		"-RemoteResize=0",
		"-ReconnectOnError=0",
		"-AlertOnFatalError=0",
		"-PasswordFile", layout.VNCAuthFile,
		fmt.Sprintf("127.0.0.1::%d", layout.VNCPort),
	)
	cmd.Env = overlayEnv(map[string]string{"DISPLAY": hostDisplay, "HOME": layout.HomeDir})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := attachProcessLog(cmd, logFile); err != nil {
		return nil, xWindow{}, "", err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(cmd.Process.Pid) {
			return nil, xWindow{}, "", fmt.Errorf("VNC viewer exited before opening a visible window; see %s", filepath.Join(layout.EvidenceDir, "vnc-viewer.log"))
		}
		visible := listedVisibleWindows(hostDisplay)
		for _, candidate := range inspectWindows(hostDisplay, cmd.Process.Pid) {
			if before[candidate.ID] || !visible[candidate.ID] || candidate.Width < 320 || candidate.Height < 240 {
				continue
			}
			return cmd, candidate, viewer, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, xWindow{}, "", fmt.Errorf("VNC viewer did not open on desktop DISPLAY=%s", hostDisplay)
}

func seedCodexHome(isolatedHome string) (string, error) {
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	src := filepath.Join(hostHome, ".codex")
	if _, err := os.Stat(filepath.Join(src, "auth.json")); err != nil {
		return "", fmt.Errorf("host Codex auth.json is missing: %w", err)
	}
	dest := filepath.Join(isolatedHome, ".codex")
	if err := os.RemoveAll(dest); err != nil {
		return "", err
	}
	if err := os.Symlink(src, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func startPackagedElectron(opts Options, layout Layout, display, evidenceDir string, debugPort int) (*exec.Cmd, error) {
	binary, err := packagedElectronBinary(opts)
	if err != nil {
		return nil, err
	}
	npmCache, err := cleardevlocal.TrustedNPMCachePath()
	if err != nil {
		return nil, fmt.Errorf("isolated Electron npm cache: %w", err)
	}
	codexHome, err := seedCodexHome(layout.HomeDir)
	if err != nil {
		return nil, err
	}
	logFile, err := os.Create(filepath.Join(evidenceDir, "electron.log"))
	if err != nil {
		return nil, err
	}
	args := []string{"--no-sandbox", "--remote-debugging-port=" + strconv.Itoa(debugPort), "--remote-allow-origins=*"}
	cmd := exec.Command(binary, args...)
	cmd.Dir = filepath.Dir(binary)
	cmd.Env = overlayEnv(map[string]string{
		"DISPLAY": display, "HOME": layout.HomeDir, "AO_DATA_DIR": layout.DataDir,
		"AO_RUN_FILE": layout.RunFile, "AO_STARTUP_RECORD_FILE": layout.StartupFile,
		"AO_PORT": strconv.Itoa(layout.Port), "CODEX_HOME": codexHome,
		"CLEARDEV_NPM_CACHE_DIR": npmCache,
		"AO_DISABLE_GPU":         "1", "AO_KEEP_DAEMON": "0", "AO_TELEMETRY_REMOTE": "off",
		"ELECTRON_DISABLE_SECURITY_WARNINGS": "1", "ELECTRON_OZONE_PLATFORM": "x11",
		"GDK_BACKEND": "x11", "LIBGL_ALWAYS_SOFTWARE": "1",
	})
	cmd.SysProcAttr = packagedElectronProcessAttributes()
	if err := attachProcessLog(cmd, logFile); err != nil {
		return nil, err
	}
	return cmd, nil
}

func packagedElectronProcessAttributes() *syscall.SysProcAttr {
	// The demo command normally owns the terminal's foreground process group.
	// Merely putting Electron in another process group leaves it attached to the
	// same controlling terminal. Its interactive login-shell probe can then stop
	// the whole Electron group through terminal job control before the daemon is
	// spawned. A new session has no controlling terminal, while its session leader
	// is still the process-group leader that ProcessGroup.Kill cleans up by PID.
	return &syscall.SysProcAttr{Setsid: true}
}

type desktopStartupFile struct {
	Stage               string `json:"stage"`
	LastSuccessfulStage string `json:"lastSuccessfulStage"`
	Failure             *struct {
		Stage   string `json:"stage"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	} `json:"failure"`
}

func waitDesktopStartup(layout Layout, electron *exec.Cmd, timeout time.Duration) (int, int, error) {
	deadline := time.Now().Add(timeout)
	lastStage := "startup record not written"
	for time.Now().Before(deadline) {
		startupRaw, startupErr := os.ReadFile(layout.StartupFile) //nolint:gosec // isolated demonstration state
		if startupErr == nil {
			var startup desktopStartupFile
			if err := json.Unmarshal(startupRaw, &startup); err == nil {
				if strings.TrimSpace(startup.Stage) != "" {
					lastStage = startup.Stage
				}
				if startup.Failure != nil {
					return 0, 0, fmt.Errorf(
						"desktop startup failed at %s [%s]: %s\n%s",
						startup.Failure.Stage,
						startup.Failure.Code,
						startup.Failure.Message,
						startup.Failure.Reason,
					)
				}
				if startup.Stage == "DAEMON_READY" {
					pid, port, err := readRunFile(layout.RunFile)
					if err == nil {
						if !processAlive(pid) {
							return 0, 0, fmt.Errorf("desktop reached DAEMON_READY but daemon pid %d is not alive", pid)
						}
						return pid, port, nil
					}
				}
			}
		}
		if electron == nil || electron.Process == nil || !processAlive(electron.Process.Pid) {
			return 0, 0, fmt.Errorf(
				"electron exited during desktop startup; last stage: %s; log: %s",
				lastStage,
				readDiagnosticTail(filepath.Join(layout.EvidenceDir, "electron.log")),
			)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, 0, fmt.Errorf(
		"desktop startup timed out after %s; last stage: %s; startup record: %s; log: %s",
		timeout,
		lastStage,
		layout.StartupFile,
		readDiagnosticTail(filepath.Join(layout.EvidenceDir, "electron.log")),
	)
}

func readRunFile(path string) (int, int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // daemon run file is inside the isolated demonstration directory
	if err != nil {
		return 0, 0, err
	}
	var info struct {
		PID  int `json:"pid"`
		Port int `json:"port"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return 0, 0, err
	}
	if info.Port <= 0 || info.PID <= 0 {
		return 0, 0, fmt.Errorf("run file has invalid pid or port")
	}
	return info.PID, info.Port, nil
}

func readDiagnosticTail(path string) string {
	raw, err := os.ReadFile(path) //nolint:gosec // isolated demonstration log
	if err != nil {
		return err.Error()
	}
	const maxBytes = 8 * 1024
	if len(raw) > maxBytes {
		raw = raw[len(raw)-maxBytes:]
	}
	return strings.TrimSpace(string(raw))
}

func tcpPortIsLoopbackOnly(port int) (bool, error) {
	wantPort := strings.ToUpper(fmt.Sprintf("%04X", port))
	found := false
	for _, procFile := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		raw, err := os.ReadFile(procFile) //nolint:gosec // kernel socket table, not user input
		if err != nil {
			return false, fmt.Errorf("inspect VNC listener %s: %w", procFile, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			address, rawPort, ok := strings.Cut(fields[1], ":")
			if !ok || !strings.EqualFold(rawPort, wantPort) {
				continue
			}
			found = true
			if procFile != "/proc/net/tcp" || !strings.EqualFold(address, "0100007F") {
				return false, nil
			}
		}
	}
	return found, nil
}

func xdotool(display string, args ...string) (string, error) {
	cmd := exec.Command("xdotool", args...)
	cmd.Env = overlayEnv(map[string]string{"DISPLAY": display})
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func collectWindowIDs(display string, pids ...int) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(raw string) {
		for _, id := range strings.Fields(raw) {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, args := range [][]string{
		{"search", "--onlyvisible", "--name", ".*"},
		{"search", "--name", ".*"},
		{"search", "--class", "electron"},
		{"search", "--class", "Electron"},
		{"search", "--class", "GtkDialog"},
	} {
		out, _ := xdotool(display, args...)
		add(out)
	}
	for _, pid := range pids {
		if pid > 0 {
			out, _ := xdotool(display, "search", "--pid", strconv.Itoa(pid), ".*")
			add(out)
		}
	}
	sort.Strings(ids)
	return ids
}

func inspectWindows(display string, pids ...int) []xWindow {
	var windows []xWindow
	visible := visibleWindowIDs(display)
	for _, id := range collectWindowIDs(display, pids...) {
		name, _ := xdotool(display, "getwindowname", id)
		pidText, _ := xdotool(display, "getwindowpid", id)
		geometry, err := xdotool(display, "getwindowgeometry", "--shell", id)
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(pidText))
		win := xWindow{ID: id, Name: strings.TrimSpace(name), PID: pid, Visible: visible[id]}
		for _, line := range strings.Split(geometry, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			number, _ := strconv.Atoi(value)
			switch key {
			case "WIDTH":
				win.Width = number
			case "HEIGHT":
				win.Height = number
			}
		}
		windows = append(windows, win)
	}
	return windows
}

func waitLargeWindow(display string, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last []xWindow
	for time.Now().Before(deadline) {
		last = inspectWindows(display, pid)
		for _, win := range last {
			if win.Width >= 800 && win.Height >= 500 {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("electron window did not become ready on DISPLAY=%s; windows=%v", display, last)
}

func listedWindows(display string, pids ...int) map[string]bool {
	out := map[string]bool{}
	for _, win := range inspectWindows(display, pids...) {
		out[win.ID] = true
	}
	return out
}

func listedVisibleWindows(display string) map[string]bool {
	return visibleWindowIDs(display)
}

func visibleWindowIDs(display string) map[string]bool {
	out := map[string]bool{}
	raw, _ := xdotool(display, "search", "--onlyvisible", "--name", ".*")
	for _, id := range strings.Fields(raw) {
		out[id] = true
	}
	return out
}

func clickNativeApprove(display, screenshotPath string, before map[string]bool, expectedTitle string, pids ...int) error {
	ops := nativeDialogOps{
		now: time.Now, sleep: time.Sleep, inspect: inspectWindows,
		screenshotWindow: screenshotWindow, screenshotDesktop: screenshotDesktop, xdotool: xdotool,
	}
	return clickNativeApproveWithOps(display, screenshotPath, before, expectedTitle, pids, 2*time.Minute, 4*time.Second, ops)
}

func clickNativeApproveWithOps(
	display, screenshotPath string,
	before map[string]bool,
	expectedTitle string,
	pids []int,
	waitTimeout, closeTimeout time.Duration,
	ops nativeDialogOps,
) error {
	if before == nil {
		before = map[string]bool{}
		for _, win := range ops.inspect(display, pids...) {
			before[win.ID] = true
		}
	}
	allowedPIDs := make(map[int]bool, len(pids))
	for _, pid := range pids {
		allowedPIDs[pid] = true
	}
	deadline := ops.now().Add(waitTimeout)
	var win xWindow
	observedByID := map[string]xWindow{}
	found := false
	for ops.now().Before(deadline) {
		for _, candidate := range ops.inspect(display, pids...) {
			if !before[candidate.ID] {
				observedByID[candidate.ID] = candidate
			}
			if nativeDialogCandidate(candidate, before, expectedTitle, allowedPIDs) {
				win = candidate
				found = true
				break
			}
		}
		if found {
			break
		}
		ops.sleep(250 * time.Millisecond)
	}
	if !found {
		observed := make([]xWindow, 0, len(observedByID))
		for _, candidate := range observedByID {
			observed = append(observed, candidate)
		}
		sort.Slice(observed, func(i, j int) bool { return observed[i].ID < observed[j].ID })
		_ = ops.screenshotDesktop(display, screenshotPath+"-missing")
		return fmt.Errorf("native confirm dialog %q did not appear for Electron pids %v; observed new windows: %+v", expectedTitle, pids, observed)
	}
	_ = ops.screenshotWindow(display, win.ID, screenshotPath)
	_, _ = ops.xdotool(display, "windowactivate", "--sync", win.ID)
	_, _ = ops.xdotool(display, "windowfocus", "--sync", win.ID)
	ops.sleep(300 * time.Millisecond)
	sequences := [][]string{
		{"key", "--window", win.ID, "--delay", "40", "Left", "Left", "Return"},
		{"key", "--window", win.ID, "--delay", "40", "ISO_Left_Tab", "ISO_Left_Tab", "Return"},
		{"key", "--window", win.ID, "Alt+A"},
	}
	var lastCloseWindows []xWindow
	for _, args := range sequences {
		_, _ = ops.xdotool(display, args...)
		closeDeadline := ops.now().Add(closeTimeout)
		for ops.now().Before(closeDeadline) {
			still := false
			lastCloseWindows = ops.inspect(display, pids...)
			for _, existing := range lastCloseWindows {
				if existing.ID == win.ID {
					still = true
					break
				}
			}
			if !still {
				return nil
			}
			ops.sleep(100 * time.Millisecond)
		}
	}
	return fmt.Errorf("native Approve click did not close dialog %s; selected window: %+v; last observed windows: %+v", win.ID, win, lastCloseWindows)
}

func nativeDialogCandidate(candidate xWindow, before map[string]bool, expectedTitle string, allowedPIDs map[int]bool) bool {
	return !before[candidate.ID] && candidate.Visible && allowedPIDs[candidate.PID] &&
		candidate.Name == expectedTitle && candidate.Width >= 200 && candidate.Height >= 80
}

func screenshotWindow(display, windowID, dest string) error {
	cmd := exec.Command("xwd", "-silent", "-id", windowID, "-out", dest+".xwd")
	cmd.Env = overlayEnv(map[string]string{"DISPLAY": display})
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xwd window %s on DISPLAY=%s: %w: %s", windowID, display, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func screenshotDesktop(display, dest string) error {
	cmd := exec.Command("xwd", "-silent", "-root", "-out", dest+".xwd")
	cmd.Env = overlayEnv(map[string]string{"DISPLAY": display})
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xwd root desktop on DISPLAY=%s: %w: %s", display, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func attachProcessLog(cmd *exec.Cmd, file *os.File) error {
	cmd.Stdout = file
	cmd.Stderr = file
	err := cmd.Start()
	_ = file.Close()
	return err
}
