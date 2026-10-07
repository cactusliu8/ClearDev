import { useEffect, useRef, useState } from "react";
import { useIsMutating, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { hasCurrentFacts, useFreshnessClock } from "../lib/cleardev-freshness";
import type { components } from "../../api/schema";
import { Button } from "./ui/button";
import { ClearDevFailureHandling } from "./ClearDevFailureHandling";
import { ClearDevBlockerDiagnosis, type OpenBlockedSession } from "./ClearDevBlockerDiagnosis";
import { ClearDevBuilderSessionCheck } from "./ClearDevBuilderSessionCheck";
import { ClearDevBuilderReplacement } from "./ClearDevBuilderReplacement";

type Option = components["schemas"]["ClearDevWorkflowRecoveryOption"];
type Recovery = components["schemas"]["ClearDevWorkflowRecovery"];
type Request = components["schemas"]["ClearDevWorkflowRecoveryRequest"];
type Scope = "discussion" | "compilation" | "execution";
type Props = { id: string; scope?: Scope; current?: boolean; completed?: boolean; blocked?: boolean; sourceKey?: string; onOpenSession?: OpenBlockedSession };
const planningAction = "RETRY_PLANNING_STEP";
const coordinationAction = "RETRY_PLANNER_COORDINATION";
const extraCoordinationRequest = "REQUEST_EXTRA_PLANNER_COORDINATION";
const stoppedCheckRequest = "REQUEST_STOPPED_CHECK_RECOVERY";
const extraPlanningRequest = "REQUEST_EXTRA_PLANNING_ATTEMPT";
const extraPlanningContinue = "CONTINUE_EXTRA_PLANNING_ATTEMPT";
const extraPlanningOperation = (action: string) => action === extraPlanningRequest || action === extraPlanningContinue;
const replacementRequest = "REQUEST_BUILDER_REPLACEMENT";
const replacementContinue = "CONTINUE_BUILDER_REPLACEMENT";
const replacementOperation = (action: string) => action === replacementRequest || action === replacementContinue;
const planningOperation = (action: string) => [planningAction, extraPlanningRequest, extraPlanningContinue].includes(action);
const actionKeys = {
  RETRY_PLANNER_COORDINATION: "cleardevRecovery.coordination",
  REQUEST_EXTRA_PLANNER_COORDINATION: "cleardevRecovery.extraCoordinationRequest",
  REQUEST_STOPPED_CHECK_RECOVERY: "cleardevRecovery.stoppedCheckRequest",
  CONTINUE_BUILDER: "cleardevRecovery.builder", RETRY_REVIEW: "cleardevRecovery.reviewer",
  RETRY_BUILDER_SESSION: "cleardevRecovery.builderSession", RETRY_CHECK: "cleardevRecovery.check",
  RETRY_STAGE: "cleardevRecovery.stage", RETRY_PLANNING_STEP: "cleardevRecovery.compile",
  REQUEST_EXTRA_PLANNING_ATTEMPT: "cleardevRecovery.extraRequest", CONTINUE_EXTRA_PLANNING_ATTEMPT: "cleardevRecovery.extraContinueCompilation",
  REQUEST_BUILDER_REPLACEMENT: "cleardevReplacement.request", CONTINUE_BUILDER_REPLACEMENT: "cleardevReplacement.continue",
} as const;
const unavailableKeys = {
  BUILDER_BUDGET_EXHAUSTED: "cleardevRecovery.budget", REVIEWER_BUDGET_EXHAUSTED: "cleardevRecovery.budget",
  RECOVERY_SESSION_UNAVAILABLE: "cleardevRecovery.sessionUnavailable", RECOVERY_NATIVE_IDENTITY_MISSING: "cleardevRecovery.nativeMissing",
  RESULT_NOT_SETTLED: "cleardevRecovery.unknown", ROLE_BUDGET_LIMIT: "cleardevRecovery.limit",
  MESSAGE_BUDGET_EXHAUSTED: "cleardevRecovery.messagesExhausted", MESSAGE_BUDGET_UNKNOWN: "cleardevRecovery.messagesUnknown",
  PLANNING_NOT_CURRENT: "cleardevFailure.planningSourceChanged", PLANNING_SOURCE_CHANGED: "cleardevFailure.planningSourceChanged",
  EXECUTION_NOT_CURRENT: "cleardevRecovery.stale", PREFLIGHT_OR_DELIVERY_REQUIRED: "cleardevRecovery.preflightRequired",
  RETRY_NOT_DUE: "cleardevRecovery.retryNotDue", CONTINUATION_REGISTERED: "cleardevRecovery.registered",
  BUILDER_RECHECK_REGISTERED: "cleardevRecheck.registered",
  COORDINATION_RECOVERY_REGISTERED: "cleardevRecovery.coordinationRegistered",
  PLANNER_COORDINATION_CONTEXT_CHANGED: "cleardevRecovery.sourceChanged",
  ORIGINAL_PLANNER_UNAVAILABLE: "cleardevRecovery.sessionUnavailable",
  STOPPED_CHECK_DECISION_PENDING: "cleardevRecovery.stoppedCheckPending",
  STOPPED_CHECK_DECISION_REJECTED: "cleardevRecovery.stoppedCheckRejected",
  CHECK_ATTEMPT_NOT_CURRENT: "cleardevRecovery.sourceChanged",
  CHECK_SOURCE_NOT_CURRENT: "cleardevRecovery.sourceChanged",
  EXTRA_COORDINATION_DECISION_PENDING: "cleardevRecovery.extraCoordinationPending",
  EXTRA_COORDINATION_DECISION_REJECTED: "cleardevRecovery.extraCoordinationRejected",
  EXTRA_ATTEMPT_DECISION_PENDING: "cleardevRecovery.extraPending",
  EXTRA_ATTEMPT_DECISION_REJECTED: "cleardevRecovery.extraRejected",
  EXTRA_ATTEMPT_CONSUMED: "cleardevRecovery.extraConsumed",
  BUILDER_REPLACEMENT_DECISION_PENDING: "cleardevReplacement.pending",
  BUILDER_REPLACEMENT_DECISION_REJECTED: "cleardevReplacement.rejected",
  BUILDER_REPLACEMENT_CONSUMED: "cleardevReplacement.consumed",
  BUILDER_REPLACED: "cleardevReplacement.consumed",
  BUILDER_REPLACEMENT_SOURCE_CHANGED: "cleardevRecovery.sourceChanged",
  BUILDER_LOSS_NOT_CONFIRMED: "cleardevReplacement.lossUnconfirmed",
  BUILDER_OPERATION_PENDING: "cleardevReplacement.operationPending",
  BUILDER_SESSION_BUSY: "cleardevRecheck.reason.BUILDER_SESSION_BUSY",
  DESKTOP_UNAVAILABLE: "cleardevDecision.desktopUnavailable",
} as const;
const knownAction = (action: string): action is keyof typeof actionKeys => Object.hasOwn(actionKeys, action);
const invalidBuilder = (option: Option) => option.action === "CONTINUE_BUILDER" && option.reason === "BUILDER_RESULT_INVALID";
const optionalSupplement = (option: Option) => planningOperation(option.action) || replacementOperation(option.action) || option.action === "RETRY_BUILDER_SESSION" || invalidBuilder(option);

export function recoverySummary(raw: string): string {
  try {
    const result: unknown = JSON.parse(raw);
    if (result && typeof result === "object") {
      if ("summary" in result && typeof result.summary === "string") return result.summary;
      if ("outputSummary" in result && typeof result.outputSummary === "string") return result.outputSummary;
    }
  } catch { /* Plain text is already readable. */ }
  return raw;
}

// A registered continuation uses the OLD request identity, not the current
// option's step ID. Missing or ambiguous history never grants a new request.
function registeredRequest(option: Option, history: Recovery[], executionRunId: string): Request | null {
  if (option.action === coordinationAction && option.unavailableReason === "COORDINATION_RECOVERY_REGISTERED") {
    const matches = history.filter((item) => item.action === option.action && item.targetId === option.targetId && item.executionRunId === executionRunId);
    if (matches.length !== 1 || !matches[0].id || !matches[0].stepId || !executionRunId || matches[0].successorId !== `${matches[0].stepId}:attempt:2` || matches[0].originalStatus !== "STOP" || matches[0].originalReason !== "PLANNER_COORDINATION_UNAVAILABLE" || !matches[0].bindingId || !matches[0].supplement?.trim()) return null;
    const item = matches[0];
    return { requestId: item.id, executionRunId, action: item.action, targetId: item.targetId, supplement: item.supplement };
  }
  if (option.action === replacementContinue && option.unavailableReason === "CONTINUATION_REGISTERED") {
    const matches = history.filter((item) => item.action === replacementContinue && item.targetId === option.targetId && item.executionRunId === executionRunId);
    if (matches.length !== 1 || !matches[0].id || !matches[0].successorId || !matches[0].executionRunId) return null;
    const item = matches[0];
    return { requestId: item.id, executionRunId: item.executionRunId, action: item.action, targetId: item.targetId, supplement: item.supplement };
  }
  if (option.action === "RETRY_BUILDER_SESSION" && option.unavailableReason === "BUILDER_RECHECK_REGISTERED") {
    const matches = history.filter((item) => item.action === option.action && item.targetId === option.targetId);
    if (matches.length !== 1 || !matches[0].id || !matches[0].executionRunId || !matches[0].stepId) return null;
    const item = matches[0];
    return { requestId: item.id, executionRunId: item.executionRunId, action: item.action, targetId: item.targetId, supplement: item.supplement };
  }
  if (option.action === extraPlanningContinue && option.unavailableReason === "CONTINUATION_REGISTERED") {
    const matches = history.filter((item) => item.action === extraPlanningContinue && item.targetId === option.targetId);
    if (matches.length !== 1 || !matches[0].id || !matches[0].successorId || matches[0].executionRunId) return null;
    const item = matches[0];
    return { requestId: item.id, executionRunId: "", action: item.action, targetId: item.targetId, supplement: item.supplement };
  }
  if (option.action !== planningAction || option.unavailableReason !== "CONTINUATION_REGISTERED") return null;
  const matches = history.filter((item) => item.action === planningAction && item.stepId === option.targetId);
  if (matches.length !== 1) return null;
  const item = matches[0];
  if (!item.id || !item.targetId || !item.successorId || item.executionRunId) return null;
  return { requestId: item.id, executionRunId: "", action: planningAction, targetId: item.targetId, supplement: item.supplement };
}
type Target = Pick<Request, "action" | "targetId" | "executionRunId">;
const sameTarget = (a: Target, b: Target) => a.action === b.action && a.targetId === b.targetId && a.executionRunId === b.executionRunId;
const sameHistory = (item: Recovery, body: Request) => item.id === body.requestId && sameTarget(item, body) && item.supplement === body.supplement;
// A different committed request may win the unique stopped target. That is
// authoritative resolution, not permission to resend the losing request.
const unresolvedRequests = (requests: Request[], history: Recovery[]) => requests.filter((body) => !history.some((item) =>
  sameHistory(item, body) || (item.id !== body.requestId && !!item.id && !!item.successorId && sameTarget(item, body))));
const extraStorageKey = (id: string) => `cleardev-extra-planning-requests:${id}`;
const replacementStorageKey = (id: string) => `cleardev-builder-replacement-requests:${id}`;
const extraRequestFields = ["requestId", "executionRunId", "action", "targetId", "supplement"];
function storedRecoveryRequest(value: unknown, replacement: boolean): value is Request {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const body = value as Record<string, unknown>;
  const fields = Object.keys(body);
  return fields.length === extraRequestFields.length && fields.every((field) => extraRequestFields.includes(field)) &&
    typeof body.requestId === "string" && body.requestId.trim().length > 0 && body.requestId.length <= (replacement ? 100 : 200) &&
    typeof body.executionRunId === "string" && (replacement ? body.executionRunId.trim().length > 0 && body.executionRunId.length <= 200 : body.executionRunId === "") &&
    typeof body.action === "string" && (replacement ? replacementOperation(body.action) : extraPlanningOperation(body.action)) &&
    typeof body.targetId === "string" && body.targetId.trim().length > 0 && body.targetId.length <= (replacement ? 600 : 512) &&
    typeof body.supplement === "string" && body.supplement.length <= (replacement ? 16000 : 4000);
}
const storedExtraRequest = (value: unknown): value is Request => storedRecoveryRequest(value, false);
const storedReplacementRequest = (value: unknown): value is Request => storedRecoveryRequest(value, true);
function readStoredRequests(key: string, valid: (value: unknown) => value is Request): Request[] {
  try {
    const encoded = sessionStorage.getItem(key);
    if (!encoded || encoded.length > 512 * 1024) return [];
    const decoded: unknown = JSON.parse(encoded);
    if (!Array.isArray(decoded) || decoded.length > 16) return [];
    const requests = decoded.filter(valid);
    // Ambiguous stored requests never choose an arbitrary payload for a target.
    return requests.filter((body, index) => !requests.some((other, otherIndex) => otherIndex !== index && sameTarget(body, other)));
  } catch { return []; }
}
function writeStoredRequests(key: string, requests: Request[], valid: (value: unknown) => value is Request) {
  try {
    const bodies = requests.filter(valid).slice(-16);
    if (bodies.length) sessionStorage.setItem(key, JSON.stringify(bodies));
    else sessionStorage.removeItem(key);
  } catch { /* Storage failure must not discard the exact request in the shared cache. */ }
}
function writePersistentRequests(id: string, action: string, requests: Request[]) {
  if (extraPlanningOperation(action)) writeStoredRequests(extraStorageKey(id), requests, storedExtraRequest);
  if (replacementOperation(action)) writeStoredRequests(replacementStorageKey(id), requests, storedReplacementRequest);
}

export function ClearDevWorkflowRecovery(props: Props) {
  // No draft, response or mutation from one requirement may leak to another.
  return <RecoveryPanel key={`${props.id}:${props.sourceKey ?? ""}:${props.completed ?? false}`} {...props} />;
}

function RecoveryPanel({ id, scope = "execution", current = true, completed = false, blocked = false, sourceKey, onOpenSession }: Props) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const queryKey = sourceKey === undefined ? ["cleardev-recoveries", id] : ["cleardev-recoveries", id, sourceKey];
  const requestKey = ["cleardev-recovery-pending-requests", id];
  const mutationKey = ["cleardev-recovery-write", id];
  const writes = useIsMutating({ mutationKey });
  const inFlight = useRef(false);
  const [draft, setDraft] = useState("");
  const [acceptedRequest, setAcceptedRequest] = useState<Request | null>(null);
  const acceptedAction = acceptedRequest?.action ?? null;
  const accepted = !!acceptedAction && acceptedAction !== extraPlanningRequest && acceptedAction !== extraCoordinationRequest && acceptedAction !== stoppedCheckRequest && !replacementOperation(acceptedAction);
  const query = useQuery({
    queryKey, enabled: hasTrustedApiBaseUrl(), refetchInterval: 5000, retry: false,
    queryFn: async ({ signal }) => {
      const result = await apiClient.GET("/api/v1/cleardev/requirements/{id}/recoveries", { params: { path: { id } }, signal });
      if (result.error) throw result.error;
      if (!result.data) throw new Error(t("cleardevRecovery.responseMissing"));
      return result.data;
    },
  });
  // Shared by the product card and workbench: a lost response cannot become a
  // different payload simply because the user changed panels or language.
  // Keep unknown requests per immutable target, not as a requirement-wide
  // lock. A new current step must not erase or be blocked by an older unknown
  // outcome; if the old target returns, its exact request remains available.
  const pending = useQuery<Request[]>({ queryKey: requestKey, queryFn: async () => [], enabled: false, initialData: () => [
    ...readStoredRequests(extraStorageKey(id), storedExtraRequest), ...readStoredRequests(replacementStorageKey(id), storedReplacementRequest),
  ], gcTime: Infinity });
  const history = query.data?.history ?? [];
  const options = query.data?.options ?? [];
  const now = useFreshnessClock();
  const diagnosis = query.data?.diagnosis;
  const diagnosisReady = !diagnosis || (diagnosis.current && !diagnosis.readError);
  const fresh = !completed && current && hasTrustedApiBaseUrl() && hasCurrentFacts(query, now) && !query.isFetching && diagnosisReady;
  const acceptedReplacementHandedOff = fresh && acceptedRequest?.action === replacementContinue &&
    query.data?.executionRunId === acceptedRequest.executionRunId && history.some((item) => sameHistory(item, acceptedRequest)) &&
    query.data.builderReplacements?.some((item) => item.state === "HANDED_OFF" &&
      item.executionRunId === acceptedRequest.executionRunId && item.targetId === acceptedRequest.targetId &&
      item.continueRequestId === acceptedRequest.requestId);
  const outstanding = fresh ? unresolvedRequests(pending.data, history) : pending.data;
  const pendingFor = (option: Option) => outstanding.find((body) => sameTarget(body, {
    action: option.action, targetId: option.targetId, executionRunId: query.data?.executionRunId ?? "",
  }));
  const refresh = async () => {
    await Promise.all([
      client.invalidateQueries({ queryKey: ["cleardev-recoveries", id] }),
      client.invalidateQueries({ queryKey: ["cleardev-operate", id] }),
      client.invalidateQueries({ queryKey: ["cleardev-products"] }),
      client.invalidateQueries({ queryKey: ["cleardev-progress"] }),
    ]);
  };
  const submit = useMutation({
    mutationKey, retry: false,
    mutationFn: async (body: Request) => {
      if (completed || !current || !hasTrustedApiBaseUrl()) throw new Error(t("cleardevRecovery.stale"));
      // A GET begun before this write must not overwrite its receipt.
      await client.cancelQueries({ queryKey: ["cleardev-recoveries", id] });
      const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/recoveries", { params: { path: { id } }, body });
      if (result.error) throw result.error;
      if (!result.data) throw new Error(t("cleardevRecovery.responseMissing"));
      return result.data;
    },
    onSuccess: (data, body) => {
      client.setQueryData(queryKey, data);
      const remaining = (client.getQueryData<Request[]>(requestKey) ?? []).filter((item) => item.requestId !== body.requestId);
      client.setQueryData(requestKey, remaining);
      writePersistentRequests(id, body.action, remaining);
      setDraft(""); setAcceptedRequest(body);
    },
    onSettled: async () => { try { await refresh(); } finally { inFlight.current = false; } },
  });
  const budget = useMutation({
    mutationKey, retry: false,
    mutationFn: async (option: Option) => {
      if (!current || !option.taskId || !hasTrustedApiBaseUrl() || !["BUILDER", "REVIEWER"].includes(option.role)) throw new Error(t("cleardevRecovery.stale"));
      const path = option.role === "BUILDER"
        ? "/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/builder-turn-authorizations"
        : "/api/v1/cleardev/requirements/{id}/development-tasks/{taskId}/review-budget-authorizations";
      const result = await apiClient.POST(path, { params: { path: { id, taskId: option.taskId } } });
      if (result.error) throw result.error;
      if (!result.data) throw new Error(t("cleardevRecovery.responseMissing"));
      return result.data;
    },
    onSettled: async () => { try { await refresh(); } finally { inFlight.current = false; } },
  });
  useEffect(() => {
    if (fresh && outstanding.length !== pending.data.length) {
      const confirmed = pending.data.filter((body) => history.some((item) => sameHistory(item, body))).at(-1);
      if (confirmed) setAcceptedRequest(confirmed);
      client.setQueryData(requestKey, outstanding);
      for (const body of pending.data.filter((item) => !outstanding.includes(item))) writePersistentRequests(id, body.action, outstanding);
      if (!writes) submit.reset();
    }
  }, [fresh, query.data, pending.data, client, id]); // eslint-disable-line react-hooks/exhaustive-deps
  const actionName = (action: string, reason?: string) => {
    if (action === planningAction) return t(scope === "discussion" ? "cleardevRecovery.discuss" : "cleardevRecovery.compile");
    if (action === extraPlanningContinue) return t(scope === "discussion" ? "cleardevRecovery.extraContinueDiscussion" : "cleardevRecovery.extraContinueCompilation");
    if (action === "CONTINUE_BUILDER" && reason === "BUILDER_RESULT_INVALID") return t("cleardevRecovery.invalidBuilder");
    return t(knownAction(action) ? actionKeys[action] : "cleardevRecovery.unsupported");
  };
  const unavailable = (reason: string) => t(Object.hasOwn(unavailableKeys, reason) ? unavailableKeys[reason as keyof typeof unavailableKeys] : "cleardevRecovery.unavailable");
  const start = (body: Request) => {
    if (!fresh || inFlight.current || client.isMutating({ mutationKey })) return;
    const remaining = unresolvedRequests(client.getQueryData<Request[]>(requestKey) ?? [], history);
    const saved = remaining.find((item) => sameTarget(item, body));
    if (saved && JSON.stringify(saved) !== JSON.stringify(body)) return;
    inFlight.current = true; setAcceptedRequest(null);
    const next = saved ? remaining : [...remaining, body];
    client.setQueryData(requestKey, next);
    writePersistentRequests(id, body.action, next);
    submit.mutate(body);
  };
  const startOption = (option: Option) => {
    if (!knownAction(option.action) || !fresh) return;
    const replay = registeredRequest(option, history, query.data?.executionRunId ?? "");
    if (replay) { start(replay); return; }
    if (option.unavailableReason || (planningOperation(option.action) ? !!query.data?.executionRunId : !query.data?.executionRunId)) return;
    const supplement = draft.trim() || (invalidBuilder(option) ? t("cleardevRecovery.defaultBuilderContext") : option.action === "RETRY_BUILDER_SESSION" ? t("cleardevRecheck.defaultContext") : "");
    if (!optionalSupplement(option) && !supplement) return;
    start({ requestId: crypto.randomUUID(), executionRunId: query.data?.executionRunId ?? "", action: option.action, targetId: option.targetId, supplement });
  };
  const retryCurrent = (body: Request) => query.data?.executionRunId === body.executionRunId && options.some((option) =>
    option.action === body.action && option.targetId === body.targetId && !option.unavailableReason && knownAction(option.action));
  const editable = options.filter((option) => knownAction(option.action) && !option.unavailableReason);
  const disabled = !fresh || writes > 0;
  const contextLocked = editable.some((option) => !!pendingFor(option));
  const historyDetails = history.length > 0 && <details className="mt-3"><summary className="cursor-pointer">{t("cleardevRecovery.history", { count: history.length })}</summary>
      {history.map((item) => <div className="mt-2 border-t pt-2" key={item.id}>
        <p>{item.action === replacementRequest ? t("cleardevReplacement.requestHistory") : item.action === replacementContinue ? t("cleardevReplacement.continueHistory") : item.action === extraPlanningRequest ? t("cleardevRecovery.extraRequestHistory") : item.action === extraPlanningContinue ? t("cleardevRecovery.extraContinueHistory") : item.action === planningAction ? t("cleardevRecovery.planningHistory") : actionName(item.action, item.originalReason)} · {new Date(item.createdAt).toLocaleString()}</p>
        <p className="mt-1 whitespace-pre-wrap break-words text-muted-foreground">{recoverySummary(item.originalSummary)}</p>
        {item.supplement && <p className="mt-1 whitespace-pre-wrap break-words">{item.supplement}</p>}
        <p className="mt-1 text-xs text-muted-foreground">{t(item.action === replacementRequest ? "cleardevReplacement.requestHistoryNotice" : item.action === extraPlanningRequest ? "cleardevRecovery.extraRequestHistoryNotice" : item.action === extraCoordinationRequest ? "cleardevRecovery.extraCoordinationHelp" : item.action === stoppedCheckRequest ? "cleardevRecovery.stoppedCheckHelp" : "cleardevRecovery.historyNotice")}</p>
      </div>)}
    </details>;
  // Completion is a separate presentation state, not a stale recovery source.
  if (completed) {
    if (query.isError) return <p className="text-sm text-muted-foreground">{t("cleardevRecovery.historyLoadError")}</p>;
    return history.length ? <section className="text-sm text-muted-foreground" data-testid="cleardev-recovery-history" data-recovery-requirement={id}>{historyDetails}</section> : null;
  }
  const refreshButton = <Button type="button" variant="ghost" size="sm" disabled={query.isFetching || !hasTrustedApiBaseUrl()} onClick={() => { void query.refetch(); }}>{t("cleardevRecovery.refresh")}</Button>;
  if (query.isError) return <section aria-label={t("cleardevRecovery.title")} data-recovery-requirement={id} className="rounded-lg border p-4 text-sm">
    <p role="alert" className="text-destructive">{t("cleardevRecovery.loadError", { error: apiErrorMessage(query.error) })}</p>{refreshButton}
  </section>;
  if (query.isPending) return blocked ? <p role="status">{t("cleardevRecovery.loading")}</p> : null;
  if (!options.length && !history.length && !query.data?.builderReplacements?.length && !acceptedAction && !blocked && !outstanding.length && !diagnosis?.issues.length && !diagnosis?.readError && !query.data?.failureHandling?.length) return null;
  return <section className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-4 text-sm" aria-label={t("cleardevRecovery.title")} data-testid="cleardev-workflow-recovery" data-recovery-requirement={id}>
    <div className="flex flex-wrap items-center justify-between gap-2"><h3 className="font-semibold">{t("cleardevRecovery.title")}</h3>{refreshButton}</div>
    <ClearDevBuilderSessionCheck checks={query.data?.builderSessionChecks ?? []} history={history} options={options} replacements={query.data?.builderReplacements ?? []} current={current && hasCurrentFacts(query, now)} />
    <ClearDevBuilderReplacement replacements={query.data?.builderReplacements ?? []} history={history} options={options} current={current && hasCurrentFacts(query, now) && diagnosisReady} executionRunId={query.data?.executionRunId ?? ""} requirementId={id} onOpenSession={onOpenSession} />
    <ClearDevFailureHandling items={query.data?.failureHandling} current={current && hasCurrentFacts(query, now) && diagnosisReady} />
    <ClearDevBlockerDiagnosis diagnosis={diagnosis} requirementId={id} current={current && hasCurrentFacts(query, now)}
      hasOperation={options.some((option) => knownAction(option.action) && (!option.unavailableReason || !!registeredRequest(option, history, query.data?.executionRunId ?? "") || ["BUILDER_BUDGET_EXHAUSTED", "REVIEWER_BUDGET_EXHAUSTED"].includes(option.unavailableReason)))} onOpenSession={onOpenSession} />
    {!current ? <p role="alert" className="mt-2">{t("cleardevRecovery.stale")}</p> : <>
      {options.length > 0 && <p className="mt-1 text-muted-foreground">{t(options.some((option) => replacementOperation(option.action)) ? "cleardevReplacement.explanation" : scope === "discussion" ? "cleardevRecovery.discussionHelp" : scope === "compilation" ? "cleardevRecovery.compilationHelp" : "cleardevRecovery.explanation")}</p>}
      {!options.length && blocked && !acceptedAction && !diagnosis?.issues.length && <p className="mt-2">{t("cleardevRecovery.noAction")}</p>}
      {editable.length > 0 && <label className="mt-3 block"><span>{t(editable.every(optionalSupplement) ? "cleardevRecovery.optionalContext" : "cleardevRecovery.supplement")}</span>
        <textarea className="mt-1 min-h-20 w-full rounded-md border bg-background p-2" maxLength={4000} value={draft} disabled={disabled || contextLocked} onChange={(event) => setDraft(event.target.value)} />
      </label>}
      {options.map((option) => {
        const replay = registeredRequest(option, history, query.data?.executionRunId ?? "");
        const known = knownAction(option.action);
        const registered = option.unavailableReason === "COORDINATION_RECOVERY_REGISTERED" || option.unavailableReason === "CONTINUATION_REGISTERED" || option.unavailableReason === "BUILDER_RECHECK_REGISTERED";
        const canBudget = known && !!option.taskId && ((option.role === "BUILDER" && option.unavailableReason === "BUILDER_BUDGET_EXHAUSTED") || (option.role === "REVIEWER" && option.unavailableReason === "REVIEWER_BUDGET_EXHAUSTED"));
        return <div key={`${option.action}:${option.targetId}`} className="mt-3 border-t pt-3">
          <p className="font-medium">{actionName(option.action, option.reason)}</p>
          {option.action === stoppedCheckRequest && <p className="mt-1 text-muted-foreground">{t("cleardevRecovery.stoppedCheckHelp")}</p>}
          {option.action === extraCoordinationRequest && <p className="mt-1 text-muted-foreground">{t("cleardevRecovery.extraCoordinationHelp")}</p>}
          {option.action === coordinationAction && <p className="mt-1 text-muted-foreground">{t("cleardevRecovery.coordinationHelp")}</p>}
          {option.action === extraPlanningRequest && <p className="mt-1 text-muted-foreground">{t("cleardevRecovery.extraRequestHelp")}</p>}
          {option.action === replacementRequest && <p className="mt-1 text-muted-foreground">{t("cleardevReplacement.requestHelp")}</p>}
          {option.action === replacementContinue && !option.unavailableReason && <p className="mt-1 text-muted-foreground">{t("cleardevReplacement.continueHelp")}</p>}
          {option.action === extraPlanningContinue && !["EXTRA_ATTEMPT_DECISION_PENDING", "EXTRA_ATTEMPT_DECISION_REJECTED", "EXTRA_ATTEMPT_CONSUMED"].includes(option.unavailableReason ?? "") && <p className="mt-1 text-muted-foreground">{t("cleardevRecovery.extraContinueHelp")}</p>}
          {option.action === "RETRY_BUILDER_SESSION" && <><p className="mt-1 text-muted-foreground">{t("cleardevRecheck.help")}</p><p className="mt-1 text-muted-foreground">{t("cleardevRecheck.defaultContext")}</p></>}
          {invalidBuilder(option) && <><p className="mt-1 text-muted-foreground">{t("cleardevRecovery.invalidBuilderHelp")}</p><p className="mt-1 text-muted-foreground">{t("cleardevRecovery.defaultBuilderContext")}</p></>}
          {option.summary && <p className="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words text-muted-foreground">{recoverySummary(option.summary)}</p>}
          {known && option.unavailableReason ? <p className="mt-2 text-amber-700 dark:text-amber-300">{registered && !replay ? t("cleardevRecovery.registrationMissing") : option.action === replacementContinue && registered ? t("cleardevReplacement.registered") : unavailable(option.unavailableReason)}</p> : null}
          {known && (!option.unavailableReason || replay) && !pendingFor(option) && <Button type="button" variant="outline" className="mt-2 h-auto whitespace-normal text-left" disabled={disabled || (!replay && !optionalSupplement(option) && !draft.trim()) || (planningOperation(option.action) ? !!query.data?.executionRunId : !query.data?.executionRunId)} onClick={() => startOption(option)}>{replay ? t(option.action === replacementContinue ? "cleardevReplacement.resumeRegistered" : "cleardevRecovery.resumeRegistered") : actionName(option.action, option.reason)}</Button>}
          {canBudget && <Button type="button" variant="outline" className="mt-2 h-auto whitespace-normal text-left" disabled={disabled || !!pendingFor(option)} onClick={() => { if (!fresh || inFlight.current || client.isMutating({ mutationKey })) return; inFlight.current = true; budget.mutate(option); }}>{t("cleardevRecovery.requestBudget")}</Button>}
          {(option.reason || option.unavailableReason) && <details className="mt-2 text-xs text-muted-foreground"><summary>{t("cleardevRecovery.technicalReason")}</summary><code className="break-all">{option.reason}{option.unavailableReason ? ` · ${option.unavailableReason}` : ""}</code></details>}
        </div>;
      })}
      {outstanding.map((body) => <div key={body.requestId} className="mt-3 border-t pt-3"><p>{t("cleardevRecovery.requestUnconfirmed")}</p>
        {retryCurrent(body) ? <Button type="button" variant="outline" className="mt-2" disabled={disabled} onClick={() => start(body)}>{t("cleardevRecovery.retryRequest")}</Button> : <p className="mt-1 text-muted-foreground">{t("cleardevRecovery.requestChanged")}</p>}
      </div>)}
      {budget.isError && <p role="alert" className="mt-2 text-destructive">{apiErrorMessage(budget.error)}</p>}
      {budget.isSuccess && <p role="status" className="mt-2">{t("cleardevRecovery.budgetPending")}</p>}
      {submit.isError && outstanding.some((body) => body.requestId === submit.variables?.requestId) && <p role="alert" className="mt-2 text-destructive">{apiErrorMessage(submit.error)}</p>}
      {accepted && !options.length && <p role="status" className="mt-2">{t("cleardevRecovery.accepted")}</p>}
      {acceptedAction === stoppedCheckRequest && !options.length && <p role="status" className="mt-2">{t("cleardevRecovery.stoppedCheckRecorded")}</p>}
      {acceptedAction === extraCoordinationRequest && !options.length && <p role="status" className="mt-2">{t("cleardevRecovery.extraCoordinationRecorded")}</p>}
      {acceptedAction === extraPlanningRequest && !options.length && <p role="status" className="mt-2">{t("cleardevRecovery.extraPending")}</p>}
      {acceptedAction === replacementRequest && !options.length && <p role="status" className="mt-2">{t("cleardevReplacement.pending")}</p>}
      {acceptedAction === replacementContinue && !options.length && !acceptedReplacementHandedOff && <p role="status" data-testid="cleardev-replacement-registered" className="mt-2">{t("cleardevReplacement.registered")}</p>}
    </>}
    {historyDetails}
  </section>;
}
