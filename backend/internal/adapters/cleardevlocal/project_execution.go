package cleardevlocal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// PrepareProjectExecution probes the existing immutable checker image, not the
// future application's tests. An EMPTY source is valid. Dependencies are still
// prepared against each actual candidate's manifests by the shared offline npm
// cache before any check can settle; no download or lifecycle script is hidden
// in admission. Missing infrastructure is an error, never a successful check.
func (r *Runner) PrepareProjectExecution(ctx context.Context, contract core.ProjectExecutionContract) error {
	if err := core.ValidateProjectExecutionContract(contract); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, checkPreparationTimeout)
	defer cancel()
	imageID, err := inspectCheckImage(ctx, projectInstallImage)
	if err != nil {
		return fmt.Errorf("%s: the pinned Node/npm checker image must be installed locally: %w", core.ReasonProjectRuntime, err)
	}
	request := ports.ClearDevCheckPreflightRequest{Image: core.StandardCandidateCheckImage, Timeout: 20 * time.Second, MemoryBytes: 512 * 1024 * 1024, PidsLimit: 64}
	for _, executable := range []string{"node", "npm"} {
		if _, err := probeCheckExecutable(ctx, request, imageID, executable); err != nil {
			return fmt.Errorf("%s: %w", core.ReasonProjectRuntime, err)
		}
	}
	return nil
}

// Copy the complete immutable contract so caller-owned slices cannot change
// an in-flight check after its request fingerprint has been saved. A project
// command must come from the admitted catalogue, including its timeout.
func normalizedProjectCheckContract(contract *core.ProjectExecutionContract, commands [][]string, timeout time.Duration) (*core.ProjectExecutionContract, error) {
	if contract == nil {
		return nil, nil
	}
	if err := core.ValidateProjectExecutionContract(*contract); err != nil {
		return nil, err
	}
	for _, argv := range commands {
		matched := core.ProjectTrialAllowsCommand(contract.Basis, argv, int(timeout/time.Second))
		for _, check := range contract.Basis.Checks {
			if slices.Equal(argv, check.Argv) && (timeout == 0 || timeout == time.Duration(check.TimeoutSeconds)*time.Second) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, errors.New("project check command or timeout is outside its admitted catalogue")
		}
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return nil, err
	}
	var copied core.ProjectExecutionContract
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return nil, err
	}
	return &copied, nil
}

// projectCheckContractDigest answers with the project's single canonical
// contract identity, the same one admission, receipts and the preview all
// compare. The earlier HTML-escaped json.Marshal spelling is only ever read
// back through the legacy-tolerant comparisons.
func projectCheckContractDigest(contract *core.ProjectExecutionContract) (string, error) {
	if contract == nil {
		return "", nil
	}
	return core.ProjectExecutionContractDigest(*contract)
}
