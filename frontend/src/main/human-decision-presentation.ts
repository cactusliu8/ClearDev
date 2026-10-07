import { BrowserWindow, dialog } from "electron";
import type { AppLocale } from "../shared/ui-locale";
import type { HumanDecisionOffer } from "./human-authority";
import type { HumanDecisionDialog } from "./human-authority-host";
import { reviewHumanDecision } from "./human-decision-reader";

// The trusted reader owns its native dialog. Cancelling the offer closes both.
export function createHumanDecisionPresentation() {
  const windows = new WeakMap<AbortSignal, BrowserWindow>();
  return {
    reviewOffer: (
      offer: HumanDecisionOffer,
      locale: AppLocale,
      signal?: AbortSignal,
    ) =>
      reviewHumanDecision({
        offer,
        locale,
        signal,
        keepOpenOnContinue: true,
        createWindow: (options) => {
          const window = new BrowserWindow(options);
          if (signal) windows.set(signal, window);
          return window;
        },
      }),
    showDialog: (async (options, signal) => {
      const parent = signal && windows.get(signal);
      if (!signal || signal.aborted || !parent || parent.isDestroyed())
        throw new Error("human decision reader is no longer active");
      return dialog.showMessageBox(parent, { ...options, signal });
    }) satisfies HumanDecisionDialog,
  };
}
