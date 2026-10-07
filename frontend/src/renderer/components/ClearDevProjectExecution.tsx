import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { useMutation } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { ProjectExecutionBasis } from "./ClearDevProjectContext";
import { Button } from "./ui/button";

type Requirement = components["schemas"]["ClearDevRequirementView"];
type Admission = components["schemas"]["ClearDevProjectExecutionInput"];
type Plan = components["schemas"]["ClearDevComplexEngineeringPlan"];

// These are display/input bindings only. The service repeats specification,
// source, plan, runtime and budget checks and alone owns admission and dispatch.
function displayedPlan(view: Requirement) {
	const version = view.requirementVersions?.find((entry) => entry.id === view.trustedProgress.currentRequirementVersionId);
	if (!version || version.status !== "CONFIRMED") return undefined;
	const plan = view.complexPlanning?.plans?.filter((entry) => entry.requirementVersionId === version.id)
		.reduce<Plan | undefined>((latest, entry) => !latest || entry.version > latest.version ? entry : latest, undefined);
	return plan?.requirementSha256 === version.sha256 ? plan : undefined;
}

function sameBinding(request: Admission, plan: ReturnType<typeof displayedPlan>, base: string) {
	return !!plan && request.planId === plan.id && request.planSha256 === plan.planSha256 &&
		request.requirementSha256 === plan.requirementSha256 && request.baseCommitSha === base;
}

export function ClearDevProjectExecution({ view, onSaved }: { view: Requirement; onSaved: () => void }) {
	const { t } = useTranslation();
	const request = useRef<Admission | null>(null);
	const inFlight = useRef(false);
	const state = view.trustedProgress.projectPlanning;
	const plan = displayedPlan(view);
	const start = useMutation({
		retry: false,
		mutationFn: async (body: Admission) => {
			const result = await apiClient.POST("/api/v1/cleardev/requirements/{id}/project-execution-runs", {
				params: { path: { id: view.requirement.id } }, body,
			});
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevExecution.receiptMissing"));
			return result.data;
		},
		onSettled: () => { inFlight.current = false; onSaved(); },
	});
	if (!state) return null;
	if (state.executionAdmitted) return <section className="rounded-md border p-3 text-sm" data-testid="cleardev-project-execution">
		<p role="status">{t(view.trustedProgress.phase === "COMPLETED" ? "cleardevExecution.completed" : "cleardevExecution.admitted")}</p>
		<details className="cleardev-disclosure"><summary>{t("cleardevProduct.technicalDetails")}</summary><code className="block break-all text-xs">{state.executionRunId}</code></details>
	</section>;
	const allowed = state.current && state.sourceCurrent && state.planReady && state.executionAvailable && !!plan &&
		view.trustedProgress.reasonCode === "PROJECT_EXECUTION_ADMISSION_REQUIRED" &&
		(!request.current || sameBinding(request.current, plan, state.baseCommitSha));
	const runtime = state.executionRuntime;
	return <section className="flex flex-col gap-2 rounded-md border p-3 text-sm" data-testid="cleardev-project-execution">
		<h3 className="font-semibold">{t("cleardevExecution.title")}</h3>
		<p role="status">{t(state.planReady ? "cleardevExecution.saved" : "cleardevExecution.waiting")}</p>
		<details className="cleardev-disclosure"><summary>{t("cleardevProduct.technicalDetails")}</summary>
		{plan ? <>
			<p>{t("cleardevExecution.plan")} <code>{plan.id}</code> {t("cleardevExecution.version", { version: plan.version })}</p>
			<code className="break-all text-xs">{plan.planSha256}</code>
			<details><summary className="cursor-pointer">{t("cleardevExecution.savedPlan")}</summary><pre className="max-h-64 overflow-auto whitespace-pre-wrap text-xs">{plan.planJson}</pre></details>
		</> : null}
		<p>{t("cleardevExecution.baseline")} <code className="break-all text-xs">{state.baseCommitSha}</code></p>
		{state.executionBasis ? <ProjectExecutionBasis basis={state.executionBasis} /> : null}
		{runtime ? <div className="text-muted-foreground" data-testid="project-admission-runtime">
			<p>{t("cleardevExecution.runtime", { environment: runtime.environment })}</p>
			<p>{t("cleardevExecution.variables", { host: runtime.hostVariable, port: runtime.portVariable, data: runtime.dataDirectoryVariable })}</p>
			{state.executionBasis?.trial?.service === false ? <p>{t("cleardevExecution.commandTrial")}</p> : <p>{t("cleardevExecution.health", { path: runtime.healthPath, status: runtime.healthStatus, seconds: runtime.healthTimeoutSeconds })}</p>}
			{runtime.prepareArgv.length ? <p>{t("cleardevExecution.prepare")} <code>{JSON.stringify(runtime.prepareArgv)}</code></p> : null}
			{runtime.env && Object.keys(runtime.env).length ? <pre className="whitespace-pre-wrap text-xs">{JSON.stringify(runtime.env, null, 2)}</pre> : null}
		</div> : null}
		</details>
		<p className="text-muted-foreground">{t("cleardevExecution.authority")}</p>
		{!allowed ? <p role="alert">{t("cleardevExecution.conditions", { reason: state.reasonCode || t("cleardevExecution.required") })}</p> : null}
		<Button type="button" className="cleardev-primary self-start" disabled={!allowed || start.isPending || start.isSuccess || !hasTrustedApiBaseUrl()} onClick={() => {
			if (inFlight.current || !allowed || !plan) return;
			request.current ??= { requestId: crypto.randomUUID(), planId: plan.id, planSha256: plan.planSha256, requirementSha256: plan.requirementSha256, baseCommitSha: state.baseCommitSha };
			inFlight.current = true;
			start.mutate(request.current);
		}}>{t(start.isPending ? "cleardevExecution.submitting" : start.isError ? "cleardevExecution.retry" : "cleardevExecution.start")}</Button>
		{start.isError ? <p role="alert">{apiErrorMessage(start.error)} {t("cleardevExecution.failed")}</p> : null}
		{start.isSuccess ? <p role="status">{t("cleardevExecution.accepted")}</p> : null}
	</section>;
}
