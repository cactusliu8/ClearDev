package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ComplexExecutionRequestResult is the strict result of asking the original
// Project Steward to dispatch one already-approved complex plan.
type ComplexExecutionRequestResult struct {
	SchemaVersion        int    `json:"schemaVersion"`
	Kind                 string `json:"kind"`
	RequestID            string `json:"requestId"`
	RequirementVersionID string `json:"requirementVersionId"`
	PlanID               string `json:"planId"`
	PlanSHA256           string `json:"planSha256"`
	Decision             string `json:"decision"`
	ReasonCode           string `json:"reasonCode"`
	Summary              string `json:"summary"`
}

// ParseComplexExecutionRequestResult accepts exactly the frozen S06 dispatch
// request protocol. Unknown fields, duplicate keys, nulls, stale IDs, and a
// mismatched decision/reason pair are all rejected before storage can create
// any task.
func ParseComplexExecutionRequestResult(raw []byte, requestID, versionID, planID, planSHA string) (ComplexExecutionRequestResult, error) {
	var result ComplexExecutionRequestResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "decision", "reasonCode", "summary",
	); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.RequestID = requestID
	result.RequirementVersionID = versionID
	result.PlanID = planID
	result.PlanSHA256 = planSHA
	validReason := map[string]string{
		string(ComplexExecutionDecisionDispatch):   "PLAN_READY",
		string(ComplexExecutionDecisionNeedsHuman): "HUMAN_DECISION_REQUIRED",
	}
	if result.SchemaVersion != ComplexExecutionProtocolVersion || result.Kind != "COMPLEX_EXECUTION_REQUEST" ||
		!validProtocolSHA256(result.PlanSHA256) ||
		validReason[result.Decision] != result.ReasonCode || !validText(result.Summary, MaxComplexAnswerRunes) {
		return result, errors.New("complex execution request does not match the approved plan")
	}
	return result, nil
}

// ComplexExecutionCheckSpec is the check subset given to a Builder. STANDARD
// derives it from FrozenComplexCheckCatalog. QUICK clones it from a completed
// source execution. It deliberately has no shell, environment, or image field.
type ComplexExecutionCheckSpec struct {
	ID             string   `json:"id"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

// ComplexExecutionRunPackage is the immutable execution-level envelope. It
// intentionally carries no task IDs: those IDs do not exist until the
// accepted Steward result is materialized.
type ComplexExecutionRunPackage struct {
	SchemaVersion        int    `json:"schemaVersion"`
	Mode                 string `json:"mode"`
	ExecutionRunID       string `json:"executionRunId"`
	RequirementVersionID string `json:"requirementVersionId"`
	RequirementSHA256    string `json:"requirementSha256"`
	PlanID               string `json:"planId"`
	PlanSHA256           string `json:"planSha256"`
	TaskSetVersion       int64  `json:"taskSetVersion"`
	// Set only by the control plane from the trusted starting repository.
	DeliveryPolicy       string `json:"deliveryPolicy,omitempty"`
	DeliveryBaseSHA      string `json:"deliveryBaseSha,omitempty"`
	AttemptPolicy        string `json:"attemptPolicy,omitempty"`
	FreezePolicy         string `json:"freezePolicy,omitempty"`
	FinalReviewPolicy    string `json:"finalReviewPolicy,omitempty"`
	BuilderFailurePolicy string `json:"builderFailurePolicy,omitempty"`
	PlanValidationPolicy string `json:"planValidationPolicy,omitempty"`
	// Present only after an explicit, current project execution admission.
	ProjectExecution *ProjectExecutionContract `json:"projectExecution,omitempty"`
	// These bounds are frozen only for fresh runtime-coordination runs.
	PlannerCoordinationPolicy       string `json:"plannerCoordinationPolicy,omitempty"`
	PlannerCoordinationMaxRounds    int    `json:"plannerCoordinationMaxRounds,omitempty"`
	PlannerCoordinationMaxRevisions int    `json:"plannerCoordinationMaxRevisions,omitempty"`
}

// BuildComplexExecutionRunPackage returns the canonical immutable package and
// SHA-256 for a complex execution run. Mode is chosen by the control plane
// only; callers pass WorkModeStandard or WorkModeParallel.
func BuildComplexExecutionRunPackage(mode WorkMode, runID, versionID, requirementSHA, planID, planSHA string) ([]byte, string, error) {
	pkg := ComplexExecutionRunPackage{
		SchemaVersion: ComplexExecutionProtocolVersion, Mode: string(mode),
		ExecutionRunID: strings.TrimSpace(runID), RequirementVersionID: strings.TrimSpace(versionID),
		RequirementSHA256: strings.TrimSpace(requirementSHA), PlanID: strings.TrimSpace(planID),
		PlanSHA256: strings.TrimSpace(planSHA), TaskSetVersion: ComplexStandardTaskSetVersion,
	}
	if pkg.SchemaVersion != ComplexExecutionProtocolVersion || !validComplexExecutionMode(pkg.Mode) ||
		!validExecutionIdentifier(pkg.ExecutionRunID) || !validExecutionIdentifier(pkg.RequirementVersionID) ||
		!validProtocolSHA256(pkg.RequirementSHA256) || !validExecutionIdentifier(pkg.PlanID) ||
		!validProtocolSHA256(pkg.PlanSHA256) || pkg.TaskSetVersion != ComplexStandardTaskSetVersion {
		return nil, "", errors.New("complex execution run package has invalid immutable bindings")
	}
	encoded, err := marshalCanonicalJSON(pkg)
	if err != nil {
		return nil, "", err
	}
	return encoded, sha256Hex(encoded), nil
}

func validComplexExecutionMode(mode string) bool {
	return mode == string(WorkModeStandard) || mode == string(WorkModeParallel)
}

// ComplexStandardExecutionPackage is the normalized immutable input for one
// task and one Builder dispatch. Storage must parse and compare it with the
// approved plan again before accepting a dispatch or a check result.
type ComplexStandardExecutionPackage struct {
	SchemaVersion              int                         `json:"schemaVersion"`
	Mode                       string                      `json:"mode"`
	ExecutionRunID             string                      `json:"executionRunId"`
	RequirementVersionID       string                      `json:"requirementVersionId"`
	RequirementSHA256          string                      `json:"requirementSha256"`
	RequirementText            string                      `json:"requirementText"`
	PlanID                     string                      `json:"planId"`
	PlanSHA256                 string                      `json:"planSha256"`
	TaskSetVersion             int64                       `json:"taskSetVersion"`
	TaskKey                    string                      `json:"taskKey"`
	TaskID                     string                      `json:"taskId"`
	Title                      string                      `json:"title"`
	Objective                  string                      `json:"objective"`
	RequirementIDs             []string                    `json:"requirementIds"`
	AcceptanceIDs              []string                    `json:"acceptanceIds"`
	DependencyTaskKeys         []string                    `json:"dependencyTaskKeys"`
	DependencyTaskIDs          []string                    `json:"dependencyTaskIds"`
	WritePaths                 []string                    `json:"writePaths"`
	GeneratedPaths             []string                    `json:"generatedPaths"`
	SharedPathsRequireApproval []string                    `json:"sharedPathsRequireApproval"`
	ForbiddenPaths             []string                    `json:"forbiddenPaths"`
	RequiredChecks             []ComplexExecutionCheckSpec `json:"requiredChecks"`
	MaxReworkCount             int                         `json:"maxReworkCount"`
	ReviewCriteria             []string                    `json:"reviewCriteria,omitempty"`
	InterfaceContracts         []ComplexInterfaceContract  `json:"interfaceContracts,omitempty"`
	RuntimeRevision            *PlannerTaskRevisionBinding `json:"runtimeRevision,omitempty"`
	ProjectExecution           *ProjectExecutionContract   `json:"projectExecution,omitempty"`
}

// ComplexStandardExecutionPackageInput collects only backend-assigned IDs and
// the already validated S04 task. Dependency task IDs must be supplied in the
// same order as Task.DependencyKeys.
type ComplexStandardExecutionPackageInput struct {
	ExecutionRunID       string
	RequirementVersionID string
	RequirementSHA256    string
	RequirementText      string
	PlanID               string
	PlanSHA256           string
	TaskSetVersion       int64
	TaskID               string
	DependencyTaskIDs    []string
	Task                 ComplexPlanTask
	PlanSchemaVersion    int
	InterfaceContracts   []ComplexInterfaceContract
	ProjectExecution     *ProjectExecutionContract
}

// BuildComplexStandardExecutionPackage resolves the only permitted check IDs
// to their fixed argv definitions, normalizes JSON arrays, and returns the
// canonical package bytes and SHA-256 saved by the control plane. Mode is
// WorkModeStandard or WorkModeParallel and must match the persisted run.
func BuildComplexStandardExecutionPackage(mode WorkMode, input ComplexStandardExecutionPackageInput) (ComplexStandardExecutionPackage, []byte, string, error) {
	return BuildComplexStandardExecutionPackageWithCatalog(mode, input, FrozenComplexCheckCatalog())
}

// BuildComplexStandardExecutionPackageWithCatalog builds the immutable execution package against an explicitly supplied frozen check catalog.
func BuildComplexStandardExecutionPackageWithCatalog(mode WorkMode, input ComplexStandardExecutionPackageInput, catalog []ComplexCheckSpec) (ComplexStandardExecutionPackage, []byte, string, error) {
	if input.ProjectExecution != nil && input.PlanSchemaVersion != ProjectPlanningVersion {
		return ComplexStandardExecutionPackage{}, nil, "", errors.New("project execution packages require the original planning protocol and an explicit contract")
	}
	pkg := newComplexExecutionPackage(input)
	pkg.Mode = string(mode)
	for _, checkID := range input.Task.RequiredCheckIDs {
		check, ok := ComplexCheckByID(catalog, checkID)
		if !ok {
			return ComplexStandardExecutionPackage{}, nil, "", fmt.Errorf("unknown frozen complex check %q", checkID)
		}
		pkg.RequiredChecks = append(pkg.RequiredChecks, ComplexExecutionCheckSpec{
			ID: check.ID, Argv: cloneExecutionStrings(check.Argv), TimeoutSeconds: check.TimeoutSeconds,
		})
	}
	if err := ValidateComplexStandardExecutionPackageWithCatalog(pkg, catalog); err != nil {
		return ComplexStandardExecutionPackage{}, nil, "", err
	}
	encoded, err := marshalCanonicalJSON(pkg)
	if err != nil {
		return ComplexStandardExecutionPackage{}, nil, "", err
	}
	return pkg, encoded, sha256Hex(encoded), nil
}

func newComplexExecutionPackage(input ComplexStandardExecutionPackageInput) ComplexStandardExecutionPackage {
	protocol := input.PlanSchemaVersion
	if protocol == 0 {
		protocol = ComplexExecutionProtocolVersion // Historical callers build V1 bytes unchanged.
	}
	if input.ProjectExecution != nil {
		protocol = ProjectExecutionProtocolVersion
	}
	return ComplexStandardExecutionPackage{
		SchemaVersion:              protocol,
		Mode:                       string(WorkModeStandard),
		ExecutionRunID:             strings.TrimSpace(input.ExecutionRunID),
		RequirementVersionID:       strings.TrimSpace(input.RequirementVersionID),
		RequirementSHA256:          strings.TrimSpace(input.RequirementSHA256),
		RequirementText:            strings.TrimSpace(input.RequirementText),
		PlanID:                     strings.TrimSpace(input.PlanID),
		PlanSHA256:                 strings.TrimSpace(input.PlanSHA256),
		TaskSetVersion:             input.TaskSetVersion,
		TaskKey:                    strings.TrimSpace(input.Task.Key),
		TaskID:                     strings.TrimSpace(input.TaskID),
		Title:                      strings.TrimSpace(input.Task.Title),
		Objective:                  strings.TrimSpace(input.Task.Objective),
		RequirementIDs:             cloneExecutionStrings(input.Task.RequirementIDs),
		AcceptanceIDs:              cloneExecutionStrings(input.Task.AcceptanceIDs),
		DependencyTaskKeys:         cloneExecutionStrings(input.Task.DependencyKeys),
		DependencyTaskIDs:          cloneExecutionStrings(input.DependencyTaskIDs),
		WritePaths:                 cloneExecutionStrings(input.Task.WritePaths),
		GeneratedPaths:             cloneExecutionStrings(input.Task.GeneratedPaths),
		SharedPathsRequireApproval: cloneExecutionStrings(input.Task.SharedPathsRequireApproval),
		ForbiddenPaths:             cloneExecutionStrings(input.Task.ForbiddenPaths),
		RequiredChecks:             []ComplexExecutionCheckSpec{},
		MaxReworkCount:             complexExecutionMaxReworkCount(input.ProjectExecution != nil),
		ReviewCriteria:             append([]string(nil), input.Task.ReviewCriteria...),
		InterfaceContracts:         InterfaceContractsForTask(input.InterfaceContracts, input.Task.Key),
		ProjectExecution:           input.ProjectExecution,
	}
}

// ParseComplexStandardExecutionPackage requires the exact package schema and
// then validates that every executable argument still comes from the fixed
// catalog. The expected backend bindings are checked by storage separately.
func ParseComplexStandardExecutionPackage(raw []byte) (ComplexStandardExecutionPackage, error) {
	pkg, err := decodeComplexExecutionPackage(raw)
	if err != nil {
		return pkg, err
	}
	catalog := allFrozenComplexExecutionCheckCatalog()
	if pkg.SchemaVersion == ProjectExecutionProtocolVersion && pkg.ProjectExecution != nil {
		catalog = pkg.ProjectExecution.Basis.CheckCatalog()
	}
	return pkg, ValidateComplexStandardExecutionPackageWithCatalog(pkg, catalog)
}

// ParseComplexStandardExecutionPackageWithCatalog parses and validates a saved execution package against an explicitly supplied frozen catalog.
func ParseComplexStandardExecutionPackageWithCatalog(raw []byte, catalog []ComplexCheckSpec) (ComplexStandardExecutionPackage, error) {
	pkg, err := decodeComplexExecutionPackage(raw)
	if err != nil {
		return pkg, err
	}
	if err := ValidateComplexStandardExecutionPackageWithCatalog(pkg, catalog); err != nil {
		return pkg, err
	}
	return pkg, nil
}

func decodeComplexExecutionPackage(raw []byte) (ComplexStandardExecutionPackage, error) {
	var pkg ComplexStandardExecutionPackage
	if err := decodeStrictAgentResult(raw, &pkg); err != nil {
		return pkg, err
	}
	required := []string{
		"schemaVersion", "mode", "executionRunId", "requirementVersionId", "requirementSha256", "requirementText",
		"planId", "planSha256", "taskSetVersion", "taskKey", "taskId", "title", "objective", "requirementIds",
		"acceptanceIds", "dependencyTaskKeys", "dependencyTaskIds", "writePaths", "generatedPaths",
		"sharedPathsRequireApproval", "forbiddenPaths", "requiredChecks", "maxReworkCount",
	}
	optional := []string{}
	if pkg.SchemaVersion == PlannerTaskContractVersion || pkg.SchemaVersion == ProjectExecutionProtocolVersion {
		required = append(required, "reviewCriteria")
		optional = append(optional, "interfaceContracts", "runtimeRevision")
		if pkg.SchemaVersion == ProjectExecutionProtocolVersion {
			required = append(required, "projectExecution")
		}
	}
	fields, err := requireJSONObjectFieldsOptional(raw, required, optional)
	if err != nil {
		return pkg, err
	}
	if err := validateInterfaceContractFields(fields["interfaceContracts"]); err != nil {
		return pkg, err
	}
	if rawRevision, ok := fields["runtimeRevision"]; ok {
		allowed := []string{}
		if pkg.SchemaVersion == ProjectExecutionProtocolVersion {
			allowed = []string{"firstRound", "projectExecutionSha256", "humanRepairExtension"}
		}
		if _, err := requireJSONObjectFieldsOptional(rawRevision, []string{"eventId", "decisionSha256", "previousPackageSha256"}, allowed); err != nil {
			return pkg, err
		}
	}
	var checks []json.RawMessage
	if err := json.Unmarshal(fields["requiredChecks"], &checks); err != nil {
		return pkg, fmt.Errorf("execution package requiredChecks: %w", err)
	}
	for index, check := range checks {
		if _, err := requireJSONObjectFields(check, "id", "argv", "timeoutSeconds"); err != nil {
			return pkg, fmt.Errorf("execution package requiredChecks[%d]: %w", index, err)
		}
	}
	return pkg, nil
}

// ValidateComplexStandardExecutionPackage verifies the protocol-intrinsic
// rules. It does not accept a plan from an Agent; storage compares this package
// with the saved ComplexEngineeringPlan before any durable write.
func ValidateComplexStandardExecutionPackage(pkg ComplexStandardExecutionPackage) error {
	catalog := allFrozenComplexExecutionCheckCatalog()
	if pkg.SchemaVersion == ProjectExecutionProtocolVersion && pkg.ProjectExecution != nil {
		catalog = pkg.ProjectExecution.Basis.CheckCatalog()
	}
	return ValidateComplexStandardExecutionPackageWithCatalog(pkg, catalog)
}

// ValidateComplexStandardExecutionPackageWithCatalog validates package-intrinsic rules and exact frozen check argv for the supplied catalog.
func ValidateComplexStandardExecutionPackageWithCatalog(pkg ComplexStandardExecutionPackage, catalog []ComplexCheckSpec) error {
	if err := validateComplexExecutionPackageShape(pkg); err != nil {
		return err
	}
	seenChecks := make(map[string]struct{}, len(pkg.RequiredChecks))
	for _, check := range pkg.RequiredChecks {
		frozen, ok := ComplexCheckByID(catalog, check.ID)
		if !ok || frozen.TimeoutSeconds != check.TimeoutSeconds || !slices.Equal(frozen.Argv, check.Argv) {
			return errors.New("execution package check is not the fixed complex check")
		}
		if _, exists := seenChecks[check.ID]; exists {
			return errors.New("execution package has duplicate check")
		}
		seenChecks[check.ID] = struct{}{}
	}
	return nil
}

func allFrozenComplexExecutionCheckCatalog() []ComplexCheckSpec {
	catalog := append([]ComplexCheckSpec(nil), FrozenComplexCheckCatalog()...)
	for _, prefix := range []string{"CHK-LIB", "CHK-INV", "CHK-TKT", "CHK-DEV"} {
		benchmark, err := BenchmarkComplexCheckCatalog(prefix)
		if err == nil {
			catalog = append(catalog, benchmark...)
		}
	}
	return catalog
}

func validateComplexExecutionPackageShape(pkg ComplexStandardExecutionPackage) error {
	if (pkg.SchemaVersion != ComplexExecutionProtocolVersion && pkg.SchemaVersion != PlannerTaskContractVersion && pkg.SchemaVersion != ProjectExecutionProtocolVersion) || !validComplexExecutionMode(pkg.Mode) ||
		!validExecutionIdentifier(pkg.ExecutionRunID) || !validExecutionIdentifier(pkg.RequirementVersionID) ||
		!validProtocolSHA256(pkg.RequirementSHA256) || !validText(pkg.RequirementText, MaxComplexPRDBytes) ||
		!validExecutionIdentifier(pkg.PlanID) || !validProtocolSHA256(pkg.PlanSHA256) ||
		pkg.TaskSetVersion != ComplexStandardTaskSetVersion || !validTemporaryKey(pkg.TaskKey) ||
		!validExecutionIdentifier(pkg.TaskID) || !validText(pkg.Title, MaxComplexAnswerRunes) ||
		!validText(pkg.Objective, MaxComplexAnswerRunes) || !complexExecutionValidReworkCount(pkg.ProjectExecution != nil, pkg.MaxReworkCount) {
		return errors.New("complex STANDARD execution package has invalid immutable bindings")
	}
	if err := validateExecutionTaskContract(pkg); err != nil {
		return err
	}
	if err := validateExecutionStringList(pkg.RequirementIDs, 1, 20, "requirementIds"); err != nil {
		return err
	}
	if err := validateExecutionStringList(pkg.AcceptanceIDs, 1, 20, "acceptanceIds"); err != nil {
		return err
	}
	if len(pkg.DependencyTaskKeys) != len(pkg.DependencyTaskIDs) || len(pkg.DependencyTaskKeys) > MaxComplexPlanTasks-1 {
		return errors.New("execution package dependency task keys and ids do not match")
	}
	seenDependencies := make(map[string]struct{}, len(pkg.DependencyTaskKeys))
	for index, key := range pkg.DependencyTaskKeys {
		if !validTemporaryKey(key) || key == pkg.TaskKey || !validExecutionIdentifier(pkg.DependencyTaskIDs[index]) {
			return errors.New("execution package has invalid dependency binding")
		}
		if _, exists := seenDependencies[key]; exists {
			return errors.New("execution package has duplicate dependency task key")
		}
		seenDependencies[key] = struct{}{}
	}
	rules := PathRules{
		WritePaths: pkg.WritePaths, GeneratedPaths: pkg.GeneratedPaths,
		SharedPathsRequireApproval: pkg.SharedPathsRequireApproval, ForbiddenPaths: pkg.ForbiddenPaths,
	}
	if err := rules.Validate(); err != nil || len(pkg.WritePaths) == 0 || len(pkg.ForbiddenPaths) == 0 {
		return errors.New("execution package path rules are invalid")
	}
	if err := validateProjectExecutionPackage(pkg); err != nil {
		return err
	}
	for _, path := range append(append(append(append([]string{}, pkg.WritePaths...), pkg.GeneratedPaths...), pkg.SharedPathsRequireApproval...), pkg.ForbiddenPaths...) {
		if pkg.SchemaVersion == ProjectExecutionProtocolVersion {
			if !ProjectPath(path, slices.Contains(pkg.ForbiddenPaths, path)) {
				return errors.New("project execution package path is not a safe repository boundary")
			}
		} else if !complexTemplatePathAllowed(path) {
			return errors.New("execution package path is outside the frozen complex template")
		}
	}
	if len(pkg.GeneratedPaths) != 0 && pkg.SchemaVersion != ProjectExecutionProtocolVersion {
		if _, ok := FrozenGeneratedCommandForPaths(pkg.GeneratedPaths); !ok {
			return errors.New("complex STANDARD execution generated paths are not a frozen command")
		}
	}
	if len(pkg.RequiredChecks) == 0 || len(pkg.RequiredChecks) > 20 {
		return errors.New("execution package needs 1-20 fixed required checks")
	}
	return nil
}

// ParseComplexExecutionBuilderResult intentionally preserves S02's strict
// Builder wire protocol.
func ParseComplexExecutionBuilderResult(raw []byte, dispatchID, taskID string, round int) (BuilderResult, error) {
	return ParseBuilderResult(raw, dispatchID, taskID, round)
}

// ParseComplexExecutionLocalReviewResult intentionally preserves S02's strict
// Reviewer wire protocol.
func ParseComplexExecutionLocalReviewResult(raw []byte, assignmentID, candidateID, candidateSHA, packetSHA string, diffPaths []string) (LocalReviewResult, error) {
	return ParseLocalReviewResult(raw, assignmentID, candidateID, candidateSHA, packetSHA, diffPaths)
}

func cloneExecutionStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func validExecutionIdentifier(value string) bool {
	return value == strings.TrimSpace(value) && value != "" && len(value) <= 200
}

func validateExecutionStringList(values []string, minimum, maximum int, field string) error {
	if len(values) < minimum || len(values) > maximum {
		return fmt.Errorf("execution package %s needs %d-%d items", field, minimum, maximum)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != strings.TrimSpace(value) || value == "" || len(value) > 200 {
			return fmt.Errorf("execution package %s has an invalid item", field)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("execution package %s has a duplicate item", field)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// complexExecutionMaxReworkCount selects the task-level rework budget: generic
// project executions allow three rounds, the historical mail policy keeps one.
func complexExecutionMaxReworkCount(projectExecution bool) int {
	if projectExecution {
		return ComplexProjectMaxReworkCount
	}
	return ComplexStandardMaxReworkCount
}

// complexExecutionValidReworkCount accepts the count this policy freezes for
// new executions and, for generic project executions frozen before the
// three-round policy, the historical single round. Frozen packages are never
// rewritten, so both values stay valid for the life of the execution.
func complexExecutionValidReworkCount(projectExecution bool, count int) bool {
	if projectExecution {
		return count == ComplexProjectMaxReworkCount || count == ComplexStandardMaxReworkCount
	}
	return count == ComplexStandardMaxReworkCount
}
