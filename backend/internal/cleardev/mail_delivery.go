package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

// MailDeliveryPolicyV1 is the shipped one-task/one-Builder contract.
// MailDeliveryPolicyV2 keeps the same repository safety envelope while admitting
// a natural 1-3 task DAG and at most two Builders for new executions only.
const (
	MailDeliveryPolicyV1 = "MAIL_INCREMENT_V1"
	MailDeliveryPolicyV2 = "MAIL_INCREMENT_V2"
)

// MailHealthProbeSHA256 pins the trusted probe, not a project-supplied script.
const MailHealthProbeSHA256 = "8a08048a7802f7783b862c0b21a8fb1152ac79a30cbb925cf39312a0969584f6"

// MailDeliveryInstructions is retained verbatim for historical V1 prompt
// reconstruction. New planning uses MailDeliveryInstructionsV2 explicitly.
const MailDeliveryInstructions = `This is a bounded increment on an existing healthy complex-mail-app. Use exactly one task and one Builder. Only backend/src/**, frontend/** and test/** may change. No dependencies, package/lock/config files, database schema/migrations, authentication, external services, CI or deployment. Existing test files must retain their paths, modes and complete original byte prefix; only append. Do not skip, replace, monkey-patch or otherwise bypass old assertions. New executable tests belong under test/ and use .js, .mjs or .ts; the trusted integration entry also executes them. For this mail policy, integrationCheckIds must be exactly ["demo-integration"]. The task requiredCheckIds must include demo-integration and may contain only demo-backend, demo-api, demo-frontend, and demo-integration. generatedPaths, sharedPathsRequireApproval, and dependencyKeys must be empty. writePaths must use normalized repository-relative file paths or supported subtree patterns such as backend/src/**, frontend/**, and test/**; bare directory names such as frontend or test, and trailing-slash directory names such as frontend/ or test/, are invalid. Inspect the actual npm test entry before choosing test paths. If npm test explicitly enumerates existing test files, either append the new regression directly to one of those listed files, or include both a new test file and an allowed append-only listed test file that imports it; never plan an orphan test file that full npm test cannot execute. The control plane additionally verifies exact candidate scope, append-only tests and real localhost health. Neither Builder nor Reviewer may lower these conditions.`

// MailDeliveryInstructionsV2 freezes the bounded task and final-candidate contract.
const MailDeliveryInstructionsV2 = `This is a bounded increment on an existing healthy complex-mail-app. Use 1-3 natural tasks; do not split work merely to demonstrate multiple Agents. recommendedBuilderCount must be 1 or 2. Dependencies must reference real task keys and form an acyclic DAG. List tasks in topological order: each dependency key must appear earlier in the tasks array. Only tasks with all dependencies satisfied and provably disjoint write paths may run concurrently; otherwise keep them in dependency order. One Builder may work on only one task at a time. Only backend/src/**, frontend/** and test/** may change. Do not add or install dependencies. No package/lock/config changes, database schema/migrations, authentication, external services, CI or deployment. Existing test files must retain their paths, modes and complete original byte prefix; only append. Do not skip, replace, monkey-patch or otherwise bypass old assertions. New executable tests belong under test/ and use .js, .mjs or .ts; the trusted final integration entry also executes them. integrationCheckIds must be exactly ["demo-integration"]. Every task requiredCheckIds must include demo-integration and may contain only demo-backend, demo-api, demo-frontend, and demo-integration. generatedPaths and sharedPathsRequireApproval must be empty. writePaths must use normalized repository-relative file paths or supported subtree patterns such as backend/src/**, frontend/**, and test/**; bare directory names such as frontend or test, and trailing-slash directory names such as frontend/ or test/, are invalid. Inspect the actual npm test entry before choosing test paths. If npm test explicitly enumerates existing test files, either append the new regression directly to one of those listed files, or include both a new test file and an allowed append-only listed test file that imports it; never plan an orphan test file that full npm test cannot execute. Each task is frozen and reviewed independently. After all tasks are verified, the control plane composes an explicit final candidate, then verifies full npm test, appended tests and real localhost health on that final SHA before the independent Requirement Final Reviewer. Neither Builder nor Reviewer may lower these conditions.`

// MailWritePathAllowed is the outer policy, intersected with the reviewed plan.
// It is never constructed from an Agent's writePaths or forbiddenPaths.
func MailWritePathAllowed(name string) bool {
	if name == "" || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") || strings.HasPrefix(name, "/") {
		return false
	}
	allowed := false
	for _, prefix := range []string{"backend/src/", "frontend/", "test/"} {
		allowed = allowed || strings.HasPrefix(name, prefix)
	}
	if !allowed {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") || slices.Contains([]string{"node_modules", "migrations", "schema", "schemas"}, component) {
			return false
		}
	}
	base := path.Base(name)
	if slices.Contains([]string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb"}, base) || strings.HasSuffix(base, ".sql") || name == "backend/src/db.ts" {
		return false
	}
	return true
}

// MailTestPath identifies old files whose complete byte prefix is protected.
func MailTestPath(name string) bool {
	return strings.HasPrefix(name, "test/") || strings.Contains(name, "/__tests__/") || strings.Contains(name, ".test.") || strings.Contains(name, ".spec.") || strings.HasSuffix(name, "_test.go")
}

// MailExtraTestPath identifies files executable through the fixed Node entry.
func MailExtraTestPath(name string) bool {
	return strings.HasPrefix(name, "test/") && (strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".mjs") || strings.HasSuffix(name, ".ts"))
}

func validateMailTaskContract(task ComplexPlanTask, allowDependencies bool) error {
	if len(task.GeneratedPaths) != 0 || len(task.SharedPathsRequireApproval) != 0 || len(task.WritePaths) == 0 || !slices.Contains(task.RequiredCheckIDs, "demo-integration") {
		return errors.New("MAIL_PLAN_OUT_OF_SCOPE: generated/shared work is unsupported and each task needs write paths plus demo-integration")
	}
	if !allowDependencies && len(task.DependencyKeys) != 0 {
		return errors.New("MAIL_PLAN_OUT_OF_SCOPE: dependencies are unsupported by MAIL_INCREMENT_V1")
	}
	for _, p := range task.WritePaths {
		probe := strings.TrimSuffix(p, "/**")
		if probe != p {
			probe += "/policy-probe.js"
		}
		if strings.ContainsAny(probe, "*?[]{}") || !MailWritePathAllowed(probe) {
			return errors.New("MAIL_PLAN_OUT_OF_SCOPE: write path exceeds the fixed policy")
		}
	}
	for _, id := range task.RequiredCheckIDs {
		if !slices.Contains([]string{"demo-backend", "demo-api", "demo-frontend", "demo-integration"}, id) {
			return errors.New("MAIL_PLAN_OUT_OF_SCOPE: unsupported required check")
		}
	}
	return nil
}

// ValidateMailPlan keeps the new production planning contract deliberately
// narrower than historical generic S07: 1-3 tasks and at most two Builders.
func ValidateMailPlan(plan ComplexEngineeringPlanResult) error {
	if len(plan.Tasks) < 1 || len(plan.Tasks) > 3 || plan.ParallelSuggestion.RecommendedBuilderCount < 1 || plan.ParallelSuggestion.RecommendedBuilderCount > 2 || !slices.Equal(plan.IntegrationCheckIDs, []string{"demo-integration"}) {
		return errors.New("MAIL_PLAN_OUT_OF_SCOPE: 1-3 tasks, one or two Builders and exactly demo-integration are required")
	}
	if len(plan.Tasks) == 1 && plan.ParallelSuggestion.RecommendedBuilderCount != 1 {
		return errors.New("MAIL_PLAN_OUT_OF_SCOPE: one task must use one Builder")
	}
	if err := validateComplexParallelPlanTasks(plan.Tasks); err != nil {
		return fmt.Errorf("MAIL_PLAN_OUT_OF_SCOPE: invalid task DAG: %w", err)
	}
	earlier := make(map[string]bool, len(plan.Tasks))
	for _, task := range plan.Tasks {
		// SQLite persists reviewed task ordinals. Reject an unsupported order
		// during plan validation rather than approving an unmaterializable DAG.
		for _, dependency := range task.DependencyKeys {
			if !earlier[dependency] {
				return fmt.Errorf("MAIL_PLAN_OUT_OF_SCOPE: dependency %s must appear before task %s", dependency, task.Key)
			}
		}
		if err := validateMailTaskContract(task, true); err != nil {
			return err
		}
		earlier[task.Key] = true
	}
	return nil
}

func validateMailPlanV1(plan ComplexEngineeringPlanResult) error {
	if len(plan.Tasks) != 1 || plan.ParallelSuggestion.RecommendedBuilderCount != 1 || !slices.Equal(plan.IntegrationCheckIDs, []string{"demo-integration"}) {
		return errors.New("MAIL_PLAN_OUT_OF_SCOPE: one task, one Builder and demo-integration are required")
	}
	return validateMailTaskContract(plan.Tasks[0], false)
}

// ValidateMailPlanForPolicy preserves the selected version's frozen task bounds.
func ValidateMailPlanForPolicy(policy string, plan ComplexEngineeringPlanResult) error {
	switch policy {
	case MailDeliveryPolicyV1:
		return validateMailPlanV1(plan)
	case MailDeliveryPolicyV2:
		return ValidateMailPlan(plan)
	default:
		return errors.New("unknown mail delivery policy")
	}
}

func bindMailDeliveryPolicy(raw []byte, baseSHA, policy string) ([]byte, string, error) {
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, "", err
	}
	if !mailSHA1(baseSHA) {
		return nil, "", errors.New("invalid mail delivery baseline")
	}
	switch policy {
	case MailDeliveryPolicyV1:
		if pkg.Mode != string(WorkModeStandard) {
			return nil, "", errors.New("MAIL_INCREMENT_V1 requires STANDARD mode")
		}
	case MailDeliveryPolicyV2:
		if pkg.Mode != string(WorkModeStandard) && pkg.Mode != string(WorkModeParallel) {
			return nil, "", errors.New("MAIL_INCREMENT_V2 requires STANDARD or PARALLEL mode")
		}
	default:
		return nil, "", errors.New("unknown mail delivery policy")
	}
	pkg.DeliveryPolicy, pkg.DeliveryBaseSHA = policy, baseSHA
	encoded, err := marshalCanonicalJSON(pkg)
	return encoded, sha256Hex(encoded), err
}

// BindMailDeliveryPolicy is retained for historical V1 fixtures.
func BindMailDeliveryPolicy(raw []byte, baseSHA string) ([]byte, string, error) {
	return bindMailDeliveryPolicy(raw, baseSHA, MailDeliveryPolicyV1)
}

// BindMailDeliveryPolicyV2 freezes the bounded multi-task contract for new runs.
func BindMailDeliveryPolicyV2(raw []byte, baseSHA string) ([]byte, string, error) {
	return bindMailDeliveryPolicy(raw, baseSHA, MailDeliveryPolicyV2)
}

// MailDeliveryPolicyFromRun returns the frozen policy version and baseline.
func MailDeliveryPolicyFromRun(run ComplexExecutionRun) (string, string, bool, error) {
	if run.ExecutionPackageJSON == "" && run.ExecutionPackageSHA256 == "" {
		return "", "", false, nil
	}
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg); err != nil || sha256Hex([]byte(run.ExecutionPackageJSON)) != run.ExecutionPackageSHA256 {
		return "", "", false, errors.New("invalid execution policy envelope")
	}
	if pkg.DeliveryPolicy == "" && pkg.DeliveryBaseSHA == "" {
		if pkg.AttemptPolicy != "" || pkg.FreezePolicy != "" {
			return "", "", false, errors.New("mail attempt or freeze policy requires a delivery policy")
		}
		return "", "", false, nil
	}
	if !mailSHA1(pkg.DeliveryBaseSHA) || pkg.SchemaVersion != ComplexExecutionProtocolVersion || pkg.Mode != string(run.Mode) || pkg.TaskSetVersion != run.TaskSetVersion || pkg.RequirementVersionID != run.RequirementVersionID || pkg.RequirementSHA256 != run.RequirementSHA256 || pkg.ExecutionRunID != run.ID || pkg.PlanID != run.PlanID || pkg.PlanSHA256 != run.PlanSHA256 {
		return "", "", false, errors.New("invalid fixed mail delivery policy")
	}
	switch pkg.DeliveryPolicy {
	case MailDeliveryPolicyV1:
		if run.Mode != WorkModeStandard || run.FixedBuilderCount != 1 {
			return "", "", false, errors.New("invalid MAIL_INCREMENT_V1 execution shape")
		}
	case MailDeliveryPolicyV2:
		if (run.Mode != WorkModeStandard || run.FixedBuilderCount != 1) && (run.Mode != WorkModeParallel || run.FixedBuilderCount != 2) {
			return "", "", false, errors.New("invalid MAIL_INCREMENT_V2 execution shape")
		}
	default:
		return "", "", false, errors.New("unknown mail delivery policy")
	}
	if pkg.AttemptPolicy != "" && pkg.AttemptPolicy != MailAttemptPolicyV1 {
		return "", "", false, errors.New("invalid mail attempt policy")
	}
	if pkg.FreezePolicy != "" && (pkg.FreezePolicy != MailFreezePolicyV1 || pkg.AttemptPolicy != MailAttemptPolicyV1) {
		return "", "", false, errors.New("invalid mail freeze policy")
	}
	return pkg.DeliveryPolicy, pkg.DeliveryBaseSHA, true, nil
}

// MailPolicyFromRun keeps the historical base/required API for existing callers.
func MailPolicyFromRun(run ComplexExecutionRun) (string, bool, error) {
	_, base, required, err := MailDeliveryPolicyFromRun(run)
	return base, required, err
}

// MailScopeProof records the trusted comparison with the original baseline.
type MailScopeProof struct {
	Policy           string   `json:"policy"`
	BaseSHA          string   `json:"baseSha"`
	CandidateSHA     string   `json:"candidateSha"`
	SourceManifestID string   `json:"sourceManifestId"`
	SourceTreeOID    string   `json:"sourceTreeOid"`
	OldTestCount     int      `json:"oldTestCount"`
	ExtraTestPaths   []string `json:"extraTestPaths"`
}

// MailCheckProof is filled from trusted runner results, not process stdout.
type MailCheckProof struct {
	RunID            string   `json:"runId"`
	CandidateSHA     string   `json:"candidateSha"`
	SourceManifestID string   `json:"sourceManifestId"`
	SourceTreeOID    string   `json:"sourceTreeOid"`
	ImageID          string   `json:"imageId"`
	EnvironmentID    string   `json:"environmentId"`
	OutputSHA256     string   `json:"outputSha256"`
	Argv             []string `json:"argv"`
	Passed           bool     `json:"passed"`
	ExitCode         int      `json:"exitCode"`
	TimedOut         bool     `json:"timedOut"`
	Truncated        bool     `json:"truncated"`
}

// MailDeliveryProof binds scope, executed tests and health to one candidate.
type MailDeliveryProof struct {
	Scope             MailScopeProof  `json:"scope"`
	Tests             MailCheckProof  `json:"tests"`
	ExtraTests        *MailCheckProof `json:"extraTests,omitempty"`
	Health            MailCheckProof  `json:"health"`
	HealthProbeSHA256 string          `json:"healthProbeSha256"`
}

// MailExtraTestArgv executes new and appended test files through a fixed entry.
func MailExtraTestArgv(paths []string) []string {
	return append([]string{"node", "--experimental-sqlite", "--experimental-strip-types", "--test", "--test-concurrency=1"}, paths...)
}

// ValidateMailScopeProofForPolicy rejects missing bindings or ambiguous test entries.
func ValidateMailScopeProofForPolicy(proof MailScopeProof, policy, base, candidate string) error {
	if (policy != MailDeliveryPolicyV1 && policy != MailDeliveryPolicyV2) || proof.Policy != policy || proof.BaseSHA != base || proof.CandidateSHA != candidate || !mailSHA1(base) || !mailSHA1(candidate) || !mailSHA1(proof.SourceTreeOID) || !validProtocolSHA256(proof.SourceManifestID) || proof.OldTestCount < 1 || proof.ExtraTestPaths == nil {
		return errors.New("MAIL_SCOPE_EVIDENCE_INVALID")
	}
	for i, name := range proof.ExtraTestPaths {
		if !MailWritePathAllowed(name) || !MailExtraTestPath(name) || i > 0 && name <= proof.ExtraTestPaths[i-1] {
			return errors.New("MAIL_EXTRA_TEST_ENTRY_INVALID")
		}
	}
	return nil
}

// ValidateMailScopeProof preserves the V1 proof API for historical callers.
func ValidateMailScopeProof(proof MailScopeProof, base, candidate string) error {
	return ValidateMailScopeProofForPolicy(proof, MailDeliveryPolicyV1, base, candidate)
}

// ValidateMailDeliveryProofForPolicy verifies the complete candidate-bound receipt.
func ValidateMailDeliveryProofForPolicy(raw, policy, base, candidate, checkRunID, imageID string) error {
	var proof MailDeliveryProof
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return errors.New("MAIL_DELIVERY_EVIDENCE_MISSING")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("MAIL_DELIVERY_EVIDENCE_TRAILING_DATA")
	}
	if err := ValidateMailScopeProofForPolicy(proof.Scope, policy, base, candidate); err != nil {
		return err
	}
	if imageID == "" || proof.HealthProbeSHA256 != MailHealthProbeSHA256 || !slices.Equal(proof.Tests.Argv, []string{"npm", "test"}) || !slices.Equal(proof.Health.Argv, []string{"trusted-mail-health-v1"}) {
		return errors.New("MAIL_DELIVERY_CHECK_CONTRACT_CHANGED")
	}
	checks := []struct {
		proof MailCheckProof
		id    string
	}{{proof.Tests, checkRunID + ":npm-test"}, {proof.Health, checkRunID + ":health"}}
	if len(proof.Scope.ExtraTestPaths) > 0 {
		if proof.ExtraTests == nil || !slices.Equal(proof.ExtraTests.Argv, MailExtraTestArgv(proof.Scope.ExtraTestPaths)) {
			return errors.New("MAIL_EXTRA_TESTS_NOT_EXECUTED")
		}
		checks = append(checks, struct {
			proof MailCheckProof
			id    string
		}{*proof.ExtraTests, checkRunID + ":extra-tests"})
	} else if proof.ExtraTests != nil {
		return errors.New("MAIL_EXTRA_TEST_EVIDENCE_UNEXPECTED")
	}
	for _, check := range checks {
		p := check.proof
		if p.RunID != check.id || !p.Passed || p.ExitCode != 0 || p.TimedOut || p.Truncated || p.CandidateSHA != candidate || p.SourceManifestID != proof.Scope.SourceManifestID || p.SourceTreeOID != proof.Scope.SourceTreeOID || p.ImageID != imageID || !validProtocolSHA256(p.EnvironmentID) || !validProtocolSHA256(p.OutputSHA256) {
			return errors.New("MAIL_DELIVERY_EVIDENCE_STALE_OR_FAILED")
		}
	}
	return nil
}

// ValidateMailDeliveryProof preserves the V1 proof API for historical callers.
func ValidateMailDeliveryProof(raw, base, candidate, checkRunID, imageID string) error {
	return ValidateMailDeliveryProofForPolicy(raw, MailDeliveryPolicyV1, base, candidate, checkRunID, imageID)
}

func mailSHA1(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
