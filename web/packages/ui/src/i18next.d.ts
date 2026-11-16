import "i18next";
import type { CommonMessages } from "./i18n";

// Message keys are checked when this package is type-checked on its own; each app declares its own namespaces.
declare module "i18next" {
  interface CustomTypeOptions {
    defaultNS: "common";
    resources: { common: CommonMessages };
  }
}
