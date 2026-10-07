import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { Button } from "./ui/button";

type Requirement = components["schemas"]["ClearDevRequirementView"];
type Request = components["schemas"]["ClearDevPlannerClarificationRequest"];
const pendingKey = (id: string) => ["cleardev-planner-answer-pending", id] as const;
const reasonKeys: Record<string, MessageKey> = {
 PLANNER_ANSWER_LIMIT_REACHED: "cleardevPlanner.limit",
 PLANNING_SOURCE_CHANGED: "cleardevPlanner.source",
 PLANNER_SESSION_NOT_READY: "cleardevPlanner.session",
 PLANNER_QUESTION_EVIDENCE_UNAVAILABLE: "cleardevPlanner.evidence",
 PLANNER_REQUIREMENT_STOPPED: "cleardevPlanner.stopped",
 PLANNER_DIRECTION_STOPPED: "cleardevPlanner.direction",
 LOGIN_REQUIRED: "cleardevPlanner.login",
 MODEL_NOT_AVAILABLE: "cleardevPlanner.model",
 QUOTA_EXHAUSTED: "cleardevPlanner.quota",
 RATE_LIMITED: "cleardevPlanner.rate",
};
const storageKey = (id: string) => `cleardev-planner-answers:${id}`;
const sameTarget = (a: Pick<Request, "planId" | "planSha256">, b: Pick<Request, "planId" | "planSha256">) => a.planId === b.planId && a.planSha256 === b.planSha256;
function readPending(id: string): Request[] {
 try {
  const raw: unknown = JSON.parse(sessionStorage.getItem(storageKey(id)) ?? "[]");
  if (!Array.isArray(raw)) return [];
  return raw.filter((item): item is Request => typeof item?.requestId === "string" && typeof item?.planId === "string" && typeof item?.planSha256 === "string" && Array.isArray(item?.answers) && item.answers.length <= 8 && item.answers.every((answer: unknown) => typeof answer === "string") && item.answers.join("").length <= 20000).slice(-8);
 } catch { return []; }
}
function writePending(id: string, pending: Request[]) {
 try { if (pending.length) sessionStorage.setItem(storageKey(id), JSON.stringify(pending.slice(-8))); else sessionStorage.removeItem(storageKey(id)); } catch { /* Query cache still retains the exact request for this app session. */ }
}

export function ClearDevPlannerAnswers({ view, current, onSaved }: { view: Requirement; current: boolean; onSaved: () => void }) {
 const { t } = useTranslation();
 const client = useQueryClient();
 const id = view.requirement.id!;
 const target = view.complexPlanning?.plannerClarification;
 const history = view.complexPlanning?.plannerAnswerHistory ?? [];
 const [draft, setDraft] = useState<string[]>([]);
 const [revision, setRevision] = useState(0);
 void revision;
 const mutationKey = ["cleardev-planner-answer-write", id];
 const pending = client.getQueryData<Request[]>(pendingKey(id)) ?? readPending(id);
 const match: Pick<Request, "planId" | "planSha256"> = { planId: target?.planId ?? "", planSha256: target?.planSha256 ?? "" };
 const recorded = target?.answer;
 const known = recorded ? { requestId: recorded.requestId, planId: recorded.planId, planSha256: recorded.planSha256, answers: recorded.answers } : undefined;
 const unknown = pending.find((item) => sameTarget(item, match));
 const body = known ?? unknown;
 const answers = body?.answers ?? target?.questions?.map((_, index) => draft[index] ?? "") ?? [];
 const save = useMutation({
  mutationKey, retry: false,
  mutationFn: async (request: Request) => {
   await client.cancelQueries({ queryKey: ["cleardev-operate", id] });
   const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/planner-clarifications", { params: { path: { id } }, body: request });
   if (result.error) throw Object.assign(new Error(apiErrorMessage(result.error)), { status: result.response?.status });
   if (!result.data) throw new Error(t("cleardevPlanner.unknown"));
   return result.data;
  },
  onSuccess: (result, request) => {
   client.setQueryData(["cleardev-operate", id], result);
   const remaining = (client.getQueryData<Request[]>(pendingKey(id)) ?? readPending(id)).filter((item) => !sameTarget(item, request));
   client.setQueryData(pendingKey(id), remaining); writePending(id, remaining);
   onSaved();
  },
  onError: (error, request) => {
   if ((error as Error & { status?: number }).status === 400) {
    const remaining = (client.getQueryData<Request[]>(pendingKey(id)) ?? readPending(id)).filter((item) => !sameTarget(item, request));
    client.setQueryData(pendingKey(id), remaining); writePending(id, remaining); setDraft(request.answers);
   }
   void client.invalidateQueries({ queryKey: ["cleardev-operate", id] });
  },
  onSettled: () => setRevision((value) => value + 1),
 });
 if (!target && !history.length) return null;
 const source = view.trustedProgress?.projectPlanning;
 const tooLong = answers.reduce((count, answer) => count + Array.from(answer.trim()).length, 0) > 10000;
 const available = current && hasTrustedApiBaseUrl() && target?.canAnswer && (!source || source.current && source.sourceCurrent);
 return <div data-testid="cleardev-planner-answers" className="mt-3 flex flex-col gap-2">
  {target ? <form onSubmit={(event) => {
   event.preventDefault();
   if (!available || tooLong || save.isPending || client.isMutating({ mutationKey }) || !answers.length || answers.some((answer) => !answer?.trim())) return;
   const latestPending = client.getQueryData<Request[]>(pendingKey(id)) ?? readPending(id);
   const request: Request = known ?? latestPending.find((item) => sameTarget(item, match)) ?? { requestId: crypto.randomUUID(), planId: target.planId, planSha256: target.planSha256, answers: answers.map((answer) => answer.trim()) };
   const next = [...latestPending.filter((item) => !sameTarget(item, request)), request].slice(-8);
   client.setQueryData(pendingKey(id), next); writePending(id, next);
   setRevision((value) => value + 1);
   save.mutate(request);
  }} className="flex flex-col gap-2">
   <p className="text-sm text-muted-foreground">{t("cleardevPlanner.help")}</p>
   {target.questions?.map((question, index) => <label key={question} className="flex flex-col gap-1">{question}<textarea aria-label={question} value={answers[index] ?? ""} disabled={!available || save.isPending || Boolean(body)} maxLength={10000} rows={3} onChange={(event) => setDraft((old) => { const next = [...old]; next[index] = event.target.value; return next; })} className="rounded-md border bg-background p-2" /></label>)}
   {recorded ? <p role="status">{t("cleardevPlanner.recorded")}</p> : unknown ? <p role="alert">{t("cleardevPlanner.unknown")}</p> : null}
   {!available ? <p role="alert">{!current ? t("cleardevOperate.stale") : t(reasonKeys[target.reasonCode ?? ""] ?? "cleardevPlanner.unavailable")}</p> : null}
   {available && target.reasonCode ? <p role="status">{t(reasonKeys[target.reasonCode] ?? "cleardevPlanner.unavailable")}</p> : null}
   {tooLong ? <p role="alert">{t("cleardevPlanner.tooLong")}</p> : null}
   {save.error ? <p role="alert">{apiErrorMessage(save.error)}</p> : null}
   <Button type="submit" disabled={!available || tooLong || save.isPending || !answers.length || answers.some((answer) => !answer?.trim())}>{save.isPending ? t("cleardevPlanner.sending") : recorded ? t("cleardevPlanner.continue") : t("cleardevPlanner.answer")}</Button>
  </form> : null}
  {history.length ? <details><summary>{t("cleardevPlanner.history")}</summary>{history.map((answer) => <div key={answer.requestId} className="border-t py-2"><code>{answer.requestId}</code><ol>{answer.answers?.map((text, index) => <li key={index}><p>{answer.questions?.[index]}</p>{text}</li>)}</ol></div>)}</details> : null}
 </div>;
}
