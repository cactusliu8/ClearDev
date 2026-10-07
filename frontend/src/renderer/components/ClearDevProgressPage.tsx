import { useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import type { MessageKey } from "../i18n";
import {
	useClearDevProgressQuery,
	useRequestClearDevProgressExplanation,
} from "../hooks/useClearDevProgressQuery";
import { apiErrorMessage } from "../lib/api-client";
import type { ClearDevTrustedProgressSummary } from "../lib/cleardev-progress";
import { isMacPlatform } from "../lib/platform";
import { cn } from "../lib/utils";
import { Badge } from "./ui/badge";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Skeleton } from "./ui/skeleton";
import { ClearDevNewRequirement, ClearDevRequirementActions } from "./ClearDevOperate";
import { ClearDevProducts } from "./ClearDevProducts";
import { ClearDevStageWorkbench } from "./ClearDevStageWorkbench";
import { hasCurrentFacts, useFreshnessClock } from "../lib/cleardev-freshness";
import { ClearDevConversationView } from "./ClearDevConversationView";
import { TopbarButton, topbarHeaderClass, topbarProjectLabelClass } from "./TopbarButton";

const COLLAPSED_ID_LENGTH = 12;
const isMac = isMacPlatform();
const dragStyle = isMac ? ({ WebkitAppRegion: "drag" } as React.CSSProperties) : undefined;
const noDragStyle = isMac ? ({ WebkitAppRegion: "no-drag" } as React.CSSProperties) : undefined;

function enumLabel(prefix: string, value: string | undefined, translate: (key: MessageKey) => string) {
	if (!value) return translate("cleardevProgress.unknown");
	const key = `${prefix}.${value}` as MessageKey;
	const label = translate(key);
	return label === key ? value.replaceAll("_", " ").toLowerCase() : label;
}

function phaseVariant(phase: string): "neutral" | "accent" | "success" | "warning" | "error" {
	switch (phase) {
		case "COMPLETED":
			return "success";
		case "CANCELLED":
			return "neutral";
		case "NEEDS_HUMAN":
		case "AWAITING_CONFIRMATION":
		case "AWAITING_CLARIFICATION":
		case "AWAITING_SCOPE":
			return "warning";
		case "BLOCKED":
		case "REWORKING":
			return "error";
		default:
			return "accent";
	}
}

function CollapsedId({ value }: { value?: string }) {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	if (!value) return null;
	if (value.length <= COLLAPSED_ID_LENGTH) {
		return <code className="font-mono text-2xs text-muted-foreground">{value}</code>;
	}
	return (
		<button
			aria-expanded={open}
			aria-label={open ? t("cleardevProgress.expandedId") : t("cleardevProgress.collapsedId")}
			className="rounded-sm font-mono text-2xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline focus-visible:outline-none"
			onClick={() => setOpen((current) => !current)}
			title={value}
			type="button"
		>
			{open ? value : `${value.slice(0, 8)}…`}
		</button>
	);
}

function Section({ title, children }: { title: string; children: ReactNode }) {
	return (
		<div className="flex flex-col gap-1.5">
			<h3 className="text-2xs font-semibold tracking-wider text-muted-foreground uppercase">{title}</h3>
			{children}
		</div>
	);
}

function IssueList({
	items,
	empty,
}: {
	items?: Array<{ kind?: string; subjectType?: string; subjectId?: string; reasonCode?: string }>;
	empty: string;
}) {
	if (!items || items.length === 0) {
		return <p className="text-sm text-muted-foreground">{empty}</p>;
	}
	return (
		<ul className="flex flex-col gap-1.5">
			{items.map((item, index) => (
				<li className="text-sm" key={`${item.kind ?? "issue"}-${item.subjectId ?? index}`}>
					<span>{item.kind ?? empty}</span>
					{item.reasonCode ? <span className="text-muted-foreground"> · {item.reasonCode}</span> : null}
					{item.subjectId ? (
						<span className="ml-2">
							<CollapsedId value={item.subjectId} />
						</span>
					) : null}
				</li>
			))}
		</ul>
	);
}

function RequirementCard({
	projectId,
	summary,
	onOpenSession,
}: {
	projectId: string;
	summary: ClearDevTrustedProgressSummary;
	onOpenSession: (requirementId: string, sessionId: string, title: string) => void;
}) {
	const { t } = useTranslation();
	const explain = useRequestClearDevProgressExplanation(projectId);
	const label = (prefix: string, value?: string) => enumLabel(prefix, value, (key) => t(key));
	const counts = summary.taskCounts;
	const explanation = summary.explanation;
	const requesting = explain.isPending && explain.variables === summary.developmentRequirementId;

	return (
		<Card id={`cleardev-requirement-${summary.developmentRequirementId}`} data-phase={summary.phase} data-testid={`cleardev-progress-requirement-${summary.developmentRequirementId}`}>
			<CardHeader className="border-b">
				<div className="flex flex-wrap items-start justify-between gap-3">
					<div className="flex min-w-0 flex-col gap-1.5">
						<CardTitle className="truncate">{summary.name}</CardTitle>
						<div className="flex flex-wrap items-center gap-2">
							<Badge variant={phaseVariant(summary.phase ?? "")}>
								{label("cleardevProgress.phase", summary.phase)}
							</Badge>
							{summary.attention && summary.attention !== "NONE" ? (
								<Badge variant={summary.attention === "NEEDS_HUMAN" ? "warning" : "error"}>
									{label("cleardevProgress.attention", summary.attention)}
								</Badge>
							) : null}
							{explanation?.stale ? <Badge variant="outline">{t("cleardevProgress.explanationStale")}</Badge> : null}
						</div>
					</div>
					<Button
						data-testid="cleardev-progress-explain"
						disabled={!summary.canRequestExplanation || requesting}
						onClick={() => {
							if (!summary.developmentRequirementId) return;
							explain.mutate(summary.developmentRequirementId);
						}}
						size="sm"
						type="button"
						variant="outline"
					>
						{requesting ? t("cleardevProgress.requestingExplanation") : t("cleardevProgress.requestExplanation")}
					</Button>
				</div>
			</CardHeader>
			<CardContent className="flex flex-col gap-5 pt-(--card-spacing)">
				<ClearDevRequirementActions projectId={projectId} requirementId={summary.developmentRequirementId!} phase={summary.phase} />
				<ClearDevStageWorkbench summary={summary} onOpenSession={onOpenSession} />
				<Section title={t("cleardevProgress.conclusion")}>
					<dl className="grid gap-2 text-sm sm:grid-cols-2">
						<div>
							<dt className="text-muted-foreground">{t("cleardevProgress.reason")}</dt>
							<dd>{summary.reasonCode || t("cleardevProgress.none")}</dd>
						</div>
						<div>
							<dt className="text-muted-foreground">{t("cleardevProgress.nextOwner")}</dt>
							<dd>
								{label("cleardevProgress.owner", summary.nextOwner?.role)} ·{" "}
								{label("cleardevProgress.action", summary.nextOwner?.action)}
							</dd>
						</div>
						<div className="sm:col-span-2">
							<dt className="text-muted-foreground">{t("cleardevProgress.taskCounts")}</dt>
							<dd>
								{t("cleardevProgress.taskCountsDetail", {
									planned: counts?.planned ?? 0,
									running: counts?.running ?? 0,
									review: counts?.review ?? 0,
									rework: counts?.rework ?? 0,
									needsHuman: counts?.needsHuman ?? 0,
									blocked: counts?.blocked ?? 0,
									done: counts?.done ?? 0,
								})}
							</dd>
						</div>
						<div>
							<dt className="text-muted-foreground">{t("cleardevProgress.requirementId")}</dt>
							<dd>
								<CollapsedId value={summary.developmentRequirementId} />
							</dd>
						</div>
					</dl>
				</Section>
				<Section title={t("cleardevProgress.currentWork")}>
					<IssueList empty={t("cleardevProgress.none")} items={summary.currentWork} />
				</Section>
				{summary.plannerCoordination?.length ? (
					<Section title={t("cleardevProgress.coordination.title")}>
						{summary.plannerCoordination.map((item) => (
							<div className="flex flex-col gap-1.5 text-sm" key={item.eventId} data-testid="cleardev-planner-coordination">
								<p>{t("cleardevProgress.coordination.reported")}: {item.reportedSummary}</p>
								{item.reportedEvidence?.map((evidence, index) => <p className="text-muted-foreground" key={index}>{evidence}</p>)}
								<p>{t("cleardevProgress.coordination.affected")}: {item.affectedTaskKeys?.join(", ")}</p>
								<p>{t("cleardevProgress.coordination.round", { round: item.coordinationRound ?? 0, max: item.maxCoordinationRounds ?? 0 })}</p>
								<p>{t(item.extraCoordinationGrant ? "cleardevRecovery.extraCoordinationOriginalLimit" : "cleardevProgress.coordination.decision")}: {item.decisionSource ? `${item.decisionSource} · ` : ""}{item.decision || t("cleardevProgress.coordination.pending")}</p>
								{item.summary ? <p>{item.summary}</p> : null}
								{item.reasonCode ? <p className="text-muted-foreground">{item.reasonCode}</p> : null}
								{item.recovery ? <p>{t("cleardevRecovery.coordinationHistory")} · {item.recoveryDecision?.outcome || t("cleardevProgress.coordination.pending")}</p> : null}
								{item.recoveryDecision?.summary ? <p>{item.recoveryDecision.summary}</p> : null}
								{item.extraCoordinationGrant ? <p>{t("cleardevRecovery.extraCoordinationGranted")} · {item.extraCoordinationDecision?.outcome || t("cleardevProgress.coordination.pending")}</p> : null}
								{item.extraCoordinationDecision?.summary ? <p>{item.extraCoordinationDecision.summary}</p> : null}
								{item.checkRecovery ? <p>{t("cleardevRecovery.stoppedCheckGranted")} · <CollapsedId value={item.checkRecovery.retryCheckRunId} /></p> : null}
								{item.questions?.map((question, index) => <p key={index}>{question}</p>)}
								<div className="flex flex-wrap items-baseline gap-2"><CollapsedId value={item.eventId} /><CollapsedId value={item.sourceCandidateSha} /><CollapsedId value={item.decisionSha256} /></div>
								{item.appliedContracts?.map((contract) => (
									<p className="flex flex-wrap items-baseline gap-2" key={contract.id}>
										<span>{t("cleardevProgress.coordination.applied")}: {contract.taskKey}</span>
										<CollapsedId value={contract.previousPackageSha256} /><span>→</span><CollapsedId value={contract.effectivePackageSha256} />
									</p>
								))}
							</div>
						))}
					</Section>
				) : null}
				<Section title={t("cleardevProgress.blockers")}>
					<IssueList empty={t("cleardevProgress.none")} items={summary.blockers} />
				</Section>
				<Section title={t("cleardevProgress.pendingDecisions")}>
					<IssueList empty={t("cleardevProgress.none")} items={summary.pendingDecisions} />
				</Section>
				<Section title={t("cleardevProgress.missingEvidence")}>
					{!summary.missingEvidence || summary.missingEvidence.length === 0 ? (
						<p className="text-sm text-muted-foreground">{t("cleardevProgress.none")}</p>
					) : (
						<ul className="flex flex-col gap-1.5">
							{summary.missingEvidence.map((item, index) => (
								<li className="text-sm" key={`${item.kind}-${item.developmentTaskId ?? item.integrationCandidateId ?? index}`}>
									<span>{item.kind}</span>
									<CollapsedId value={item.developmentTaskId ?? item.integrationCandidateId} />
								</li>
							))}
						</ul>
					)}
				</Section>
				<Section title={t("cleardevProgress.currentCandidates")}>
					{!summary.currentCandidates || summary.currentCandidates.length === 0 ? (
						<p className="text-sm text-muted-foreground">{t("cleardevProgress.none")}</p>
					) : (
						<ul className="flex flex-col gap-1.5">
							{summary.currentCandidates.map((candidate) => (
								<li className="text-sm" key={candidate.id}>
									<span>
										{candidate.kind}
										{candidate.current ? "" : ` · ${t("cleardevProgress.historicalTask")}`}
									</span>
									<span className="ml-2">
										<CollapsedId value={candidate.commitSha || candidate.id} />
									</span>
								</li>
							))}
						</ul>
					)}
				</Section>
				<Section title={t("cleardevProgress.recentFacts")}>
					{!summary.recentFacts || summary.recentFacts.length === 0 ? (
						<p className="text-sm text-muted-foreground">{t("cleardevProgress.none")}</p>
					) : (
						<ul className="flex flex-col gap-1.5">
							{summary.recentFacts.map((fact) => (
								<li className="text-sm" key={fact.sequence}>
									<span>
										#{fact.sequence} {fact.action}
									</span>
									<span className="ml-2">
										<CollapsedId value={fact.subjectId} />
									</span>
								</li>
							))}
						</ul>
					)}
				</Section>
				<Section title={t("cleardevProgress.explanation")}>
					<div
						data-explanation-status={explanation?.status ?? "none"}
						data-testid="cleardev-progress-explanation"
					>
					{!summary.canRequestExplanation ? (
						<p className="text-sm text-muted-foreground">{t("cleardevProgress.explanationUnavailable")}</p>
					) : null}
					{explain.isError ? (
						<p className="text-sm text-destructive" role="alert">
							{apiErrorMessage(explain.error, t("cleardevProgress.loadFailed"))}
						</p>
					) : null}
					{!explanation ? (
						<p className="text-sm text-muted-foreground">{t("cleardevProgress.explanationNone")}</p>
					) : (
						<div className="flex flex-col gap-1.5 text-sm">
							{explanation.status === "PENDING" || explanation.status === "SENT" ? (
								<p>{t("cleardevProgress.explanationPending")}</p>
							) : null}
							{explanation.status === "FAILED" ? <p>{t("cleardevProgress.explanationFailed")}</p> : null}
							{explanation.summary ? <p className="whitespace-pre-wrap">{explanation.summary}</p> : null}
							{explanation.citedFactIds && explanation.citedFactIds.length > 0 ? (
								<ul className="flex flex-col gap-1">
									{explanation.citedFactIds.map((id) => (
										<li key={id}>
											<CollapsedId value={id} />
										</li>
									))}
								</ul>
							) : null}
						</div>
					)}
					</div>
				</Section>
			</CardContent>
		</Card>
	);
}

export function ClearDevProgressPage({ projectId }: { projectId: string }) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const query = useClearDevProgressQuery(projectId);
	const now = useFreshnessClock();
	const current = hasCurrentFacts(query, now);
	const [selectedSession, setSelectedSession] = useState<{ requirementId: string; sessionId: string; title: string } | null>(null);
	const [returnTarget, setReturnTarget] = useState<string | null>(null);
	useEffect(() => {
		if (!selectedSession && returnTarget) {
			const frame = requestAnimationFrame(() => {
				const target = document.getElementById(`cleardev-requirement-${returnTarget}`) ?? document.getElementById(`cleardev-product-${returnTarget}`);
				target?.scrollIntoView({ block: "start" });
				setReturnTarget(null);
			});
			return () => cancelAnimationFrame(frame);
		}
		return undefined;
	}, [selectedSession, returnTarget]);
	if (selectedSession) {
		return <ClearDevConversationView key={selectedSession.sessionId} sessionId={selectedSession.sessionId} title={selectedSession.title} onBack={() => {
			setReturnTarget(selectedSession.requirementId);
			setSelectedSession(null);
		}} />;
	}

	return (
		<div className="relative flex h-full min-h-0 flex-col bg-background text-foreground" data-testid="cleardev-progress-page">
			<div className={cn(topbarHeaderClass, "workspace-topbar-container")} style={dragStyle}>
				<div className="flex min-w-0 items-center gap-2" style={noDragStyle}>
					<TopbarButton
						aria-label={t("cleardevProgress.back")}
						onClick={() => void navigate({ to: "/projects/$projectId", params: { projectId } })}
						variant="icon"
					>
						<ArrowLeft className="size-icon-md" aria-hidden="true" />
					</TopbarButton>
					<h1 className={topbarProjectLabelClass}>{t("cleardevProgress.title")}</h1>
				</div>
			</div>
			<div className="min-h-0 flex-1 overflow-y-auto px-6 py-6">
				<div className="mx-auto flex w-full max-w-4xl flex-col gap-4">
					<ClearDevProducts key={`products-${projectId}`} projectId={projectId} onOpenSession={(requirementId, sessionId, title) => setSelectedSession({ requirementId, sessionId, title })} onOpenProject={(targetProjectId) => void navigate({ to: "/projects/$projectId/progress", params: { projectId: targetProjectId } })} />
					{current ? <ClearDevNewRequirement key={projectId} projectId={projectId} collapsible busy={(query.data?.requirements ?? []).some((item) => !["COMPLETED", "CANCELLED", "BLOCKED", "REJECTED"].includes(item.phase ?? ""))} /> : null}
					{query.isLoading ? (
						<div aria-busy="true" className="flex flex-col gap-3" data-testid="cleardev-progress-loading">
							<p className="text-sm text-muted-foreground">{t("cleardevProgress.loading")}</p>
							<Skeleton className="h-40 w-full" />
							<Skeleton className="h-40 w-full" />
						</div>
					) : null}
					{query.isError ? (
						<div className="flex flex-col items-start gap-3" data-testid="cleardev-progress-error">
							<p className="text-sm text-destructive" role="alert">
								{apiErrorMessage(query.error, t("cleardevProgress.loadFailed"))}
							</p>
							<Button onClick={() => void query.refetch()} type="button" variant="outline">
								{t("cleardevProgress.retry")}
							</Button>
						</div>
					) : null}
					{!query.isError && query.data && !current ? <p className="text-sm text-destructive" role="alert">{t("cleardevProgress.stale")}</p> : null}
					{current && (query.data?.requirements?.length ?? 0) === 0 ? (
						<p className="text-sm text-muted-foreground" data-testid="cleardev-progress-empty">
							{t("cleardevProgress.empty")}
						</p>
					) : null}
					{current
						? (query.data?.requirements ?? []).map((summary) => (
								<RequirementCard
									key={summary.developmentRequirementId}
									projectId={projectId}
									summary={summary}
									onOpenSession={(requirementId, sessionId, title) => setSelectedSession({ requirementId, sessionId, title })}
								/>
							))
						: null}
				</div>
			</div>
		</div>
	);
}
