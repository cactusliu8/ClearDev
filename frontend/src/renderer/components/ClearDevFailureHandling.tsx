import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";

type Handling = components["schemas"]["ClearDevWorkflowFailureHandling"];

const ownerKeys = {
  BUILDER: "cleardevFailure.owner.builder",
  ENGINEERING_PLANNER: "cleardevFailure.owner.planner",
  STEWARD: "cleardevFailure.owner.steward",
  REVIEWER: "cleardevFailure.owner.reviewer",
  STAGE_REVIEWER: "cleardevFailure.owner.reviewer",
  FINAL_REVIEWER: "cleardevFailure.owner.reviewer",
  CONTROL_PLANE: "cleardevFailure.owner.control",
  HUMAN: "cleardevFailure.owner.human",
} as const;
const actionKeys = {
  REPAIR_ORIGINAL: "cleardevFailure.action.repair",
  RETRY_ORIGINAL: "cleardevFailure.action.retry",
  ASK_PLANNER: "cleardevFailure.action.planner",
  WAIT_FOR_EVIDENCE: "cleardevFailure.action.observe",
  ASK_HUMAN: "cleardevFailure.action.human",
  KEEP_STOPPED: "cleardevFailure.action.stopped",
  EXISTING_RECOVERY: "cleardevFailure.action.existing",
} as const;
const statusKeys = {
  AWAITING_DISPOSITION: "cleardevFailure.status.ready",
  NEEDS_HUMAN: "cleardevFailure.status.human",
  WAITING: "cleardevFailure.status.waiting",
  RECOVERY_REGISTERED: "cleardevFailure.status.registered",
  REPAIRING: "cleardevFailure.status.repairing",
  COORDINATING: "cleardevFailure.status.coordinating",
  VERIFIED: "cleardevFailure.status.verified",
} as const;

// Pure presentation: no POST, model call, permission decision or optimistic
// transition. Only the service's current facts can say who owns a failure.
export function ClearDevFailureHandling({ items, current }: { items?: Handling[]; current: boolean }) {
  const { t } = useTranslation();
  if (!items?.length) return null;
  const owner = (value: string) => t((Object.hasOwn(ownerKeys, value) ? ownerKeys[value as keyof typeof ownerKeys] : "cleardevFailure.owner.unknown") as MessageKey);
  const action = (value: string) => t((Object.hasOwn(actionKeys, value) ? actionKeys[value as keyof typeof actionKeys] : "cleardevFailure.action.unknown") as MessageKey);
  const status = (value: string) => t((Object.hasOwn(statusKeys, value) ? statusKeys[value as keyof typeof statusKeys] : "cleardevFailure.status.waiting") as MessageKey);
  const render = (item: Handling) => <div className="space-y-2" key={`${item.id}:${item.action}`}>
    <p className="font-medium">{owner(item.owner)} · {action(item.action)}</p>
    <p className="text-muted-foreground">{status(item.status)}</p>
    {item.sourceRole && <p className="text-xs text-muted-foreground">{t("cleardevFailure.source", { role: owner(item.sourceRole) })}</p>}
    {item.summary && <p className="whitespace-pre-wrap break-words">{item.summary}</p>}
    {item.path && <p className="break-all text-xs"><span className="font-medium">{t("cleardevFailure.path")}</span> <code>{item.path}</code></p>}
    <details className="text-xs text-muted-foreground">
      <summary className="cursor-pointer">{t("cleardevFailure.evidence")}</summary>
      <p className="mt-1 break-all">{item.sourceKind}: {item.sourceId}</p>
      <p className="break-all">{item.reason}</p>
    </details>
  </div>;
  const active = items.filter((item) => !item.historical);
  const history = items.filter((item) => item.historical);
  return <section className="mt-3 space-y-3 border-t pt-3" data-testid="cleardev-failure-handling">
    <h4 className="font-semibold">{t("cleardevFailure.title")}</h4>
    {!current ? <p role="status" className="text-muted-foreground">{t("cleardevFailure.stale")}</p> : <>
      {active.map((item) => <article className="rounded border bg-background/50 p-3" key={`${item.id}:${item.action}`}>{render(item)}</article>)}
      {history.length > 0 && <details className="rounded border p-3"><summary className="cursor-pointer font-medium">{t("cleardevFailure.history", { count: history.length })}</summary><div className="mt-3 space-y-4">{history.map(render)}</div></details>}
      <p className="text-xs text-muted-foreground">{t("cleardevFailure.boundary")}</p>
    </>}
  </section>;
}
