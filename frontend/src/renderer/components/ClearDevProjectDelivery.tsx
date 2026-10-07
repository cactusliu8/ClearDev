import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { hasCurrentFacts, useFreshnessClock } from "../lib/cleardev-freshness";
import { Button } from "./ui/button";

type Discussion = components["schemas"]["ClearDevProductDiscussionInput"];
type Preview = components["schemas"]["ClearDevResultPreviewView"];

// Uses the existing immutable product discussion/choice endpoint, not project
// configuration updates. The backend resolves and verifies delivery provenance.
export function ClearDevProjectDelivery({ productId, projectId, requirementId, preview }: {
	productId: string; projectId: string; requirementId: string; preview?: Preview;
}) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const now = useFreshnessClock();
	const key = ["cleardev-delivery-choice", productId];
	const product = useQuery({
		queryKey: key, enabled: hasTrustedApiBaseUrl(), retry: 1, refetchInterval: 3000,
		queryFn: async () => {
			const result = await apiClient.GET("/api/v1/cleardev/products/{id}", { params: { path: { id: productId } } });
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevDelivery.unavailable"));
			return result.data;
		},
	});
	const inFlight = useRef(false);
	const request = useRef<Discussion | null>(null);
	const choose = useMutation({
		retry: false,
		mutationFn: async (body: Discussion) => {
			// Re-read the actual result before a write, rather than accepting a
			// cached task-local SHA or silently selecting a newer candidate.
			const fresh = await apiClient.GET("/api/v1/cleardev/requirements/{id}/result-preview", { params: { path: { id: requirementId } } });
			if (fresh.error) throw fresh.error;
			if (!fresh.data || !["ready", "stopped"].includes(fresh.data.preview.state) ||
				fresh.data.candidateSha !== body.choice?.expectedBaseCommitSha || fresh.data.sourceBranch !== preview?.sourceBranch ||
				(product.data?.selection?.delivery && !fresh.data.dataReady)) {
				throw new Error(t("cleardevDelivery.changed"));
			}
			const result = await apiClient.POST("/api/v1/cleardev/products/{id}/discussions", { params: { path: { id: productId } }, body });
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevDelivery.receiptMissing"));
			return result.data;
		},
		onSettled: async () => {
			try {
				await Promise.all([
					client.invalidateQueries({ queryKey: key }),
					client.invalidateQueries({ queryKey: ["cleardev-products"] }),
				]);
			} finally { inFlight.current = false; }
		},
	});
	const current = hasCurrentFacts(product, now);
	const validPreview = !!preview && ["ready", "stopped"].includes(preview.preview.state) && !!preview.candidateSha && !!preview.sourceBranch;
	const selection = current ? product.data?.selection : undefined;
	const needsDataPreparation = !!selection?.delivery;
	const latest = current ? product.data?.discussions.at(-1) : undefined;
	const selected = validPreview && current && product.data?.sourceCurrent === true && selection?.aoProjectId === projectId &&
		selection.delivery?.requirementId === requirementId && selection.delivery.candidateSha === preview.candidateSha && selection.baseCommitSha === preview.candidateSha;
	const requestCurrent = !request.current || request.current.expectedPreviousId === latest?.id &&
		request.current.choice?.expectedBaseCommitSha === preview?.candidateSha && request.current.choice?.optionKey === selection?.option.key;
	const canChoose = validPreview && current && (!needsDataPreparation || preview?.dataReady === true) && product.data?.canDiscuss && product.data.remainingDiscussions > 0 &&
		!!selection && !!latest && !selected && requestCurrent;
	return <div className="flex flex-col gap-2" data-testid="cleardev-project-delivery-choice">
		<p className="text-sm text-muted-foreground">{t("cleardevDelivery.authority")}</p>
		<Button type="button" variant="outline" className="cleardev-action self-start" data-tone="accent" disabled={!canChoose || choose.isPending || choose.isSuccess} onClick={() => {
			if (inFlight.current || !canChoose || !latest || !selection || !preview) return;
			request.current ??= {
				requestId: crypto.randomUUID(), expectedPreviousId: latest.id,
				message: t("cleardevDelivery.message", { sha: preview.candidateSha }),
				choice: { optionKey: selection.option.key, aoProjectId: projectId, reason: t("cleardevDelivery.reason"), expectedBaseCommitSha: preview.candidateSha, deliveryRequirementId: requirementId },
			};
			inFlight.current = true;
			choose.mutate(request.current);
		}}>{t(choose.isError ? "cleardevDelivery.retry" : "cleardevDelivery.choose")}</Button>
		{needsDataPreparation && validPreview && !preview?.dataReady ? <p className="text-sm">{t("cleardevDelivery.prepareData")}</p> : null}
		{selected ? <p role="status">{t("cleardevDelivery.selected")}</p> : null}
		{current && !selected && !product.data?.canDiscuss ? <p className="text-sm">{t("cleardevDelivery.busy")}</p> : null}
		{choose.isError ? <p role="alert">{apiErrorMessage(choose.error)} {t("cleardevDelivery.failed")}</p> : null}
		{product.isError ? <p role="alert">{t("cleardevDelivery.baselineUnavailable", { error: apiErrorMessage(product.error) })}</p> : !current && product.data ? <p role="alert">{t("cleardevDelivery.stale")}</p> : null}
	</div>;
}
