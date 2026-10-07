package codexappserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processdiagnostics"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// clientName identifies AO to the provider. It shows up in the app-server's
// reported user agent, which makes a stray process attributable.
const (
	clientName    = "agent-orchestrator"
	clientTitle   = "Agent Orchestrator"
	clientVersion = "0.1.0"
	// This is the oldest Codex build whose complete Chat surface AO exercised:
	// thread start/resume, turn start/interrupt, approvals, and every advertised
	// extension. initialize plus model/list alone cannot prove those mutating
	// methods without creating provider state during preflight.
	minimumCodexVersion = "0.146.0"
)

// handshakeTimeout bounds initialize and thread open. These are local IPC calls
// that normally settle in well under a second.
const handshakeTimeout = 60 * time.Second

// codexPlugin is the subset of AO's existing Codex agent plugin that the Chat
// driver reuses. Binary resolution and local auth probing already live there and
// must not be reimplemented: a second copy would drift from what TUI sessions do.
type codexPlugin interface {
	ResolveBinary(ctx context.Context) (string, error)
	AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error)
}

// process is a running app-server, abstracted so tests can substitute pipes for
// a child process.
type process struct {
	diagnostics *processdiagnostics.Capture
	stdin       io.WriteCloser
	stdout      io.Reader
	// stop releases the process. It must be safe to call more than once.
	stop           func() error
	processStopped func() bool
}

// spawnFunc launches an app-server. Injected so tests never exec anything.
type spawnFunc func(ctx context.Context, bin, workdir string, env, args []string) (*process, error)

type versionProbeFunc func(context.Context, string) (string, error)

// Driver opens Codex conversations over `codex app-server`.
type Driver struct {
	roleContainer bool
	plugin        codexPlugin
	log           *slog.Logger
	spawn         spawnFunc
	versionProbe  versionProbeFunc
}

// New builds a Chat driver over the existing Codex agent plugin.
func New(plugin codexPlugin, log *slog.Logger) *Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Driver{
		plugin: plugin, log: log, spawn: spawnAppServer,
		versionProbe: installedCodexVersion,
	}
}

var (
	_ ports.ChatDriver           = (*Driver)(nil)
	_ ports.ChatAccountInspector = (*Driver)(nil)
)

// Harness reports which agent this driver serves.
func (d *Driver) Harness() domain.AgentHarness { return domain.HarnessCodex }

// capabilities is what a Codex app-server of a supported version provides. Each
// entry here was exercised against a live app-server rather than read off a doc.
func capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming:   true,
		ports.ChatCapabilityTools:       true,
		ports.ChatCapabilityApprovals:   true,
		ports.ChatCapabilityInterrupt:   true,
		ports.ChatCapabilityResume:      true,
		ports.ChatCapabilityHistory:     true,
		ports.ChatCapabilityUsage:       true,
		ports.ChatCapabilityDiffs:       true,
		ports.ChatCapabilityPlans:       true,
		ports.ChatCapabilityInteractive: true,
		ports.ChatCapabilityModels:      true,
		// The account's quota position is both pushed (account/rateLimits/updated)
		// and readable on demand (account/rateLimits/read), verified against a live
		// account.
		ports.ChatCapabilityRateLimits: true,
		// Without this a long conversation eventually cannot accept another turn at
		// all: every turn re-sends the history, so context fills on its own and the
		// only way back is to summarize what is already there.
		ports.ChatCapabilityCompaction: true,
		// History operations, all three exercised against a live app-server. Rollback
		// is advertised despite thread/rollback carrying a DEPRECATED annotation:
		// what the installed provider does is the only honest answer, and gating the
		// feature off while the call still works would take undo away for no reason.
		ports.ChatCapabilityRollback: true,
		ports.ChatCapabilityFork:     true,
		ports.ChatCapabilityRename:   true,
		ports.ChatCapabilitySkills:   true,
		// config/mcpServer/reload plus the status inventory read after it, both
		// exercised against a live app-server.
		ports.ChatCapabilityMCPReload: true,
		// Guidance into a turn already in flight, over turn/steer. Advertised only
		// after being driven against a live app-server (TestLiveSteerKeepsTheTurnAndItsWork
		// on codex-cli 0.146.0): the steered turn kept its id, emitted one
		// turn/started and one turn/completed, settled `completed` rather than
		// interrupted, and followed the correction. Strictly better than
		// interrupt-and-resend, which throws the turn's context and in-flight work
		// away.
		ports.ChatCapabilitySteer: true,
	}
}

// Probe reports what this install can do without creating a conversation, so an
// unsupported request can be refused before AO commits a session or worktree.
func (d *Driver) Probe(ctx context.Context) (ports.ChatCapabilities, error) {
	conv, err := d.openProbeConversation(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conv.Close() }()
	listCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var models struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := conv.conn.request(listCtx, "model/list", map[string]any{}, &models); err != nil {
		return nil, fmt.Errorf("%w: model/list: %w", ports.ErrChatDriverIncompatible, err)
	}
	return capabilities(), nil
}

// InspectAccount reads the live catalog and quota without creating a session.
func (d *Driver) InspectAccount(ctx context.Context) (ports.ChatAccountInspection, error) {
	conv, err := d.openProbeConversation(ctx)
	if err != nil {
		return ports.ChatAccountInspection{}, err
	}
	defer func() { _ = conv.Close() }()
	models, err := conv.ListModels(ctx)
	if err != nil {
		return ports.ChatAccountInspection{}, fmt.Errorf("%w: model/list: %w", ports.ErrChatDriverIncompatible, err)
	}
	limits, err := conv.ReadRateLimits(ctx)
	if err != nil {
		return ports.ChatAccountInspection{Capabilities: capabilities(), Models: models},
			fmt.Errorf("%w: account/rateLimits/read: %w", ports.ErrChatProviderUnavailable, err)
	}
	return ports.ChatAccountInspection{
		Capabilities: capabilities(),
		Models:       models,
		RateLimits:   limits,
	}, nil
}

// InspectModels reads model-specific reasoning choices without creating a thread or turn.
func (d *Driver) InspectModels(ctx context.Context, workdir string, env map[string]string) ([]ports.ChatModel, error) {
	conv, err := d.openModelProbe(ctx, workdir, env)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conv.Close() }()
	return conv.ListModels(ctx)
}

func (d *Driver) openProbeConversation(ctx context.Context) (*conversation, error) {
	return d.openModelProbe(ctx, "", nil)
}

func (d *Driver) openModelProbe(ctx context.Context, workdir string, env map[string]string) (*conversation, error) {
	bin, err := d.plugin.ResolveBinary(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
	}

	// An unknown auth result is not proof of failure — the same rule AO already
	// applies to runtime probes. Only an explicit unauthorized blocks creation.
	status, err := d.plugin.AuthStatus(ctx)
	if err == nil && status == ports.AgentAuthStatusUnauthorized {
		return nil, ports.ErrChatAuthRequired
	}
	if err != nil {
		d.log.Debug("codex auth probe inconclusive; continuing", "error", err)
	}
	versionProbe := d.versionProbe
	if versionProbe == nil {
		versionProbe = installedCodexVersion
	}
	versionCtx, versionCancel := context.WithTimeout(ctx, 5*time.Second)
	versionOutput, versionErr := versionProbe(versionCtx, bin)
	versionCancel()
	if versionErr != nil {
		return nil, fmt.Errorf("%w: read Codex version: %w", ports.ErrChatDriverIncompatible, versionErr)
	}
	installed, ok := parseCodexVersion(versionOutput)
	if !ok {
		return nil, fmt.Errorf("%w: unrecognized Codex version %q (AO requires %s or newer)",
			ports.ErrChatDriverIncompatible, strings.TrimSpace(versionOutput), minimumCodexVersion)
	}
	minimum, _ := parseCodexVersion(minimumCodexVersion)
	if installed.less(minimum) {
		return nil, fmt.Errorf("%w: Codex %s is older than AO's tested minimum %s",
			ports.ErrChatDriverIncompatible, installed, minimumCodexVersion)
	}

	if workdir == "" {
		workdir, err = os.Getwd()
		if err != nil || !filepath.IsAbs(workdir) {
			workdir = os.TempDir()
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	return d.connect(probeCtx, workdir, env, false)
}

type codexVersion [3]int

var codexVersionPattern = regexp.MustCompile(`\b(\d+)\.(\d+)\.(\d+)\b`)

func parseCodexVersion(output string) (codexVersion, bool) {
	match := codexVersionPattern.FindStringSubmatch(output)
	if len(match) != 4 {
		return codexVersion{}, false
	}
	var version codexVersion
	for i := range version {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return codexVersion{}, false
		}
		version[i] = value
	}
	return version, true
}

func (v codexVersion) less(other codexVersion) bool {
	for i := range v {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v codexVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func installedCodexVersion(ctx context.Context, bin string) (string, error) {
	output, err := aoprocess.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// Start opens a new Codex thread in the session worktree.
func (d *Driver) Start(ctx context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	if !filepath.IsAbs(cfg.WorkspacePath) {
		// app-server resolves a relative cwd against its own process directory,
		// which would silently put the agent in the wrong tree.
		return nil, fmt.Errorf("workspace path must be absolute, got %q", cfg.WorkspacePath)
	}

	conv, err := d.connect(context.WithValue(ctx, diagnosticContextKey{}, diagnosticContext{cfg.DataDir, string(cfg.SessionID), d.log}), cfg.WorkspacePath, cfg.Env, cfg.DisableMemories)
	if err != nil {
		return nil, err
	}
	creationKey := strings.TrimSpace(cfg.CreationIdempotencyKey)
	if cfg.RecoverProviderConversation {
		if creationKey == "" {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: provider create recovery requires a creation key", ports.ErrChatResumeFailed)
		}
		providerID, recoverErr := recoverCreatedThread(ctx, conv, cfg.WorkspacePath, providerCreationMarker(creationKey))
		if recoverErr != nil {
			_ = conv.Close()
			return nil, recoverErr
		}
		return resumeStartedConversation(ctx, conv, providerID, cfg)
	}

	policy, sandbox, approvalsReviewer := approvalSettings(cfg.Permissions)
	params := map[string]any{
		"cwd":            cfg.WorkspacePath,
		"approvalPolicy": policy,
		"sandbox":        sandbox,
	}
	if approvalsReviewer != "" {
		params["approvalsReviewer"] = approvalsReviewer
	}
	if cfg.Model != "" {
		params["model"] = cfg.Model
	}
	if cfg.SystemPrompt != "" {
		params["developerInstructions"] = cfg.SystemPrompt
	}
	if creationKey != "" {
		// threadSource is stored atomically with thread/start. Hash the internal
		// key so provider state contains only an opaque recovery marker, not the
		// ClearDev workflow identity itself.
		params["threadSource"] = providerCreationMarker(creationKey)
	}

	var resp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	if conv.roleContainer {
		applyRoleContainerSettings(params, false)
	}
	openCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := conv.conn.request(openCtx, "thread/start", params, &resp); err != nil {
		err = conv.proc.diagnostics.Failure("handshake/thread", err)
		_ = conv.Close()
		return nil, fmt.Errorf("thread/start: %w", err)
	}
	if resp.Thread.ID == "" {
		_ = conv.Close()
		return nil, errors.New("thread/start returned no thread id")
	}

	conv.start(resp.Thread.ID, resp.Model, resp.ReasoningEffort)
	return conv, nil
}

func providerCreationMarker(key string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return "agent-orchestrator-idempotent-" + fmt.Sprintf("%x", digest)
}

func recoverCreatedThread(
	ctx context.Context,
	conv *conversation,
	workspacePath, marker string,
) (string, error) {
	type listedThread struct {
		ID           string  `json:"id"`
		Cwd          string  `json:"cwd"`
		ThreadSource *string `json:"threadSource"`
	}
	type listResponse struct {
		Data       []listedThread `json:"data"`
		NextCursor *string        `json:"nextCursor"`
	}

	var matches []string
	var cursor *string
	seenCursors := map[string]struct{}{}
	for {
		params := map[string]any{
			"cwd":         []string{workspacePath},
			"limit":       100,
			"sourceKinds": []string{"appServer"},
		}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		var resp listResponse
		listCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
		err := conv.conn.request(listCtx, "thread/list", params, &resp)
		cancel()
		if err != nil {
			return "", fmt.Errorf("%w: thread/list: %w", ports.ErrChatResumeFailed, err)
		}
		for _, thread := range resp.Data {
			if thread.ID == "" || thread.ThreadSource == nil || *thread.ThreadSource != marker ||
				filepath.Clean(thread.Cwd) != filepath.Clean(workspacePath) {
				continue
			}
			matches = append(matches, thread.ID)
		}
		if resp.NextCursor == nil || strings.TrimSpace(*resp.NextCursor) == "" {
			break
		}
		next := strings.TrimSpace(*resp.NextCursor)
		if _, repeated := seenCursors[next]; repeated {
			return "", fmt.Errorf("%w: thread/list repeated cursor", ports.ErrChatResumeFailed)
		}
		seenCursors[next] = struct{}{}
		cursor = &next
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("%w: provider create recovery found %d tagged threads", ports.ErrChatResumeFailed, len(matches))
	}
	return matches[0], nil
}

func resumeStartedConversation(
	ctx context.Context,
	conv *conversation,
	providerID string,
	cfg ports.ChatStartConfig,
) (ports.ChatConversation, error) {
	policy, sandbox, approvalsReviewer := approvalSettings(cfg.Permissions)
	params := map[string]any{
		"threadId":       providerID,
		"cwd":            cfg.WorkspacePath,
		"approvalPolicy": policy,
		"sandbox":        sandbox,
	}
	if approvalsReviewer != "" {
		params["approvalsReviewer"] = approvalsReviewer
	}
	if cfg.Model != "" {
		params["model"] = cfg.Model
	}
	if cfg.SystemPrompt != "" {
		params["developerInstructions"] = cfg.SystemPrompt
	}
	if conv.roleContainer {
		applyRoleContainerSettings(params, false)
	}
	resumeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var resp struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	if err := conv.conn.request(resumeCtx, "thread/resume", params, &resp); err != nil {
		err = conv.proc.diagnostics.Failure("handshake/thread", err)
		_ = conv.Close()
		return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, err)
	}
	conv.start(providerID, resp.Model, resp.ReasoningEffort)
	return conv, nil
}

// Resume reattaches to a stored Codex thread after a daemon or app-server
// restart. A thread that is still running is rejoined rather than restarted.
func (d *Driver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	if cfg.ProviderConversationID == "" {
		return nil, fmt.Errorf("%w: no stored thread id", ports.ErrChatResumeFailed)
	}
	if !filepath.IsAbs(cfg.WorkspacePath) {
		return nil, fmt.Errorf("workspace path must be absolute, got %q", cfg.WorkspacePath)
	}

	conv, err := d.connect(context.WithValue(ctx, diagnosticContextKey{}, diagnosticContext{cfg.DataDir, string(cfg.SessionID), d.log}), cfg.WorkspacePath, cfg.Env, cfg.DisableMemories)
	if err != nil {
		return nil, err
	}

	policy, sandbox, approvalsReviewer := approvalSettings(cfg.Permissions)
	params := map[string]any{
		"threadId":       cfg.ProviderConversationID,
		"cwd":            cfg.WorkspacePath,
		"approvalPolicy": policy,
		"sandbox":        sandbox,
	}
	if approvalsReviewer != "" {
		params["approvalsReviewer"] = approvalsReviewer
	}
	if cfg.Model != "" {
		params["model"] = cfg.Model
	}
	// Developer instructions are launch context, not durable conversation
	// history. Reapply AO's current standing role when app-server reconstructs a
	// native thread, just as the TUI adapter does with its resume command.
	if cfg.SystemPrompt != "" {
		params["developerInstructions"] = cfg.SystemPrompt
	}
	if conv.roleContainer {
		applyRoleContainerSettings(params, false)
	}
	resumeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	var resp struct {
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	err = conv.conn.request(resumeCtx, "thread/resume", params, &resp)
	if err != nil {
		err = conv.proc.diagnostics.Failure("handshake/thread", err)
		_ = conv.Close()
		// Deliberately not falling back to thread/start: silently opening a new
		// conversation would present unrelated history as continuous.
		return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, err)
	}

	conv.start(cfg.ProviderConversationID, resp.Model, resp.ReasoningEffort)
	return conv, nil
}

// connect spawns app-server and completes the initialize handshake.
func (d *Driver) connect(ctx context.Context, workdir string, env map[string]string, disableMemories bool) (*conversation, error) {
	bin, err := d.plugin.ResolveBinary(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
	}

	args := []string{"app-server"}
	if disableMemories {
		args = []string{"--disable", "memories", "app-server"}
	}
	proc, err := d.spawn(ctx, bin, workdir, envSlice(env), args)
	if err != nil {
		return nil, fmt.Errorf("%w: launch app-server: %w", ports.ErrChatDriverUnavailable, err)
	}

	conv := newConversation(proc, d.log)
	conv.roleContainer = d.roleContainer

	initCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	if err := conv.conn.request(initCtx, "initialize", initializeParams(), nil); err != nil {
		err = conv.proc.diagnostics.Failure("handshake/thread", err)
		_ = conv.Close()
		// A handshake the provider rejects means a protocol AO cannot speak.
		return nil, fmt.Errorf("%w: initialize: %w", ports.ErrChatDriverIncompatible, err)
	}

	if err := conv.conn.notify("initialized", nil); err != nil {
		err = conv.proc.diagnostics.Failure("handshake/thread", err)
		_ = conv.Close()
		return nil, fmt.Errorf("notify initialized: %w", err)
	}
	return conv, nil
}

// approvalSettings maps AO's existing per-session permission mode onto Codex's
// approval policy and sandbox.
//
// The default matches what AO already passes a Codex TUI session
// (--dangerously-bypass-approvals-and-sandbox): AO sessions run in isolated
// worktrees and are expected to work without prompting. Chat does not quietly
// become stricter than the terminal path for the same setting.
func approvalSettings(mode ports.PermissionMode) (policy, sandbox, approvalsReviewer string) {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits:
		return "on-request", "workspace-write", ""
	case ports.PermissionModeAuto:
		// Match the Codex TUI adapter's auto posture: keep writes inside the
		// worktree and route provider approval requests through Codex's native
		// auto reviewer. Without this field an unattended Auto Chat turn parks on
		// ordinary operations such as `git add` waiting for a person.
		return "on-request", "workspace-write", "auto_review"
	default:
		return "never", "danger-full-access", ""
	}
}

type diagnosticContextKey struct{}
type diagnosticContext struct {
	dataDir, session string
	log              *slog.Logger
}

// spawnAppServer is the real launcher.
func spawnAppServer(ctx context.Context, bin, workdir string, env, args []string) (*process, error) {
	cmd := aoprocess.Command(bin, args...)
	cmd.Dir = workdir
	cmd.WaitDelay = 2 * time.Second
	if len(env) > 0 {
		cmd.Env = env
	}

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

	var diagnostic *processdiagnostics.Capture
	if cfg, ok := ctx.Value(diagnosticContextKey{}).(diagnosticContext); ok {
		diagnostic = processdiagnostics.New(cfg.dataDir, "codex", cfg.session, cmd.Environ(), cfg.log)
		cmd.Stderr = diagnostic
	} else {
		cmd.Stderr = io.Discard
	} // Account/catalog probes have no worker session.
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		reported := diagnostic.Failure("start", err)
		diagnostic.Finish(err)
		return nil, fmt.Errorf("start Codex app-server: %w", reported)
	}
	_ = stdoutWriter.Close()
	diagnostic.Record("process", fmt.Sprintf("pid=%d", cmd.Process.Pid))
	waited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); diagnostic.Finish(waitErr); close(waited) }()
	var once sync.Once

	return &process{
		diagnostics: diagnostic,
		processStopped: func() bool {
			select {
			case <-waited:
				return cmd.ProcessState != nil
			default:
				return false
			}
		},
		stdin:  stdin,
		stdout: stdout,

		stop: func() error {
			once.Do(func() {
				defer func() { _ = stdout.Close() }()
				_ = stdin.Close()
				select {
				case <-waited:
				case <-time.After(3 * time.Second):
					_ = cmd.Process.Kill()
					select {
					case <-waited:
					case <-time.After(2 * time.Second):
					}
				}
			})
			return nil
		},
	}, nil
}

// envSlice merges AO's session env OVER the daemon's own, in the KEY=VALUE form
// exec wants. Sorted so a relaunch is byte-identical, which makes process diffs
// readable.
//
// The merge is the point. AO's map is an OVERLAY -- session id, project id, the
// HookPATH-pinned PATH -- not a whole environment; the terminal path gets away
// with treating it as one only because tmux inherits the daemon's env underneath.
// Using it as a replacement launched the provider with eight variables and no
// HOME, USER, TMPDIR, LANG or SSH_AUTH_SOCK. The provider itself survived that
// (its home-directory lookup falls back to the passwd database), which is why it
// went unnoticed, but every shell command the agent runs inherits this env too:
// no SSH agent means `git push` over SSH fails, no HOME means global git config
// and every toolchain cache is missing. Found while writing the Claude driver,
// where the same shape failed outright with "Not logged in".
func envSlice(env map[string]string) []string {
	return processenv.Merge(env)
}

func initializeParams() map[string]any {
	return map[string]any{
		"clientInfo": map[string]any{
			"name":    clientName,
			"title":   clientTitle,
			"version": clientVersion,
		},
		"capabilities": map[string]any{
			"experimentalApi":           true,
			"optOutNotificationMethods": nil,
		},
	}
}
