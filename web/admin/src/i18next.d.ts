import "i18next";
import type { CommonMessages } from "@axiom/ui";
import type admin from "./locales/zh";
import type dashboard from "./locales/zh/dashboard";
import type goals from "./locales/zh/goals";
import type monitor from "./locales/zh/monitor";
import type ops from "./locales/zh/ops";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "admin";
    // The operations pages keep their words in namespaces of their own, loaded with the page (locales/load-page.ts), so
    // their keys are not part of the one catalog every page's t() is typed against.
    resources: {
      common: CommonMessages;
      admin: typeof admin;
      dashboard: typeof dashboard;
      goals: typeof goals;
      monitor: typeof monitor;
      ops: typeof ops;
    };
  }
}
