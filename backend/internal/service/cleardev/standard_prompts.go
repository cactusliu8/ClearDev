package cleardev

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

type standardExecutionPackage = core.StandardExecutionPackage

func encodeStandardExecutionPackage(value standardExecutionPackage) ([]byte, string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return encoded, coreDigest(encoded), nil
}

func planningPrompt(requestID, versionID, requirementSHA, requirementText string) string {
	return fmt.Sprintf(`You are the ClearDev Project Steward. Decide only whether to request engineering planning for the already confirmed requirement below.

Confirmed requirement:
%s

Return exactly one JSON object, with no Markdown or surrounding prose:
{"schemaVersion":1,"kind":"REQUEST_PLANNING","decision":"PLAN"}`, requirementText)
}

func engineeringPlanPrompt(requestID, versionID, requirementSHA, requirementText string, baseTaskSetVersion int64) string {
	taskJSON, _ := json.Marshal(core.FrozenStandardTaskPlan())
	return fmt.Sprintf(`You are the ClearDev Engineering Planner. Produce the single frozen STANDARD task for this confirmed requirement. Do not modify files.

Confirmed requirement:
%s

The task object must be exactly this JSON value:
%s

Return exactly one JSON object, with no Markdown or surrounding prose. Do not include planningRequestId, requirementVersionId, requirementSha256, or baseTaskSetVersion; the control plane binds those from the current request:
{"schemaVersion":1,"kind":"ENGINEERING_PLAN","mode":"STANDARD","task":%s}`, requirementText, taskJSON, taskJSON)
}

func planReviewPrompt(planID, planSHA string, normalizedPlan []byte) string {
	return fmt.Sprintf(`You are the ClearDev Project Steward. Review the exact immutable engineering plan below for requirement coverage, scope, and checks. Do not rewrite it.

Plan:
%s

Current bindings (do not copy into the JSON):
planId=%s
planSha256=%s

If it matches the confirmed bounded email-normalization task, return exactly this JSON object and no other text:
{"schemaVersion":1,"kind":"PLAN_REVIEW","verdict":"APPROVED","reasonCode":"PLAN_ACCEPTABLE","summary":"计划覆盖已确认需求，范围和检查适合本次单任务演示。"}

If it must be replanned, use verdict REPLAN with reasonCode REPLAN_REQUIRED. If a human decision is required, use verdict NEEDS_HUMAN with reasonCode HUMAN_DECISION_REQUIRED.`, normalizedPlan, planID, planSHA)
}

func dispatchRequestPrompt(planID, planSHA string) string {
	return fmt.Sprintf(`You are the ClearDev Project Steward. Request dispatch of the exact approved STANDARD plan.

Current bindings (do not copy into the JSON):
planId=%s
planSha256=%s

Return exactly one JSON object and no other text:
{"schemaVersion":1,"kind":"DISPATCH_REQUEST","mode":"STANDARD","decision":"DISPATCH"}`, planID, planSHA)
}

func builderPrompt(executionPackage []byte, round int, feedback string) string {
	feedbackSection := ""
	if feedback != "" {
		feedbackSection = "\n\nControl Plane feedback from the prior candidate:\n" + feedback
	}
	return fmt.Sprintf(`You are the ClearDev Builder. Implement only the execution package below in your current managed worktree.

Execution package:
%s

Current bindings (do not copy into the JSON):
dispatchId=%s
taskId=%s
round=%d

Rules:
- Modify only the allowed paths in the package. Do not change package.json or any forbidden path.
- Implement normalizeEmail exactly as required and add the requested tests.
- This is an unattended bounded workflow. Do not ask questions, request user input, wait for confirmation, or start a watch/interactive process. The package contains every decision you need.
- Run the required checks with commands that exit on completion.
- Commit the completed changes non-interactively to the current branch with an explicit commit message. Do not report or invent a commit SHA; the Control Plane will observe Git itself.
- If implementation cannot continue, report BLOCKED or NEEDS_HUMAN.
%s

After the worktree is clean and committed, immediately return exactly one JSON object and no other text, then end the turn:
{"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"CANDIDATE_READY","summary":"已实现邮箱规范化并补充测试。"}`, executionPackage, executionPackageField(executionPackage, "dispatchId"), executionPackageField(executionPackage, "taskId"), round, feedbackSection)
}

type standardReviewPacket struct {
	SchemaVersion int                   `json:"schemaVersion"`
	AssignmentID  string                `json:"reviewAssignmentId"`
	Requirement   string                `json:"requirement"`
	PlanJSON      json.RawMessage       `json:"plan"`
	ExecutionPack json.RawMessage       `json:"executionPackage"`
	BaseSHA       string                `json:"baseSha"`
	CandidateID   string                `json:"candidateId"`
	CandidateSHA  string                `json:"candidateSha"`
	Diff          []standardReviewPath  `json:"diff"`
	Checks        []standardReviewCheck `json:"checks"`
}

type standardReviewPath struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
}

type standardReviewCheck struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Result       string `json:"result"`
	OutputSHA256 string `json:"outputSha256,omitempty"`
}

func legacyReviewerPrompt(packet []byte, assignmentID, candidateID, candidateSHA, packetSHA string) string {
	return fmt.Sprintf(`You are the independent ClearDev Reviewer. Review only the exact local candidate and evidence packet below. Do not modify or commit files. The Builder candidate is immutable and the Control Plane, not you, owns all test facts. This is an unattended bounded workflow: do not ask questions, request user input, wait for confirmation, or start an interactive process.

Inspect the local candidate read-only, including the relevant diff, source, and tests. Repository-required estimate and progress messages before or during tool use are allowed as intermediate messages; they are not the review result.

Review packet:
%s

Current bindings (do not copy into the JSON):
reviewAssignmentId=%s
candidateId=%s
candidateSha=%s
reviewPacketSha256=%s

After the inspection, make the final assistant message exactly one JSON object and no other text. Do not include reviewAssignmentId, candidateId, candidateSha, or reviewPacketSha256. For a passing candidate use:
{"schemaVersion":1,"kind":"LOCAL_REVIEW","verdict":"PASS","reasonCode":"REVIEW_PASSED","summary":"实现符合需求，测试覆盖主要行为。","findings":[]}

For REWORK use reasonCode REVIEW_CHANGES_REQUIRED and at least one BLOCKING finding whose path appears in the packet diff. BLOCKED uses REVIEW_BLOCKED. NEEDS_HUMAN uses REVIEW_NEEDS_HUMAN.`, packet, assignmentID, candidateID, candidateSHA, packetSHA)
}

func statusReportPrompt(requirementID, phase, attention string) string {
	return fmt.Sprintf(`You are the ClearDev Project Steward. Report the already-derived durable status. Your message cannot change it.

Current bindings (do not copy into the JSON):
developmentRequirementId=%s
phase=%s
attention=%s

Return exactly one JSON object and no other text:
{"schemaVersion":1,"kind":"STATUS_REPORT","summary":"一个开发任务和集成检查均已完成。"}`, requirementID, phase, attention)
}

func executionPackageField(encoded []byte, field string) string {
	var values map[string]json.RawMessage
	if json.Unmarshal(encoded, &values) != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(values[field], &value)
	return value
}

func coreDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("%x", digest)
}
