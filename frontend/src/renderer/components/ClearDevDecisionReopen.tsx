import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";
import {
  apiClient,
  apiErrorMessage,
  hasTrustedApiBaseUrl,
} from "../lib/api-client";
import { hasCurrentFacts, useFreshnessClock } from "../lib/cleardev-freshness";
import { Button } from "./ui/button";

type Request = components["schemas"]["ClearDevDecisionReopenRequest"];
const pendingKey = (id: string) =>
  ["cleardev-decision-reopen-pending", id] as const;
const storageKey = (id: string) => `cleardev-decision-reopens:${id}`;
const sameTarget = (a: Request, b: Request) =>
  a.decisionRequestId === b.decisionRequestId &&
  a.contentSha256 === b.contentSha256 &&
  a.previousDispatchId === b.previousDispatchId;
const reasons: Record<string, MessageKey> = {
  DESKTOP_UNAVAILABLE: "cleardevDecision.desktopUnavailable",
  DECISION_NOT_CURRENT: "cleardevDecision.changed",
  DECISION_FACTS_UNAVAILABLE: "cleardevDecision.unavailable",
  DECISION_NOT_DISPLAYED: "cleardevDecision.first",
  DECISION_WINDOW_OPEN: "cleardevDecision.open",
  REOPEN_REGISTERED: "cleardevDecision.registered",
  DECISION_RESOLVED: "cleardevDecision.resolved",
};
const titles: Record<string, MessageKey> = {
  CONFIRM_PRODUCT_PLAN: "humanDecision.confirmProductPlan",
  CONFIRM_REQUIREMENT_VERSION: "humanDecision.confirmRequirement",
  APPROVE_DIRECTION_CHANGE: "humanDecision.confirmDirection",
  AUTHORIZE_EXTRA_BUILDER_TURN: "humanDecision.confirmRecovery",
  AUTHORIZE_PLANNING_REVIEW_RECOVERY: "humanDecision.confirmRecovery",
  AUTHORIZE_FINAL_REVIEW_EVIDENCE_RECHECK: "humanDecision.confirmRecovery",
  AUTHORIZE_EXTRA_REVIEW_BUDGET: "humanDecision.confirmRecovery",
  AUTHORIZE_EXTRA_PLANNING_ATTEMPT: "humanDecision.confirmExtraPlanningAttempt",
  AUTHORIZE_BUILDER_REPLACEMENT: "humanDecision.confirmBuilderReplacement",
};
const outcomes: Record<string, MessageKey> = {
  EXPIRED: "cleardevDecision.expired",
  DISCONNECTED: "cleardevDecision.disconnected",
  LATER: "cleardevDecision.later",
  APPROVE: "cleardevDecision.approved",
  REJECT: "cleardevDecision.rejected",
  INVALID: "cleardevDecision.changed",
};
function readPending(id: string): Request[] {
  try {
    const raw: unknown = JSON.parse(
      sessionStorage.getItem(storageKey(id)) ?? "[]",
    );
    if (!Array.isArray(raw)) return [];
    return raw
      .filter(
        (r): r is Request =>
          r &&
          [
            r.requestId,
            r.decisionRequestId,
            r.contentSha256,
            r.previousDispatchId,
          ].every(
            (v) => typeof v === "string" && v.length > 0 && v.length <= 200,
          ),
      )
      .slice(-16);
  } catch {
    return [];
  }
}
function writePending(id: string, values: Request[]) {
  try {
    sessionStorage.setItem(storageKey(id), JSON.stringify(values.slice(-16)));
  } catch {
    /* The shared query cache retains the original request in this app session. */
  }
}

export function ClearDevDecisionReopen({
  requirementId: id,
  current,
}: {
  requirementId: string;
  current: boolean;
}) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const [, setRevision] = useState(0);
  const key = ["cleardev-decision-displays", id];
  const mutationKey = ["cleardev-decision-reopen-write", id];
  const facts = useQuery({
    queryKey: key,
    enabled: hasTrustedApiBaseUrl(),
    retry: 1,
    refetchInterval: 2000,
    queryFn: async () => {
      const result = await apiClient.GET(
        "/api/v1/cleardev/requirements/{id}/decision-displays",
        { params: { path: { id } } },
      );
      if (result.error) throw result.error;
      if (!result.data) throw new Error(t("cleardevDecision.unavailable"));
      return result.data;
    },
  });
  const now = useFreshnessClock();
  const save = useMutation({
    mutationKey,
    retry: false,
    mutationFn: async (body: Request) => {
      await client.cancelQueries({ queryKey: key });
      const result = await apiClient.POST(
        "/api/v1/cleardev/requirements/{id}/decision-displays",
        { params: { path: { id } }, body },
      );
      if (result.error)
        throw Object.assign(new Error(apiErrorMessage(result.error)), {
          status: result.response?.status,
        });
      if (!result.data) throw new Error(t("cleardevDecision.unknown"));
      return result.data;
    },
    onSuccess: (data, body) => {
      client.setQueryData(key, data);
      const remaining = (
        client.getQueryData<Request[]>(pendingKey(id)) ?? readPending(id)
      ).filter((r) => !sameTarget(r, body));
      client.setQueryData(pendingKey(id), remaining);
      writePending(id, remaining);
    },
    onSettled: () => {
      setRevision((v) => v + 1);
      void client.invalidateQueries({ queryKey: key });
    },
  });
  if (facts.isError)
    return <p role="alert">{t("cleardevDecision.unavailable")}</p>;
  if (!facts.data?.items?.length) return null;
  const available =
    current && hasTrustedApiBaseUrl() && hasCurrentFacts(facts, now);
  return (
    <section
      className="rounded-md border p-3"
      data-testid="cleardev-decision-reopen"
    >
      <h3 className="font-semibold">{t("cleardevDecision.title")}</h3>
      <p className="text-sm text-muted-foreground">
        {t("cleardevDecision.help")}
      </p>
      <p role="status">
        {t(
          facts.data.connected
            ? "cleardevDecision.connected"
            : "cleardevDecision.waitingConnection",
        )}
      </p>
      {facts.data.items.map((item) => {
        const previous = item.dispatches?.at(-1);
        const target: Request = {
          requestId: "",
          decisionRequestId: item.decisionRequestId,
          contentSha256: item.contentSha256,
          previousDispatchId: previous?.id ?? "",
        };
        const pending =
          client.getQueryData<Request[]>(pendingKey(id)) ?? readPending(id);
        const unknown = pending.find((r) => sameTarget(r, target));
        return (
          <div key={item.decisionRequestId} className="mt-3 border-t pt-2">
            <p title={item.title}>
              {titles[item.decisionKind]
                ? t(titles[item.decisionKind])
                : item.title}
            </p>
            {previous ? (
              <p>
                {t("cleardevDecision.last")}:{" "}
                {t(
                  outcomes[
                    previous.outcome ||
                      (Date.parse(previous.expiresAt) <= now ? "EXPIRED" : "")
                  ] ?? "cleardevDecision.open",
                )}
              </p>
            ) : null}
            {item.reasonCode ? (
              <p role="status">
                {t(reasons[item.reasonCode] ?? "cleardevDecision.changed")}
              </p>
            ) : null}
            {!available ? (
              <p role="alert">{t("cleardevOperate.stale")}</p>
            ) : null}
            {unknown && item.canReopen ? (
              <p role="alert">{t("cleardevDecision.unknown")}</p>
            ) : null}
            {item.pending ? (
              <Button
                type="button"
                disabled={!available || !item.canReopen || save.isPending}
                onClick={() => {
                  if (
                    !available ||
                    !item.canReopen ||
                    save.isPending ||
                    client.isMutating({ mutationKey })
                  )
                    return;
                  const latest =
                    client.getQueryData<Request[]>(pendingKey(id)) ??
                    readPending(id);
                  const body = latest.find((r) => sameTarget(r, target)) ?? {
                    ...target,
                    requestId: crypto.randomUUID(),
                  };
                  const next = [
                    ...latest.filter((r) => !sameTarget(r, body)),
                    body,
                  ].slice(-16);
                  client.setQueryData(pendingKey(id), next);
                  writePending(id, next);
                  setRevision((v) => v + 1);
                  save.mutate(body);
                }}
              >
                {t(
                  save.isPending
                    ? "cleardevDecision.sending"
                    : "cleardevDecision.reopen",
                )}
              </Button>
            ) : null}
            {item.reopens?.length ? (
              <details>
                <summary>{t("cleardevDecision.history")}</summary>
                <ul>
                  {item.reopens.map((r) => (
                    <li key={r.requestId}>
                      <code>{r.requestId}</code> · {r.createdAt}
                    </li>
                  ))}
                </ul>
              </details>
            ) : null}
          </div>
        );
      })}
      {save.error ? <p role="alert">{apiErrorMessage(save.error)}</p> : null}
    </section>
  );
}
