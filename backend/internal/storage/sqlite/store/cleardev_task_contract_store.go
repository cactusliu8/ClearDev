package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// validatePlannerContractCommand replays deterministic admission from the
// settled Planner message, not from a caller's assertion that a plan is valid.
// The plan and its admission are appended in the same transaction.
func validatePlannerContractCommand(ctx context.Context, q *gen.Queries, command core.CreateComplexPlanCommand, requireUnused bool) error {
	plan := command.Plan
	var envelope struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(plan.PlanJSON), &envelope); err != nil {
		return complexExecutionRule("invalid Planner result JSON")
	}
	// Identify the Stage before the legacy protocol fast path. Missing versions
	// must not turn a generic project into a historical, unvalidated plan.
	stageRow, stageErr := q.GetClearDevProductStageByRequirement(ctx, nullableString(plan.DevelopmentRequirementID))
	if stageErr != nil && !errors.Is(stageErr, sql.ErrNoRows) {
		return stageErr
	}
	if stageErr == nil {
		stage, err := productStageFromRow(stageRow)
		if err != nil {
			return err
		}
		if stage.Definition.ExecutionBasis != nil {
			planningOnly := envelope.SchemaVersion == core.ProjectPlanningVersion && envelope.Kind == "COMPLEX_ENGINEERING_PLAN"
			clarification := envelope.SchemaVersion == core.PlannerTaskContractVersion && envelope.Kind == core.PlannerClarificationKind
			if command.Validation != nil || (!planningOnly && !clarification) {
				return complexExecutionRule("project plan requires the planning-only protocol or product clarification")
			}
			if _, err := projectPlanningStage(ctx, q, plan.DevelopmentRequirementID); err != nil {
				return err
			}
		}
	}
	if envelope.SchemaVersion != core.PlannerTaskContractVersion && envelope.SchemaVersion != core.ProjectPlanningVersion {
		if command.Validation != nil || envelope.SchemaVersion > core.ComplexProtocolVersion {
			return complexExecutionRule("only a V2 Task Contract can receive deterministic admission")
		}
		return nil // Keep historical V1 creation and replay semantics.
	}
	var projectStage core.ProductStage
	if envelope.SchemaVersion == core.ProjectPlanningVersion {
		if command.Validation != nil || envelope.Kind != "COMPLEX_ENGINEERING_PLAN" {
			return complexExecutionRule("planning-only results cannot acquire executable admission")
		}
		var err error
		projectStage, err = projectPlanningStage(ctx, q, plan.DevelopmentRequirementID)
		if err != nil {
			return err
		}
	}
	version, err := q.GetClearDevRequirementVersion(ctx, plan.RequirementVersionID)
	if err != nil {
		return err
	}
	if version.State != "APPROVED" || version.SupersededByID.Valid || (requireUnused && version.TaskSetVersion != 0) ||
		version.DevelopmentProjectID != plan.DevelopmentRequirementID || version.Sha256 != plan.RequirementSHA256 ||
		!requirementDigestMatches(version.ContractText, version.Sha256) {
		return complexExecutionRule("Task Contract requires the unused current confirmed Stage")
	}
	compilations, err := q.ListClearDevPlannerReadyCompilations(ctx, gen.ListClearDevPlannerReadyCompilationsParams{
		DevelopmentProjectID: plan.DevelopmentRequirementID, CompilationSha256: plan.CompilationSHA256,
		NormalizedRequirementJson: version.ContractText,
	})
	if err != nil {
		return err
	}
	if len(compilations) == 0 || !requirementDigestMatches(version.ContractText, plan.CompilationSHA256) {
		return complexExecutionRule("Task Contract does not bind the confirmed Stage's READY compilation")
	}
	step, err := q.GetClearDevComplexAgentStep(ctx, plan.AgentStepID)
	if err != nil {
		return err
	}
	if step.SendStatus != string(core.AgentStepSendStatusSettled) || step.StepKind != string(core.ComplexAgentStepEngineeringPlan) ||
		step.RoleBindingID != plan.PlannerRoleBindingID || step.RequestID != plan.PlanningRequestID ||
		step.TurnID.String != plan.TurnID || step.FinalMessageID.String != plan.FinalMessageID ||
		!requirementDigestMatches(step.FinalMessageText.String, step.MessageSha256.String) {
		return complexExecutionRule("Task Contract must bind the exact settled Planner result")
	}
	var canonical []byte
	var digest string
	if envelope.Kind == core.PlannerClarificationKind {
		if command.Validation != nil {
			return complexExecutionRule("product clarification cannot authorize execution")
		}
		_, canonical, digest, err = core.ParsePlannerProductClarification([]byte(step.FinalMessageText.String), plan.PlanningRequestID, plan.RequirementVersionID, plan.RequirementSHA256, plan.CompilationSHA256)
	} else {
		var document core.NormalizedRequirementDocument
		if err := json.Unmarshal([]byte(version.ContractText), &document); err != nil {
			return complexExecutionRule("Task Contract has no normalized confirmed acceptance")
		}
		if envelope.SchemaVersion == core.ProjectPlanningVersion {
			_, canonical, digest, err = core.ParseProjectEngineeringPlanResult([]byte(step.FinalMessageText.String), plan.PlanningRequestID, plan.RequirementVersionID, plan.RequirementSHA256, plan.CompilationSHA256, core.CoverageFromNormalizedDocument(document), *projectStage.Definition.ExecutionBasis)
			if err != nil || string(canonical) != plan.PlanJSON || digest != plan.PlanSHA256 {
				return complexExecutionRule("project plan differs from its exact Planner result or confirmed basis")
			}
			return nil
		}
		_, catalog, catalogErr := benchmarkExecutionPolicyAndCatalog(ctx, q, plan.DevelopmentRequirementID)
		if catalogErr != nil {
			return catalogErr
		}
		_, canonical, digest, err = core.ParsePlannerTaskContractResult([]byte(step.FinalMessageText.String), plan.PlanningRequestID, plan.RequirementVersionID, plan.RequirementSHA256, plan.CompilationSHA256, core.CoverageFromNormalizedDocument(document), catalog)
		if err != nil {
			return complexExecutionRule("Planner Task Contract failed deterministic validation: " + err.Error())
		}
		catalogSHA, hashErr := core.ComplexCheckCatalogSHA256(catalog)
		if hashErr != nil {
			return hashErr
		}
		validation := command.Validation
		if validation == nil || validation.PlanID != plan.ID || validation.PlanSHA256 != digest ||
			validation.Policy != core.PlannerTaskContractPolicy || validation.CheckCatalogSHA256 != catalogSHA ||
			!validation.CreatedAt.Equal(plan.CreatedAt) {
			return complexExecutionRule("Task Contract admission does not bind the exact plan and approved check catalog")
		}
	}
	if err != nil || string(canonical) != plan.PlanJSON || digest != plan.PlanSHA256 {
		return complexExecutionRule("saved Task Contract differs from the settled Planner result")
	}
	return nil
}

// Execution rechecks the stored admission and source result; it does not
// reinterpret an old Steward approval as a new contract, or vice versa.
func validatePlannerContractRun(ctx context.Context, q *gen.Queries, run core.ComplexExecutionRun, planRow gen.CleardevComplexEngineeringPlan) error {
	contract, err := core.PlannerTaskContractRun(run)
	if err != nil {
		return err
	}
	var plan core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(planRow.PlanJson), &plan); err != nil {
		return err
	}
	_, project, projectErr := core.ProjectContractFromRun(run)
	if projectErr != nil {
		return projectErr
	}
	if plan.SchemaVersion == core.ProjectPlanningVersion {
		if !project {
			return complexExecutionRule("generic project planning does not authorize execution")
		}
		return validateProjectExecutionRunAdmission(ctx, q, run, planRow)
	}
	if project {
		return complexExecutionRule("project execution cannot reinterpret an older plan protocol")
	}
	if contract != (plan.SchemaVersion == core.PlannerTaskContractVersion) {
		return complexExecutionRule("run admission policy does not match the immutable plan protocol")
	}
	if !contract {
		return nil
	}
	validation, err := q.GetClearDevComplexPlanValidation(ctx, planRow.ID)
	if err != nil {
		return err
	}
	fact := complexPlanValidationFromGen(validation)
	return validatePlannerContractCommand(ctx, q, core.CreateComplexPlanCommand{Plan: clearDevComplexPlanFromGen(planRow), Validation: &fact}, false)
}

func complexPlanValidationFromGen(row gen.CleardevComplexPlanValidation) core.ComplexPlanValidation {
	return core.ComplexPlanValidation{
		PlanID: row.PlanID, PlanSHA256: row.PlanSha256, Policy: row.Policy,
		CheckCatalogSHA256: row.CheckCatalogSha256, CreatedAt: row.CreatedAt,
	}
}

func insertPlannerContractValidation(ctx context.Context, q *gen.Queries, validation *core.ComplexPlanValidation) error {
	if validation == nil {
		return nil
	}
	return q.InsertClearDevComplexPlanValidation(ctx, gen.InsertClearDevComplexPlanValidationParams{
		PlanID: validation.PlanID, PlanSha256: validation.PlanSHA256, Policy: validation.Policy,
		CheckCatalogSha256: validation.CheckCatalogSHA256, CreatedAt: validation.CreatedAt,
	})
}

func verifyPlannerContractReplay(ctx context.Context, q *gen.Queries, existing gen.CleardevComplexEngineeringPlan, command core.CreateComplexPlanCommand) error {
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal([]byte(existing.PlanJson), &envelope); err != nil {
		return err
	}
	if envelope.SchemaVersion != core.PlannerTaskContractVersion && envelope.SchemaVersion != core.ProjectPlanningVersion && command.Validation == nil {
		return nil
	}
	plan := command.Plan
	if existing.ID != plan.ID || existing.PlanJson != plan.PlanJSON || existing.PlanSha256 != plan.PlanSHA256 ||
		existing.DevelopmentProjectID != plan.DevelopmentRequirementID || existing.Version != plan.Version || !existing.CreatedAt.Equal(plan.CreatedAt) ||
		existing.PlanningRequestID != plan.PlanningRequestID || existing.AgentStepID != plan.AgentStepID ||
		existing.RequirementVersionID != plan.RequirementVersionID || existing.RequirementSha256 != plan.RequirementSHA256 ||
		existing.CompilationSha256 != plan.CompilationSHA256 || existing.PlannerRoleBindingID != plan.PlannerRoleBindingID ||
		existing.TurnID != plan.TurnID || existing.FinalMessageID != plan.FinalMessageID {
		return complexExecutionRule("a changed contract cannot reuse an existing plan identity")
	}
	if envelope.SchemaVersion == core.ProjectPlanningVersion {
		if command.Validation != nil {
			return complexExecutionRule("project plan replay cannot acquire execution admission")
		}
		return nil
	}
	if _, clarification := core.PlannerClarificationForPlan(plan); clarification {
		if command.Validation != nil {
			return complexExecutionRule("a clarification cannot be replayed as an execution admission")
		}
		return nil
	}
	validation, err := q.GetClearDevComplexPlanValidation(ctx, plan.ID)
	if err != nil {
		return err
	}
	if command.Validation == nil || validation.PlanID != command.Validation.PlanID || validation.PlanSha256 != command.Validation.PlanSHA256 ||
		validation.Policy != command.Validation.Policy || validation.CheckCatalogSha256 != command.Validation.CheckCatalogSHA256 ||
		!validation.CreatedAt.Equal(command.Validation.CreatedAt) {
		return complexExecutionRule("contract replay does not match its immutable admission")
	}
	return nil
}
