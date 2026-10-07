package cleardev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

const (
	// ProjectExecutionPolicyV1 admits a saved Plan V3 without changing its planning-only semantics.
	ProjectExecutionPolicyV1 = "PROJECT_EXECUTION_V1"
	// ProjectExecutionProtocolVersion identifies tasks created from an explicit project admission.
	ProjectExecutionProtocolVersion = 4
	// ReasonProjectAdmission requires the human's explicit start of the current saved plan.
	ReasonProjectAdmission ReasonCode = "PROJECT_EXECUTION_ADMISSION_REQUIRED"
	// ReasonProjectRuntime reports missing or unsupported local execution prerequisites.
	ReasonProjectRuntime ReasonCode = "PROJECT_RUNTIME_UNSUPPORTED"
)

// ProjectExecutionAdmission is an explicit request for the exact displayed
// plan. It cannot carry commands, change scope or approve a specification.
type ProjectExecutionAdmission struct {
	RequestID         string `json:"requestId"`
	PlanID            string `json:"planId"`
	PlanSHA256        string `json:"planSha256"`
	RequirementSHA256 string `json:"requirementSha256"`
	BaseCommitSHA     string `json:"baseCommitSha"`
}

// ProjectExecutionContract binds the execution to the current confirmed sources.
// The admission fact and run envelope must contain the same contract, in one
// transaction. Neither the existence of a Plan V3 nor task completion is a grant.
type ProjectExecutionContract struct {
	Policy                string                `json:"policy"`
	RequestID             string                `json:"requestId"`
	ExecutionRunID        string                `json:"executionRunId"`
	ProductID             string                `json:"productId"`
	DiscussionID          string                `json:"discussionId"`
	StageID               string                `json:"stageId"`
	StageDefinitionSHA256 string                `json:"stageDefinitionSha256"`
	Selection             ProductSelection      `json:"selection"`
	SelectionSHA256       string                `json:"selectionSha256"`
	BaseCommitSHA         string                `json:"baseCommitSha"`
	RequirementVersionID  string                `json:"requirementVersionId"`
	RequirementSHA256     string                `json:"requirementSha256"`
	PlanID                string                `json:"planId"`
	PlanSHA256            string                `json:"planSha256"`
	Basis                 ProjectExecutionBasis `json:"basis"`
	Runtime               ProjectRuntime        `json:"runtime"`
}

// BuildProjectExecutionContract consumes backend-assigned IDs and the immutable
// selected Stage. Current plan/spec/source checks are repeated by the service
// and storage; this pure constructor does not grant authority by itself.
func BuildProjectExecutionContract(admission ProjectExecutionAdmission, stage ProductStage, runID, versionID string) (ProjectExecutionContract, error) {
	var contract ProjectExecutionContract
	if stage.Selection == nil || stage.Definition.ExecutionBasis == nil || stage.DevelopmentRequirementID == "" || stage.BaseCommitSHA != stage.Selection.BaseCommitSHA {
		return contract, errors.New("project execution requires a selected and prepared Stage")
	}
	selection, err := marshalCanonicalJSON(stage.Selection)
	if err != nil {
		return contract, err
	}
	basis, err := marshalCanonicalJSON(stage.Definition.ExecutionBasis)
	if err != nil {
		return contract, err
	}
	// Copy nested slices/maps as well: constructing a run must not mutate its
	// persisted planning input or retain aliases to caller-owned proposal data.
	if err := json.Unmarshal(selection, &contract.Selection); err != nil {
		return contract, err
	}
	if err := json.Unmarshal(basis, &contract.Basis); err != nil {
		return contract, err
	}
	contract.Policy, contract.RequestID, contract.ExecutionRunID = ProjectExecutionPolicyV1, admission.RequestID, runID
	contract.ProductID, contract.DiscussionID, contract.StageID = stage.ProductID, stage.DiscussionID, stage.ID
	contract.StageDefinitionSHA256, contract.SelectionSHA256 = stage.DefinitionSHA256, sha256Hex(selection)
	contract.BaseCommitSHA, contract.RequirementVersionID = admission.BaseCommitSHA, versionID
	contract.RequirementSHA256, contract.PlanID, contract.PlanSHA256 = admission.RequirementSHA256, admission.PlanID, admission.PlanSHA256
	contract.Runtime, err = ResolveProjectRuntime(contract.Basis)
	if err != nil {
		return contract, err
	}
	return contract, ValidateProjectExecutionContract(contract)
}

// ValidateProjectExecutionContract rejects partial, drifted and unsupported
// execution contracts. This is not a replacement for checking durable sources.
func ValidateProjectExecutionContract(contract ProjectExecutionContract) error {
	if contract.Policy != ProjectExecutionPolicyV1 || !validExecutionIdentifier(contract.RequestID) ||
		!validExecutionIdentifier(contract.ExecutionRunID) || !validExecutionIdentifier(contract.RequirementVersionID) ||
		!validExecutionIdentifier(contract.PlanID) || !validProtocolSHA256(contract.PlanSHA256) ||
		!validProtocolSHA256(contract.RequirementSHA256) || !mailSHA1(contract.BaseCommitSHA) ||
		!validExecutionIdentifier(contract.ProductID) || !validExecutionIdentifier(contract.DiscussionID) ||
		!validExecutionIdentifier(contract.StageID) || !validProtocolSHA256(contract.StageDefinitionSHA256) ||
		contract.Selection.AOProjectID == "" || contract.Selection.RepositoryPath == "" || contract.Selection.BaseCommitSHA != contract.BaseCommitSHA ||
		!validExecutionIdentifier(contract.Selection.SourceDiscussionID) || !validText(contract.Selection.Reason, 10000) {
		return errors.New("project execution contract lost its immutable source or admission")
	}
	if err := ValidateProductDeliveryBaseline(contract.Selection); err != nil {
		return err
	}
	if err := validateProductOptions(ProductDiscoveryResult{Options: []ProductOption{contract.Selection.Option}, Evidence: []ProductEvidence{}}); err != nil {
		return err
	}
	selection, err := marshalCanonicalJSON(contract.Selection)
	if err != nil || sha256Hex(selection) != contract.SelectionSHA256 {
		return errors.New("project execution selection digest does not match")
	}
	runtime, err := ResolveProjectRuntime(contract.Basis)
	if err != nil {
		return err
	}
	expected, err := marshalCanonicalJSON(runtime)
	if err != nil {
		return err
	}
	actual, err := marshalCanonicalJSON(contract.Runtime)
	if err != nil || !bytes.Equal(actual, expected) {
		return errors.New("project runtime differs from its frozen versioned basis")
	}
	return nil
}

// BindProjectExecutionPolicy binds only a fresh STANDARD run. Existing run rows
// are immutable and must never be upgraded in place to the new execution path.
func BindProjectExecutionPolicy(raw []byte, contract ProjectExecutionContract) ([]byte, string, error) {
	if err := ValidateProjectExecutionContract(contract); err != nil {
		return nil, "", err
	}
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, "", err
	}
	if pkg.SchemaVersion != ComplexExecutionProtocolVersion || pkg.Mode != string(WorkModeStandard) ||
		pkg.ExecutionRunID != contract.ExecutionRunID || pkg.RequirementVersionID != contract.RequirementVersionID ||
		pkg.PlanID != contract.PlanID || pkg.PlanSHA256 != contract.PlanSHA256 || pkg.RequirementSHA256 != contract.RequirementSHA256 ||
		pkg.TaskSetVersion != ComplexStandardTaskSetVersion || pkg.DeliveryPolicy != "" || pkg.DeliveryBaseSHA != "" || pkg.ProjectExecution != nil ||
		pkg.AttemptPolicy != "" || pkg.FreezePolicy != "" || pkg.PlanValidationPolicy != "" || pkg.PlannerCoordinationPolicy != "" || pkg.BuilderFailurePolicy != "" {
		return nil, "", errors.New("project execution can only bind a fresh exact STANDARD run")
	}
	pkg.PlanValidationPolicy, pkg.FinalReviewPolicy = ProjectExecutionPolicyV1, RequirementFinalReviewPolicyV1
	pkg.ProjectExecution = &contract
	pkg.BuilderFailurePolicy = BuilderFirstFailurePolicyV1
	encoded, err := marshalCanonicalJSON(pkg)
	return encoded, sha256Hex(encoded), err
}

// ProjectContractFromRun validates the complete immutable project binding. An
// unmarked historical run remains historical; partially marked runs fail closed.
func ProjectAdmissionContractFromRun(run ComplexExecutionRun) (ProjectExecutionContract, bool, error) {
	var pkg ComplexExecutionRunPackage
	if run.ExecutionPackageJSON == "" {
		return ProjectExecutionContract{}, false, nil
	}
	if err := json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg); err != nil {
		return ProjectExecutionContract{}, false, err
	}
	if pkg.ProjectExecution == nil && pkg.PlanValidationPolicy != ProjectExecutionPolicyV1 {
		return ProjectExecutionContract{}, false, nil
	}
	if pkg.ProjectExecution == nil || pkg.PlanValidationPolicy != ProjectExecutionPolicyV1 ||
		pkg.SchemaVersion != ComplexExecutionProtocolVersion || pkg.Mode != string(WorkModeStandard) || run.Mode != WorkModeStandard || run.FixedBuilderCount != 1 ||
		pkg.ExecutionRunID != run.ID || pkg.RequirementVersionID != run.RequirementVersionID || pkg.RequirementSHA256 != run.RequirementSHA256 ||
		pkg.PlanID != run.PlanID || pkg.PlanSHA256 != run.PlanSHA256 || pkg.TaskSetVersion != run.TaskSetVersion || pkg.TaskSetVersion != ComplexStandardTaskSetVersion ||
		run.PlanReviewID != "" || pkg.DeliveryPolicy != "" || pkg.DeliveryBaseSHA != "" || pkg.AttemptPolicy != "" || pkg.FreezePolicy != "" ||
		pkg.PlannerCoordinationPolicy != "" || pkg.FinalReviewPolicy != RequirementFinalReviewPolicyV1 ||
		(pkg.BuilderFailurePolicy != "" && pkg.BuilderFailurePolicy != BuilderFirstFailurePolicyV1) ||
		sha256Hex([]byte(run.ExecutionPackageJSON)) != run.ExecutionPackageSHA256 {
		return ProjectExecutionContract{}, false, errors.New("project execution run lost its immutable grant")
	}
	contract := *pkg.ProjectExecution
	if contract.ExecutionRunID != run.ID || contract.RequirementVersionID != run.RequirementVersionID ||
		contract.PlanID != run.PlanID || contract.PlanSHA256 != run.PlanSHA256 || contract.RequirementSHA256 != run.RequirementSHA256 {
		return contract, false, errors.New("project execution admission no longer matches its run")
	}
	if err := ValidateProjectExecutionContract(contract); err != nil {
		return contract, false, err
	}
	return contract, true, nil
}

// ValidateExecutableProjectPlan keeps execution bounded without rewriting the
// saved Plan V3. Its existing path/check/coverage parser must also have passed.
func ValidateExecutableProjectPlan(plan ComplexEngineeringPlanResult, basis ProjectExecutionBasis) error {
	if plan.SchemaVersion != ProjectPlanningVersion || plan.Kind != "COMPLEX_ENGINEERING_PLAN" || len(plan.Tasks) < 1 || len(plan.Tasks) > 3 {
		return errors.New("project execution requires a saved Plan V3 with one to three tasks")
	}
	earlier := map[string]bool{}
	for _, task := range plan.Tasks {
		if earlier[task.Key] || !validTemporaryKey(task.Key) || len(task.WritePaths) == 0 || len(task.RequiredCheckIDs) == 0 {
			return errors.New("project execution task is incomplete or duplicated")
		}
		if len(task.SharedPathsRequireApproval) != 0 {
			return fmt.Errorf("project task %s requires shared-path approval that project execution does not support; include confirmed paths in writePaths and save a new plan", task.Key)
		}
		for _, key := range task.DependencyKeys {
			if !earlier[key] {
				return fmt.Errorf("project dependency %s must precede task %s", key, task.Key)
			}
		}
		for _, p := range slices.Concat(task.WritePaths, task.GeneratedPaths, task.SharedPathsRequireApproval) {
			if !ProjectPath(p, false) || !matchesAny(basis.WritePaths, p) {
				return fmt.Errorf("project task %s exceeds the confirmed write scope", task.Key)
			}
		}
		for _, id := range task.RequiredCheckIDs {
			if _, ok := ComplexCheckByID(basis.CheckCatalog(), id); !ok {
				return fmt.Errorf("project task %s has an unapproved check", task.Key)
			}
		}
		earlier[task.Key] = true
	}
	if len(plan.IntegrationCheckIDs) == 0 {
		return errors.New("project execution needs integration checks")
	}
	for _, id := range plan.IntegrationCheckIDs {
		if _, ok := ComplexCheckByID(basis.CheckCatalog(), id); !ok {
			return errors.New("project integration has an unapproved check")
		}
	}
	_, err := ResolveProjectRuntime(basis)
	return err
}

// ProjectExecutionMode serializes the bounded project plan on the existing
// STANDARD engine. The Planner's suggestion remains visible but is not a grant
// for another Builder or a new runtime coordination protocol.
func ProjectExecutionMode(plan ComplexEngineeringPlanResult) ComplexModeSelection {
	batches := make([][]string, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		batches = append(batches, []string{task.Key})
	}
	return ComplexModeSelection{Mode: WorkModeStandard, BuilderCount: 1, ReasonCode: ReasonOneBuilderRequired,
		SuggestedCount: plan.ParallelSuggestion.RecommendedBuilderCount, SafeConcurrent: 1, Batches: batches}
}

// ProjectExecutionPlan retains the task contract while requiring all confirmed
// basis checks at final integration. The additional checks strengthen execution;
// the saved proposal JSON and its original digest are not changed.
func ProjectExecutionPlan(plan ComplexEngineeringPlanResult, basis ProjectExecutionBasis) ComplexEngineeringPlanResult {
	plan.IntegrationCheckIDs = make([]string, 0, len(basis.Checks))
	for _, check := range basis.Checks {
		plan.IntegrationCheckIDs = append(plan.IntegrationCheckIDs, check.ID)
	}
	return plan
}

// BuilderFirstFailurePolicyV1 is the original admission marker, retained for history.
const BuilderFirstFailurePolicyV1 = "BUILDER_FIRST_FAILURE_V1"

// BuilderFirstFailureRun reads the original marker for compatible prompt reconstruction.
func BuilderFirstFailureRun(run ComplexExecutionRun) bool {
	_, project, err := ProjectContractFromRun(run)
	if err != nil || !project || run.CompletedAt != nil {
		return false
	}
	var pkg ComplexExecutionRunPackage
	return json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg) == nil && pkg.BuilderFailurePolicy == BuilderFirstFailurePolicyV1
}

// BuilderFirstFailureEnabled applies the user's default repair policy to active
// STANDARD projects, including historical admissions. Their packages stay intact.
func BuilderFirstFailureEnabled(run ComplexExecutionRun) bool {
	_, project, err := ProjectContractFromRun(run)
	return err == nil && project && run.Mode == WorkModeStandard && run.CompletedAt == nil
}
