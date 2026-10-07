package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

// Keep the first V2 instruction bytes available for already-bound Planner turns.
const initialPlannerRepositoryInvestigation = "Investigate the actual repository before producing a plan: inspect relevant source, tests, architecture, existing interfaces, and current boundaries. Do not modify files, install dependencies, request wider permissions, or submit executable commands. Evidence must come from this repository, not a guessed architecture."

const plannerRepositoryInvestigation = `Investigate the actual repository before producing a plan. Read-only tool calls are permitted and required: inspect the checked-out baseline, relevant source files, tests, existing public interfaces, and package scripts. Reading files, searching source and inspecting Git state are investigation, not implementation. The JSON-only rule below applies to your final response, not to these investigative tool calls.
Do not modify files, execute project scripts or tests, install dependencies, request wider permissions, or include executable commands in the final plan. Do not infer existing behavior from the product request or check catalog. In technicalApproach, cite the repository-relative files and test entry you actually inspected and explain how their observed boundaries justify the natural task split and interfaces. Distinguish existing behavior from behavior this Stage must add. Keep this evidence concise; do not give Builders a recipe. Use only the review criteria needed to establish completion: field limits are ceilings, not quotas.`

func plannerTaskContractPrompt(requestID string, version *core.RequirementVersion, compilation core.ComplexCompilation, document core.NormalizedRequirementDocument, originalPRD string, catalog []core.ComplexCheckSpec, feedback *core.ComplexPlanFeedback) string {
	requirementIDs := make([]string, 0, len(document.Requirements))
	for _, requirement := range document.Requirements {
		requirementIDs = append(requirementIDs, requirement.ID)
	}
	acceptanceIDs := make([]string, 0, len(document.AcceptanceScenarios))
	for _, acceptance := range document.AcceptanceScenarios {
		acceptanceIDs = append(acceptanceIDs, acceptance.ID)
	}
	return fmt.Sprintf(`You are the ClearDev Engineering Planner for one confirmed Stage. You own its overall engineering plan, task boundaries, dependencies, stable shared interfaces, and Task Reviewer completion criteria. Steward owns product goals, Functional Acceptance and Non-goals, not routine approval of your technical plan.

%s

Confirmed Stage / requirement (the product authority):
%s

Normalized functional acceptance and stable identifiers:
%s

Original product context (does not expand the confirmed Stage):
%s

Control-plane bindings, supplied for context only:
kind=COMPLEX_ENGINEERING_PLAN
planningRequestId=%s
requirementVersionId=%s
requirementVersionSha256=%s
compilationSha256=%s
stableRequirementIds=%s
stableAcceptanceIds=%s

Approved check catalog (use its check IDs, never invent commands or change argv):
%s

Historical planning feedback, if any; not permission to change product scope:
%s

Return exactly one JSON object, with no Markdown or prose. Do not include control-plane binding fields; the backend binds the current request, Stage and plan identities.

Executable outcome:
- schemaVersion: integer 2; kind: "COMPLEX_ENGINEERING_PLAN".
- Required top-level fields: schemaVersion, kind, technicalApproach, tasks, integrationCheckIds, parallelSuggestion, risks.
- Optional top-level field: interfaceContracts. Omit it when no cross-task interface is needed. No other fields are allowed.
- Create 1-3 natural tasks. Use one task for a cohesive small change. Do not split work to hit a task or Builder count. Cover every confirmed requirement MUST and every Functional Acceptance scenario in the tasks, including boundary and compatibility behavior.
- Each task contains exactly: key, title, objective, requirementIds, acceptanceIds, writePaths, generatedPaths, sharedPathsRequireApproval, forbiddenPaths, requiredCheckIds, dependencyKeys, reviewCriteria.
- Objective describes the engineering result. reviewCriteria is 1-12 unique nonempty strings stating how an independent Task Reviewer can determine completion: observable behavior, important edge cases, compatibility, preserved semantics, and tests that need proof. Criteria are NOT implementation steps, a file-edit recipe, pseudocode, or a Builder's internal plan.
- Builders independently investigate, plan their implementation, code, debug and test within this Task Contract. Do not make those internal choices for them.
- Every plural task field is a JSON string array. requirementIds, acceptanceIds, writePaths, forbiddenPaths and requiredCheckIds contain 1-20 items; generatedPaths, sharedPathsRequireApproval and dependencyKeys may be empty. Use only confirmed stable requirement/acceptance IDs and approved check IDs.
- Give tasks the narrowest valid repository-relative write boundaries. Paths must remain within src/, test/, .git/, backend/, frontend/, migrations/, package.json or package-lock.json. Do not request other roots, compiler configurations, .github/, scripts/, schema/auth/external-service permissions, dependency upgrades, or writes to locked template files. A forbidden path must also be within these approved roots.
- Task keys and interface keys match ^[a-z][a-z0-9-]{0,39}$ and are unique within their respective lists.
- Record every actual ordering dependency. Each dependencyKeys entry must exactly match another task key; no cycles, self-dependencies, invented references or hidden dependencies.
- An interfaceContracts entry contains exactly: key, providerTaskKey, consumerTaskKeys, expectations. There are at most 6 entries. Each names one providing task, 1-2 distinct consuming tasks, and 1-12 unique nonempty expectations describing the stable behavior or public data/function/HTTP interface the consumer needs. Preserve existing semantics explicitly when relevant. Do not design an RPC/IDL platform.
- Every interface consumer must directly include the provider in dependencyKeys; the provider cannot consume itself. Include an interface only for real task collaboration. A task receives only interfaces it provides or consumes. Interfaces and criteria will be frozen into plan and execution-package hashes and reviewed against exact candidates; they cannot be silently changed during execution.
- parallelSuggestion contains exactly recommendedBuilderCount (1 or 2) and reason. Recommend parallel work only when tasks can genuinely be ready together with disjoint narrow write/generated/shared paths. The control plane makes the final deterministic mode selection. Dependent provider/consumer tasks must not be presented as independent parallel tasks.
- integrationCheckIds contains 1-10 approved check IDs. risks is a string array with 0-20 items. technicalApproach, task title/objective, each criterion/expectation, parallelSuggestion.reason and each risk are nonempty and at most 10000 Unicode characters. All string arrays contain unique nonempty values.
- Task Reviewer PASS is necessary but never replaces the independent Stage/Requirement Final Reviewer, which checks the integrated result against the product's Functional Acceptance.

Product clarification outcome, ONLY when missing product intent prevents an engineering decision:
{"schemaVersion":2,"kind":"PRODUCT_CLARIFICATION_REQUIRED","summary":"The specific missing product decision and why it blocks this Stage.","questions":["A product question for Steward / Human Authority."]}
Use 1-8 unique, nonempty product questions and a bounded summary (at most 10000 characters each). Do not return tasks with this outcome. Technical choices such as files, task split, dependencies and implementation belong to you or Builder, not to a routine Steward approval. This outcome safely stops before execution; it does not authorize runtime replanning or change the confirmed product goals.
`, plannerRepositoryInvestigation, version.RequirementText, mustComplexPromptJSON(document), originalPRD, requestID, version.ID, version.SHA256, compilation.CompilationSHA256, strings.Join(requirementIDs, ", "), strings.Join(acceptanceIDs, ", "), mustComplexPromptJSON(catalog), mustComplexPromptJSON(feedback))
}

// An in-flight prompt remains byte-for-byte authoritative across upgrades.
// A changed flag must never reinterpret a legacy result as a Task Contract.
func chooseComplexPlannerPrompt(enabled bool, step core.AgentStep, legacy, contract string) (string, bool, error) {
	if step.ID != "" {
		// Project guidance only changes new messages; never rewrite an issued request.
		priorProject := strings.TrimPrefix(legacy, projectEnvironmentGuidance)
		if priorProject != legacy && step.PromptSHA256 == coreDigest([]byte(priorProject)) {
			return priorProject, false, nil
		}
		switch step.PromptSHA256 {
		case coreDigest([]byte(legacy)):
			return legacy, false, nil
		case coreDigest([]byte(contract)):
			return contract, true, nil
		default:
			// Reconstruct only the known initial V2 template. Exact persisted
			// hashes, not the feature flag, authorize replay of its original bytes.
			initialContract := strings.Replace(contract, plannerRepositoryInvestigation, initialPlannerRepositoryInvestigation, 1)
			if step.PromptSHA256 == coreDigest([]byte(initialContract)) {
				return initialContract, true, nil
			}
			return "", false, errors.New("saved Planner prompt no longer matches its immutable protocol")
		}
	}
	if enabled {
		return contract, true, nil
	}
	return legacy, false, nil
}

// A nil executable result with no error is a valid, non-executable product
// clarification. Its canonical bytes still bind the same planning request.
func parseComplexPlannerOutcome(raw []byte, contract bool, requestID, versionID, versionSHA, compilationSHA string, coverage core.ComplexCoverage, catalog []core.ComplexCheckSpec) (*core.ComplexEngineeringPlanResult, []byte, string, error) {
	var result core.ComplexEngineeringPlanResult
	var encoded []byte
	var digest string
	var err error
	if contract {
		var envelope struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(raw, &envelope) == nil && envelope.Kind == core.PlannerClarificationKind {
			_, encoded, digest, err = core.ParsePlannerProductClarification(raw, requestID, versionID, versionSHA, compilationSHA)
			return nil, encoded, digest, err
		}
		result, encoded, digest, err = core.ParsePlannerTaskContractResult(raw, requestID, versionID, versionSHA, compilationSHA, coverage, catalog)
	} else {
		result, encoded, digest, err = core.ParseComplexEngineeringPlanResultWithCatalog(raw, requestID, versionID, versionSHA, compilationSHA, coverage, catalog)
	}
	return &result, encoded, digest, err
}
