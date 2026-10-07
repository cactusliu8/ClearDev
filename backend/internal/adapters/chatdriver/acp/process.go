package acp

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processdiagnostics"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
)

type process struct {
	stdin          io.WriteCloser
	stdout         io.Reader
	stop           func() error
	processStopped func() bool
	diagnostics    *processdiagnostics.Capture
}

type spawnFunc func(Launch, string) (*process, error)

func spawnAgent(launch Launch, workdir string) (*process, error) {
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.Dir = workdir
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = processenv.Merge(launch.Env)
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	diagnostic := processdiagnostics.New(launch.diagnosticDataDir, "acp", launch.diagnosticSession, cmd.Env, launch.diagnosticLogger)
	cmd.Stderr = diagnostic // exec drains concurrently and Wait waits for the final bytes.
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		reported := diagnostic.Failure("start", err)
		diagnostic.Finish(err)
		return nil, fmt.Errorf("start ACP process: %w", reported)
	}
	_ = stdoutWriter.Close()
	diagnostic.Record("process", fmt.Sprintf("pid=%d", cmd.Process.Pid))

	done := make(chan error, 1)
	waited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); diagnostic.Finish(waitErr); done <- waitErr; close(waited) }()
	var once sync.Once
	return &process{
		diagnostics: diagnostic,
		processStopped: func() bool {
			select {
			case <-waited:
				return processExitError(waitErr) == nil
			default:
				return false
			}
		},
		stdin:  stdin,
		stdout: stdout,
		stop: func() error {
			var stopErr error
			once.Do(func() {
				defer func() { _ = stdout.Close() }()
				_ = stdin.Close()
				select {
				case err := <-done:
					stopErr = processExitError(err)
				case <-time.After(3 * time.Second):
					stopErr = killProcessTree(cmd)
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						if stopErr == nil {
							stopErr = errors.New("ACP process did not exit after kill")
						}
					}
				}
			})
			return stopErr
		},
	}, nil
}

func processExitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// A provider that exits during shutdown has already released its resources.
		return nil
	}
	return err
}
