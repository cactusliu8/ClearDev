//go:build !windows

package e2e

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const electronE2EEnv = "AO_ELECTRON_E2E"

type xWindow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Class  string `json:"class"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func requireElectronE2E(t *testing.T) {
	t.Helper()
	if os.Getenv(electronE2EEnv) != "1" {
		t.Skip("set AO_ELECTRON_E2E=1 with Xvfb/xdotool, a packaged Electron binary, and an X11 display to click native Approve")
	}
	if home, err := os.UserHomeDir(); err == nil {
		localBin := filepath.Join(home, ".local", "bin")
		t.Setenv("PATH", localBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	for _, bin := range []string{"Xvfb", "xdotool", "xwd"} {
		if hostToolPath(bin) == "" {
			t.Fatalf("S05 native click driver needs %s on PATH or ~/.local/bin", bin)
		}
	}
}

func hostToolPath(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(home, ".local", "bin", name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func removeStaleXLock(n int) {
	path := fmt.Sprintf("/tmp/.X%d-lock", n)
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 || !processAlive(pid) {
		_ = os.Remove(path)
	}
}

func tcpPortFree(hostPort string) bool {
	listener, err := net.Listen("tcp", hostPort)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func overlayEnv(overrides map[string]string) []string {
	seen := make(map[string]bool, len(overrides))
	out := make([]string, 0, len(os.Environ())+len(overrides))
	for key, value := range overrides {
		out = append(out, key+"="+value)
		seen[key] = true
	}
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || seen[key] {
			continue
		}
		out = append(out, item)
	}
	return out
}

func electronX11Env(display string, extra map[string]string) []string {
	overrides := map[string]string{
		"DISPLAY":                            display,
		"AO_DISABLE_GPU":                     "1",
		"AO_KEEP_DAEMON":                     "0",
		"AO_TELEMETRY_REMOTE":                "off",
		"ELECTRON_DISABLE_SECURITY_WARNINGS": "1",
		"ELECTRON_OZONE_PLATFORM":            "x11",
		"GDK_BACKEND":                        "x11",
		"QT_X11_NO_MITSHM":                   "1",
		"LIBGL_ALWAYS_SOFTWARE":              "1",
	}
	for key, value := range extra {
		overrides[key] = value
	}
	return overlayEnv(overrides)
}

func startXvfbDisplay(t *testing.T) (display string, pid int) {
	t.Helper()
	xvfb := hostToolPath("Xvfb")
	for n := 21; n <= 99; n++ {
		port := 6000 + n
		hostPort := fmt.Sprintf("127.0.0.1:%d", port)
		if !tcpPortFree(hostPort) {
			continue
		}
		removeStaleXLock(n)
		logPath := filepath.Join(t.TempDir(), fmt.Sprintf("xvfb-%d.log", n))
		logFile, err := os.Create(logPath)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(xvfb,
			fmt.Sprintf(":%d", n),
			"-screen", "0", "1280x800x24",
			"-ac",
			"-nolisten", "unix",
			"-listen", "tcp",
			"-nolock",
		)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			continue
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if !processAlive(cmd.Process.Pid) {
				break
			}
			conn, err := net.DialTimeout("tcp", hostPort, 50*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				t.Cleanup(func() {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
					_, _ = cmd.Process.Wait()
					_ = logFile.Close()
				})
				tcpDisplay := fmt.Sprintf("127.0.0.1:%d", n)
				t.Logf("Xvfb display=%s pid=%d log=%s", tcpDisplay, cmd.Process.Pid, logPath)
				return tcpDisplay, cmd.Process.Pid
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = logFile.Close()
	}
	t.Fatal("could not start an isolated Xvfb display on TCP (unix sockets are unavailable on this host)")
	return "", 0
}

func xdotoolOutput(t *testing.T, display string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(hostToolPath("xdotool"), args...)
	cmd.Env = overlayEnv(map[string]string{"DISPLAY": display})
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func xdotool(t *testing.T, display string, args ...string) string {
	t.Helper()
	out, _ := xdotoolOutput(t, display, args...)
	return out
}

func collectWindowIDs(t *testing.T, display string, pids ...int) []string {
	t.Helper()
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
	add(xdotool(t, display, "search", "--onlyvisible", "--name", ".*"))
	add(xdotool(t, display, "search", "--name", ".*"))
	add(xdotool(t, display, "search", "--class", "electron"))
	add(xdotool(t, display, "search", "--class", "Electron"))
	add(xdotool(t, display, "search", "--classname", "electron"))
	add(xdotool(t, display, "search", "--class", "GtkDialog"))
	for _, pid := range pids {
		if pid > 0 {
			add(xdotool(t, display, "search", "--pid", strconv.Itoa(pid), ".*"))
		}
	}
	sort.Strings(ids)
	return ids
}

func inspectXWindows(t *testing.T, display string, pids ...int) []xWindow {
	t.Helper()
	var windows []xWindow
	for _, id := range collectWindowIDs(t, display, pids...) {
		name, _ := xdotoolOutput(t, display, "getwindowname", id)
		class := xWindowClass(t, display, id)
		geometry, err := xdotoolOutput(t, display, "getwindowgeometry", "--shell", id)
		if err != nil {
			continue
		}
		win := xWindow{ID: id, Name: name, Class: class}
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

func listedXWindows(t *testing.T, display string, pids ...int) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, win := range inspectXWindows(t, display, pids...) {
		out[win.ID] = true
	}
	return out
}

func xWindowClass(t *testing.T, display, id string) string {
	t.Helper()
	xprop, err := exec.LookPath("xprop")
	if err != nil {
		return ""
	}
	cmd := exec.Command(xprop, "-id", id, "WM_CLASS")
	cmd.Env = overlayEnv(map[string]string{"DISPLAY": display})
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func looksLikeRootOrTiny(win xWindow) bool {
	name := strings.ToLower(strings.TrimSpace(win.Name))
	if strings.Contains(name, "clipboard") {
		return true
	}
	if win.Width >= 1200 && win.Height >= 700 {
		return true
	}
	if win.Width <= 20 && win.Height <= 20 {
		return true
	}
	if win.Width == win.Height && win.Width <= 256 {
		return true
	}
	return name == "electron" && win.Width <= 256 && win.Height <= 256
}

func dialogScore(win xWindow) int {
	blob := strings.ToLower(win.Name + " " + win.Class)
	score := 0
	if strings.Contains(blob, "approve") || strings.Contains(blob, "confirm") || strings.Contains(blob, "direction") {
		score += 10
	}
	if strings.Contains(blob, "gtk") || strings.Contains(blob, "dialog") {
		score += 5
	}
	if win.Width >= 300 && win.Height >= 80 && win.Width >= win.Height*2 && win.Width <= 1000 && win.Height <= 400 {
		score += 8
	}
	if win.Width >= 200 && win.Height >= 80 && win.Width <= 900 && win.Height <= 700 {
		score += 3
	}
	return score
}

func pickDecisionDialog(windows []xWindow, before map[string]bool) (xWindow, bool) {
	var candidates []xWindow
	for _, win := range windows {
		if before[win.ID] || looksLikeRootOrTiny(win) {
			continue
		}
		candidates = append(candidates, win)
	}
	if len(candidates) == 0 {
		return xWindow{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		si, sj := dialogScore(candidates[i]), dialogScore(candidates[j])
		if si != sj {
			return si > sj
		}
		ai := candidates[i].Width * candidates[i].Height
		aj := candidates[j].Width * candidates[j].Height
		if ai != aj {
			return ai < aj
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], true
}

func waitForDecisionDialog(t *testing.T, display string, before map[string]bool, timeout time.Duration, pids ...int) xWindow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []xWindow
	for time.Now().Before(deadline) {
		last = inspectXWindows(t, display, pids...)
		if win, ok := pickDecisionDialog(last, before); ok && dialogScore(win) >= 8 {
			return win
		}
		time.Sleep(250 * time.Millisecond)
	}
	if win, ok := pickDecisionDialog(last, nil); ok && dialogScore(win) >= 8 {
		t.Logf("using already-visible dialog %s name=%q class=%q %dx%d", win.ID, win.Name, win.Class, win.Width, win.Height)
		return win
	}
	t.Fatalf("native dialog did not appear on DISPLAY=%s within %s; windows=%s",
		display, timeout, mustJSON(last))
	return xWindow{}
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(raw)
}

func xWindowExists(t *testing.T, display, windowID string) bool {
	t.Helper()
	_, err := xdotoolOutput(t, display, "getwindowname", windowID)
	return err == nil
}

func screenshotXWindow(t *testing.T, display, windowID, dest string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		t.Fatal(err)
	}
	env := overlayEnv(map[string]string{"DISPLAY": display})
	if path := hostToolPath("import"); path != "" {
		cmd := exec.Command(path, "-window", windowID, dest)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err == nil {
			return
		}
		t.Logf("import screenshot failed: %v\n%s", err, out)
	}
	xwdPath := dest
	if !strings.HasSuffix(dest, ".xwd") {
		xwdPath = dest + ".xwd"
	}
	xwd := hostToolPath("xwd")
	if xwd == "" {
		t.Fatalf("screenshot window %s: xwd is missing", windowID)
	}
	cmd := exec.Command(xwd, "-id", windowID, "-out", xwdPath)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("screenshot window %s: %v\n%s", windowID, err, out)
	}
}

func clickNativeApprove(t *testing.T, display, screenshotPath string, before map[string]bool, pids ...int) {
	t.Helper()
	if before == nil {
		before = listedXWindows(t, display, pids...)
	}
	win := waitForDecisionDialog(t, display, before, 2*time.Minute, pids...)
	writeJSONFile(t, screenshotPath+".windows.json", inspectXWindows(t, display, pids...))
	screenshotXWindow(t, display, win.ID, screenshotPath)
	if out, err := xdotoolOutput(t, display, "windowactivate", "--sync", win.ID); err != nil {
		t.Logf("windowactivate: %v %s", err, out)
	}
	if out, err := xdotoolOutput(t, display, "windowfocus", "--sync", win.ID); err != nil {
		t.Logf("windowfocus: %v %s", err, out)
	}
	time.Sleep(300 * time.Millisecond)
	// Production dialogs use buttons [Approve, Reject, Later] with Later focused.
	sequences := [][]string{
		{"key", "--window", win.ID, "--delay", "40", "Left", "Left", "Return"},
		{"key", "--window", win.ID, "--delay", "40", "ISO_Left_Tab", "ISO_Left_Tab", "Return"},
		{"key", "--window", win.ID, "Alt+A"},
		{"mousemove", "--window", win.ID, strconv.Itoa(max(20, win.Width/6)), strconv.Itoa(max(20, win.Height*4/5)), "click", "1"},
	}
	for _, args := range sequences {
		out, err := xdotoolOutput(t, display, args...)
		if err != nil {
			t.Logf("xdotool %v: %v %s", args, err, out)
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if !xWindowExists(t, display, win.ID) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	t.Fatalf("native Approve click did not close dialog window %s name=%q class=%q", win.ID, win.Name, win.Class)
}

func packagedElectronBinary(t *testing.T) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv("AO_ELECTRON_BIN")); configured != "" {
		resolved, err := filepath.Abs(configured)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(resolved); err != nil {
			t.Fatalf("AO_ELECTRON_BIN=%s: %v", configured, err)
		}
		return resolved
	}
	root := e2eRepoRoot(t)
	patterns := []string{
		filepath.Join(root, "frontend", "out") + "/*/agent-orchestrator",
		filepath.Join(root, "frontend", "out") + "/*/*/agent-orchestrator",
	}
	var matches []string
	for _, pattern := range patterns {
		found, _ := filepath.Glob(pattern)
		matches = append(matches, found...)
	}
	if len(matches) == 0 {
		t.Fatal("packaged Electron binary not found; set AO_ELECTRON_BIN or run npm --prefix frontend run package")
	}
	return matches[0]
}

func unpackagedElectronBinary(t *testing.T) string {
	t.Helper()
	root := e2eRepoRoot(t)
	candidate := filepath.Join(root, "frontend", "node_modules", "electron", "dist", "electron")
	if _, err := os.Stat(candidate); err != nil {
		t.Fatalf("unpackaged Electron dist is missing: %v", err)
	}
	return candidate
}

func electronVersion(t *testing.T, binary string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(binary), "version"))
	if err != nil {
		return "unknown"
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		return "unknown"
	}
	if strings.HasPrefix(version, "v") {
		return version
	}
	return "v" + version
}

func seedIsolatedCodexHome(t *testing.T, isolatedHome string) string {
	t.Helper()
	hostHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(hostHome, ".codex")
	if _, err := os.Stat(filepath.Join(srcDir, "auth.json")); err != nil {
		t.Fatalf("host Codex auth.json is missing: %v", err)
	}
	destDir := filepath.Join(isolatedHome, ".codex")
	if err := os.RemoveAll(destDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(srcDir, destDir); err != nil {
		t.Fatal(err)
	}
	return srcDir
}

func startPackagedElectron(t *testing.T, display, home, dataDir, runFile string, port int) *exec.Cmd {
	t.Helper()
	binary := packagedElectronBinary(t)
	codexHome := seedIsolatedCodexHome(t, home)
	logPath := filepath.Join(t.TempDir(), "electron.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "--no-sandbox")
	cmd.Dir = filepath.Dir(binary)
	cmd.Env = electronX11Env(display, map[string]string{
		"HOME":        home,
		"AO_DATA_DIR": dataDir,
		"AO_RUN_FILE": runFile,
		"AO_PORT":     strconv.Itoa(port),
		"CODEX_HOME":  codexHome,
	})
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start packaged Electron: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_, _ = cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	})
	t.Logf("packaged Electron binary=%s version=%s pid=%d display=%s log=%s",
		binary, electronVersion(t, binary), cmd.Process.Pid, display, logPath)
	return cmd
}

func attachDaemonFromRunFile(t *testing.T, dataDir, runFile string, timeout time.Duration) *daemon {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var info *runfile.Info
	for time.Now().Before(deadline) {
		got, err := runfile.Read(runFile)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if got != nil && got.Port > 0 && got.PID > 0 {
			info = got
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if info == nil {
		t.Fatalf("packaged Electron did not write %s within %s", runFile, timeout)
	}
	daemonPID := info.PID
	t.Cleanup(func() {
		_ = syscall.Kill(-daemonPID, syscall.SIGTERM)
		_ = syscall.Kill(daemonPID, syscall.SIGTERM)
		time.Sleep(300 * time.Millisecond)
		_ = syscall.Kill(-daemonPID, syscall.SIGKILL)
		_ = syscall.Kill(daemonPID, syscall.SIGKILL)
	})
	d := &daemon{
		t:       t,
		dataDir: dataDir,
		port:    info.Port,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d/api/v1", info.Port),
		logPath: filepath.Join(dataDir, "daemon.log"),
		pgid:    info.PID,
	}
	d.waitReady()
	health, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", info.Port))
	if err != nil {
		t.Fatalf("healthz after packaged Electron start: %v", err)
	}
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("healthz status=%d", health.StatusCode)
	}
	t.Logf("attached production daemon pid=%d port=%d", info.PID, info.Port)
	return d
}

func e2eRepoRoot(t *testing.T) string {
	t.Helper()
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for directory := filepath.Clean(workingDir); ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "frontend", "package.json")); err == nil {
			if _, err := os.Stat(filepath.Join(directory, "backend")); err == nil {
				return directory
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	t.Fatal("could not locate repository root")
	return ""
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
