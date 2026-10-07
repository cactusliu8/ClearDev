package cleardev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// StartProjectExecution admits exactly the plan displayed by the workbench.
// It does not approve a requirement or accept commands, paths, candidates or
// sessions from the caller. Historical automatic/standard entry points retain
// their planning-only gate. All work uses the existing STANDARD execution loop.
func (s *Service) StartProjectExecution(ctx context.Context, requirementID string, admission core.ProjectExecutionAdmission) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}
	requirementID = strings.TrimSpace(requirementID)
	if requirementID == "" || admission.RequestID == "" || admission.RequestID != strings.TrimSpace(admission.RequestID) || len(admission.RequestID) > 200 {
		return RequirementView{}, apierr.Invalid("PROJECT_EXECUTION_REQUEST_INVALID", "A requirement and explicit execution request are required", nil)
	}
	if s.facts == nil || s.complex == nil || s.complexExecution == nil || s.direction == nil || s.finalReviews == nil || s.ao == nil || s.sessions == nil || s.chat == nil || s.inspector == nil || s.checks == nil {
		return RequirementView{}, apierr.Internal("PROJECT_EXECUTION_UNAVAILABLE", "Project execution requires the task engine, candidate checker and independent final reviewer")
	}
	preparer, supported := s.checks.(ports.ClearDevProjectExecutionPreparer)
	if !supported {
		return RequirementView{}, apierr.Conflict(string(core.ReasonProjectRuntime), "The configured checker does not support versioned Node/npm project execution", nil)
	}
	stage, linked, err := s.productStageSource(ctx, requirementID)
	if err != nil {
		return RequirementView{}, err
	}
	if !linked || stage.Definition.ExecutionBasis == nil || stage.Selection == nil {
		return RequirementView{}, apierr.Conflict("PROJECT_EXECUTION_SOURCE_REQUIRED", "Project execution requires a selected generic Stage", nil)
	}
	state, err := s.projectPlanningState(ctx, requirementID)
	if err != nil {
		return RequirementView{}, err
	}
	if state == nil || !state.Current || !state.SourceCurrent || state.ReasonCode != "" {
		return RequirementView{}, apierr.Conflict("PROJECT_EXECUTION_SOURCE_CHANGED", "The selected Stage or repository baseline is no longer current", nil)
	}
	snapshot, found, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, err
	}
	version, confirmed := currentConfirmedVersion(snapshot)
	if !found || !confirmed || snapshot.Requirement.CancelledAt != nil {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Project execution requires the current human-confirmed specification", nil)
	}
	planning, found, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil {
		return RequirementView{}, err
	}
	plan, parsed, ready := savedProjectExecutionPlan(planning, version.ID)
	if !found || !ready || admission.PlanID != plan.ID || admission.PlanSHA256 != plan.PlanSHA256 || admission.RequirementSHA256 != version.SHA256 || admission.BaseCommitSHA != stage.BaseCommitSHA {
		return RequirementView{}, apierr.Conflict("PROJECT_EXECUTION_BINDING_CHANGED", "The execution request does not match the current saved plan, specification and baseline", nil)
	}
	if err := core.ValidateExecutableProjectPlan(parsed, *stage.Definition.ExecutionBasis); err != nil {
		return RequirementView{}, apierr.Conflict(string(core.ReasonProjectRuntime), err.Error(), nil)
	}
	steward, bound := core.ComplexRoleBindingByRole(planning, core.StandardRoleSteward)
	if !bound || steward.Status != core.RoleBindingStatusBound || steward.AOSessionID == "" {
		return RequirementView{}, apierr.Conflict("STEWARD_BINDING_MISSING", "The saved plan has no original Project Steward session binding", nil)
	}
	runID := s.newID()
	contract, err := core.BuildProjectExecutionContract(admission, stage, runID, version.ID)
	if err != nil {
		return RequirementView{}, apierr.Conflict("PROJECT_EXECUTION_CONTRACT_INVALID", err.Error(), nil)
	}
	existing, exists, err := s.complexExecution.GetClearDevComplexExecution(ctx, requirementID)
	if err != nil {
		return RequirementView{}, err
	}
	if exists && existing.Run.RequirementVersionID == version.ID {
		prior, project, err := core.ProjectAdmissionContractFromRun(existing.Run)
		if err != nil || !project || prior.PlanID != contract.PlanID || prior.PlanSHA256 != contract.PlanSHA256 {
			return RequirementView{}, apierr.Conflict("PROJECT_EXECUTION_ALREADY_BOUND", "An existing execution cannot be upgraded or rebound to a different plan", nil)
		}
		contract.ExecutionRunID, contract.RequestID = prior.ExecutionRunID, prior.RequestID
		oldJSON, oldErr := json.Marshal(prior)
		newJSON, newErr := json.Marshal(contract)
		if oldErr != nil || newErr != nil || !bytes.Equal(oldJSON, newJSON) {
			return RequirementView{}, apierr.Conflict("PROJECT_EXECUTION_BINDING_CHANGED", "The existing admission no longer matches the selected sources", nil)
		}
		if existing.Run.CompletedAt == nil {
			s.scheduleComplexStandardExecution(requirementID)
		}
		return s.GetRequirement(ctx, requirementID)
	}
	if version.TaskSetVersion != 0 {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Project execution requires an unused confirmed task set", nil)
	}
	if err := preparer.PrepareProjectExecution(ctx, contract); err != nil {
		return RequirementView{}, apierr.Conflict(string(core.ReasonProjectRuntime), err.Error(), nil)
	}
	// Preparation can take time. Re-observe the external repository before the
	// transaction; SQLite independently rechecks the durable Stage/spec/plan.
	if !s.selectedProjectCurrent(ctx, stage.Selection) {
		return RequirementView{}, apierr.Conflict("PROJECT_EXECUTION_SOURCE_CHANGED", "The repository changed during execution preparation", nil)
	}
	selection := core.ProjectExecutionMode(parsed)
	raw, _, err := core.BuildComplexExecutionRunPackage(selection.Mode, runID, version.ID, version.SHA256, plan.ID, plan.PlanSHA256)
	if err != nil {
		return RequirementView{}, err
	}
	raw, digest, err := core.BindProjectExecutionPolicy(raw, contract)
	if err != nil {
		return RequirementView{}, err
	}
	run := core.ComplexExecutionRun{
		ID: runID, DevelopmentRequirementID: requirementID, RequirementVersionID: version.ID, RequirementSHA256: version.SHA256,
		PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, Mode: selection.Mode, ModeReason: string(selection.ReasonCode), FixedBuilderCount: 1,
		ExpectedTaskSetVersion: 0, TaskSetVersion: core.ComplexStandardTaskSetVersion, ExecutionPackageJSON: string(raw), ExecutionPackageSHA256: digest,
		StewardRoleBindingID: steward.ID, CreatedAt: s.now().UTC(),
	}
	if _, _, err := s.complexExecution.StartClearDevComplexExecution(ctx, core.StartComplexExecutionCommand{Run: run}); err != nil {
		return RequirementView{}, mapStoreError(err, "PROJECT_EXECUTION_ADMISSION_FAILED")
	}
	s.scheduleComplexStandardExecution(requirementID)
	return s.GetRequirement(ctx, requirementID)
}

func savedProjectExecutionPlan(planning core.ComplexPlanningSnapshot, versionID string) (core.ComplexEngineeringPlan, core.ComplexEngineeringPlanResult, bool) {
	var best core.ComplexEngineeringPlan
	for _, plan := range planning.Plans {
		if plan.RequirementVersionID == versionID && (best.ID == "" || plan.Version > best.Version) {
			best = plan
		}
	}
	var parsed core.ComplexEngineeringPlanResult
	if best.ID == "" || coreDigest([]byte(best.PlanJSON)) != best.PlanSHA256 || json.Unmarshal([]byte(best.PlanJSON), &parsed) != nil || parsed.SchemaVersion != core.ProjectPlanningVersion || parsed.Kind != "COMPLEX_ENGINEERING_PLAN" {
		return best, parsed, false
	}
	return best, parsed, true
}

// executionPlanForRun never makes a saved V3 executable on its own. Recovery
// and dispatch must possess the exact immutable run grant and current source.
func (s *Service) executionPlanForRun(ctx context.Context, planning core.ComplexPlanningSnapshot, run core.ComplexExecutionRun) (core.ComplexEngineeringPlan, core.ComplexPlanReview, core.ComplexEngineeringPlanResult, error) {
	contract, project, err := core.ProjectContractFromRun(run)
	if err != nil {
		return core.ComplexEngineeringPlan{}, core.ComplexPlanReview{}, core.ComplexEngineeringPlanResult{}, err
	}
	if !project {
		plan, review, ok := approvedComplexExecutionPlan(planning, run.RequirementVersionID)
		if !ok {
			return plan, review, core.ComplexEngineeringPlanResult{}, errComplexExecutionStopped
		}
		parsed, err := parseComplexExecutionPlan(plan)
		return plan, review, parsed, err
	}
	plan, parsed, ready := savedProjectExecutionPlan(planning, run.RequirementVersionID)
	if !ready || plan.ID != run.PlanID || plan.PlanSHA256 != run.PlanSHA256 || plan.RequirementSHA256 != run.RequirementSHA256 {
		return plan, core.ComplexPlanReview{}, parsed, errComplexExecutionStopped
	}
	stage, linked, err := s.productStageSource(ctx, run.DevelopmentRequirementID)
	if err != nil || !linked {
		return plan, core.ComplexPlanReview{}, parsed, errors.Join(errComplexExecutionStopped, err)
	}
	expected, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: contract.RequestID, PlanID: run.PlanID, PlanSHA256: run.PlanSHA256, RequirementSHA256: run.RequirementSHA256, BaseCommitSHA: stage.BaseCommitSHA,
	}, stage, run.ID, run.RequirementVersionID)
	if err != nil {
		return plan, core.ComplexPlanReview{}, parsed, err
	}
	original, _, originalErr := core.ProjectAdmissionContractFromRun(run)
	if originalErr != nil {
		return plan, core.ComplexPlanReview{}, parsed, originalErr
	}
	oldJSON, oldErr := json.Marshal(original)
	newJSON, newErr := json.Marshal(expected)
	state, stateErr := s.projectPlanningState(ctx, run.DevelopmentRequirementID)
	if oldErr != nil || newErr != nil || !bytes.Equal(oldJSON, newJSON) || stateErr != nil || state == nil || !state.Current || !state.SourceCurrent || state.ReasonCode != "" {
		return plan, core.ComplexPlanReview{}, parsed, errComplexExecutionStopped
	}
	if err := core.ValidateExecutableProjectPlan(parsed, contract.Basis); err != nil {
		return plan, core.ComplexPlanReview{}, parsed, err
	}
	return plan, core.ComplexPlanReview{}, core.ProjectExecutionPlan(parsed, contract.Basis), nil
}
