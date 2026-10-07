package cleardev

import (
	"fmt"
	"math"
	"path"
	"strings"
	"time"
)

// ComplexQuickMaxFiles keeps one follow-up limited to two concrete files.
// QUICK task-set versions are derived from the completed source execution.
const ComplexQuickMaxFiles = 2

// NextComplexQuickTaskSetVersion returns the only task-set version a QUICK
// run may accept after its completed source execution.
func NextComplexQuickTaskSetVersion(source int64) (int64, error) {
	if source <= 0 || source == math.MaxInt64 {
		return 0, fmt.Errorf("quick execution source task-set version is invalid")
	}
	return source + 1, nil
}

// Selection and stop reasons persisted on the independent QUICK run. Failure
// never falls back to STANDARD or PARALLEL.
const (
	ReasonQuickFollowUpApproved    ReasonCode = "QUICK_FOLLOW_UP_APPROVED"
	ReasonQuickNeedsHuman          ReasonCode = "HUMAN_DECISION_REQUIRED"
	ReasonQuickNotSingleCompleted  ReasonCode = "QUICK_NOT_SINGLE_COMPLETED_TASK"
	ReasonQuickPathsNotSubset      ReasonCode = "QUICK_PATHS_NOT_SUBSET"
	ReasonQuickFileLimitExceeded   ReasonCode = "QUICK_FILE_LIMIT_EXCEEDED"
	ReasonQuickUnknownIDs          ReasonCode = "QUICK_UNKNOWN_IDS"
	ReasonQuickSharedOrGenerated   ReasonCode = "QUICK_SHARED_OR_GENERATED_PATHS"
	ReasonQuickSensitivePath       ReasonCode = "QUICK_SENSITIVE_PATH"
	ReasonQuickChecksNotFromPlan   ReasonCode = "QUICK_CHECKS_NOT_FROM_PLAN"
	ReasonQuickIntegrationMismatch ReasonCode = "QUICK_INTEGRATION_BASE_MISMATCH"
	ReasonQuickTaskSetChanged      ReasonCode = "QUICK_TASK_SET_CHANGED"
	ReasonQuickUnsafe              ReasonCode = "QUICK_UNSAFE"
)

// ComplexQuickAgentStepRequest is the Steward follow-up dispatch kind.
const (
	ComplexQuickAgentStepRequest AgentStepKind = "DISPATCH_REQUEST"
)

// QUICK event subjects and actions are independent of S06/S07 execution runs.
const (
	SubjectComplexQuickRun      SubjectType = "COMPLEX_QUICK_RUN"
	SubjectComplexQuickDispatch SubjectType = "COMPLEX_QUICK_DISPATCH"
	ActionRequestComplexQuick   Action      = "REQUEST_COMPLEX_QUICK"
	ActionAcceptComplexQuick    Action      = "ACCEPT_COMPLEX_QUICK"
	ActionRejectComplexQuick    Action      = "REJECT_COMPLEX_QUICK"
	ActionCompleteComplexQuick  Action      = "COMPLETE_COMPLEX_QUICK"
)

// ComplexQuickSelection is the control-plane decision after a Steward follow-up
// request. Callers and agents never choose the mode.
type ComplexQuickSelection struct {
	Mode       WorkMode
	ReasonCode ReasonCode
	SourceTask ComplexPlanTask
	Request    ComplexQuickTaskRequestResult
}

// SelectComplexQuickExecution applies the frozen S08 low-risk rules. Failure
// never falls back to STANDARD or PARALLEL.
func SelectComplexQuickExecution(request ComplexQuickTaskRequestResult, source ComplexPlanTask, plan ComplexEngineeringPlanResult, version RequirementVersion, integrationSHA string, sourceTaskDone bool) (ComplexQuickSelection, error) {
	selection := ComplexQuickSelection{Mode: WorkModeQuick, Request: request, SourceTask: source}
	if request.Decision == string(ComplexExecutionDecisionNeedsHuman) {
		selection.ReasonCode = ReasonQuickNeedsHuman
		return selection, nil
	}
	if !sourceTaskDone || strings.TrimSpace(source.Key) == "" || request.SourceTaskKey != source.Key {
		return rejectedQuick(ReasonQuickNotSingleCompleted)
	}
	if version.TaskSetVersion <= 0 || request.TaskSetVersion != version.TaskSetVersion {
		return rejectedQuick(ReasonQuickTaskSetChanged)
	}
	if request.RequirementVersionID != version.ID || request.RequirementSHA256 != version.SHA256 || request.IntegrationCommitSHA != integrationSHA || !validComplexExecutionCommitSHA(integrationSHA) {
		return rejectedQuick(ReasonQuickIntegrationMismatch)
	}
	if len(request.GeneratedPaths) != 0 || len(request.SharedPathsRequireApproval) != 0 || len(source.GeneratedPaths) != 0 || len(source.SharedPathsRequireApproval) != 0 {
		return rejectedQuick(ReasonQuickSharedOrGenerated)
	}
	if !sameStringSet(request.ForbiddenPaths, source.ForbiddenPaths) {
		return rejectedQuick(ReasonQuickPathsNotSubset)
	}
	allowed := quickAllowedPaths(source)
	changed := uniqueQuickPaths(append(append([]string{}, request.WritePaths...), request.TestPaths...))
	if len(changed) == 0 || len(changed) > ComplexQuickMaxFiles {
		return rejectedQuick(ReasonQuickFileLimitExceeded)
	}
	for _, item := range changed {
		if strings.ContainsAny(item, "*?[") {
			return rejectedQuick(ReasonQuickPathsNotSubset)
		}
		if quickPathIsSensitive(item) {
			return rejectedQuick(ReasonQuickSensitivePath)
		}
		if !quickPathAllowed(item, allowed) {
			return rejectedQuick(ReasonQuickPathsNotSubset)
		}
	}
	if !sameStringSet(request.RequirementIDs, source.RequirementIDs) || !sameStringSet(request.AcceptanceIDs, source.AcceptanceIDs) {
		return rejectedQuick(ReasonQuickUnknownIDs)
	}
	if !sameStringSet(request.RequiredCheckIDs, source.RequiredCheckIDs) || !sameStringSet(request.IntegrationCheckIDs, plan.IntegrationCheckIDs) {
		return rejectedQuick(ReasonQuickChecksNotFromPlan)
	}
	selection.ReasonCode = ReasonQuickFollowUpApproved
	return selection, nil
}

func rejectedQuick(reason ReasonCode) (ComplexQuickSelection, error) {
	return ComplexQuickSelection{Mode: WorkModeQuick, ReasonCode: reason}, fmt.Errorf("quick execution rejected: %s", reason)
}

func quickAllowedPaths(source ComplexPlanTask) []string {
	allowed := append([]string{}, source.WritePaths...)
	for _, id := range source.RequiredCheckIDs {
		spec, ok := FrozenComplexCheckByID(id)
		if !ok {
			continue
		}
		for _, item := range spec.MainPaths {
			if !strings.ContainsAny(item, "*?[") {
				allowed = append(allowed, item)
			}
		}
	}
	return uniqueQuickPaths(allowed)
}

func uniqueQuickPaths(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func quickPathAllowed(item string, allowed []string) bool {
	for _, candidate := range allowed {
		if item == candidate {
			return true
		}
	}
	return false
}

func quickPathIsSensitive(item string) bool {
	normalized, base, ext := quickNormalizePath(item)
	if normalized == "" {
		return true
	}
	return quickSensitiveDatabase(normalized, ext) ||
		quickSensitiveDependency(base) ||
		quickSensitiveConfig(normalized, base, ext) ||
		quickSensitivePermission(normalized, base) ||
		quickSensitiveAuth(normalized, ext) ||
		quickSensitiveNetwork(normalized, base, ext) ||
		quickSensitiveCI(normalized, base) ||
		quickSensitiveRepoControl(normalized, base)
}

func quickNormalizePath(item string) (normalized, base, ext string) {
	lower := strings.ToLower(strings.TrimSpace(item))
	if lower == "" {
		return "", "", ""
	}
	normalized = path.Clean(strings.ReplaceAll(lower, "\\", "/"))
	normalized = strings.TrimPrefix(normalized, "/")
	base = path.Base(normalized)
	ext = path.Ext(base)
	return normalized, base, ext
}

func quickHasPathPrefix(normalized, prefix string) bool {
	return normalized == prefix || strings.HasPrefix(normalized, prefix+"/")
}

func quickPathContainsToken(normalized string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func quickSensitiveDatabase(normalized, ext string) bool {
	if ext == ".sql" {
		return true
	}
	return quickPathContainsToken(normalized, "migration", "migrate", "alembic", "prisma", "flyway", "liquibase")
}

func quickSensitiveDependency(base string) bool {
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
		"bun.lock", "bun.lockb", "npm-shrinkwrap.json", "cargo.toml", "cargo.lock", "gemfile",
		"gemfile.lock", "pipfile", "pipfile.lock", "poetry.lock", "requirements.txt", "pyproject.toml",
		"composer.json", "composer.lock", "mix.exs", "mix.lock", "podfile", "podfile.lock",
		"package.swift", "deno.json", "deno.lock":
		return true
	}
	return false
}

func quickSensitiveConfig(normalized, base, ext string) bool {
	if strings.Contains(base, "config") || strings.HasPrefix(base, "appsettings") {
		return true
	}
	switch ext {
	case ".toml", ".ini", ".cfg", ".conf", ".yml", ".yaml", ".properties":
		return true
	}
	for _, seg := range strings.Split(normalized, "/") {
		if seg == "config" || seg == "configs" || seg == ".config" {
			return true
		}
	}
	return false
}

func quickSensitivePermission(normalized, base string) bool {
	if base == "codeowners" {
		return true
	}
	return quickPathContainsToken(normalized, "permission", "rbac", "acl", "sudoers", "casbin")
}

func quickSensitiveAuth(normalized, ext string) bool {
	switch ext {
	case ".pem", ".key", ".p12", ".pfx", ".crt", ".cer":
		return true
	}
	if quickHasPathPrefix(normalized, ".ssh") {
		return true
	}
	return quickPathContainsToken(normalized, "auth", "oauth", "credential", "secret", "jwt", "passwd", "keystore", "htpasswd")
}

func quickSensitiveNetwork(normalized, base, ext string) bool {
	if strings.HasPrefix(base, "dockerfile") || strings.HasPrefix(base, "docker-compose") || base == "caddyfile" {
		return true
	}
	if ext == ".conf" {
		return true
	}
	return quickPathContainsToken(normalized, "nginx", "haproxy", "traefik", "envoy", "ingress", "firewall", "compose")
}

func quickSensitiveCI(normalized, base string) bool {
	if base == "makefile" || base == "jenkinsfile" {
		return true
	}
	if quickHasPathPrefix(normalized, ".circleci") || quickHasPathPrefix(normalized, ".buildkite") {
		return true
	}
	for _, seg := range strings.Split(normalized, "/") {
		if seg == "ci" || seg == ".ci" || seg == "workflows" {
			return true
		}
	}
	return strings.Contains(base, "workflow") || strings.Contains(normalized, "cloudbuild")
}

func quickSensitiveRepoControl(normalized, base string) bool {
	if strings.HasPrefix(base, ".") {
		return true
	}
	if quickHasPathPrefix(normalized, ".git") || quickHasPathPrefix(normalized, ".github") || quickHasPathPrefix(normalized, ".gitlab") {
		return true
	}
	switch base {
	case "license", "copying", "notice", "codeowners", "contributing.md", "security.md", "code_of_conduct.md":
		return true
	}
	return false
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := map[string]int{}
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

// ComplexQuickRun is the durable S08 follow-up request. It never reuses the
// S06/S07 complex execution run row.
type ComplexQuickRun struct {
	ID                       string     `json:"id"`
	DevelopmentRequirementID string     `json:"developmentRequirementId"`
	RequirementVersionID     string     `json:"requirementVersionId"`
	RequirementSHA256        string     `json:"requirementSha256"`
	SourceExecutionRunID     string     `json:"sourceExecutionRunId"`
	PlanID                   string     `json:"planId"`
	PlanSHA256               string     `json:"planSha256"`
	SourceTaskKey            string     `json:"sourceTaskKey"`
	IntegrationBaseSHA       string     `json:"integrationBaseSha"`
	Mode                     WorkMode   `json:"mode"`
	ModeReason               string     `json:"modeReason"`
	ExpectedTaskSetVersion   int64      `json:"expectedTaskSetVersion"`
	TaskSetVersion           int64      `json:"taskSetVersion"`
	ExecutionPackageJSON     string     `json:"executionPackageJson"`
	ExecutionPackageSHA256   string     `json:"executionPackageSha256"`
	StewardRoleBindingID     string     `json:"stewardRoleBindingId"`
	BuilderRoleBindingID     string     `json:"builderRoleBindingId,omitempty"`
	DevelopmentTaskID        string     `json:"developmentTaskId,omitempty"`
	Decision                 string     `json:"decision,omitempty"`
	ReasonCode               ReasonCode `json:"reasonCode,omitempty"`
	CreatedAt                time.Time  `json:"createdAt"`
	CompletedAt              *time.Time `json:"completedAt,omitempty"`
}

// ComplexQuickSnapshot is the derived S08 read model.
type ComplexQuickSnapshot struct {
	Run             ComplexQuickRun                 `json:"run"`
	Phase           ComplexExecutionPhase           `json:"phase"`
	PhaseReason     ReasonCode                      `json:"phaseReason,omitempty"`
	MissingEvidence []string                        `json:"missingEvidence"`
	RoleBindings    []ComplexExecutionRoleBinding   `json:"roleBindings"`
	AgentSteps      []AgentStep                     `json:"agentSteps"`
	Task            *ComplexExecutionTask           `json:"task,omitempty"`
	Dispatches      []ComplexExecutionDispatch      `json:"dispatches"`
	CheckSpecs      []ComplexExecutionCheckSpecFact `json:"checkSpecs"`
	CheckRuns       []ComplexExecutionCheckRun      `json:"checkRuns"`
	Integration     *ComplexExecutionIntegration    `json:"integration,omitempty"`
}

// DeriveComplexQuickPhase computes the scheduler phase from durable QUICK facts.
func DeriveComplexQuickPhase(snapshot ComplexQuickSnapshot) (ComplexExecutionPhase, ReasonCode) {
	run := snapshot.Run
	switch {
	case run.CompletedAt != nil:
		return ComplexExecutionCompleted, ""
	case run.ReasonCode == ReasonQuickNeedsHuman || run.Decision == string(ComplexExecutionDecisionNeedsHuman):
		return ComplexExecutionNeedsHuman, ReasonQuickNeedsHuman
	case run.ReasonCode != "" && run.ReasonCode != ReasonQuickFollowUpApproved:
		return ComplexExecutionBlocked, run.ReasonCode
	case run.Decision != ComplexQuickDecisionSubmit:
		return ComplexExecutionAwaitingSteward, ""
	}
	builder := ComplexExecutionRoleBinding{}
	for _, binding := range snapshot.RoleBindings {
		if binding.Role == StandardRoleBuilder {
			builder = binding
		}
	}
	if builder.ID == "" || builder.Status == RoleBindingStatusRequested {
		return ComplexExecutionBindingBuilder, ""
	}
	if builder.Status == RoleBindingStatusFailed || builder.Status == RoleBindingStatusEnded {
		return ComplexExecutionBlocked, builder.ReasonCode
	}
	if snapshot.Task == nil || snapshot.Task.Status == DevelopmentTaskStatusPlanned || len(snapshot.Dispatches) == 0 {
		return ComplexExecutionReadyToDispatch, ""
	}
	switch snapshot.Task.Status {
	case DevelopmentTaskStatusNeedsHuman:
		return ComplexExecutionNeedsHuman, ReasonQuickNeedsHuman
	case DevelopmentTaskStatusBlocked:
		return ComplexExecutionBlocked, ReasonComplexExecutionStopped
	case DevelopmentTaskStatusRework:
		return ComplexExecutionReworking, ""
	case DevelopmentTaskStatusDone:
		if snapshot.Run.CompletedAt != nil {
			return ComplexExecutionCompleted, ""
		}
		return ComplexExecutionIntegrating, ""
	}
	if snapshot.Integration != nil {
		return ComplexExecutionIntegrating, ""
	}
	return ComplexExecutionBuilding, ""
}

// DeriveComplexQuickMissingEvidence lists the checks still required before QUICK can complete.
func DeriveComplexQuickMissingEvidence(snapshot ComplexQuickSnapshot) []string {
	phase, _ := DeriveComplexQuickPhase(snapshot)
	missing := []string{}
	switch phase {
	case ComplexExecutionCompleted:
		return missing
	case ComplexExecutionNeedsHuman:
		return []string{"STEWARD_DECISION"}
	case ComplexExecutionBindingBuilder:
		return []string{"BUILDER_BINDING"}
	case ComplexExecutionBuilding, ComplexExecutionReadyToDispatch:
		missing = append(missing, "SCOPE_CHECK", "REQUIRED_CHECKS", "INTEGRATION_CHECKS")
	case ComplexExecutionIntegrating:
		missing = append(missing, "INTEGRATION_CHECKS")
	}
	return missing
}
