package cleardev

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// COMPLEX_QUICK_TASK_REQUEST is the only Steward follow-up protocol. SUBMIT
// with FOLLOW_UP_READY is the only pair that can create a task.
const (
	ComplexQuickTaskRequestKind = "COMPLEX_QUICK_TASK_REQUEST"
	ComplexQuickDecisionSubmit  = "SUBMIT"
	ReasonFollowUpReady         = "FOLLOW_UP_READY"
)

// ComplexQuickTaskRequestResult is the strict Project Steward follow-up
// request. Callers and agents still cannot choose mode, commands, a candidate,
// or DONE; this object only describes one original completed task.
type ComplexQuickTaskRequestResult struct {
	SchemaVersion              int      `json:"schemaVersion"`
	Kind                       string   `json:"kind"`
	RequestID                  string   `json:"requestId"`
	RequirementVersionID       string   `json:"requirementVersionId"`
	RequirementSHA256          string   `json:"requirementSha256"`
	TaskSetVersion             int64    `json:"taskSetVersion"`
	IntegrationCommitSHA       string   `json:"integrationCommitSha"`
	SourceTaskKey              string   `json:"sourceTaskKey"`
	Decision                   string   `json:"decision"`
	ReasonCode                 string   `json:"reasonCode"`
	Summary                    string   `json:"summary"`
	Objective                  string   `json:"objective"`
	RequirementIDs             []string `json:"requirementIds"`
	AcceptanceIDs              []string `json:"acceptanceIds"`
	WritePaths                 []string `json:"writePaths"`
	TestPaths                  []string `json:"testPaths"`
	GeneratedPaths             []string `json:"generatedPaths"`
	SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
	ForbiddenPaths             []string `json:"forbiddenPaths"`
	RequiredCheckIDs           []string `json:"requiredCheckIds"`
	IntegrationCheckIDs        []string `json:"integrationCheckIds"`
}

// ParseComplexQuickTaskRequestResult accepts exactly the frozen S08 follow-up
// protocol. Unknown fields, duplicate keys, nulls, and a mismatched
// decision/reason pair are rejected before any task exists.
func ParseComplexQuickTaskRequestResult(raw []byte, requestID, versionID, versionSHA, sourceTaskKey, integrationSHA string, taskSetVersion int64) (ComplexQuickTaskRequestResult, error) {
	var result ComplexQuickTaskRequestResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "sourceTaskKey", "decision", "reasonCode", "summary",
		"objective", "requirementIds", "acceptanceIds", "writePaths", "testPaths",
		"generatedPaths", "sharedPathsRequireApproval", "forbiddenPaths",
		"requiredCheckIds", "integrationCheckIds",
	); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.Objective = strings.TrimSpace(result.Objective)
	result.RequestID = requestID
	result.RequirementVersionID = versionID
	result.RequirementSHA256 = versionSHA
	result.TaskSetVersion = taskSetVersion
	result.IntegrationCommitSHA = integrationSHA
	validReason := map[string]string{
		ComplexQuickDecisionSubmit:                 ReasonFollowUpReady,
		string(ComplexExecutionDecisionNeedsHuman): "HUMAN_DECISION_REQUIRED",
	}
	if result.SchemaVersion != ComplexExecutionProtocolVersion || result.Kind != ComplexQuickTaskRequestKind ||
		!validProtocolSHA256(result.RequirementSHA256) ||
		!validComplexExecutionCommitSHA(result.IntegrationCommitSHA) || result.SourceTaskKey != sourceTaskKey ||
		!validExecutionIdentifier(result.SourceTaskKey) || validReason[result.Decision] != result.ReasonCode ||
		!validText(result.Summary, MaxComplexAnswerRunes) || !validText(result.Objective, MaxComplexAnswerRunes) {
		return result, errors.New("complex quick task request does not match the current completed execution")
	}
	if err := validateExecutionStringList(result.RequirementIDs, 1, 8, "requirementIds"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.AcceptanceIDs, 1, 8, "acceptanceIds"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.WritePaths, 0, ComplexQuickMaxFiles, "writePaths"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.TestPaths, 0, ComplexQuickMaxFiles, "testPaths"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.GeneratedPaths, 0, 8, "generatedPaths"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.SharedPathsRequireApproval, 0, 8, "sharedPathsRequireApproval"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.ForbiddenPaths, 0, 8, "forbiddenPaths"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.RequiredCheckIDs, 1, 8, "requiredCheckIds"); err != nil {
		return result, err
	}
	if err := validateExecutionStringList(result.IntegrationCheckIDs, 1, 8, "integrationCheckIds"); err != nil {
		return result, err
	}
	return result, nil
}

// ComplexQuickRunPackage is the immutable execution-level envelope for one
// S08 follow-up. Mode is always QUICK and is chosen by the control plane.
type ComplexQuickRunPackage struct {
	SchemaVersion        int    `json:"schemaVersion"`
	Mode                 string `json:"mode"`
	ExecutionRunID       string `json:"executionRunId"`
	RequirementVersionID string `json:"requirementVersionId"`
	RequirementSHA256    string `json:"requirementSha256"`
	PlanID               string `json:"planId"`
	PlanSHA256           string `json:"planSha256"`
	SourceTaskKey        string `json:"sourceTaskKey"`
	IntegrationBaseSHA   string `json:"integrationBaseSha"`
	TaskSetVersion       int64  `json:"taskSetVersion"`
}

// BuildComplexQuickRunPackage binds the immutable QUICK envelope. Mode is
// always QUICK and is chosen by the control plane.
func BuildComplexQuickRunPackage(runID, versionID, requirementSHA, planID, planSHA, sourceTaskKey, integrationSHA string, taskSetVersion int64) ([]byte, string, error) {
	pkg := ComplexQuickRunPackage{
		SchemaVersion: ComplexExecutionProtocolVersion, Mode: string(WorkModeQuick),
		ExecutionRunID: strings.TrimSpace(runID), RequirementVersionID: strings.TrimSpace(versionID),
		RequirementSHA256: strings.TrimSpace(requirementSHA), PlanID: strings.TrimSpace(planID),
		PlanSHA256: strings.TrimSpace(planSHA), SourceTaskKey: strings.TrimSpace(sourceTaskKey),
		IntegrationBaseSHA: strings.TrimSpace(integrationSHA), TaskSetVersion: taskSetVersion,
	}
	if pkg.SchemaVersion != ComplexExecutionProtocolVersion || pkg.Mode != string(WorkModeQuick) ||
		!validExecutionIdentifier(pkg.ExecutionRunID) || !validExecutionIdentifier(pkg.RequirementVersionID) ||
		!validProtocolSHA256(pkg.RequirementSHA256) || !validExecutionIdentifier(pkg.PlanID) ||
		!validProtocolSHA256(pkg.PlanSHA256) || (pkg.SourceTaskKey != "" && !validExecutionIdentifier(pkg.SourceTaskKey)) ||
		!validComplexExecutionCommitSHA(pkg.IntegrationBaseSHA) || pkg.TaskSetVersion <= ComplexStandardTaskSetVersion {
		return nil, "", errors.New("complex quick run package has invalid immutable bindings")
	}
	encoded, err := marshalCanonicalJSON(pkg)
	if err != nil {
		return nil, "", err
	}
	return encoded, sha256Hex(encoded), nil
}

// BuildComplexQuickExecutionPackage freezes one follow-up task packet at the
// task-set version derived from its completed source execution.
func BuildComplexQuickExecutionPackage(input ComplexStandardExecutionPackageInput, writePaths []string, requiredChecks []ComplexExecutionCheckSpec) (ComplexStandardExecutionPackage, []byte, string, error) {
	quickTaskSetVersion := input.TaskSetVersion
	if quickTaskSetVersion <= ComplexStandardTaskSetVersion {
		return ComplexStandardExecutionPackage{}, nil, "", errors.New("quick execution package requires a successor task-set version")
	}
	input.TaskSetVersion = ComplexStandardTaskSetVersion
	pkg := newComplexExecutionPackage(input)
	for _, check := range requiredChecks {
		pkg.RequiredChecks = append(pkg.RequiredChecks, ComplexExecutionCheckSpec{
			ID: strings.TrimSpace(check.ID), Argv: cloneExecutionStrings(check.Argv), TimeoutSeconds: check.TimeoutSeconds,
		})
	}
	pkg.Mode = string(WorkModeQuick)
	pkg.TaskSetVersion = quickTaskSetVersion
	pkg.WritePaths = uniqueQuickPaths(writePaths)
	pkg.GeneratedPaths = []string{}
	pkg.SharedPathsRequireApproval = []string{}
	if err := ValidateComplexQuickExecutionPackage(pkg); err != nil {
		return ComplexStandardExecutionPackage{}, nil, "", err
	}
	encoded, err := marshalCanonicalJSON(pkg)
	if err != nil {
		return ComplexStandardExecutionPackage{}, nil, "", err
	}
	return pkg, encoded, sha256Hex(encoded), nil
}

// ValidateComplexQuickExecutionPackage requires QUICK mode, one or two concrete
// write paths, and the S06 STANDARD packet rules after cloning those fields.
func ValidateComplexQuickExecutionPackage(pkg ComplexStandardExecutionPackage) error {
	if pkg.Mode != string(WorkModeQuick) || pkg.TaskSetVersion <= ComplexStandardTaskSetVersion {
		return fmt.Errorf("quick execution package must use QUICK at a successor task-set version")
	}
	if len(pkg.WritePaths) == 0 || len(pkg.WritePaths) > ComplexQuickMaxFiles {
		return fmt.Errorf("quick execution package write paths must be 1-%d concrete files", ComplexQuickMaxFiles)
	}
	if len(pkg.GeneratedPaths) != 0 || len(pkg.SharedPathsRequireApproval) != 0 {
		return errors.New("quick execution package cannot include generated or shared paths")
	}
	clone := pkg
	clone.Mode = string(WorkModeStandard)
	clone.TaskSetVersion = ComplexStandardTaskSetVersion
	if err := validateComplexExecutionPackageShape(clone); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(pkg.RequiredChecks))
	for _, check := range pkg.RequiredChecks {
		if !validExecutionIdentifier(check.ID) || len(check.Argv) == 0 || len(check.Argv) > 32 || check.TimeoutSeconds <= 0 {
			return errors.New("quick execution package has an invalid inherited check")
		}
		for _, arg := range check.Argv {
			if arg != strings.TrimSpace(arg) || arg == "" || len(arg) > 1000 {
				return errors.New("quick execution package has an invalid inherited check argument")
			}
		}
		if _, duplicate := seen[check.ID]; duplicate {
			return errors.New("quick execution package has a duplicate inherited check")
		}
		seen[check.ID] = struct{}{}
	}
	return nil
}

// ParseComplexQuickExecutionPackage accepts the S06 packet schema and then
// requires the QUICK-only constraints.
func ParseComplexQuickExecutionPackage(raw []byte) (ComplexStandardExecutionPackage, error) {
	pkg, err := decodeComplexExecutionPackage(raw)
	if err != nil {
		return pkg, err
	}
	if err := ValidateComplexQuickExecutionPackage(pkg); err != nil {
		return pkg, err
	}
	return pkg, nil
}

// StartComplexQuickExecutionCommand durably creates the follow-up request and
// its Steward message before any Chat call occurs.
type StartComplexQuickExecutionCommand struct {
	Run                ComplexQuickRun
	StewardRoleBinding ComplexExecutionRoleBinding
	AgentStep          AgentStep
}

// MaterializeComplexQuickExecutionCommand converts an accepted follow-up into
// one QUICK work item, one Builder, and checks inherited from its source.
type MaterializeComplexQuickExecutionCommand struct {
	RunID          string
	SourceTaskKey  string
	Task           ComplexExecutionTask
	CheckSpecs     []ComplexExecutionCheckSpecFact
	BuilderBinding ComplexExecutionRoleBinding
	WritePaths     []string
	GeneratedPaths []string
	SharedPaths    []string
	ForbiddenPaths []string
	At             time.Time
}

// CompleteComplexQuickExecutionCommand writes the new integration at the
// QUICK run's accepted task-set version.
type CompleteComplexQuickExecutionCommand struct {
	RunID       string
	Integration ComplexExecutionIntegration
	At          time.Time
}
