import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import type { MessageKey } from "../i18n/messages";

type Selection = components["schemas"]["ClearDevProductSelection"];
type Result = components["schemas"]["ClearDevProductDiscoveryResult"];
type Basis = components["schemas"]["ClearDevProjectExecutionBasis"];

const origins: Record<string, MessageKey> = {
	EMPTY: "cleardevProject.origin.EMPTY",
	EXISTING: "cleardevProject.origin.EXISTING",
	DISCOVERED: "cleardevProject.origin.DISCOVERED",
};

export function ProjectSelection({ selection, historical = false }: { selection: Selection; historical?: boolean }) {
	const { t } = useTranslation();
	return <details className="cleardev-disclosure cleardev-project-source text-sm" data-testid="project-selection">
		<summary><span>{t(historical ? "cleardevProject.historicalChoice" : "cleardevProject.savedChoice")}: {selection.option.title}</span><span className="ml-2 text-xs text-muted-foreground">{origins[selection.option.origin] ? t(origins[selection.option.origin]) : selection.option.origin}</span></summary>
		<p className="whitespace-pre-wrap">{selection.reason}</p>
		<p className="break-all text-muted-foreground">{selection.repositoryPath}</p>
		{selection.repositoryUrl ? <p className="break-all text-muted-foreground">{t("cleardevProject.identity")} {selection.repositoryUrl}</p> : null}
		<p className="break-all">{t("cleardevProject.observedBaseline")} <code>{selection.baseCommitSha}</code></p>
		<p className="text-xs text-muted-foreground">{t("cleardevProject.choiceAuthority")}</p>
	</details>;
}

export function ProjectProposalContext({ result }: { result: Result }) {
	const { t } = useTranslation();
	if (result.schemaVersion !== 2) return null;
	return <div className="flex flex-col gap-3 text-sm">
		<details className="cleardev-disclosure"><summary>{t("cleardevProject.options")}</summary>
			{result.options?.map((option) => <div key={option.key} className="mt-2 border-l pl-3">
				<h4>{option.title} · {origins[option.origin] ? t(origins[option.origin]) : option.origin}</h4>
				<p>{option.description}</p>
				{option.repositoryUrl ? <p className="break-all text-muted-foreground">{t("cleardevProject.proposedSource")} {option.repositoryUrl}</p> : null}
				<ul className="list-disc pl-5">{option.tradeoffs.map((text) => <li key={text}>{text}</li>)}</ul>
			</div>)}
		</details>
		<details className="cleardev-disclosure"><summary>{t("cleardevProject.investigation")}</summary>
			<p className="text-xs text-muted-foreground">{t("cleardevProject.evidenceAuthority")}</p>
			{result.evidence?.length ? result.evidence.map((item, index) => <div key={`${index}-${item.status}`} className="mt-2 border-l pl-3" data-evidence-status={item.status}>
				<p className="font-medium">{t(item.status === "OBSERVED" ? "cleardevProject.observed" : item.status === "ASSUMPTION" ? "cleardevProject.assumption" : "cleardevProject.unverified")}</p>
				<p>{item.claim}</p><p className="break-all text-muted-foreground">{t("cleardevProject.source")} {item.source}</p>
			</div>) : <p className="text-muted-foreground">{t("cleardevProject.noEvidence")}</p>}
		</details>
	</div>;
}

export function ProjectExecutionBasis({ basis }: { basis: Basis }) {
	const { t } = useTranslation();
	return <details className="cleardev-disclosure text-sm" data-testid="project-execution-basis">
		<summary>{t("cleardevProject.basis")}</summary>
		<div className="mt-2 flex flex-col gap-2">
			<p className="text-muted-foreground">{t("cleardevProject.basisAuthority")}</p>
			<h4>{t("cleardevProject.scope")}</h4><ul className="list-disc pl-5">{basis.writePaths.map((path) => <li key={path}><code>{path}</code></li>)}</ul>
			<h4>{t("cleardevProject.dependencies")}</h4>{basis.dependencyNeeds.length ? <ul className="list-disc pl-5">{basis.dependencyNeeds.map((text) => <li key={text}>{text}</li>)}</ul> : <p>{t("cleardevProject.noDependencies")}</p>}
			<h4>{t("cleardevProject.checks")}</h4>
			{basis.checks.map((check) => <div key={check.id} className="border-l pl-3">
				<p>{t("cleardevProject.checkTimeout", { id: check.id, seconds: check.timeoutSeconds })}</p><code className="break-all">{JSON.stringify(check.argv)}</code>
				<p className="break-all text-muted-foreground">{t("cleardevProject.covers")} {check.mainPaths.join(", ")}</p>
			</div>)}
			{basis.trial ? <section aria-label="阶段试用方式" className="rounded-md border p-3">
                <h4 className="font-medium">阶段验收：审核者亲自试用</h4>
                <p>{basis.trial.service ? "启动服务，再逐项操作功能。" : "逐项执行命令并检查结果，无需启动网页服务。"}</p>
                <ol className="list-decimal pl-5">{basis.trial.steps.map(step => <li key={step.id} className="mt-2">
                    <p>{step.kind === "COMMAND" ? "执行命令" : step.kind === "HTTP" ? "请求接口" : "操作页面"} · {step.observe}</p>
                    {step.argv?.length ? <code className="break-all">{step.argv.join(" ")}</code> : null}
                    {step.outputFiles?.length ? <p>检查生成文件：{step.outputFiles.join("、")}</p> : null}
                </li>)}</ol>
                <p className="text-muted-foreground">运行成功仅证明操作已执行，功能是否符合要求仍需逐项审核。</p>
            </section> : null}
            <h4>{t("cleardevProject.launch")}</h4><p>{basis.launch.description}</p>
			<p>{t("cleardevProject.directory")} <code>{basis.launch.workingDirectory}</code></p>
			<code className="break-all">{basis.launch.argv.length ? JSON.stringify(basis.launch.argv) : t("cleardevProject.noLaunch")}</code>
		</div>
	</details>;
}
