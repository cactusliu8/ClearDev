package cleardev

import (
	"encoding/json"
	"fmt"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func complexCompilationPrompt(snapshot core.ComplexPlanningSnapshot, requestID, contextSHA string, round int) string {
	example := complexCompilationExample(snapshot, requestID, contextSHA, round)
	previous := "none"
	if len(snapshot.CompilationRequests) > 0 {
		encoded, _ := json.Marshal(core.CompilationContextFromSnapshot(snapshot, snapshot.Requirement.TargetRequirementVersionID))
		previous = string(encoded)
	}
	return fmt.Sprintf(`You are the ClearDev Project Steward. Compile the original product-requirements document into one strict JSON result. Do not modify files.

Original PRD:
%s

Saved previous rounds:
%s

Current bindings (do not copy into the JSON):
compilationRequestId=%s
requirementVersionId=%s
compilationContextSha256=%s
clarificationRound=%d

Rules:
- Return exactly one JSON object, with no Markdown or surrounding prose.
- Do not include compilationRequestId, requirementVersionId, compilationContextSha256, or clarificationRound in the JSON. The control plane binds those from the current request.
- Round 0 must use outcome CLARIFICATION_REQUIRED and ask 1-8 blocking questions.
- After answers, round 1 should usually be READY. Only use CLARIFICATION_REQUIRED again with a non-empty additionalRoundReason.
- Do not ask a third round. Unknown fields, null, and duplicate keys are rejected.
- Keep every MUST from the original PRD. If it requires SQLite storage, a local HTTP API, and a browser page, those remain MUST requirements and must not be compiled away.
- Do not search the repository for compilation examples, sample JSON, or previous compilation results. The JSON shape is given below.
- If the original PRD names contract files, you may read those files. Do not explore unrelated files looking for examples.
- After you finish reading any contract files named by the original PRD, the next assistant message must be the complete JSON object. Do not write any more commentary.
- Every requirement key, acceptance scenario key, and blocking question key must match ^[a-z][a-z0-9-]{0,39}$ exactly, must be at most 40 characters, and must be unique within its list. Do not use underscores.

Return exactly this JSON object, filling only the allowed fields:
%s`, snapshot.Requirement.OriginalPRDText, previous, requestID, snapshot.Requirement.TargetRequirementVersionID, contextSHA, round, example)
}

func complexCompilationExample(snapshot core.ComplexPlanningSnapshot, requestID, contextSHA string, round int) string {
	result := core.RequirementCompilationResult{
		SchemaVersion:            core.ComplexProtocolVersion,
		Kind:                     "REQUIREMENT_COMPILATION",
		CompilationRequestID:     requestID,
		RequirementVersionID:     snapshot.Requirement.TargetRequirementVersionID,
		CompilationContextSHA256: contextSHA,
		ClarificationRound:       round,
		Summary:                  "需求包含邮件规范化、按规范化邮箱去重，以及接受和拒绝数量统计。",
		Requirements: []core.CompilationRequirement{
			{Key: "normalize-email", Priority: "MUST", Text: "把邮箱地址两端空白去除并转换为小写。", AcceptanceKeys: []string{"normalize-scenario"}},
			{Key: "deduplicate-email", Priority: "MUST", Text: "按规范化后的邮箱去重，保留第一次出现的记录。", AcceptanceKeys: []string{"deduplicate-scenario"}},
			{Key: "count-summary", Priority: "MUST", Text: "输出接受数量和拒绝数量。", AcceptanceKeys: []string{"summary-scenario"}},
		},
		AcceptanceScenarios: []core.CompilationAcceptance{
			{Key: "normalize-scenario", Text: "输入带空白和大写字母的邮箱时，输出规范化地址。"},
			{Key: "deduplicate-scenario", Text: "两个只是大小写或空白不同的地址只保留一条。"},
			{Key: "summary-scenario", Text: "处理完成后输出接受数量和拒绝数量。"},
		},
		Constraints:       []string{"不访问外部网络。"},
		NonGoals:          []string{"不发送邮件。"},
		Terms:             []core.CompilationTerm{{Term: "接受数量", Definition: "通过全部输入规则并进入去重集合的记录数。"}},
		Assumptions:       []string{"输入文件使用 UTF-8 编码。"},
		Conflicts:         []core.CompilationConflict{},
		BlockingQuestions: []core.CompilationBlockingQuestion{},
	}
	if requirementNeedsFullStackDemo(snapshot.Requirement.OriginalPRDText) {
		result.Summary = "需求把规范化后的邮箱保存在 SQLite，提供本机 HTTP 接口和浏览器页面，并统计接受、拒绝和重复数量。"
		result.Requirements = []core.CompilationRequirement{
			{Key: "store-sqlite", Priority: "MUST", Text: "把邮箱记录保存在本地 SQLite 数据库中。", AcceptanceKeys: []string{"sqlite-scenario"}},
			{Key: "normalize-email", Priority: "MUST", Text: "导入时去掉邮箱两端空白并转换为小写。", AcceptanceKeys: []string{"normalize-scenario"}},
			{Key: "reject-invalid", Priority: "MUST", Text: "校验邮箱格式；格式无效的地址计入拒绝数量。", AcceptanceKeys: []string{"reject-scenario"}},
			{Key: "deduplicate-email", Priority: "MUST", Text: "按规范化后的邮箱去重，重复地址忽略不计，只保留第一次出现的记录。", AcceptanceKeys: []string{"deduplicate-scenario"}},
			{Key: "local-api", Priority: "MUST", Text: "提供只绑定 127.0.0.1 的本地 HTTP 接口，用于导入名单并查询接受、拒绝和重复数量。", AcceptanceKeys: []string{"api-scenario"}},
			{Key: "browser-page", Priority: "MUST", Text: "提供浏览器页面：导入数据后显示接受数量、拒绝数量和重复数量。", AcceptanceKeys: []string{"page-scenario"}},
		}
		result.AcceptanceScenarios = []core.CompilationAcceptance{
			{Key: "sqlite-scenario", Text: "导入后 SQLite contacts 表保存规范化邮箱。"},
			{Key: "normalize-scenario", Text: "输入带空白和大写字母的邮箱时，保存小写无空白地址。"},
			{Key: "reject-scenario", Text: "格式无效的地址计入拒绝数量。"},
			{Key: "deduplicate-scenario", Text: "重复地址忽略不计，只保留第一次出现的记录。"},
			{Key: "api-scenario", Text: "本机 HTTP 导入接口返回接受、拒绝和重复数量。"},
			{Key: "page-scenario", Text: "浏览器页面显示的接受、拒绝和重复数量与接口、SQLite 一致。"},
		}
		result.Terms = []core.CompilationTerm{
			{Term: "接受数量", Definition: "通过校验并首次写入 SQLite 的规范化邮箱数。"},
			{Term: "拒绝数量", Definition: "格式无效的地址数。"},
			{Term: "重复数量", Definition: "规范化后与已接受记录相同、被忽略的地址数。"},
		}
	}
	if round == 0 {
		result.Outcome = "CLARIFICATION_REQUIRED"
		rejectKeys := []string{"count-summary"}
		duplicateKeys := []string{"deduplicate-email", "count-summary"}
		if requirementNeedsFullStackDemo(snapshot.Requirement.OriginalPRDText) {
			rejectKeys = []string{"reject-invalid"}
			duplicateKeys = []string{"deduplicate-email", "reject-invalid"}
		}
		result.BlockingQuestions = []core.CompilationBlockingQuestion{
			{Key: "invalid-address", Text: "格式无效的地址是否计入拒绝数量？", Reason: "这会改变统计验收结果。", RequirementKeys: rejectKeys},
			{Key: "duplicate-address", Text: "重复地址是计入拒绝数量还是忽略不计？", Reason: "这会改变去重后的统计验收结果。", RequirementKeys: duplicateKeys},
		}
	} else {
		result.Outcome = "READY"
	}
	return mustAgentChosenPromptJSON(result)
}

func complexEngineeringPlanPrompt(requestID string, version *core.RequirementVersion, compilation core.ComplexCompilation, doc core.NormalizedRequirementDocument, originalPRD string) string {
	return complexEngineeringPlanPromptWithCatalog(requestID, version, compilation, doc, originalPRD, core.FrozenComplexCheckCatalog(), nil)
}

func complexEngineeringPlanPromptWithCatalog(requestID string, version *core.RequirementVersion, compilation core.ComplexCompilation, doc core.NormalizedRequirementDocument, originalPRD string, catalog []core.ComplexCheckSpec, feedback *core.ComplexPlanFeedback) string {
	checkIDs := make([]string, 0, len(catalog))
	for _, spec := range catalog {
		checkIDs = append(checkIDs, spec.ID)
	}
	requirementIDs := make([]string, 0, len(doc.Requirements))
	for _, requirement := range doc.Requirements {
		requirementIDs = append(requirementIDs, requirement.ID)
	}
	acceptanceIDs := make([]string, 0, len(doc.AcceptanceScenarios))
	for _, acceptance := range doc.AcceptanceScenarios {
		acceptanceIDs = append(acceptanceIDs, acceptance.ID)
	}
	feedbackSection := ""
	if feedback != nil {
		feedbackSection = fmt.Sprintf(`

Production Steward review of your previous plan (you must revise the plan to answer it):
planId=%s
planSha256=%s
reviewId=%s
reviewRequestId=%s
verdict=%s
reasonCode=%s
summary=%s
findings=%s
previousPlan=%s
`, feedback.PlanID, feedback.PlanSHA256, feedback.ReviewID, feedback.ReviewRequestID,
			feedback.Verdict, feedback.ReasonCode, feedback.Summary, feedback.FindingsJSON, feedback.PlanJSON)
	}
	return fmt.Sprintf(`You are the ClearDev Engineering Planner. Produce a 1-6 task DAG for the confirmed requirement. Do not modify files. Do not submit shell commands or argv arrays.

Confirmed requirement:
%s

Original PRD:
%s

Stable identifiers and compilation sha256:
kind=COMPLEX_ENGINEERING_PLAN
planningRequestId=%s
requirementVersionId=%s
requirementVersionSha256=%s
compilationSha256=%s
stableRequirementIds=%s
stableAcceptanceIds=%s

Frozen check ids (submit these ids only): %s
%s
Rules:
- Return exactly one JSON object, with no Markdown or surrounding prose.
- Do not include planningRequestId, requirementVersionId, requirementVersionSha256, or compilationSha256 in the JSON. The control plane binds those from the current request.
- Before planning, inspect the repository's current modules, relevant files, tests, and existing boundaries.
- Create 1-6 natural tasks from the confirmed requirement, repository structure, dependencies, risks, path conflicts, and coordination cost. Do not split work only to reach a task or builder count. A single task is correct when the work naturally fits one boundary; it must still cover every MUST requirement and acceptance scenario with valid checks.
- If a production Steward review of your previous plan is shown above, address every finding in the revised plan. The review is feedback about your own previous plan, not a new requirement: do not change the confirmed requirement, add requirements, or treat a finding as permission to write outside the frozen paths.
- Every MUST requirement and acceptance scenario must map to a task. Each mapped MUST must have at least one frozen check id that covers the task's paths.
- Give each task only the narrowest repository-relative paths needed for its objective. Do not request an entire code layer when a narrower module or file boundary is sufficient, and do not use broad paths to manufacture apparent non-conflicts.
- Paths must stay inside src/, test/, .git/, backend/, frontend/, migrations/, package.json or package-lock.json.
- Every path in writePaths, generatedPaths, sharedPathsRequireApproval, and forbiddenPaths must stay inside those frozen roots. Do not name compiler configuration files or any other file outside those roots, even as a forbidden path.
- Do not write .github/, scripts/, or other locked template files.
- Record every real ordering dependency and keep the graph acyclic. Do not omit dependencies merely to make tasks appear parallel.
- Suggest 1-3 builders only from tasks that can actually be ready together, have provably disjoint narrow paths, and justify their risk and coordination cost. Separating frontend and backend layers alone is not evidence of safe parallel work. A one-task plan must recommend exactly 1 builder.
- The Control Plane makes the final mode decision. Do not provide or optimize for a target mode.
- The result must contain exactly these top-level fields and no others:
  schemaVersion (integer 1), kind (string COMPLEX_ENGINEERING_PLAN), technicalApproach,
  tasks, integrationCheckIds, parallelSuggestion, risks.
- tasks must be an array of 1-6 objects. Every task object must contain exactly:
  key, title, objective, requirementIds, acceptanceIds, writePaths, generatedPaths,
  sharedPathsRequireApproval, forbiddenPaths, requiredCheckIds, dependencyKeys.
  Use JSON string arrays for every plural task field. Use an empty array when an allowed optional list has no values.
- Each task key must match ^[a-z][a-z0-9-]{0,39}$ exactly, must be at most 40 characters, and must be unique within the result.
- Every JSON string-array value must be non-empty, and values must be unique within that array.
- For each task, requirementIds, acceptanceIds, writePaths, forbiddenPaths, and requiredCheckIds must each contain 1-20 items.
- For each task, generatedPaths, sharedPathsRequireApproval, and dependencyKeys must each contain 0-20 items.
- Every dependencyKeys entry must exactly equal a task key in the same result. Do not normalize, rewrite, or substitute characters in task keys or dependencyKeys.
- integrationCheckIds must contain 1-10 frozen check ids. risks must contain 0-20 items.
- technicalApproach, every task title and objective, parallelSuggestion.reason, and every risks item must each be non-empty and at most 10000 Unicode characters.
- parallelSuggestion must contain exactly two fields: recommendedBuilderCount (an integer chosen from 1, 2, or 3 using the evidence above) and reason (a non-empty evidence-based string).

Return the result now.`, version.RequirementText, originalPRD, requestID, version.ID, version.SHA256, compilation.CompilationSHA256,
		strings.Join(requirementIDs, ", "), strings.Join(acceptanceIDs, ", "), strings.Join(checkIDs, ", "), feedbackSection)
}

func requirementNeedsFullStackDemo(texts ...string) bool {
	joined := strings.Join(texts, "\n")
	lower := strings.ToLower(joined)
	return strings.Contains(lower, "sqlite") &&
		(strings.Contains(joined, "浏览器") || strings.Contains(lower, "browser")) &&
		strings.Contains(lower, "http")
}

func complexPlanReviewPrompt(requestID string, plan core.ComplexEngineeringPlan, doc core.NormalizedRequirementDocument) string {
	return complexPlanReviewPromptWithCatalog(requestID, plan, doc, core.FrozenComplexCheckCatalog())
}

func complexPlanReviewPromptWithCatalog(requestID string, plan core.ComplexEngineeringPlan, doc core.NormalizedRequirementDocument, catalog []core.ComplexCheckSpec) string {
	checkIDs := make([]string, 0, len(catalog))
	for _, spec := range catalog {
		checkIDs = append(checkIDs, spec.ID)
	}
	return fmt.Sprintf(`You are the ClearDev Project Steward. Review the exact immutable engineering plan. Do not rewrite tasks.

Plan:
%s

Confirmed normalized requirement:
%s

Frozen check ids: %s

Review bindings:
kind=COMPLEX_PLAN_REVIEW
reviewRequestId=%s
planId=%s
planSha256=%s

Review rules:
- Check every MUST requirement and acceptance scenario for task coverage, and confirm every mapped MUST has a frozen check that covers the task paths.
- Check that requested paths are the narrowest natural repository boundaries. Return REPLAN when a task requests an entire code layer without necessity, or when broad paths manufacture apparent non-conflicts.
- Check that every real ordering dependency is present and that the graph is acyclic.
- Check that a multi-builder suggestion has evidence from simultaneously ready tasks, provably disjoint narrow paths, risk, coordination cost, and enough work to justify handoffs. Frontend/backend separation alone is not sufficient evidence.
- Return REPLAN when the plan is split only to reach a task or builder count, or when the same work would require avoidable coordination across the proposed boundaries.
- Return NEEDS_HUMAN only when a missing human decision prevents correct planning. Do not use it for a correctable planning defect.
- The Control Plane makes the final mode decision. Do not approve or reject a plan merely to reach a target mode.
- Do not include reviewRequestId, planId, or planSha256 in the JSON. The control plane binds those from the current request. APPROVED requires reasonCode PLAN_ACCEPTABLE and an empty findings array. REPLAN requires REPLAN_REQUIRED and 1-20 findings. NEEDS_HUMAN requires HUMAN_DECISION_REQUIRED and 1-20 findings.
- Findings may reference only current requirement ids and task keys. Do not return a replacement task graph or edited plan.
- Return exactly one JSON object, with no Markdown or surrounding prose. It must contain exactly:
  schemaVersion (integer 1), kind (string COMPLEX_PLAN_REVIEW), verdict, reasonCode, summary, findings.
- Each finding object must contain exactly code, message, requirementIds, taskKeys. The two id fields are JSON string arrays and may be empty.

Return the review now.
`, plan.PlanJSON, mustComplexPromptJSON(doc), strings.Join(checkIDs, ", "), requestID, plan.ID, plan.PlanSHA256)
}

func mustComplexPromptJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func mustAgentChosenPromptJSON(value any) string {
	encoded, err := core.MarshalAgentChosenResult(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}
