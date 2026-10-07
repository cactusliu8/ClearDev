import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
const { post, get } = vi.hoisted(() => ({ post: vi.fn(), get: vi.fn() }));
vi.mock("../lib/api-client", () => ({
  apiClient: { POST: post, GET: get },
  hasTrustedApiBaseUrl: () => true,
  apiErrorMessage: (e: unknown) => String(e),
}));
import { ClearDevProductPlan } from "./ClearDevProductPlan";
type Product = components["schemas"]["ClearDevProductView"];
const product = {
  goal: { id: "product" },
  phase: "READY",
  stages: [
    {
      current: true,
      stage: {
        id: "stage",
        definitionSha256: "digest",
        definition: { executionBasis: {} },
      },
    },
  ],
} as Product;
function mount(value = product) {
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { mutations: { retry: false } } })
      }
    >
      <ClearDevProductPlan product={value} onSaved={vi.fn()} />
    </QueryClientProvider>,
  );
}
beforeEach(() => {
  post.mockReset();
  get.mockReset();
  get.mockResolvedValue({ data: { items: [] } });
});
it("only requests one native whole-plan decision, never approves it", async () => {
  post.mockResolvedValue({ data: product });
  mount();
  const b = screen.getByRole("button", {
    name: "Confirm plan and develop stages automatically",
  });
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(b);
  fireEvent.click(b);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0]).toEqual([
    "/api/v1/cleardev/products/{id}/stages/{stageId}/prepare",
    {
      params: { path: { id: "product", stageId: "stage" } },
      body: { definitionSha256: "digest", automatic: true },
    },
  ]);
  expect(
    await screen.findByText(/Waiting for the native desktop confirmation/),
  ).toBeVisible();
  expect(b).toBeDisabled();
});
it("keeps pending and approved states distinct", () => {
  mount({
    ...product,
    automation: {
      requestId: "r",
      binding: {
        productId: "product",
        discussionId: "d",
        resultSha256: "x",
        selectionSha256: "s",
      },
      status: "PENDING",
    },
  });
  expect(
    screen.getByText(/Waiting for the native desktop confirmation/),
  ).toBeVisible();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});
it("shows an approved whole plan without asking for per-stage approval", () => {
  mount({
    ...product,
    automation: {
      requestId: "r",
      binding: {
        productId: "product",
        discussionId: "d",
        resultSha256: "x",
        selectionSha256: "s",
      },
      status: "RESOLVED",
      decision: "APPROVE",
    },
  });
  expect(screen.getByText(/Whole plan confirmed/)).toBeVisible();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});
it("does not claim authorization after an unknown response", async () => {
  post.mockRejectedValue(new Error("lost response"));
  mount();
  fireEvent.click(screen.getByRole("button"));
  expect(await screen.findByRole("alert")).toHaveTextContent("lost response");
  expect(screen.queryByText(/Whole plan confirmed/)).not.toBeInTheDocument();
});

it("allows whole-plan confirmation after an earlier manual stage froze the product", () => {
  mount({ ...product, phase: "FROZEN" });
  expect(
    screen.getByRole("button", {
      name: "Confirm plan and develop stages automatically",
    }),
  ).toBeEnabled();
});
it("shows the actual automatic stop and retries only the original plan", async () => {
  post.mockResolvedValue({ data: product });
  mount({
    ...product,
    automation: {
      requestId: "r",
      binding: {
        productId: "product",
        discussionId: "d",
        resultSha256: "x",
        selectionSha256: "s",
      },
      status: "RESOLVED",
      decision: "APPROVE",
      reasonCode: "RESULT_DATA_IN_USE",
      reason: "Existing preview is running",
    },
  });
  expect(screen.getByRole("alert")).toHaveTextContent(
    "Existing preview is running",
  );
  fireEvent.click(screen.getByRole("button"));
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][1].body).toEqual({
    definitionSha256: "digest",
    automatic: true,
  });
});

it("reopens a dismissed whole-plan window without approving it", async () => {
  const data = {
    connected: true,
    desktopAvailable: true,
    items: [
      {
        decisionRequestId: "r",
        decisionKind: "CONFIRM_PRODUCT_PLAN",
        contentSha256: "a".repeat(64),
        title: "Signed plan",
        pending: true,
        canReopen: true,
        dispatches: [{ id: "old-window", outcome: "LATER" }],
        reopens: [],
      },
    ],
  };
  get.mockResolvedValue({ data });
  post.mockResolvedValue({ data });
  mount({
    ...product,
    automation: {
      requestId: "r",
      binding: {
        productId: "product",
        discussionId: "d",
        resultSha256: "x",
        selectionSha256: "s",
      },
      status: "PENDING",
    },
  });
  const button = await screen.findByRole("button", {
    name: "Reopen pending confirmation",
  });
  expect(screen.getByText("Confirm the whole product plan")).toBeVisible();
  expect(post).not.toHaveBeenCalled();
  fireEvent.click(button);
  await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][0]).toBe(
    "/api/v1/cleardev/requirements/{id}/decision-displays",
  );
  expect(post.mock.calls[0][1].body).toMatchObject({
    decisionRequestId: "r",
    previousDispatchId: "old-window",
  });
  expect(post.mock.calls[0][1].body).not.toHaveProperty("decision");
});
