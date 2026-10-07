import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { get, post, put, del, openExternal } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn(), openExternal: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get, POST: post, PUT: put, DELETE: del }, hasTrustedApiBaseUrl: () => true,
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : String((error as { message?: string })?.message ?? "request failed"),
}));
vi.mock("../lib/bridge", () => ({ aoBridge: { app: { openExternal } } }));
import { ClearDevNewRequirement, ClearDevRequirementActions, isLocalResultURL } from "./ClearDevOperate";

function wrapper(children: React.ReactNode) {
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	return { ...render(<QueryClientProvider client={client}>{children}</QueryClientProvider>), client };
}
beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset(); openExternal.mockReset(); });

describe("ClearDev independent operations", () => {
	it("submits plain input once through the existing complex requirement endpoint", async () => {
		post.mockResolvedValue({ data: { requirement: { id: "created" } } });
		wrapper(<ClearDevNewRequirement projectId="project-one" busy={false} />);
		await userEvent.type(screen.getByLabelText("Requirement name"), "Mail count");
		await userEvent.type(screen.getByLabelText("What to change, what not to change, and how to check it"), "Display contact count without changing data");
		await userEvent.dblClick(screen.getByRole("button", { name: "Submit to Steward" }));
		await screen.findByRole("status");
		expect(post).toHaveBeenCalledTimes(1);
		expect(post).toHaveBeenCalledWith("/api/v1/cleardev/requirements/complex", { body: { aoProjectId: "project-one", name: "Mail count", prdText: "Display contact count without changing data" } });
	});

	it("retains input after an unknown send and does not retry it", async () => {
		post.mockRejectedValue(new Error("connection lost"));
		wrapper(<ClearDevNewRequirement projectId="project-one" busy={false} />);
		await userEvent.type(screen.getByLabelText("Requirement name"), "Keep this input");
		await userEvent.type(screen.getByLabelText("What to change, what not to change, and how to check it"), "Small requirement");
		await userEvent.click(screen.getByRole("button", { name: "Submit to Steward" }));
		expect(await screen.findByRole("alert")).toHaveTextContent("Do not submit again blindly");
		expect(screen.getByLabelText("Requirement name")).toHaveValue("Keep this input");
		expect(screen.getByRole("button", { name: "Submit to Steward" })).toBeDisabled();
		expect(post).toHaveBeenCalledTimes(1);
	});

	it("binds clarification answers to the exact round and exposes no approval action", async () => {
		get.mockResolvedValue({ data: { requirement: { id: "req" }, trustedProgress: { phase: "AWAITING_CLARIFICATION" }, complexPlanning: {
			compilationRequests: [{ id: "old", clarificationRound: 0 }, { id: "current", clarificationRound: 1 }],
			questions: [{ compilationRequestId: "old", questionKey: "old", text: "Old question" }, { compilationRequestId: "current", questionKey: "placement", text: "Where to display it?" }],
		} } });
		post.mockResolvedValue({ data: {} });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="AWAITING_CLARIFICATION" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		await userEvent.type(await screen.findByLabelText("Where to display it?"), "Beside Contacts");
		expect(screen.queryByText("Old question")).not.toBeInTheDocument();
		await userEvent.click(screen.getByRole("button", { name: "Submit clarification answers" }));
		await waitFor(() => expect(post).toHaveBeenCalledWith("/api/v1/cleardev/requirements/{id}/complex-clarifications", { params: { path: { id: "req" } }, body: { compilationRequestId: "current", clarificationRound: 1, answers: [{ questionKey: "placement", text: "Beside Contacts" }] } }));
		expect(screen.queryByRole("button", { name: /approve|confirm/i })).not.toBeInTheDocument();
	});

	it("opens only the program-returned local result and stops without clearing completion", async () => {
		let state = "stopped";
		const data = () => ({ candidateSha: "a".repeat(40), preview: { sessionId: "builder", state, url: "http://127.0.0.1:4567/", logs: [] } });
		get.mockImplementation(async (path: string) => ({ data: path.endsWith("result-preview") ? data() : { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } }));
		post.mockImplementation(async () => { state = "ready"; return { data: data() }; });
		del.mockImplementation(async () => { state = "stopped"; return { data: data() }; });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		await userEvent.click(await screen.findByRole("button", { name: "Start delivered application" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Open application" })).toBeEnabled());
		await userEvent.click(screen.getByRole("button", { name: "Open application" }));
		expect(openExternal).toHaveBeenCalledWith("http://127.0.0.1:4567/");
		await userEvent.click(screen.getByRole("button", { name: "Stop application (keep data)" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Open application" })).toBeDisabled());
		expect(del).toHaveBeenCalledWith("/api/v1/cleardev/requirements/{id}/result-preview", { params: { path: { id: "req" } } });
	});

	it("revalidates a ready result before opening and disables stale cached readiness after rejection", async () => {
		let state = "stopped";
		let rejectPreview = false;
		const data = () => ({ candidateSha: "a".repeat(40), preview: { sessionId: "builder", state, url: "http://127.0.0.1:4567/", logs: [] } });
		get.mockImplementation(async (path: string) => {
			if (!path.endsWith("result-preview")) return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
			if (rejectPreview) return { error: new Error("RESULT_SOURCE_CHANGED") };
			return { data: data() };
		});
		post.mockImplementation(async () => { state = "ready"; return { data: data() }; });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		await userEvent.click(await screen.findByRole("button", { name: "Start delivered application" }));
		const open = screen.getByRole("button", { name: "Open application" });
		await waitFor(() => expect(open).toBeEnabled());
		rejectPreview = true;
		await userEvent.click(open);
		expect(await screen.findByText("RESULT_SOURCE_CHANGED")).toBeInTheDocument();
		expect(openExternal).not.toHaveBeenCalled();
		expect(open).toBeDisabled();
	});

	it("sends one preview start for a rapid double click even when the first response fails", async () => {
		get.mockImplementation(async (path: string) => {
			if (path.endsWith("result-preview")) return { data: { candidateSha: "a".repeat(40), preview: { state: "stopped", logs: [] } } };
			return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
		});
		post.mockResolvedValue({ error: new Error("start blocked") });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		await userEvent.dblClick(await screen.findByRole("button", { name: "Start delivered application" }));
		expect(await screen.findByText("start blocked")).toBeInTheDocument();
		expect(post).toHaveBeenCalledTimes(1);
	});

	it("selects only a freshly validated delivered baseline and preserves project settings", async () => {
		const sha = "a".repeat(40);
		const result = { candidateSha: sha, sourceBranch: "cleardev-delivery-1", preview: { sessionId: "builder", state: "ready", url: "http://127.0.0.1:4567/", logs: [] } };
		get.mockImplementation(async (path: string) => {
			if (path.endsWith("result-preview")) return { data: result };
			if (path === "/api/v1/projects/{id}") return { data: { status: "ok", project: { id: "p", kind: "single_repo", name: "Mail", config: { defaultBranch: "main", sessionPrefix: "keep-me" } } } };
			return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
		});
		put.mockResolvedValue({ data: {} });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		await userEvent.click(await screen.findByRole("button", { name: "Use this delivery for the next stage" }));
		await waitFor(() => expect(put).toHaveBeenCalledWith("/api/v1/projects/{id}", { params: { path: { id: "p" } }, body: { displayName: "Mail", config: { defaultBranch: "cleardev-delivery-1", sessionPrefix: "keep-me" } } }));
		expect(post).not.toHaveBeenCalled();
	});

	it("can select a completed delivery after its application was stopped", async () => {
		const result = { candidateSha: "a".repeat(40), sourceBranch: "cleardev-delivery-1", preview: { sessionId: "builder", state: "stopped", logs: [] } };
		get.mockImplementation(async (path: string) => {
			if (path.endsWith("result-preview")) return { data: result };
			if (path === "/api/v1/projects/{id}") return { data: { status: "ok", project: { id: "p", kind: "single_repo", name: "Mail", config: { defaultBranch: "main" } } } };
			return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
		});
		put.mockResolvedValue({ data: {} });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		await userEvent.click(await screen.findByRole("button", { name: "Use this delivery for the next stage" }));
		await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
		expect(post).not.toHaveBeenCalled();
	});

	it("hides a cached selected baseline when project refresh fails", async () => {
		let projectUnavailable = false;
		const sha = "a".repeat(40);
		get.mockImplementation(async (path: string) => {
			if (path.endsWith("result-preview")) return { data: { candidateSha: sha, sourceBranch: "cleardev-delivery-1", preview: { state: "stopped", logs: [] } } };
			if (path === "/api/v1/projects/{id}") return projectUnavailable ? { error: new Error("project offline") } : { data: { status: "ok", project: { id: "p", kind: "single_repo", name: "Mail", config: { defaultBranch: "cleardev-delivery-1" } } } };
			return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
		});
		const { client } = wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		expect(await screen.findByText(/This delivery is the selected baseline/)).toBeInTheDocument();
		projectUnavailable = true;
		await client.invalidateQueries({ queryKey: ["project", "p"] });
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("project offline"));
		expect(screen.queryByText(/This delivery is the selected baseline/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Use this delivery for the next stage" })).toBeDisabled();
	});

	it("hides a cached delivery SHA and selected baseline after source validation fails", async () => {
		let sourceChanged = false;
		const sha = "a".repeat(40);
		get.mockImplementation(async (path: string) => {
			if (path.endsWith("result-preview")) return sourceChanged ? { error: new Error("RESULT_SOURCE_CHANGED") } : { data: { candidateSha: sha, sourceBranch: "cleardev-delivery-1", preview: { state: "stopped", logs: [] } } };
			if (path === "/api/v1/projects/{id}") return { data: { status: "ok", project: { id: "p", kind: "single_repo", name: "Mail", config: { defaultBranch: "cleardev-delivery-1" } } } };
			return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
		});
		const { client } = wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		expect(await screen.findByText(sha)).toBeInTheDocument();
		expect(screen.getByText(/This delivery is the selected baseline/)).toBeInTheDocument();
		sourceChanged = true;
		await client.invalidateQueries({ queryKey: ["cleardev-result-preview", "req"] });
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("RESULT_SOURCE_CHANGED"));
		expect(screen.queryByText(sha)).not.toBeInTheDocument();
		expect(screen.queryByText(/This delivery is the selected baseline/)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Use this delivery for the next stage" })).toBeDisabled();
	});

	it("keeps Stop available after source validation fails and accepts one stop receipt", async () => {
		let sourceChanged = false;
		const sha = "a".repeat(40);
		get.mockImplementation(async (path: string) => {
			if (path.endsWith("result-preview")) return sourceChanged ? { error: new Error("RESULT_SOURCE_CHANGED") } : { data: { candidateSha: sha, sourceBranch: "cleardev-delivery-1", preview: { state: "ready", url: "http://127.0.0.1:4567/", logs: [] } } };
			if (path === "/api/v1/projects/{id}") return { data: { status: "ok", project: { id: "p", kind: "single_repo", name: "Mail", config: { defaultBranch: "main" } } } };
			return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
		});
		del.mockResolvedValue({ data: { candidateSha: sha, preview: { state: "stopped", logs: [] } } });
		const { client } = wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		expect(await screen.findByText(sha)).toBeInTheDocument();
		sourceChanged = true;
		await client.invalidateQueries({ queryKey: ["cleardev-result-preview", "req"] });
		expect(await screen.findByText("RESULT_SOURCE_CHANGED")).toBeInTheDocument();
		expect(screen.queryByText(sha)).not.toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Start delivered application" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Open application" })).toBeDisabled();
		expect(screen.getByRole("button", { name: "Use this delivery for the next stage" })).toBeDisabled();
		const stop = screen.getByRole("button", { name: "Stop application (keep data)" });
		expect(stop).toBeEnabled();
		await userEvent.dblClick(stop);
		await waitFor(() => expect(del).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(stop).toBeDisabled());
	});

	it("does not trust a stop receipt while independent source validation is pending or fails", async () => {
		let sourceChanged = false;
		let deferRefresh = false;
		let finishRefresh!: (value: { error: Error }) => void;
		const refresh = new Promise<{ error: Error }>((resolve) => { finishRefresh = resolve; });
		const sha = "a".repeat(40);
		const result = { candidateSha: sha, sourceBranch: "cleardev-delivery-1", preview: { state: "ready", url: "http://127.0.0.1:4567/", logs: [] } };
		get.mockImplementation(async (path: string) => {
			if (path.endsWith("result-preview")) {
				if (deferRefresh) return refresh;
				return sourceChanged ? { error: new Error("RESULT_SOURCE_CHANGED") } : { data: result };
			}
			if (path === "/api/v1/projects/{id}") return { data: { status: "ok", project: { id: "p", kind: "single_repo", config: { defaultBranch: result.sourceBranch } } } };
			return { data: { requirement: { id: "req" }, trustedProgress: { phase: "COMPLETED" } } };
		});
		del.mockResolvedValue({ data: { ...result, preview: { state: "stopped", logs: [] } } });
		const { client } = wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="COMPLETED" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		expect(await screen.findByText(sha)).toBeInTheDocument();
		sourceChanged = true;
		await client.invalidateQueries({ queryKey: ["cleardev-result-preview", "req"] });
		expect(await screen.findByText("RESULT_SOURCE_CHANGED")).toBeInTheDocument();
		deferRefresh = true;
		await userEvent.click(screen.getByRole("button", { name: "Stop application (keep data)" }));
		await waitFor(() => expect(del).toHaveBeenCalledTimes(1));
		const expectUnverified = () => {
			expect(screen.queryByText(sha)).not.toBeInTheDocument();
			expect(screen.getByTestId("cleardev-result-branch")).toBeEmptyDOMElement();
			expect(screen.queryByText(/This delivery is the selected baseline/)).not.toBeInTheDocument();
			for (const name of ["Start delivered application", "Open application", "Use this delivery for the next stage", "Stop application (keep data)"]) expect(screen.getByRole("button", { name })).toBeDisabled();
		};
		expectUnverified();
		finishRefresh({ error: new Error("RESULT_SOURCE_CHANGED") });
		await waitFor(() => expect(client.isMutating()).toBe(0), { timeout: 3000 });
		expectUnverified();
	});

	it("shows only the exact plan admission, without a technical approval action", async () => {
		get.mockResolvedValue({ data: { requirement: { id: "req" }, trustedProgress: { phase: "PLANNING" }, complexPlanning: {
			phase: "VALIDATED", plans: [{ id: "current-plan", planSha256: "b".repeat(64) }],
			validations: [{ planId: "current-plan", planSha256: "a".repeat(64) }, { planId: "current-plan", planSha256: "b".repeat(64) }],
		} } });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="PLANNING" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		expect(await screen.findByTestId("cleardev-plan-validation")).toHaveTextContent("This is not product approval or final acceptance");
		expect(screen.getByTestId("cleardev-plan-validation")).toHaveTextContent("current-plan");
		expect(screen.queryByRole("button", { name: /approve|confirm|dispatch/i })).not.toBeInTheDocument();
		expect(post).not.toHaveBeenCalled();
	});

	it("exposes non-executable product questions without reusing the technical or compilation approval route", async () => {
		get.mockResolvedValue({ data: { requirement: { id: "req" }, trustedProgress: { phase: "NEEDS_HUMAN" }, complexPlanning: {
			plans: [{ id: "clarification-plan", planSha256: "b".repeat(64) }],
			validations: [{ planId: "previous-plan", planSha256: "a".repeat(64) }],
			productClarification: { summary: "Retention semantics are missing.", questions: ["Should removed contacts be recoverable?"] },
		} } });
		wrapper(<ClearDevRequirementActions requirementId="req" projectId="p" phase="NEEDS_HUMAN" />);
		await userEvent.click(screen.getByRole("button", { name: "Requirement and result actions" }));
		expect(await screen.findByTestId("cleardev-product-clarification")).toHaveTextContent("Should removed contacts be recoverable?");
		expect(screen.getByTestId("cleardev-product-clarification")).toHaveTextContent("Execution is stopped");
		expect(screen.queryByTestId("cleardev-plan-validation")).not.toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /approve|confirm|Submit clarification answers/i })).not.toBeInTheDocument();
		expect(post).not.toHaveBeenCalled();
	});

	it("rejects non-local or credential-bearing artifact URLs", () => {
		for (const value of ["javascript:alert(1)", "https://example.com", "http://127.0.0.1.evil/", "http://user:secret@localhost/", "file:///tmp/app"]) expect(isLocalResultURL(value)).toBe(false);
		expect(isLocalResultURL("http://127.0.0.1:4567/")).toBe(true);
	});
});

describe("Collapsed legacy entry", () => {
	it("opens the legacy form explicitly and preserves its draft without submitting", async () => {
		wrapper(<ClearDevNewRequirement projectId="project-one" busy={false} collapsible />);
		expect(screen.getByLabelText("Requirement name")).not.toBeVisible();
		const summary = screen.getAllByText("Legacy mail requirement").find((item) => item.tagName === "SUMMARY")!;
		await userEvent.click(summary);
		await userEvent.type(screen.getByLabelText("Requirement name"), "Keep legacy draft");
		await userEvent.click(summary);
		await userEvent.click(summary);
		expect(screen.getByLabelText("Requirement name")).toHaveValue("Keep legacy draft");
		expect(post).not.toHaveBeenCalled();
	});
});
