import "i18next";
import type { CommonMessages } from "@axiom/ui";
import type site from "./locales/zh";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "site";
    resources: { common: CommonMessages; site: typeof site };
  }
}
