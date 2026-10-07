package previewserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type projectPreviewConfiguration struct {
	contract     core.ProjectExecutionContract
	contractSHA  string
	candidateSHA string
	prepare      ports.ClearDevProjectResultPrepare
	publishData  func() error
	data         string
	node         string
	npm          string
	trialOwner   domain.SessionID
}

// StartProject uses the existing session process manager, not a second preview
// system. The caller is the completed-result service; launch-file/UI callers
// cannot set the private project marker or supply a preparation callback.
func (m *Manager) StartProject(ctx context.Context, sessionID domain.SessionID, sourceWorkspace, candidateSHA string, contract core.ProjectExecutionContract, prepare ports.ClearDevProjectResultPrepare) (Status, error) {
	if !core.ProjectTrialNeedsService(contract.Basis) {
		return stoppedStatus(sessionID), serviceError("RESULT_COMMAND_USAGE", "这是命令行交付物，没有网页服务；请按已确认的命令和使用说明运行。")
	}
	return m.startProject(ctx, sessionID, sourceWorkspace, candidateSHA, contract, prepare, false, false)
}

// PrepareProjectProgression runs the existing migration/start/readiness sequence
// under the project lock, then stops only the process created by this call.
// It never stops an already running user preview or publishes failed migrations.
func (m *Manager) PrepareProjectProgression(ctx context.Context, sessionID domain.SessionID, workspace, candidate string, contract core.ProjectExecutionContract, prepare ports.ClearDevProjectResultPrepare) error {
	if !core.ProjectTrialNeedsService(contract.Basis) {
		return serviceError("RESULT_COMMAND_USAGE", "Automatic data preparation requires the declared service runtime")
	}
	_, err := m.startProject(ctx, sessionID, workspace, candidate, contract, prepare, false, true)
	return err
}

// StartStageTrial reuses the owned process lifecycle with isolated review data.
// Only the service may supply a trusted project preparation callback.
func (m *Manager) StartStageTrial(ctx context.Context, sessionID domain.SessionID, sourceWorkspace, candidateSHA string, contract core.ProjectExecutionContract, prepare ports.ClearDevProjectResultPrepare) (Status, error) {
	return m.startProject(ctx, sessionID, sourceWorkspace, candidateSHA, contract, prepare, true, false)
}

func (m *Manager) startProject(ctx context.Context, sessionID domain.SessionID, sourceWorkspace, candidateSHA string, contract core.ProjectExecutionContract, prepare ports.ClearDevProjectResultPrepare, trial, temporary bool) (Status, error) {
	if err := core.ValidateProjectExecutionContract(contract); err != nil {
		return stoppedStatus(sessionID), err
	}
	if sessionID == "" || prepare == nil || !filepath.IsAbs(sourceWorkspace) || !validProjectPreviewSHA(candidateSHA) {
		return stoppedStatus(sessionID), errors.New("completed project preview binding is incomplete")
	}
	raw, err := json.Marshal(contract)
	if err != nil {
		return stoppedStatus(sessionID), err
	}
	var copied core.ProjectExecutionContract
	if err := json.Unmarshal(raw, &copied); err != nil {
		return stoppedStatus(sessionID), err
	}
	digest, err := core.ProjectExecutionContractDigest(copied)
	if err != nil {
		return stoppedStatus(sessionID), err
	}
	// Serial data-version preparation reuses the existing operation lock. Two
	// deliveries may not independently migrate the same live project database.
	release := m.acquireOperation(domain.SessionID("project-data:" + contract.Selection.AOProjectID))
	defer release()
	if temporary {
		ready, err := m.ProjectDataReady(ctx, contract, candidateSHA)
		if err != nil {
			return stoppedStatus(sessionID), err
		}
		if ready {
			return stoppedStatus(sessionID), nil
		}
	}
	m.mu.Lock()
	busy := false
	for id, run := range m.runs {
		busy = busy || (temporary || id != sessionID) && run.projectID == contract.Selection.AOProjectID && run.cmd != nil
	}
	m.mu.Unlock()
	if busy {
		return stoppedStatus(sessionID), serviceError("RESULT_DATA_IN_USE", "Stop this project's other delivered version before opening the next version; its data has not been changed")
	}
	node, npm, err := projectHostExecutables(ctx)
	if err != nil {
		return stoppedStatus(sessionID), err
	}
	project := &projectPreviewConfiguration{contract: copied, contractSHA: digest, candidateSHA: candidateSHA, prepare: prepare, node: node, npm: npm}
	cfg := Configuration{
		Name: "cleardev-completed-project", RuntimeExecutable: node, Cwd: ".", AutoPort: true,
		URL: "http://127.0.0.1:${PORT}/", TargetKind: TargetApp,
		ReadyTimeoutMillis: copied.Runtime.HealthTimeoutSeconds * 1000, project: project,
	}
	if trial {
		project.trialOwner = sessionID
		cfg.Name = "cleardev-stage-trial"
	}
	status, err := m.startConfigured(ctx, sessionID, sourceWorkspace, cfg)
	if !temporary || err != nil {
		return status, err
	}
	// Project callers serialize on the project lock. Launch-file callers use
	// only the session lock, so also bind cleanup to this exact start time.
	releaseSession := m.acquireOperation(sessionID)
	defer releaseSession()
	m.mu.Lock()
	owned := m.runs[sessionID]
	same := owned != nil && owned.projectCandidateSHA == candidateSHA && owned.projectContractSHA == digest && owned.status.StartedAt.Equal(status.StartedAt)
	m.mu.Unlock()
	if !same {
		return status, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, stopErr := m.stop(cleanupCtx, sessionID)
	return status, errors.Join(err, stopErr)
}

// StageTrialStatus never treats a completed-result preview as a review trial.
func (m *Manager) StageTrialStatus(sessionID domain.SessionID, candidateSHA, contractSHA string) Status {
	status := m.ProjectStatus(sessionID, candidateSHA, contractSHA)
	if status.Configuration != "cleardev-stage-trial" {
		return stoppedStatus(sessionID)
	}
	return status
}

// ProjectStatus reports only the preview bound to this exact candidate and contract.
func (m *Manager) ProjectStatus(sessionID domain.SessionID, candidateSHA, contractSHA string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[sessionID]
	if run == nil || run.projectCandidateSHA != candidateSHA || run.projectContractSHA != contractSHA {
		return stoppedStatus(sessionID)
	}
	return m.statusForLocked(run)
}

func validProjectPreviewSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Host execution deliberately supports only the installed Node22/npm10 lane.
// Dependencies and candidate source still come from the isolated pinned check
// preparation. This observation is preview readiness, never delivery evidence.
func projectHostExecutables(ctx context.Context) (string, string, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return "", "", serviceError("PROJECT_RUNTIME_UNSUPPORTED", "Node 22 is required to open the delivered application")
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return "", "", serviceError("PROJECT_RUNTIME_UNSUPPORTED", "npm 10 is required to open the delivered application")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for executable, prefix := range map[string]string{node: "v22.", npm: "10."} {
		command := exec.CommandContext(probeCtx, executable, "--version") //nolint:gosec // fixed runtime executable and version probe.
		command.Env = []string{"PATH=" + projectHostPath(node, npm), "NODE_ENV=production"}
		raw, err := command.Output()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(raw)), prefix) {
			return "", "", serviceError("PROJECT_RUNTIME_UNSUPPORTED", "The local preview needs Node 22 and npm 10; no application was started")
		}
	}
	return node, npm, nil
}

func projectHostPath(node, npm string) string {
	return strings.Join([]string{filepath.Dir(node), filepath.Dir(npm), "/usr/bin", "/bin"}, string(filepath.ListSeparator))
}

// projectPreviewSourceMatches binds a prepared source to one completed project
// contract. The environment digest is read in either the canonical or the
// earlier HTML-escaped spelling, because both spellings identify the same
// contract object; every other binding stays exact.
func projectPreviewSourceMatches(source ports.ClearDevProjectResultSource, project *projectPreviewConfiguration) bool {
	if source.CandidateSHA != project.candidateSHA || source.ContractSHA256 != project.contractSHA || source.Release == nil || !filepath.IsAbs(source.WorkspacePath) {
		return false
	}
	if source.Environment.CandidateSHA != source.CandidateSHA || source.Environment.SourceManifestID == "" {
		return false
	}
	return core.ProjectExecutionDigestMatches(source.Environment.ProjectExecutionSHA256, project.contract)
}

func (m *Manager) prepareProjectPreview(ctx context.Context, cfg *Configuration) (ports.ClearDevProjectResultSource, error) {
	project := cfg.project
	source, err := project.prepare(ctx)
	if err != nil {
		return source, err
	}
	fail := func(err error) (ports.ClearDevProjectResultSource, error) {
		if source.Release != nil {
			_ = source.Release(context.WithoutCancel(ctx))
		}
		return ports.ClearDevProjectResultSource{}, err
	}
	if !projectPreviewSourceMatches(source, project) {
		return fail(errors.New("the prepared result does not match its completed project contract"))
	}
	var data projectDataPreparation
	if project.trialOwner != "" {
		data, err = m.prepareStageTrialData(ctx, project.contract, project.candidateSHA, project.contractSHA, project.trialOwner)
	} else {
		data, err = m.prepareProjectData(ctx, project.contract, project.candidateSHA)
	}
	if err != nil {
		return fail(err)
	}
	project.data, project.publishData = data.directory, data.publish
	payload, err := json.Marshal(struct {
		Prepare []string `json:"prepare"`
		Start   []string `json:"start"`
	}{Prepare: project.contract.Runtime.PrepareArgv, Start: project.contract.Basis.Launch.Argv})
	if err != nil {
		return fail(err)
	}
	// This backend-owned wrapper sequences argv, never shell interpolation.
	// Both preparation and the child application remain in the existing owned
	// process group, so timeout/stop/daemon restart use the same PID safeguards.
	cfg.RuntimeExecutable, cfg.RuntimeArgs = project.node, []string{"-e", projectPreviewWrapper, string(payload)}
	return source, nil
}

const projectPreviewWrapper = `const { spawn } = require('node:child_process');
const config = JSON.parse(process.argv[1]);
let child;
function start(argv, next) {
  child = spawn(argv[0], argv.slice(1), {stdio:'inherit', shell:false, env:process.env});
  child.once('error', e => { console.error('project startup:', e.message); process.exit(1); });
  child.once('exit', (code, signal) => {
    if (code !== 0 || signal) { process.exit(code || 1); }
    else if (next) { next(); } else { process.exit(0); }
  });
}
process.on('SIGTERM', () => { if (child) child.kill('SIGTERM'); });
process.on('SIGINT', () => { if (child) child.kill('SIGINT'); });
if (config.prepare && config.prepare.length) start(config.prepare, () => start(config.start));
else start(config.start);
`

func projectPreviewEnvironment(project *projectPreviewConfiguration, workspace string, port int) []string {
	root := filepath.Dir(workspace)
	runtimeEnv := core.ProjectRuntimeEnvironment(project.contract.Runtime, project.data, port, false)
	env := make([]string, 0, 9+len(runtimeEnv))
	env = append(env,
		"PATH="+projectHostPath(project.node, project.npm), "HOME="+filepath.Join(root, "home"),
		"TMPDIR="+filepath.Join(root, "tmp"), "TMP="+filepath.Join(root, "tmp"), "TEMP="+filepath.Join(root, "tmp"),
		"npm_config_offline=true", "npm_config_ignore_scripts=true", "npm_config_audit=false", "npm_config_fund=false",
	)
	return append(env, runtimeEnv...)
}

func (m *Manager) probeProject(ctx context.Context, target string, runtime core.ProjectRuntime) error {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	url := strings.TrimSuffix(target, "/") + runtime.HealthPath
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	client := *m.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != runtime.HealthStatus || response.Request.URL.String() != url {
		return fmt.Errorf("project health returned %d, expected %d at the declared loopback path", response.StatusCode, runtime.HealthStatus)
	}
	read, err := io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024+1))
	if err != nil || read > 64*1024 {
		return errors.New("project health response exceeds the bounded local probe")
	}
	return nil
}
