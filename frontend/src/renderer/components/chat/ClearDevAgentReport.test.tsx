import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { aoBridge } from "../../lib/bridge";
import { ClearDevAgentReport, parseClearDevReport } from "./ClearDevAgentReport";

const builder = ' {"schemaVersion":1,"kind":"BUILDER_RESULT","outcome":"CANDIDATE_READY","summary":"保存成功，重启后仍可读取。","customDetail":{"extra":"未知字段保留"}} ';
describe("ClearDev report presentation", () => {
	it("recognizes complete supported reports and preserves their exact original text", async () => {
		const report = parseClearDevReport(builder)!;
		render(<ClearDevAgentReport report={report} original={builder} />);
		expect(screen.getByRole("heading")).toHaveTextContent("Builder report");
		expect(screen.getByText("保存成功，重启后仍可读取。")).toBeVisible();
		expect(screen.getByText("未知字段保留")).toBeVisible();
		expect(screen.getByText("Candidate ready for checks and review")).toBeVisible();
		expect(screen.getByText(/official task and stage status/)).toBeVisible();
		await userEvent.click(screen.getByText("Original report"));
		expect(document.querySelector("pre")?.textContent).toBe(builder);
		const copy = vi.spyOn(aoBridge.clipboard, "writeText").mockResolvedValueOnce(undefined);
		await userEvent.click(screen.getByRole("button", { name: "Copy original report" }));
		expect(copy).toHaveBeenCalledWith(builder);
		copy.mockRestore();
	});
	it("handles fenced plans, reviews and stage acceptance as read-only reports", () => {
		for (const [kind, schemaVersion] of [["COMPLEX_ENGINEERING_PLAN",3],["LOCAL_REVIEW",1],["REQUIREMENT_FINAL_REVIEW_RESULT",1],["PRODUCT_DISCOVERY",2]] as const) {
			expect(parseClearDevReport('```json\n'+JSON.stringify({kind,schemaVersion,summary:"报告"})+'\n```')?.kind).toBe(kind);
		}
	});
	it("falls back for unknown versions, kinds, malformed, trailing and overly nested input", () => {
		for (const text of ['{"schemaVersion":99,"kind":"BUILDER_RESULT"}', '{"schemaVersion":1,"kind":"toString"}', builder+' trailing', builder.slice(0,-4), '[1,2]', JSON.stringify({schemaVersion:1,kind:"BUILDER_RESULT",nested:Array.from({length:21}).reduce<object>(p=>({inside:p}),{})})]) expect(parseClearDevReport(text)).toBeNull();
	});
	it("renders untrusted HTML, links and prototype-named fields as inert text", () => {
		const original='{"schemaVersion":1,"kind":"LOCAL_REVIEW","verdict":"REWORK","summary":"<img src=x onerror=alert(1)>","findings":[{"path":"src/a.ts","message":"[click](javascript:alert(1))"}],"__proto__":"ordinary field"}';
		render(<ClearDevAgentReport report={parseClearDevReport(original)!} original={original} />);
		expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeVisible();
		expect(screen.getByText("[click](javascript:alert(1))")).toBeVisible();
		expect(screen.getByText("ordinary field")).toBeVisible();
		expect(document.querySelector("img, a")).toBeNull();
	});
});

it("labels plan review acceptance as a reported opinion", () => {
	const original='{"schemaVersion":1,"kind":"COMPLEX_PLAN_REVIEW","verdict":"APPROVED","summary":"方案可接受","findings":[]}';
	render(<ClearDevAgentReport report={parseClearDevReport(original)!} original={original} />);
	expect(screen.getByText("Plan reported acceptable")).toBeVisible();
	expect(screen.queryByText("APPROVED", {exact:true})).not.toBeInTheDocument();
});
