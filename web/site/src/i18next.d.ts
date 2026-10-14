import "i18next";
import type common from "../../packages/ui/src/locales/zh";
import type site from "./locales/zh";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "site";
    resources: { common: typeof common; site: typeof site };
  }
}
