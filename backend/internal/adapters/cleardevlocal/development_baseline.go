package cleardevlocal

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

//go:embed baseline_health.mjs
var baselineHealthProbe string

const baselineMemoryBytes = 512 * 1024 * 1024

// CheckDevelopmentBaseline recognizes the fixed mail product from immutable
// HEAD/root commits, not from a mutable file or an Agent-supplied policy flag.
// Other products retain their existing checks; Required=false is not a PASS.
func (r *Runner) CheckDevelopmentBaseline(ctx context.Context, request ports.ClearDevBaselineRequest) (result ports.ClearDevBaselineResult, retErr error) {
	fail := func(reason string, err error) (ports.ClearDevBaselineResult, error) {
		result.ReasonCode = reason
		return result, fmt.Errorf("%s: %w", reason, err)
	}
	if strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.WorkspacePath) == "" {
		return fail("BASELINE_CHECKER_UNAVAILABLE", errors.New("missing service-owned baseline binding"))
	}
	head, err := baselineGit(ctx, request.WorkspacePath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !validCommit(strings.TrimSpace(head)) {
		return fail("BASELINE_WORKSPACE_INVALID", errors.New("cannot resolve the project HEAD"))
	}
	rootHead := strings.TrimSpace(head)
	required, err := mailBaselineRequired(ctx, request.WorkspacePath, rootHead)
	if err != nil {
		return fail("BASELINE_CHECKER_UNAVAILABLE", err)
	}
	if !required {
		return result, nil
	}
	sha, err := selectedBaselineSHA(ctx, request, rootHead)
	if err != nil {
		return fail("BASELINE_WORKSPACE_INVALID", err)
	}
	result.Required, result.CandidateSHA = true, sha
	if err := r.inspectUnchangedBaseline(ctx, request.WorkspacePath, rootHead); err != nil {
		return fail("BASELINE_WORKSPACE_INVALID", err)
	}
	// The approved npm test/start entry points and dependencies are frozen.
	// Renaming the package or replacing npm test with a no-op cannot opt out.
	for _, name := range []string{"package.json", "package-lock.json"} {
		got, _, readErr := readBaselineBlob(ctx, request.WorkspacePath, sha, name)
		want, frozenErr := core.DemoTemplateFS.ReadFile("testdata/complex-mail-app/" + name)
		if readErr != nil || frozenErr != nil || !bytes.Equal([]byte(got), want) {
			return fail("BASELINE_CONTRACT_CHANGED", fmt.Errorf("frozen %s does not match", name))
		}
	}
	if request.IdentifyOnly {
		return result, nil
	}
	commands := [][]string{{"npm", "test"}, {"node", "--input-type=module", "-e", baselineHealthProbe}}
	preflightID := request.RunID + ":environment"
	env, err := r.PrepareCandidateChecks(ctx, ports.ClearDevCheckPreflightRequest{
		RunID: preflightID, WorkspacePath: request.WorkspacePath, CandidateSHA: sha,
		Image: core.StandardCandidateCheckImage, Argv: commands, Timeout: 2 * time.Minute,
		MemoryBytes: baselineMemoryBytes, PidsLimit: 64,
	})
	if err != nil {
		return fail("BASELINE_CHECKER_UNAVAILABLE", err)
	}
	defer func() {
		// The durable preflight/check records remain; only the live cache
		// reference is released, including on a failed baseline.
		if err := r.ReleaseCandidateChecks(context.WithoutCancel(ctx), preflightID); err != nil && retErr == nil {
			result.ReasonCode = "BASELINE_CHECKER_UNAVAILABLE"
			retErr = err
		}
	}()
	if env.CandidateSHA != sha || env.CheckEnvironmentID == "" || env.ImageID == "" {
		return fail("BASELINE_CHECKER_UNAVAILABLE", errors.New("preflight returned incomplete or stale evidence"))
	}
	result.TestRunID, result.HealthRunID = request.RunID+":npm-test", request.RunID+":health"
	for index, argv := range commands {
		id, reason, timeout := result.TestRunID, "BASELINE_TEST_FAILED", time.Minute
		if index == 1 {
			id, reason, timeout = result.HealthRunID, "BASELINE_HEALTH_FAILED", 20*time.Second
		}
		checked, checkErr := r.RunCandidateCheck(ctx, ports.ClearDevCheckRequest{
			RunID: id, WorkspacePath: request.WorkspacePath, CandidateSHA: sha,
			Image: core.StandardCandidateCheckImage, Argv: argv, Timeout: timeout,
			MemoryBytes: baselineMemoryBytes, PidsLimit: 64, OutputLimit: 64 * 1024,
		})
		if index == 0 {
			result.Tests = checked
		} else {
			result.Health = checked
		}
		if checkErr != nil {
			return fail("BASELINE_CHECKER_UNAVAILABLE", checkErr)
		}
		if checked.Outcome == ports.ClearDevCheckInfraError || checked.Outcome == ports.ClearDevCheckTimedOut {
			return fail("BASELINE_CHECKER_UNAVAILABLE", fmt.Errorf("%s infrastructure outcome %s", id, checked.Outcome))
		}
		if checked.CandidateSHA != sha || checked.ImageID != env.ImageID || checked.CheckEnvironmentID == "" || checked.OutputSHA256 == "" {
			return fail("BASELINE_CHECKER_UNAVAILABLE", errors.New("check returned incomplete or stale candidate evidence"))
		}
		if checked.Outcome != ports.ClearDevCheckPass || checked.ExitCode != 0 || checked.TimedOut || checked.OutputTruncated {
			return fail(reason, fmt.Errorf("%s outcome %s, exit %d", id, checked.Outcome, checked.ExitCode))
		}
	}
	if err := r.inspectUnchangedBaseline(ctx, request.WorkspacePath, rootHead); err != nil {
		return fail("BASELINE_WORKSPACE_CHANGED", err)
	}
	current, err := selectedBaselineSHA(ctx, request, rootHead)
	if err != nil || current != sha {
		return fail("BASELINE_WORKSPACE_CHANGED", errors.New("selected baseline branch changed during its checks"))
	}
	return result, nil
}

func (r *Runner) inspectUnchangedBaseline(ctx context.Context, workspace, sha string) error {
	inspection, err := r.InspectCandidate(ctx, workspace, sha)
	if err != nil {
		return err
	}
	if inspection.BaseSHA != sha || inspection.CandidateSHA != sha || len(inspection.Paths) != 0 {
		return ports.ErrClearDevCandidateInvalid
	}
	return nil
}

func mailBaselineRequired(ctx context.Context, workspace, sha string) (bool, error) {
	roots, err := baselineGit(ctx, workspace, "rev-list", "--max-parents=0", "--max-count=17", sha)
	if err != nil {
		return false, err
	}
	commits := strings.Fields(roots)
	if len(commits) == 0 || len(commits) > 16 {
		return false, errors.New("baseline history roots cannot be determined within the fixed limit")
	}
	commits = append([]string{sha}, commits...)
	seen := make(map[string]bool)
	for _, commit := range commits {
		if seen[commit] {
			continue
		}
		seen[commit] = true
		if !validCommit(commit) {
			return false, ports.ErrClearDevCandidateInvalid
		}
		data, exists, err := readBaselineBlob(ctx, workspace, commit, "package.json")
		if err != nil {
			return false, err
		}
		if !exists {
			continue
		}
		var manifest struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(data), &manifest); err != nil {
			return false, errors.New("cannot identify a malformed baseline manifest")
		}
		if manifest.Name == "complex-mail-app" {
			return true, nil
		}
	}
	return false, nil
}

// Only fixed root manifests are read, from full Git IDs. A bounded command
// prevents candidate-controlled blobs/history from becoming unbounded output.
func readBaselineBlob(ctx context.Context, workspace, sha, name string) (string, bool, error) {
	entry, err := baselineGit(ctx, workspace, "ls-tree", sha, "--", name)
	if err != nil || entry == "" {
		return "", false, err
	}
	fields := strings.Fields(entry)
	if len(fields) != 4 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !validCommit(fields[2]) || fields[3] != name {
		return "", false, errors.New("baseline manifest must be a regular Git blob")
	}
	data, err := baselineGit(ctx, workspace, "cat-file", "blob", fields[2])
	return data, true, err
}

func baselineGit(ctx context.Context, workspace string, args ...string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	argv := append([]string{"--no-replace-objects", "-c", "core.fsmonitor=false", "-C", workspace}, args...)
	cmd := exec.CommandContext(bounded, "git", argv...) //nolint:gosec // service path, fixed commands and full Git IDs; no shell.
	output, stderr := newOutputCollector(64*1024, cancel), newOutputCollector(4096, cancel)
	cmd.Stdout, cmd.Stderr = output, stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("baseline Git read failed: %w", err)
	}
	if output.LimitExceeded() || stderr.LimitExceeded() {
		return "", errors.New("baseline Git read exceeded its output limit")
	}
	return output.String(), nil
}
