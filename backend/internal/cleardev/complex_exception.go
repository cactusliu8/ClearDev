package cleardev

import (
	"fmt"
	"strings"
	"time"
)

// Frozen S09 protocol, model, and per-role Chat turn budgets. Unused turns
// cannot move between roles.
const (
	ComplexExceptionProtocolVersion = 1
	ComplexExceptionModelSelection  = "codex-chat"

	ComplexExceptionBuilderMaxTurns          = 3
	ComplexExceptionReviewerMaxTurns         = 2
	ComplexExceptionStewardExceptionMaxTurns = 1
	ComplexExceptionSpecialistMaxTurns       = 1
	ComplexExceptionRecoveryMaxTurns         = 1
)

// On-demand role and Agent-step kinds for Specialist, Recovery, scope
// decisions, and Builder continuation.
const (
	StandardRoleRecoverySpecialist StandardRole = "RECOVERY_SPECIALIST"

	ComplexExceptionAgentStepScopeDecision   AgentStepKind = "SCOPE_EXPANSION_DECISION"
	ComplexExceptionAgentStepSpecialist      AgentStepKind = "SPECIALIST_RESULT"
	ComplexExceptionAgentStepRecovery        AgentStepKind = "RECOVERY_RESULT"
	ComplexExceptionAgentStepBuilderContinue AgentStepKind = "BUILDER_CONTINUE"
)

// Exception event subjects and actions are independent of S06–S08 execution
// subjects.
const (
	SubjectComplexExceptionScope      SubjectType = "COMPLEX_EXCEPTION_SCOPE"
	SubjectComplexExceptionOnDemand   SubjectType = "COMPLEX_EXCEPTION_ONDEMAND"
	SubjectComplexExceptionSpecialist SubjectType = "COMPLEX_EXCEPTION_SPECIALIST"
	SubjectComplexExceptionRecovery   SubjectType = "COMPLEX_EXCEPTION_RECOVERY"
	SubjectComplexExceptionBudget     SubjectType = "COMPLEX_EXCEPTION_BUDGET"

	ActionRecordComplexScopeRequest    Action = "RECORD_COMPLEX_SCOPE_REQUEST"
	ActionDecideComplexScopeRequest    Action = "DECIDE_COMPLEX_SCOPE_REQUEST"
	ActionBindComplexOnDemandRole      Action = "BIND_COMPLEX_ONDEMAND_ROLE"
	ActionRecordComplexSpecialist      Action = "RECORD_COMPLEX_SPECIALIST"
	ActionRecordComplexRecovery        Action = "RECORD_COMPLEX_RECOVERY"
	ActionOccupyComplexExceptionBudget Action = "OCCUPY_COMPLEX_EXCEPTION_BUDGET"
)

// Budget kinds, on-demand modes, scope decisions, recovery actions, and
// durable stop reasons for controlled exceptions.
const (
	ComplexExceptionBudgetBuilder          = "BUILDER"
	ComplexExceptionBudgetReviewer         = "REVIEWER"
	ComplexExceptionBudgetStewardException = "STEWARD_EXCEPTION"
	ComplexExceptionBudgetSpecialist       = "SPECIALIST"
	ComplexExceptionBudgetRecovery         = "RECOVERY"

	ComplexOnDemandModeSpecialist = "SPECIALIST"
	ComplexOnDemandModeRecovery   = "RECOVERY"

	ComplexScopeDecisionApprove    = "APPROVE"
	ComplexScopeDecisionNeedsHuman = "NEEDS_HUMAN"

	ComplexRecoveryActionRetryInfraCheck = "RETRY_SETTLED_INFRA_CHECK"
	ComplexRecoveryActionRebuildReviewer = "REBUILD_INDEPENDENT_REVIEWER"
	ComplexRecoveryActionRestoreSession  = "RESTORE_ORIGINAL_SESSION"

	ReasonScopePathNotListed       ReasonCode = "SCOPE_PATH_NOT_LISTED"
	ReasonScopeRequestInvalid      ReasonCode = "SCOPE_REQUEST_INVALID"
	ReasonScopeDecisionInvalid     ReasonCode = "SCOPE_DECISION_INVALID"
	ReasonScopePathForbidden       ReasonCode = "SCOPE_PATH_FORBIDDEN"
	ReasonScopeStaleBinding        ReasonCode = "SCOPE_STALE_BINDING"
	ReasonScopeDuplicateRequest    ReasonCode = "SCOPE_DUPLICATE_REQUEST"
	ReasonScopePathConcurrent      ReasonCode = "SCOPE_PATH_CONCURRENT"
	ReasonGeneratedProofMissing    ReasonCode = "GENERATED_PROOF_MISSING"
	ReasonGeneratedProofMismatch   ReasonCode = "GENERATED_PROOF_MISMATCH"
	ReasonSpecialistRequired       ReasonCode = "SPECIALIST_REQUIRED"
	ReasonSpecialistInvalid        ReasonCode = "SPECIALIST_RESULT_INVALID"
	ReasonSpecialistBindingChanged ReasonCode = "SPECIALIST_BINDING_CHANGED"
	ReasonRecoveryNotTriggered     ReasonCode = "RECOVERY_NOT_TRIGGERED"
	ReasonRecoveryNeedsHuman       ReasonCode = "RECOVERY_NEEDS_HUMAN"
	ReasonExceptionBudgetExhausted ReasonCode = "EXCEPTION_BUDGET_EXHAUSTED"
	ReasonQuickRejectsExceptions   ReasonCode = "QUICK_SHARED_OR_GENERATED_PATHS"
)

// FrozenGeneratedCommand is one argv-only generator. Agents cannot supply
// command, image, or output paths.
type FrozenGeneratedCommand struct {
	ID             string
	Argv           []string
	TimeoutSeconds int
	OutputPaths    []string
	Image          string
}

// FrozenSpecialistCheck is one argv-only specialist check.
type FrozenSpecialistCheck struct {
	ID             string
	Argv           []string
	TimeoutSeconds int
	MainPaths      []string
	Image          string
}

func frozenGeneratedLockScript() string {
	return `const fs=require("fs");const p=JSON.parse(fs.readFileSync("package.json","utf8"));const n=String(p.name||"app");const v=String(p.version||"0.0.0");const r={name:n,version:v};if(p.private)r.private=true;fs.writeFileSync("package-lock.json",JSON.stringify({name:n,version:v,lockfileVersion:3,requires:true,packages:{"":r}},null,2)+String.fromCharCode(10));`
}

func frozenSpecialistPackageScript() string {
	return `const fs=require("fs");JSON.parse(fs.readFileSync("package.json","utf8"));`
}

// FrozenGeneratedCommandCatalog is the argv-only generator list for this stage.
func FrozenGeneratedCommandCatalog() []FrozenGeneratedCommand {
	return []FrozenGeneratedCommand{{
		ID: "npm-package-lock", Argv: []string{"node", "-e", frozenGeneratedLockScript()},
		TimeoutSeconds: 60, OutputPaths: []string{"package-lock.json"}, Image: StandardRequiredImage,
	}}
}

// FrozenSpecialistCheckCatalog is the specialist check list for this stage.
func FrozenSpecialistCheckCatalog() []FrozenSpecialistCheck {
	return []FrozenSpecialistCheck{
		{
			ID: "package-json-specialist", Argv: []string{"node", "-e", frozenSpecialistPackageScript()},
			TimeoutSeconds: 60, MainPaths: []string{"package.json"}, Image: StandardRequiredImage,
		},
		{
			ID: "sqlite-migration-specialist", Argv: []string{"npm", "run", "check:migrations"},
			TimeoutSeconds: 60, MainPaths: []string{"migrations/**"}, Image: StandardRequiredImage,
		},
	}
}

// FrozenGeneratedCommandByID returns one frozen generator by identifier.
func FrozenGeneratedCommandByID(id string) (FrozenGeneratedCommand, bool) {
	for _, spec := range FrozenGeneratedCommandCatalog() {
		if spec.ID == id {
			return spec, true
		}
	}
	return FrozenGeneratedCommand{}, false
}

// FrozenSpecialistCheckByID returns one frozen specialist check by identifier.
func FrozenSpecialistCheckByID(id string) (FrozenSpecialistCheck, bool) {
	for _, spec := range FrozenSpecialistCheckCatalog() {
		if spec.ID == id {
			return spec, true
		}
	}
	return FrozenSpecialistCheck{}, false
}

// FrozenGeneratedCommandForPaths matches an exact frozen output-path set.
func FrozenGeneratedCommandForPaths(paths []string) (FrozenGeneratedCommand, bool) {
	normalized := uniqueQuickPaths(paths)
	for _, spec := range FrozenGeneratedCommandCatalog() {
		if sameStringSet(normalized, spec.OutputPaths) {
			return spec, true
		}
	}
	return FrozenGeneratedCommand{}, false
}

// FrozenSpecialistChecksForTask returns specialist checks covering this task.
func FrozenSpecialistChecksForTask(task ComplexPlanTask) []FrozenSpecialistCheck {
	out := []FrozenSpecialistCheck{}
	for _, spec := range FrozenSpecialistCheckCatalog() {
		if specialistCheckCoversTask(spec, task) {
			out = append(out, spec)
		}
	}
	return out
}

func specialistCheckCoversTask(spec FrozenSpecialistCheck, task ComplexPlanTask) bool {
	for _, path := range exceptionTaskPaths(task) {
		if matchesAny(spec.MainPaths, path) {
			return true
		}
	}
	return false
}

func exceptionTaskPaths(task ComplexPlanTask) []string {
	return uniqueQuickPaths(append(append(append([]string{}, task.WritePaths...), task.GeneratedPaths...), task.SharedPathsRequireApproval...))
}

// TaskRequiresSpecialist is true when a STANDARD/PARALLEL task touches a
// frozen high-risk category. Repository-control paths stay forbidden.
func TaskRequiresSpecialist(task ComplexPlanTask) bool {
	for _, path := range exceptionTaskPaths(task) {
		if exceptionPathIsRepoControl(path) {
			continue
		}
		if quickPathIsSensitive(path) {
			return true
		}
	}
	return false
}

func exceptionPathIsRepoControl(path string) bool {
	normalized, base, _ := quickNormalizePath(path)
	return quickSensitiveRepoControl(normalized, base)
}

// ComplexTaskDispatchPathsDisjoint reports whether two tasks can run in the
// same wave. Shared and generated paths are exclusive, the same as write paths.
func ComplexTaskDispatchPathsDisjoint(a, b ComplexPlanTask) bool {
	left := exceptionTaskPaths(a)
	right := exceptionTaskPaths(b)
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	for _, l := range left {
		for _, r := range right {
			if !complexPatternDomainsDisjoint(l, r) {
				return false
			}
		}
	}
	return true
}

// ComplexExceptionFacts are the durable S09 exception records. They are loaded
// with the execution snapshot and never stored as a second lifecycle.
type ComplexExceptionFacts struct {
	Budgets            []ComplexExceptionBudget        `json:"budgets"`
	ScopeRequests      []ComplexScopeExpansionRequest  `json:"scopeRequests"`
	ScopeDecisions     []ComplexScopeExpansionDecision `json:"scopeDecisions"`
	PermissionVersions []PermissionVersion             `json:"permissionVersions"`
	GeneratedCommands  []ComplexGeneratedCommandFact   `json:"generatedCommands"`
	GeneratedProofs    []ComplexGeneratedProof         `json:"generatedProofs"`
	OnDemandBindings   []ComplexOnDemandBinding        `json:"onDemandBindings"`
	OnDemandSteps      []AgentStep                     `json:"onDemandSteps"`
	SpecialistResults  []ComplexSpecialistResult       `json:"specialistResults"`
	SpecialistChecks   []ComplexSpecialistCheck        `json:"specialistChecks"`
	RecoveryActions    []ComplexRecoveryAction         `json:"recoveryActions"`
	PathLeases         []ComplexExceptionPathLease     `json:"pathLeases"`
}

// ComplexExceptionPathLease records exclusive use of a shared or generated path.
type ComplexExceptionPathLease struct {
	ID                     string     `json:"id"`
	ExecutionRunID         string     `json:"executionRunId"`
	ComplexExecutionTaskID string     `json:"complexExecutionTaskId"`
	Path                   string     `json:"path"`
	Status                 string     `json:"status"`
	CreatedAt              time.Time  `json:"createdAt"`
	ReleasedAt             *time.Time `json:"releasedAt,omitempty"`
}

func deriveComplexExceptionPhase(snapshot ComplexExecutionSnapshot) (ComplexExecutionPhase, ReasonCode, bool) {
	facts := snapshot.Exception
	if facts == nil {
		return "", ReasonNone, false
	}
	for _, binding := range facts.OnDemandBindings {
		if binding.Status == RoleBindingStatusFailed {
			reason := binding.ReasonCode
			if reason == ReasonNone {
				reason = ReasonComplexExecutionStopped
			}
			return ComplexExecutionBlocked, reason, true
		}
		if binding.Status == RoleBindingStatusRequested || binding.Status == RoleBindingStatusBound {
			if binding.Mode == ComplexOnDemandModeRecovery {
				return ComplexExecutionRecovering, binding.TriggerReason, true
			}
			return ComplexExecutionAwaitingSpecialist, ReasonSpecialistRequired, true
		}
	}
	for _, request := range facts.ScopeRequests {
		if request.Status == "PENDING" {
			return ComplexExecutionAwaitingScope, ReasonNone, true
		}
	}
	return "", ReasonNone, false
}

// ComplexExceptionBudget preserves the per-role business-step and rework limits.
// MaxTurns and UsedTurns are compatibility aliases for steps, not message sends.
type ComplexExceptionBudget struct {
	ID                     string    `json:"id"`
	ExecutionRunID         string    `json:"executionRunId"`
	ComplexExecutionTaskID string    `json:"complexExecutionTaskId,omitempty"`
	RoleKind               string    `json:"roleKind"`
	AllowedAgentTypes      []string  `json:"allowedAgentTypes"`
	ModelSelection         string    `json:"modelSelection"`
	MaxTurns               int       `json:"maxTurns"`
	UsedTurns              int       `json:"usedTurns"`
	MaxReworkCount         int       `json:"maxReworkCount"`
	AuthorizedExtraTurns   int       `json:"authorizedExtraTurns,omitempty"`
	CreatedAt              time.Time `json:"createdAt"`
}

// ComplexExceptionBudgetOccupancy reserves one business step; messages use a separate ledger.
type ComplexExceptionBudgetOccupancy struct {
	ID          string    `json:"id"`
	BudgetID    string    `json:"budgetId"`
	AgentStepID string    `json:"agentStepId"`
	OccupiedAt  time.Time `json:"occupiedAt"`
}

// ComplexScopeExpansionRequest is the Builder request for listed shared paths.
type ComplexScopeExpansionRequest struct {
	ID                     string     `json:"id"`
	ExecutionRunID         string     `json:"executionRunId"`
	ComplexExecutionTaskID string     `json:"complexExecutionTaskId"`
	DispatchID             string     `json:"dispatchId"`
	Round                  int        `json:"round"`
	RequirementVersionID   string     `json:"requirementVersionId"`
	RequirementSHA256      string     `json:"requirementSha256"`
	PlanID                 string     `json:"planId"`
	PlanSHA256             string     `json:"planSha256"`
	RequestedPaths         []string   `json:"requestedPaths"`
	AgentStepID            string     `json:"agentStepId"`
	Status                 string     `json:"status"`
	ReasonCode             ReasonCode `json:"reasonCode,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	SettledAt              *time.Time `json:"settledAt,omitempty"`
}

// ComplexScopeExpansionDecision is the Steward request and control-plane result.
type ComplexScopeExpansionDecision struct {
	ID                   string     `json:"id"`
	RequestID            string     `json:"requestId"`
	ExecutionRunID       string     `json:"executionRunId"`
	RequirementVersionID string     `json:"requirementVersionId"`
	RequirementSHA256    string     `json:"requirementSha256"`
	PlanID               string     `json:"planId"`
	PlanSHA256           string     `json:"planSha256"`
	Paths                []string   `json:"paths"`
	Decision             string     `json:"decision"`
	ReasonCode           ReasonCode `json:"reasonCode"`
	Summary              string     `json:"summary"`
	StewardRoleBindingID string     `json:"stewardRoleBindingId"`
	AgentStepID          string     `json:"agentStepId"`
	PermissionVersionID  string     `json:"permissionVersionId,omitempty"`
	ControlAccepted      bool       `json:"controlAccepted"`
	CreatedAt            time.Time  `json:"createdAt"`
}

// ComplexGeneratedCommandFact is the frozen generator bound to one task.
type ComplexGeneratedCommandFact struct {
	ID                     string    `json:"id"`
	ExecutionRunID         string    `json:"executionRunId"`
	ComplexExecutionTaskID string    `json:"complexExecutionTaskId"`
	CommandID              string    `json:"commandId"`
	Argv                   []string  `json:"argv"`
	TimeoutSeconds         int       `json:"timeoutSeconds"`
	OutputPaths            []string  `json:"outputPaths"`
	Image                  string    `json:"image"`
	CommandSpecSHA256      string    `json:"commandSpecSha256"`
	CreatedAt              time.Time `json:"createdAt"`
}

// ComplexGeneratedProof is one candidate's rebuild-and-hash comparison.
type ComplexGeneratedProof struct {
	ID                     string         `json:"id"`
	ExecutionRunID         string         `json:"executionRunId"`
	ComplexExecutionTaskID string         `json:"complexExecutionTaskId"`
	DispatchID             string         `json:"dispatchId"`
	CandidateCommitID      string         `json:"candidateCommitId"`
	CandidateCommitSHA     string         `json:"candidateCommitSha"`
	CommandFactID          string         `json:"commandFactId"`
	ContainerImageID       string         `json:"containerImageId"`
	OutputSHA256JSON       string         `json:"outputSha256Json"`
	Status                 string         `json:"status"`
	Result                 EvidenceResult `json:"result,omitempty"`
	ReasonCode             ReasonCode     `json:"reasonCode,omitempty"`
	CreatedAt              time.Time      `json:"createdAt"`
	SettledAt              *time.Time     `json:"settledAt,omitempty"`
}

// ComplexOnDemandBinding is one Specialist or Recovery role for this run.
type ComplexOnDemandBinding struct {
	ID                            string            `json:"id"`
	ExecutionRunID                string            `json:"executionRunId"`
	ComplexExecutionTaskID        string            `json:"complexExecutionTaskId,omitempty"`
	Mode                          string            `json:"mode"`
	TriggerReason                 ReasonCode        `json:"triggerReason,omitempty"`
	SessionCreationIdempotencyKey string            `json:"sessionCreationIdempotencyKey"`
	AOSessionID                   string            `json:"aoSessionId,omitempty"`
	WorkspacePath                 string            `json:"workspacePath,omitempty"`
	BaseCommitSHA                 string            `json:"baseCommitSha,omitempty"`
	Status                        RoleBindingStatus `json:"status"`
	ReasonCode                    ReasonCode        `json:"reasonCode,omitempty"`
	BindingFingerprint            string            `json:"bindingFingerprint"`
	RequestedAt                   time.Time         `json:"requestedAt"`
	BoundAt                       *time.Time        `json:"boundAt,omitempty"`
	EndedAt                       *time.Time        `json:"endedAt,omitempty"`
}

// ComplexSpecialistResult is the specialist PASS or stop for one task fingerprint.
type ComplexSpecialistResult struct {
	ID                     string     `json:"id"`
	ExecutionRunID         string     `json:"executionRunId"`
	ComplexExecutionTaskID string     `json:"complexExecutionTaskId"`
	OnDemandBindingID      string     `json:"onDemandBindingId"`
	AgentStepID            string     `json:"agentStepId"`
	BindingFingerprint     string     `json:"bindingFingerprint"`
	Outcome                string     `json:"outcome"`
	ConstraintsJSON        string     `json:"constraintsJson"`
	ReasonCode             ReasonCode `json:"reasonCode,omitempty"`
	Summary                string     `json:"summary"`
	CreatedAt              time.Time  `json:"createdAt"`
}

// ComplexSpecialistCheck is one frozen specialist check bound to a result.
type ComplexSpecialistCheck struct {
	ID                     string         `json:"id"`
	ExecutionRunID         string         `json:"executionRunId"`
	ComplexExecutionTaskID string         `json:"complexExecutionTaskId"`
	SpecialistResultID     string         `json:"specialistResultId"`
	CheckID                string         `json:"checkId"`
	Argv                   []string       `json:"argv"`
	ContainerImageID       string         `json:"containerImageId,omitempty"`
	Status                 string         `json:"status"`
	Result                 EvidenceResult `json:"result,omitempty"`
	ReasonCode             ReasonCode     `json:"reasonCode,omitempty"`
	CreatedAt              time.Time      `json:"createdAt"`
	SettledAt              *time.Time     `json:"settledAt,omitempty"`
}

// ComplexRecoveryAction preserves a Recovery proposal. Only ExecutionResult proves an actual outcome.
type ComplexRecoveryAction struct {
	ExecutionResult        *FixedRecoveryResult `json:"executionResult" nullable:"true"`
	ID                     string               `json:"id"`
	ExecutionRunID         string               `json:"executionRunId"`
	ComplexExecutionTaskID string               `json:"complexExecutionTaskId,omitempty"`
	OnDemandBindingID      string               `json:"onDemandBindingId"`
	AgentStepID            string               `json:"agentStepId"`
	TriggerReason          ReasonCode           `json:"triggerReason"`
	TriggerFactID          string               `json:"triggerFactId"`
	Action                 string               `json:"action"`
	Outcome                string               `json:"outcome"`
	RetryCheckRunID        string               `json:"retryCheckRunId,omitempty"`
	ReasonCode             ReasonCode           `json:"reasonCode,omitempty"`
	Summary                string               `json:"summary"`
	CreatedAt              time.Time            `json:"createdAt"`
}

// FreezeComplexExceptionBudgets returns the immutable per-task and run-level
// budgets. Unused turns cannot move between roles.
func FreezeComplexExceptionBudgets(runID string, tasks []ComplexExecutionTask, plan []ComplexPlanTask, maxReworkCount int, at time.Time) []ComplexExceptionBudget {
	// One turn is one round: each role gets its initial round plus this
	// task's rework rounds, never below the historical single-rework shapes.
	// The bounded mail policy keeps its wider frozen shapes via its override.
	builderTurns := ComplexExceptionBuilderMaxTurns
	if want := 1 + maxReworkCount; want > builderTurns {
		builderTurns = want
	}
	reviewerTurns := ComplexExceptionReviewerMaxTurns
	if want := 1 + maxReworkCount; want > reviewerTurns {
		reviewerTurns = want
	}
	planByKey := map[string]ComplexPlanTask{}
	for _, task := range plan {
		planByKey[task.Key] = task
	}
	out := []ComplexExceptionBudget{{
		ID: runID + ":budget:recovery", ExecutionRunID: runID, RoleKind: ComplexExceptionBudgetRecovery,
		AllowedAgentTypes: []string{string(StandardRoleRecoverySpecialist)}, ModelSelection: ComplexExceptionModelSelection,
		MaxTurns: ComplexExceptionRecoveryMaxTurns, MaxReworkCount: 0, CreatedAt: at,
	}}
	for _, task := range tasks {
		planTask := planByKey[task.TaskKey]
		out = append(out,
			ComplexExceptionBudget{
				ID: task.ID + ":budget:builder", ExecutionRunID: runID, ComplexExecutionTaskID: task.ID,
				RoleKind: ComplexExceptionBudgetBuilder, AllowedAgentTypes: []string{string(StandardRoleBuilder)},
				ModelSelection: ComplexExceptionModelSelection, MaxTurns: builderTurns,
				MaxReworkCount: maxReworkCount, CreatedAt: at,
			},
			ComplexExceptionBudget{
				ID: task.ID + ":budget:reviewer", ExecutionRunID: runID, ComplexExecutionTaskID: task.ID,
				RoleKind: ComplexExceptionBudgetReviewer, AllowedAgentTypes: []string{string(StandardRoleReviewer)},
				ModelSelection: ComplexExceptionModelSelection, MaxTurns: reviewerTurns,
				MaxReworkCount: 0, CreatedAt: at,
			},
			ComplexExceptionBudget{
				ID: task.ID + ":budget:steward-exception", ExecutionRunID: runID, ComplexExecutionTaskID: task.ID,
				RoleKind: ComplexExceptionBudgetStewardException, AllowedAgentTypes: []string{string(StandardRoleSteward)},
				ModelSelection: ComplexExceptionModelSelection, MaxTurns: ComplexExceptionStewardExceptionMaxTurns,
				MaxReworkCount: 0, CreatedAt: at,
			},
		)
		if TaskRequiresSpecialist(planTask) || len(planTask.SharedPathsRequireApproval) > 0 || len(planTask.GeneratedPaths) > 0 {
			out = append(out, ComplexExceptionBudget{
				ID: task.ID + ":budget:specialist", ExecutionRunID: runID, ComplexExecutionTaskID: task.ID,
				RoleKind: ComplexExceptionBudgetSpecialist, AllowedAgentTypes: []string{string(StandardRoleRecoverySpecialist)},
				ModelSelection: ComplexExceptionModelSelection, MaxTurns: ComplexExceptionSpecialistMaxTurns,
				MaxReworkCount: 0, CreatedAt: at,
			})
		}
	}
	return out
}

// BudgetHasRemaining is true when this role still has unused Chat turns.
func BudgetHasRemaining(budget ComplexExceptionBudget) bool {
	return budget.UsedTurns < budget.MaxTurns
}

// SpecialistBindingFingerprint hashes the current requirement, plan, and paths.
func SpecialistBindingFingerprint(run ComplexExecutionRun, task ComplexExecutionTask, planTask ComplexPlanTask) string {
	parts := []string{run.RequirementVersionID, run.RequirementSHA256, run.PlanID, run.PlanSHA256, task.TaskKey, strings.Join(exceptionTaskPaths(planTask), ",")}
	if command, ok := FrozenGeneratedCommandForPaths(planTask.GeneratedPaths); ok {
		parts = append(parts, command.ID, strings.Join(command.Argv, "\x00"), command.Image)
	}
	return sha256Hex([]byte(strings.Join(parts, "|")))
}

// ApprovedSharedPaths accepts only current-plan shared paths that are not forbidden.
func ApprovedSharedPaths(planTask ComplexPlanTask, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, fmt.Errorf("scope request has no paths")
	}
	allowed := map[string]struct{}{}
	for _, path := range planTask.SharedPathsRequireApproval {
		allowed[path] = struct{}{}
	}
	forbidden := PathRules{ForbiddenPaths: planTask.ForbiddenPaths, WritePaths: planTask.WritePaths, GeneratedPaths: planTask.GeneratedPaths, SharedPathsRequireApproval: planTask.SharedPathsRequireApproval}
	out := make([]string, 0, len(requested))
	seen := map[string]struct{}{}
	for _, path := range requested {
		if _, dup := seen[path]; dup {
			return nil, fmt.Errorf("%s: duplicate path", ReasonScopeDuplicateRequest)
		}
		seen[path] = struct{}{}
		classification, err := forbidden.ClassifyPath(path)
		if err != nil {
			return nil, err
		}
		if classification == PathForbidden {
			return nil, fmt.Errorf("%s", ReasonScopePathForbidden)
		}
		if _, ok := allowed[path]; !ok {
			return nil, fmt.Errorf("%s", ReasonScopePathNotListed)
		}
		out = append(out, path)
	}
	return out, nil
}

// PermissionRulesAfterScopeApproval moves approved shared paths into write paths.
func PermissionRulesAfterScopeApproval(current PathRules, approved []string) PathRules {
	write := uniqueQuickPaths(append(append([]string{}, current.WritePaths...), approved...))
	remaining := []string{}
	approvedSet := map[string]struct{}{}
	for _, path := range approved {
		approvedSet[path] = struct{}{}
	}
	for _, path := range current.SharedPathsRequireApproval {
		if _, ok := approvedSet[path]; !ok {
			remaining = append(remaining, path)
		}
	}
	return PathRules{
		WritePaths: write, GeneratedPaths: append([]string{}, current.GeneratedPaths...),
		SharedPathsRequireApproval: remaining, ForbiddenPaths: append([]string{}, current.ForbiddenPaths...),
	}
}

// RecoveryTriggerAllowed is the fixed failure set that may start Recovery.
func RecoveryTriggerAllowed(reason ReasonCode) bool {
	switch reason {
	case ReasonReworkLimitReached, ReasonCompositionConflict, ReasonCode("CHECKER_UNAVAILABLE"),
		ReasonCode("REVIEWER_UNAVAILABLE"), ReasonCode("REVIEW_TIMEOUT"), ReasonCode("BUILDER_UNAVAILABLE"),
		ReasonCode("BUILDER_TIMEOUT"), ReasonCode("REVIEW_WORKTREE_INVALID"), ReasonCode("BUILDER_WORKTREE_DIRTY"):
		return true
	default:
		return false
	}
}

// RecoveryActionAllowed is the fixed action set Recovery may request.
func RecoveryActionAllowed(action string) bool {
	switch action {
	case ComplexRecoveryActionRetryInfraCheck, ComplexRecoveryActionRebuildReviewer, ComplexRecoveryActionRestoreSession:
		return true
	default:
		return false
	}
}
