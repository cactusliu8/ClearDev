import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
const { post, get } = vi.hoisted(() => ({ post: vi.fn(), get: vi.fn() }));
vi.mock("../lib/api-client", () => ({
  apiClient: { POST: post, GET: get },
  hasTrustedApiBaseUrl: () => true,
  apiErrorMessage: (error: unknown) =>
    error instanceof Error ? error.message : "failed",
}));
import { ClearDevDecisionReopen } from "./ClearDevDecisionReopen";
const item = {
  decisionRequestId: "original",
  decisionKind: "CONFIRM_REQUIREMENT_VERSION",
  contentSha256: "a".repeat(64),
  title: "Original specification",
  pending: true,
  canReopen: true,
  reasonCode: "",
  dispatches: [
    { id: "old-window", expiresAt: "2026-01-01T00:00:00Z", outcome: "EXPIRED" },
  ],
  reopens: [],
};
const facts = (extra = {}) => ({
  connected: true,
  desktopAvailable: true,
  items: [{ ...item, ...extra }],
});
function mount(
  current = true,
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  }),
) {
  return {
    ...render(
      <QueryClientProvider client={client}>
        <ClearDevDecisionReopen requirementId="requirement" current={current} />
      </QueryClientProvider>,
    ),
    client,
  };
}
beforeEach(() => {
  post.mockReset();
  get.mockReset();
  sessionStorage.clear();
  get.mockResolvedValue({ data: facts() });
});
describe("pending confirmation reopen", () => {
  it("names the replacement decision and never treats reopening as approval or continuation", async () => {
    get.mockResolvedValue({ data: facts({ decisionKind: "AUTHORIZE_BUILDER_REPLACEMENT", title: "Signed backend replacement title" }) });
    mount();
    expect(await screen.findByText("Replace worker")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reopen pending confirmation" })).toBeEnabled();
    expect(post).not.toHaveBeenCalled();
  });
  it("names the extra planning decision without treating reopen as approval", async () => {
    get.mockResolvedValue({ data: facts({ decisionKind: "AUTHORIZE_EXTRA_PLANNING_ATTEMPT", title: "Signed backend title" }) });
    mount();
    expect(await screen.findByText("Authorize one extra discussion or compilation attempt")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reopen pending confirmation" })).toBeEnabled();
    expect(post).not.toHaveBeenCalled();
  });
  it("only reads on display and preserves the exact pending identity on double-click", async () => {
    let resolve!: (result: unknown) => void;
    post.mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    mount();
    const button = await screen.findByRole("button", {
      name: "Reopen pending confirmation",
    });
    expect(post).not.toHaveBeenCalled();
    await userEvent.dblClick(button);
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0][0]).toBe(
      "/api/v1/cleardev/requirements/{id}/decision-displays",
    );
    expect(Object.keys(post.mock.calls[0][1].body).sort()).toEqual([
      "contentSha256",
      "decisionRequestId",
      "previousDispatchId",
      "requestId",
    ]);
    expect(post.mock.calls[0][1].body).toMatchObject({
      decisionRequestId: "original",
      contentSha256: item.contentSha256,
      previousDispatchId: "old-window",
    });
    resolve({
      data: facts({ canReopen: false, reasonCode: "REOPEN_REGISTERED" }),
    });
  });
  it("retries the complete original request after unknown response and a full remount", async () => {
    post.mockRejectedValue(new Error("connection lost"));
    const first = mount();
    await userEvent.click(await screen.findByRole("button"));
    await screen.findByText("connection lost");
    const original = post.mock.calls[0][1].body;
    first.unmount();
    mount();
    await userEvent.click(await screen.findByRole("button"));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post.mock.calls[1][1].body).toEqual(original);
  });
  it("shares the pending write across two panels", async () => {
    post.mockReturnValue(new Promise(() => {}));
    const client = new QueryClient();
    mount(true, client);
    mount(true, client);
    const buttons = await screen.findAllByRole("button");
    await userEvent.click(buttons[0]);
    await userEvent.click(buttons[1]);
    expect(post).toHaveBeenCalledTimes(1);
  });
  it.each([
    "DECISION_NOT_CURRENT",
    "DESKTOP_UNAVAILABLE",
    "DECISION_WINDOW_OPEN",
    "REOPEN_REGISTERED",
  ])("blocks %s without sending", async (reasonCode) => {
    get.mockResolvedValue({ data: facts({ canReopen: false, reasonCode }) });
    mount();
    const button = await screen.findByRole("button");
    expect(button).toBeDisabled();
    expect(post).not.toHaveBeenCalled();
  });
  it("blocks stale parent facts and failed reads", async () => {
    mount(false);
    expect(await screen.findByRole("button")).toBeDisabled();
    expect(post).not.toHaveBeenCalled();
  });
});
