//go:build !windows

package cleardevlocal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func lockCheckFile(ctx context.Context, path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ClearDev capacity lock: %w", err)
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock ClearDev capacity state: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func checkAvailableBytes(path string) (int64, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return 0, err
	}
	blockSize := uint64(stats.Bsize) //nolint:gosec // Statfs reports a non-negative kernel block size.
	high, available := bits.Mul64(stats.Bavail, blockSize)
	if high != 0 || available > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(available), nil //nolint:gosec // the MaxInt64 bound is checked above.
}

func checkProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func checkProcessIdentity(pid int) string {
	payload, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		text := string(payload)
		end := strings.LastIndex(text, ") ")
		if end >= 0 {
			fields := strings.Fields(text[end+2:])
			if len(fields) > 19 {
				return "proc:" + fields[19]
			}
		}
	}
	// macOS and other supported Unix hosts do not expose /proc. lstart is a
	// stable process-birth fact and prevents an old reservation from being
	// mistaken for a newly reused PID.
	output, err := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // validated numeric pid and fixed system executable.
	if err != nil || strings.TrimSpace(string(output)) == "" {
		return ""
	}
	return "ps:" + strings.TrimSpace(string(output))
}
