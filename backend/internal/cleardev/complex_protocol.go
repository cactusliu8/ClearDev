package cleardev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	complexIDRequirementKind = "REQUIREMENT"
	complexIDAcceptanceKind  = "ACCEPTANCE"
)

var temporaryKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// ComplexCheckSpec is a frozen, argv-only check. Agents cannot submit argv.
type ComplexCheckSpec struct {
	ID             string
	Argv           []string
	TimeoutSeconds int
	MainPaths      []string
}

func FrozenComplexCheckCatalog() []ComplexCheckSpec {
	return []ComplexCheckSpec{
		{ID: "email-unit", Argv: []string{"node", "--test", "test/email.test.js"}, TimeoutSeconds: 60, MainPaths: []string{"src/email.js", "test/email.test.js"}},
		{ID: "deduplicate-unit", Argv: []string{"node", "--test", "test/deduplicate.test.js"}, TimeoutSeconds: 60, MainPaths: []string{"src/deduplicate.js", "test/deduplicate.test.js"}},
		{ID: "summary-unit", Argv: []string{"node", "--test", "test/summary.test.js"}, TimeoutSeconds: 60, MainPaths: []string{"src/summary.js", "test/summary.test.js"}},
		{ID: "all-tests", Argv: []string{"node", "--test"}, TimeoutSeconds: 60, MainPaths: []string{"src/**", "test/**", "package.json", "package-lock.json"}},
		{ID: "demo-backend", Argv: []string{"npm", "run", "test:backend"}, TimeoutSeconds: 60, MainPaths: []string{"backend/**"}},
		{ID: "demo-database", Argv: []string{"npm", "run", "test:database"}, TimeoutSeconds: 60, MainPaths: []string{"migrations/**"}},
		{ID: "demo-api", Argv: []string{"npm", "run", "test:api"}, TimeoutSeconds: 60, MainPaths: []string{"backend/**"}},
		{ID: "demo-frontend", Argv: []string{"npm", "run", "test:frontend"}, TimeoutSeconds: 60, MainPaths: []string{"frontend/**"}},
		{ID: "demo-integration", Argv: []string{"npm", "test"}, TimeoutSeconds: 60, MainPaths: []string{"backend/**", "frontend/**", "migrations/**", "test/**", "package.json", "package-lock.json"}},
	}
}

func FrozenComplexCheckByID(id string) (ComplexCheckSpec, bool) {
	for _, spec := range FrozenComplexCheckCatalog() {
		if spec.ID == id {
			return spec, true
		}
	}
	return ComplexCheckSpec{}, false
}

type CompilationContext struct {
	SchemaVersion                  int                       `json:"schemaVersion"`
	DevelopmentRequirementID       string                    `json:"developmentRequirementId"`
	TargetRequirementVersionID     string                    `json:"targetRequirementVersionId"`
	SourcePRDSHA256                string                    `json:"sourcePrdSha256"`
	PreviousRequirementVersionID   string                    `json:"previousRequirementVersionId,omitempty"`
	PreviousRequirementSHA256      string                    `json:"previousRequirementSha256,omitempty"`
	DirectionIntentID              string                    `json:"directionIntentId,omitempty"`
	UserMessageSHA256              string                    `json:"userMessageSha256,omitempty"`
	DirectionRequestID             string                    `json:"directionRequestId,omitempty"`
	DirectionRequestSHA256         string                    `json:"directionRequestSha256,omitempty"`
	DirectionDecisionRequestID     string                    `json:"directionDecisionRequestId,omitempty"`
	DirectionDecisionContentSHA256 string                    `json:"directionDecisionContentSha256,omitempty"`
	ExistingRequirementIDs         []string                  `json:"existingRequirementIds,omitempty"`
	ExistingAcceptanceIDs          []string                  `json:"existingAcceptanceIds,omitempty"`
	PreviousRounds                 []CompilationContextRound `json:"previousRounds"`
}

type CompilationContextRound struct {
	ClarificationRound    int                          `json:"clarificationRound"`
	CompilationRequestID  string                       `json:"compilationRequestId"`
	AdditionalRoundReason string                       `json:"additionalRoundReason"`
	Questions             []CompilationContextQuestion `json:"questions"`
	Answers               []CompilationContextAnswer   `json:"answers"`
}

type CompilationContextQuestion struct {
	Key             string   `json:"key"`
	Text            string   `json:"text"`
	Reason          string   `json:"reason"`
	RequirementKeys []string `json:"requirementKeys"`
}

type CompilationContextAnswer struct {
	QuestionKey string `json:"questionKey"`
	Text        string `json:"text"`
}

type RequirementCompilationResult struct {
	SchemaVersion            int                           `json:"schemaVersion"`
	Kind                     string                        `json:"kind"`
	CompilationRequestID     string                        `json:"compilationRequestId"`
	RequirementVersionID     string                        `json:"requirementVersionId"`
	CompilationContextSHA256 string                        `json:"compilationContextSha256"`
	ClarificationRound       int                           `json:"clarificationRound"`
	Outcome                  string                        `json:"outcome"`
	Summary                  string                        `json:"summary"`
	Requirements             []CompilationRequirement      `json:"requirements"`
	AcceptanceScenarios      []CompilationAcceptance       `json:"acceptanceScenarios"`
	Constraints              []string                      `json:"constraints"`
	NonGoals                 []string                      `json:"nonGoals"`
	Terms                    []CompilationTerm             `json:"terms"`
	Assumptions              []string                      `json:"assumptions"`
	Conflicts                []CompilationConflict         `json:"conflicts"`
	BlockingQuestions        []CompilationBlockingQuestion `json:"blockingQuestions"`
	AdditionalRoundReason    string                        `json:"additionalRoundReason,omitempty"`
}

type CompilationRequirement struct {
	Key                   string   `json:"key"`
	Priority              string   `json:"priority"`
	Text                  string   `json:"text"`
	AcceptanceKeys        []string `json:"acceptanceKeys"`
	PreviousRequirementID string   `json:"previousRequirementId,omitempty"`
}

type CompilationAcceptance struct {
	Key                  string `json:"key"`
	Text                 string `json:"text"`
	PreviousAcceptanceID string `json:"previousAcceptanceId,omitempty"`
}

type CompilationTerm struct {
	Term       string `json:"term"`
	Definition string `json:"definition"`
}

type CompilationConflict struct {
	Text     string `json:"text"`
	Blocking bool   `json:"blocking"`
}

type CompilationBlockingQuestion struct {
	Key             string   `json:"key"`
	Text            string   `json:"text"`
	Reason          string   `json:"reason"`
	RequirementKeys []string `json:"requirementKeys"`
}

type NormalizedRequirementDocument struct {
	SchemaVersion       int                     `json:"schemaVersion"`
	SourcePRDSHA256     string                  `json:"sourcePrdSha256"`
	Summary             string                  `json:"summary"`
	Requirements        []NormalizedRequirement `json:"requirements"`
	AcceptanceScenarios []NormalizedAcceptance  `json:"acceptanceScenarios"`
	Constraints         []string                `json:"constraints"`
	NonGoals            []string                `json:"nonGoals"`
	Terms               []CompilationTerm       `json:"terms"`
	Assumptions         []string                `json:"assumptions"`
	Conflicts           []CompilationConflict   `json:"conflicts"`
}

type NormalizedRequirement struct {
	ID            string   `json:"id"`
	Priority      string   `json:"priority"`
	Text          string   `json:"text"`
	AcceptanceIDs []string `json:"acceptanceIds"`
}

type NormalizedAcceptance struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type ComplexEngineeringPlanResult struct {
	SchemaVersion            int                        `json:"schemaVersion"`
	Kind                     string                     `json:"kind"`
	PlanningRequestID        string                     `json:"planningRequestId"`
	RequirementVersionID     string                     `json:"requirementVersionId"`
	RequirementVersionSHA256 string                     `json:"requirementVersionSha256"`
	CompilationSHA256        string                     `json:"compilationSha256"`
	TechnicalApproach        string                     `json:"technicalApproach"`
	Tasks                    []ComplexPlanTask          `json:"tasks"`
	IntegrationCheckIDs      []string                   `json:"integrationCheckIds"`
	ParallelSuggestion       ComplexParallelSuggestion  `json:"parallelSuggestion"`
	Risks                    []string                   `json:"risks"`
	InterfaceContracts       []ComplexInterfaceContract `json:"interfaceContracts,omitempty"`
}

type ComplexPlanTask struct {
	Key                        string   `json:"key"`
	Title                      string   `json:"title"`
	Objective                  string   `json:"objective"`
	RequirementIDs             []string `json:"requirementIds"`
	AcceptanceIDs              []string `json:"acceptanceIds"`
	WritePaths                 []string `json:"writePaths"`
	GeneratedPaths             []string `json:"generatedPaths"`
	SharedPathsRequireApproval []string `json:"sharedPathsRequireApproval"`
	ForbiddenPaths             []string `json:"forbiddenPaths"`
	RequiredCheckIDs           []string `json:"requiredCheckIds"`
	DependencyKeys             []string `json:"dependencyKeys"`
	ReviewCriteria             []string `json:"reviewCriteria,omitempty"`
}

type ComplexParallelSuggestion struct {
	RecommendedBuilderCount int    `json:"recommendedBuilderCount"`
	Reason                  string `json:"reason"`
}

type ComplexPlanReviewResult struct {
	SchemaVersion   int                    `json:"schemaVersion"`
	Kind            string                 `json:"kind"`
	ReviewRequestID string                 `json:"reviewRequestId"`
	PlanID          string                 `json:"planId"`
	PlanSHA256      string                 `json:"planSha256"`
	Verdict         string                 `json:"verdict"`
	ReasonCode      string                 `json:"reasonCode"`
	Summary         string                 `json:"summary"`
	Findings        []ComplexReviewFinding `json:"findings"`
}

type ComplexReviewFinding struct {
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	RequirementIDs []string `json:"requirementIds"`
	TaskKeys       []string `json:"taskKeys"`
}

type ComplexCoverage struct {
	MUSTIDs        []string
	RequirementIDs map[string]string
	AcceptanceIDs  map[string]string
}

func HashCompilationContext(context CompilationContext) (string, []byte, error) {
	if context.PreviousRounds == nil {
		context.PreviousRounds = []CompilationContextRound{}
	}
	for i := range context.PreviousRounds {
		if context.PreviousRounds[i].Questions == nil {
			context.PreviousRounds[i].Questions = []CompilationContextQuestion{}
		}
		if context.PreviousRounds[i].Answers == nil {
			context.PreviousRounds[i].Answers = []CompilationContextAnswer{}
		}
	}
	raw, err := marshalCanonicalJSON(context)
	if err != nil {
		return "", nil, err
	}
	return sha256Hex(raw), raw, nil
}

func ParseRequirementCompilationResult(
	raw []byte,
	requestID, versionID, contextSHA string,
	round int,
	allowPreviousIDs bool,
) (RequirementCompilationResult, error) {
	return parseRequirementCompilationResult(raw, requestID, versionID, contextSHA, round, allowPreviousIDs, false)
}

func parseRequirementCompilationResult(raw []byte, requestID, versionID, contextSHA string, round int, allowPreviousIDs, stageReadyFirst bool) (RequirementCompilationResult, error) {
	var result RequirementCompilationResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	required := []string{
		"schemaVersion", "kind", "outcome", "summary",
		"requirements", "acceptanceScenarios", "constraints", "nonGoals", "terms",
		"assumptions", "conflicts", "blockingQuestions",
	}
	optional := []string{}
	if round == 1 && result.Outcome == "CLARIFICATION_REQUIRED" {
		optional = []string{"additionalRoundReason"}
	}
	fields, err := requireJSONObjectFieldsOptional(raw, required, optional)
	if err != nil {
		return result, err
	}
	if err := validateCompilationNestedFields(fields, allowPreviousIDs); err != nil {
		return result, err
	}
	result.CompilationRequestID = requestID
	result.RequirementVersionID = versionID
	result.CompilationContextSHA256 = contextSHA
	result.ClarificationRound = round
	if result.SchemaVersion != ComplexProtocolVersion || result.Kind != "REQUIREMENT_COMPILATION" {
		return result, errors.New("compilation result does not match the saved request")
	}
	result.Summary = strings.TrimSpace(result.Summary)
	if err := validateResultText("summary", result.Summary, MaxComplexAnswerRunes); err != nil {
		return result, err
	}
	if err := normalizeCompilationLists(&result); err != nil {
		return result, err
	}
	validationRound := round
	if stageReadyFirst && round == 0 && result.Outcome == "READY" {
		validationRound = 1 // Only a persisted product-stage source permits initial READY.
	}
	if err := validateCompilationOutcome(result, validationRound); err != nil {
		return result, err
	}
	if err := validateCompilationReferences(result, allowPreviousIDs); err != nil {
		return result, err
	}
	return result, nil
}

func BuildNormalizedRequirementDocument(result RequirementCompilationResult, sourcePRDSHA string, requirementMaps, acceptanceMaps []ComplexIDMap) (NormalizedRequirementDocument, []byte, string, error) {
	reqByKey := map[string]string{}
	for _, item := range requirementMaps {
		reqByKey[item.TemporaryKey] = item.StableID
	}
	accByKey := map[string]string{}
	for _, item := range acceptanceMaps {
		accByKey[item.TemporaryKey] = item.StableID
	}
	doc := NormalizedRequirementDocument{
		SchemaVersion: ComplexProtocolVersion, SourcePRDSHA256: sourcePRDSHA, Summary: result.Summary,
		Requirements:        make([]NormalizedRequirement, 0, len(result.Requirements)),
		AcceptanceScenarios: make([]NormalizedAcceptance, 0, len(result.AcceptanceScenarios)),
		Constraints:         append([]string(nil), result.Constraints...),
		NonGoals:            append([]string(nil), result.NonGoals...),
		Terms:               append([]CompilationTerm(nil), result.Terms...),
		Assumptions:         append([]string(nil), result.Assumptions...),
		Conflicts:           append([]CompilationConflict(nil), result.Conflicts...),
	}
	if doc.Constraints == nil {
		doc.Constraints = []string{}
	}
	if doc.NonGoals == nil {
		doc.NonGoals = []string{}
	}
	if doc.Terms == nil {
		doc.Terms = []CompilationTerm{}
	}
	if doc.Assumptions == nil {
		doc.Assumptions = []string{}
	}
	if doc.Conflicts == nil {
		doc.Conflicts = []CompilationConflict{}
	}
	for _, requirement := range result.Requirements {
		stable, ok := reqByKey[requirement.Key]
		if !ok {
			return doc, nil, "", fmt.Errorf("missing stable id for requirement key %q", requirement.Key)
		}
		acceptanceIDs := make([]string, 0, len(requirement.AcceptanceKeys))
		for _, key := range requirement.AcceptanceKeys {
			id, exists := accByKey[key]
			if !exists {
				return doc, nil, "", fmt.Errorf("missing stable id for acceptance key %q", key)
			}
			acceptanceIDs = append(acceptanceIDs, id)
		}
		doc.Requirements = append(doc.Requirements, NormalizedRequirement{
			ID: stable, Priority: requirement.Priority, Text: requirement.Text, AcceptanceIDs: acceptanceIDs,
		})
	}
	for _, acceptance := range result.AcceptanceScenarios {
		stable, ok := accByKey[acceptance.Key]
		if !ok {
			return doc, nil, "", fmt.Errorf("missing stable id for acceptance key %q", acceptance.Key)
		}
		doc.AcceptanceScenarios = append(doc.AcceptanceScenarios, NormalizedAcceptance{ID: stable, Text: acceptance.Text})
	}
	raw, err := marshalCanonicalJSON(doc)
	if err != nil {
		return doc, nil, "", err
	}
	return doc, raw, sha256Hex(raw), nil
}

func AssignStableCompilationIDs(compilationID string, result RequirementCompilationResult) (requirements, acceptances []ComplexIDMap) {
	for index, requirement := range result.Requirements {
		requirements = append(requirements, ComplexIDMap{
			CompilationID: compilationID, Kind: complexIDRequirementKind,
			TemporaryKey: requirement.Key, StableID: fmt.Sprintf("REQ-%03d", index+1), Ordinal: index,
		})
	}
	for index, acceptance := range result.AcceptanceScenarios {
		acceptances = append(acceptances, ComplexIDMap{
			CompilationID: compilationID, Kind: complexIDAcceptanceKind,
			TemporaryKey: acceptance.Key, StableID: fmt.Sprintf("ACC-%03d", index+1), Ordinal: index,
		})
	}
	return requirements, acceptances
}

// AssignStableCompilationIDsFromPrevious reuses previous-version identifiers
// when the steward named them, and otherwise allocates identifiers that do not
// collide with the previous version.
func AssignStableCompilationIDsFromPrevious(
	compilationID string,
	result RequirementCompilationResult,
	existingRequirementIDs, existingAcceptanceIDs []string,
) (requirements, acceptances []ComplexIDMap, err error) {
	allowedReq := map[string]struct{}{}
	for _, id := range existingRequirementIDs {
		allowedReq[id] = struct{}{}
	}
	allowedAcc := map[string]struct{}{}
	for _, id := range existingAcceptanceIDs {
		allowedAcc[id] = struct{}{}
	}
	usedReq := map[string]struct{}{}
	usedAcc := map[string]struct{}{}
	nextUnused := func(prefix string, next *int, allowed, used map[string]struct{}) string {
		for {
			id := fmt.Sprintf("%s-%03d", prefix, *next)
			*next++
			if _, exists := allowed[id]; exists {
				continue
			}
			if _, exists := used[id]; exists {
				continue
			}
			return id
		}
	}
	nextReq, nextAcc := 1, 1
	for index, requirement := range result.Requirements {
		stable := strings.TrimSpace(requirement.PreviousRequirementID)
		if stable != "" {
			if _, ok := allowedReq[stable]; !ok {
				return nil, nil, fmt.Errorf("previousRequirementId %s is not from the previous version", stable)
			}
			if _, used := usedReq[stable]; used {
				return nil, nil, fmt.Errorf("previousRequirementId %s is used more than once", stable)
			}
		} else {
			stable = nextUnused("REQ", &nextReq, allowedReq, usedReq)
		}
		usedReq[stable] = struct{}{}
		requirements = append(requirements, ComplexIDMap{
			CompilationID: compilationID, Kind: complexIDRequirementKind,
			TemporaryKey: requirement.Key, StableID: stable, Ordinal: index,
		})
	}
	for index, acceptance := range result.AcceptanceScenarios {
		stable := strings.TrimSpace(acceptance.PreviousAcceptanceID)
		if stable != "" {
			if _, ok := allowedAcc[stable]; !ok {
				return nil, nil, fmt.Errorf("previousAcceptanceId %s is not from the previous version", stable)
			}
			if _, used := usedAcc[stable]; used {
				return nil, nil, fmt.Errorf("previousAcceptanceId %s is used more than once", stable)
			}
		} else {
			stable = nextUnused("ACC", &nextAcc, allowedAcc, usedAcc)
		}
		usedAcc[stable] = struct{}{}
		acceptances = append(acceptances, ComplexIDMap{
			CompilationID: compilationID, Kind: complexIDAcceptanceKind,
			TemporaryKey: acceptance.Key, StableID: stable, Ordinal: index,
		})
	}
	return requirements, acceptances, nil
}

func ParseComplexEngineeringPlanResult(
	raw []byte,
	requestID, versionID, versionSHA, compilationSHA string,
	coverage ComplexCoverage,
) (ComplexEngineeringPlanResult, []byte, string, error) {
	return ParseComplexEngineeringPlanResultWithCatalog(raw, requestID, versionID, versionSHA, compilationSHA, coverage, FrozenComplexCheckCatalog())
}

// ParseComplexEngineeringPlanResultWithCatalog validates a planner result against the explicitly supplied frozen check catalog.
func ParseComplexEngineeringPlanResultWithCatalog(
	raw []byte,
	requestID, versionID, versionSHA, compilationSHA string,
	coverage ComplexCoverage,
	catalog []ComplexCheckSpec,
) (ComplexEngineeringPlanResult, []byte, string, error) {
	return parseComplexEngineeringPlanResult(raw, requestID, versionID, versionSHA, compilationSHA, coverage, catalog, ComplexProtocolVersion)
}

func parseComplexEngineeringPlanResult(raw []byte, requestID, versionID, versionSHA, compilationSHA string, coverage ComplexCoverage, catalog []ComplexCheckSpec, protocol int) (ComplexEngineeringPlanResult, []byte, string, error) {
	var result ComplexEngineeringPlanResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, nil, "", err
	}
	optional := []string{}
	if protocol == PlannerTaskContractVersion || protocol == ProjectPlanningVersion {
		optional = append(optional, "interfaceContracts")
	}
	fields, err := requireJSONObjectFieldsOptional(raw, []string{
		"schemaVersion", "kind", "technicalApproach",
		"tasks", "integrationCheckIds", "parallelSuggestion", "risks",
	}, optional)
	if err != nil {
		return result, nil, "", err
	}
	if _, err := requireJSONObjectFields(fields["parallelSuggestion"], "recommendedBuilderCount", "reason"); err != nil {
		return result, nil, "", fmt.Errorf("parallelSuggestion: %w", err)
	}
	var taskRaws []json.RawMessage
	if err := json.Unmarshal(fields["tasks"], &taskRaws); err != nil {
		return result, nil, "", fmt.Errorf("tasks: %w", err)
	}
	for index, taskRaw := range taskRaws {
		required := []string{
			"key", "title", "objective", "requirementIds", "acceptanceIds",
			"writePaths", "generatedPaths", "sharedPathsRequireApproval", "forbiddenPaths",
			"requiredCheckIds", "dependencyKeys",
		}
		if protocol == PlannerTaskContractVersion || protocol == ProjectPlanningVersion {
			required = append(required, "reviewCriteria")
		}
		if _, err := requireJSONObjectFields(taskRaw, required...); err != nil {
			return result, nil, "", fmt.Errorf("tasks[%d]: %w", index, err)
		}
	}
	result.TechnicalApproach = strings.TrimSpace(result.TechnicalApproach)
	result.ParallelSuggestion.Reason = strings.TrimSpace(result.ParallelSuggestion.Reason)
	result.PlanningRequestID = requestID
	result.RequirementVersionID = versionID
	result.RequirementVersionSHA256 = versionSHA
	result.CompilationSHA256 = compilationSHA
	if result.SchemaVersion != protocol || result.Kind != "COMPLEX_ENGINEERING_PLAN" {
		return result, nil, "", errors.New("engineering plan does not match the saved request")
	}
	if !validText(result.TechnicalApproach, MaxComplexAnswerRunes) || !validText(result.ParallelSuggestion.Reason, MaxComplexAnswerRunes) {
		return result, nil, "", errors.New("engineering plan text is empty or too long")
	}
	if result.ParallelSuggestion.RecommendedBuilderCount < 1 || result.ParallelSuggestion.RecommendedBuilderCount > 3 {
		return result, nil, "", errors.New("recommendedBuilderCount must be 1, 2, or 3")
	}
	if err := normalizePlanLists(&result); err != nil {
		return result, nil, "", err
	}
	if err := validateComplexPlanWithCatalog(result, coverage, catalog); err != nil {
		return result, nil, "", err
	}
	if protocol == PlannerTaskContractVersion || protocol == ProjectPlanningVersion {
		if err := validateInterfaceContractFields(fields["interfaceContracts"]); err != nil {
			return result, nil, "", err
		}
		if err := normalizeAndValidateTaskContracts(&result, coverage, catalog, protocol == ProjectPlanningVersion); err != nil {
			return result, nil, "", err
		}
	}
	normalized, err := marshalCanonicalJSON(result)
	if err != nil {
		return result, nil, "", err
	}
	return result, normalized, sha256Hex(normalized), nil
}

func ParseComplexPlanReviewResult(raw []byte, requestID, planID, planSHA string, coverage ComplexCoverage, taskKeys []string) (ComplexPlanReviewResult, []byte, error) {
	var result ComplexPlanReviewResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, nil, err
	}
	fields, err := requireJSONObjectFields(raw,
		"schemaVersion", "kind", "verdict", "reasonCode", "summary", "findings",
	)
	if err != nil {
		return result, nil, err
	}
	var findingRaws []json.RawMessage
	if err := json.Unmarshal(fields["findings"], &findingRaws); err != nil {
		return result, nil, fmt.Errorf("findings: %w", err)
	}
	for index, findingRaw := range findingRaws {
		if _, err := requireJSONObjectFields(findingRaw, "code", "message", "requirementIds", "taskKeys"); err != nil {
			return result, nil, fmt.Errorf("findings[%d]: %w", index, err)
		}
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.ReviewRequestID = requestID
	result.PlanID = planID
	result.PlanSHA256 = planSHA
	if result.SchemaVersion != ComplexProtocolVersion || result.Kind != "COMPLEX_PLAN_REVIEW" {
		return result, nil, errors.New("plan review does not match the saved plan")
	}
	if !validText(result.Summary, MaxComplexAnswerRunes) {
		return result, nil, errors.New("plan review summary is empty or too long")
	}
	valid := map[string]string{
		"APPROVED": "PLAN_ACCEPTABLE", "REPLAN": "REPLAN_REQUIRED", "NEEDS_HUMAN": "HUMAN_DECISION_REQUIRED",
	}
	if valid[result.Verdict] != result.ReasonCode {
		return result, nil, errors.New("plan review verdict and reason do not match")
	}
	if result.Findings == nil {
		result.Findings = []ComplexReviewFinding{}
	}
	if result.Verdict == "APPROVED" && len(result.Findings) != 0 {
		return result, nil, errors.New("APPROVED reviews cannot include findings")
	}
	if result.Verdict != "APPROVED" && (len(result.Findings) < 1 || len(result.Findings) > 20) {
		return result, nil, errors.New("REPLAN and NEEDS_HUMAN reviews need 1-20 findings")
	}
	knownTasks := map[string]struct{}{}
	for _, key := range taskKeys {
		knownTasks[key] = struct{}{}
	}
	for index := range result.Findings {
		finding := &result.Findings[index]
		finding.Code = strings.TrimSpace(finding.Code)
		finding.Message = strings.TrimSpace(finding.Message)
		if !validText(finding.Code, 80) || !validText(finding.Message, MaxComplexAnswerRunes) {
			return result, nil, fmt.Errorf("invalid review finding %d", index)
		}
		if finding.RequirementIDs == nil {
			finding.RequirementIDs = []string{}
		}
		if finding.TaskKeys == nil {
			finding.TaskKeys = []string{}
		}
		if len(finding.RequirementIDs) > 20 || len(finding.TaskKeys) > 6 {
			return result, nil, fmt.Errorf("review finding %d has too many references", index)
		}
		if err := requireUniqueStrings(finding.RequirementIDs); err != nil {
			return result, nil, fmt.Errorf("review finding %d requirementIds: %w", index, err)
		}
		if err := requireUniqueStrings(finding.TaskKeys); err != nil {
			return result, nil, fmt.Errorf("review finding %d taskKeys: %w", index, err)
		}
		for _, id := range finding.RequirementIDs {
			if _, ok := coverage.RequirementIDs[id]; !ok {
				return result, nil, fmt.Errorf("review finding %d names an unknown requirement", index)
			}
		}
		for _, key := range finding.TaskKeys {
			if _, ok := knownTasks[key]; !ok {
				return result, nil, fmt.Errorf("review finding %d names an unknown task", index)
			}
		}
	}
	normalized, err := marshalCanonicalJSON(result)
	if err != nil {
		return result, nil, err
	}
	return result, normalized, nil
}

func CoverageFromNormalizedDocument(doc NormalizedRequirementDocument) ComplexCoverage {
	coverage := ComplexCoverage{
		RequirementIDs: map[string]string{},
		AcceptanceIDs:  map[string]string{},
	}
	for _, requirement := range doc.Requirements {
		coverage.RequirementIDs[requirement.ID] = requirement.Priority
		if requirement.Priority == "MUST" {
			coverage.MUSTIDs = append(coverage.MUSTIDs, requirement.ID)
		}
	}
	for _, acceptance := range doc.AcceptanceScenarios {
		coverage.AcceptanceIDs[acceptance.ID] = acceptance.Text
	}
	return coverage
}

// CanonicalJSONBytes is the single canonical encoder for durable facts: no
// HTML escaping, no trailing newline. Two encoders for one value would make
// text comparisons of the same object fail on "<", ">" and "&".
func CanonicalJSONBytes(value any) ([]byte, error) {
	return marshalCanonicalJSON(value)
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	raw := buf.Bytes()
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	return raw, nil
}

func requireJSONObjectFieldsOptional(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("read JSON object fields: %w", err)
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return nil, fmt.Errorf("agent result is missing field %q", field)
		}
		allowed[field] = struct{}{}
	}
	for _, field := range optional {
		allowed[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return nil, fmt.Errorf("agent result has unknown field %q", field)
		}
	}
	return fields, nil
}

func validateCompilationNestedFields(fields map[string]json.RawMessage, allowPreviousIDs bool) error {
	var requirementRaws []json.RawMessage
	if err := json.Unmarshal(fields["requirements"], &requirementRaws); err != nil {
		return fmt.Errorf("requirements: %w", err)
	}
	requirementBase := []string{"key", "priority", "text", "acceptanceKeys"}
	requirementOptional := []string{}
	if allowPreviousIDs {
		requirementOptional = []string{"previousRequirementId"}
	}
	for index, item := range requirementRaws {
		if _, err := requireJSONObjectFieldsOptional(item, requirementBase, requirementOptional); err != nil {
			return fmt.Errorf("requirements[%d]: %w", index, err)
		}
	}
	var acceptanceRaws []json.RawMessage
	if err := json.Unmarshal(fields["acceptanceScenarios"], &acceptanceRaws); err != nil {
		return fmt.Errorf("acceptanceScenarios: %w", err)
	}
	acceptanceBase := []string{"key", "text"}
	acceptanceOptional := []string{}
	if allowPreviousIDs {
		acceptanceOptional = []string{"previousAcceptanceId"}
	}
	for index, item := range acceptanceRaws {
		if _, err := requireJSONObjectFieldsOptional(item, acceptanceBase, acceptanceOptional); err != nil {
			return fmt.Errorf("acceptanceScenarios[%d]: %w", index, err)
		}
	}
	var termRaws []json.RawMessage
	if err := json.Unmarshal(fields["terms"], &termRaws); err != nil {
		return fmt.Errorf("terms: %w", err)
	}
	for index, item := range termRaws {
		if _, err := requireJSONObjectFields(item, "term", "definition"); err != nil {
			return fmt.Errorf("terms[%d]: %w", index, err)
		}
	}
	var conflictRaws []json.RawMessage
	if err := json.Unmarshal(fields["conflicts"], &conflictRaws); err != nil {
		return fmt.Errorf("conflicts: %w", err)
	}
	for index, item := range conflictRaws {
		if _, err := requireJSONObjectFields(item, "text", "blocking"); err != nil {
			return fmt.Errorf("conflicts[%d]: %w", index, err)
		}
	}
	var questionRaws []json.RawMessage
	if err := json.Unmarshal(fields["blockingQuestions"], &questionRaws); err != nil {
		return fmt.Errorf("blockingQuestions: %w", err)
	}
	for index, item := range questionRaws {
		if _, err := requireJSONObjectFields(item, "key", "text", "reason", "requirementKeys"); err != nil {
			return fmt.Errorf("blockingQuestions[%d]: %w", index, err)
		}
	}
	return nil
}

func normalizeCompilationLists(result *RequirementCompilationResult) error {
	if result.Requirements == nil {
		result.Requirements = []CompilationRequirement{}
	}
	if result.AcceptanceScenarios == nil {
		result.AcceptanceScenarios = []CompilationAcceptance{}
	}
	if result.Constraints == nil {
		result.Constraints = []string{}
	}
	if result.NonGoals == nil {
		result.NonGoals = []string{}
	}
	if result.Terms == nil {
		result.Terms = []CompilationTerm{}
	}
	if result.Assumptions == nil {
		result.Assumptions = []string{}
	}
	if result.Conflicts == nil {
		result.Conflicts = []CompilationConflict{}
	}
	if result.BlockingQuestions == nil {
		result.BlockingQuestions = []CompilationBlockingQuestion{}
	}
	for i := range result.Requirements {
		result.Requirements[i].Key = strings.TrimSpace(result.Requirements[i].Key)
		result.Requirements[i].Priority = strings.TrimSpace(result.Requirements[i].Priority)
		result.Requirements[i].Text = strings.TrimSpace(result.Requirements[i].Text)
		result.Requirements[i].PreviousRequirementID = strings.TrimSpace(result.Requirements[i].PreviousRequirementID)
		if result.Requirements[i].AcceptanceKeys == nil {
			result.Requirements[i].AcceptanceKeys = []string{}
		}
	}
	for i := range result.AcceptanceScenarios {
		result.AcceptanceScenarios[i].Key = strings.TrimSpace(result.AcceptanceScenarios[i].Key)
		result.AcceptanceScenarios[i].Text = strings.TrimSpace(result.AcceptanceScenarios[i].Text)
		result.AcceptanceScenarios[i].PreviousAcceptanceID = strings.TrimSpace(result.AcceptanceScenarios[i].PreviousAcceptanceID)
	}
	for i := range result.Terms {
		result.Terms[i].Term = strings.TrimSpace(result.Terms[i].Term)
		result.Terms[i].Definition = strings.TrimSpace(result.Terms[i].Definition)
	}
	for i := range result.Conflicts {
		result.Conflicts[i].Text = strings.TrimSpace(result.Conflicts[i].Text)
	}
	for i := range result.BlockingQuestions {
		result.BlockingQuestions[i].Key = strings.TrimSpace(result.BlockingQuestions[i].Key)
		result.BlockingQuestions[i].Text = strings.TrimSpace(result.BlockingQuestions[i].Text)
		result.BlockingQuestions[i].Reason = strings.TrimSpace(result.BlockingQuestions[i].Reason)
		if result.BlockingQuestions[i].RequirementKeys == nil {
			result.BlockingQuestions[i].RequirementKeys = []string{}
		}
	}
	result.AdditionalRoundReason = strings.TrimSpace(result.AdditionalRoundReason)
	var err error
	result.Constraints, err = normalizeStringList(result.Constraints, 20)
	if err != nil {
		return prefixResultField("constraints", err)
	}
	result.NonGoals, err = normalizeStringList(result.NonGoals, 20)
	if err != nil {
		return prefixResultField("nonGoals", err)
	}
	result.Assumptions, err = normalizeStringList(result.Assumptions, 20)
	if err != nil {
		return prefixResultField("assumptions", err)
	}
	return nil
}

func validateCompilationOutcome(result RequirementCompilationResult, round int) error {
	switch round {
	case 0:
		if result.Outcome != "CLARIFICATION_REQUIRED" || result.AdditionalRoundReason != "" {
			return errors.New("round 0 must ask 1-8 blocking questions")
		}
		if len(result.BlockingQuestions) < 1 || len(result.BlockingQuestions) > MaxComplexClarifications {
			return errors.New("round 0 must ask 1-8 blocking questions")
		}
	case 1:
		switch result.Outcome {
		case "READY":
			if len(result.BlockingQuestions) != 0 || result.AdditionalRoundReason != "" {
				return errors.New("round 1 READY cannot include questions or an extra-round reason")
			}
		case "CLARIFICATION_REQUIRED":
			if err := validateResultText("additionalRoundReason", result.AdditionalRoundReason, MaxComplexAnswerRunes); err != nil {
				return err
			}
			if len(result.BlockingQuestions) < 1 || len(result.BlockingQuestions) > MaxComplexClarifications {
				return errors.New("round 1 extra questions must contain 1-8 items")
			}
		default:
			return errors.New("round 1 outcome must be READY or CLARIFICATION_REQUIRED")
		}
	case 2:
		if result.AdditionalRoundReason != "" {
			return errors.New("round 2 cannot include additionalRoundReason")
		}
		switch result.Outcome {
		case "READY":
			if len(result.BlockingQuestions) != 0 {
				return errors.New("round 2 READY cannot include questions")
			}
		case "NEEDS_HUMAN":
			if len(result.BlockingQuestions) < 1 || len(result.BlockingQuestions) > MaxComplexClarifications {
				return errors.New("round 2 NEEDS_HUMAN must include 1-8 unresolved questions")
			}
		default:
			return errors.New("round 2 outcome must be READY or NEEDS_HUMAN")
		}
	default:
		return errors.New("third compilation round is not allowed")
	}
	return nil
}

func validateCompilationReferences(result RequirementCompilationResult, allowPreviousIDs bool) error {
	if len(result.Requirements) < 1 || len(result.Requirements) > 50 {
		return errors.New("compilation must include 1-50 requirements")
	}
	if len(result.AcceptanceScenarios) < 1 || len(result.AcceptanceScenarios) > 100 {
		return errors.New("compilation must include 1-100 acceptance scenarios")
	}
	if len(result.Terms) > 20 || len(result.Conflicts) > 20 {
		return errors.New("terms and conflicts are limited to 20 items")
	}
	requirementKeys := map[string]CompilationRequirement{}
	for i, requirement := range result.Requirements {
		if err := validateResultText(fmt.Sprintf("requirements[%d].text", i), requirement.Text, MaxComplexAnswerRunes); err != nil {
			return err
		}
		if !validTemporaryKey(requirement.Key) || (requirement.Priority != "MUST" && requirement.Priority != "SHOULD") {
			return errors.New("requirement fields are invalid")
		}
		if requirement.PreviousRequirementID != "" && !allowPreviousIDs {
			return errors.New("initial compilation cannot carry previousRequirementId")
		}
		if _, exists := requirementKeys[requirement.Key]; exists {
			return errors.New("requirement keys must be unique")
		}
		if err := requireUniqueStrings(requirement.AcceptanceKeys); err != nil {
			return fmt.Errorf("requirement %s acceptanceKeys: %w", requirement.Key, err)
		}
		if len(requirement.AcceptanceKeys) < 1 || len(requirement.AcceptanceKeys) > 20 {
			return errors.New("each requirement needs 1-20 acceptanceKeys")
		}
		requirementKeys[requirement.Key] = requirement
	}
	acceptanceKeys := map[string]struct{}{}
	for i, acceptance := range result.AcceptanceScenarios {
		if err := validateResultText(fmt.Sprintf("acceptanceScenarios[%d].text", i), acceptance.Text, MaxComplexAnswerRunes); err != nil {
			return err
		}
		if !validTemporaryKey(acceptance.Key) {
			return errors.New("acceptance scenario fields are invalid")
		}
		if acceptance.PreviousAcceptanceID != "" && !allowPreviousIDs {
			return errors.New("initial compilation cannot carry previousAcceptanceId")
		}
		if _, exists := acceptanceKeys[acceptance.Key]; exists {
			return errors.New("acceptance keys must be unique")
		}
		acceptanceKeys[acceptance.Key] = struct{}{}
	}
	for _, requirement := range result.Requirements {
		if requirement.Priority == "MUST" && len(requirement.AcceptanceKeys) == 0 {
			return errors.New("each MUST requirement must reference an acceptance scenario")
		}
		for _, key := range requirement.AcceptanceKeys {
			if _, ok := acceptanceKeys[key]; !ok {
				return fmt.Errorf("requirement %s references unknown acceptance key %s", requirement.Key, key)
			}
		}
	}
	for i, term := range result.Terms {
		if err := validateResultText(fmt.Sprintf("terms[%d].term", i), term.Term, MaxComplexAnswerRunes); err != nil {
			return err
		}
		if err := validateResultText(fmt.Sprintf("terms[%d].definition", i), term.Definition, MaxComplexAnswerRunes); err != nil {
			return err
		}
	}
	blockingConflict := false
	for i, conflict := range result.Conflicts {
		if err := validateResultText(fmt.Sprintf("conflicts[%d].text", i), conflict.Text, MaxComplexAnswerRunes); err != nil {
			return err
		}
		if conflict.Blocking {
			blockingConflict = true
		}
	}
	if result.Outcome == "READY" && blockingConflict {
		return errors.New("READY compilations cannot contain blocking conflicts")
	}
	questionKeys := map[string]struct{}{}
	for i, question := range result.BlockingQuestions {
		if err := validateResultText(fmt.Sprintf("blockingQuestions[%d].text", i), question.Text, MaxComplexAnswerRunes); err != nil {
			return err
		}
		if err := validateResultText(fmt.Sprintf("blockingQuestions[%d].reason", i), question.Reason, MaxComplexAnswerRunes); err != nil {
			return err
		}
		if !validTemporaryKey(question.Key) {
			return errors.New("blocking question fields are invalid")
		}
		if _, exists := questionKeys[question.Key]; exists {
			return errors.New("blocking question keys must be unique")
		}
		questionKeys[question.Key] = struct{}{}
		if err := requireUniqueStrings(question.RequirementKeys); err != nil {
			return err
		}
		if len(question.RequirementKeys) < 1 || len(question.RequirementKeys) > 20 {
			return errors.New("each blocking question needs 1-20 requirementKeys")
		}
		for _, key := range question.RequirementKeys {
			if _, ok := requirementKeys[key]; !ok {
				return fmt.Errorf("blocking question %s references unknown requirement key %s", question.Key, key)
			}
		}
	}
	return nil
}

func normalizePlanLists(result *ComplexEngineeringPlanResult) error {
	if result.Tasks == nil {
		result.Tasks = []ComplexPlanTask{}
	}
	if result.IntegrationCheckIDs == nil {
		result.IntegrationCheckIDs = []string{}
	}
	if result.Risks == nil {
		result.Risks = []string{}
	}
	var err error
	result.Risks, err = normalizeStringList(result.Risks, 20)
	if err != nil {
		return fmt.Errorf("risks: %w", err)
	}
	result.IntegrationCheckIDs, err = normalizeStringList(result.IntegrationCheckIDs, 10)
	if err != nil {
		return fmt.Errorf("integrationCheckIds: %w", err)
	}
	for i := range result.Tasks {
		task := &result.Tasks[i]
		task.Key = strings.TrimSpace(task.Key)
		task.Title = strings.TrimSpace(task.Title)
		task.Objective = strings.TrimSpace(task.Objective)
		if task.RequirementIDs == nil {
			task.RequirementIDs = []string{}
		}
		if task.AcceptanceIDs == nil {
			task.AcceptanceIDs = []string{}
		}
		if task.WritePaths == nil {
			task.WritePaths = []string{}
		}
		if task.GeneratedPaths == nil {
			task.GeneratedPaths = []string{}
		}
		if task.SharedPathsRequireApproval == nil {
			task.SharedPathsRequireApproval = []string{}
		}
		if task.ForbiddenPaths == nil {
			task.ForbiddenPaths = []string{}
		}
		if task.RequiredCheckIDs == nil {
			task.RequiredCheckIDs = []string{}
		}
		if task.DependencyKeys == nil {
			task.DependencyKeys = []string{}
		}
	}
	return nil
}

func validateComplexPlan(result ComplexEngineeringPlanResult, coverage ComplexCoverage) error {
	return validateComplexPlanWithCatalog(result, coverage, FrozenComplexCheckCatalog())
}

func validateComplexPlanWithCatalog(result ComplexEngineeringPlanResult, coverage ComplexCoverage, catalog []ComplexCheckSpec) error {
	if len(result.Tasks) < MinComplexPlanTasks || len(result.Tasks) > MaxComplexPlanTasks {
		return errors.New("engineering plan must contain 1-6 tasks")
	}
	if len(result.IntegrationCheckIDs) < 1 {
		return errors.New("engineering plan must include at least one integration check")
	}
	for _, id := range result.IntegrationCheckIDs {
		if _, ok := ComplexCheckByID(catalog, id); !ok {
			return fmt.Errorf("unknown integration check %s", id)
		}
	}
	taskKeys := map[string]int{}
	coveredMUST := map[string]bool{}
	for index, task := range result.Tasks {
		if !validTemporaryKey(task.Key) || !validText(task.Title, MaxComplexAnswerRunes) || !validText(task.Objective, MaxComplexAnswerRunes) {
			return fmt.Errorf("task %d fields are invalid", index)
		}
		if _, exists := taskKeys[task.Key]; exists {
			return errors.New("task keys must be unique")
		}
		taskKeys[task.Key] = index
		if err := requireUniqueStrings(task.RequirementIDs); err != nil {
			return fmt.Errorf("task %s requirementIds: %w", task.Key, err)
		}
		if err := requireUniqueStrings(task.AcceptanceIDs); err != nil {
			return fmt.Errorf("task %s acceptanceIds: %w", task.Key, err)
		}
		if err := requireUniqueStrings(task.WritePaths); err != nil {
			return fmt.Errorf("task %s writePaths: %w", task.Key, err)
		}
		if err := requireUniqueStrings(task.GeneratedPaths); err != nil {
			return fmt.Errorf("task %s generatedPaths: %w", task.Key, err)
		}
		if err := requireUniqueStrings(task.SharedPathsRequireApproval); err != nil {
			return fmt.Errorf("task %s sharedPathsRequireApproval: %w", task.Key, err)
		}
		if err := requireUniqueStrings(task.ForbiddenPaths); err != nil {
			return fmt.Errorf("task %s forbiddenPaths: %w", task.Key, err)
		}
		if err := requireUniqueStrings(task.RequiredCheckIDs); err != nil {
			return fmt.Errorf("task %s requiredCheckIds: %w", task.Key, err)
		}
		if err := requireUniqueStrings(task.DependencyKeys); err != nil {
			return fmt.Errorf("task %s dependencyKeys: %w", task.Key, err)
		}
		if err := requireCount(task.RequirementIDs, 1, 20); err != nil {
			return fmt.Errorf("task %s requirementIds: %w", task.Key, err)
		}
		if err := requireCount(task.AcceptanceIDs, 1, 20); err != nil {
			return fmt.Errorf("task %s acceptanceIds: %w", task.Key, err)
		}
		if err := requireCount(task.WritePaths, 1, 20); err != nil {
			return fmt.Errorf("task %s writePaths: %w", task.Key, err)
		}
		if err := requireCount(task.RequiredCheckIDs, 1, 20); err != nil {
			return fmt.Errorf("task %s requiredCheckIds: %w", task.Key, err)
		}
		if err := requireCount(task.ForbiddenPaths, 1, 20); err != nil {
			return fmt.Errorf("task %s forbiddenPaths: %w", task.Key, err)
		}
		if err := requireCount(task.GeneratedPaths, 0, 20); err != nil {
			return fmt.Errorf("task %s generatedPaths: %w", task.Key, err)
		}
		if err := requireCount(task.SharedPathsRequireApproval, 0, 20); err != nil {
			return fmt.Errorf("task %s sharedPathsRequireApproval: %w", task.Key, err)
		}
		if err := requireCount(task.DependencyKeys, 0, 20); err != nil {
			return fmt.Errorf("task %s dependencyKeys: %w", task.Key, err)
		}
		for _, id := range task.RequirementIDs {
			priority, ok := coverage.RequirementIDs[id]
			if !ok {
				return fmt.Errorf("task %s references unknown requirement %s", task.Key, id)
			}
			if priority == "MUST" {
				coveredMUST[id] = true
			}
		}
		for _, id := range task.AcceptanceIDs {
			if _, ok := coverage.AcceptanceIDs[id]; !ok {
				return fmt.Errorf("task %s references unknown acceptance %s", task.Key, id)
			}
		}
		rules := PathRules{
			WritePaths: task.WritePaths, GeneratedPaths: task.GeneratedPaths,
			SharedPathsRequireApproval: task.SharedPathsRequireApproval, ForbiddenPaths: task.ForbiddenPaths,
		}
		if err := rules.Validate(); err != nil {
			return fmt.Errorf("task %s paths: %w", task.Key, err)
		}
		if result.SchemaVersion == ProjectPlanningVersion {
			for _, path := range append(append(append([]string{}, task.WritePaths...), task.GeneratedPaths...), task.SharedPathsRequireApproval...) {
				if !ProjectPath(path, false) {
					return fmt.Errorf("task %s has an unsafe project path %q", task.Key, path)
				}
			}
			for _, path := range task.ForbiddenPaths {
				if !ProjectPath(path, true) {
					return fmt.Errorf("task %s has an unsafe forbidden path %q", task.Key, path)
				}
			}
		} else {
			for _, path := range append(append(append(append([]string{}, task.WritePaths...), task.GeneratedPaths...), task.SharedPathsRequireApproval...), task.ForbiddenPaths...) {
				if !complexTemplatePathAllowed(path) {
					return fmt.Errorf("task %s path %q is outside the frozen template", task.Key, path)
				}
			}
		}
		hasCheck := false
		for _, id := range task.RequiredCheckIDs {
			if _, ok := ComplexCheckByID(catalog, id); !ok {
				return fmt.Errorf("task %s references unknown check %s", task.Key, id)
			}
			hasCheck = true
		}
		if !hasCheck {
			return fmt.Errorf("task %s has no executable check", task.Key)
		}
		if err := taskRequiredChecksCoverPathsWithCatalog(task, catalog); err != nil {
			return err
		}
	}
	for _, mustID := range coverage.MUSTIDs {
		if !coveredMUST[mustID] {
			return fmt.Errorf("MUST requirement %s is not mapped to a task", mustID)
		}
		mappedCheck := false
		for _, task := range result.Tasks {
			if !slices.Contains(task.RequirementIDs, mustID) {
				continue
			}
			if len(task.RequiredCheckIDs) > 0 {
				mappedCheck = true
				break
			}
		}
		if !mappedCheck {
			return fmt.Errorf("MUST requirement %s is not covered by a task check", mustID)
		}
	}
	for _, task := range result.Tasks {
		for _, dep := range task.DependencyKeys {
			if _, ok := taskKeys[dep]; !ok {
				return fmt.Errorf("task %s has dangling dependency %s", task.Key, dep)
			}
			if dep == task.Key {
				return fmt.Errorf("task %s depends on itself", task.Key)
			}
		}
	}
	if complexDependencyCyclic(result.Tasks) {
		return errors.New("engineering plan dependencies contain a cycle")
	}
	if len(result.Tasks) == 1 && result.ParallelSuggestion.RecommendedBuilderCount != 1 {
		return errors.New("a one-task plan must recommend exactly one builder")
	}
	return nil
}

func taskRequiredChecksCoverPaths(task ComplexPlanTask) error {
	return taskRequiredChecksCoverPathsWithCatalog(task, FrozenComplexCheckCatalog())
}

func taskRequiredChecksCoverPathsWithCatalog(task ComplexPlanTask, catalog []ComplexCheckSpec) error {
	paths := make([]string, 0, len(task.WritePaths)+len(task.GeneratedPaths)+len(task.SharedPathsRequireApproval))
	paths = append(paths, task.WritePaths...)
	paths = append(paths, task.GeneratedPaths...)
	paths = append(paths, task.SharedPathsRequireApproval...)
	for _, path := range paths {
		covered := false
		for _, id := range task.RequiredCheckIDs {
			spec, ok := ComplexCheckByID(catalog, id)
			if !ok {
				continue
			}
			if complexCheckCoversPath(spec, path) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("task %s path %q is not covered by a required check", task.Key, path)
		}
	}
	return nil
}

func complexCheckCoversPath(spec ComplexCheckSpec, path string) bool {
	return matchesAny(spec.MainPaths, path)
}

func complexTemplatePathAllowed(path string) bool {
	if path == "package.json" || path == "package-lock.json" {
		return true
	}
	for _, root := range []string{"src", "test", ".git", "backend", "frontend", "migrations"} {
		if path == root || path == root+"/**" || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func complexDependencyCyclic(tasks []ComplexPlanTask) bool {
	index := make(map[string]int, len(tasks))
	for i, task := range tasks {
		index[task.Key] = i
	}
	const visiting, visited = 1, 2
	state := make([]int, len(tasks))
	var visit func(int) bool
	visit = func(i int) bool {
		if state[i] == visited {
			return false
		}
		if state[i] == visiting {
			return true
		}
		state[i] = visiting
		for _, dep := range tasks[i].DependencyKeys {
			j, ok := index[dep]
			if !ok {
				continue
			}
			if visit(j) {
				return true
			}
		}
		state[i] = visited
		return false
	}
	for i := range tasks {
		if visit(i) {
			return true
		}
	}
	return false
}

func validTemporaryKey(value string) bool {
	return temporaryKeyPattern.MatchString(value)
}

func requireUniqueStrings(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate value %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func requireCount(values []string, min, max int) error {
	if len(values) < min || len(values) > max {
		return fmt.Errorf("need %d-%d items, got %d", min, max, len(values))
	}
	return nil
}

func normalizeStringList(values []string, max int) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for i, value := range values {
		trimmed := strings.TrimSpace(value)
		if err := validateResultText(fmt.Sprintf("[%d]", i), trimmed, MaxComplexAnswerRunes); err != nil {
			return nil, err
		}
		if _, exists := seen[trimmed]; exists {
			return nil, fmt.Errorf("duplicate value %q", trimmed)
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) > max {
		return nil, fmt.Errorf("too many items: %d", len(out))
	}
	return out, nil
}

func ValidateComplexPRDText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", errors.New("prdText is required")
	}
	if len(trimmed) > MaxComplexPRDBytes {
		return "", errors.New("prdText exceeds 65536 bytes")
	}
	if !utf8.ValidString(trimmed) {
		return "", errors.New("prdText is not valid UTF-8")
	}
	return trimmed, nil
}
