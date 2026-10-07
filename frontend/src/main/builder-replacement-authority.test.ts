// @vitest-environment node
import { expect, it } from "vitest";
import { buildHumanDecisionResult, decisionFromDialogIndex, encodeFrame, offerDialogOptions, parseHumanDecisionOffer, splitFrames } from "./human-authority";
import { SIMULATED_USER_ALLOWED_KINDS } from "./benchmark-simulated-user";

// Protocol/native-dialog adapter evidence, not a live user approval.
const replacementOffer = () => ({
  protocolVersion: 1, kind: "HUMAN_DECISION_OFFER", desktopRunId: "explicit-test-desktop", requestId: "exact-handoff-request",
  decisionKind: "AUTHORIZE_BUILDER_REPLACEMENT", bindingSchemaVersion: 1,
  binding: { executionRunId: "run", taskId: "original-task", logicalStepId: "unsent-step", oldAOSessionId: "lost-worker", snapshotSha256: "a".repeat(64), sessionCreationKey: "unique-replacement-key" },
  contentSha256: "b".repeat(64), nonce: "explicit-nonproduction-fixture-nonce", expiresAt: "2099-01-01T00:00:00Z",
  display: { title: "Replace worker", summary: "Replace the lost Builder for the original task without changing its code, history or budgets.", fullContent: "Preserve original code and budget. No new worker or task message before a separate Continue.", changeSummary: "Authorize precisely one bound worker handoff." },
});
it.each([0, 1, 2, -1])("returns dialog choice %s with every exact immutable replacement binding preserved", (index) => {
  const source = replacementOffer();
  const frames = splitFrames(encodeFrame(source));
  expect(frames.rest.length).toBe(0);
  const offer = parseHumanDecisionOffer(frames.frames[0]);
  const result = buildHumanDecisionResult(offer, decisionFromDialogIndex(index));
  expect(result).toMatchObject({ requestId: source.requestId, decisionKind: source.decisionKind, binding: source.binding, contentSha256: source.contentSha256, nonce: source.nonce, decision: index === 0 ? "APPROVE" : index === 1 ? "REJECT" : "LATER" });
});
it("defaults and cancellation select Later, and benchmark simulation cannot approve a replacement", () => {
  expect(offerDialogOptions(replacementOffer(), "zh-CN")).toMatchObject({ title: "更换工作者", buttons: ["批准", "拒绝", "稍后决定"], defaultId: 2, cancelId: 2 });
  expect(SIMULATED_USER_ALLOWED_KINDS as readonly string[]).not.toContain("AUTHORIZE_BUILDER_REPLACEMENT");
});
