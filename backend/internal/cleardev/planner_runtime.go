package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Planner runtime coordination preserves immutable execution authority. Its
// business-round bound is additional to the existing per-step message ledger.
const (
	PlannerRuntimePolicy       = "PLANNER_RUNTIME_COORDINATION_V1"
	PlannerRuntimeMaxRounds    = 2
	PlannerRuntimeMaxRevisions = 2
	PlannerRuntimeResultKind   = "PLANNER_RUNTIME_COORDINATION"

	PlannerRuntimeContinue = "CONTINUE"
	PlannerRuntimeAmend    = "AMEND_REMAINING"
	PlannerRuntimeProduct  = "PRODUCT_CLARIFICATION_REQUIRED"
	PlannerRuntimeStop     = "STOP"

	ReasonPlannerRuntimePending     ReasonCode = "PLANNER_COORDINATION_PENDING"
	ReasonPlannerRuntimeStopped     ReasonCode = "PLANNER_COORDINATION_STOPPED"
	ReasonPlannerRuntimeStale       ReasonCode = "PLANNER_COORDINATION_CONTEXT_CHANGED"
	ReasonPlannerRuntimeBudget      ReasonCode = "PLANNER_COORDINATION_LIMIT_REACHED"
	ReasonPlannerRuntimeUnavailable ReasonCode = "PLANNER_COORDINATION_UNAVAILABLE"
)

// PlannerCoordinationReport is a Builder observation, not trusted execution
// evidence or permission to change the task it is currently implementing.
type PlannerCoordinationReport struct {
	Category         string   `json:"category"`
	Summary          string   `json:"summary"`
	Evidence         []string `json:"evidence"`
	AffectedTaskKeys []string `json:"affectedTaskKeys"`
}

// PlannerRemainingAmendment strengthens a task contract and may revise an
// admitted project's engineering means after its current work settles.
// V1 deliberately does not change task identity, paths, checks, dependencies,
// interfaces, attempts, or Stage acceptance. Other changes must stop safely.
type PlannerRemainingAmendment struct {
	TaskKey                  string                 `json:"taskKey"`
	AdditionalReviewCriteria []string               `json:"additionalReviewCriteria"`
	ExecutionBasis           *ProjectExecutionBasis `json:"executionBasis,omitempty"`
}

// PlannerCoordinationResult is normalized against one frozen request. Binding
// fields are supplied by the control plane, not trusted from model prose.
type PlannerCoordinationResult struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Kind          string                      `json:"kind"`
	EventID       string                      `json:"eventId"`
	ContextSHA256 string                      `json:"contextSha256"`
	Decision      string                      `json:"decision"`
	Summary       string                      `json:"summary"`
	Questions     []string                    `json:"questions"`
	Amendments    []PlannerRemainingAmendment `json:"amendments"`
}

// PlannerCoordinationEvent is appended with the exact settled Builder message.
// The report remains an observation; candidate and review facts arrive through
// the ordinary control-plane pipeline before the coordination request starts.
type PlannerCoordinationEvent struct {
	ID                  string                    `json:"id"`
	ExecutionRunID      string                    `json:"executionRunId"`
	DispatchID          string                    `json:"dispatchId"`
	SourceStepID        string                    `json:"sourceStepId"`
	SourceMessageSHA256 string                    `json:"sourceMessageSha256"`
	Report              PlannerCoordinationReport `json:"report"`
	CreatedAt           time.Time                 `json:"createdAt"`
}

// PlannerCoordinationRequest reserves one durable round and the original Stage
// Planner identity. Context and prompt bytes never change after reservation.
type PlannerCoordinationRequest struct {
	EventID              string    `json:"eventId"`
	ExecutionRunID       string    `json:"executionRunId"`
	Ordinal              int       `json:"ordinal"`
	AgentStepID          string    `json:"agentStepId"`
	PlannerRoleBindingID string    `json:"plannerRoleBindingId"`
	AOSessionID          string    `json:"aoSessionId"`
	ContextJSON          string    `json:"contextJson"`
	ContextSHA256        string    `json:"contextSha256"`
	Prompt               string    `json:"prompt"`
	CreatedAt            time.Time `json:"createdAt"`
}

// PlannerCoordinationDecision keeps the proposed result separate from the
// control plane's actual disposition. STALE/STOP cannot be interpreted as an
// applied amendment or as a Reviewer verdict.
type PlannerCoordinationDecision struct {
	EventID        string     `json:"eventId"`
	ExecutionRunID string     `json:"executionRunId"`
	Source         string     `json:"source"`
	Outcome        string     `json:"outcome"`
	ReasonCode     ReasonCode `json:"reasonCode"`
	ResultJSON     string     `json:"resultJson"`
	ResultSHA256   string     `json:"resultSha256"`
	Summary        string     `json:"summary"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// PlannerTaskAmendment preserves both contract versions. Project revisions
// apply from their FirstRound; earlier dispatches keep their original identity.
type PlannerTaskAmendment struct {
	ID                     string    `json:"id"`
	EventID                string    `json:"eventId"`
	ExecutionRunID         string    `json:"executionRunId"`
	TaskMappingID          string    `json:"taskMappingId"`
	Ordinal                int       `json:"ordinal"`
	PreviousPackageSHA256  string    `json:"previousPackageSha256"`
	ExecutionPackageJSON   string    `json:"executionPackageJson"`
	ExecutionPackageSHA256 string    `json:"executionPackageSha256"`
	CreatedAt              time.Time `json:"createdAt"`
}

// PlannerRuntimeSnapshot is durable coordination history, not display state.
type PlannerRuntimeSnapshot struct {
	Events     []PlannerCoordinationEvent    `json:"events"`
	Requests   []PlannerCoordinationRequest  `json:"requests"`
	Decisions  []PlannerCoordinationDecision `json:"decisions"`
	Amendments []PlannerTaskAmendment        `json:"amendments"`
	// RepairAuthorizations names events whose control-plane STOP a human
	// coordination repair decision approved. Omitted from packets and
	// digests when empty so historical bytes stay identical.
	RepairAuthorizations       []string                      `json:"repairAuthorizations,omitempty"`
	Recoveries                 []PlannerCoordinationRecovery `json:"recoveries,omitempty"`
	RecoveryDecisions          []PlannerCoordinationDecision `json:"recoveryDecisions,omitempty"`
	CheckRecoveries            []StoppedCheckRecovery        `json:"checkRecoveries,omitempty"`
	ExtraCoordinationGrants    []ExtraCoordinationGrant      `json:"extraCoordinationGrants,omitempty"`
	ExtraCoordinationDecisions []PlannerCoordinationDecision `json:"extraCoordinationDecisions,omitempty"`
}

// PlannerTaskRevisionBinding ties an amended packet to its immutable decision
// and predecessor. Original packets omit this field and retain identical bytes.
type PlannerTaskRevisionBinding struct {
	FirstRound             int    `json:"firstRound,omitempty"`
	ProjectExecutionSHA256 string `json:"projectExecutionSha256,omitempty"`
	EventID                string `json:"eventId"`
	DecisionSHA256         string `json:"decisionSha256"`
	PreviousPackageSHA256  string `json:"previousPackageSha256"`
	// HumanRepairExtension widens the bounded revision window by the repair
	// rounds a human coordination repair grant approved. The controlled
	// amendment builder sets it from settled grants; zero keeps the original
	// bound and the original bytes.
	HumanRepairExtension int `json:"humanRepairExtension,omitempty"`
}

// BindPlannerRuntimePolicy is used only while constructing a fresh V2 run.
func BindPlannerRuntimePolicy(raw []byte) ([]byte, string, error) {
	var pkg ComplexExecutionRunPackage
	if err := decodeStrictAgentResult(raw, &pkg); err != nil {
		return nil, "", err
	}
	if pkg.PlanValidationPolicy != PlannerTaskContractPolicy || pkg.FinalReviewPolicy != RequirementFinalReviewPolicyV1 ||
		(pkg.PlannerCoordinationPolicy != "" && pkg.PlannerCoordinationPolicy != PlannerRuntimePolicy) {
		return nil, "", errors.New("runtime coordination requires the admitted task and final review policies")
	}
	pkg.PlannerCoordinationPolicy = PlannerRuntimePolicy
	pkg.PlannerCoordinationMaxRounds = PlannerRuntimeMaxRounds
	pkg.PlannerCoordinationMaxRevisions = PlannerRuntimeMaxRevisions
	encoded, err := marshalCanonicalJSON(pkg)
	return encoded, sha256Hex(encoded), err
}

// PlannerRuntimeRun recognizes admitted projects and explicitly enabled V2 runs.
// Recognizing an old project never rewrites its immutable admission.
func PlannerRuntimeRun(run ComplexExecutionRun) (bool, error) {
	var pkg ComplexExecutionRunPackage
	if run.ExecutionPackageJSON == "" {
		return false, nil
	}
	if err := json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg); err != nil {
		return false, err
	}
	if pkg.PlanValidationPolicy == ProjectExecutionPolicyV1 {
		_, admitted, err := ProjectAdmissionContractFromRun(run)
		return admitted, err
	}
	if pkg.PlannerCoordinationPolicy == "" && pkg.PlannerCoordinationMaxRounds == 0 && pkg.PlannerCoordinationMaxRevisions == 0 {
		return false, nil
	}
	contract, err := PlannerTaskContractRun(run)
	if err != nil || !contract || pkg.PlannerCoordinationPolicy != PlannerRuntimePolicy ||
		pkg.PlannerCoordinationMaxRounds != PlannerRuntimeMaxRounds || pkg.PlannerCoordinationMaxRevisions != PlannerRuntimeMaxRevisions ||
		pkg.FinalReviewPolicy != RequirementFinalReviewPolicyV1 {
		return false, errors.New("runtime coordination lost its frozen contract or bounded policy")
	}
	return true, nil
}

// ParsePlannerRuntimeBuilderResult adds an optional engineering observation to
// eligible runs. Calling the old parser still rejects this extension.
func ParsePlannerRuntimeBuilderResult(raw []byte, dispatchID, taskID string, round int) (BuilderResult, *PlannerCoordinationReport, error) {
	var envelope struct {
		BuilderResult
		Coordination *PlannerCoordinationReport `json:"coordination,omitempty"`
	}
	if err := decodeStrictAgentResult(raw, &envelope); err != nil {
		return BuilderResult{}, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return BuilderResult{}, nil, err
	}
	reportRaw, supplied := fields["coordination"]
	delete(fields, "coordination")
	baseRaw, err := json.Marshal(fields)
	if err != nil {
		return BuilderResult{}, nil, err
	}
	result, err := ParseBuilderResult(baseRaw, dispatchID, taskID, round)
	if err != nil || !supplied {
		return result, nil, err
	}
	if envelope.Coordination == nil {
		return result, nil, errors.New("coordination must be an explicit non-null report")
	}
	if _, err := requireJSONObjectFields(reportRaw, "category", "summary", "evidence", "affectedTaskKeys"); err != nil {
		return result, nil, err
	}
	report := *envelope.Coordination
	if report.Category != "ENGINEERING" && report.Category != "PRODUCT" {
		return result, nil, errors.New("coordination category must be ENGINEERING or PRODUCT")
	}
	report.Summary = strings.TrimSpace(report.Summary)
	if !validText(report.Summary, MaxComplexAnswerRunes) {
		return result, nil, errors.New("coordination needs a bounded factual summary")
	}
	report.Evidence, err = normalizeStringList(report.Evidence, 6)
	if err != nil || len(report.Evidence) == 0 {
		return result, nil, errors.New("coordination needs 1-6 concrete observations")
	}
	if len(report.AffectedTaskKeys) < 1 || len(report.AffectedTaskKeys) > 3 || requireUniqueStrings(report.AffectedTaskKeys) != nil {
		return result, nil, errors.New("coordination needs 1-3 distinct affected task keys")
	}
	for _, key := range report.AffectedTaskKeys {
		if !validTemporaryKey(key) {
			return result, nil, errors.New("coordination has an invalid affected task key")
		}
	}
	return result, &report, nil
}

// ParsePlannerCoordinationResult accepts only a bounded engineering decision.
// A report explicitly about product intent cannot be resolved technically.
func ParsePlannerCoordinationResult(raw []byte, event PlannerCoordinationEvent, contextSHA string) (PlannerCoordinationResult, []byte, string, error) {
	var result PlannerCoordinationResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, nil, "", err
	}
	fields, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "decision", "summary", "questions", "amendments")
	if err != nil {
		return result, nil, "", err
	}
	if result.SchemaVersion != 1 || result.Kind != PlannerRuntimeResultKind || !validProtocolSHA256(contextSHA) || event.ID == "" {
		return result, nil, "", errors.New("invalid runtime coordination result binding")
	}
	result.EventID, result.ContextSHA256 = event.ID, contextSHA
	result.Summary = strings.TrimSpace(result.Summary)
	if !validText(result.Summary, MaxComplexAnswerRunes) {
		return result, nil, "", errors.New("runtime decision requires a bounded explanation")
	}
	if result.Questions == nil || result.Amendments == nil {
		return result, nil, "", errors.New("runtime decision questions and amendments must be arrays")
	}
	result.Questions, err = normalizeStringList(result.Questions, 6)
	if err != nil {
		return result, nil, "", err
	}
	switch result.Decision {
	case PlannerRuntimeContinue, PlannerRuntimeStop:
		if len(result.Questions) != 0 || len(result.Amendments) != 0 {
			return result, nil, "", errors.New("continue or stop cannot smuggle a plan revision or product decision")
		}
	case PlannerRuntimeProduct:
		if len(result.Questions) == 0 || len(result.Amendments) != 0 {
			return result, nil, "", errors.New("product clarification needs questions and cannot revise engineering work")
		}
	case PlannerRuntimeAmend:
		if len(result.Questions) != 0 || len(result.Amendments) < 1 || len(result.Amendments) > 3 {
			return result, nil, "", errors.New("engineering revision needs 1-3 task amendments and no product decision")
		}
	default:
		return result, nil, "", errors.New("unsupported runtime coordination decision")
	}
	if event.Report.Category == "PRODUCT" && result.Decision != PlannerRuntimeProduct && result.Decision != PlannerRuntimeStop {
		return result, nil, "", errors.New("planner cannot resolve a product question as an engineering decision")
	}
	var amendmentFields []json.RawMessage
	if err := json.Unmarshal(fields["amendments"], &amendmentFields); err != nil {
		return result, nil, "", err
	}
	seen := map[string]bool{}
	for i := range result.Amendments {
		amendment := &result.Amendments[i]
		if _, err := requireJSONObjectFieldsOptional(amendmentFields[i], []string{"taskKey", "additionalReviewCriteria"}, []string{"executionBasis"}); err != nil {
			return result, nil, "", err
		}
		if !validTemporaryKey(amendment.TaskKey) || seen[amendment.TaskKey] || !slices.Contains(event.Report.AffectedTaskKeys, amendment.TaskKey) {
			return result, nil, "", errors.New("amendment must address a distinct reported task")
		}
		seen[amendment.TaskKey] = true
		amendment.AdditionalReviewCriteria, err = normalizeStringList(amendment.AdditionalReviewCriteria, 6)
		if err != nil || (len(amendment.AdditionalReviewCriteria) == 0 && amendment.ExecutionBasis == nil) {
			return result, nil, "", errors.New("amendment needs 1-6 additional observable engineering results")
		}
	}
	encoded, err := marshalCanonicalJSON(result)
	return result, encoded, sha256Hex(encoded), err
}

// BuildPlannerAmendedTaskPackage preserves original product authority and
// criteria. Project engineering revisions apply to a new bounded repair round;
// legacy amendments still require a never-started task.
func BuildPlannerAmendedTaskPackage(task ComplexExecutionTask, amendment PlannerRemainingAmendment, eventID, decisionSHA string, humanRepairExtension int, sources ...*FailureCoordinationSource) ([]byte, string, error) {
	if project, err := ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON)); err == nil && project.ProjectExecution != nil {
		return buildProjectEngineeringAmendment(task, project, amendment, eventID, decisionSHA, humanRepairExtension, sources...)
	}
	if amendment.ExecutionBasis != nil {
		return nil, "", errors.New("execution basis revisions require an admitted project")
	}
	if task.Status != DevelopmentTaskStatusPlanned || task.ReworkCount != 0 || task.CurrentRound != 0 || task.CurrentDispatchID != "" ||
		task.TaskKey != amendment.TaskKey || eventID == "" || !validProtocolSHA256(decisionSHA) ||
		sha256Hex([]byte(task.ExecutionPackageJSON)) != task.ExecutionPackageSHA256 {
		return nil, "", errors.New("only an intact, never-started task contract may be amended")
	}
	pkg, err := ParseComplexStandardExecutionPackage([]byte(task.ExecutionPackageJSON))
	if err != nil || pkg.SchemaVersion != PlannerTaskContractVersion || pkg.TaskKey != task.TaskKey || pkg.TaskID != task.DevelopmentTaskID || pkg.ExecutionRunID != task.ExecutionRunID {
		return nil, "", errors.New("amendment requires the exact admitted task package")
	}
	criteria, err := normalizeStringList(amendment.AdditionalReviewCriteria, 6)
	if err != nil || len(criteria) == 0 {
		return nil, "", errors.New("amendment requires additional engineering work")
	}
	for _, criterion := range criteria {
		if slices.Contains(pkg.ReviewCriteria, criterion) {
			return nil, "", fmt.Errorf("amendment repeats an existing review criterion for %s", task.TaskKey)
		}
	}
	pkg.ReviewCriteria = append(pkg.ReviewCriteria, criteria...)
	pkg.RuntimeRevision = &PlannerTaskRevisionBinding{EventID: eventID, DecisionSHA256: decisionSHA, PreviousPackageSHA256: task.ExecutionPackageSHA256}
	if err := ValidateComplexStandardExecutionPackage(pkg); err != nil {
		return nil, "", err
	}
	encoded, err := marshalCanonicalJSON(pkg)
	return encoded, sha256Hex(encoded), err
}
