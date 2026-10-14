import "i18next";
import type common from "../../packages/ui/src/locales/zh";
import type console from "./locales/zh";

declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "console";
    resources: { common: typeof common; console: typeof console };
  }
}
