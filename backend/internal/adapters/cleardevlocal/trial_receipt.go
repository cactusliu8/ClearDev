package cleardevlocal

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ReadCandidateCheck reads settled evidence without starting or retrying a command.
func (r *Runner) ReadCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, bool, error) {
	var empty ports.ClearDevCheckResult
	if err := ctx.Err(); err != nil {
		return empty, false, err
	}
	request, err := normalizeCheckRequest(request)
	if err != nil || request.RunID == "" {
		return empty, false, errors.New("trial receipt requires a valid frozen request")
	}
	path, err := checkRunStatePath(request.RunID)
	if err != nil {
		return empty, false, err
	}
	raw, err := readJSONStateBytes(path, "ClearDev check RunID")
	if errors.Is(err, os.ErrNotExist) {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, err
	}
	var state checkRunState
	if err := json.Unmarshal(raw, &state); err != nil {
		return empty, false, err
	}
	digest, err := checkReceiptFingerprint(request, state)
	if err != nil {
		return empty, false, err
	}
	if state.Version != checkRunStateVersion || state.Fingerprint != digest {
		return empty, false, errors.New("trial receipt binding changed")
	}
	if state.State != "settled" || state.Result == nil {
		return empty, false, errors.New("trial action is unresolved; do not repeat it")
	}
	if state.Error != "" {
		return *state.Result, true, errors.New(state.Error)
	}
	if state.SourceManifest == nil || verifySavedCandidateManifest(*state.SourceManifest, checkEnvironmentForResult(*state.Result)) != nil {
		return empty, false, errors.New("trial source evidence is invalid")
	}
	return *state.Result, true, nil
}
