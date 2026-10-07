// Explicit isolated native test shell; the displayed component is production code.
import React from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nextProvider } from "react-i18next";
import { appI18n } from "../src/renderer/i18n/instance";
import { setApiBaseUrl } from "../src/renderer/lib/api-client";
import { ClearDevDecisionReopen } from "../src/renderer/components/ClearDevDecisionReopen";
const params = new URLSearchParams(location.search);
setApiBaseUrl(location.origin);
void appI18n.changeLanguage(params.get("locale") ?? "en");
createRoot(document.getElementById("root")!).render(
  <I18nextProvider i18n={appI18n}>
    <QueryClientProvider client={new QueryClient()}>
      <h1>ClearDev isolated confirmation test</h1>
      <ClearDevDecisionReopen
        requirementId={params.get("id")!}
        current={true}
      />
    </QueryClientProvider>
  </I18nextProvider>,
);
