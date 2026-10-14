import "i18next";
import type { CommonMessages } from "@axiom/ui";
import type admin from "./locales/zh";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "admin";
    resources: { common: CommonMessages; admin: typeof admin };
  }
}
