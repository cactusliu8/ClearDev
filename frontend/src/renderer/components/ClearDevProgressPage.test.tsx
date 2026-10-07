import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "./ui/tooltip";
import type { ClearDevTrustedProgressSummary } from "../lib/cleardev-progress";

const { navigateMock, queryMock, mutateMock } = vi.hoisted(() => ({
	navigateMock: vi.fn(),
	queryMock: vi.fn(),
	mutateMock: vi.fn(),
}));

vi.mock("@tanstack/react-router", () => ({
	useNavigate: () => navigateMock,
}));

vi.mock("../hooks/useClearDevProgressQuery", () => ({
	useClearDevProgressQuery: (...args: unknown[]) => queryMock(...args),
	useRequestClearDevProgressExplanation: () => ({
		mutate: mutateMock,
		isPending: false,
		isError: false,
		error: null,
		variables: undefined,
	}),
}));

import { ClearDevProgressPage } from "./ClearDevProgressPage";

function summary(overrides: Partial<ClearDevTrustedProgressSummary> = {}): ClearDevTrustedProgressSummary {
	return {
		developmentRequirementId: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		aoProjectId: "proj-1",
		name: "Demo requirement",
		phase: "AWAITING_CONFIRMATION",
		attention: "NEEDS_HUMAN",
		taskCounts: { planned: 0, running: 0, review: 0, rework: 0, needsHuman: 0, blocked: 0, done: 0 },
		tasks: [],
		currentWork: [],
		blockers: [],
		pendingDecisions: [
			{
				kind: "REQUIREMENT_CONFIRMATION",
				subjectType: "REQUIREMENT_VERSION",
				subjectId: "version-aaaaaaaa-bbbb-cccc-dddd",
			},
		],
		nextOwner: { role: "HUMAN", action: "CONFIRM_REQUIREMENT" },
		missingEvidence: [],
		currentCandidates: [],
		recentFacts: [],
		latestFactSequence: 2,
		factSummarySha256: "a".repeat(64),
		sortRank: 0,
		canRequestExplanation: true,
		...overrides,
	} as ClearDevTrustedProgressSummary;
}

function renderPage() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<TooltipProvider>
				<ClearDevProgressPage projectId="proj-1" />
			</TooltipProvider>
		</QueryClientProvider>,
	);
}

beforeEach(() => {
	navigateMock.mockReset();
	queryMock.mockReset();
	mutateMock.mockReset();
});

describe("ClearDevProgressPage", () => {
	it("hides cached progress when its trusted refresh age expires", () => {
		queryMock.mockReturnValue({ isLoading: false, isError: false, isSuccess: true,
			dataUpdatedAt: Date.now() - 31_000, data: { aoProjectId: "proj-1", requirements: [summary({ phase: "COMPLETED" })] } });
		renderPage();
		expect(screen.getByRole("alert")).toHaveTextContent("out of date");
		expect(screen.queryByTestId("cleardev-progress-requirement-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")).not.toBeInTheDocument();
	});
	it("shows an empty project", () => {
		queryMock.mockReturnValue({
			isLoading: false,
			isError: false,
			isSuccess: true, dataUpdatedAt: Date.now(),
			data: { aoProjectId: "proj-1", requirements: [] },
		});
		renderPage();
		expect(screen.getByTestId("cleardev-progress-empty")).toHaveTextContent(
			"This project has no development requirements yet.",
		);
	});

	it("shows loading and failure with retry", async () => {
		queryMock.mockReturnValue({ isLoading: true, isError: false, isSuccess: false });
		const { rerender } = renderPage();
		expect(screen.getByTestId("cleardev-progress-loading")).toBeInTheDocument();

		const refetch = vi.fn();
		queryMock.mockReturnValue({
			isLoading: false,
			isError: true,
			isSuccess: false,
			error: { message: "daemon down" },
			refetch,
		});
		rerender(
			<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
				<TooltipProvider>
					<ClearDevProgressPage projectId="proj-1" />
				</TooltipProvider>
			</QueryClientProvider>,
		);
		expect(screen.getByRole("alert")).toHaveTextContent("daemon down");
		await userEvent.click(screen.getByRole("button", { name: "Retry" }));
		expect(refetch).toHaveBeenCalledTimes(1);
	});

	it("shows conclusion first and collapses long identifiers", async () => {
		queryMock.mockReturnValue({
			isLoading: false,
			isError: false,
			isSuccess: true, dataUpdatedAt: Date.now(),
			data: { aoProjectId: "proj-1", requirements: [summary()] },
		});
		renderPage();
		const card = screen.getByTestId("cleardev-progress-requirement-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee");
		expect(card).toHaveAttribute("data-phase", "AWAITING_CONFIRMATION");
		expect(card.textContent).toMatch(/Awaiting confirmation/);
		expect(card.textContent).toMatch(/Needs a person/);
		expect(card.textContent).toMatch(/Person/);
		expect(card.textContent).toMatch(/Confirm requirement/);
		expect(screen.queryByText("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")).not.toBeInTheDocument();
		await userEvent.click(screen.getAllByRole("button", { name: "Show full identifier" })[0]);
		expect(screen.getByText("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")).toBeInTheDocument();
	});

	it.each([
		["DEVELOPING", "Developing"],
		["COORDINATING", "Coordinating engineering"],
		["VERIFYING", "Verifying"],
		["REWORKING", "Reworking"],
		["BLOCKED", "Blocked"],
		["INTEGRATING", "Integrating"],
		["COMPLETED", "Completed"],
		["CANCELLED", "Cancelled"],
	] as const)("renders the %s phase", (phase, label) => {
		queryMock.mockReturnValue({
			isLoading: false,
			isError: false,
			isSuccess: true, dataUpdatedAt: Date.now(),
			data: {
				aoProjectId: "proj-1",
				requirements: [
					summary({
						phase,
						attention: phase === "BLOCKED" ? "BLOCKED" : "NONE",
						pendingDecisions: [],
					}),
				],
			},
		});
		renderPage();
		expect(
			screen.getByTestId("cleardev-progress-requirement-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		).toHaveAttribute("data-phase", phase);
		expect(screen.getAllByText(label).length).toBeGreaterThan(0);
	});

	it("projects coordination facts without inventing approvals or applied revisions", () => {
		queryMock.mockReturnValue({
			isLoading: false, isError: false, isSuccess: true, dataUpdatedAt: Date.now(),
			data: { aoProjectId: "proj-1", requirements: [summary({
				phase: "COORDINATING", attention: "NONE", pendingDecisions: [],
				nextOwner: { role: "PLANNER", action: "COORDINATE_ENGINEERING" },
				plannerCoordination: [{
					eventId: "event-1", dispatchId: "dispatch-1", sourceTaskId: "source", category: "ENGINEERING",
					reportedSummary: "Observed stable ordering", reportedEvidence: ["Builder observation only"],
					affectedTaskKeys: ["consumer"], coordinationRound: 1, maxCoordinationRounds: 2,
					decisionSource: "CONTROL_PLANE", decision: "STOP", summary: "Started contracts cannot change",
					questions: ["Which product ordering is intended?"], appliedContracts: [],
				}],
			})] },
		});
		renderPage();
		const coordination = screen.getByTestId("cleardev-planner-coordination");
		expect(coordination.textContent).toContain("Builder report (not a trusted check): Observed stable ordering");
		expect(coordination.textContent).toContain("Coordination rounds: 1 / 2");
		expect(coordination.textContent).toContain("Actual decision: CONTROL_PLANE · STOP");
		expect(coordination.textContent).toContain("Which product ordering is intended?");
		expect(coordination.textContent).not.toContain("Applied contract revision");
		expect(screen.getByText(/Coordinate remaining engineering work/)).toBeInTheDocument();
	});

	it("keeps the original coordination limit separate from the one granted request", () => {
		queryMock.mockReturnValue({
			isLoading: false, isError: false, isSuccess: true, dataUpdatedAt: Date.now(),
			data: { aoProjectId: "proj-1", requirements: [summary({
				phase: "COORDINATING", attention: "NONE", pendingDecisions: [],
				plannerCoordination: [{
					eventId: "event-3", dispatchId: "dispatch-3", sourceTaskId: "source-task", category: "ENGINEERING",
					reportedSummary: "Exact current engineering observation", reportedEvidence: [],
					affectedTaskKeys: ["consumer"], coordinationRound: 3, maxCoordinationRounds: 3,
					decisionSource: "CONTROL_PLANE", decision: "LIMIT_REACHED", summary: "The original two rounds remain consumed.",
					appliedContracts: [], extraCoordinationGrant: { eventId: "event-3", decisionRequestId: "native-decision", ordinal: 3 },
				}],
			})] },
		});
		renderPage();
		const text = screen.getByTestId("cleardev-planner-coordination").textContent;
		expect(text).toContain("Coordination rounds: 3 / 3");
		expect(text).toContain("Original stop (retained history): CONTROL_PLANE · LIMIT_REACHED");
		expect(text).toContain("One extra coordination was approved on the native desktop");
		expect(text).not.toContain("Actual decision: PLANNER · CONTINUE");
		expect(text).not.toContain("Applied contract revision");
	});

	it("keeps the actual Planner STOP beside a same-candidate check grant", () => {
        queryMock.mockReturnValue({ isLoading: false, isError: false, isSuccess: true, dataUpdatedAt: Date.now(), data: { aoProjectId: "proj-1", requirements: [summary({
            phase: "VERIFYING", attention: "NONE", pendingDecisions: [],
            plannerCoordination: [{ eventId: "event-stop", dispatchId: "dispatch", sourceTaskId: "task", category: "ENGINEERING", reportedSummary: "Original checker unavailable", reportedEvidence: [], affectedTaskKeys: ["consumer"], coordinationRound: 3, maxCoordinationRounds: 3, decisionSource: "PLANNER", decision: "STOP", summary: "No authority to rerun the checker", appliedContracts: [], checkRecovery: { eventId: "event-stop", decisionRequestId: "native-check", stopSha256: "a".repeat(64), taskId: "task", dispatchId: "dispatch", candidateSha: "b".repeat(40), taskPacketSha256: "c".repeat(64), checkRunId: "old-check", retryCheckRunId: "old-check:stopped-check-retry", reworkCount: 9 } }],
        })] } });
        renderPage();
        const text = screen.getByTestId("cleardev-planner-coordination").textContent;
        expect(text).toContain("PLANNER · STOP");
        expect(text).toContain("Same-candidate check recovery was authorized by the human");
        expect(text).not.toContain("PLANNER · CONTINUE");
        expect(text).not.toContain("Applied contract revision");
    });

	it("shows Project Steward as next owner while scope approval is waiting", () => {
		queryMock.mockReturnValue({
			isLoading: false,
			isError: false,
			isSuccess: true, dataUpdatedAt: Date.now(),
			data: {
				aoProjectId: "proj-1",
				requirements: [
					summary({
						phase: "AWAITING_SCOPE",
						attention: "NONE",
						pendingDecisions: [],
						currentWork: [{ kind: "SCOPE_EXPANSION" }],
						nextOwner: { role: "STEWARD", action: "DECIDE_SCOPE" },
					}),
				],
			},
		});
		renderPage();
		const card = screen.getByTestId("cleardev-progress-requirement-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee");
		expect(card).toHaveAttribute("data-phase", "AWAITING_SCOPE");
		expect(card.textContent).toMatch(/Awaiting scope decision/);
		expect(card.textContent).toMatch(/Project Steward/);
		expect(card.textContent).toMatch(/Decide scope/);
		expect(card.textContent).not.toMatch(/Needs a person/);
		expect(card.textContent).toMatch(/SCOPE_EXPANSION/);
	});

	it("requests an explanation without sending phase or note text", async () => {
		queryMock.mockReturnValue({
			isLoading: false,
			isError: false,
			isSuccess: true, dataUpdatedAt: Date.now(),
			data: { aoProjectId: "proj-1", requirements: [summary()] },
		});
		renderPage();
		await userEvent.click(screen.getByRole("button", { name: "Request note" }));
		expect(mutateMock).toHaveBeenCalledWith("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee");
	});

	it("returns to the project board", async () => {
		queryMock.mockReturnValue({
			isLoading: false,
			isError: false,
			isSuccess: true, dataUpdatedAt: Date.now(),
			data: { aoProjectId: "proj-1", requirements: [] },
		});
		renderPage();
		await userEvent.click(screen.getByRole("button", { name: "Back to board" }));
		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId",
			params: { projectId: "proj-1" },
		});
	});
});
