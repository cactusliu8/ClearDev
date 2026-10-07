import { useRef, useState } from "react";
import { ChevronDown, ExternalLink, Mail, Play, SlidersHorizontal, Square } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { cleardevProgressQueryKey } from "../lib/cleardev-progress";
import { hasCurrentFacts, useFreshnessClock } from "../lib/cleardev-freshness";
import { aoBridge } from "../lib/bridge";
import { ClearDevDecisionReopen } from "./ClearDevDecisionReopen";
import { ClearDevPlannerAnswers } from "./ClearDevPlannerAnswers";
import { ClearDevProjectExecution } from "./ClearDevProjectExecution";
import { ClearDevProjectDelivery } from "./ClearDevProjectDelivery";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import "./cleardev-products.css";

type Requirement = components["schemas"]["ClearDevRequirementView"];

const previewStates: Record<string, MessageKey> = {
	stopped: "cleardevOperate.preview.stopped", starting: "cleardevOperate.preview.starting",
	ready: "cleardevOperate.preview.ready", stopping: "cleardevOperate.preview.stopping", failed: "cleardevOperate.preview.failed",
};

export function isLocalResultURL(value: string): boolean {
	try {
		const url = new URL(value);
		return url.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(url.hostname) && !url.username && !url.password;
	} catch { return false; }
}

export function ClearDevNewRequirement({ projectId, busy, collapsible = false }: { projectId: string; busy: boolean; collapsible?: boolean }) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const [name, setName] = useState("");
	const [text, setText] = useState("");
	const [submitted, setSubmitted] = useState(false);
	const inFlight = useRef(false);
	const create = useMutation({
		retry: false,
		mutationFn: async () => {
			const result = await apiClient.POST("/api/v1/cleardev/requirements/complex", {
				body: { aoProjectId: projectId, name: name.trim(), prdText: text.trim() },
			});
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevOperate.creationMissing"));
			return result.data;
		},
		onSuccess: () => { setName(""); setText(""); setSubmitted(true); },
		onSettled: () => { inFlight.current = false; void client.invalidateQueries({ queryKey: cleardevProgressQueryKey(projectId) }); },
	});
	const form = (
		<form className="mb-5 flex flex-col gap-3 rounded-lg border p-4" data-testid="cleardev-new-requirement" onSubmit={(event) => {
			event.preventDefault();
			if (inFlight.current || create.isError || busy || !name.trim() || !text.trim()) return;
			inFlight.current = true;
			setSubmitted(false);
			create.mutate();
		}}>
			<h2 className="text-sm font-semibold">{t("cleardevOperate.new")}</h2>
			<p className="text-sm text-muted-foreground">{t("cleardevOperate.contract")}</p>
			<label className="text-sm">{t("cleardevOperate.name")}<Input value={name} onChange={(event) => setName(event.target.value)} maxLength={200} required /></label>
			<label className="text-sm">{t("cleardevOperate.text")}<textarea className="mt-1 w-full rounded-md border bg-transparent p-2" value={text} onChange={(event) => setText(event.target.value)} rows={4} maxLength={20000} required /></label>
			<Button className="self-start" type="submit" disabled={busy || create.isPending || create.isError || !hasTrustedApiBaseUrl()}>{create.isPending ? t("cleardevOperate.sending") : t("cleardevOperate.submit")}</Button>
			{busy ? <p className="text-sm text-muted-foreground">{t("cleardevOperate.busy")}</p> : null}
			{submitted ? <p role="status">{t("cleardevOperate.created")}</p> : null}
			{create.isError ? <p role="alert">{apiErrorMessage(create.error)} {t("cleardevOperate.unknownSubmission")}</p> : null}
		</form>
	);
	return collapsible ? <details className="cleardev-disclosure" open={create.isError || submitted || undefined}>
		<summary><Mail className="mr-2 inline size-3.5" aria-hidden="true" />{t("cleardevOperate.new")}</summary>
		{form}
	</details> : form;
}

export function ClearDevRequirementActions({ projectId, requirementId, phase }: { projectId: string; requirementId: string; phase?: string }) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	return <div className="flex flex-col gap-3">
		<Button type="button" variant="outline" className="cleardev-action self-start" data-tone={phase === "COMPLETED" ? "success" : phase === "AWAITING_CONFIRMATION" || phase === "AWAITING_CLARIFICATION" || phase === "NEEDS_HUMAN" ? "warning" : "accent"} aria-expanded={open} onClick={() => setOpen(!open)}><SlidersHorizontal className="size-4" aria-hidden="true" />{t("cleardevOperate.actions")}<ChevronDown className={open ? "size-3.5 rotate-180" : "size-3.5"} aria-hidden="true" /></Button>
		{open ? <RequirementOperations projectId={projectId} requirementId={requirementId} phase={phase} /> : null}
	</div>;
}

function RequirementOperations({ projectId, requirementId, phase }: { projectId: string; requirementId: string; phase?: string }) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const key = ["cleardev-operate", requirementId];
	const detail = useQuery({
		queryKey: key,
		enabled: hasTrustedApiBaseUrl(),
		retry: 1,
		refetchInterval: phase === "COMPLETED" ? false : 3000,
		queryFn: async () => {
			const result = await apiClient.GET("/api/v1/cleardev/requirements/{id}", { params: { path: { id: requirementId } } });
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevOperate.unavailable"));
			return result.data;
		},
	});
	const now = useFreshnessClock();
	if (detail.isError) return <p role="alert">{apiErrorMessage(detail.error)}</p>;
	if (!detail.data) return <p>{t("cleardevOperate.loading")}</p>;
	const view = detail.data;
	const request = view.complexPlanning?.compilationRequests?.at(-1);
	const questions = view.complexPlanning?.questions?.filter((q) => q.compilationRequestId === request?.id) ?? [];
	const state = view.trustedProgress?.phase;
	const planning = view.complexPlanning;
	const latestPlan = planning?.plans?.at(-1);
	const validation = planning?.validations?.find((item) => item.planId === latestPlan?.id && item.planSha256 === latestPlan?.planSha256);
	const productClarification = planning?.productClarification;
	return <div className="flex flex-col gap-3">
		{view.trustedProgress.projectPlanning && hasCurrentFacts(detail, now) ? <ClearDevProjectExecution key={requirementId} view={view} onSaved={() => {
			void client.invalidateQueries({ queryKey: key });
			void client.invalidateQueries({ queryKey: cleardevProgressQueryKey(projectId) });
			void client.invalidateQueries({ queryKey: ["cleardev-products"] });
		}} /> : null}
		{view.trustedProgress.projectPlanning && !hasCurrentFacts(detail, now) ? <p role="alert">{t("cleardevOperate.stale")}</p> : null}
		{validation ? <p role="status" data-testid="cleardev-plan-validation">{t("cleardevOperate.planValidated")} <code>{validation.planId}</code></p> : null}
		{productClarification ? <section role="status" data-testid="cleardev-product-clarification" className="rounded-md border p-3">
			<h3 className="font-semibold">{t("cleardevOperate.productClarification")}</h3>
			<p>{productClarification.summary}</p>
			<ul className="list-disc pl-5">{productClarification.questions.map((question) => <li key={question}>{question}</li>)}</ul>
			<p className="mt-2 text-sm text-muted-foreground">{t("cleardevOperate.productClarificationAuthority")}</p>
   <ClearDevPlannerAnswers key={planning?.plannerClarification?.planId ?? requirementId} view={view} current={hasCurrentFacts(detail, now)} onSaved={() => {
 void client.invalidateQueries({queryKey:key});
 void client.invalidateQueries({queryKey:cleardevProgressQueryKey(projectId)});
 void client.invalidateQueries({queryKey:["cleardev-products"]});
 void client.invalidateQueries({queryKey:["cleardev-recoveries"]});
 }} />
		</section> : null}
		{!productClarification && planning?.plannerAnswerHistory?.length ? <ClearDevPlannerAnswers view={view} current={hasCurrentFacts(detail, now)} onSaved={() => {}} /> : null}
  {state === "AWAITING_CLARIFICATION" && request && questions.length ? <ClarifyForm key={request.id} view={view} onSaved={() => {
			void client.invalidateQueries({ queryKey: key });
			void client.invalidateQueries({ queryKey: cleardevProgressQueryKey(projectId) });
		}} /> : null}
		{state === "AWAITING_CONFIRMATION" ? <p role="status">{t("cleardevOperate.nativeApproval")}</p> : null}
 <ClearDevDecisionReopen requirementId={requirementId} current={hasCurrentFacts(detail,now)} />
		{state === "COMPLETED" ? <ResultControls projectId={projectId} requirementId={requirementId} productId={view.trustedProgress.projectPlanning?.productId} /> : <p className="text-sm text-muted-foreground">{t("cleardevOperate.notCompleted")}</p>}
	</div>;
}

function ClarifyForm({ view, onSaved }: { view: Requirement; onSaved: () => void }) {
	const { t } = useTranslation();
	const request = view.complexPlanning!.compilationRequests!.at(-1)!;
	const questions = view.complexPlanning!.questions!.filter((q) => q.compilationRequestId === request.id);
	const [answers, setAnswers] = useState<Record<string, string>>({});
	const send = useMutation({
		retry: false,
		mutationFn: async () => {
			const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/complex-clarifications", {
				params: { path: { id: view.requirement.id! } },
				body: { compilationRequestId: request.id!, clarificationRound: request.clarificationRound!, answers: questions.map((q) => ({ questionKey: q.questionKey!, text: answers[q.questionKey!]!.trim() })) },
			});
			if (result.error) throw result.error;
			return result.data;
		},
		onSuccess: onSaved,
	});
	return <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); if (!send.isPending && !send.isSuccess) send.mutate(); }}>
		{questions.map((q) => <label key={q.questionKey} className="text-sm">{q.text}<textarea className="mt-1 w-full rounded-md border bg-transparent p-2" required rows={2} value={answers[q.questionKey!] ?? ""} onChange={(event) => setAnswers({ ...answers, [q.questionKey!]: event.target.value })} /></label>)}
		<Button type="submit" className="self-start" disabled={send.isPending || send.isSuccess || questions.some((q) => !answers[q.questionKey!]?.trim())}>{t("cleardevOperate.answer")}</Button>
		{send.isError ? <p role="alert">{apiErrorMessage(send.error)}</p> : null}
	</form>;
}

function ResultControls({ projectId, requirementId, productId }: { projectId: string; requirementId: string; productId?: string }) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const key = ["cleardev-result-preview", requirementId];
	const [openError, setOpenError] = useState("");
	const [opening, setOpening] = useState(false);
	const baselineInFlight = useRef(false);
	const previewActionInFlight = useRef(false);
	const now = useFreshnessClock();
	const project = useQuery({
		queryKey: ["project", projectId], retry: 1, refetchInterval: 5000,
		queryFn: async () => {
			const result = await apiClient.GET("/api/v1/projects/{id}", { params: { path: { id: projectId } } });
			if (result.error) throw result.error;
			if (!result.data || result.data.status !== "ok" || "resolveError" in result.data.project) throw new Error(t("cleardevOperate.projectUnavailable"));
			return result.data.project;
		},
	});
	const status = useQuery({
		queryKey: key, retry: 1, refetchInterval: 5000,
		queryFn: async () => {
			const result = await apiClient.GET("/api/v1/cleardev/requirements/{id}/result-preview", { params: { path: { id: requirementId } } });
			if (result.error) throw result.error;
			return result.data;
		},
	});
	const action = useMutation({
		retry: false,
		mutationFn: async (stop: boolean) => {
			const options = { params: { path: { id: requirementId } } };
			const result = stop ? await apiClient.DELETE("/api/v1/cleardev/requirements/{id}/result-preview", options) : await apiClient.POST("/api/v1/cleardev/requirements/{id}/result-preview", options);
			if (result.error) throw result.error;
			return result.data;
		},
		onSuccess: () => { setOpenError(""); },
		onSettled: async () => {
			// A stop receipt confirms shutdown, not that the delivered source is valid.
			try { await client.invalidateQueries({ queryKey: key }); }
			finally { previewActionInFlight.current = false; }
		},
	});
	const requestPreviewAction = (stop: boolean) => {
		if (previewActionInFlight.current) return;
		previewActionInFlight.current = true;
		action.mutate(stop);
	};
	const selectBaseline = useMutation({
		retry: false,
		mutationFn: async () => {
			const fresh = await apiClient.GET("/api/v1/cleardev/requirements/{id}/result-preview", { params: { path: { id: requirementId } } });
			if (fresh.error) throw fresh.error;
			const delivered = fresh.data;
			if (!delivered || !["ready", "stopped"].includes(delivered.preview.state) || !delivered.sourceBranch ||
				delivered.candidateSha !== status.data?.candidateSha || delivered.sourceBranch !== status.data.sourceBranch) {
				throw new Error(t("cleardevOperate.deliveryChanged"));
			}
			const projectResult = await apiClient.GET("/api/v1/projects/{id}", { params: { path: { id: projectId } } });
			if (projectResult.error) throw projectResult.error;
			if (!projectResult.data || projectResult.data.status !== "ok" || "resolveError" in projectResult.data.project || projectResult.data.project.kind !== "single_repo") {
				throw new Error(t("cleardevOperate.projectUnavailable"));
			}
			const config = projectResult.data.project.config ?? {};
			if (config.defaultBranch === delivered.sourceBranch) return;
			const saved = await apiClient.PUT("/api/v1/projects/{id}", {
				params: { path: { id: projectId } }, body: { displayName: projectResult.data.project.name, config: { ...config, defaultBranch: delivered.sourceBranch } },
			});
			if (saved.error) throw saved.error;
		},
		onSuccess: () => {
			void client.invalidateQueries({ queryKey: ["project", projectId] });
			void client.invalidateQueries({ queryKey: ["cleardev-products", projectId] });
		},
		onSettled: () => { baselineInFlight.current = false; },
	});
	const statusCurrent = !action.isPending && hasCurrentFacts(status, now);
	const projectCurrent = hasCurrentFacts(project, now);
	const preview = statusCurrent ? status.data?.preview : undefined;
	const previewStateLabel = preview?.state ? (previewStates[preview.state] ? t(previewStates[preview.state]) : preview.state) : t("cleardevOperate.loading");
	const ready = preview?.state === "ready" && isLocalResultURL(preview.url ?? "");
	const selectableBaseline = statusCurrent && projectCurrent && ["ready", "stopped"].includes(preview?.state ?? "") && !!status.data?.candidateSha && !!status.data?.sourceBranch;
	// Stopping is safe even if the delivery source changed. A successful stop
	// receipt prevents repeat calls while a failing status query remains stale.
	const stopAvailable = statusCurrent ? !!preview && preview.state !== "stopped" :
		(status.isError || !!status.data) && action.data?.preview.state !== "stopped";
	return <div className="flex flex-col gap-2" data-testid="cleardev-result-controls">
		<p className="text-sm">{t("cleardevOperate.resultData")}</p>
		<details className="cleardev-disclosure"><summary>{t("cleardevProduct.technicalDetails")}</summary>
			<code className="block break-all text-xs">{statusCurrent ? status.data?.candidateSha : null}</code>
			<p className="text-sm">{t("cleardevOperate.nextBaseline")}</p>
			<code className="block break-all text-xs" data-testid="cleardev-result-branch">{statusCurrent ? status.data?.sourceBranch : null}</code>
		</details>
		<p role="status">{previewStateLabel}{preview?.error ? ` · ${preview.error}` : ""}</p>
		<div className="flex flex-wrap gap-2">
			<Button type="button" className="cleardev-primary" disabled={!statusCurrent || action.isPending || ready} onClick={(event) => {
				if (event.detail > 1) return;
				requestPreviewAction(false);
			}}><Play className="size-3.5" aria-hidden="true" />{t("cleardevOperate.startResult")}</Button>
			<Button type="button" variant="outline" className="cleardev-action" data-tone="success" disabled={!ready || action.isPending || opening} onClick={async () => {
				setOpening(true);
				setOpenError("");
				try {
					const fresh = await status.refetch();
					if (fresh.error) throw fresh.error;
					const current = fresh.data?.preview;
					if (current?.state !== "ready" || !isLocalResultURL(current.url ?? "")) {
						setOpenError(t("cleardevOperate.notCompleted"));
						return;
					}
					await aoBridge.app.openExternal(current.url!);
				} catch (error) { setOpenError(apiErrorMessage(error)); }
				finally { setOpening(false); }
			}}><ExternalLink className="size-3.5" aria-hidden="true" />{t("cleardevOperate.openResult")}</Button>
			<Button type="button" variant="outline" disabled={action.isPending || !stopAvailable} onClick={(event) => {
				if (event.detail > 1) return;
				requestPreviewAction(true);
			}}><Square className="size-3.5" aria-hidden="true" />{t("cleardevOperate.stopResult")}</Button>
			{!productId ? <Button type="button" variant="outline" disabled={!selectableBaseline || selectBaseline.isPending || project.data?.config?.defaultBranch === status.data?.sourceBranch} onClick={() => {
				if (baselineInFlight.current) return;
				baselineInFlight.current = true;
				selectBaseline.mutate();
			}}>{t("cleardevDelivery.choose")}</Button> : null}
		</div>
		{productId ? <ClearDevProjectDelivery productId={productId} projectId={projectId} requirementId={requirementId} preview={statusCurrent ? status.data : undefined} /> : null}
		{!productId && projectCurrent && statusCurrent && project.data?.config?.defaultBranch === status.data?.sourceBranch && status.data?.sourceBranch ? <p className="text-sm" role="status">{t("cleardevOperate.selected")}</p> : null}
		{selectBaseline.isError ? <p role="alert">{apiErrorMessage(selectBaseline.error)}</p> : null}
		{ready ? <code className="text-xs">{preview?.url}</code> : null}
		{action.isError || status.isError || openError ? <p role="alert">{openError || apiErrorMessage(action.error ?? status.error)}</p> : null}
		{!statusCurrent && stopAvailable ? <p className="text-sm text-muted-foreground">{t("cleardevOperate.resultUnavailable")}</p> : null}
		{project.isError ? <p role="alert">{t("cleardevOperate.baselineUnavailable", { error: apiErrorMessage(project.error) })}</p> : null}
		{!status.isError && status.data && !statusCurrent ? <p role="alert">{t("cleardevOperate.deliveryStale")}</p> : null}
		{!project.isError && project.data && !projectCurrent ? <p role="alert">{t("cleardevOperate.baselineStale")}</p> : null}
	</div>;
}
