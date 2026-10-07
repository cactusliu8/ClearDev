import { describe, expect, it } from "vitest";
import { buildHumanDecisionResult, decisionFromDialogIndex, encodeFrame, offerDialogOptions, parseHumanDecisionOffer, splitFrames } from "./human-authority";
import { SIMULATED_USER_ALLOWED_KINDS } from "./benchmark-simulated-user";

// Protocol/native-dialog adapter test, not a claim of live Electron approval.
const extraOffer = () => ({
 protocolVersion: 1, kind: "HUMAN_DECISION_OFFER", desktopRunId: "explicit-test-desktop", requestId: "cleardev-mail-extra-attempt:run",
 decisionKind: "AUTHORIZE_MAIL_EXTRA_ATTEMPT", bindingSchemaVersion: 1,
 binding: { developmentRequirementId: "requirement", requirementVersionId: "version", requirementVersionSha256: "a".repeat(64), executionRunId: "run", planSha256: "b".repeat(64), taskId: "task", dispatchId: "failed-dispatch", candidateSha: "c".repeat(40), nextRound: 3 },
 contentSha256: "d".repeat(64), nonce: "explicit-nonproduction-test-nonce", expiresAt: "2026-09-19T23:00:00Z",
 display: { title: "Authorize one additional development attempt", summary: "Automatic attempts are exhausted.", fullContent: "Task task; candidate " + "c".repeat(40), changeSummary: "Exactly one attempt. No scope, model, acceptance criteria, or existing counter changes." },
});

describe("extra mail attempt uses existing desktop authority", () => {
 it.each([0, 1, 2, -1])("preserves all immutable bindings for dialog choice %s", (index) => {
  const source = extraOffer();
  const frames = splitFrames(encodeFrame(source));
  expect(frames.rest.length).toBe(0);
  const offer = parseHumanDecisionOffer(frames.frames[0]);
  const result = buildHumanDecisionResult(offer, decisionFromDialogIndex(index));
  expect(result.binding).toEqual(source.binding);
  expect(result.contentSha256).toBe(source.contentSha256);
  expect(result.nonce).toBe(source.nonce);
  expect(result.requestId).toBe(source.requestId);
  expect(result.decisionKind).toBe(source.decisionKind);
  expect(result.decision).toBe(index === 0 ? "APPROVE" : index === 1 ? "REJECT" : "LATER");
 });
 it("shows exact scope and defaults to Later, never silent approval", () => {
  const options = offerDialogOptions(extraOffer());
  expect(options.buttons).toEqual(["Approve", "Reject", "Later"]);
  expect(options.defaultId).toBe(2);
  expect(options.cancelId).toBe(2);
  expect(options.detail).toContain("c".repeat(40));
  expect(options.detail).toContain("Exactly one attempt");
 });
 it("does not enable benchmark simulated-user approval for the new authority", () => {
  expect(SIMULATED_USER_ALLOWED_KINDS as readonly string[]).not.toContain("AUTHORIZE_MAIL_EXTRA_ATTEMPT");
 });
});
