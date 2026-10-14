import "i18next";
import type common from "./locales/zh";

// Message keys are checked when this package is type-checked on its own; each app declares its own namespaces.
declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "common";
    resources: { common: typeof common };
  }
}
