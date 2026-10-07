package benchmarkruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type sessionFacts struct {
	record domain.SessionRecord
	err    error
	reads  int
}

func (f *sessionFacts) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	f.reads++
	return f.record, true, f.err
}

func TestRuntimeRejectsUnprovenSessionsBeforeOpeningTransport(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	facts := &sessionFacts{record: domain.SessionRecord{ID: "session", Harness: domain.HarnessCodex, Mode: domain.SessionModeChat,
		Metadata: domain.SessionMetadata{WorkspacePath: workspace, WorkspaceRepoPath: root}}}
	driver := New(Binding{DataRoot: root, ProjectRoot: root, SocketPath: filepath.Join(root, "absent.sock")}, facts, nil)
	cfg := ports.ChatStartConfig{SessionID: "session", DataDir: root, WorkspacePath: workspace}
	facts.err = errors.New("real storage unavailable")
	if _, err := driver.Start(context.Background(), cfg); !errors.Is(err, facts.err) {
		t.Fatalf("storage error hidden: %v", err)
	}
	facts.err = nil
	cfg.MCPServers = []ports.ChatMCPServerConfig{{Name: "injected", Command: "/bin/sh"}}
	if _, err := driver.Start(context.Background(), cfg); err == nil || err.Error() != "runtime start settings outside binding" {
		t.Fatalf("MCP reached transport: %v", err)
	}
	cfg.MCPServers = nil
	cfg.WorkspacePath = root
	if _, err := driver.Start(context.Background(), cfg); err == nil || err.Error() != "runtime AO session mismatch" {
		t.Fatalf("wrong worktree reached transport: %v", err)
	}
	cfg.WorkspacePath = workspace
	cfg.DataDir = workspace
	if _, err := driver.Start(context.Background(), cfg); err == nil || err.Error() != "runtime session source missing" {
		t.Fatalf("wrong data root reached transport: %v", err)
	}
	if _, err := driver.Resume(context.Background(), ports.ChatResumeConfig{SessionID: "session", DataDir: root, WorkspacePath: workspace}); err == nil || err.Error() != "runtime resume settings outside binding" {
		t.Fatalf("missing provider ID reached transport: %v", err)
	}
}
