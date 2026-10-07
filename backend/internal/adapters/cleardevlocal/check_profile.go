package cleardevlocal

import (
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func validateNodeCheckProfile(profile string, runtime *core.ProjectRuntime) error {
	if profile == "" {
		return nil
	}
	if profile != core.NodeCheckSmallThreadsV1 || runtime == nil || runtime.Environment != core.ProjectRuntimeNodeNPMV1 {
		return errors.New("unknown or inapplicable Node check profile")
	}
	if value, declared := runtime.EnvironmentVariables["UV_THREADPOOL_SIZE"]; declared && value != "1" {
		return errors.New("node check profile conflicts with frozen UV_THREADPOOL_SIZE")
	}
	if _, declared := runtime.EnvironmentVariables["NODE_OPTIONS"]; declared {
		return errors.New("node check profile conflicts with frozen NODE_OPTIONS")
	}
	return nil
}

// Receipt reads are read-only. Absence means the historical profile, not the
// current default. Every other fingerprint field remains exact, and an explicit
// requested profile cannot borrow another profile's result or retry permission.
func checkReceiptFingerprint(request ports.ClearDevCheckRequest, state checkRunState) (string, error) {
	if request.ExecutionProfile != "" && request.ExecutionProfile != state.ExecutionProfile {
		return "", errors.New("check receipt executor profile changed")
	}
	request.ExecutionProfile = state.ExecutionProfile
	var runtime *core.ProjectRuntime
	if request.ProjectExecution != nil {
		runtime = &request.ProjectExecution.Runtime
	}
	if err := validateNodeCheckProfile(request.ExecutionProfile, runtime); err != nil {
		return "", err
	}
	return checkRequestSHA256(request)
}
