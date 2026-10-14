import "i18next";
import type { CommonMessages } from "@axiom/ui";
import type console from "./locales/zh";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "console";
    resources: { common: CommonMessages; console: typeof console };
  }
}
