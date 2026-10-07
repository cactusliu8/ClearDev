package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Planner Task Contract protocol and admission identifiers apply only to new
// planning requests. V1 plans and packets remain immutable historical inputs.
const (
	PlannerTaskContractVersion = 2
	PlannerTaskContractPolicy  = "PLANNER_TASK_CONTRACT_V1"
	PlannerClarificationKind   = "PRODUCT_CLARIFICATION_REQUIRED"

	ReasonProductClarificationRequired ReasonCode           = PlannerClarificationKind
	ComplexPlanningValidated           ComplexPlanningPhase = "VALIDATED"
)

// ComplexInterfaceContract is local to one immutable plan. The provider and
// consumers are plan task keys, never independently reusable global IDs.
// Expectations describe stable engineering results, not implementation steps.
type ComplexInterfaceContract struct {
	Key              string   `json:"key"`
	ProviderTaskKey  string   `json:"providerTaskKey"`
	ConsumerTaskKeys []string `json:"consumerTaskKeys"`
	Expectations     []string `json:"expectations"`
}

// ComplexPlanValidation is a control-plane admission fact, not a Steward or
// Reviewer verdict. PlanID is its primary key; all contract bytes are already
// covered by the immutable plan hash. The check catalog is frozen separately.
type ComplexPlanValidation struct {
	PlanID             string    `json:"planId"`
	PlanSHA256         string    `json:"planSha256"`
	Policy             string    `json:"policy"`
	CheckCatalogSHA256 string    `json:"checkCatalogSha256"`
	CreatedAt          time.Time `json:"createdAt"`
}

// ComplexPlannerClarification is an alternative planning outcome. It records
// the product decision that is missing; it cannot create a task or revise a
// confirmed Stage. The control plane supplies every identity/hash field.
type ComplexPlannerClarification struct {
	SchemaVersion            int      `json:"schemaVersion"`
	Kind                     string   `json:"kind"`
	PlanningRequestID        string   `json:"planningRequestId"`
	RequirementVersionID     string   `json:"requirementVersionId"`
	RequirementVersionSHA256 string   `json:"requirementVersionSha256"`
	CompilationSHA256        string   `json:"compilationSha256"`
	Summary                  string   `json:"summary"`
	Questions                []string `json:"questions"`
}

// ParsePlannerTaskContractResult accepts the new production plan protocol.
// It does not accept V1 as a fallback: a missing contract is a failed result,
// not permission to dispatch an old-style task.
func ParsePlannerTaskContractResult(raw []byte, requestID, versionID, versionSHA, compilationSHA string, coverage ComplexCoverage, catalog []ComplexCheckSpec) (ComplexEngineeringPlanResult, []byte, string, error) {
	return parseComplexEngineeringPlanResult(raw, requestID, versionID, versionSHA, compilationSHA, coverage, catalog, PlannerTaskContractVersion)
}

func validateInterfaceContractFields(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var contracts []json.RawMessage
	if err := json.Unmarshal(raw, &contracts); err != nil {
		return fmt.Errorf("interfaceContracts: %w", err)
	}
	for i, contract := range contracts {
		if _, err := requireJSONObjectFields(contract, "key", "providerTaskKey", "consumerTaskKeys", "expectations"); err != nil {
			return fmt.Errorf("interfaceContracts[%d]: %w", i, err)
		}
	}
	return nil
}

// NormalizeAndValidatePlannerTaskContracts validates the engineering contract
// against the confirmed acceptance set and approved catalog. It deliberately
// cannot decide whether prose is a good implementation; that remains the
// Planner's responsibility and each fixed candidate's Reviewer obligation.
func NormalizeAndValidatePlannerTaskContracts(plan *ComplexEngineeringPlanResult, coverage ComplexCoverage, catalog []ComplexCheckSpec) error {
	return normalizeAndValidateTaskContracts(plan, coverage, catalog, false)
}

func normalizeAndValidateTaskContracts(plan *ComplexEngineeringPlanResult, coverage ComplexCoverage, catalog []ComplexCheckSpec, planningOnly bool) error {
	protocol := PlannerTaskContractVersion
	if planningOnly {
		protocol = ProjectPlanningVersion
	}
	if plan.SchemaVersion != protocol || plan.Kind != "COMPLEX_ENGINEERING_PLAN" {
		return fmt.Errorf("task contract requires Planner protocol version %d", protocol)
	}
	if len(plan.Tasks) < 1 || len(plan.Tasks) > 3 || plan.ParallelSuggestion.RecommendedBuilderCount < 1 || plan.ParallelSuggestion.RecommendedBuilderCount > 2 {
		return errors.New("task contract admits 1-3 tasks and at most two Builders")
	}
	if err := validateComplexPlanWithCatalog(*plan, coverage, catalog); err != nil {
		return err
	}
	covered := make(map[string]bool, len(coverage.AcceptanceIDs))
	tasks := make(map[string]ComplexPlanTask, len(plan.Tasks))
	for i := range plan.Tasks {
		task := &plan.Tasks[i]
		criteria, err := normalizeContractExpectations(task.ReviewCriteria)
		if err != nil {
			return fmt.Errorf("task %s reviewCriteria: %w", task.Key, err)
		}
		task.ReviewCriteria = criteria
		tasks[task.Key] = *task
		for _, id := range task.AcceptanceIDs {
			covered[id] = true
		}
	}
	for id := range coverage.AcceptanceIDs {
		if !covered[id] {
			return fmt.Errorf("stage acceptance %s is not mapped to a Task Contract", id)
		}
	}
	if len(plan.InterfaceContracts) > 6 {
		return errors.New("a bounded plan admits at most six interface contracts")
	}
	seen := make(map[string]bool, len(plan.InterfaceContracts))
	for i := range plan.InterfaceContracts {
		contract := &plan.InterfaceContracts[i]
		if err := validateInterfaceContractShape(*contract); err != nil {
			return err
		}
		if seen[contract.Key] {
			return fmt.Errorf("duplicate interface contract %s", contract.Key)
		}
		seen[contract.Key] = true
		if _, ok := tasks[contract.ProviderTaskKey]; !ok {
			return fmt.Errorf("interface %s has unknown provider %s", contract.Key, contract.ProviderTaskKey)
		}
		for _, consumerKey := range contract.ConsumerTaskKeys {
			consumer, ok := tasks[consumerKey]
			if !ok || !slices.Contains(consumer.DependencyKeys, contract.ProviderTaskKey) {
				return fmt.Errorf("interface %s consumer %s must depend on provider %s", contract.Key, consumerKey, contract.ProviderTaskKey)
			}
		}
		contract.Expectations, _ = normalizeContractExpectations(contract.Expectations)
	}
	// Reuse the existing DAG, generated-path and disjoint-wave checks. An
	// unsafe parallel suggestion still degrades to STANDARD under that policy.
	if !planningOnly {
		selection, err := SelectComplexExecutionMode(plan.Tasks, plan.ParallelSuggestion.RecommendedBuilderCount)
		if err != nil {
			return err
		}
		if selection.BuilderCount > 2 {
			return errors.New("task contract cannot expand the Builder production bound")
		}
	}
	// SQLite materialization requires every dependency to have an earlier
	// ordinal. Canonicalize before hashing/admission, never during execution.
	// Keep an already topological plan unchanged, including independent order.
	ordered, err := orderContractTasks(plan.Tasks)
	if err != nil {
		return err
	}
	plan.Tasks = ordered
	return nil
}

func orderContractTasks(tasks []ComplexPlanTask) ([]ComplexPlanTask, error) {
	ordered := make([]ComplexPlanTask, 0, len(tasks))
	seen := make(map[string]bool, len(tasks))
	for len(ordered) < len(tasks) {
		advanced := false
		for _, task := range tasks {
			if seen[task.Key] || slices.ContainsFunc(task.DependencyKeys, func(key string) bool { return !seen[key] }) {
				continue
			}
			ordered = append(ordered, task)
			seen[task.Key] = true
			advanced = true
			break
		}
		if !advanced {
			return nil, errors.New("task contract dependencies cannot be ordered")
		}
	}
	return ordered, nil
}

// ErrReviewCriteriaCeiling marks the review-criteria count ceiling. A human
// repair grant widens it; the refusal is therefore a permission stop the human
// may authorize, not a structural impossibility.
var ErrReviewCriteriaCeiling = errors.New("execution package exceeds the review criteria ceiling")

// ErrRepairWindowExceeded marks the revision window bound. A human repair grant
// widens it; same permission-stop semantics as ErrReviewCriteriaCeiling.
var ErrRepairWindowExceeded = errors.New("project revision exceeds its human-granted repair window")

func normalizeContractExpectations(values []string) ([]string, error) {
	return normalizeContractExpectationsMax(values, 12)
}

func normalizeContractExpectationsMax(values []string, limit int) ([]string, error) {
	out, err := normalizeStringList(values, limit)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("at least one observable engineering result is required")
	}
	return out, nil
}

func validateInterfaceContractShape(contract ComplexInterfaceContract) error {
	if !validTemporaryKey(contract.Key) || !validTemporaryKey(contract.ProviderTaskKey) {
		return errors.New("interface contract key and provider must be valid plan task keys")
	}
	if len(contract.ConsumerTaskKeys) < 1 || len(contract.ConsumerTaskKeys) > 2 {
		return fmt.Errorf("interface %s needs 1-2 consumers", contract.Key)
	}
	if err := requireUniqueStrings(contract.ConsumerTaskKeys); err != nil {
		return err
	}
	for _, key := range contract.ConsumerTaskKeys {
		if !validTemporaryKey(key) || key == contract.ProviderTaskKey {
			return fmt.Errorf("interface %s has invalid or self-consuming task %s", contract.Key, key)
		}
	}
	_, err := normalizeContractExpectations(contract.Expectations)
	return err
}

// InterfaceContractsForTask returns only interfaces this task provides or
// consumes. The enclosing package binds these keys to the exact plan and task.
func InterfaceContractsForTask(contracts []ComplexInterfaceContract, key string) []ComplexInterfaceContract {
	var out []ComplexInterfaceContract
	for _, contract := range contracts {
		if contract.ProviderTaskKey == key || slices.Contains(contract.ConsumerTaskKeys, key) {
			contract.ConsumerTaskKeys = append([]string(nil), contract.ConsumerTaskKeys...)
			contract.Expectations = append([]string(nil), contract.Expectations...)
			out = append(out, contract)
		}
	}
	return out
}

func validateExecutionTaskContract(pkg ComplexStandardExecutionPackage) error {
	if pkg.SchemaVersion == ComplexExecutionProtocolVersion {
		if len(pkg.ReviewCriteria) != 0 || len(pkg.InterfaceContracts) != 0 || pkg.RuntimeRevision != nil {
			return errors.New("legacy execution package cannot silently acquire a Task Contract")
		}
		return nil
	}
	if revision := pkg.RuntimeRevision; revision != nil {
		if !validExecutionIdentifier(revision.EventID) || !validProtocolSHA256(revision.DecisionSHA256) || !validProtocolSHA256(revision.PreviousPackageSHA256) {
			return errors.New("runtime task revision requires its exact decision and predecessor")
		}
	}
	// A human-authorized repair extends the review-criteria ceiling by exactly
	// one amendment's worth of additions (six), so an authorized revision of an
	// already full task can still land; history without the extension keeps the
	// original twelve.
	criteriaCap := 12
	if revision := pkg.RuntimeRevision; revision != nil && revision.HumanRepairExtension > 0 {
		criteriaCap = 12 + 6
	}
	if len(pkg.ReviewCriteria) > criteriaCap {
		return ErrReviewCriteriaCeiling
	}
	criteria, err := normalizeContractExpectationsMax(pkg.ReviewCriteria, criteriaCap)
	if err != nil || !slices.Equal(criteria, pkg.ReviewCriteria) {
		return errors.New("execution package needs normalized Task Review Criteria")
	}
	if len(pkg.InterfaceContracts) > 6 {
		return errors.New("execution package has too many interface contracts")
	}
	seen := make(map[string]bool, len(pkg.InterfaceContracts))
	for _, contract := range pkg.InterfaceContracts {
		if err := validateInterfaceContractShape(contract); err != nil {
			return err
		}
		if seen[contract.Key] {
			return errors.New("execution package has duplicate interface contracts")
		}
		seen[contract.Key] = true
		expectations, _ := normalizeContractExpectations(contract.Expectations)
		if !slices.Equal(expectations, contract.Expectations) {
			return errors.New("execution interface expectations are not normalized")
		}
		if contract.ProviderTaskKey != pkg.TaskKey {
			if !slices.Contains(contract.ConsumerTaskKeys, pkg.TaskKey) || !slices.Contains(pkg.DependencyTaskKeys, contract.ProviderTaskKey) {
				return errors.New("execution interface is unrelated to this task or contradicts its dependencies")
			}
		}
	}
	return nil
}

// ParsePlannerProductClarification binds a non-executable product question to
// the current planning request and confirmed requirement.
func ParsePlannerProductClarification(raw []byte, requestID, versionID, versionSHA, compilationSHA string) (ComplexPlannerClarification, []byte, string, error) {
	var result ComplexPlannerClarification
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, nil, "", err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "summary", "questions"); err != nil {
		return result, nil, "", err
	}
	if result.SchemaVersion != PlannerTaskContractVersion || result.Kind != PlannerClarificationKind {
		return result, nil, "", errors.New("invalid product clarification outcome")
	}
	result.Summary = strings.TrimSpace(result.Summary)
	if !validText(result.Summary, MaxComplexAnswerRunes) {
		return result, nil, "", errors.New("product clarification needs a bounded summary")
	}
	questions, err := normalizeStringList(result.Questions, 8)
	if err != nil || len(questions) == 0 {
		return result, nil, "", errors.New("product clarification needs 1-8 nonempty unique product questions")
	}
	result.Questions = questions
	result.PlanningRequestID, result.RequirementVersionID = requestID, versionID
	result.RequirementVersionSHA256, result.CompilationSHA256 = versionSHA, compilationSHA
	encoded, err := marshalCanonicalJSON(result)
	if err != nil {
		return result, nil, "", err
	}
	return result, encoded, sha256Hex(encoded), nil
}

// PlannerClarificationForPlan returns only a clarification with intact plan bindings.
func PlannerClarificationForPlan(plan ComplexEngineeringPlan) (ComplexPlannerClarification, bool) {
	var result ComplexPlannerClarification
	if json.Unmarshal([]byte(plan.PlanJSON), &result) != nil || result.SchemaVersion != PlannerTaskContractVersion || result.Kind != PlannerClarificationKind ||
		result.PlanningRequestID != plan.PlanningRequestID || result.RequirementVersionID != plan.RequirementVersionID ||
		result.RequirementVersionSHA256 != plan.RequirementSHA256 || result.CompilationSHA256 != plan.CompilationSHA256 ||
		sha256Hex([]byte(plan.PlanJSON)) != plan.PlanSHA256 || len(result.Questions) == 0 {
		return ComplexPlannerClarification{}, false
	}
	return result, true
}

// PlanValidationForPlan finds the exact immutable deterministic admission fact.
func PlanValidationForPlan(snapshot ComplexPlanningSnapshot, plan ComplexEngineeringPlan) (ComplexPlanValidation, bool) {
	for _, validation := range snapshot.Validations {
		if validation.PlanID == plan.ID && validation.PlanSHA256 == plan.PlanSHA256 && validation.Policy == PlannerTaskContractPolicy &&
			validProtocolSHA256(validation.CheckCatalogSHA256) && sha256Hex([]byte(plan.PlanJSON)) == plan.PlanSHA256 {
			return validation, true
		}
	}
	return ComplexPlanValidation{}, false
}

// ComplexCheckCatalogSHA256 hashes the canonical approved catalog used at admission.
func ComplexCheckCatalogSHA256(catalog []ComplexCheckSpec) (string, error) {
	encoded, err := marshalCanonicalJSON(catalog)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}
