package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Explicit executor-receipt double for known ended preparation errors. It
// compares the complete original request; it does not run a real container.
type builderFailureReceiptHarness struct {
	*projectExecutionFlowHarness
	receipts map[string]ports.ClearDevCheckRequest
	unknown  bool
	readErr  error
}

func (h *builderFailureReceiptHarness) RunCandidateCheck(ctx context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	result, err := h.projectExecutionFlowHarness.RunCandidateCheck(ctx, request)
	if result.Outcome == ports.ClearDevCheckInfraError {
		h.receipts[request.RunID] = request
	}
	return result, err
}

func (h *builderFailureReceiptHarness) CanRetryWorkflowCheck(_ context.Context, request ports.ClearDevCheckRequest) (bool, error) {
	if h.readErr != nil {
		return false, h.readErr
	}
	original, found := h.receipts[request.RunID]
	// AllowInfraInject is deliberately excluded by the production fingerprint.
	original.AllowInfraInject, request.AllowInfraInject = false, false
	return !h.unknown && found && reflect.DeepEqual(original, request), nil
}

func knownBuilderFailureReceipt(runner *projectExecutionFlowHarness, requests ...ports.ClearDevCheckRequest) *builderFailureReceiptHarness {
	h := &builderFailureReceiptHarness{projectExecutionFlowHarness: runner, receipts: map[string]ports.ClearDevCheckRequest{}}
	for _, request := range requests {
		h.receipts[request.RunID] = request
	}
	return h
}

func TestUnifiedInfrastructureReceiptStopsRequiredAndIntegrationRepair(t *testing.T) {
	for _, kind := range []string{"historical-required", "integration"} {
		for _, proof := range []string{"missing-interface", "started", "unreleased-resources", "wrong-request", "unreadable"} {
			t.Run(kind+"/"+proof, func(t *testing.T) {
				var f *projectPlanningFixture
				var base *projectExecutionFlowHarness
				var before core.ComplexExecutionSnapshot
				if kind == "integration" {
					var h *unifiedFailureHarness
					f, h, before = settledIntegrationFailure(t, true, "infra")
					base = h.projectExecutionFlowHarness
				} else {
					var h *projectDependencyRecoveryHarness
					f, h, _, before = blockedProjectDependencyFixture(t)
					base = h.projectExecutionFlowHarness
					f.s.complexExecution = f.store
				}
				id := before.Run.DevelopmentRequirementID
				receipt := knownBuilderFailureReceipt(base, base.checkRequests...)
				switch proof {
				case "missing-interface":
					f.s.checks = base
				case "started", "unreleased-resources":
					receipt.unknown = true
					f.s.checks = receipt
				case "wrong-request":
					for id, request := range receipt.receipts {
						request.CandidateSHA = forty("d")
						receipt.receipts[id] = request
					}
					f.s.checks = receipt
				case "unreadable":
					receipt.readErr = errors.New("fixture native receipt unreadable")
					f.s.checks = receipt
				}
				oldChecks, _ := json.Marshal(before.CheckRuns)
				sends := len(base.relays)
				for range 4 {
					_, _, _ = f.s.advanceComplexStandardExecution(context.Background(), id)
				}
				after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
				checks, _ := json.Marshal(after.CheckRuns)
				if err != nil || after.Tasks[0].ReworkCount != 0 || len(after.Dispatches) != 1 || len(base.relays) != sends || string(checks) != string(oldChecks) {
					t.Fatal("unknown native execution changed rounds, sends or check history", err)
				}
			})
		}
	}
}

// Exact production diagnostic for a local RunID still marked started. This
// runner intentionally offers no settled receipt or cache-recovery authority.
type builderFailureUnknownHarness struct{ *projectExecutionFlowHarness }

func (h *builderFailureUnknownHarness) RunCandidateCheck(_ context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, error) {
	h.checkRequests = append(h.checkRequests, request)
	return ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError, ExitCode: -1}, errors.New("ClearDev check RunID has an unsettled external action")
}

func TestUnifiedFreshUnknownInfrastructureDoesNotReturnBuilder(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "historical"}[historical], func(t *testing.T) {
			f, child, admission, preparer := plannedProjectExecutionFixture(t, "EXISTING")
			if historical {
				f.s.complexExecution = &legacyFailureAdmissionStore{f.store}
			}
			h := &builderFailureUnknownHarness{attachProjectFlow(f, preparer)}
			f.s.checks = h
			id := child.Requirement.ID
			if _, err := f.s.StartProjectExecution(context.Background(), id, admission); err != nil {
				t.Fatal(err)
			}
			var before core.ComplexExecutionSnapshot
			found := false
			for range 100 {
				var err error
				before, _, err = f.store.GetClearDevComplexExecution(context.Background(), id)
				if err != nil {
					t.Fatal(err)
				}
				for _, check := range before.CheckRuns {
					found = found || check.Kind == core.CandidateCheckRequired && check.Status == core.ComplexExecutionCheckRunFailed
				}
				if found {
					break
				}
				changed, stop, err := f.s.advanceComplexStandardExecution(context.Background(), id)
				if err != nil || stop || !changed {
					t.Fatal("before infrastructure diagnostic", changed, stop, err)
				}
			}
			if !found {
				t.Fatal("missing persisted infrastructure diagnostic")
			}
			if historical {
				if changed, _, err := f.s.advanceComplexStandardExecution(context.Background(), id); err != nil || !changed {
					t.Fatal("old blocked transition", changed, err)
				}
				f.s.complexExecution = f.store
			}
			oldChecks, _ := json.Marshal(before.CheckRuns)
			sends := len(h.relays)
			for range 4 {
				_, _, _ = f.s.advanceComplexStandardExecution(context.Background(), id)
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), id)
			checks, _ := json.Marshal(after.CheckRuns)
			if err != nil || after.Tasks[0].Status != core.DevelopmentTaskStatusBlocked || after.Tasks[0].ReworkCount != 0 || len(after.Dispatches) != 1 || len(h.relays) != sends || string(checks) != string(oldChecks) {
				t.Fatal("unknown external action initiated repair or rewrote history", err)
			}
		})
	}
}

type builderFailureTrialReceiptHarness struct {
	*builderFailureReceiptHarness
	result   ports.ClearDevCheckResult
	found    bool
	readErr  error
	requests []ports.ClearDevCheckRequest
}

func (h *builderFailureTrialReceiptHarness) ReadCandidateCheck(_ context.Context, request ports.ClearDevCheckRequest) (ports.ClearDevCheckResult, bool, error) {
	h.requests = append(h.requests, request)
	return h.result, h.found, h.readErr
}

func TestUnifiedFinalCommandReceiptMustSettleBeforeBuilderReturn(t *testing.T) {
	f, base, before := settledHistoricalProjectFailure(t)
	// Prospective command contract only: never rewrite the stored review/package.
	// The persisted failure and task are used to verify rejected entry calls have
	// no side effects; positive receipt predicates are not claimed as full review.
	var pkg core.ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(before.Run.ExecutionPackageJSON), &pkg); err != nil {
		t.Fatal(err)
	}
	step := core.ProjectTrialStep{ID: "failure-cli", Kind: "COMMAND", Argv: []string{"node", "src/storage.ts"}, TimeoutSeconds: 30, AcceptanceCriteria: pkg.ProjectExecution.Basis.Trial.Steps[0].AcceptanceCriteria, Observe: "核对原命令失败与恢复"}
	pkg.ProjectExecution.Basis.Trial.Steps = []core.ProjectTrialStep{step}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	before.Run.ExecutionPackageJSON, before.Run.ExecutionPackageSHA256 = string(raw), coreDigest(raw)
	contract, project, err := core.ProjectContractFromRun(before.Run)
	if err != nil || !project || !core.BuilderFirstFailureEnabled(before.Run) {
		t.Fatal("invalid prospective command contract", err)
	}
	request := stageTrialCommandRequest(*before.FinalReview, contract, step)
	for _, mode := range []string{"missing-interface", "started", "unreadable", "unreleased-resources", "not-started", "ended-infra", "ended-fail", "ended-timeout"} {
		t.Run(mode, func(t *testing.T) {
			h := &builderFailureTrialReceiptHarness{builderFailureReceiptHarness: knownBuilderFailureReceipt(base, request), found: true, result: ports.ClearDevCheckResult{Outcome: ports.ClearDevCheckInfraError}, readErr: errors.New("fixture completed preparation failure")}
			f.s.checks = h
			switch mode {
			case "missing-interface":
				f.s.checks = base
			case "started", "unreadable":
				h.found = false
				h.readErr = errors.New("fixture trial action unresolved or unreadable")
			case "unreleased-resources":
				h.unknown = true
			case "not-started":
				h.found, h.readErr = false, nil
			case "ended-fail":
				h.result.Outcome, h.readErr = ports.ClearDevCheckFail, nil
			case "ended-timeout":
				h.result.Outcome, h.readErr = ports.ClearDevCheckTimedOut, nil
			}
			allowed := mode == "not-started" || mode == "ended-infra" || mode == "ended-fail" || mode == "ended-timeout"
			ready, _ := f.s.builderFirstFinalCommandsSettled(context.Background(), before)
			if ready != allowed {
				t.Fatal("wrong command settlement boundary", ready)
			}
			if len(h.requests) > 0 && !reflect.DeepEqual(h.requests[0], request) {
				t.Fatal("final failure probed a different command/reviewer/candidate")
			}
			if allowed {
				return
			}
			sends := len(base.relays)
			if _, changed, _ := f.s.advanceBuilderFirstFinalFailure(context.Background(), before); changed {
				t.Fatal("unresolved final command returned Builder")
			}
			after, _, err := f.store.GetClearDevComplexExecution(context.Background(), before.Run.DevelopmentRequirementID)
			if err != nil || after.Tasks[0].ReworkCount != 0 || len(after.Dispatches) != 1 || len(base.relays) != sends {
				t.Fatal("unknown command changed rounds or sent a message", err)
			}
		})
	}
}
