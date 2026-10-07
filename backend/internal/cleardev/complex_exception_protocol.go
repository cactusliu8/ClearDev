package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Strict Agent result kinds for S09 scope, specialist, and recovery messages.
const (
	ComplexScopeExpansionRequestKind  = "SCOPE_EXPANSION_REQUEST"
	ComplexScopeExpansionDecisionKind = "SCOPE_EXPANSION_DECISION"
	ComplexSpecialistResultKind       = "SPECIALIST_RESULT"
	ComplexRecoveryResultKind         = "RECOVERY_RESULT"
)

// ComplexScopeExpansionRequestResult is the strict Builder request for listed
// shared paths. Callers still cannot choose a path that is not in the current
// approved plan.
type ComplexScopeExpansionRequestResult struct {
	SchemaVersion        int      `json:"schemaVersion"`
	Kind                 string   `json:"kind"`
	DispatchID           string   `json:"dispatchId"`
	TaskID               string   `json:"taskId"`
	Round                int      `json:"round"`
	RequirementVersionID string   `json:"requirementVersionId"`
	RequirementSHA256    string   `json:"requirementSha256"`
	PlanID               string   `json:"planId"`
	PlanSHA256           string   `json:"planSha256"`
	RequestedPaths       []string `json:"requestedPaths"`
	Summary              string   `json:"summary"`
}

// ComplexScopeExpansionDecisionResult is the Steward request to approve listed paths.
type ComplexScopeExpansionDecisionResult struct {
	SchemaVersion        int      `json:"schemaVersion"`
	Kind                 string   `json:"kind"`
	RequestID            string   `json:"requestId"`
	RequirementVersionID string   `json:"requirementVersionId"`
	RequirementSHA256    string   `json:"requirementSha256"`
	PlanID               string   `json:"planId"`
	PlanSHA256           string   `json:"planSha256"`
	Paths                []string `json:"paths"`
	Decision             string   `json:"decision"`
	ReasonCode           string   `json:"reasonCode"`
	Summary              string   `json:"summary"`
}

// ComplexSpecialistResultWire is the specialist PASS or stop bound to one task.
type ComplexSpecialistResultWire struct {
	SchemaVersion        int      `json:"schemaVersion"`
	Kind                 string   `json:"kind"`
	ExecutionRunID       string   `json:"executionRunId"`
	TaskID               string   `json:"taskId"`
	RequirementVersionID string   `json:"requirementVersionId"`
	RequirementSHA256    string   `json:"requirementSha256"`
	PlanID               string   `json:"planId"`
	PlanSHA256           string   `json:"planSha256"`
	Outcome              string   `json:"outcome"`
	Constraints          []string `json:"constraints"`
	ReasonCode           string   `json:"reasonCode"`
	Summary              string   `json:"summary"`
}

// ComplexRecoveryResultWire is the recovery action bound to one recorded failure.
type ComplexRecoveryResultWire struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Kind           string `json:"kind"`
	ExecutionRunID string `json:"executionRunId"`
	TriggerReason  string `json:"triggerReason"`
	TriggerFactID  string `json:"triggerFactId"`
	Action         string `json:"action"`
	Outcome        string `json:"outcome"`
	ReasonCode     string `json:"reasonCode"`
	Summary        string `json:"summary"`
}

// ParseComplexScopeExpansionRequest accepts only the current dispatch, task, round, and hashes.
func ParseComplexScopeExpansionRequest(raw []byte, dispatchID, taskID string, round int, versionID, versionSHA, planID, planSHA string) (ComplexScopeExpansionRequestResult, error) {
	var result ComplexScopeExpansionRequestResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "requestedPaths", "summary",
	); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.DispatchID = dispatchID
	result.TaskID = taskID
	result.Round = round
	result.RequirementVersionID = versionID
	result.RequirementSHA256 = versionSHA
	result.PlanID = planID
	result.PlanSHA256 = planSHA
	if err := validateExecutionStringList(result.RequestedPaths, 1, 8, "requestedPaths"); err != nil {
		return result, err
	}
	if result.SchemaVersion != ComplexExceptionProtocolVersion || result.Kind != ComplexScopeExpansionRequestKind ||
		!validText(result.Summary, 10000) {
		return result, errors.New("scope expansion request does not match the active execution round")
	}
	return result, nil
}

// ParseComplexScopeExpansionDecision accepts only the current request, hashes, and listed paths.
func ParseComplexScopeExpansionDecision(raw []byte, requestID, versionID, versionSHA, planID, planSHA string, paths []string) (ComplexScopeExpansionDecisionResult, error) {
	var result ComplexScopeExpansionDecisionResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "paths", "decision", "reasonCode", "summary",
	); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.RequestID = requestID
	result.RequirementVersionID = versionID
	result.RequirementSHA256 = versionSHA
	result.PlanID = planID
	result.PlanSHA256 = planSHA
	if err := validateExecutionStringList(result.Paths, 1, 8, "paths"); err != nil {
		return result, err
	}
	if !sameStringSet(result.Paths, paths) {
		return result, errors.New("scope expansion decision paths do not match the recorded request")
	}
	valid := map[string]string{ComplexScopeDecisionApprove: "SCOPE_APPROVED", ComplexScopeDecisionNeedsHuman: string(ReasonHumanDecisionRequired)}
	if result.SchemaVersion != ComplexExceptionProtocolVersion || result.Kind != ComplexScopeExpansionDecisionKind ||
		valid[result.Decision] != result.ReasonCode ||
		!validText(result.Summary, 10000) {
		return result, errors.New("scope expansion decision does not match the recorded request")
	}
	return result, nil
}

// ParseComplexSpecialistResult accepts only the current run, task, and hashes.
func ParseComplexSpecialistResult(raw []byte, runID, taskID, versionID, versionSHA, planID, planSHA string) (ComplexSpecialistResultWire, error) {
	var result ComplexSpecialistResultWire
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "outcome", "constraints", "reasonCode", "summary",
	); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.ExecutionRunID = runID
	result.TaskID = taskID
	result.RequirementVersionID = versionID
	result.RequirementSHA256 = versionSHA
	result.PlanID = planID
	result.PlanSHA256 = planSHA
	if result.Constraints == nil {
		result.Constraints = []string{}
	}
	if err := validateExecutionStringList(result.Constraints, 0, 20, "constraints"); err != nil {
		return result, err
	}
	validOutcome := result.Outcome == "PASS" || result.Outcome == "BLOCKED" || result.Outcome == "NEEDS_HUMAN"
	if result.SchemaVersion != ComplexExceptionProtocolVersion || result.Kind != ComplexSpecialistResultKind ||
		!validOutcome || !validText(result.Summary, 10000) {
		return result, errors.New("specialist result does not match the current task binding")
	}
	if result.Outcome == "PASS" && result.ReasonCode != "SPECIALIST_PASS" {
		return result, errors.New("specialist result does not match the current task binding")
	}
	if result.Outcome == "NEEDS_HUMAN" && result.ReasonCode != string(ReasonHumanDecisionRequired) {
		return result, errors.New("specialist result does not match the current task binding")
	}
	if result.Outcome == "BLOCKED" && strings.TrimSpace(result.ReasonCode) == "" {
		return result, errors.New("specialist result does not match the current task binding")
	}
	return result, nil
}

// ParseComplexRecoveryResult accepts only the recorded failure and an allowed action.
func ParseComplexRecoveryResult(raw []byte, runID, triggerReason, triggerFactID string) (ComplexRecoveryResultWire, error) {
	var result ComplexRecoveryResultWire
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "action", "outcome", "reasonCode", "summary",
	); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.ExecutionRunID = runID
	result.TriggerReason = triggerReason
	result.TriggerFactID = triggerFactID
	if result.SchemaVersion != ComplexExceptionProtocolVersion || result.Kind != ComplexRecoveryResultKind ||
		!RecoveryActionAllowed(result.Action) || (result.Outcome != "PASS" && result.Outcome != "NEEDS_HUMAN") ||
		!validText(result.Summary, 10000) {
		return result, errors.New("recovery result does not match the recorded failure")
	}
	if result.Outcome == "PASS" && result.ReasonCode != "RECOVERY_SAFE" {
		return result, errors.New("recovery result does not match the recorded failure")
	}
	if result.Outcome == "NEEDS_HUMAN" && result.ReasonCode != string(ReasonHumanDecisionRequired) && result.ReasonCode != string(ReasonRecoveryNeedsHuman) {
		return result, errors.New("recovery result does not match the recorded failure")
	}
	return result, nil
}

// EncodeSpecialistConstraints stores specialist constraints as a JSON array.
func EncodeSpecialistConstraints(constraints []string) (string, error) {
	if constraints == nil {
		constraints = []string{}
	}
	raw, err := json.Marshal(constraints)
	if err != nil {
		return "", fmt.Errorf("specialist constraints: %w", err)
	}
	return string(raw), nil
}

// ValidateComplexExecutionGeneratedPaths rejects unfrozen generated paths.
func ValidateComplexExecutionGeneratedPaths(tasks []ComplexPlanTask) error {
	for _, task := range tasks {
		for _, path := range exceptionTaskPaths(task) {
			if exceptionPathIsRepoControl(path) && !matchesAny(task.ForbiddenPaths, path) {
				return fmt.Errorf("task %q repository-control path %q is not forbidden", task.Key, path)
			}
		}
		if len(task.GeneratedPaths) == 0 {
			continue
		}
		if _, ok := FrozenGeneratedCommandForPaths(task.GeneratedPaths); !ok {
			return fmt.Errorf("task %q generated paths are not a frozen command", task.Key)
		}
	}
	return nil
}
