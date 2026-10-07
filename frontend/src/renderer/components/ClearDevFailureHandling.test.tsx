import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import { beforeEach, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { createAppI18n } from "../i18n/instance";
import en from "../i18n/en.json";
import zh from "../i18n/zh-CN.json";
import { ClearDevFailureHandling } from "./ClearDevFailureHandling";

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get, POST: post }, hasTrustedApiBaseUrl: () => true, apiErrorMessage: () => "failed" }));
import { ClearDevWorkflowRecovery } from "./ClearDevWorkflowRecovery";

type Handling = components["schemas"]["ClearDevWorkflowFailureHandling"];
const repair: Handling = { id: "failure:source", sourceKind: "CANDIDATE_HANDOFF", sourceId: "original-dispatch", sourceRole: "BUILDER", owner: "BUILDER", action: "REPAIR_ORIGINAL", reason: "RETURN_SOURCE_WITH_EVIDENCE", status: "AWAITING_DISPOSITION", summary: "No implementation change was available to freeze.", path: "app/main.cjs", historical: false };
const renderView = (locale: "en" | "zh-CN", items: Handling[], current = true) => <I18nextProvider i18n={createAppI18n(locale)}><ClearDevFailureHandling items={items} current={current} /></I18nextProvider>;
beforeEach(() => { get.mockReset(); post.mockReset(); });

it.each(["en", "zh-CN"] as const)("shows source, current owner and pre-dispatch gates without claiming completion in %s", (locale) => {
  const words = locale === "en" ? en : zh;
  render(renderView(locale, [repair]));
  expect(screen.getByText(words["cleardevFailure.title"])).toBeVisible();
  expect(screen.getByText(words["cleardevFailure.status.ready"])).toBeVisible();
  expect(screen.getByText("app/main.cjs")).toBeVisible();
  expect(screen.queryByText(words["cleardevFailure.status.verified"])).not.toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it.each(["en", "zh-CN"] as const)("keeps escalation distinct from permission and exposes human stop in %s", (locale) => {
  const words = locale === "en" ? en : zh;
  render(renderView(locale, [{ ...repair, owner: "HUMAN", action: "ASK_HUMAN", reason: "PATH_SCOPE", status: "NEEDS_HUMAN" }]));
  expect(screen.getByText(words["cleardevFailure.boundary"])).toBeVisible();
  expect(screen.getByText(words["cleardevFailure.status.human"])).toBeVisible();
  expect(screen.queryByText(words["cleardevFailure.status.repairing"])).not.toBeInTheDocument();
  expect(post).not.toHaveBeenCalled();
});

it("hides stale action recommendations instead of retaining optimistic ownership", () => {
  const { rerender } = render(renderView("en", [repair]));
  rerender(renderView("en", [repair], false));
  expect(screen.getByRole("status")).toHaveTextContent(en["cleardevFailure.stale"]);
  expect(screen.queryByText(repair.summary!)).not.toBeInTheDocument();
  expect(screen.queryByText("app/main.cjs")).not.toBeInTheDocument();
});

it("shows registered history without counting it as verification", () => {
  render(renderView("en", [{ ...repair, historical: true, status: "RECOVERY_REGISTERED", action: "EXISTING_RECOVERY" }]));
  fireEvent.click(screen.getByText("Preserved handling history (1)"));
  expect(screen.getByText(en["cleardevFailure.status.registered"])).toBeVisible();
  expect(screen.queryByText(en["cleardevFailure.status.verified"])).not.toBeInTheDocument();
});

it("renders escaped provider text and conservative unknown routing without executing it", () => {
  render(renderView("en", [{ ...repair, owner: "FUTURE_ROLE", action: "FUTURE_ACTION", status: "FUTURE_STATUS", summary: "<img src=x onerror=alert(1)>" }]));
  expect(screen.getByText(/Owner not confirmed/)).toBeVisible();
  expect(screen.getByText("<img src=x onerror=alert(1)>")).toBeVisible();
  expect(screen.queryByRole("img")).not.toBeInTheDocument();
});

it("integrates failure-only responses even when no manual recovery option exists", async () => {
  get.mockResolvedValue({ data: { executionRunId: "run", options: [], history: [], failureHandling: [repair] } });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<I18nextProvider i18n={createAppI18n("en")}><QueryClientProvider client={client}><ClearDevWorkflowRecovery id="req" scope="execution" current /></QueryClientProvider></I18nextProvider>);
  expect(await screen.findByText(en["cleardevFailure.title"])).toBeVisible();
  expect(post).not.toHaveBeenCalled();
});

it("keeps English and Chinese failure wording keys and interpolation identical", () => {
  const keys = Object.keys(en).filter((key) => key.startsWith("cleardevFailure."));
  expect(keys.sort()).toEqual(Object.keys(zh).filter((key) => key.startsWith("cleardevFailure.")).sort());
  for (const key of keys) {
    const textEn = en[key as keyof typeof en];
    const textZh = zh[key as keyof typeof zh];
    expect(textEn.match(/\{\{[^}]+\}\}/gu) ?? []).toEqual(textZh.match(/\{\{[^}]+\}\}/gu) ?? []);
  }
});
