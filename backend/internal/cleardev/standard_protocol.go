package cleardev

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	StandardProtocolVersion = 1
	maxAgentResultBytes     = 32 * 1024
)

const (
	// StandardTaskTitle is the frozen STANDARD task title.
	StandardTaskTitle = "实现邮箱规范化"
	// StandardTaskObjective is the frozen STANDARD task objective.
	StandardTaskObjective = "实现已确认需求中的 normalizeEmail，并补充测试。"
	// StandardRequiredImage is the planner-facing STANDARD image requirement.
	StandardRequiredImage = "node:22-bookworm-slim"
	// StandardCandidateCheckImage is the immutable production checker image.
	StandardCandidateCheckImage = "node@sha256:d649c27dae7ba0137b3cef5dd75baa422c08dc3d9e3fc0c23dfb172dc3cc6436"
)

var (
	standardWritePaths     = []string{"src/email.js", "test/email.test.js"}
	standardForbiddenPaths = []string{".git/**", "package.json", "package-lock.json"}
)

// StandardCheckSpec is the executable, argv-only check contract emitted by the
// fixed S02 engineering plan. It deliberately has no shell or environment.
type StandardCheckSpec struct {
	Name           string   `json:"name"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

// StandardTaskPlan is the only plan shape S02 accepts.
type StandardTaskPlan struct {
	Title                      string              `json:"title"`
	Objective                  string              `json:"objective"`
	WritePaths                 []string            `json:"writePaths"`
	GeneratedPaths             []string            `json:"generatedPaths"`
	SharedPathsRequireApproval []string            `json:"sharedPathsRequireApproval"`
	ForbiddenPaths             []string            `json:"forbiddenPaths"`
	RequiredChecks             []StandardCheckSpec `json:"requiredChecks"`
	IntegrationCheck           StandardCheckSpec   `json:"integrationCheck"`
	MaxReworkCount             int                 `json:"maxReworkCount"`
}

// StandardExecutionPackage is the immutable, fully approved input shown to a
// Builder. Storage parses this exact shape again before accepting a dispatch or
// a checker run, so the service cannot accidentally separate executable facts
// from the plan which authorized them.
type StandardExecutionPackage struct {
	SchemaVersion        int              `json:"schemaVersion"`
	Mode                 string           `json:"mode"`
	DispatchID           string           `json:"dispatchId"`
	TaskID               string           `json:"taskId"`
	RequirementVersionID string           `json:"requirementVersionId"`
	RequirementSHA256    string           `json:"requirementSha256"`
	RequirementText      string           `json:"requirementText"`
	PlanID               string           `json:"planId"`
	PlanSHA256           string           `json:"planSha256"`
	Task                 StandardTaskPlan `json:"task"`
}

func FrozenStandardTaskPlan() StandardTaskPlan {
	return StandardTaskPlan{
		Title: StandardTaskTitle, Objective: StandardTaskObjective,
		WritePaths:     append([]string(nil), standardWritePaths...),
		GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{},
		ForbiddenPaths: append([]string(nil), standardForbiddenPaths...),
		RequiredChecks: []StandardCheckSpec{{
			Name: "email-unit", Argv: []string{"node", "--test", "test/email.test.js"}, TimeoutSeconds: 60,
		}},
		IntegrationCheck: StandardCheckSpec{
			Name: "node-all", Argv: []string{"node", "--test"}, TimeoutSeconds: 60,
		},
		MaxReworkCount: 1,
	}
}

// ParseStandardExecutionPackage accepts only the frozen S02 execution-package
// protocol. Dispatch-specific IDs are checked by storage against persisted
// requirement, plan, review and task facts.
func ParseStandardExecutionPackage(raw []byte) (StandardExecutionPackage, error) {
	var result StandardExecutionPackage
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	fields, err := requireJSONObjectFields(raw, "schemaVersion", "mode", "dispatchId", "taskId", "requirementVersionId", "requirementSha256", "requirementText", "planId", "planSha256", "task")
	if err != nil {
		return result, err
	}
	taskFields, err := requireJSONObjectFields(fields["task"], "title", "objective", "writePaths", "generatedPaths", "sharedPathsRequireApproval", "forbiddenPaths", "requiredChecks", "integrationCheck", "maxReworkCount")
	if err != nil {
		return result, fmt.Errorf("execution package task: %w", err)
	}
	var requiredChecks []json.RawMessage
	if err := json.Unmarshal(taskFields["requiredChecks"], &requiredChecks); err != nil {
		return result, fmt.Errorf("execution package requiredChecks: %w", err)
	}
	for index, check := range requiredChecks {
		if _, err := requireJSONObjectFields(check, "name", "argv", "timeoutSeconds"); err != nil {
			return result, fmt.Errorf("execution package requiredChecks[%d]: %w", index, err)
		}
	}
	if _, err := requireJSONObjectFields(taskFields["integrationCheck"], "name", "argv", "timeoutSeconds"); err != nil {
		return result, fmt.Errorf("execution package integrationCheck: %w", err)
	}
	if result.SchemaVersion != StandardProtocolVersion || result.Mode != string(WorkModeStandard) ||
		strings.TrimSpace(result.DispatchID) == "" || strings.TrimSpace(result.TaskID) == "" ||
		strings.TrimSpace(result.RequirementVersionID) == "" || !validProtocolSHA256(result.RequirementSHA256) ||
		strings.TrimSpace(result.RequirementText) == "" || strings.TrimSpace(result.PlanID) == "" ||
		!validProtocolSHA256(result.PlanSHA256) || !equalStandardTaskPlan(result.Task, FrozenStandardTaskPlan()) {
		return result, errors.New("execution package does not match the frozen STANDARD protocol")
	}
	return result, nil
}

// EqualStandardTaskPlan compares every ordered path and executable argument in
// two plan tasks. It is exported for the persistence trust boundary.
func EqualStandardTaskPlan(got, want StandardTaskPlan) bool {
	return equalStandardTaskPlan(got, want)
}

type PlanningResult struct {
	SchemaVersion        int    `json:"schemaVersion"`
	Kind                 string `json:"kind"`
	PlanningRequestID    string `json:"planningRequestId"`
	RequirementVersionID string `json:"requirementVersionId"`
	RequirementSHA256    string `json:"requirementSha256"`
	Decision             string `json:"decision"`
}

type EngineeringPlanResult struct {
	SchemaVersion        int              `json:"schemaVersion"`
	Kind                 string           `json:"kind"`
	PlanningRequestID    string           `json:"planningRequestId"`
	RequirementVersionID string           `json:"requirementVersionId"`
	RequirementSHA256    string           `json:"requirementSha256"`
	BaseTaskSetVersion   int64            `json:"baseTaskSetVersion"`
	Mode                 string           `json:"mode"`
	Task                 StandardTaskPlan `json:"task"`
}

type PlanReviewResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	PlanID        string `json:"planId"`
	PlanSHA256    string `json:"planSha256"`
	Verdict       string `json:"verdict"`
	ReasonCode    string `json:"reasonCode"`
	Summary       string `json:"summary"`
}

type DispatchRequestResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	PlanID        string `json:"planId"`
	PlanSHA256    string `json:"planSha256"`
	Mode          string `json:"mode"`
	Decision      string `json:"decision"`
}

type BuilderResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"`
	DispatchID    string `json:"dispatchId"`
	TaskID        string `json:"taskId"`
	Round         int    `json:"round"`
	Outcome       string `json:"outcome"`
	Summary       string `json:"summary"`
}

type ReviewFinding struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type LocalReviewResult struct {
	SchemaVersion      int             `json:"schemaVersion"`
	Kind               string          `json:"kind"`
	ReviewAssignmentID string          `json:"reviewAssignmentId"`
	CandidateID        string          `json:"candidateId"`
	CandidateSHA       string          `json:"candidateSha"`
	ReviewPacketSHA256 string          `json:"reviewPacketSha256"`
	Verdict            string          `json:"verdict"`
	ReasonCode         string          `json:"reasonCode"`
	Summary            string          `json:"summary"`
	Findings           []ReviewFinding `json:"findings"`
}

type StatusReportResult struct {
	SchemaVersion            int    `json:"schemaVersion"`
	Kind                     string `json:"kind"`
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	Phase                    string `json:"phase"`
	Attention                string `json:"attention"`
	Summary                  string `json:"summary"`
}

func ParsePlanningResult(raw []byte, requestID, versionID, requirementSHA string) (PlanningResult, error) {
	var result PlanningResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "decision"); err != nil {
		return result, err
	}
	result.PlanningRequestID = requestID
	result.RequirementVersionID = versionID
	result.RequirementSHA256 = requirementSHA
	if result.SchemaVersion != StandardProtocolVersion || result.Kind != "REQUEST_PLANNING" ||
		result.Decision != "PLAN" {
		return result, errors.New("planning result does not match the saved request")
	}
	return result, nil
}

func ParseEngineeringPlanResult(raw []byte, requestID, versionID, requirementSHA string, baseTaskSet int64) (EngineeringPlanResult, []byte, string, error) {
	var result EngineeringPlanResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, nil, "", err
	}
	fields, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "mode", "task")
	if err != nil {
		return result, nil, "", err
	}
	taskFields, err := requireJSONObjectFields(fields["task"], "title", "objective", "writePaths", "generatedPaths", "sharedPathsRequireApproval", "forbiddenPaths", "requiredChecks", "integrationCheck", "maxReworkCount")
	if err != nil {
		return result, nil, "", fmt.Errorf("engineering plan task: %w", err)
	}
	var requiredChecks []json.RawMessage
	if err := json.Unmarshal(taskFields["requiredChecks"], &requiredChecks); err != nil {
		return result, nil, "", fmt.Errorf("engineering plan requiredChecks: %w", err)
	}
	for index, check := range requiredChecks {
		if _, err := requireJSONObjectFields(check, "name", "argv", "timeoutSeconds"); err != nil {
			return result, nil, "", fmt.Errorf("engineering plan requiredChecks[%d]: %w", index, err)
		}
	}
	if _, err := requireJSONObjectFields(taskFields["integrationCheck"], "name", "argv", "timeoutSeconds"); err != nil {
		return result, nil, "", fmt.Errorf("engineering plan integrationCheck: %w", err)
	}
	result.Task.Title = strings.TrimSpace(result.Task.Title)
	result.Task.Objective = strings.TrimSpace(result.Task.Objective)
	result.PlanningRequestID = requestID
	result.RequirementVersionID = versionID
	result.RequirementSHA256 = requirementSHA
	result.BaseTaskSetVersion = baseTaskSet
	if result.SchemaVersion != StandardProtocolVersion || result.Kind != "ENGINEERING_PLAN" ||
		result.Mode != "STANDARD" || !equalStandardTaskPlan(result.Task, FrozenStandardTaskPlan()) {
		return result, nil, "", errors.New("PLAN_OUT_OF_SCOPE")
	}
	normalized, err := json.Marshal(result)
	if err != nil {
		return result, nil, "", err
	}
	return result, normalized, sha256Hex(normalized), nil
}

func ParsePlanReviewResult(raw []byte, planID, planSHA string) (PlanReviewResult, error) {
	var result PlanReviewResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "verdict", "reasonCode", "summary"); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.PlanID = planID
	result.PlanSHA256 = planSHA
	valid := map[string]string{
		"APPROVED": "PLAN_ACCEPTABLE", "REPLAN": "REPLAN_REQUIRED", "NEEDS_HUMAN": "HUMAN_DECISION_REQUIRED",
	}
	if result.SchemaVersion != StandardProtocolVersion || result.Kind != "PLAN_REVIEW" ||
		valid[result.Verdict] != result.ReasonCode || !validText(result.Summary, 10000) {
		return result, errors.New("plan review does not match the saved plan")
	}
	return result, nil
}

func ParseDispatchRequestResult(raw []byte, planID, planSHA string) (DispatchRequestResult, error) {
	var result DispatchRequestResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "mode", "decision"); err != nil {
		return result, err
	}
	result.PlanID = planID
	result.PlanSHA256 = planSHA
	if result.SchemaVersion != StandardProtocolVersion || result.Kind != "DISPATCH_REQUEST" ||
		result.Mode != "STANDARD" || result.Decision != "DISPATCH" {
		return result, errors.New("dispatch result does not match the approved plan")
	}
	return result, nil
}

func ParseBuilderResult(raw []byte, dispatchID, taskID string, round int) (BuilderResult, error) {
	var result BuilderResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "outcome", "summary"); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.DispatchID = dispatchID
	result.TaskID = taskID
	result.Round = round
	if result.SchemaVersion != StandardProtocolVersion {
		return result, fmt.Errorf("builder result schemaVersion must be %d", StandardProtocolVersion)
	}
	if result.Kind != "BUILDER_RESULT" {
		return result, errors.New("builder result kind must be BUILDER_RESULT")
	}
	if result.Outcome != "CANDIDATE_READY" && result.Outcome != "BLOCKED" && result.Outcome != "NEEDS_HUMAN" {
		return result, errors.New("builder result outcome must be CANDIDATE_READY, BLOCKED or NEEDS_HUMAN")
	}
	if err := validateResultText("summary", result.Summary, 10000); err != nil {
		return result, err
	}
	return result, nil
}

func ParseLocalReviewResult(raw []byte, assignmentID, candidateID, candidateSHA, packetSHA string, diffPaths []string) (LocalReviewResult, error) {
	var result LocalReviewResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	fields, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "verdict", "reasonCode", "summary", "findings")
	if err != nil {
		return result, err
	}
	var findings []json.RawMessage
	if err := json.Unmarshal(fields["findings"], &findings); err != nil {
		return result, fmt.Errorf("local review findings: %w", err)
	}
	for index, finding := range findings {
		if _, err := requireJSONObjectFields(finding, "severity", "path", "message"); err != nil {
			return result, fmt.Errorf("local review findings[%d]: %w", index, err)
		}
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.ReviewAssignmentID = assignmentID
	result.CandidateID = candidateID
	result.CandidateSHA = candidateSHA
	result.ReviewPacketSHA256 = packetSHA
	if result.SchemaVersion != StandardProtocolVersion || result.Kind != "LOCAL_REVIEW" ||
		!validText(result.Summary, 10000) || len(result.Findings) > 20 {
		return result, errors.New("local review does not match the saved assignment")
	}
	validReason := map[string]string{
		"PASS": "REVIEW_PASSED", "REWORK": "REVIEW_CHANGES_REQUIRED",
		"BLOCKED": "REVIEW_BLOCKED", "NEEDS_HUMAN": "REVIEW_NEEDS_HUMAN",
	}
	if validReason[result.Verdict] != result.ReasonCode {
		return result, errors.New("local review verdict and reason do not match")
	}
	paths := make(map[string]struct{}, len(diffPaths))
	for _, path := range diffPaths {
		paths[path] = struct{}{}
	}
	blocking := 0
	for index := range result.Findings {
		finding := &result.Findings[index]
		finding.Path = strings.TrimSpace(finding.Path)
		finding.Message = strings.TrimSpace(finding.Message)
		if (finding.Severity != "BLOCKING" && finding.Severity != "NOTE") ||
			!validText(finding.Path, 256) || !validText(finding.Message, 10000) {
			return result, fmt.Errorf("invalid review finding %d", index)
		}
		if _, ok := paths[finding.Path]; !ok {
			return result, fmt.Errorf("review finding %d names a path outside the saved diff", index)
		}
		if finding.Severity == "BLOCKING" {
			blocking++
		}
	}
	if (result.Verdict == "PASS" && blocking != 0) || (result.Verdict == "REWORK" && blocking == 0) {
		return result, errors.New("review findings do not support the verdict")
	}
	return result, nil
}

func ParseStatusReportResult(raw []byte, requirementID, phase, attention string) (StatusReportResult, error) {
	var result StatusReportResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "summary"); err != nil {
		return result, err
	}
	result.Summary = strings.TrimSpace(result.Summary)
	result.DevelopmentRequirementID = requirementID
	result.Phase = phase
	result.Attention = attention
	if result.SchemaVersion != StandardProtocolVersion || result.Kind != "STATUS_REPORT" ||
		!validText(result.Summary, 10000) {
		return result, errors.New("status report does not match derived progress")
	}
	return result, nil
}

func equalStandardTaskPlan(got, want StandardTaskPlan) bool {
	if got.Title != want.Title || got.Objective != want.Objective || got.MaxReworkCount != want.MaxReworkCount ||
		!slices.Equal(got.WritePaths, want.WritePaths) || !slices.Equal(got.GeneratedPaths, want.GeneratedPaths) ||
		!slices.Equal(got.SharedPathsRequireApproval, want.SharedPathsRequireApproval) ||
		!slices.Equal(got.ForbiddenPaths, want.ForbiddenPaths) || len(got.RequiredChecks) != len(want.RequiredChecks) {
		return false
	}
	for i := range got.RequiredChecks {
		if !equalCheckSpec(got.RequiredChecks[i], want.RequiredChecks[i]) {
			return false
		}
	}
	return equalCheckSpec(got.IntegrationCheck, want.IntegrationCheck)
}

func equalCheckSpec(got, want StandardCheckSpec) bool {
	return got.Name == want.Name && got.TimeoutSeconds == want.TimeoutSeconds && slices.Equal(got.Argv, want.Argv)
}

func validText(value string, maximum int) bool {
	return value != "" && utf8.RuneCountInString(value) <= maximum
}

func validProtocolSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("%x", digest)
}

func decodeStrictAgentResult(raw []byte, out any) error {
	if len(raw) > maxAgentResultBytes {
		return &AgentResultValidationError{Code: "RESULT_TOO_LARGE", Actual: len(raw), Limit: maxAgentResultBytes}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return &AgentResultValidationError{Code: "RESULT_EMPTY", Actual: len(raw)}
	}
	if !utf8.Valid(raw) {
		return &AgentResultValidationError{Code: "RESULT_ENCODING_INVALID"}
	}
	if err := validateStrictJSON(raw); err != nil {
		return describeResultJSONError(raw, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode agent result: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return nil
}

// validateStrictJSON rejects duplicate object keys and null at any depth.
// encoding/json deliberately accepts both, which is useful for ordinary APIs
// but ambiguous for a result that authorizes a durable control transition.
func validateStrictJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("read agent result: %w", err)
	}
	if first != json.Delim('{') {
		return errors.New("agent result must be exactly one JSON object")
	}
	if err := consumeJSONObject(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func consumeJSONObject(decoder *json.Decoder) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("JSON object key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("agent result repeats JSON field %q", key)
		}
		seen[key] = struct{}{}
		if err := consumeJSONValue(decoder); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return errors.New("unterminated JSON object")
	}
	return nil
}

func consumeJSONArray(decoder *json.Decoder) error {
	for decoder.More() {
		if err := consumeJSONValue(decoder); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim(']') {
		return errors.New("unterminated JSON array")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("agent result cannot contain null")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return consumeJSONObject(decoder)
	case '[':
		return consumeJSONArray(decoder)
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("agent result must contain exactly one JSON object")
		}
		return fmt.Errorf("read trailing agent result: %w", err)
	}
	return nil
}

// requireJSONObjectFields makes protocol fields genuinely required. Go's JSON
// decoder rejects unknown fields but otherwise maps a missing field to its zero
// value. Bound identifiers and hashes are omitted from Agent JSON and assigned
// by the control plane, so this check is the agent-chosen key set only.
func requireJSONObjectFields(raw []byte, expected ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("read JSON object fields: %w", err)
	}
	if len(fields) != len(expected) {
		return nil, errors.New("agent result has missing or extra fields")
	}
	for _, field := range expected {
		if _, ok := fields[field]; !ok {
			return nil, fmt.Errorf("agent result is missing field %q", field)
		}
	}
	return fields, nil
}
