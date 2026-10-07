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
vi.mock("./ClearDevOperate", () => ({ ClearDevRequirementActions: ({ requirementId }: { requirementId: string }) => <div data-testid="child-actions">{requirementId}</div> }));
import { ClearDevProducts, ProductCard } from "./ClearDevProducts";

type Product = components["schemas"]["ClearDevProductView"];

function proposal(): Product {
	return {
		goal: { id: "product", aoProjectId: "project", name: "Local mail", goalText: "Manage mail locally first", requestId: "request", createdAt: "2026-09-22T00:00:00Z" },
		phase: "READY", remainingDiscussions: 10, stewardSessionId: "steward", canDiscuss: true, sourceCurrent: false,
		// These existing control facts are rendered by the separately tested progress UI.
		controlProgress: {} as Product["controlProgress"],
		messageBudget: { budgetVersion: "MESSAGE_BUDGET_V1", confirmedSentMessages: 2, reservedMessages: 2, maxMessages: null, remainingMessages: null, roles: [], steps: [] },
		discussions: [{ id: "round-2", productId: "product", ordinal: 1, userMessage: "Local first", createdAt: "2026-09-22T00:00:01Z", result: {
			schemaVersion: 1, kind: "PRODUCT_DISCOVERY", outcome: "READY", message: "Use two outcome-oriented stages.", feasibilitySummary: "The current app has a local contacts API.", questions: [],
			features: [{ key: "search", title: "Contact search", description: "Find saved contacts" }, { key: "accounts", title: "Connect accounts", description: "Real mailbox connection" }], stages: [],
		} }],
		stages: [
			{ current: true, planningOnly: false, phase: "PLANNED", stage: { id: "local", productId: "product", discussionId: "round-2", ordinal: 0, definitionSha256: "a".repeat(64), createdAt: "2026-09-22T00:00:01Z", definition: {
				key: "local", title: "Local search", goal: "Find contacts locally", featureKeys: ["search"], acceptanceCriteria: ["Typing finds matching saved contacts"], nonGoals: ["No real account connection"], feasibility: "SUPPORTED", feasibilityReason: "Local API exists",
			} } },
			{ current: true, planningOnly: false, phase: "NEEDS_CAPABILITY", stage: { id: "remote", productId: "product", discussionId: "round-2", ordinal: 1, definitionSha256: "b".repeat(64), createdAt: "2026-09-22T00:00:01Z", definition: {
				key: "remote", title: "Account connection", goal: "Connect mailboxes", featureKeys: ["accounts"], acceptanceCriteria: ["A mailbox can be connected"], nonGoals: [], feasibility: "NEEDS_CAPABILITY", feasibilityReason: "Authentication is not enabled",
			} } },
		],
	};
}
function mount(children: React.ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	return { ...render(<QueryClientProvider client={client}>{children}</QueryClientProvider>), client };
}
beforeEach(() => {
	get.mockReset(); post.mockReset();
	get.mockResolvedValue({ data: { aoProjectId: "project", products: [], execution: {
		agent: "codex", model: "legacy-model", legacy: true, known: true, toolLocked: true, modelLocked: true,
	} } });
});

describe("project-wide execution choice", () => {
	it("atomically submits OpenCode and its model with the first discussion and preserves unknown-response retries", async () => {
		get.mockImplementation(async (path: string) => ({ data: path.includes("/models") ? { models: [] } : {
			aoProjectId: "project", products: [], execution: { agent: "codex", model: "", legacy: true, known: true, toolLocked: false, modelLocked: false },
		} }));
		post.mockRejectedValueOnce(new Error("connection lost")).mockResolvedValueOnce({ data: proposal() });
		mount(<ClearDevProducts projectId="project" />);
		const tool = await screen.findByRole("combobox", { name: "Execution tool" });
		await userEvent.click(tool);
		await userEvent.click(screen.getByRole("option", { name: "OpenCode" }));
		await userEvent.type(screen.getByRole("textbox", { name: "Execution model" }), "example/model");
		await userEvent.type(screen.getByLabelText("Product name"), "Local counter");
		await userEvent.type(screen.getByLabelText("Product goal and intended use"), "A small counter");
		await userEvent.dblClick(screen.getByRole("button", { name: "Start product discussion" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0]).toEqual(["/api/v1/cleardev/products", { body: expect.objectContaining({
			execution: { agent: "opencode", model: "example/model" },
		}) }]);
		expect(screen.getByRole("textbox", { name: "Execution model" })).toBeDisabled();
		await userEvent.click(screen.getByRole("button", { name: "Retry the same request" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
		expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
	});

	it("does not offer a cross-tool switch for an existing Codex project", async () => {
		get.mockResolvedValue({ data: { aoProjectId: "project", products: [], execution: {
			agent: "codex", model: "legacy-model", legacy: true, known: true, toolLocked: true, modelLocked: true,
		} } });
		mount(<ClearDevProducts projectId="project" />);
		expect(await screen.findByTestId("cleardev-execution-choice")).toHaveTextContent("legacy-model");
		expect(screen.queryByRole("combobox", { name: "Execution tool" })).not.toBeInTheDocument();
		expect(post).not.toHaveBeenCalled();
	});
});

describe("Steward product discovery", () => {
	it("hides cached stage preparation after the product refresh fails", async () => {
		get.mockResolvedValueOnce({ data: { aoProjectId: "project", products: [proposal()] } }).mockResolvedValue({ error: new Error("offline") });
		const { client } = mount(<ClearDevProducts projectId="project" />);
		expect(await screen.findByRole("heading", { name: /Local search/ })).toBeInTheDocument();
		await client.invalidateQueries({ queryKey: ["cleardev-products", "project"] }).catch(() => undefined);
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("offline"));
		expect(screen.queryByRole("button", { name: "Prepare stage for specification confirmation" })).not.toBeInTheDocument();
	});
	it("submits a product goal, not an engineering task or approval", async () => {
		post.mockResolvedValue({ data: proposal() });
		mount(<ClearDevProducts projectId="project" />);
		await userEvent.type(screen.getByLabelText("Product name"), "Mail software");
		await userEvent.type(screen.getByLabelText("Product goal and intended use"), "Help me choose stages");
		await userEvent.dblClick(screen.getByRole("button", { name: "Start product discussion" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post).toHaveBeenCalledWith("/api/v1/cleardev/products", { body: expect.objectContaining({ aoProjectId: "project", requestId: expect.any(String), name: "Mail software", goalText: "Help me choose stages" }) });
		expect(screen.queryByRole("button", { name: /^approve$/i })).not.toBeInTheDocument();
	});

	it("unknown create retries reuse the exact request and input", async () => {
		post.mockRejectedValueOnce(new Error("connection lost")).mockResolvedValueOnce({ data: proposal() });
		mount(<ClearDevProducts projectId="project" />);
		await userEvent.type(screen.getByLabelText("Product name"), "Mail");
		await userEvent.type(screen.getByLabelText("Product goal and intended use"), "Local first");
		await userEvent.click(screen.getByRole("button", { name: "Start product discussion" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("connection lost");
		expect(screen.getByLabelText("Product name")).toBeDisabled();
		await userEvent.click(screen.getByRole("button", { name: "Retry the same request" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
		expect(post.mock.calls[1]).toEqual(post.mock.calls[0]);
	});

	it("prepares only a supported stage and binds the immutable definition hash", async () => {
		post.mockResolvedValue({ data: proposal() });
		mount(<ProductCard view={proposal()} projectId="project" onSaved={vi.fn()} />);
		const stages = screen.getAllByTestId("cleardev-product-stage");
		expect(within(stages[0]).getByText("Typing finds matching saved contacts")).toBeInTheDocument();
		expect(within(stages[1]).queryByRole("button")).not.toBeInTheDocument();
		await userEvent.dblClick(within(stages[0]).getByRole("button", { name: "Prepare stage for specification confirmation" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post).toHaveBeenCalledWith("/api/v1/cleardev/products/{id}/stages/{stageId}/prepare", { params: { path: { id: "product", stageId: "local" } }, body: { definitionSha256: "a".repeat(64) } });
	});

	it("keeps old discussion history and sends answers against the current round", async () => {
		const view = proposal();
		view.discussions.unshift({ id: "round-1", productId: "product", ordinal: 0, userMessage: "Original broad goal", createdAt: "2026-09-22T00:00:00Z" });
		post.mockResolvedValue({ data: view });
		mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
		expect(screen.getByText("Original broad goal")).toBeInTheDocument();
		await userEvent.type(screen.getByLabelText("Answer questions or revise the proposal before stage handoff"), "Keep account connection for later");
		await userEvent.click(screen.getByRole("button", { name: "Continue discussion with Steward" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0][1].body).toEqual({ requestId: expect.any(String), expectedPreviousId: "round-2", message: "Keep account connection for later" });
	});

	it("freezes editing after handoff and derives stage state from the linked requirement", () => {
		const view = proposal();
		view.phase = "FROZEN";
		view.stages[0].phase = "AWAITING_CONFIRMATION";
		view.stages[0].stage.developmentRequirementId = "child-requirement";
		mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
		expect(screen.queryByRole("button", { name: "Continue discussion with Steward" })).not.toBeInTheDocument();
		expect(screen.getByTestId("child-actions")).toHaveTextContent("child-requirement");
		expect(screen.getByText("Waiting for your approval")).toBeInTheDocument();
	});
});

describe("Steward discussion disclosures", () => {
	it("keeps a draft when the compact new-discussion form is closed and reopened", async () => {
		get.mockResolvedValue({ data: { aoProjectId: "project", products: [proposal()] } });
		mount(<ClearDevProducts projectId="project" />);
		const open = await screen.findByRole("button", { name: "New discussion" });
		expect(screen.getByLabelText("Product name")).not.toBeVisible();
		await userEvent.click(open);
		await userEvent.type(screen.getByLabelText("Product name"), "Keep my draft");
		await userEvent.click(screen.getByRole("button", { name: "Close" }));
		await userEvent.click(screen.getByRole("button", { name: "New discussion" }));
		expect(screen.getByLabelText("Product name")).toHaveValue("Keep my draft");
		expect(post).not.toHaveBeenCalled();
	});

	it("keeps questions and source warnings visible while original text and acceptance details remain inspectable", async () => {
		const view = proposal();
		view.phase = "AWAITING_INPUT";
		view.selection = { sourceDiscussionId: "round-2", option: { key: "local", title: "Existing repository", origin: "EXISTING", description: "Local", tradeoffs: [] }, reason: "Keep existing data", aoProjectId: "project", repositoryPath: "/project", repositoryUrl: "", baseCommitSha: "a".repeat(40), createdAt: "2026-09-22T00:00:00Z" };
		view.discussions[0].result!.questions = [{ key: "retention", text: "Keep archived contacts?", reason: "This affects the scope." }];
		mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
		expect(screen.getByText("Keep archived contacts?")).toBeVisible();
		expect(screen.getByRole("alert")).toBeVisible();
		expect(screen.getByText(view.goal.goalText)).not.toBeVisible();
		await userEvent.click(screen.getByText("Original goal"));
		expect(screen.getByText(view.goal.goalText)).toBeVisible();
		const acceptance = screen.getAllByTestId("cleardev-stage-acceptance")[0];
		expect(within(acceptance).getByText("Typing finds matching saved contacts")).not.toBeVisible();
		await userEvent.click(within(acceptance).getByText("Functional acceptance", { exact: false }));
		expect(within(acceptance).getByText("Typing finds matching saved contacts")).toBeVisible();
		expect(post).not.toHaveBeenCalled();
	});
});


it("keeps exhausted discussion capacity visible when the discussion form is unavailable", () => {
	const view = proposal();
	view.remainingDiscussions = 0;
	view.canDiscuss = false;
	mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
	expect(screen.getByText(/Remaining discussion rounds: 0/)).toBeVisible();
	expect(screen.queryByRole("button", { name: "Continue discussion with Steward" })).not.toBeInTheDocument();
});


it("labels a stage waiting for human intervention without exposing its internal enum", () => {
	const view = proposal();
	view.stages[0].phase = "NEEDS_HUMAN";
	mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
	const stage = screen.getAllByTestId("cleardev-product-stage")[0];
	expect(within(stage).getByRole("status")).toHaveTextContent("Needs a person");
	expect(within(stage).queryByText("NEEDS_HUMAN")).not.toBeInTheDocument();
});

it("waits for final stage delivery before offering a later generic plan", async () => {
	const view = proposal();
	view.sourceCurrent = true;
	for (const stage of view.stages) { stage.planningOnly = true; stage.phase = "PLANNED"; }
	const { rerender } = mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
	const stages = screen.getAllByTestId("cleardev-product-stage");
	expect(within(stages[0]).getByRole("button", { name: "Prepare stage for planning" })).toBeEnabled();
	const later = within(stages[1]).getByRole("button", { name: "Prepare stage for planning" });
	expect(later).toBeDisabled();
	expect(within(stages[1]).getByText(/Wait for the previous stage to pass final acceptance/)).toBeVisible();
	await userEvent.click(later);
	expect(post).not.toHaveBeenCalled();
	// Task completion alone leaves the predecessor developing.
	view.stages[0].phase = "DEVELOPING";
	rerender(<QueryClientProvider client={new QueryClient()}><ProductCard view={{ ...view }} projectId="project" onSaved={vi.fn()} /></QueryClientProvider>);
	expect(screen.getAllByRole("button", { name: "Prepare stage for planning" })[1]).toBeDisabled();
});

it("shows proposed defaults separately without declaring the discussion complete", () => {
	const view = proposal();
	const result = view.discussions[0].result!;
	result.schemaVersion = 2;
	result.stages = view.stages.map((stage) => stage.stage.definition);
	result.evidence = [{ status: "ASSUMPTION", claim: "Costs use weighted averages", source: "Steward proposal, not a user decision" }, { status: "UNVERIFIED", claim: "Installability unknown", source: "No tools yet" }];
	mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
	const check = within(screen.getByTestId("cleardev-discussion-readiness"));
	expect(check.getByText("Costs use weighted averages")).toBeVisible();
	expect(check.getByText("Steward proposal, not a user decision")).toBeVisible();
	expect(check.getByText(/Questions to answer: 0.*Proposed acceptance conditions: 2/)).toBeVisible();
	expect(check.getByText(/does not prove that discussion is complete/)).toBeVisible();
	expect(check.queryByText("Installability unknown")).not.toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Continue discussion with Steward" })).toBeEnabled();
	expect(post).not.toHaveBeenCalled();
});

it("does not infer no assumptions from an empty list or reopen a frozen product", () => {
	const view = proposal();
	view.phase = "FROZEN";
	mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
	const check = within(screen.getByTestId("cleardev-discussion-readiness"));
	expect(check.getByText(/No defaults were listed separately/)).toBeVisible();
	expect(check.getByText(/Confirmed specifications and delivery status/)).toBeVisible();
	expect(screen.queryByRole("button", { name: "Continue discussion with Steward" })).not.toBeInTheDocument();
	expect(post).not.toHaveBeenCalled();
});

it("keeps unresolved questions visible during product discussion", () => {
	const view = proposal();
	view.phase = "AWAITING_INPUT";
	view.discussions[0].result!.outcome = "DISCUSS";
	view.discussions[0].result!.questions = [{ key: "cost", text: "Which cost rule?", reason: "This changes the reported margin" }];
	mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
	expect(screen.getByText("Which cost rule?")).toBeVisible();
	const check = within(screen.getByTestId("cleardev-discussion-readiness"));
	expect(check.getByText(/proposal is still being discussed/)).toBeVisible();
	expect(check.getByText(/Questions to answer: 1/)).toBeVisible();
	expect(post).not.toHaveBeenCalled();
});

it("offers original-step recovery for an unsettled Steward turn without a dead discussion form", async () => {
	const view = proposal();
	view.phase = "BLOCKED";
	view.canDiscuss = false;
	view.reason = "PRODUCT_STEWARD_UNAVAILABLE";
	view.discussions.push({
		id: "round-3", productId: "product", ordinal: 2, userMessage: "Environment repaired", createdAt: "2026-09-22T00:00:02Z",
		protocolVersion: 2,
	});
	get.mockResolvedValue({ data: { executionRunId: "", history: [], options: [{ action: "RETRY_PLANNING_STEP", targetId: "old-step", role: "STEWARD", reason: "PRODUCT_STEWARD_UNAVAILABLE", summary: "provider unavailable" }] } });
	mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
	expect(await screen.findByRole("button", { name: "Continue this discussion round" })).toBeEnabled();
	expect(screen.getByText(/no new product is needed/)).toBeVisible();
	expect(screen.queryByLabelText("Answer questions or revise the proposal before stage handoff")).not.toBeInTheDocument();
	expect(post).not.toHaveBeenCalled();
});


it("keeps one whole-plan confirmation when the discussion form survives refreshes", () => {
 const view = proposal();
 view.stages[0].stage.definition.executionBasis = { writePaths: ["src/**"], dependencyNeeds: [], checks: [], launch: {description: "No service", workingDirectory: ".", argv: []} } as NonNullable<Product["stages"][number]["stage"]["definition"]["executionBasis"]>;
 const client = new QueryClient({defaultOptions:{queries:{retry:false}}});
 const content = () => <QueryClientProvider client={client}><ProductCard view={{...view}} projectId="project" onSaved={vi.fn()} /></QueryClientProvider>;
 const {rerender} = render(content());
 expect(screen.getByLabelText("Answer questions or revise the proposal before stage handoff")).toBeVisible();
 for (let i=0;i<4;i++) rerender(content());
 expect(screen.getAllByTestId("cleardev-product-plan")).toHaveLength(1);
 expect(screen.getAllByRole("button", {name:"Confirm plan and develop stages automatically"})).toHaveLength(1);
 expect(post).not.toHaveBeenCalled();
});

describe("project source preparation", () => {
 it("shows normal preparation without sending a retry or offering stage execution", () => {
  const view = proposal(); view.phase = "PREPARING_SOURCE"; view.canDiscuss = false;
  view.sourcePreparation = { state: "PENDING", sourceUrl: "https://example.org/source.git", input: { requestId: "source-1", expectedPreviousId: "round-2", message: "use source" } };
  mount(<ProductCard view={view} projectId="project" onSaved={vi.fn()} />);
  expect(screen.getByTestId("cleardev-source-preparation")).toHaveTextContent("Preparing project source");
  expect(screen.queryByRole("button", { name: "Retry source preparation" })).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Prepare stage for specification confirmation" })).toBeDisabled();
  expect(post).not.toHaveBeenCalled();
 });
 it("retains the exact saved request after a failed retry and reload", async () => {
  const view = proposal(); view.phase = "SOURCE_PREPARATION_FAILED"; view.canDiscuss = false;
  const input = { requestId: "source-1", expectedPreviousId: "round-2", message: "use source", choice: { optionKey: "upstream", aoProjectId: "project", reason: "reuse code" } };
  view.sourcePreparation = { state: "FAILED", sourceUrl: "https://example.org/source.git", failure: "git fetch failed: connection refused", input };
  post.mockRejectedValueOnce(new Error("response lost")).mockResolvedValueOnce({ data: view });
  const saved = vi.fn();
  const mounted = mount(<ProductCard view={view} projectId="project" onSaved={saved} />);
  expect(screen.getByTestId("cleardev-source-preparation")).toHaveTextContent("connection refused");
  await userEvent.click(screen.getByRole("button", { name: "Retry source preparation" }));
  await waitFor(() => expect(saved).toHaveBeenCalled());
  mounted.unmount();
  mount(<ProductCard view={view} projectId="project" onSaved={saved} />);
  await userEvent.click(screen.getByRole("button", { name: "Retry source preparation" }));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[0][1].body).toEqual(input);
  expect(post.mock.calls[1][1].body).toEqual(input);
 });
});
