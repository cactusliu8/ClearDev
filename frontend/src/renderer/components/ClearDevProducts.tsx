import { useRef, useState } from "react";
import { ArrowRight, CircleDot, MessageSquare, Plus } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";
import { apiClient, apiErrorCode, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { cleardevProgressQueryKey } from "../lib/cleardev-progress";
import { hasCurrentFacts, useFreshnessClock } from "../lib/cleardev-freshness";
import { ClearDevRequirementActions } from "./ClearDevOperate";
import { ClearDevProductPlan } from "./ClearDevProductPlan";
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";
import { ClearDevExecutionFields, ClearDevExecutionSummary, ClearDevPreflight, validExecutionChoice, type ExecutionChoice, type ExecutionView } from "./ClearDevExecutionChoice";
import { ProjectExecutionBasis, ProjectProposalContext, ProjectSelection } from "./ClearDevProjectContext";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Card, CardHeader, CardTitle, CardContent } from "./ui/card";
import { Badge } from "./ui/badge";
import "./cleardev-products.css";

type Product = components["schemas"]["ClearDevProductView"];
type Stage = components["schemas"]["ClearDevProductStageView"];
type NewProduct = components["schemas"]["CreateClearDevProductInput"];
type Discussion = components["schemas"]["ClearDevProductDiscussionInput"];
const productKey = (projectId: string) => ["cleardev-products", projectId];
const textAreaClass = "mt-1 w-full rounded-md border bg-transparent p-2 text-sm";
// Only pre-persistence validation failures permit changing an immutable request.
// Unknown/network failures must retry the same ID because the choice may exist.
const rejectedProjectChoiceCodes = new Set([
	"PRODUCT_OPTION_UNAVAILABLE", "PRODUCT_CHOICE_REASON_REQUIRED", "PRODUCT_PROJECT_UNAVAILABLE",
	"PRODUCT_SOURCE_INSPECTION_UNAVAILABLE", "PRODUCT_SOURCE_UNAVAILABLE", "PRODUCT_BASELINE_CHANGED",
	"PRODUCT_SOURCE_NOT_EMPTY", "PRODUCT_SOURCE_MISMATCH",
]);
const productPhaseNames: Record<string, MessageKey> = {
	PREPARING_SOURCE: "cleardevProduct.sourcePreparing", SOURCE_PREPARATION_FAILED: "cleardevProduct.sourceFailed",
 THINKING: "cleardevProduct.phase.THINKING", AWAITING_INPUT: "cleardevProduct.phase.AWAITING_INPUT",
	READY: "cleardevProduct.phase.READY", FROZEN: "cleardevProduct.phase.FROZEN", BLOCKED: "cleardevWorkbench.phase.BLOCKED",
};
// Blockers state what happened and the next step. Unmapped codes stay visible
// verbatim instead of being hidden behind a generic message.
const productReasonNames: Record<string, MessageKey> = {
	PRODUCT_STEWARD_UNAVAILABLE: "cleardevProduct.reason.PRODUCT_STEWARD_UNAVAILABLE",
	PRODUCT_STEWARD_TIMEOUT: "cleardevProduct.reason.PRODUCT_STEWARD_TIMEOUT",
	PRODUCT_DISCOVERY_INVALID: "cleardevProduct.reason.PRODUCT_DISCOVERY_INVALID",
	PRODUCT_BASELINE_CHANGED: "cleardevProduct.reason.PRODUCT_BASELINE_CHANGED",
	PRODUCT_EXECUTION_CAPABILITY_UNAVAILABLE: "cleardevProduct.reason.PRODUCT_EXECUTION_CAPABILITY_UNAVAILABLE",
	PRODUCT_STAGE_CHANGED: "cleardevProduct.reason.PRODUCT_STAGE_CHANGED",
	PRODUCT_DISCUSSION_LIMIT_REACHED: "cleardevProduct.reason.PRODUCT_DISCUSSION_LIMIT_REACHED",
	PRODUCT_PROJECT_UNAVAILABLE: "cleardevProduct.reason.PRODUCT_PROJECT_UNAVAILABLE",
	PRODUCT_DISCOVERY_UNAVAILABLE: "cleardevProduct.reason.PRODUCT_DISCOVERY_UNAVAILABLE",
	PRODUCT_REQUEST_CHANGED: "cleardevProduct.reason.PRODUCT_REQUEST_CHANGED",
};
const stagePhaseNames: Record<string, MessageKey> = {
	PLANNED: "cleardevProduct.stage.PLANNED", NEEDS_CAPABILITY: "cleardevProduct.stage.NEEDS_CAPABILITY",
	DEFINING_REQUIREMENT: "cleardevProduct.stage.DEFINING_REQUIREMENT", AWAITING_CLARIFICATION: "cleardevWorkbench.phase.AWAITING_CLARIFICATION",
	AWAITING_CONFIRMATION: "cleardevProduct.stage.AWAITING_CONFIRMATION", PLANNING: "cleardevWorkbench.phase.PLANNING",
	COORDINATING: "cleardevWorkbench.phase.COORDINATING", AWAITING_PLAN_REVIEW: "cleardevWorkbench.phase.AWAITING_PLAN_REVIEW",
	DEVELOPING: "cleardevWorkbench.phase.DEVELOPING", VERIFYING: "cleardevWorkbench.phase.VERIFYING", REWORKING: "cleardevWorkbench.phase.REWORKING", INTEGRATING: "cleardevProduct.stage.INTEGRATING",
	NEEDS_HUMAN: "cleardevProgress.phase.NEEDS_HUMAN",
	COMPLETED: "cleardevWorkbench.phase.COMPLETED", BLOCKED: "cleardevWorkbench.phase.BLOCKED", SUPERSEDED: "cleardevProduct.stage.SUPERSEDED",
};

function phaseTone(phase: string): "neutral" | "accent" | "success" | "warning" | "error" {
	if (phase === "COMPLETED") return "success";
	if (phase === "BLOCKED" || phase === "REWORKING" || phase === "SOURCE_PREPARATION_FAILED") return "error";
	if (["AWAITING_INPUT", "AWAITING_CONFIRMATION", "AWAITING_CLARIFICATION", "NEEDS_CAPABILITY", "NEEDS_HUMAN"].includes(phase)) return "warning";
	if (["FROZEN", "SUPERSEDED", "CANCELLED"].includes(phase)) return "neutral";
	return "accent";
}

export function ClearDevProducts({ projectId, onOpenSession, onOpenProject }: { projectId: string; onOpenSession?: (requirementId: string, sessionId: string, title: string) => void; onOpenProject?: (projectId: string) => void }) {
	const { t } = useTranslation();
	const client = useQueryClient();
	const query = useQuery({
		queryKey: productKey(projectId), enabled: hasTrustedApiBaseUrl(), retry: 1, refetchInterval: 3000,
		queryFn: async () => {
			const result = await apiClient.GET("/api/v1/cleardev/projects/{projectId}/products", { params: { path: { projectId } } });
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevProduct.unavailable"));
			return result.data;
		},
	});
	const now = useFreshnessClock();
	const current = hasCurrentFacts(query, now);
	const refresh = () => {
		void client.invalidateQueries({ queryKey: productKey(projectId) });
		void client.invalidateQueries({ queryKey: cleardevProgressQueryKey(projectId) });
	};
	return <section className="cleardev-products flex flex-col gap-4" data-testid="cleardev-products">
		<NewProductForm key={projectId} projectId={projectId} compact={!!query.data?.products.length} execution={current ? query.data?.execution : undefined} onSaved={refresh} />
		{query.isError ? <p role="alert">{apiErrorMessage(query.error)}</p> : null}
		{!query.isError && query.data && !current ? <p role="alert">{t("cleardevProduct.stale")}</p> : null}
		{query.isLoading ? <p role="status">{t("cleardevProduct.loading")}</p> : null}
		{current ? query.data?.products.map((view) => <ProductCard key={view.goal.id} view={view} projectId={projectId} onSaved={refresh} onOpenSession={onOpenSession} onOpenProject={onOpenProject} />) : null}
	</section>;
}

function NewProductForm({ projectId, compact, execution, onSaved }: { projectId: string; compact: boolean; execution?: ExecutionView; onSaved: () => void }) {
	const { t } = useTranslation();
	const [expanded, setExpanded] = useState(false);
	const [name, setName] = useState("");
	const [goal, setGoal] = useState("");
	const [draft, setDraft] = useState<ExecutionChoice | null>(null);
	const savedRequest = useRef<NewProduct | null>(null);
	const inFlight = useRef(false);
	const create = useMutation({
		retry: false,
		mutationFn: async (body: NewProduct) => {
			const result = await apiClient.POST("/api/v1/cleardev/products", { body });
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevProduct.creationMissing"));
			return result.data;
		},
		onSuccess: () => { savedRequest.current = null; setDraft(null); setName(""); setGoal(""); setExpanded(false); onSaved(); },
		onError: (error) => {
			// These errors precede registration. An ambiguous connection failure
			// keeps the exact first-choice payload and request ID for replay.
			if (["EXECUTION_CHOICE_INVALID", "EXECUTION_CHOICE_FROZEN"].includes(apiErrorCode(error) ?? "")) savedRequest.current = null;
		},
		onSettled: () => { inFlight.current = false; onSaved(); },
	});
	const open = !compact || expanded || create.isError;
	const submissionLocked = create.isPending || (create.isError && savedRequest.current !== null);
	const legacyLocked = execution?.known && execution.legacy && execution.toolLocked;
	const configured = execution ? { agent: execution.agent, model: execution.model, effort: execution.effort } : { agent: "codex", model: "" };
	const editable = execution?.known && !legacyLocked && !execution.modelLocked;
	const candidate = draft ?? configured;
	const choice = savedRequest.current?.execution ?? (execution?.modelLocked ? configured : { ...candidate, agent: execution?.toolLocked ? execution.agent : candidate.agent });
	const canChoose = execution?.known && (legacyLocked || validExecutionChoice(choice));
	return <Card className="cleardev-product-create">
		<CardHeader className="flex flex-row items-center justify-between gap-3">
			<div className="flex items-center gap-2"><MessageSquare className="size-4 text-primary" aria-hidden="true" /><CardTitle>{t("cleardevProduct.new")}</CardTitle></div>
			{compact ? <Button type="button" variant="ghost" size="sm" disabled={create.isError} aria-expanded={open} aria-controls={`new-product-${projectId}`} onClick={() => setExpanded(!open)}><Plus className={open ? "size-4 rotate-45" : "size-4"} aria-hidden="true" />{t(open ? "common.close" : "cleardevProduct.newToggle")}</Button> : null}
		</CardHeader>
		<CardContent id={`new-product-${projectId}`} hidden={!open}>
			<form className="flex flex-col gap-3" data-testid="cleardev-new-product" onSubmit={(event) => {
				event.preventDefault();
				if (inFlight.current || !name.trim() || !goal.trim() || !hasTrustedApiBaseUrl() || (!savedRequest.current && !canChoose)) return;
				savedRequest.current ??= { aoProjectId: projectId, requestId: crypto.randomUUID(), name: name.trim(), goalText: goal.trim(), ...(legacyLocked ? {} : { execution: { ...choice, model: choice.model.trim() } }) };
				inFlight.current = true;
				create.mutate(savedRequest.current);
			}}>
				<p className="text-sm text-muted-foreground">{t("cleardevProduct.intro")}</p>
				{editable || savedRequest.current?.execution ? <ClearDevExecutionFields projectId={projectId} value={choice} onChange={setDraft} toolLocked={execution?.toolLocked ?? true} disabled={submissionLocked || !execution?.known} /> : <ClearDevExecutionSummary execution={execution} />}
				<label className="text-sm">{t("cleardevProduct.name")}<Input required maxLength={200} value={name} disabled={submissionLocked} onChange={(event) => setName(event.target.value)} /></label>
				<label className="text-sm">{t("cleardevProduct.goal")}<textarea required rows={3} maxLength={16000} className={textAreaClass} value={goal} disabled={submissionLocked} onChange={(event) => setGoal(event.target.value)} /></label>
				<Button type="submit" className="cleardev-primary self-start" onClick={(event) => { if (event.detail > 1) event.preventDefault(); }} disabled={create.isPending || !hasTrustedApiBaseUrl() || (!savedRequest.current && !canChoose)}>{create.isError && savedRequest.current ? t("cleardevProduct.retry") : t("cleardevProduct.begin")}</Button>
				{create.isError ? <p role="alert">{apiErrorMessage(create.error)} {savedRequest.current ? t("cleardevProduct.retryNotice") : null}</p> : null}
			</form>
		</CardContent>
	</Card>;
}

export function ProductCard({ view, projectId, onSaved, onOpenSession, onOpenProject }: { view: Product; projectId: string; onSaved: () => void; onOpenSession?: (requirementId: string, sessionId: string, title: string) => void; onOpenProject?: (projectId: string) => void }) {
	const { t } = useTranslation();
	const latest = view.discussions.at(-1);
	const currentStages = view.stages.filter((stage) => stage.current);
	const history = view.discussions.slice(0, -1);
	const stageNumber = (stage: Stage) => stage.stage.ordinal + 1 + view.stages.filter((earlier) =>
		earlier.phase === "COMPLETED" && earlier.stage.createdAt < stage.stage.createdAt).length;
	const interrupted = latest?.protocolVersion === 2 && latest.failureReason === "PRODUCT_STEWARD_WORKSPACE_CHANGED" && !!latest.selection && !latest.result;
	const protocolFailed = latest?.protocolVersion === 2 && latest.failureReason === "PRODUCT_DISCOVERY_INVALID" && !!latest.selection && !latest.result;
	const proposal = latest?.result ?? (interrupted || protocolFailed ? history.find((entry) => entry.id === latest?.selection?.sourceDiscussionId)?.result : undefined);
	// An unsettled discussion uses its original-step recovery below. A new
	// discussion remains a distinct explicit action, not a retry workaround.
	const canDiscuss = view.canDiscuss && !["CANCELLED", "FROZEN"].includes(view.phase) && view.remainingDiscussions > 0 && !!proposal;
	return <Card className="cleardev-product-card" id={`cleardev-product-${view.goal.id}`} data-testid="cleardev-product">
		<CardHeader className="cleardev-product-heading">
			<div className="flex flex-wrap items-start justify-between gap-3">
				<div className="min-w-0"><CardTitle className="break-words">{view.goal.name}</CardTitle><p role="status" className="mt-2"><Badge variant={phaseTone(view.phase)} data-tone={phaseTone(view.phase)}>{productPhaseNames[view.phase] ? t(productPhaseNames[view.phase]) : view.phase}</Badge>{view.reason && view.latestPreflight?.outcome !== "FAILED" ? <span className="ml-2 break-all text-xs text-muted-foreground">{productReasonNames[view.reason] ? t(productReasonNames[view.reason]) : view.reason}</span> : null}</p></div>
				{view.stewardSessionId && onOpenSession ? <Button type="button" variant="ghost" size="sm" onClick={() => onOpenSession(view.goal.id, view.stewardSessionId!, t("cleardevProduct.steward"))}><MessageSquare className="size-3.5" aria-hidden="true" />{t("cleardevProduct.viewSteward")}</Button> : null}
			</div>
			<details className="cleardev-disclosure" data-testid="cleardev-product-brief"><summary>{t("cleardevProduct.originalGoal")}</summary><p className="whitespace-pre-wrap break-words">{view.goal.goalText}</p></details>
		</CardHeader>
		<CardContent className="flex flex-col gap-3">
			<ClearDevExecutionSummary execution={view.execution} />
 {view.sourcePreparation && view.phase !== "CANCELLED" ? <ProductSourcePreparation key={view.sourcePreparation.input.requestId} productId={view.goal.id} preparation={view.sourcePreparation} onSaved={onSaved} /> : null}
			<ClearDevPreflight preflight={view.latestPreflight} requirementId={view.goal.id} execution={view.execution} current={!!view.latestPreflight?.id && view.controlProgress.controlledWork?.some((work) => work.preflightId === view.latestPreflight?.id && work.roleBindingId === view.latestPreflight?.roleBindingId) === true} onSaved={onSaved} />
			{view.selection ? <ProjectSelection selection={view.selection} /> : null}
			{view.selection && !view.sourceCurrent && !view.sourcePreparation ? <p role="alert">{t("cleardevProduct.sourceChanged")}</p> : null}
			{view.phase === "THINKING" ? <p className="text-sm text-muted-foreground">{t("cleardevProduct.thinking")}</p> : null}
			{view.phase === "FROZEN" ? <p className="text-sm text-muted-foreground">{t("cleardevProduct.frozen")}</p> : null}
			{history.length ? <details className="cleardev-disclosure"><summary>{t("cleardevProduct.history", { count: history.length })}</summary>
				{history.map((entry) => <div key={entry.id} className="my-3 border-l pl-3 text-sm">
					<p className="whitespace-pre-wrap">{entry.userMessage}</p>
					<p className="mt-2 whitespace-pre-wrap text-muted-foreground">{entry.result?.message}</p>
					{entry.selection ? <ProjectSelection selection={entry.selection} historical /> : null}
					{entry.result ? <ProjectProposalContext result={entry.result} /> : null}
				</div>)}
			</details> : null}
			{latest ? <div className="flex flex-col gap-2 text-sm" data-testid="cleardev-product-discussion">
				<details className="cleardev-disclosure"><summary>{t("cleardevProduct.latestInput")}</summary><p className="whitespace-pre-wrap break-words">{latest.userMessage}</p></details>
				{latest.result ? <>
					<div className="cleardev-steward-proposal"><h3 className="mb-2 flex items-center gap-2 font-medium"><MessageSquare className="size-4 text-primary" aria-hidden="true" />{t("cleardevProduct.proposal")}</h3><p className="whitespace-pre-wrap break-words leading-relaxed">{latest.result.message}</p></div>
					{latest.result.questions.length ? <section className="cleardev-attention" data-testid="cleardev-product-questions"><h3 className="mb-2 font-medium">{t("cleardevProduct.questions")}</h3>{latest.result.questions.map((question) => <div key={question.key} className="mt-2"><p>{question.text}</p><p className="mt-1 text-xs text-muted-foreground">{question.reason}</p></div>)}</section> : null}
					{latest.result.features.length ? <div><h3 className="mb-2 font-medium">{t("cleardevProduct.features")}</h3><ul className="cleardev-feature-grid">{latest.result.features.map((feature) => <li key={feature.key}><CircleDot className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" /><div><p className="font-medium">{feature.title}</p><p className="mt-1 text-xs leading-relaxed text-muted-foreground">{feature.description}</p></div></li>)}</ul></div> : null}
					<details className="cleardev-disclosure"><summary>{t("cleardevProduct.feasibility")}</summary><p className="whitespace-pre-wrap text-muted-foreground">{latest.result.feasibilitySummary}</p></details>
					<ProductDiscussionReadiness result={latest.result} historical={view.phase === "FROZEN"} />
					<ProjectProposalContext result={latest.result} />
				</> : null}
			</div> : null}
			<ClearDevWorkflowRecovery id={view.goal.id} scope="discussion" sourceKey={latest?.id ?? ""} onOpenSession={onOpenSession}
				current={!["CANCELLED", "FROZEN", "PREPARING_SOURCE", "SOURCE_PREPARATION_FAILED"].includes(view.phase) && (!view.selection || view.sourceCurrent)}
				blocked={["BLOCKED", "NEEDS_HUMAN"].includes(view.phase)} />
			{interrupted ? <p role="alert">{t("cleardevProduct.interrupted")}</p> : null}
			{protocolFailed ? <p role="alert">{t("cleardevProduct.protocolFailed")}</p> : null}
			<ClearDevProductPlan key={`product-plan:${latest?.id}`} product={view} onSaved={onSaved} />
			{currentStages.map((stage, index) => <StageCard key={stage.stage.id} view={stage} displayNumber={stageNumber(stage)} projectId={projectId} onSaved={onSaved} onOpenProject={onOpenProject} onOpenSession={onOpenSession} recoveryCurrent={!view.selection || view.sourceCurrent}
				automatic={view.automation?.status === "RESOLVED" && view.automation.decision === "APPROVE"}
                waitingForPrevious={currentStages.slice(0, index).some((previous) => previous.phase !== "COMPLETED")}
				canPrepare={["READY", "FROZEN"].includes(view.phase) && (!stage.planningOnly || view.sourceCurrent) && currentStages.slice(0, index).every((previous) => previous.phase === "COMPLETED")} />)}
			{canDiscuss && latest ? <ProductDiscussionForm key={latest.id} productId={view.goal.id} previousId={latest.id} options={proposal?.options ?? []} selection={view.selection} sourceCurrent={view.sourceCurrent} requireChoice={interrupted} onSaved={onSaved} /> : null}
			{canDiscuss || view.remainingDiscussions <= 0 ? <p className={view.remainingDiscussions <= 0 ? "cleardev-attention text-xs" : "text-xs text-muted-foreground"}>{t("cleardevProduct.remaining", { count: view.remainingDiscussions })}</p> : null}

			{view.stages.some((stage) => !stage.current) ? <details className="cleardev-disclosure"><summary>{t("cleardevProduct.superseded")}</summary>
				{view.stages.filter((stage) => !stage.current).map((stage) => <StageCard key={stage.stage.id} view={stage} displayNumber={stageNumber(stage)} projectId={projectId} onSaved={onSaved} onOpenProject={onOpenProject} canPrepare={false} />)}
			</details> : null}
		</CardContent>
	</Card>;
}

function ProductSourcePreparation({ productId, preparation, onSaved }: { productId: string; preparation: NonNullable<Product["sourcePreparation"]>; onSaved: () => void }) {
 const { t } = useTranslation();
 const inFlight = useRef(false);
 const retry = useMutation({ retry: false, mutationFn: async () => {
  const result = await apiClient.POST("/api/v1/cleardev/products/{id}/discussions", { params: { path: { id: productId } }, body: preparation.input });
  if (result.error) throw result.error;
  if (!result.data) throw new Error(t("cleardevProduct.discussionMissing"));
  return result.data;
 }, onSettled: () => { inFlight.current = false; onSaved(); } });
 return <section className="rounded-md border p-3 text-sm" data-testid="cleardev-source-preparation">
  <p role="status">{t(preparation.state === "FAILED" ? "cleardevProduct.sourceFailed" : "cleardevProduct.sourcePreparing")}</p>
  <p className="mt-2 break-all">{preparation.sourceUrl}</p>
  <p className="mt-2 text-muted-foreground">{t("cleardevProduct.sourcePreparationHelp")}</p>
  {preparation.failure ? <details className="mt-2"><summary>{t("cleardevProduct.sourceErrors")}</summary><p className="whitespace-pre-wrap break-words">{preparation.failure}</p></details> : null}
  {preparation.state === "FAILED" ? <Button type="button" className="mt-2" disabled={retry.isPending || !hasTrustedApiBaseUrl()} onClick={() => { if (inFlight.current) return; inFlight.current = true; retry.mutate(); }}>{t("cleardevProduct.sourceRetry")}</Button> : null}
  {retry.isError ? <p role="alert">{apiErrorMessage(retry.error)}</p> : null}
 </section>;
}

function ProductDiscussionReadiness({ result, historical }: { result: components["schemas"]["ClearDevProductDiscoveryResult"]; historical: boolean }) {
	const { t } = useTranslation();
	const assumptions = result.evidence?.filter((item) => item.status === "ASSUMPTION") ?? [];
	const acceptances = result.stages.reduce((count, stage) => count + stage.acceptanceCriteria.length, 0);
	return <section className="rounded-md border bg-muted/20 p-3" data-testid="cleardev-discussion-readiness">
		<h3 className="font-medium">{t("cleardevDiscussion.checkTitle")}</h3>
		<p className="mt-1 text-muted-foreground">{t(historical ? "cleardevDiscussion.historical" : result.outcome === "READY" ? "cleardevDiscussion.ready" : "cleardevDiscussion.discuss")}</p>
		<p className="mt-2 text-xs text-muted-foreground">{t("cleardevDiscussion.rounds")}</p>
		<p className="mt-2">{t("cleardevDiscussion.counts", { questions: result.questions.length, acceptances })}</p>
		<h4 className="mt-3 font-medium">{t("cleardevDiscussion.assumptions")}</h4>
		{assumptions.length ? <ul className="mt-1 space-y-2">{assumptions.map((item, index) => <li key={index} className="border-l-2 border-primary/30 pl-3"><p className="whitespace-pre-wrap">{item.claim}</p><p className="mt-1 text-xs text-muted-foreground">{item.source}</p></li>)}</ul> : <p className="mt-1 text-muted-foreground">{t("cleardevDiscussion.noAssumptions")}</p>}
	</section>;
}

function ProductDiscussionForm({ productId, previousId, options, selection, sourceCurrent, requireChoice, onSaved }: {
	productId: string; previousId: string; options: components["schemas"]["ClearDevProductOption"][];
	selection?: components["schemas"]["ClearDevProductSelection"]; sourceCurrent: boolean; requireChoice: boolean; onSaved: () => void;
}) {
	const { t } = useTranslation();
	const [message, setMessage] = useState("");
	const [optionKey, setOptionKey] = useState("");
	const [targetProject, setTargetProject] = useState("");
	const [reason, setReason] = useState("");
	const projects = useQuery({
		queryKey: ["cleardev-project-choices"], enabled: options.length > 0 && hasTrustedApiBaseUrl(), retry: 1, refetchInterval: 3000,
		queryFn: async () => {
			const result = await apiClient.GET("/api/v1/projects");
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevProduct.projectsUnavailable"));
			return result.data.projects.filter((project) => project.kind === "single_repo" && !project.resolveError);
		},
	});
	const now = useFreshnessClock();
	const projectsCurrent = hasCurrentFacts(projects, now);
	const choiceValid = !optionKey ? !requireChoice && (!selection || sourceCurrent) : projectsCurrent && !!reason.trim() && !!projects.data?.some((project) => project.id === targetProject);
	const request = useRef<Discussion | null>(null);
	const inFlight = useRef(false);
	const send = useMutation({
		retry: false,
		mutationFn: async (body: Discussion) => {
			const result = await apiClient.POST("/api/v1/cleardev/products/{id}/discussions", { params: { path: { id: productId } }, body });
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevProduct.discussionMissing"));
			return result.data;
		},
		onSettled: () => { inFlight.current = false; onSaved(); },
	});
	return <form className="cleardev-discussion-form flex flex-col gap-3" onSubmit={(event) => {
		event.preventDefault();
		if (inFlight.current || send.isSuccess || !message.trim() || (!request.current && !choiceValid) || !hasTrustedApiBaseUrl()) return;
		request.current ??= {
			requestId: crypto.randomUUID(), expectedPreviousId: previousId, message: message.trim(),
			...(optionKey ? { choice: { optionKey, aoProjectId: targetProject, reason: reason.trim() } } : {}),
		};
		inFlight.current = true;
		send.mutate(request.current);
	}}>
		{options.length ? <fieldset className="cleardev-choices flex flex-col gap-2 text-sm" disabled={send.isPending || send.isError || send.isSuccess}>
			<legend className="px-1 font-medium">{t("cleardevProduct.choose")}</legend>
			<p className="text-muted-foreground">{t("cleardevProduct.chooseHelp")}</p>
			{!requireChoice ? <label className="cleardev-choice" data-selected={!optionKey}><input type="radio" name={`choice-${productId}`} checked={!optionKey} onChange={() => setOptionKey("")} />{t(selection ? "cleardevProduct.keepChoice" : "cleardevProduct.noChoice")}</label> : null}
			{options.map((option) => <label key={option.key} className="cleardev-choice" data-selected={optionKey === option.key}><input type="radio" name={`choice-${productId}`} checked={optionKey === option.key} onChange={() => setOptionKey(option.key)} />{option.title}</label>)}
			{optionKey ? <>
				<label htmlFor={`project-choice-${productId}`}>{t("cleardevProduct.registeredProject")}</label>
				<Select value={targetProject} onValueChange={setTargetProject} disabled={!projectsCurrent || send.isPending || send.isError || send.isSuccess}>
					<SelectTrigger id={`project-choice-${productId}`} aria-label={t("cleardevProduct.registeredProject")}><SelectValue placeholder={t("cleardevProduct.selectRepository")} /></SelectTrigger>
					<SelectContent>{projectsCurrent ? projects.data?.map((project) => <SelectItem key={project.id} value={project.id}>{project.name || project.id} · {project.path}</SelectItem>) : null}</SelectContent>
				</Select>
				<label>{t("cleardevProduct.choiceReason")}<textarea required maxLength={4000} rows={2} className={textAreaClass} value={reason} onChange={(event) => setReason(event.target.value)} /></label>
				{projects.isError ? <p role="alert">{apiErrorMessage(projects.error)}</p> : !projectsCurrent ? <p role="status">{t("cleardevProduct.projectsLoading")}</p> : !projects.data?.length ? <p role="alert">{t("cleardevProduct.projectsNone")}</p> : null}
			</> : null}
		</fieldset> : null}
		<label className="text-sm">{t("cleardevProduct.discuss")}<textarea required className={textAreaClass} maxLength={16000} rows={3} value={message} disabled={send.isPending || send.isError || send.isSuccess} onChange={(event) => setMessage(event.target.value)} /></label>
		<Button type="submit" className="cleardev-primary self-start" disabled={send.isPending || send.isSuccess || (!request.current && !choiceValid) || !hasTrustedApiBaseUrl()}><ArrowRight className="size-4" aria-hidden="true" />{send.isError ? t("cleardevProduct.retry") : t("cleardevProduct.send")}</Button>
		{send.isError ? <>
			<p role="alert">{apiErrorMessage(send.error)} {rejectedProjectChoiceCodes.has(apiErrorCode(send.error) ?? "") ? t("cleardevProduct.choiceRejected") : t("cleardevProduct.retryNotice")}</p>
			{rejectedProjectChoiceCodes.has(apiErrorCode(send.error) ?? "") ? <Button type="button" variant="outline" className="self-start" onClick={() => {
				request.current = null;
				send.reset();
				onSaved();
			}}>{t("cleardevProduct.editChoice")}</Button> : null}
		</> : null}
	</form>;
}

function StageCard({ view, displayNumber, projectId, canPrepare, automatic = false, waitingForPrevious = false, recoveryCurrent = false, onSaved, onOpenProject, onOpenSession }: { view: Stage; displayNumber: number; projectId: string; canPrepare: boolean; automatic?: boolean; waitingForPrevious?: boolean; recoveryCurrent?: boolean; onSaved: () => void; onOpenProject?: (projectId: string) => void; onOpenSession?: (requirementId: string, sessionId: string, title: string) => void }) {
	const { t } = useTranslation();
	const stage = view.stage;
	const targetProject = stage.selection?.aoProjectId ?? projectId;
	const planReady = view.progress?.projectPlanning?.planReady === true;
	const executionAvailable = view.progress?.projectPlanning?.executionAvailable === true;
	const stale = !view.current || view.progress?.projectPlanning?.current === false;
	const inFlight = useRef(false);
	const prepare = useMutation({
		retry: false,
		mutationFn: async () => {
			const result = await apiClient.POST("/api/v1/cleardev/products/{id}/stages/{stageId}/prepare", {
				params: { path: { id: stage.productId, stageId: stage.id } }, body: { definitionSha256: stage.definitionSha256 },
			});
			if (result.error) throw result.error;
			if (!result.data) throw new Error(t("cleardevProduct.preparationMissing"));
			return result.data;
		},
		onSettled: () => { inFlight.current = false; onSaved(); },
	});
	return <section className="cleardev-stage flex flex-col gap-3 text-sm" data-tone={stale ? "neutral" : phaseTone(view.phase)} data-testid="cleardev-product-stage">
		<div className="flex flex-wrap items-start justify-between gap-2"><h3 className="font-medium">{t("cleardevProduct.stage", { number: displayNumber })} · {stage.definition.title}</h3>
		<Badge variant={stale ? "neutral" : phaseTone(view.phase)} data-tone={stale ? "neutral" : phaseTone(view.phase)} role="status">{waitingForPrevious ? t("cleardevProduct.waitingPreviousStageShort") : stale ? t(view.phase === "COMPLETED" ? "cleardevProduct.historicalDelivery" : "cleardevProduct.stage.SUPERSEDED") : view.planningOnly && planReady ? t(executionAvailable ? "cleardevProduct.planAvailable" : "cleardevProduct.planUnavailable") : automatic && view.phase === "PLANNED" ? t("cleardevAutoPlan.waiting") : stagePhaseNames[view.phase] ? t(stagePhaseNames[view.phase]) : view.phase}</Badge></div>
		<p>{stage.definition.goal}</p>
		{waitingForPrevious ? <p className="cleardev-attention" role="status">{t("cleardevProduct.waitingPreviousStage")}</p> : null}
		{stage.definition.feasibility === "NEEDS_CAPABILITY" ? <p className="cleardev-attention">{stage.definition.feasibilityReason}</p> : <details className="cleardev-disclosure"><summary>{t("cleardevProduct.feasibility")}</summary><p className="text-muted-foreground">{stage.definition.feasibilityReason}</p></details>}
		<details className="cleardev-disclosure" data-testid="cleardev-stage-acceptance"><summary>{t("cleardevProduct.acceptance")} <span className="text-muted-foreground">({stage.definition.acceptanceCriteria.length})</span></summary><ul className="list-disc space-y-1.5 pl-5">{stage.definition.acceptanceCriteria.map((text) => <li key={text}>{text}</li>)}</ul></details>
		{stage.definition.nonGoals.length ? <details className="cleardev-disclosure"><summary>{t("cleardevProduct.nonGoals")}</summary><ul className="list-disc space-y-1 pl-5">{stage.definition.nonGoals.map((text) => <li key={text}>{text}</li>)}</ul></details> : null}
		{stage.definition.executionBasis ? <ProjectExecutionBasis basis={stage.definition.executionBasis} /> : null}
		{view.planningOnly ? <p className="text-muted-foreground">{t("cleardevProduct.notAdmitted")}</p> : null}
		{view.progress && !view.planningOnly ? <p className="text-muted-foreground">{t("cleardevProduct.taskAcceptance", { done: view.progress.taskCounts.done, total: view.progress.tasks.filter((task) => task.current).length, acceptance: t(view.progress.phase === "COMPLETED" ? "cleardevProduct.accepted" : "cleardevProduct.pendingAcceptance") })}</p> : null}
		{stage.developmentRequirementId ? <>
			<ClearDevWorkflowRecovery id={stage.developmentRequirementId} scope={view.progress?.projectPlanning?.executionAdmitted ? "execution" : "compilation"} onOpenSession={onOpenSession}
				sourceKey={stage.definitionSha256} completed={view.phase === "COMPLETED"} current={recoveryCurrent && !stale && !waitingForPrevious && view.phase !== "COMPLETED"}
				blocked={["BLOCKED", "NEEDS_HUMAN"].includes(view.phase)} />
			<Button type="button" variant="outline" className="cleardev-action self-start" data-tone={phaseTone(view.phase)} disabled={targetProject !== projectId && !onOpenProject} onClick={() => {
				if (targetProject !== projectId) onOpenProject?.(targetProject);
				else document.getElementById(`cleardev-requirement-${stage.developmentRequirementId}`)?.scrollIntoView({ behavior: "smooth", block: "start" });
			}}><ArrowRight className="size-4" aria-hidden="true" />{t(targetProject !== projectId ? "cleardevProduct.viewSelectedStage" : "cleardevProduct.viewStage")}</Button>
			{!stale || view.phase === "COMPLETED" ? <ClearDevRequirementActions projectId={targetProject} requirementId={stage.developmentRequirementId} phase={view.phase} /> : null}
		</> : <>
			<p className="text-xs text-muted-foreground">{automatic ? t("cleardevAutoPlan.stageNotice") : view.planningOnly ? t("cleardevProduct.planningNotice") : stage.definition.feasibility === "NEEDS_CAPABILITY" ? t("cleardevProduct.unsupported") : t("cleardevProduct.prepareNotice")}</p>
			{view.current && !automatic && (view.planningOnly || stage.definition.feasibility === "SUPPORTED") ? <Button type="button" className="cleardev-primary self-start" disabled={!canPrepare || prepare.isPending || prepare.isSuccess || !hasTrustedApiBaseUrl()} onClick={() => {
				if (inFlight.current) return;
				inFlight.current = true;
				prepare.mutate();
			}}><ArrowRight className="size-4" aria-hidden="true" />{prepare.isError ? t("cleardevProduct.retry") : view.planningOnly ? t("cleardevProduct.preparePlanning") : t("cleardevProduct.prepare")}</Button> : null}
		</>}
		{prepare.isError ? <p role="alert">{apiErrorMessage(prepare.error)}</p> : null}
	</section>;
}
