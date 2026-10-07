package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (s *Service) projectPlanningState(ctx context.Context, requirementID string) (*core.ProjectPlanningState, error) {
	stage, linked, err := s.productStageSource(ctx, requirementID)
	if err != nil || !linked || stage.Definition.ExecutionBasis == nil {
		return nil, err
	}
	store, err := s.productStore()
	if err != nil {
		return nil, err
	}
	product, found, err := store.GetClearDevProduct(ctx, stage.ProductID)
	if err != nil {
		return nil, err
	}
	state := &core.ProjectPlanningState{
		ProductID: stage.ProductID, StageID: stage.ID, DiscussionID: stage.DiscussionID, BaseCommitSHA: stage.BaseCommitSHA,
		ExecutionBasis: stage.Definition.ExecutionBasis,
	}
	if found && len(product.Discussions) > 0 {
		state.LatestDiscussionID = product.Discussions[len(product.Discussions)-1].ID
		state.Current = state.DiscussionID == state.LatestDiscussionID
	}
	if !state.Current {
		state.ReasonCode = core.ReasonProductPlanSuperseded
		return state, nil
	}
	parent, exists, err := s.facts.GetClearDevRequirement(ctx, stage.ProductID)
	if err != nil {
		return nil, err
	}
	if !exists || parent.Requirement.CancelledAt != nil {
		state.ReasonCode = "PRODUCT_CANCELLED"
		return state, nil
	}
	if err := s.requirePreviousProductStage(ctx, product, stage); err != nil {
		var conflict *apierr.Error
		if !errors.As(err, &conflict) {
			return nil, err
		}
		state.ReasonCode = core.ReasonCode(conflict.Code)
		return state, nil
	}
	if plans, ok := s.complex.(productPlanStore); ok {
		authorization, e := plans.GetClearDevProductPlanAuthorization(ctx, stage.ProductID, stage.DiscussionID)
		if e != nil {
			return nil, e
		}
		if authorization != nil && authorization.Decision == "APPROVE" && authorization.Status != "RESOLVED" {
			state.ReasonCode = "PRODUCT_PLAN_AUTHORIZATION_INACTIVE"
			return state, nil
		}
	}
	state.SourceCurrent = s.selectedProjectCurrent(ctx, stage.Selection)
	if !state.SourceCurrent {
		state.ReasonCode = "PRODUCT_BASELINE_CHANGED"
	}
	_, capable := s.checks.(ports.ClearDevProjectExecutionPreparer)
	if capable && s.finalReviews != nil && s.complexExecution != nil && s.inspector != nil && state.ReasonCode == "" {
		runtime, runtimeErr := core.ResolveProjectRuntime(*stage.Definition.ExecutionBasis)
		state.ExecutionAvailable = runtimeErr == nil
		if runtimeErr == nil {
			state.ExecutionRuntime = &runtime
		}
		// This only advertises that an explicit request can be evaluated. The
		// actual pinned image/cache/source preflight still happens at admission.
		if runtimeErr != nil {
			state.ReasonCode = core.ReasonProjectRuntime
		}
	}
	return state, nil
}

// This gate applies to both manual and automatic entry points. The database
// repeats the restriction at run insertion; a model's plan is never a grant.
func (s *Service) projectExecutionGate(ctx context.Context, requirementID string) error {
	stage, linked, err := s.productStageSource(ctx, requirementID)
	if err != nil {
		return err
	}
	if linked && stage.Definition.ExecutionBasis != nil {
		return apierr.Conflict(string(core.ReasonProjectPlanningOnly), "The saved project plan requires an explicit, current project execution admission", nil)
	}
	return nil
}

func projectEngineeringPrompt(requestID string, version *core.RequirementVersion, compilation core.ComplexCompilation, document core.NormalizedRequirementDocument, originalPRD string, stage core.ProductStage, feedback *core.ComplexPlanFeedback, harness ...domain.AgentHarness) string {
	toolName := "Codex" // Preserve the exact historical prompt when unspecified.
	if len(harness) > 0 && harness[0] == domain.HarnessOpenCode {
		toolName = "OpenCode"
	}
	return projectEnvironmentGuidance + fmt.Sprintf(`You are the ClearDev Engineering Planner for one confirmed project Stage. Reuse the normal engineering task and interface contract, but produce a PLANNING-ONLY version 3 result. A saved plan does not authorize execution: the backend requires a separate explicit, exact-source project admission. Do not dispatch, implement, install dependencies, run project scripts/tests, start a server, approve anything or claim delivery.

Investigate using the %s read/search/Git tools actually available in this session. Inspect the selected baseline, relevant source and tests, package declarations and public interfaces. Local file reads/searches are limited to this session workspace and the immutable selected repository path supplied below; never read HOME, CODEX_HOME, ~/.codex, memories, credential directories, adjacent repositories, or any other local path. If the selected source cannot be read or a tool result is uncertain, report that limitation instead of falling back to personal memory or unrelated local data. The final JSON-only rule does not prohibit investigative tool calls. Cite actual repository-relative files and observations in technicalApproach. An empty repository is valid: distinguish proposed new files/checks from existing facts. Do not invent tool results. Web investigation is optional and only when needed and available.

Immutable user choice and exact repository version:
%s

Confirmed Stage (product authority, including its proposed project execution basis):
%s

Normalized functional acceptance and stable IDs:
%s

Project-specific proposed execution basis (not the mail template; not permission to run commands):
%s

Original product context (does not expand this Stage):
%s

Control bindings supplied only as context: planningRequestId=%s, requirementVersionId=%s, requirementVersionSha256=%s, compilationSha256=%s, stageDefinitionSha256=%s.
Historical feedback (not an authority grant):
%s

Return exactly one JSON object, no Markdown, no control binding fields or unknown fields.
Planning outcome: schemaVersion=3, kind="COMPLEX_ENGINEERING_PLAN". Required fields are schemaVersion, kind, technicalApproach, tasks, integrationCheckIds, parallelSuggestion, risks. Optional field: interfaceContracts.
Create 1–3 natural tasks, not a quota. Each task contains exactly key, title, objective, requirementIds, acceptanceIds, writePaths, generatedPaths, sharedPathsRequireApproval, forbiddenPaths, requiredCheckIds, dependencyKeys, reviewCriteria.
Use only the confirmed requirement/acceptance IDs. Cover every MUST and every functional acceptance scenario. Each task needs 1–12 unique observable reviewCriteria, not implementation recipes. Explain the engineering result and compatibility/edge cases. User product acceptance must not be replaced by technical checks.
All plural task fields are string arrays. requirementIds, acceptanceIds, writePaths, forbiddenPaths and requiredCheckIds need 1–20 items. generatedPaths and dependencyKeys may be empty (at most 20). Set sharedPathsRequireApproval to []: project execution does not support its separate approval workflow. Put confirmed shared configuration paths in writePaths instead. Paths must be safe repository-relative boundaries. The only wildcard form allowed is a terminal /**; do not use *.json, bare *, ?, or [] patterns elsewhere. No absolute paths, traversal, credentials or .git writes. Include .git/** as forbidden. Narrow all write/generated boundaries within this Stage's executionBasis.writePaths. Project configuration, dependencies and database changes can be planned only inside that confirmed basis; do not silently expand it or fall back to mail-only roots.
Use check IDs only from executionBasis.checks for requiredCheckIds and integrationCheckIds (1–10 integration checks). Every writable/generated boundary must be covered by a chosen check's mainPaths. Checks and launch commands are proposed future operations, not executed evidence. Do not put new executable commands in this plan. Dependency needs and launch approach remain frozen in the confirmed basis.
Each task's required checks run on that task's frozen candidate before its reviewer and before later tasks. For an empty repository, the first task must create every manifest, script and test entry that its required checks need; a later dependent task cannot supply them retroactively. If the confirmed scope or check catalogue makes that impossible, return PRODUCT_CLARIFICATION_REQUIRED instead of a plan that will fail at the first check. A no-dependency Node/npm project may omit package-lock.json; any declared package or workspace requires a candidate lockfile.
Keys match ^[a-z][a-z0-9-]{0,39}$, task keys unique. Record real dependencies using exact task keys; no self-dependencies, dangling references or cycles. parallelSuggestion has only recommendedBuilderCount (1 or 2) and reason; one task requires one Builder. This is a future suggestion, not an execution start.
interfaceContracts, when needed, has at most 6 entries, each with key, providerTaskKey, consumerTaskKeys (1–2), expectations (1–12). Consumers must directly depend on the provider, and cannot be the provider. State stable shared behavior and preserved semantics, not an RPC framework. TechnicalApproach, titles, objectives, criteria, expectations, risks and reasons are nonempty and at most 10000 characters; risks has 0–20 unique strings.
When product intent genuinely blocks engineering, return the existing non-executable clarification protocol instead:
{"schemaVersion":2,"kind":"PRODUCT_CLARIFICATION_REQUIRED","summary":"The missing product decision and why it matters","questions":["The product question"]}
Use 1–8 unique questions, each at most 10000 characters. Do not ask users routine implementation questions or claim an execution adapter is missing when the confirmed Node/npm basis is supported. Save the engineering plan; explicit execution admission and environment preparation occur separately after the confirmed specification.
`, toolName, mustComplexPromptJSON(stage.Selection), version.RequirementText, mustComplexPromptJSON(document), mustComplexPromptJSON(stage.Definition.ExecutionBasis), originalPRD,
		requestID, version.ID, version.SHA256, compilation.CompilationSHA256, stage.DefinitionSHA256, mustComplexPromptJSON(feedback))
}

func parseProjectPlannerOutcome(raw []byte, requestID, versionID, versionSHA, compilationSHA string, coverage core.ComplexCoverage, basis core.ProjectExecutionBasis) (*core.ComplexEngineeringPlanResult, []byte, string, error) {
	var envelope struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Kind == core.PlannerClarificationKind {
		_, data, digest, err := core.ParsePlannerProductClarification(raw, requestID, versionID, versionSHA, compilationSHA)
		return nil, data, digest, err
	}
	plan, data, digest, err := core.ParseProjectEngineeringPlanResult(raw, requestID, versionID, versionSHA, compilationSHA, coverage, basis)
	if err == nil {
		err = core.ValidateProposedProjectTaskEnvironments(plan, basis)
	}
	return &plan, data, digest, err
}
