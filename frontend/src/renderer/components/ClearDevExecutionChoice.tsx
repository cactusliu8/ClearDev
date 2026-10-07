import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";
import { agentModelsQueryKey, agentModelsQueryOptions, refreshAgentModels } from "../hooks/useAgentModelsQuery";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { useFreshnessClock } from "../lib/cleardev-freshness";
import { AgentModelCombobox } from "./settings/AgentModelCombobox";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

export type ExecutionChoice = components["schemas"]["ClearDevExecutionConfig"];
export type ExecutionView = components["schemas"]["ClearDevExecutionChoiceView"];
type Preflight = components["schemas"]["ClearDevControlledPreflightView"];

export function validExecutionChoice(choice: ExecutionChoice): boolean {
	const model = choice.model.trim();
	if (!["codex", "opencode"].includes(choice.agent) || !model || model.length > 240 || /\s/u.test(model) || [...model].some((char) => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)) return false;
	if (choice.agent === "opencode") return /^[^/]+\/.+$/u.test(model) && (!choice.effort || (choice.effort.length <= 64 && !/\s/u.test(choice.effort)));
	return true;
}

const toolName = (agent: string) => agent === "opencode" ? "OpenCode" : agent === "codex" ? "Codex" : "—";

export function ClearDevExecutionFields({ projectId, value, onChange, disabled = false, toolLocked = false }: {
	projectId: string; value: ExecutionChoice; onChange: (value: ExecutionChoice) => void; disabled?: boolean; toolLocked?: boolean;
}) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const catalog = useQuery({ ...agentModelsQueryOptions(value.agent, projectId), enabled: hasTrustedApiBaseUrl() && !disabled && ["codex", "opencode"].includes(value.agent) });
	const efforts = catalog.data?.models?.find((model) => model.id === value.model)?.efforts;
	const selectModel = (model: string) => onChange({ ...value, model, effort: undefined });
	const refresh = useMutation({
		retry: false,
		mutationFn: (selection: { agent: string; projectId: string }) => refreshAgentModels(selection.agent, selection.projectId),
		onSuccess: (result, selection) => client.setQueryData(agentModelsQueryKey(selection.agent, selection.projectId), result),
	});
	return <fieldset className="flex min-w-0 flex-col gap-3 rounded-md border p-3" disabled={disabled}>
		<legend className="px-1 text-sm font-semibold">{t("cleardevExecution.toolTitle")}</legend>
		<div className="grid min-w-0 gap-3 sm:grid-cols-[minmax(8rem,0.4fr)_minmax(0,1fr)]">
			<div className="text-sm"><span>{t("cleardevExecution.tool")}</span>
				<Select value={value.agent} disabled={disabled || toolLocked} onValueChange={(agent) => onChange({ agent, model: "" })}>
					<SelectTrigger className="mt-1 w-full" aria-label={t("cleardevExecution.tool")}><SelectValue /></SelectTrigger>
					<SelectContent><SelectItem value="codex">Codex</SelectItem><SelectItem value="opencode">OpenCode</SelectItem></SelectContent>
				</Select>
			</div>
			<label className="min-w-0 text-sm">{t("cleardevExecution.model")}
				<Input className="mt-1" value={value.model} maxLength={240} required autoComplete="off" spellCheck={false} placeholder={value.agent === "opencode" ? "provider/model" : t("cleardevExecution.model")} onChange={(event) => selectModel(event.target.value)} />
			</label>
		</div>
		<div className="flex flex-wrap items-center gap-2">
			{catalog.data?.models?.length ? <AgentModelCombobox aria-label={t("cleardevExecution.model")} value={value.model} models={catalog.data.models} allowCustom={true} disabled={disabled} emptyLabel={t("cleardevExecution.modelRequired")} triggerLabel={t("settings.models.browse")} menuAlign="start" recentScope={`cleardev:${value.agent}`} onChange={selectModel} onCustom={selectModel} /> : null}
			<Button type="button" size="sm" variant="outline" disabled={disabled || refresh.isPending || catalog.isFetching} onClick={() => refresh.mutate({ agent: value.agent, projectId })}>{t("settings.models.refreshList")}</Button>
			{catalog.isFetching ? <span className="text-xs text-muted-foreground">{t("settings.models.loading")}</span> : null}
		</div>
        <label className="text-sm">{t("cleardevExecution.effort")}
          <select aria-label={t("cleardevExecution.effort")} className="mt-1 block w-full rounded border bg-background p-2" disabled={disabled || !efforts?.length} value={value.effort ?? ""} onChange={(event) => onChange({ ...value, effort: event.target.value || undefined })}>
            <option value="">{t("cleardevExecution.effortDefault")}</option>
            {value.effort && !efforts?.includes(value.effort) ? <option value={value.effort} disabled>{value.effort} — {t("cleardevExecution.effortUnavailable")}</option> : null}
            {efforts?.map((effort) => <option key={effort} value={effort}>{effort}</option>)}
          </select>
        </label>
        <p className="text-xs text-muted-foreground">{t("cleardevExecution.effortHint")}</p>
		{catalog.isError || refresh.isError || catalog.data?.stale ? <p role="alert" className="text-sm text-error">{t("settings.models.loadFailed")} {t("cleardevExecution.catalogHint")}</p> : null}
		<p className="text-xs text-muted-foreground">{value.agent === "opencode" ? t("cleardevExecution.modelHint") : t("cleardevExecution.modelRequired")}</p>
		<p className="text-xs text-muted-foreground">{t("cleardevExecution.rules")}</p>
	</fieldset>;
}

export function ClearDevExecutionSummary({ execution }: { execution?: ExecutionView }) {
	const { t } = useTranslation();
	if (!execution) return <p role="status" className="text-sm text-muted-foreground">{t("cleardevExecution.unknown")}</p>;
	return <div className="flex flex-col gap-1 text-sm" data-testid="cleardev-execution-choice">
		<p><span className="font-medium">{t("cleardevExecution.tool")}：</span>{toolName(execution.agent)}<span className="ml-3 font-medium">{t("cleardevExecution.model")}：</span><span className="break-all">{execution.model || "—"}</span></p>
		<p className="text-sm">{t("cleardevExecution.effort")}：{execution.effort || t("cleardevExecution.effortDefault")}</p>
		<p className="text-xs text-muted-foreground">{t(!execution.known ? "cleardevExecution.unknown" : execution.legacy ? "cleardevExecution.legacy" : execution.modelLocked ? "cleardevExecution.locked" : "cleardevExecution.rules")}</p>
	</div>;
}

const reasonMessages: Record<string, MessageKey> = {
	EXECUTION_TOOL_NOT_INSTALLED: "cleardevExecution.missingInstall",
	EXECUTION_TOOL_CONFIG_INVALID: "cleardevExecution.invalidConfig",
	EXECUTION_CHOICE_CHANGED: "cleardevExecution.staleChoice",
	MODEL_NOT_AVAILABLE: "cleardevExecution.missingModel",
	LOGIN_REQUIRED: "cleardevExecution.unauthenticated",
	RATE_LIMITED: "cleardevExecution.rateLimited",
	QUOTA_EXHAUSTED: "cleardevExecution.quotaExhausted",
	PROVIDER_UNAVAILABLE: "cleardevExecution.providerUnavailable",
};

export function ClearDevPreflight({ preflight, requirementId, execution, current = false, onSaved }: {
	preflight?: Preflight; requirementId: string; execution?: ExecutionView; current?: boolean; onSaved: () => void;
}) {
	const { t } = useTranslation();
	const now = useFreshnessClock();
	const inFlight = useRef(false);
	const retry = useMutation({
		retry: false,
		mutationFn: async () => {
			if (!current || !preflight?.id || preflight.outcome !== "FAILED") throw new Error(t("cleardevExecution.unknown"));
			const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/preflight-retries", { params: { path: { id: requirementId } }, body: { preflightId: preflight.id } });
			if (result.error) throw result.error;
		},
		onSettled: () => { inFlight.current = false; onSaved(); },
	});
	if (!preflight) return null;
	const local = preflight.evidence?.scope === "LOCAL_CONFIGURATION";
	const failed = preflight.outcome === "FAILED";
	const retryAt = preflight.retryAt ? Date.parse(preflight.retryAt) : 0;
	const waiting = preflight.retryAt ? !Number.isFinite(retryAt) || retryAt > now : false;
	const evidence = preflight.evidence;
	const fact = (value: string | undefined, expected: string, label: MessageKey) => t(value === expected ? label : value === "UNAVAILABLE" ? "cleardevExecution.unavailable" : "cleardevExecution.factUnknown");
	return <section className="flex flex-col gap-2 rounded-md border p-3 text-sm" role={failed ? "alert" : "status"} data-testid="cleardev-preflight">
		<p className="font-semibold">{t(failed ? "cleardevExecution.preflightFailed" : local ? "cleardevExecution.localPassed" : "cleardevExecution.passed")}</p>
		<p className="break-all">{toolName(preflight.provider)} · {preflight.resolvedModel || preflight.requestedModel || "—"}</p>
		{local ? <>
			<p>{t("cleardevExecution.localFacts", { installation: fact(evidence?.installation, "AVAILABLE", "cleardevExecution.available"), configuration: fact(evidence?.configuration, "VALID", "cleardevExecution.valid"), model: evidence?.model === "UNAVAILABLE" ? t("cleardevExecution.notListed") : fact(evidence?.model, "LISTED", "cleardevExecution.listed") })}</p>
			<p>{t(evidence?.authentication === "CONFIGURED" ? "cleardevExecution.authConfigured" : "cleardevExecution.authUnknown")}</p>
			<p>{t("cleardevExecution.remoteUnknown")}</p>
		</> : null}
		{failed ? <>
			<p>{t(reasonMessages[preflight.reasonCode ?? ""] ?? "cleardevExecution.unknownFailure")}</p>
			{waiting ? <p>{t("cleardevExecution.retryAt", { time: Number.isFinite(retryAt) ? new Date(retryAt).toLocaleString() : "—" })}</p> : null}
			{current ? <>
			{execution?.known && !execution.legacy && !execution.modelLocked ? <ClearDevModelRepair key={`${preflight.aoProjectId}:${execution.agent}:${execution.model}`} projectId={preflight.aoProjectId} execution={execution} onSaved={onSaved} /> : execution?.modelLocked ? <p>{t("cleardevExecution.modelLocked")}</p> : null}
			<Button type="button" size="sm" className="self-start" variant="outline" disabled={!hasTrustedApiBaseUrl() || !preflight.id || !preflight.retryable || waiting || retry.isPending} onClick={() => { if (inFlight.current) return; inFlight.current = true; retry.mutate(); }}>{t("cleardevExecution.retryPreflight")}</Button>
			<p className="text-xs text-muted-foreground">{t("cleardevExecution.retryNotice")}</p>
			{retry.isError ? <p className="text-error">{apiErrorMessage(retry.error)}</p> : null}
			</> : <p>{t("cleardevExecution.inactiveFailure")}</p>}
		</> : null}
	</section>;
}

function ClearDevModelRepair({ projectId, execution, onSaved }: { projectId: string; execution: ExecutionView; onSaved: () => void }) {
	const { t } = useTranslation();
	const [choice, setChoice] = useState<ExecutionChoice>({ agent: execution.agent, model: execution.model, effort: execution.effort });
	const inFlight = useRef(false);
	const save = useMutation({
		retry: false,
		mutationFn: async () => {
			const result = await apiClient.PUT("/api/v1/cleardev/projects/{projectId}/execution", { params: { path: { projectId } }, body: { ...choice, model: choice.model.trim() } });
			if (result.error) throw result.error;
		},
		onSettled: () => { inFlight.current = false; onSaved(); },
	});
	return <details className="cleardev-disclosure">
		<summary>{t("cleardevExecution.repair")}</summary>
		<div className="flex flex-col gap-2 py-2">
			<ClearDevExecutionFields projectId={projectId} value={choice} onChange={setChoice} toolLocked disabled={save.isPending || !execution.known || execution.modelLocked} />
			<Button type="button" size="sm" variant="outline" className="self-start" disabled={save.isPending || !validExecutionChoice(choice) || !execution.known || execution.modelLocked || (choice.model.trim() === execution.model && choice.effort === execution.effort)} onClick={() => { if (inFlight.current) return; inFlight.current = true; save.mutate(); }}>{t("cleardevExecution.saveModel")}</Button>
			{save.isSuccess ? <p role="status">{t("cleardevExecution.savedModel")}</p> : null}
			{save.isError ? <p role="alert" className="text-error">{apiErrorMessage(save.error)}</p> : null}
		</div>
	</details>;
}
