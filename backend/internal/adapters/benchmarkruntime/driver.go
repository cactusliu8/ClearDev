package benchmarkruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SessionReader reads persisted AO identities before opening a role.
type SessionReader interface {
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
}

// Driver leaves account inspection and the actual container lifecycle with the
// external owner. Only AO-persisted session facts enter its private transport.
type Driver struct {
	binding  Binding
	sessions SessionReader
	log      *slog.Logger
}

// New constructs a private driver without starting a runtime.
func New(binding Binding, sessions SessionReader, log *slog.Logger) *Driver {
	return &Driver{binding: binding, sessions: sessions, log: log}
}

var _ ports.ChatDriver = (*Driver)(nil)
var _ ports.ChatAccountInspector = (*Driver)(nil)

// Harness identifies the fixed native Codex driver.
func (d *Driver) Harness() domain.AgentHarness { return domain.HarnessCodex }

type ownerRequest struct {
	Operation              string                `json:"operation"`
	BindingSHA256          string                `json:"bindingSHA256"`
	Session                *domain.SessionRecord `json:"session,omitempty"`
	ProviderConversationID string                `json:"providerConversationId,omitempty"`
	WorkspacePath          string                `json:"workspacePath,omitempty"`
}

type ownerReply struct {
	OK              bool                        `json:"ok"`
	Error           string                      `json:"error,omitempty"`
	AccountingScope string                      `json:"accountingScope"`
	Inspection      ports.ChatAccountInspection `json:"inspection"`
}

func (d *Driver) open(ctx context.Context, request ownerRequest) (net.Conn, *bufio.Reader, ownerReply, error) {
	var reply ownerReply
	if !runtimeRecordClasses[d.binding.RecordClass] || d.binding.PlanCommit != PlanCommit {
		return nil, nil, reply, fmt.Errorf("private runtime authority missing")
	}
	info, err := os.Lstat(d.binding.SocketPath)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, nil, reply, fmt.Errorf("private runtime socket unavailable")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", d.binding.SocketPath)
	if err != nil {
		return nil, nil, reply, err
	}
	deadline := time.Now().Add(60 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, nil, reply, err
	}
	request.BindingSHA256 = d.binding.BindingSHA256
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		_ = conn.Close()
		return nil, nil, reply, err
	}
	reader := bufio.NewReaderSize(conn, 65536)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		_ = conn.Close()
		return nil, nil, reply, fmt.Errorf("runtime acknowledgement: %w", err)
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		_ = conn.Close()
		return nil, nil, reply, err
	}
	if !reply.OK || reply.AccountingScope != "BOUND_PROVIDER_GATEWAY" {
		_ = conn.Close()
		return nil, nil, reply, fmt.Errorf("runtime refused: %s", reply.Error)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, nil, reply, err
	}
	return conn, reader, reply, nil
}

// InspectAccount obtains the external owner's read-only preflight observation.
func (d *Driver) InspectAccount(ctx context.Context) (ports.ChatAccountInspection, error) {
	conn, _, reply, err := d.open(ctx, ownerRequest{Operation: "preflight"})
	if err != nil {
		return ports.ChatAccountInspection{}, err
	}
	_ = conn.Close()
	if len(reply.Inspection.Models) == 0 || len(reply.Inspection.Capabilities) == 0 {
		return ports.ChatAccountInspection{}, fmt.Errorf("actual upstream preflight missing")
	}
	return reply.Inspection, nil
}

// Probe reports capabilities observed by the external owner.
func (d *Driver) Probe(ctx context.Context) (ports.ChatCapabilities, error) {
	inspection, err := d.InspectAccount(ctx)
	return inspection.Capabilities, err
}

func (d *Driver) session(ctx context.Context, id domain.SessionID, dataRoot, workspace string, starting bool) (domain.SessionRecord, error) {
	if d.sessions == nil || id == "" || dataRoot != d.binding.DataRoot {
		return domain.SessionRecord{}, fmt.Errorf("runtime session source missing")
	}
	record, found, err := d.sessions.GetSession(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, err
	}
	if !found || record.ID != id || record.IsTerminated || record.Harness != domain.HarnessCodex || record.Mode != domain.SessionModeChat {
		return domain.SessionRecord{}, fmt.Errorf("runtime AO session mismatch")
	}
	seeded := starting && (d.binding.Group == "G1" || d.binding.Group == "G2") && record.CreationIdempotencyKey == "" && record.Metadata.WorkspacePath == "" && record.Metadata.WorkspaceRepoPath == "" && record.Metadata.ProviderConversationID == ""
	if seeded {
		// Ordinary AO creation persists the seed before Start, but commits its
		// workspace metadata only in ControllerReady. The external owner must
		// additionally verify the actual registered Git worktree before launch.
		if workspace != filepath.Join(dataRoot, "worktrees", string(record.ProjectID), string(id)) {
			return domain.SessionRecord{}, fmt.Errorf("runtime seeded workspace mismatch")
		}
	} else if record.Metadata.WorkspacePath != workspace || record.Metadata.WorkspaceRepoPath != d.binding.ProjectRoot {
		return domain.SessionRecord{}, fmt.Errorf("runtime AO session mismatch")
	}
	resolved, err := filepath.EvalSymlinks(workspace)
	if err != nil || !filepath.IsAbs(workspace) || resolved != workspace {
		return domain.SessionRecord{}, fmt.Errorf("runtime workspace identity")
	}
	return record, nil
}

func (d *Driver) native(record domain.SessionRecord, providerID, workspace string) *codexappserver.Driver {
	return codexappserver.NewRoleContainer(codex.New(), d.log, func(ctx context.Context, _ string) (*codexappserver.RoleContainerProcess, error) {
		conn, reader, _, err := d.open(ctx, ownerRequest{Operation: "attach", Session: &record, ProviderConversationID: providerID, WorkspacePath: workspace})
		if err != nil {
			return nil, err
		}
		var once sync.Once
		var stopped error
		stop := func() error {
			once.Do(func() {
				// Closing the stream tells the sole external owner to stop its container;
				// the separate stop acknowledgement is returned only after external scans.
				_ = conn.Close()
				stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				control, _, _, err := d.open(stopCtx, ownerRequest{Operation: "stop", Session: &record})
				if control != nil {
					_ = control.Close()
				}
				stopped = err
			})
			return stopped
		}
		return &codexappserver.RoleContainerProcess{Stdin: conn, Stdout: reader, Stop: stop}, nil
	})
}

// Start asks the owner to launch the persisted session in its own container.
func (d *Driver) Start(ctx context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	record, err := d.session(ctx, cfg.SessionID, cfg.DataDir, cfg.WorkspacePath, true)
	if err != nil {
		return nil, err
	}
	if cfg.CreationIdempotencyKey != record.CreationIdempotencyKey || len(cfg.MCPServers) > 0 || len(cfg.AdditionalDirectories) > 0 {
		return nil, fmt.Errorf("runtime start settings outside binding")
	}
	cfg.Env = nil
	return d.native(record, "", cfg.WorkspacePath).Start(ctx, cfg)
}

// Resume reuses the bound provider conversation after verifying its AO session.
func (d *Driver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	record, err := d.session(ctx, cfg.SessionID, cfg.DataDir, cfg.WorkspacePath, false)
	if err != nil {
		return nil, err
	}
	if cfg.ProviderConversationID == "" || len(cfg.MCPServers) > 0 || len(cfg.AdditionalDirectories) > 0 {
		return nil, fmt.Errorf("runtime resume settings outside binding")
	}
	cfg.Env = nil
	return d.native(record, cfg.ProviderConversationID, cfg.WorkspacePath).Resume(ctx, cfg)
}
