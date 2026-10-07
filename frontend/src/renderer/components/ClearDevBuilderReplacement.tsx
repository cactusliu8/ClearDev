import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";
import type { OpenBlockedSession } from "./ClearDevBlockerDiagnosis";
import { Button } from "./ui/button";

type Replacement = components["schemas"]["ClearDevBuilderReplacement"];
type Recovery = components["schemas"]["ClearDevWorkflowRecovery"];
type Option = components["schemas"]["ClearDevWorkflowRecoveryOption"];
const states: Record<string, MessageKey> = {
  PENDING_DECISION: "cleardevReplacement.state.PENDING_DECISION",
  APPROVED_AWAITING_CONTINUE: "cleardevReplacement.state.APPROVED_AWAITING_CONTINUE",
  REGISTERED: "cleardevReplacement.state.REGISTERED",
  CREATE_FAILED: "cleardevReplacement.state.CREATE_FAILED",
  COPY_FAILED: "cleardevReplacement.state.COPY_FAILED",
  UNKNOWN: "cleardevReplacement.state.UNKNOWN",
  HANDED_OFF: "cleardevReplacement.state.HANDED_OFF",
  REJECTED: "cleardevReplacement.state.REJECTED",
};

// Saved handoff facts explain what happened. Only live recovery options can
// admit a write; neither an old READY checkpoint nor a state label does so.
export function ClearDevBuilderReplacement({ replacements, history, options, current, executionRunId, requirementId, onOpenSession }: {
  replacements: Replacement[]; history: Recovery[]; options: Option[]; current: boolean;
  executionRunId: string; requirementId: string; onOpenSession?: OpenBlockedSession;
}) {
  const { t } = useTranslation();
  if (!replacements.length) return null;
  return <section className="mt-3 rounded border bg-background/50 p-3" data-testid="cleardev-builder-replacement">
    <h4 className="font-medium">{t("cleardevReplacement.title")}</h4>
    {replacements.map((item) => {
      const applies = current && executionRunId === item.executionRunId && history.some((request) =>
        request.action === "REQUEST_BUILDER_REPLACEMENT" && request.executionRunId === item.executionRunId &&
        request.targetId === item.targetId && request.taskId === item.taskId && request.stepId === item.stepId &&
        request.successorId === item.decisionRequestId) && options.some((option) =>
        ["REQUEST_BUILDER_REPLACEMENT", "CONTINUE_BUILDER_REPLACEMENT"].includes(option.action) && option.targetId === item.targetId && option.taskId === item.taskId);
      const facts: [MessageKey, string][] = [
        ["cleardevReplacement.oldWorker", item.oldAOSessionId], ["cleardevReplacement.newWorker", item.newAOSessionId],
        ["cleardevReplacement.oldDirectory", item.oldWorkspacePath], ["cleardevReplacement.newDirectory", item.newWorkspacePath],
        ["cleardevReplacement.oldHead", item.oldHeadSha], ["cleardevReplacement.snapshot", item.snapshotPath],
        ["cleardevReplacement.budget", item.budgetSummary],
      ];
      return <div key={item.id} className="mt-3 border-t pt-3" data-replacement-request={item.id}>
        <p>{t(states[item.state] ?? "cleardevReplacement.unknownState")}</p>
        {!applies && <p className="mt-1 text-muted-foreground">{t("cleardevReplacement.historical")}</p>}
        <p className="mt-2 text-muted-foreground">{t("cleardevReplacement.preserved")}</p>
        <dl className="mt-2 grid gap-x-3 gap-y-1 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
          {facts.map(([label, value]) => <div className="contents" key={label}><dt>{t(label)}</dt><dd className="whitespace-pre-wrap break-all">{value || t("cleardevReplacement.notCreated")}</dd></div>)}
        </dl>
        <p className="mt-2">{t("cleardevReplacement.files", { count: item.fileCount, bytes: item.totalBytes })}</p>
        {onOpenSession && <div className="mt-2 flex flex-wrap gap-2">
          {item.oldAOSessionId && <Button type="button" variant="outline" onClick={() => onOpenSession(requirementId, item.oldAOSessionId, t("cleardevReplacement.oldWorker"))}>{t("cleardevReplacement.oldWorker")}</Button>}
          {item.newAOSessionId && <Button type="button" variant="outline" onClick={() => onOpenSession(requirementId, item.newAOSessionId, t("cleardevReplacement.newWorker"))}>{t("cleardevReplacement.newWorker")}</Button>}
        </div>}
        <details className="mt-2 text-xs text-muted-foreground"><summary>{t("cleardevReplacement.technical")}</summary>
          <dl className="mt-2 grid gap-x-3 gap-y-1 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
            {([
              ["cleardevReplacement.oldRole", item.oldRoleBindingId], ["cleardevReplacement.newRole", item.newRoleBindingId],
              ["cleardevReplacement.snapshotDigest", item.snapshotSha256], ["cleardevReplacement.stage", item.lastStage],
              ["cleardevReplacement.outcome", item.lastOutcome],
            ] as [MessageKey, string][]).map(([label, value]) => <div className="contents" key={label}><dt>{t(label)}</dt><dd className="break-all">{value || "—"}</dd></div>)}
          </dl>
          <p className="mt-1 break-all"><code>{item.id} · {item.executionRunId} · {item.targetId} · {item.taskId} · {item.dispatchId} · {item.stepId}</code></p>
          <p className="mt-1 break-all"><code>{item.decisionRequestId} · {item.continueRequestId} · {item.state} · {item.reasonCode}</code></p>
        </details>
      </div>;
    })}
  </section>;
}
