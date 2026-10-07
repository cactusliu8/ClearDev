package cleardevdemo

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ProcessGroup tracks PIDs started by this demonstration so failure cleanup
// cannot touch other projects.
type ProcessGroup struct {
	pids []int
}

func (g *ProcessGroup) add(pid int) {
	if pid > 0 {
		g.pids = append(g.pids, pid)
	}
}

// Kill terminates process groups started by this demonstration. It does not
// signal PIDs that this run did not track.
func (g *ProcessGroup) Kill() {
	if g == nil {
		return
	}
	for i := len(g.pids) - 1; i >= 0; i-- {
		pid := g.pids[i]
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !g.anyAlive() {
			g.reapAll()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	for i := len(g.pids) - 1; i >= 0; i-- {
		pid := g.pids[i]
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !g.anyAlive() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	g.reapAll()
}

func (g *ProcessGroup) anyAlive() bool {
	for _, pid := range g.pids {
		if processAlive(pid) || processGroupAlive(pid) {
			return true
		}
	}
	return false
}

func processGroupAlive(groupID int) bool {
	if groupID <= 0 {
		return false
	}
	err := syscall.Kill(-groupID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (g *ProcessGroup) reapAll() {
	for _, pid := range g.pids {
		reapPID(pid)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		reapPID(pid)
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		reapPID(pid)
		return false
	}
	text := string(raw)
	i := strings.LastIndex(text, ") ")
	if i >= 0 && i+2 < len(text) {
		switch text[i+2] {
		case 'Z', 'X':
			reapPID(pid)
			return false
		}
	}
	return true
}

func reapPID(pid int) {
	var status syscall.WaitStatus
	_, _ = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
}

func waitPortsClosed(ports []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allClosed := true
		for _, port := range ports {
			conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				allClosed = false
			}
		}
		if allClosed {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func demoWorkDirectoriesGone(layout Layout) bool {
	for _, dir := range []string{layout.HomeDir, layout.DataDir, layout.RepoDir, layout.AppDir} {
		if _, err := os.Stat(filepath.Clean(dir)); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}
