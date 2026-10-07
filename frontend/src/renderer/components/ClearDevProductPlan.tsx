import { useRef } from "react";
import { useMutation } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import {
  apiClient,
  apiErrorMessage,
  hasTrustedApiBaseUrl,
} from "../lib/api-client";
import { Button } from "./ui/button";

import { ClearDevDecisionReopen } from "./ClearDevDecisionReopen";

type Product = components["schemas"]["ClearDevProductView"];

// This requests the existing private desktop decision; it never submits approval.
export function ClearDevProductPlan({
  product,
  onSaved,
}: {
  product: Product;
  onSaved: () => void;
}) {
  const { t } = useTranslation();
  const inFlight = useRef(false);
  const stage = product.stages.find((s) => s.current)?.stage;
  const authorization = product.automation;
  const active =
    authorization?.status === "RESOLVED" &&
    authorization.decision === "APPROVE";
  const request = useMutation({
    retry: false,
    throwOnError: false,
    mutationFn: async () => {
      if (!stage) throw new Error(t("cleardevAutoPlan.unavailable"));
      const result = await apiClient.POST(
        "/api/v1/cleardev/products/{id}/stages/{stageId}/prepare",
        {
          params: { path: { id: product.goal.id, stageId: stage.id } },
          body: { definitionSha256: stage.definitionSha256, automatic: true },
        },
      );
      if (result.error) throw result.error;
      if (!result.data) throw new Error(t("cleardevAutoPlan.unavailable"));
      return result.data;
    },
    onSettled: () => {
      inFlight.current = false;
      onSaved();
    },
  });
  if (!stage?.definition.executionBasis) return null;
  return (
    <section
      className="flex flex-col gap-2 rounded-lg border p-3"
      data-testid="cleardev-product-plan"
    >
      <p className="font-medium">{t("cleardevAutoPlan.title")}</p>
      <p className="text-sm text-muted-foreground">
        {t("cleardevAutoPlan.scope")}
      </p>
      {active ? (
        <p role="status">{t("cleardevAutoPlan.active")}</p>
      ) : authorization ? (
        <p role="status">
          {t(
            authorization.status === "PENDING"
              ? "cleardevAutoPlan.pending"
              : "cleardevAutoPlan.inactive",
          )}
        </p>
      ) : (
        <Button
          type="button"
          className="self-start"
          disabled={
            !hasTrustedApiBaseUrl() ||
            !["READY", "FROZEN"].includes(product.phase) ||
            request.isPending ||
            request.isSuccess
          }
          onClick={() => {
            if (inFlight.current) return;
            inFlight.current = true;
            request.mutate();
          }}
        >
          {t(
            request.isError
              ? "cleardevProduct.retry"
              : "cleardevAutoPlan.confirm",
          )}
        </Button>
      )}
      {request.isSuccess && !authorization ? (
        <p role="status">{t("cleardevAutoPlan.pending")}</p>
      ) : null}
      {active && authorization?.reason ? (
        <>
          <p role="alert">
            {authorization.reason} ({authorization.reasonCode})
          </p>
          <Button
            type="button"
            className="self-start"
            disabled={!hasTrustedApiBaseUrl() || request.isPending}
            onClick={() => {
              if (inFlight.current) return;
              inFlight.current = true;
              request.mutate();
            }}
          >
            {t("cleardevProduct.retry")}
          </Button>
        </>
      ) : null}
      {authorization?.status === "PENDING" ? (
        <ClearDevDecisionReopen
          requirementId={product.goal.id}
          current={true}
        />
      ) : null}
      {request.isError ? (
        <p role="alert">{apiErrorMessage(request.error)}</p>
      ) : null}
    </section>
  );
}
