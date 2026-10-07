// Isolated fixture. Recovery, handoff facts and decision reopening are
// production components. Provider/approval doubles belong to the test bridge.
import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { appI18n } from "../src/renderer/i18n/instance";
import { setApiBaseUrl } from "../src/renderer/lib/api-client";
import { ClearDevWorkflowRecovery } from "../src/renderer/components/ClearDevWorkflowRecovery";
import { ClearDevDecisionReopen } from "../src/renderer/components/ClearDevDecisionReopen";
const params = new URLSearchParams(location.search);
const id = params.get("id")!;
setApiBaseUrl(location.origin);
void appI18n.changeLanguage(params.get("locale") ?? "en");
createRoot(document.getElementById("root")!).render(
  <I18nextProvider i18n={appI18n}><QueryClientProvider client={new QueryClient()}>
    <h1>Isolated Builder handoff check</h1>
    <p>This fixture uses real Git code transfer and explicit provider, process, native-loss, preflight, check and approval doubles. Test approval is not a human decision.</p>
    {Array.from({ length: params.get("copies") === "2" ? 2 : 1 }, (_, index) => <ClearDevWorkflowRecovery key={index} id={id} scope="execution" current blocked />)}
    <ClearDevDecisionReopen requirementId={id} current />
  </QueryClientProvider></I18nextProvider>,
);
