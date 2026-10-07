import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { ClearDevExecutionFields, ClearDevPreflight, validExecutionChoice, type ExecutionView } from "./ClearDevExecutionChoice";

const { get, post, put } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: get, POST: post, PUT: put }, hasTrustedApiBaseUrl: () => true,
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
}));

type Preflight = components["schemas"]["ClearDevControlledPreflightView"];
type ExecutionChoice = components["schemas"]["ClearDevExecutionConfig"];
function NativeFields() {
	const [choice, setChoice] = useState<ExecutionChoice>({ agent: "codex", model: "" });
	return <ClearDevExecutionFields projectId="native-project" value={choice} onChange={setChoice} />;
}

const execution: ExecutionView = { agent: "opencode", model: "example/model", legacy: false, known: true, toolLocked: true, modelLocked: false };
const preflight: Preflight = {
	id: "failed-observation", roleBindingId: "steward-role", aoProjectId: "project", requestedModel: "example/model", resolvedModel: "", provider: "opencode", catalogSha256: "a".repeat(64), catalogModelIds: [], outcome: "FAILED", reasonCode: "MODEL_NOT_AVAILABLE", retryable: true, checkedAt: "2026-09-28T00:00:00Z",
	evidence: { scope: "LOCAL_CONFIGURATION", installation: "AVAILABLE", configuration: "VALID", model: "UNAVAILABLE", authentication: "UNKNOWN", service: "UNKNOWN", quota: "UNKNOWN" },
};

function mount(receipt: Preflight, choice = execution, current = true) {
	const saved = vi.fn();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	render(<QueryClientProvider client={client}><ClearDevPreflight preflight={receipt} execution={choice} current={current} requirementId="requirement" onSaved={saved} /></QueryClientProvider>);
	return saved;
}

beforeEach(() => {
	get.mockReset(); post.mockReset(); put.mockReset();
	get.mockResolvedValue({ data: { models: [] } });
	post.mockResolvedValue({ data: {} }); put.mockResolvedValue({ data: execution });
});

describe("execution admission presentation", () => {
	it("distinguishes local configuration from unknown remote authentication and quota", () => {
		mount({ ...preflight, outcome: "PASSED", reasonCode: undefined, resolvedModel: "example/model", evidence: { ...preflight.evidence!, model: "LISTED" } });
		expect(screen.getByText("Local configuration check passed")).toBeInTheDocument();
		expect(screen.getByText(/Remote service and quota remain unverified/)).toBeInTheDocument();
		expect(screen.getByText(/Authentication information is not confirmed/)).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Retry preflight after repair" })).not.toBeInTheDocument();
	});

	it("retries the exact failure and does not send commands or approval", async () => {
		mount(preflight, { ...execution, modelLocked: true });
		await userEvent.click(screen.getByRole("button", { name: "Retry preflight after repair" }));
		await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
		expect(post).toHaveBeenCalledWith("/api/v1/cleardev/requirements/{id}/preflight-retries", { params: { path: { id: "requirement" } }, body: { preflightId: "failed-observation" } });
		expect(screen.queryByText("Correct the model before first admission")).not.toBeInTheDocument();
	});

	it("changes only the model before admission without replacing project configuration", async () => {
		mount(preflight);
		await userEvent.click(screen.getByText("Correct the model before first admission"));
		expect(screen.getByRole("combobox", { name: "Execution tool" })).toBeDisabled();
		const input = screen.getByRole("textbox", { name: "Execution model" });
		await userEvent.clear(input);
		await userEvent.type(input, "example/corrected");
		await userEvent.click(screen.getByRole("button", { name: "Save model selection" }));
		await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
		expect(put).toHaveBeenCalledWith("/api/v1/cleardev/projects/{projectId}/execution", { params: { path: { projectId: "project" } }, body: { agent: "opencode", model: "example/corrected", effort: undefined } });
		expect(post).not.toHaveBeenCalled();
	});

	it("keeps a future retry time disabled and hides raw protocol errors", () => {
		mount({ ...preflight, retryAt: "2999-01-01T00:00:00Z", errorSummary: '{"raw":"do not display protocol"}' }, { ...execution, modelLocked: true });
		expect(screen.getByRole("button", { name: "Retry preflight after repair" })).toBeDisabled();
		expect(screen.queryByText(/do not display protocol/)).not.toBeInTheDocument();
	});

	it("does not offer retry or model repair for a cancelled or historical failure", () => {
		mount(preflight, execution, false);
		expect(screen.queryByRole("button", { name: "Retry preflight after repair" })).not.toBeInTheDocument();
		expect(screen.queryByText("Correct the model before first admission")).not.toBeInTheDocument();
		expect(post).not.toHaveBeenCalled();
		expect(put).not.toHaveBeenCalled();
	});

	it("rejects provider defaults, malformed OpenCode identifiers and unsupported effort", () => {
		for (const model of ["", "model-only", "provider/", "/model", "provider/bad model"]) {
			expect(validExecutionChoice({ agent: "opencode", model })).toBe(false);
		}
		expect(validExecutionChoice({ agent: "opencode", model: "provider/model", effort: "bad value" })).toBe(false);
		expect(validExecutionChoice({ agent: "opencode", model: "provider/model" })).toBe(true);
		expect(validExecutionChoice({ agent: "opencode", model: "provider/model", effort: "max" })).toBe(true);
	});
});

 it("offers model-advertised efforts and saves the user's selection", async () => {
  get.mockResolvedValue({data:{models:[{id:"provider/model",label:"Model",efforts:["low","high","max"]}]}});
  const change=vi.fn();const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
  render(<QueryClientProvider client={client}><ClearDevExecutionFields projectId="project" value={{agent:"opencode",model:"provider/model"}} onChange={change}/></QueryClientProvider>);
  await screen.findByRole("option",{name:"max"});
  await userEvent.selectOptions(screen.getByRole("combobox",{name:"Reasoning effort"}),"high");
  expect(change).toHaveBeenLastCalledWith({agent:"opencode",model:"provider/model",effort:"high"});
  await userEvent.clear(screen.getByRole("textbox",{name:"Execution model"}));
  expect(change).toHaveBeenLastCalledWith({agent:"opencode",model:"",effort:undefined});
 });

it("switches native catalogs with the tool and retains provider-qualified models and native variants", async () => {
	get.mockImplementation((_path, options) => Promise.resolve({ data: { models: options.params.path.agent === "codex"
		? [{ id: "native-codex", label: "Native Codex model", efforts: ["medium", "ultra"] }]
		: [{ id: "proxy/vendor/flash", provider: "proxy", label: "Native proxy Flash", efforts: ["budgeted", "max"] }] } }));
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(<QueryClientProvider client={client}><NativeFields /></QueryClientProvider>);
	await userEvent.click(await screen.findByRole("button", { name: "Execution model" }));
	await userEvent.click(screen.getByRole("menuitem", { name: /Native Codex model/ }));
	expect(screen.getByRole("textbox", { name: "Execution model" })).toHaveValue("native-codex");
	await userEvent.selectOptions(screen.getByRole("combobox", { name: "Reasoning effort" }), "ultra");
	expect(screen.getByRole("combobox", { name: "Reasoning effort" })).toHaveValue("ultra");
	await userEvent.click(screen.getByRole("combobox", { name: "Execution tool" }));
	await userEvent.click(screen.getByRole("option", { name: "OpenCode" }));
	await waitFor(() => expect(get).toHaveBeenLastCalledWith("/api/v1/agents/{agent}/models", { params: { path: { agent: "opencode" }, query: { projectId: "native-project" } } }));
	expect(screen.getByRole("textbox", { name: "Execution model" })).toHaveValue("");
	expect(screen.queryByRole("option", { name: "ultra" })).not.toBeInTheDocument();
	await userEvent.click(await screen.findByRole("button", { name: "Execution model" }));
	await userEvent.click(screen.getByRole("menuitem", { name: /Native proxy Flash/ }));
	expect(screen.getByRole("textbox", { name: "Execution model" })).toHaveValue("proxy/vendor/flash");
	await userEvent.selectOptions(screen.getByRole("combobox", { name: "Reasoning effort" }), "budgeted");
	expect(screen.getByRole("combobox", { name: "Reasoning effort" })).toHaveValue("budgeted");
	expect(post).not.toHaveBeenCalled();
	expect(put).not.toHaveBeenCalled();
});

it("refreshes the selected native tool/project and offers a newly configured provider without a whitelist", async () => {
	get.mockResolvedValue({ data: { models: [{ id: "proxy/old", label: "Original model", provider: "proxy" }] } });
	post.mockResolvedValue({ data: { models: [{ id: "native-added/new-flash", label: "New native model", provider: "native-added", efforts: ["deep"] }] } });
	const change = vi.fn();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(<QueryClientProvider client={client}><ClearDevExecutionFields projectId="native-project" value={{ agent: "opencode", model: "" }} onChange={change} /></QueryClientProvider>);
	await screen.findByRole("button", { name: "Execution model" });
	await userEvent.click(screen.getByRole("button", { name: "Refresh list" }));
	await waitFor(() => expect(post).toHaveBeenCalledWith("/api/v1/agents/{agent}/models/refresh", { params: { path: { agent: "opencode" }, query: { projectId: "native-project", revalidate: undefined } } }));
	await userEvent.click(screen.getByRole("button", { name: "Execution model" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: /New native model/ }));
	expect(change).toHaveBeenLastCalledWith({ agent: "opencode", model: "native-added/new-flash", effort: undefined });
	expect(put).not.toHaveBeenCalled();
});

it("does not present a stale cached catalog as a successful native refresh or expose raw diagnostics", async () => {
	get.mockResolvedValue({ data: { models: [{ id: "proxy/old", label: "Cached model" }], stale: true, warning: "SYNTHETIC_PRIVATE_DIAGNOSTIC" } });
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(<QueryClientProvider client={client}><ClearDevExecutionFields projectId="native-project" value={{ agent: "opencode", model: "proxy/old" }} onChange={vi.fn()} /></QueryClientProvider>);
	await screen.findByRole("alert");
	expect(screen.queryByText(/SYNTHETIC_PRIVATE_DIAGNOSTIC/)).not.toBeInTheDocument();
	expect(screen.getByRole("textbox", { name: "Execution model" })).toHaveValue("proxy/old");
});
