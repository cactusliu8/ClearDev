package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// prepareProjectExecutionAdmission runs inside StartClearDevComplexExecution's
// transaction. A deferred foreign key prevents an orphaned admission from ever
// committing. The caller still inserts and validates the original execution run.
func prepareProjectExecutionAdmission(ctx context.Context, q *gen.Queries, run core.ComplexExecutionRun) (*core.ComplexExecutionRun, error) {
	contract, project, err := core.ProjectAdmissionContractFromRun(run)
	if err != nil || !project {
		return nil, err
	}
	plan, err := q.GetClearDevProjectExecutionSourcePlan(ctx, run.PlanID)
	if err != nil {
		return nil, err
	}
	old, err := q.GetClearDevComplexExecutionRunForPlan(ctx, gen.GetClearDevComplexExecutionRunForPlanParams{
		RequirementVersionID: run.RequirementVersionID, PlanID: run.PlanID,
	})
	if err == nil {
		existing := complexExecutionRunFromGen(old)
		prior, admitted, err := core.ProjectAdmissionContractFromRun(existing)
		if err != nil || !admitted {
			return nil, complexExecutionRule("an existing execution cannot be upgraded to a project admission")
		}
		// A second click or concurrent retry may allocate another request/run ID.
		// Return the same execution without changing its immutable sources.
		contract.ExecutionRunID, contract.RequestID = prior.ExecutionRunID, prior.RequestID
		expected, err := core.CanonicalJSONBytes(prior)
		if err != nil {
			return nil, err
		}
		actual, err := core.CanonicalJSONBytes(contract)
		if err != nil || !bytes.Equal(expected, actual) || run.ModeReason != existing.ModeReason {
			return nil, complexExecutionRule("a repeated project request changed the existing admission")
		}
		if err := validateProjectExecutionRunAdmission(ctx, q, existing, plan); err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if _, err := validateProjectExecutionSource(ctx, q, run, plan, true); err != nil {
		return nil, err
	}
	encoded, err := core.CanonicalJSONBytes(contract)
	if err != nil {
		return nil, err
	}
	if err := q.InsertClearDevProjectExecutionAdmission(ctx, gen.InsertClearDevProjectExecutionAdmissionParams{
		ExecutionRunID: run.ID, StageID: contract.StageID, PlanID: contract.PlanID,
		ContractJson: string(encoded), ContractSha256: complexExecutionRawDigest(encoded), CreatedAt: run.CreatedAt,
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

// validateProjectExecutionSource replays the original settled Planner result
// against the confirmed specification, not a caller-supplied validation claim.
func validateProjectExecutionSource(ctx context.Context, q *gen.Queries, run core.ComplexExecutionRun, planRow gen.CleardevComplexEngineeringPlan, unused bool) (core.ProjectExecutionContract, error) {
	contract, project, err := core.ProjectAdmissionContractFromRun(run)
	if err != nil {
		return contract, err
	}
	if !project || planRow.ID != run.PlanID || planRow.PlanSha256 != run.PlanSHA256 ||
		planRow.RequirementVersionID != run.RequirementVersionID || planRow.RequirementSha256 != run.RequirementSHA256 ||
		planRow.DevelopmentProjectID != run.DevelopmentRequirementID {
		return contract, complexExecutionRule("project admission does not match its saved plan/specification")
	}
	if err := validatePlannerContractCommand(ctx, q, core.CreateComplexPlanCommand{Plan: clearDevComplexPlanFromGen(planRow)}, unused); err != nil {
		return contract, err
	}
	stage, err := projectPlanningStage(ctx, q, run.DevelopmentRequirementID)
	if err != nil {
		return contract, err
	}
	expected, err := core.BuildProjectExecutionContract(core.ProjectExecutionAdmission{
		RequestID: contract.RequestID, PlanID: planRow.ID, PlanSHA256: planRow.PlanSha256,
		RequirementSHA256: planRow.RequirementSha256, BaseCommitSHA: stage.BaseCommitSHA,
	}, stage, run.ID, run.RequirementVersionID)
	if err != nil {
		return contract, err
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return contract, err
	}
	actualJSON, err := json.Marshal(contract)
	if err != nil || !bytes.Equal(actualJSON, expectedJSON) {
		return contract, complexExecutionRule("project execution differs from the current immutable Stage source")
	}
	var plan core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(planRow.PlanJson), &plan); err != nil {
		return contract, err
	}
	if err := core.ValidateExecutableProjectPlan(plan, contract.Basis); err != nil {
		return contract, err
	}
	return contract, nil
}

// validateProjectExecutionRunAdmission checks the durable mirror as well as the
// run package. A V3 plan without this exact fact remains non-executable.
func validateProjectExecutionRunAdmission(ctx context.Context, q *gen.Queries, run core.ComplexExecutionRun, planRow gen.CleardevComplexEngineeringPlan) error {
	contract, err := validateProjectExecutionSource(ctx, q, run, planRow, false)
	if err != nil {
		return err
	}
	admission, err := q.GetClearDevProjectExecutionAdmission(ctx, run.ID)
	if err != nil {
		return err
	}
	canonical, err := core.CanonicalJSONBytes(contract)
	if err != nil {
		return err
	}
	// Admissions written before the canonical encoder used Go's HTML escaping;
	// accept that exact legacy byte form (with its own digest) as well.
	legacy, err := json.Marshal(contract)
	if err != nil {
		return err
	}
	matches := admission.ContractJson == string(canonical) && admission.ContractSha256 == complexExecutionRawDigest(canonical)
	if !matches {
		matches = admission.ContractJson == string(legacy) && admission.ContractSha256 == complexExecutionRawDigest(legacy)
	}
	if admission.StageID != contract.StageID || admission.PlanID != run.PlanID || !matches || !admission.CreatedAt.Equal(run.CreatedAt) {
		return complexExecutionRule("project execution lost its exact durable admission")
	}
	return nil
}

func projectExecutionCatalog(ctx context.Context, q *gen.Queries, requirementID string) ([]core.ComplexCheckSpec, bool, error) {
	row, err := q.GetClearDevProductStageByRequirement(ctx, nullableString(requirementID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	stage, err := productStageFromRow(row)
	if err != nil {
		return nil, false, err
	}
	if stage.Definition.ExecutionBasis == nil {
		return nil, false, nil
	}
	if err := core.ValidateProjectExecutionBasis(*stage.Definition.ExecutionBasis); err != nil {
		return nil, false, err
	}
	return stage.Definition.ExecutionBasis.CheckCatalog(), true, nil
}
