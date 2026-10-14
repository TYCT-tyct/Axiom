import "i18next";
import type common from "../../packages/ui/src/locales/zh";
import type admin from "./locales/zh";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "admin";
    resources: { common: typeof common; admin: typeof admin };
  }
}
