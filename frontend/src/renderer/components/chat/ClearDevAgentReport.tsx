import { useTranslation } from "react-i18next";
import type { MessageKey } from "../../i18n/messages";
import { CopyButton } from "./CopyButton";

type Report = Record<string, unknown>;
const kinds: Record<string, { versions: number[]; label: MessageKey }> = {
	BUILDER_RESULT: { versions: [1], label: "cleardevReport.builder" },
	LOCAL_REVIEW: { versions: [1], label: "cleardevReport.reviewer" },
	REQUIREMENT_FINAL_REVIEW_RESULT: { versions: [1], label: "cleardevReport.stageReviewer" },
	ENGINEERING_PLAN: { versions: [1], label: "cleardevReport.planner" },
	COMPLEX_ENGINEERING_PLAN: { versions: [1, 3], label: "cleardevReport.planner" },
	PLAN_REVIEW: { versions: [1], label: "cleardevReport.planReview" },
	COMPLEX_PLAN_REVIEW: { versions: [1], label: "cleardevReport.planReview" },
	REQUIREMENT_COMPILATION: { versions: [1], label: "cleardevReport.specification" },
	PRODUCT_DISCOVERY: { versions: [1, 2], label: "cleardevReport.steward" },
	PRODUCT_CLARIFICATION_REQUIRED: { versions: [2], label: "cleardevReport.clarification" },
};
const labels: Record<string, MessageKey> = {
	summary: "cleardevReport.summary", message: "cleardevReport.summary",
	outcome: "cleardevReport.outcome", verdict: "cleardevReport.outcome",
	technicalApproach: "cleardevReport.approach", tasks: "cleardevReport.tasks",
	objective: "cleardevReport.objective", reviewCriteria: "cleardevReport.criteria",
	findings: "cleardevReport.findings", severity: "cleardevReport.severity",
	path: "cleardevReport.path", reason: "cleardevReport.reason", questions: "cleardevProduct.questions",
	acceptanceSummary: "cleardevReport.acceptance", consistencySummary: "cleardevReport.consistency",
	scopeSummary: "cleardevReport.scope", regressionSummary: "cleardevReport.regression",
	evidenceSummary: "cleardevReport.evidence", title: "cleardevReport.title",
	requirements: "humanDecision.field.requirements", acceptanceScenarios: "humanDecision.field.acceptanceScenarios",
	constraints: "humanDecision.field.constraints", nonGoals: "humanDecision.field.nonGoals",
	assumptions: "humanDecision.field.assumptions", conflicts: "humanDecision.field.conflicts",
	id: "humanDecision.field.id", key: "humanDecision.field.key", text: "humanDecision.field.text",
	priority: "humanDecision.field.priority", writePaths: "humanDecision.field.writePaths",
	checks: "humanDecision.field.checks", description: "humanDecision.field.description",
};
const statuses: Record<string, MessageKey> = {
	CANDIDATE_READY: "cleardevReport.candidateReady", PASS: "cleardevReport.pass",
	REWORK: "cleardevReport.rework", BLOCKED: "cleardevReport.blocked",
	NEEDS_HUMAN: "cleardevReport.needsHuman", READY: "cleardevReport.ready",
	DISCUSS: "cleardevReport.discuss", CLARIFICATION_REQUIRED: "cleardevReport.clarification",
	APPROVED: "cleardevReport.planAcceptable", REPLAN: "cleardevReport.replan",
};

// Recognition is for presentation only; the backend remains the protocol
// validator. Large or deeply nested content keeps the original Markdown view.
export function parseClearDevReport(text: string): Report | null {
	if (text.length > 1_000_000) return null;
	const trimmed = text.trim();
	const body = /^```(?:json)?\s*\n([\s\S]*)\n```$/i.exec(trimmed)?.[1] ?? trimmed;
	try {
		const value: unknown = JSON.parse(body);
		if (!value || typeof value !== "object" || Array.isArray(value)) return null;
		const report = value as Report;
		const kind = typeof report.kind === "string" && Object.hasOwn(kinds, report.kind) ? kinds[report.kind] : undefined;
		if (!kind || typeof report.schemaVersion !== "number" || !kind.versions.includes(report.schemaVersion)) return null;
		const queue: [unknown, number][] = [[report, 0]];
		let count = 0;
		while (queue.length) {
			const [entry, depth] = queue.pop()!;
			if (++count > 10_000 || depth > 20) return null;
			if (entry && typeof entry === "object") for (const child of Object.values(entry)) queue.push([child, depth + 1]);
		}
		return report;
	} catch { return null; }
}

export function ClearDevAgentReport({ report, original }: { report: Report; original: string }) {
	const { t } = useTranslation();
	function label(key: string) { return Object.hasOwn(labels, key) ? t(labels[key]) : key; }
	function renderValue(value: unknown, key = ""): React.ReactNode {
		if (value === null) return <span className="text-muted-foreground">{t("humanDecision.empty")}</span>;
		if (Array.isArray(value)) return value.length ? <ul className="space-y-2 border-l pl-3">{value.map((item, index) => <li key={index}>{renderValue(item)}</li>)}</ul> : <span className="text-muted-foreground">{t("humanDecision.empty")}</span>;
		if (typeof value === "object") return <dl className="space-y-2">{Object.entries(value as Report).map(([field, item]) => <div key={field}><dt className="text-xs text-muted-foreground">{label(field)}</dt><dd className="whitespace-pre-wrap break-words">{renderValue(item, field)}</dd></div>)}</dl>;
		if ((key === "outcome" || key === "verdict") && typeof value === "string" && Object.hasOwn(statuses, value)) return t(statuses[value]);
		return String(value);
	}
	const entries = Object.entries(report).filter(([key]) => key !== "schemaVersion" && key !== "kind");
	const technical = (key: string) => /(?:sha256|sha|Ids?|Version)$/i.test(key) || key === "reasonCode";
	return <section className="rounded-lg border bg-muted/20 p-4" data-testid="cleardev-agent-report">
		<h3 className="font-medium">{t(kinds[report.kind as string].label)}</h3>
		<p className="mt-1 text-xs text-muted-foreground">{t("cleardevReport.reportOnly")}</p>
		<dl className="mt-3 space-y-3">{entries.filter(([key]) => !technical(key)).map(([key, value]) => <div key={key}><dt className="font-medium">{label(key)}</dt><dd className="mt-1 whitespace-pre-wrap break-words">{renderValue(value, key)}</dd></div>)}</dl>
		{entries.some(([key]) => technical(key)) ? <details className="mt-3"><summary className="cursor-pointer text-xs text-muted-foreground">{t("humanDecision.technical")}</summary><dl className="mt-2 space-y-2">{entries.filter(([key]) => technical(key)).map(([key, value]) => <div key={key}><dt>{label(key)}</dt><dd className="whitespace-pre-wrap break-all">{renderValue(value, key)}</dd></div>)}</dl></details> : null}
		<details className="mt-3"><summary className="cursor-pointer text-xs text-muted-foreground">{t("cleardevReport.original")}</summary><CopyButton text={original} label={t("cleardevReport.copyOriginal")} /><pre className="mt-2 overflow-x-auto whitespace-pre-wrap break-all text-xs">{original}</pre></details>
	</section>;
}
