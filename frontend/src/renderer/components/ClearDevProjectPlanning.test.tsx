import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true,
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
	apiErrorCode: (error: unknown) => typeof error === "object" && error !== null && "code" in error ? error.code : undefined,
}));
vi.mock("./ClearDevOperate", () => ({ ClearDevRequirementActions: ({ projectId, requirementId }: { projectId: string; requirementId: string }) => <div data-testid="planning-child-actions">{projectId}/{requirementId}</div> }));
import { ProductCard } from "./ClearDevProducts";

type Product = components["schemas"]["ClearDevProductView"];
type Selection = components["schemas"]["ClearDevProductSelection"];
const createdAt = "2026-09-23T00:00:00Z";
const option = { key: "empty", title: "Start notes from zero", origin: "EMPTY", description: "Build only the needed local note workflow.", tradeoffs: ["No inherited code; storage and tests must be created."] };
const binding: Selection = {
	sourceDiscussionId: "proposal-round", option, reason: "Local personal use needs no account integration.", aoProjectId: "notes", repositoryPath: "/projects/notes", repositoryUrl: "", baseCommitSha: "a".repeat(40), createdAt,
};
function project(selected = false): Product {
	const discussionId = selected ? "selected-round" : "proposal-round";
	return {
		goal: { id: "product", aoProjectId: "container", name: "Personal notes", goalText: "Save notes and read them after restarting", requestId: "create", createdAt },
		phase: selected ? "READY" : "AWAITING_INPUT", canDiscuss: true, sourceCurrent: selected, remainingDiscussions: 10,
		controlProgress: {} as Product["controlProgress"], messageBudget: { budgetVersion: "MESSAGE_BUDGET_V1", confirmedSentMessages: 2, reservedMessages: 2, maxMessages: null, remainingMessages: null, roles: [], steps: [] },
		...(selected ? { selection: binding } : {}),
		discussions: [{ id: discussionId, productId: "product", ordinal: selected ? 1 : 0, protocolVersion: 2, userMessage: "Keep notes local", createdAt, ...(selected ? { selection: binding } : {}), result: {
			schemaVersion: 2, kind: "PRODUCT_DISCOVERY", outcome: selected ? "READY" : "DISCUSS", message: "Compare the source before choosing.", feasibilitySummary: "Checks are proposed, not run.", questions: [], features: [], stages: [], options: [option],
			evidence: [{ status: "UNVERIFIED", claim: "The future persistence checks have not run.", source: "Proposed test entry only" }, { status: "OBSERVED", claim: "The selected baseline has no tracked files.", source: "Steward git ls-tree observation" }],
		} }],
		stages: selected ? [{ current: true, planningOnly: true, phase: "NEEDS_CAPABILITY", stage: {
			id: "stage", productId: "product", discussionId, ordinal: 0, definitionSha256: "b".repeat(64), createdAt, selection: binding, baseCommitSha: binding.baseCommitSha,
			definition: { key: "notes", title: "Persist local notes", goal: "Keep notes across restarts", featureKeys: ["notes"], acceptanceCriteria: ["The saved note is still readable after restart"], nonGoals: ["No cloud accounts"], feasibility: "NEEDS_CAPABILITY", feasibilityReason: "Planning is available; generic execution is not.",
				executionBasis: { writePaths: ["src/**", "package.json"], dependencyNeeds: ["Propose a local persistence library"], checks: [{ id: "notes-tests", argv: ["npm", "test"], timeoutSeconds: 120, mainPaths: ["src/**", "package.json"] }], launch: { argv: ["npm", "start"], workingDirectory: ".", description: "Start the planned notes app" } },
			},
		} }] : [],
	};
}
function mount(view: Product, onOpenProject = vi.fn()) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	return { ...render(<QueryClientProvider client={client}><ProductCard view={view} projectId="container" onSaved={vi.fn()} onOpenProject={onOpenProject} /></QueryClientProvider>), client, onOpenProject };
}
beforeEach(() => {
	get.mockReset(); post.mockReset();
	get.mockResolvedValue({ data: { projects: [{ id: "notes", name: "Notes", kind: "single_repo", path: "/projects/notes", sessionPrefix: "notes" }, { id: "workspace", name: "Workspace", kind: "workspace", path: "/projects", sessionPrefix: "workspace" }] } });
	post.mockResolvedValue({ data: project(true) });
});

describe("Generic project discussion and planning", () => {
	it("records explicit option, registered project and reason without approving execution", async () => {
		mount(project());
		expect(screen.getByText("Project starting options — proposals")).toBeInTheDocument();
		expect(screen.getByText("Not verified")).toBeInTheDocument();
		expect(screen.getByText("Steward observation")).toBeInTheDocument();
		await userEvent.click(screen.getByLabelText("Start notes from zero"));
		const dropdown = screen.getByRole("combobox", { name: "Registered project" });
		await waitFor(() => expect(dropdown).toBeEnabled());
		await userEvent.click(dropdown);
		expect(screen.queryByRole("option", { name: /Workspace/ })).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("option", { name: "Notes · /projects/notes" }));
		await userEvent.type(screen.getByLabelText("Reason for this choice"), "No inherited code is needed");
		await userEvent.type(screen.getByLabelText("Answer questions or revise the proposal before stage handoff"), "Use this source for local notes");
		await userEvent.dblClick(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post).toHaveBeenCalledWith("/api/v1/cleardev/products/{id}/discussions", { params: { path: { id: "product" } }, body: {
			requestId: expect.any(String), expectedPreviousId: "proposal-round", message: "Use this source for local notes", choice: { optionKey: "empty", aoProjectId: "notes", reason: "No inherited code is needed" },
		} });
		expect(screen.queryByRole("button", { name: /^approve|start execution$/i })).not.toBeInTheDocument();
	});

	it("allows correcting a rejected source choice without reusing its immutable request ID", async () => {
		post.mockResolvedValueOnce({ error: { code: "PRODUCT_SOURCE_NOT_EMPTY", message: "Choose an empty source" } });
		mount(project());
		await userEvent.click(screen.getByLabelText("Start notes from zero"));
		const dropdown = screen.getByRole("combobox", { name: "Registered project" });
		await waitFor(() => expect(dropdown).toBeEnabled());
		await userEvent.click(dropdown);
		await userEvent.click(screen.getByRole("option", { name: "Notes · /projects/notes" }));
		await userEvent.type(screen.getByLabelText("Reason for this choice"), "Choose this source");
		await userEvent.type(screen.getByLabelText("Answer questions or revise the proposal before stage handoff"), "Use this repository");
		await userEvent.click(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		await userEvent.click(await screen.findByRole("button", { name: "Edit rejected choice" }));
		expect(screen.getByLabelText("Start notes from zero")).toBeEnabled();
		await userEvent.click(screen.getByLabelText("Continue discussing without choosing"));
		await userEvent.click(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
		expect(post.mock.calls[1][1].body).not.toHaveProperty("choice");
		expect(post.mock.calls[1][1].body.requestId).not.toBe(post.mock.calls[0][1].body.requestId);
	});

	it("preserves the exact request when a network failure leaves persistence unknown", async () => {
		post.mockRejectedValueOnce(new Error("connection lost"));
		mount(project());
		await userEvent.type(screen.getByLabelText("Answer questions or revise the proposal before stage handoff"), "Discuss without choosing");
		await userEvent.click(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		const retry = await screen.findByRole("button", { name: "Retry the same request" });
		expect(screen.queryByRole("button", { name: "Edit rejected choice" })).not.toBeInTheDocument();
		expect(screen.getByLabelText("Start notes from zero")).toBeDisabled();
		await userEvent.click(retry);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
		expect(post.mock.calls[1][1].body).toEqual(post.mock.calls[0][1].body);
	});

	it("offers an explicit new choice from the original proposal after an interrupted investigation", async () => {
		const view = project();
		view.selection = binding;
		view.phase = "BLOCKED";
		view.reason = "PRODUCT_BASELINE_CHANGED";
		view.discussions.push({ id: "failed-round", productId: "product", ordinal: 1, protocolVersion: 2, userMessage: "Use the previous source", selection: binding, failureReason: "PRODUCT_STEWARD_WORKSPACE_CHANGED", createdAt, settledAt: createdAt });
		mount(view);
		expect(screen.getByText(/The previous investigation was stopped/)).toBeInTheDocument();
		expect(screen.queryByLabelText("Keep the saved choice and discuss")).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Continue discussion with Steward" })).toBeDisabled();
		await userEvent.click(screen.getByLabelText("Start notes from zero"));
		const dropdown = screen.getByRole("combobox", { name: "Registered project" });
		await waitFor(() => expect(dropdown).toBeEnabled());
		await userEvent.click(dropdown);
		await userEvent.click(screen.getByRole("option", { name: "Notes · /projects/notes" }));
		await userEvent.type(screen.getByLabelText("Reason for this choice"), "Use the current source explicitly");
		await userEvent.type(screen.getByLabelText("Answer questions or revise the proposal before stage handoff"), "Continue from this new version");
		await userEvent.click(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0][1].body.expectedPreviousId).toBe("failed-round");
		expect(post.mock.calls[0][1].body.choice).toMatchObject({ optionKey: "empty", aoProjectId: "notes" });
	});

	it("keeps the original proposal available after a rejected protocol reply", async () => {
		const view = project();
		view.selection = binding;
		view.sourceCurrent = true;
		view.phase = "BLOCKED";
		view.reason = "PRODUCT_DISCOVERY_INVALID";
		view.discussions.push({ id: "failed-round", productId: "product", ordinal: 1, protocolVersion: 2, userMessage: "Choose for me", selection: binding, failureReason: "PRODUCT_DISCOVERY_INVALID", createdAt, settledAt: createdAt });
		mount(view);
		expect(screen.getByText(/Steward’s reply did not match/)).toBeInTheDocument();
		expect(screen.getByLabelText("Keep the saved choice and discuss")).toBeVisible();
		await userEvent.type(screen.getByLabelText("Answer questions or revise the proposal before stage handoff"), "Plan the next observable outcome");
		await userEvent.click(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0][1].body.expectedPreviousId).toBe("failed-round");
		expect(post.mock.calls[0][1].body).not.toHaveProperty("choice");
	});

	it("allows discussion without a forced source choice or question", async () => {
		mount(project());
		await userEvent.type(screen.getByLabelText("Answer questions or revise the proposal before stage handoff"), "Explain the persistence tradeoff first");
		await userEvent.click(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0][1].body).not.toHaveProperty("choice");
	});

	it("permits planning a capability-blocked stage and displays proposed checks as proposals", async () => {
		mount(project(true));
		expect(screen.getByTestId("project-selection")).toHaveTextContent(binding.reason);
		expect(screen.getByTestId("project-selection")).toHaveTextContent(binding.baseCommitSha);
		const basis = screen.getByTestId("project-execution-basis");
		await userEvent.click(within(basis).getByText("Proposed project execution basis"));
		expect(within(basis).getByText("Proposed checks (not results)")).toBeVisible();
		expect(within(basis).getByText('["npm","test"]')).toBeVisible();
		await userEvent.click(screen.getByRole("button", { name: "Prepare stage for planning" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0][1].body).toEqual({ definitionSha256: "b".repeat(64) });
	});

	it("shows a saved plan as non-executable and follows the selected project", async () => {
		const view = project(true);
		view.stages[0].stage.developmentRequirementId = "notes-child";
		view.stages[0].phase = "BLOCKED";
		view.stages[0].progress = { projectPlanning: { productId: "product", stageId: "stage", discussionId: "selected-round", latestDiscussionId: "selected-round", baseCommitSha: binding.baseCommitSha, current: true, sourceCurrent: true, planReady: true, executionAvailable: false, reasonCode: "PROJECT_EXECUTION_NOT_AVAILABLE" } } as Product["controlProgress"];
		const { onOpenProject } = mount(view);
		expect(screen.getByText("Engineering plan saved — execution not available")).toBeInTheDocument();
		expect(screen.getByTestId("planning-child-actions")).toHaveTextContent("notes/notes-child");
		await userEvent.click(screen.getByRole("button", { name: "View stage in selected project" }));
		expect(onOpenProject).toHaveBeenCalledWith("notes");
		expect(screen.queryByText(/Stage acceptance: completed/)).not.toBeInTheDocument();
	});

	it("keeps superseded stages visible without current confirmation controls", () => {
		const view = project(true);
		view.stages[0].current = false;
		view.stages[0].phase = "SUPERSEDED";
		view.stages[0].stage.developmentRequirementId = "old-child";
		mount(view);
		expect(screen.getByText("Superseded stages and planning history")).toBeInTheDocument();
		expect(screen.getByText("Historical stage — superseded")).toBeInTheDocument();
		expect(screen.queryByTestId("planning-child-actions")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Prepare stage for planning" })).not.toBeInTheDocument();
	});

	it("blocks silent inheritance and preparation after a source change", () => {
		const view = project(true);
		view.sourceCurrent = false; view.phase = "BLOCKED"; view.reason = "PRODUCT_BASELINE_CHANGED";
		mount(view);
		expect(screen.getByRole("alert")).toHaveTextContent("selected repository or code version has changed");
		expect(screen.getByRole("button", { name: "Continue discussion with Steward" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Prepare stage for planning" })).toBeDisabled();
		expect(screen.getByLabelText("Start notes from zero")).toBeEnabled();
	});
});
