import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";

const { get, post, put } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get, POST: post, PUT: put }, hasTrustedApiBaseUrl: () => true,
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
}));
import { ClearDevProjectDelivery } from "./ClearDevProjectDelivery";

type Product = components["schemas"]["ClearDevProductView"];
type Preview = components["schemas"]["ClearDevResultPreviewView"];
const sha = "d".repeat(40);
const displayed: Preview = { candidateSha: sha, sourceBranch: "cleardev-delivered", dataReady: true, preview: { sessionId: "builder-session", state: "stopped", logs: [] } };
// A focused API double, not a model-created delivery or a live desktop proof.
function product(selected = false): Product {
	return {
		goal: { id: "product", aoProjectId: "notes" }, canDiscuss: true, sourceCurrent: true, remainingDiscussions: 8,
		discussions: [{ id: selected ? "selection-request" : "discussion", ordinal: selected ? 2 : 1 }],
		selection: {
			aoProjectId: "notes", option: { key: "empty" }, baseCommitSha: selected ? sha : "a".repeat(40),
			...(selected ? { delivery: { requirementId: "req", candidateSha: sha, executionRunId: "run", resultId: "result", integrationCandidateId: "integration" } } : {}),
		},
	} as unknown as Product;
}
function mount(preview: Preview | undefined = displayed) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 }, mutations: { retry: false } } });
	const node = (value?: Preview) => <QueryClientProvider client={client}><ClearDevProjectDelivery productId="product" projectId="notes" requirementId="req" preview={value} /></QueryClientProvider>;
	const mounted = render(node(preview));
	return { client, updatePreview: (value?: Preview) => mounted.rerender(node(value)) };
}
beforeEach(() => {
	get.mockReset(); post.mockReset(); put.mockReset();
	get.mockImplementation(async (path: string) => ({ data: path.endsWith("result-preview") ? displayed : product() }));
	post.mockResolvedValue({ data: product(true) });
});

describe("Selecting a generic final delivery", () => {
	it("requires an inherited delivery's data migration before another continuation", async () => {
		const continued = product();
		continued.selection!.delivery = { requirementId: "prior", candidateSha: "a".repeat(40), executionRunId: "prior-run", resultId: "prior-result", integrationCandidateId: "prior-integration" };
		get.mockImplementation(async (path: string) => ({ data: path.endsWith("result-preview") ? displayed : continued }));
		const { updatePreview } = mount({ ...displayed, dataReady: false });
		const button = screen.getByRole("button", { name: "Use this delivery for the next stage" });
		await screen.findByText(/Open this delivery once and complete its data migration/);
		expect(button).toBeDisabled();
		updatePreview(displayed);
		await waitFor(() => expect(button).toBeEnabled());
		expect(post).not.toHaveBeenCalled();
	});

	it("submits one immutable product choice, never a default-branch update", async () => {
		let chosen = false;
		get.mockImplementation(async (path: string) => ({ data: path.endsWith("result-preview") ? displayed : product(chosen) }));
		post.mockImplementation(async () => { chosen = true; return { data: product(true) }; });
		mount();
		const button = await screen.findByRole("button", { name: "Use this delivery for the next stage" });
		await waitFor(() => expect(button).toBeEnabled());
		expect(post).not.toHaveBeenCalled();
		fireEvent.click(button); fireEvent.click(button);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post.mock.calls[0]).toEqual(["/api/v1/cleardev/products/{id}/discussions", {
			params: { path: { id: "product" } }, body: {
				requestId: expect.any(String), expectedPreviousId: "discussion", message: expect.stringContaining(sha),
				choice: { optionKey: "empty", aoProjectId: "notes", reason: expect.any(String), expectedBaseCommitSha: sha, deliveryRequirementId: "req" },
			},
		}]);
		expect(put).not.toHaveBeenCalled();
		expect(await screen.findByText(/This exact delivery is the selected baseline/)).toBeInTheDocument();
		expect(button).toBeDisabled();
	});

	it("retains the same request after an unknown response", async () => {
		post.mockRejectedValueOnce(new Error("connection lost"));
		mount();
		const button = screen.getByRole("button", { name: "Use this delivery for the next stage" });
		await waitFor(() => expect(button).toBeEnabled());
		fireEvent.click(button);
		const retry = await screen.findByRole("button", { name: "Retry the same delivery selection" });
		await waitFor(() => expect(retry).toBeEnabled());
		expect(post).toHaveBeenCalledTimes(1);
		expect(screen.queryByText(/This exact delivery is the selected baseline/)).not.toBeInTheDocument();
		fireEvent.click(retry);
		await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
		expect(post.mock.calls[1][1].body).toEqual(post.mock.calls[0][1].body);
		expect(put).not.toHaveBeenCalled();
	});

	it("refuses a different final candidate observed at click time", async () => {
		get.mockImplementation(async (path: string) => ({ data: path.endsWith("result-preview") ? { ...displayed, candidateSha: "e".repeat(40) } : product() }));
		mount();
		const button = screen.getByRole("button", { name: "Use this delivery for the next stage" });
		await waitFor(() => expect(button).toBeEnabled());
		fireEvent.click(button);
		expect(await screen.findByRole("alert")).toHaveTextContent("final delivery changed");
		expect(post).not.toHaveBeenCalled();
		expect(put).not.toHaveBeenCalled();
	});

	it("hides a cached selection and disables writes after product refresh fails", async () => {
		let broken = false;
		get.mockImplementation(async (path: string) => path.endsWith("result-preview") ? { data: displayed } : broken ? { error: new Error("product disconnected") } : { data: product(true) });
		const { client } = mount();
		await screen.findByText(/This exact delivery is the selected baseline/);
		broken = true;
		await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-delivery-choice", "product"] }); });
		expect(await screen.findByRole("alert")).toHaveTextContent("product disconnected");
		expect(screen.queryByText(/This exact delivery is the selected baseline/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Use this delivery for the next stage" })).toBeDisabled();
		expect(post).not.toHaveBeenCalled();
	});

	it("does not treat a valid product selection as a valid stale preview", async () => {
		get.mockResolvedValue({ data: product(true) });
		const { updatePreview } = mount();
		await screen.findByText(/This exact delivery is the selected baseline/);
		updatePreview(undefined);
		expect(screen.queryByText(/This exact delivery is the selected baseline/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Use this delivery for the next stage" })).toBeDisabled();
		expect(post).not.toHaveBeenCalled();
	});

	it("does not rebind an uncertain selection to a later discussion", async () => {
		let later = false;
		post.mockRejectedValueOnce(new Error("unknown persistence"));
		get.mockImplementation(async (path: string) => ({ data: path.endsWith("result-preview") ? displayed : { ...product(), discussions: [{ id: later ? "later-discussion" : "discussion" }] } }));
		const { client } = mount();
		const button = screen.getByRole("button", { name: "Use this delivery for the next stage" });
		await waitFor(() => expect(button).toBeEnabled());
		fireEvent.click(button);
		const retry = await screen.findByRole("button", { name: "Retry the same delivery selection" });
		await waitFor(() => expect(retry).toBeEnabled());
		later = true;
		await act(async () => { await client.invalidateQueries({ queryKey: ["cleardev-delivery-choice", "product"] }); });
		await waitFor(() => expect(screen.getByRole("button", { name: "Retry the same delivery selection" })).toBeDisabled());
		expect(post).toHaveBeenCalledTimes(1);
	});
});
