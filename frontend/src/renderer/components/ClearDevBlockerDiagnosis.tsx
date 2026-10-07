import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";
import { Button } from "./ui/button";

type Diagnosis = components["schemas"]["ClearDevWorkflowDiagnosis"];
type Issue = components["schemas"]["ClearDevWorkflowDiagnosisIssue"];
type Evidence = components["schemas"]["ClearDevWorkflowDiagnosisEvidence"];
export type OpenBlockedSession = (requirementId: string, sessionId: string, title: string) => void;
type Props = { diagnosis?: Diagnosis; requirementId: string; current: boolean; hasOperation: boolean; onOpenSession?: OpenBlockedSession };

// This component only explains saved facts. It contains no recovery POST,
// model call, permission decision, or derivation of write eligibility.
export function ClearDevBlockerDiagnosis({ diagnosis, requirementId, current, hasOperation, onOpenSession }: Props) {
  const { t, i18n } = useTranslation();
  if (!diagnosis) return null;
  if (diagnosis.readError || !diagnosis.current || !current) return <section className="mt-3 border-t pt-3" data-testid="cleardev-blocker-diagnosis">
    <h4 className="font-medium">{t("cleardevBlocker.title")}</h4>
    <p role="alert" className="mt-2 text-muted-foreground">{t(diagnosis.readError === "DIAGNOSIS_UNAVAILABLE" ? "cleardevBlocker.readError" : "cleardevBlocker.stale")}</p>
  </section>;
  if (!diagnosis.issues.length) return null;
  const message = (key: string, fallback: MessageKey) => t((i18n.exists(key) ? key : fallback) as MessageKey);
  const value = (raw: string) => message(`cleardevBlocker.value.${raw}`, "cleardevBlocker.value.unavailable");
  const guide = (issue: Issue, part: "title" | "cause" | "risk" | "steps" | "done") => message(`cleardevBlocker.${issue.category}.${part}`, `cleardevBlocker.UNKNOWN.${part}`);
  const displayValue = (fact: Evidence) => {
    const statuses = ["SESSION_STATE", "SESSION_TERMINATED", "NATIVE_IDENTITY_PRESENT", "SESSION_READ", "STEP_STATUS", "DISPATCH_STATUS", "MESSAGE_STATE", "CHECK_TIMED_OUT"];
    return statuses.includes(fact.kind) && i18n.exists(`cleardevBlocker.value.${fact.value}`) ? value(fact.value) : fact.value;
  };
  const factLabel = (fact: Evidence) => i18n.exists(`cleardevBlocker.fact.${fact.kind}`) ? t(`cleardevBlocker.fact.${fact.kind}` as MessageKey) : fact.kind;
  const facts = (issue: Issue) => <details className="mt-2" open={issue.relationship === "CURRENT"}>
    <summary className="cursor-pointer text-muted-foreground">{t("cleardevBlocker.facts")}</summary>
    <dl className="mt-2 grid grid-cols-1 gap-x-3 gap-y-1 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
      {issue.evidence.filter((fact) => fact.kind !== "CHECK_OUTPUT").map((fact, index) => <div className="contents" key={`${fact.kind}:${fact.factId ?? ""}:${index}`}>
        <dt className="text-muted-foreground">{factLabel(fact)}</dt>
        <dd className="whitespace-pre-wrap break-words" title={fact.factId ? `${t("cleardevBlocker.factId")}: ${fact.factId}` : undefined}>{displayValue(fact)}{fact.truncated && <span className="block text-xs text-muted-foreground">{t("cleardevBlocker.excerpt")}</span>}</dd>
      </div>)}
    </dl>
    {issue.evidence.some((fact) => fact.kind === "SESSION_STATE") && <p className="mt-2 text-xs text-muted-foreground">{t("cleardevBlocker.sessionObservation")}</p>}
    {issue.evidence.filter((fact) => fact.kind === "CHECK_OUTPUT").map((fact, index) => <div className="mt-2" key={`log:${fact.factId ?? ""}:${index}`}>
      <p className="text-muted-foreground">{factLabel(fact)}</p>
      <pre className="mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded border bg-background p-2 text-xs">{fact.value}</pre>
      {fact.truncated && <p className="mt-1 text-xs text-muted-foreground">{t("cleardevBlocker.excerpt")}</p>}
    </div>)}
  </details>;
  const body = (issue: Issue) => <>
    {issue.relationship === "PRECEDING_FAILURE" && <p className="mt-1 text-muted-foreground">{t("cleardevBlocker.previousNotice")}</p>}
    <p className="mt-2"><span className="font-medium">{t("cleardevBlocker.cause")}</span>{": "}{guide(issue, "cause")}</p>
    {issue.summary ? <p className="mt-2 whitespace-pre-wrap break-words"><span className="font-medium">{t("cleardevBlocker.savedSummary")}</span>{": "}{issue.summary}</p> : issue.relationship === "CURRENT" && !issue.evidence.some((fact) => ["PARSE_ERROR", "FAILURE_SUMMARY", "CHECK_OUTPUT", "PREFLIGHT_SUMMARY"].includes(fact.kind)) ? <p className="mt-2 text-muted-foreground">{t("cleardevBlocker.noDetail")}</p> : null}
    {issue.evidence.length > 0 && facts(issue)}
    {issue.relationship !== "HISTORICAL" && <div className="mt-3 space-y-2">
      <p><span className="font-medium">{t("cleardevBlocker.steps")}</span>{": "}{guide(issue, "steps")}</p>
      <p className="text-muted-foreground"><span className="font-medium">{t("cleardevBlocker.risk")}</span>{": "}{guide(issue, "risk")}</p>
      <p className="text-muted-foreground"><span className="font-medium">{t("cleardevBlocker.done")}</span>{": "}{guide(issue, "done")}</p>
    </div>}
    {issue.aoSessionId && onOpenSession && <Button type="button" variant="outline" size="sm" className="mt-2" onClick={() => onOpenSession(requirementId, issue.aoSessionId!, t("cleardevBlocker.sessionTitle"))}>{t("cleardevBlocker.openSession")}</Button>}
    <p className="mt-2 text-xs text-muted-foreground">{t("cleardevBlocker.code")}{": "}<code className="break-all">{issue.reasonCode}</code></p>
  </>;
  const stamp = new Date(diagnosis.observedAt);
  return <section className="mt-3 border-t pt-3" data-testid="cleardev-blocker-diagnosis">
    <h4 className="font-semibold">{t("cleardevBlocker.title")}</h4>
    <p className="mt-1 text-muted-foreground">{t("cleardevBlocker.phase")}{": "}{i18n.exists(`cleardevBlocker.value.${diagnosis.phase}`) ? value(diagnosis.phase) : diagnosis.phase}</p>
    {diagnosis.nextOwner.role && <p className="mt-1 text-muted-foreground">{t("cleardevBlocker.nextOwner")}{": "}{i18n.exists(`cleardevBlocker.owner.${diagnosis.nextOwner.role}`) ? t(`cleardevBlocker.owner.${diagnosis.nextOwner.role}` as MessageKey) : diagnosis.nextOwner.role}</p>}
    {Number.isFinite(stamp.getTime()) && <p className="mt-1 text-xs text-muted-foreground">{t("cleardevBlocker.observed", { time: stamp.toLocaleString(i18n.resolvedLanguage) })}</p>}
    {diagnosis.issues.map((issue, index) => issue.relationship === "CURRENT" && index === 0 ? <article className="mt-3 rounded border bg-background/50 p-3" key={issue.id}>
      <h5 className="font-medium">{t("cleardevBlocker.current")}{": "}{guide(issue, "title")}</h5>{body(issue)}
    </article> : <details className="mt-3 rounded border bg-background/50 p-3" key={issue.id}>
      <summary className="cursor-pointer font-medium">{t(issue.relationship === "PRECEDING_FAILURE" ? "cleardevBlocker.previous" : issue.relationship === "LIMIT" ? "cleardevBlocker.limit" : issue.relationship === "HISTORICAL" ? "cleardevBlocker.historical" : "cleardevBlocker.current")}{": "}{guide(issue, "title")}</summary>{body(issue)}
    </details>)}
    {!hasOperation && <p className="mt-3 text-muted-foreground">{t("cleardevBlocker.noOperation")}</p>}
    <p className="mt-2 text-xs text-muted-foreground">{t("cleardevBlocker.guidanceOnly")}</p>
  </section>;
}
