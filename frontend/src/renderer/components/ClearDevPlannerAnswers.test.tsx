import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
const { post } = vi.hoisted(() => ({post: vi.fn()}));
vi.mock("../lib/api-client", () => ({apiClient:{POST:post},hasTrustedApiBaseUrl:()=>true,apiErrorMessage:(error:unknown)=>error instanceof Error?error.message:"failed"}));
import { ClearDevPlannerAnswers } from "./ClearDevPlannerAnswers";
type Requirement = components["schemas"]["ClearDevRequirementView"];
const request = {requestId:"original-request",planId:"question-plan",planSha256:"a".repeat(64),answers:["Newest first"]};
function view(extra = {}): Requirement {return {requirement:{id:"requirement"},trustedProgress:{phase:"NEEDS_HUMAN"},complexPlanning:{plannerClarification:{planId:request.planId,planSha256:request.planSha256,questions:["Newest first?"],canAnswer:true,...extra},plannerAnswerHistory:[]}} as unknown as Requirement;}
function mount(data = view(), current = true) {const client = new QueryClient({defaultOptions:{mutations:{retry:false}}});return {...render(<QueryClientProvider client={client}><ClearDevPlannerAnswers view={data} current={current} onSaved={()=>{}} /></QueryClientProvider>),client};}
beforeEach(()=>{post.mockReset();sessionStorage.clear();});
describe("Planner question answers",()=>{
 it("requires every answer and submits exactly once to the dedicated route",async()=>{
  let resolve!: (value:unknown)=>void;post.mockReturnValue(new Promise((done)=>{resolve=done;}));mount();expect(screen.getByRole("button")).toBeDisabled();
  await userEvent.type(screen.getByLabelText("Newest first?"),"Newest first");await userEvent.dblClick(screen.getByRole("button",{name:"Submit answers and continue planning"}));await waitFor(()=>expect(post).toHaveBeenCalledTimes(1));
  expect(post.mock.calls[0][0]).toBe("/api/v1/cleardev/requirements/{id}/planner-clarifications");expect(post.mock.calls[0][1].body).toMatchObject({planId:request.planId,planSha256:request.planSha256,answers:request.answers});expect(screen.queryByRole("button",{name:/approve|confirm/i})).not.toBeInTheDocument();resolve({data:view()});
 });
 it("replays the durable registered request with immutable answers",async()=>{post.mockResolvedValue({data:view()});mount(view({answer:request}));expect(screen.getByLabelText("Newest first?")).toBeDisabled();await userEvent.click(screen.getByRole("button",{name:"Continue the recorded answers"}));await waitFor(()=>expect(post).toHaveBeenCalledWith(expect.any(String),{params:{path:{id:"requirement"}},body:request}));});
 it("preserves unknown requests across a new query client and full remount",async()=>{post.mockRejectedValue(new Error("connection lost"));const first=mount();await userEvent.type(screen.getByLabelText("Newest first?"),"Newest first");await userEvent.click(screen.getByRole("button"));await screen.findByText("connection lost");const original=post.mock.calls[0][1].body;first.unmount();mount();expect(screen.getByLabelText("Newest first?")).toHaveValue("Newest first");expect(screen.getByLabelText("Newest first?")).toBeDisabled();await userEvent.click(screen.getByRole("button"));await waitFor(()=>expect(post).toHaveBeenCalledTimes(2));expect(post.mock.calls[1][1].body).toEqual(original);});
 it("uses the server's winner after a competing unknown submission",async()=>{sessionStorage.setItem("cleardev-planner-answers:requirement",JSON.stringify([{...request,requestId:"loser",answers:["Old draft"]}]));post.mockResolvedValue({data:view()});mount(view({answer:request}));expect(screen.getByLabelText("Newest first?")).toHaveValue("Newest first");await userEvent.click(screen.getByRole("button"));await waitFor(()=>expect(post.mock.calls[0][1].body).toEqual(request));});
 it.each([false,true])("blocks stale or unavailable targets (current=%s)",async(current)=>{mount(view({canAnswer:!current,answer:request,reasonCode:"PLANNING_SOURCE_CHANGED"}),current);expect(screen.getByRole("button")).toBeDisabled();await userEvent.click(screen.getByRole("button"));expect(post).not.toHaveBeenCalled();});
 it("does not let an unknown old question lock a new target",async()=>{sessionStorage.setItem("cleardev-planner-answers:requirement",JSON.stringify([request]));post.mockResolvedValue({data:view()});mount(view({planId:"next-plan"}));expect(screen.getByLabelText("Newest first?")).not.toBeDisabled();await userEvent.type(screen.getByLabelText("Newest first?"),"New answer");await userEvent.click(screen.getByRole("button"));await waitFor(()=>expect(post.mock.calls[0][1].body).toMatchObject({planId:"next-plan",answers:["New answer"]}));});
 it("allows editing after a definite input rejection, while retaining the user's draft",async()=>{
  post.mockResolvedValue({error:{message:"answer invalid"},response:{status:400}});mount();
  await userEvent.type(screen.getByLabelText("Newest first?"),"Newest first");await userEvent.click(screen.getByRole("button"));
  await waitFor(()=>expect(screen.getByLabelText("Newest first?")).not.toBeDisabled());
  expect(screen.getByLabelText("Newest first?")).toHaveValue("Newest first");
  await userEvent.clear(screen.getByLabelText("Newest first?"));await userEvent.type(screen.getByLabelText("Newest first?"),"Corrected answer");
  post.mockResolvedValue({data:view()});await userEvent.click(screen.getByRole("button"));await waitFor(()=>expect(post).toHaveBeenCalledTimes(2));
  expect(post.mock.calls[1][1].body.answers).toEqual(["Corrected answer"]);expect(post.mock.calls[1][1].body.requestId).not.toBe(post.mock.calls[0][1].body.requestId);
 });

});
