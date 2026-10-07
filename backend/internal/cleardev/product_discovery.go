package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Product goals reuse the existing development container for controlled Agent
// delivery, message accounting and audit history. The explicit product marker
// prevents that container from becoming an executable requirement. Only its
// separately linked stages may enter the existing requirement workflow.
const (
	ProductDiscoveryVersion = 1
	ProductMaxDiscussions   = 12
	ProductMaxStages        = 8
)

// ProductGoal is the durable identity of one product exploration container.
type ProductGoal struct {
	ID          string    `json:"id"`
	AOProjectID string    `json:"aoProjectId"`
	Name        string    `json:"name"`
	GoalText    string    `json:"goalText"`
	RequestID   string    `json:"requestId"`
	CreatedAt   time.Time `json:"createdAt"`
	// RequestedExecution preserves the original first-message input for replay.
	// Current project configuration and preflight records remain authoritative.
	RequestedExecution *domain.ClearDevExecutionConfig `json:"requestedExecution,omitempty"`
}

// ProductFeature is one user-visible capability named in a proposal.
type ProductFeature struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// ProductQuestion is one Steward question that changes scope, priority or acceptance.
type ProductQuestion struct {
	Key    string `json:"key"`
	Text   string `json:"text"`
	Reason string `json:"reason"`
}

// ProductStageDefinition specifies user-visible outcomes, not implementation
// steps, task paths, worker counts or an engineering DAG.
type ProductStageDefinition struct {
	Key                string                 `json:"key"`
	Title              string                 `json:"title"`
	Goal               string                 `json:"goal"`
	FeatureKeys        []string               `json:"featureKeys"`
	AcceptanceCriteria []string               `json:"acceptanceCriteria"`
	NonGoals           []string               `json:"nonGoals"`
	Feasibility        string                 `json:"feasibility"`
	FeasibilityReason  string                 `json:"feasibilityReason"`
	ExecutionBasis     *ProjectExecutionBasis `json:"executionBasis,omitempty"`
}

// ProductDiscoveryResult is model output containing choices and prose only.
// IDs, rounds, repository SHA, session identity and stage/requirement links are
// bound by the control plane.
type ProductDiscoveryResult struct {
	SchemaVersion      int                      `json:"schemaVersion"`
	Kind               string                   `json:"kind"`
	Outcome            string                   `json:"outcome"`
	Message            string                   `json:"message"`
	FeasibilitySummary string                   `json:"feasibilitySummary"`
	Questions          []ProductQuestion        `json:"questions"`
	Features           []ProductFeature         `json:"features"`
	Stages             []ProductStageDefinition `json:"stages"`
	Options            []ProductOption          `json:"options,omitempty"`
	SelectedOptionKey  string                   `json:"selectedOptionKey,omitempty"`
	Evidence           []ProductEvidence        `json:"evidence,omitempty"`
}

// ProductDiscussion is one immutable user message and its settled Steward reply.
type ProductDiscussion struct {
	ID              string                  `json:"id"`
	ProductID       string                  `json:"productId"`
	Ordinal         int                     `json:"ordinal"`
	UserMessage     string                  `json:"userMessage"`
	ProtocolVersion int                     `json:"protocolVersion,omitempty"`
	Selection       *ProductSelection       `json:"selection,omitempty"`
	AgentStepID     string                  `json:"agentStepId,omitempty"`
	Result          *ProductDiscoveryResult `json:"result,omitempty"`
	ResultSHA256    string                  `json:"resultSha256,omitempty"`
	FailureReason   string                  `json:"failureReason,omitempty"`
	CreatedAt       time.Time               `json:"createdAt"`
	SettledAt       *time.Time              `json:"settledAt,omitempty"`
}

// ProductStage binds one immutable proposal stage to its optional child requirement.
type ProductStage struct {
	ID                       string                 `json:"id"`
	ProductID                string                 `json:"productId"`
	DiscussionID             string                 `json:"discussionId"`
	Ordinal                  int                    `json:"ordinal"`
	Definition               ProductStageDefinition `json:"definition"`
	DefinitionSHA256         string                 `json:"definitionSha256"`
	DevelopmentRequirementID string                 `json:"developmentRequirementId,omitempty"`
	BaseCommitSHA            string                 `json:"baseCommitSha,omitempty"`
	Selection                *ProductSelection      `json:"selection,omitempty"`
	CreatedAt                time.Time              `json:"createdAt"`
}

// ProductSnapshot is the full durable state of one product goal.
type ProductSnapshot struct {
	Goal        ProductGoal         `json:"goal"`
	Discussions []ProductDiscussion `json:"discussions"`
	Stages      []ProductStage      `json:"stages"`
}

// CreateProductGoalCommand registers a product container and its first discussion atomically.
type CreateProductGoalCommand struct {
	Goal       ProductGoal
	Container  CreateComplexRequirementCommand
	Discussion ProductDiscussion
}

// AppendProductDiscussionCommand adds one user message after the settled previous round.
type AppendProductDiscussionCommand struct {
	Discussion         ProductDiscussion
	ExpectedPreviousID string
	Selection          *ProductSelection
}

// SettleProductDiscussionCommand binds one exact settled Steward step as the reply.
type SettleProductDiscussionCommand struct {
	ProductID    string
	DiscussionID string
	AgentStepID  string
	At           time.Time
}

// PrepareProductStageCommand binds a stage to a child requirement and selected baseline.
type PrepareProductStageCommand struct {
	ProductID        string
	StageID          string
	DefinitionSHA256 string
	BaseCommitSHA    string
	Container        CreateComplexRequirementCommand
}

// ParseProductDiscoveryResult strictly decodes and validates one Steward reply.
func ParseProductDiscoveryResult(raw []byte) (ProductDiscoveryResult, error) {
	var r ProductDiscoveryResult
	if err := decodeStrictAgentResult(raw, &r); err != nil {
		return r, err
	}
	if (r.SchemaVersion != ProductDiscoveryVersion && r.SchemaVersion != ProjectDiscoveryVersion) || r.Kind != "PRODUCT_DISCOVERY" {
		return r, errors.New("product discussion schemaVersion or kind is invalid")
	}
	if err := validateResultText("message", r.Message, 10000); err != nil {
		return r, err
	}
	if err := validateResultText("feasibilitySummary", r.FeasibilitySummary, 10000); err != nil {
		return r, err
	}
	if r.Questions == nil || r.Features == nil || r.Stages == nil || len(r.Questions) > 8 || len(r.Features) > 32 || len(r.Stages) > ProductMaxStages {
		return r, errors.New("product lists must be present and bounded")
	}
	switch r.Outcome {
	case "DISCUSS":
		if len(r.Questions) == 0 && (r.SchemaVersion != ProjectDiscoveryVersion || len(r.Options) == 0) {
			return r, errors.New("DISCUSS needs a question that changes scope, priority or acceptance")
		}
	case "READY":
		if len(r.Questions) != 0 || len(r.Features) == 0 || len(r.Stages) == 0 {
			return r, errors.New("READY needs a feature/stage proposal and no unresolved questions")
		}
	default:
		return r, errors.New("product outcome must be DISCUSS or READY")
	}
	questions, features, stages, assigned := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, q := range r.Questions {
		if err := validateResultText(fmt.Sprintf("questions[%d].text", i), q.Text, 10000); err != nil {
			return r, err
		}
		if err := validateResultText(fmt.Sprintf("questions[%d].reason", i), q.Reason, 10000); err != nil {
			return r, err
		}
		if !productKey(q.Key) || questions[q.Key] {
			return r, errors.New("invalid or duplicate product question")
		}
		questions[q.Key] = true
	}
	for i, f := range r.Features {
		if err := validateResultText(fmt.Sprintf("features[%d].title", i), f.Title, 200); err != nil {
			return r, err
		}
		if err := validateResultText(fmt.Sprintf("features[%d].description", i), f.Description, 10000); err != nil {
			return r, err
		}
		if !productKey(f.Key) || features[f.Key] {
			return r, errors.New("invalid or duplicate product feature")
		}
		features[f.Key] = true
	}
	for i, stage := range r.Stages {
		if err := validateResultText(fmt.Sprintf("stages[%d].title", i), stage.Title, 200); err != nil {
			return r, err
		}
		if err := validateResultText(fmt.Sprintf("stages[%d].goal", i), stage.Goal, 10000); err != nil {
			return r, err
		}
		if err := validateResultText(fmt.Sprintf("stages[%d].feasibilityReason", i), stage.FeasibilityReason, 10000); err != nil {
			return r, err
		}
		if !productKey(stage.Key) || stages[stage.Key] {
			return r, errors.New("invalid or duplicate product stage")
		}
		stages[stage.Key] = true
		if stage.Feasibility != "SUPPORTED" && stage.Feasibility != "NEEDS_CAPABILITY" {
			return r, errors.New("stage feasibility must be SUPPORTED or NEEDS_CAPABILITY")
		}
		if err := productTextList(stage.AcceptanceCriteria, 1, 20); err != nil {
			return r, prefixResultField(fmt.Sprintf("stages[%d].acceptanceCriteria", i), err)
		}
		if stage.ExecutionBasis != nil {
			if err := ValidateProjectTrialCoverage(stage.ExecutionBasis.Trial, stage.AcceptanceCriteria); err != nil {
				return r, err
			}
		}
		if err := productTextList(stage.NonGoals, 0, 20); err != nil {
			return r, prefixResultField(fmt.Sprintf("stages[%d].nonGoals", i), err)
		}
		if err := productTextList(stage.FeatureKeys, 1, 32); err != nil {
			return r, err
		}
		for _, key := range stage.FeatureKeys {
			if !features[key] || assigned[key] {
				return r, errors.New("each stage feature must exist and belong to only one stage")
			}
			assigned[key] = true
		}
	}
	if r.Outcome == "READY" && len(assigned) != len(features) {
		return r, errors.New("READY must place every proposed feature in a stage")
	}
	if r.SchemaVersion == ProjectDiscoveryVersion {
		if err := validateProductOptions(r); err != nil {
			return r, err
		}
	} else {
		if r.Options != nil || r.Evidence != nil || r.SelectedOptionKey != "" {
			return r, errors.New("legacy discovery cannot acquire project choices")
		}
		for _, stage := range r.Stages {
			if stage.ExecutionBasis != nil {
				return r, errors.New("legacy discovery cannot acquire generic execution permissions")
			}
		}
	}
	return r, nil
}

func productKey(key string) bool {
	if len(key) < 1 || len(key) > 40 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for _, c := range key {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func productTextList(values []string, minCount, maxCount int) error {
	if values == nil || len(values) < minCount || len(values) > maxCount {
		return errors.New("list is missing or outside its limit")
	}
	seen := map[string]bool{}
	for i, value := range values {
		if err := validateResultText(fmt.Sprintf("[%d]", i), value, 10000); err != nil {
			return err
		}
		if value != strings.TrimSpace(value) || seen[value] {
			return errors.New("list contains invalid or duplicate text")
		}
		seen[value] = true
	}
	return nil
}

// ProductStageJSON returns the canonical stage definition bytes and their digest.
func ProductStageJSON(stage ProductStageDefinition) ([]byte, string, error) {
	data, err := json.Marshal(stage)
	return data, sha256Hex(data), err
}

// ProductStagePRD is assembled from persisted product facts, never caller text.
// The old requirement compiler and native confirmation still decide the exact
// executable specification. The product proposal is not an approval bypass.
func ProductStagePRD(goal ProductGoal, stage ProductStageDefinition) string {
	var b strings.Builder
	fmt.Fprintf(&b, "产品目标：%s\n当前阶段：%s\n阶段目标：%s\n\n功能验收（必须逐条保留，不得以技术任务替代）：\n", goal.Name, stage.Title, stage.Goal)
	for _, text := range stage.AcceptanceCriteria {
		fmt.Fprintf(&b, "- %s\n", text)
	}
	b.WriteString("\n本阶段不做：\n")
	for _, text := range stage.NonGoals {
		fmt.Fprintf(&b, "- %s\n", text)
	}
	if stage.ExecutionBasis != nil {
		fmt.Fprintf(&b, "\n完整产品目标：%s\n%s\n", goal.GoalText, ProjectStageBasisConstraint(stage))
		b.WriteString("\n只规划本阶段。修改范围、依赖和命令是本项目的拟议执行依据，不是邮箱模板，也不是运行授权。规格经 Electron Human Authority 确认后生成工程计划；通用执行尚未开放，不能派发 Builder 或标记交付。\n")
	} else {
		b.WriteString("\n产品阶段来自 Steward 讨论，不是代码实现计划。只执行本阶段，不把其它阶段或整个产品目标并入当前交付。不扩大已有权限、路径、依赖或数据库限制。规格仍须经 Electron Human Authority 确认；未确认不得启动开发。\n")
	}
	return b.String()
}

// ParseProductStageCompilationResult validates a prepared stage's compiled
// specification. A prepared stage has already gone through product discovery;
// it need not repeat a mandatory question round, but none of its functional
// acceptance or non-goals may disappear during conversion into the executable
// specification.
func ParseProductStageCompilationResult(raw []byte, requestID, versionID, contextSHA string, round int, stage ProductStageDefinition) (RequirementCompilationResult, error) {
	r, err := parseRequirementCompilationResult(raw, requestID, versionID, contextSHA, round, false, true)
	if err != nil || r.Outcome != "READY" {
		return r, err
	}
	covered := map[string]bool{}
	for _, req := range r.Requirements {
		if req.Priority == "MUST" {
			for _, key := range req.AcceptanceKeys {
				covered[key] = true
			}
		}
	}
	for _, text := range stage.AcceptanceCriteria {
		found := false
		for _, acceptance := range r.AcceptanceScenarios {
			found = found || acceptance.Text == text && covered[acceptance.Key]
		}
		if !found {
			return r, errors.New("stage functional acceptance must remain verbatim and covered by a MUST")
		}
	}
	if basis := ProjectStageBasisConstraint(stage); basis != "" {
		found := false
		for _, constraint := range r.Constraints {
			found = found || ProductBasisConstraintPreserved(constraint, basis)
		}
		if !found {
			return r, errors.New("project execution basis must remain verbatim in confirmed constraints")
		}
	}
	for _, text := range stage.NonGoals {
		found := false
		for _, nonGoal := range r.NonGoals {
			found = found || nonGoal == text
		}
		if !found {
			return r, errors.New("stage non-goals must remain verbatim")
		}
	}
	return r, nil
}
