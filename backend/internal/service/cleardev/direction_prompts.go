package cleardev

import (
	"fmt"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

func directionChangePrompt(requirement core.DevelopmentRequirement, version core.RequirementVersion, message, directionRequestID string) string {
	example, err := core.MarshalAgentChosenResult(core.DirectionChangeRequestResult{
		SchemaVersion: core.ComplexProtocolVersion, Kind: string(core.DirectionAgentStepChange),
		DirectionRequestID: directionRequestID, DevelopmentRequirementID: requirement.ID,
		CurrentRequirementVersionID: version.ID, CurrentRequirementSHA256: version.SHA256,
		UserMessageSHA256: core.DirectionMessageSHA256(message), Decision: core.DirectionChangeDecisionStop,
		Summary:                "Stop current work and revise the confirmed requirement to the new direction.",
		AffectedRequirementIDs: []string{},
	})
	if err != nil {
		example = []byte("{}")
	}
	return fmt.Sprintf(`You are the original ClearDev Project Steward. The user asked to change direction of the current confirmed requirement. Do not modify files.

Current confirmed requirement version %s SHA-256 %s:
%s

User direction message:
%s

Current bindings (do not copy into the JSON):
directionRequestId=%s
developmentRequirementId=%s
currentRequirementVersionId=%s
currentRequirementSha256=%s
userMessageSha256=%s

Rules:
- Return exactly one JSON object, with no Markdown or surrounding prose.
- Do not include directionRequestId, developmentRequirementId, currentRequirementVersionId, currentRequirementSha256, or userMessageSha256 in the JSON. The control plane binds those from the current request.
- decision must be REQUEST_STOP_AND_REVISE.
- affectedRequirementIds may be empty or list stable requirement identifiers from the current version.

Return exactly this JSON object, filling only the allowed fields:
%s`, version.ID, version.SHA256, version.RequirementText, message, directionRequestID, requirement.ID, version.ID, version.SHA256, core.DirectionMessageSHA256(message), string(example))
}

func directionCompilationPrompt(planning core.ComplexPlanningSnapshot, previous *core.RequirementVersion, change core.DirectionChangeSnapshot, requestID, contextSHA string, round int) string {
	planning.Requirement.TargetRequirementVersionID = change.Revision.TargetRequirementVersionID
	example := complexCompilationExample(planning, requestID, contextSHA, round)
	previousText := ""
	if previous != nil {
		previousText = previous.RequirementText
	}
	message := ""
	if change.Intent != nil {
		message = change.Intent.Message
	}
	return fmt.Sprintf(`You are the original ClearDev Project Steward. Compile version 2 of the requirement after an approved direction change. Do not modify files.

Original PRD:
%s

Previous confirmed requirement:
%s

Approved user direction message:
%s

Rules:
- Return exactly one JSON object, with no Markdown or surrounding prose.
- Do not include compilationRequestId, requirementVersionId, compilationContextSha256, or clarificationRound in the JSON. The control plane binds those from the current request.
- previousRequirementId and previousAcceptanceId may reuse stable identifiers from the previous version. Omit them for items that should not appear in v2. Do not invent new stable identifiers.
- Round 0 must use outcome CLARIFICATION_REQUIRED and ask 1-8 blocking questions. Do not use READY on round 0, even if the approved direction message looks complete.
- After answers, round 1 should usually be READY. Only use CLARIFICATION_REQUIRED again with a non-empty additionalRoundReason.
- Do not ask a third round. Unknown fields, null, and duplicate keys are rejected.

Return exactly this JSON object, filling only the allowed fields:
%s`, planning.Requirement.OriginalPRDText, previousText, message, example)
}
