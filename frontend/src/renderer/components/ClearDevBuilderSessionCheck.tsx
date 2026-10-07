import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";

type Check = components["schemas"]["ClearDevBuilderSessionCheck"];
type Recovery = components["schemas"]["ClearDevWorkflowRecovery"];
type Option = components["schemas"]["ClearDevWorkflowRecoveryOption"];
type Replacement = components["schemas"]["ClearDevBuilderReplacement"];
const reasonGroups: Record<string, string> = {
  LOGIN_REQUIRED: "AUTH", MODEL_NOT_AVAILABLE: "TOOL", EXECUTION_TOOL_NOT_INSTALLED: "TOOL",
  EXECUTION_TOOL_CONFIG_INVALID: "TOOL", DRIVER_INCOMPATIBLE: "TOOL", PROVIDER_UNAVAILABLE: "PROVIDER",
  QUOTA_EXHAUSTED: "QUOTA", RATE_LIMITED: "RATE_LIMIT", RECOVERY_NOT_CURRENT: "SOURCE",
  RECOVERY_SESSION_UNAVAILABLE: "SESSION", RECOVERY_NATIVE_IDENTITY_MISSING: "SESSION",
  BUILDER_WORKTREE_DIRTY: "WORKTREE", CANDIDATE_INVALID: "WORKTREE", GIT_UNAVAILABLE: "WORKTREE",
  GIT_CHECKER_UNAVAILABLE: "WORKTREE", CANDIDATE_NOT_DESCENDANT: "WORKTREE",
};

// Saved checkpoints explain one explicit request. Neither READY nor text in a
// checkpoint creates an action; the enclosing panel uses live backend options.
export function ClearDevBuilderSessionCheck({ checks, history, options, current, replacements = [] }: { checks: Check[]; history: Recovery[]; options: Option[]; current: boolean; replacements?: Replacement[] }) {
  const { t, i18n } = useTranslation();
  const request = history.filter((item) => item.action === "RETRY_BUILDER_SESSION").at(-1);
  if (!request) return null;
  const replaced = replacements.some((item) => item.executionRunId === request.executionRunId && item.taskId === request.taskId && item.stepId === request.stepId && item.oldRoleBindingId === request.bindingId);
  const applies = !replaced && current && options.some((option) => option.action === request.action && option.targetId === request.targetId && option.taskId === request.taskId);
  const related = checks.filter((item) => item.recoveryId === request.id);
  const last = related.at(-1);
  const stageKey = last ? `cleardevRecheck.stage.${last.stage}` : "";
  const reasonKey = last?.reasonCode ? `cleardevRecheck.reason.${last.reasonCode}` : "";
  const fallback = `cleardevBlocker.${reasonGroups[last?.reasonCode ?? ""] ?? "UNKNOWN"}.steps` as MessageKey;
  const outcomeKey: MessageKey = !last ? "cleardevRecheck.waiting" : last.outcome === "FAILED" ? "cleardevRecheck.failed" : last.outcome === "READY" ? "cleardevRecheck.ready" : "cleardevRecheck.pending";
  return <section className="mt-3 rounded border bg-background/50 p-3" data-testid="cleardev-builder-session-check">
    <h4 className="font-medium">{t("cleardevRecheck.title")}</h4>
    <p className="mt-2">{t(outcomeKey)}</p>
    {!applies && <p className="mt-1 text-muted-foreground">{t("cleardevRecheck.historical")}</p>}
    {replaced && <p className="mt-1 text-muted-foreground">{t("cleardevReplacement.oldCheck")}</p>}
    {last && <p className="mt-2"><span className="font-medium">{t("cleardevRecheck.stage")}{": "}</span>{i18n.exists(stageKey) ? t(stageKey as MessageKey) : t("cleardevRecheck.unknownStage")}</p>}
    {last?.reasonCode && <>
      {applies && <p className="mt-2 whitespace-pre-wrap break-words">{i18n.exists(reasonKey) ? t(reasonKey as MessageKey) : t(fallback)}</p>}
      <p className="mt-1 text-xs text-muted-foreground">{t("cleardevRecheck.reason")}{": "}<code>{last.reasonCode}</code></p>
    </>}
    <p className="mt-2 break-all text-xs text-muted-foreground">{t("cleardevRecheck.request")}{": "}{request.id}</p>
    <p className="mt-1 break-all text-xs text-muted-foreground">{t("cleardevBlocker.fact.STEP_ID")}{": "}{request.stepId}</p>
    {last?.preflightId && <p className="mt-1 break-all text-xs text-muted-foreground">{t("cleardevRecheck.preflight")}{": "}{last.preflightId}</p>}
  </section>;
}
